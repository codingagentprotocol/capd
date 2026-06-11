package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/coder/websocket"
	"github.com/spf13/cobra"

	"github.com/codingagentprotocol/capd/internal/config"
	"github.com/codingagentprotocol/capd/internal/daemon"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

// newRunCmd sends one task to an agent through a running capd daemon and
// streams the events to the terminal — the protocol round trip without
// writing a client.
func newRunCmd() *cobra.Command {
	var (
		agentID    string
		cwd        string
		permission string
		sessionID  string
		model      string
		effort     string
		images     []string
		showJSON   bool
	)

	cmd := &cobra.Command{
		Use:   "run <prompt>",
		Short: "Send a task to an agent via the daemon and stream the result",
		Long: `Send a task to an agent through the running capd daemon (start it with
'capd start'). Streams output, tool activity, and approvals to the terminal.

  capd run "explain this repo"
  capd run --agent codex --cwd ~/project "fix the failing test"
  capd run --session s_ab12cd34 "now add a unit test"   # continue a session`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTask(cmd, runOpts{agent: agentID, cwd: cwd, permission: permission,
				session: sessionID, model: model, effort: effort, images: images, json: showJSON, prompt: args[0]})
		},
	}
	cmd.Flags().StringVar(&agentID, "agent", "codex", "agent to drive")
	cmd.Flags().StringVar(&cwd, "cwd", "", "working directory for the agent (default: current directory)")
	cmd.Flags().StringVar(&permission, "permission", "", "permission mode: default | acceptEdits | full")
	cmd.Flags().StringVar(&sessionID, "session", "", "continue an existing capd session instead of creating one")
	cmd.Flags().StringVar(&model, "model", "", "agent-native model id (empty = agent default)")
	cmd.Flags().StringVar(&effort, "effort", "", "reasoning effort where supported (codex: minimal..xhigh)")
	cmd.Flags().StringSliceVar(&images, "image", nil, "image file(s) to attach (agents that support it)")
	cmd.Flags().BoolVar(&showJSON, "json", false, "print raw event JSON instead of formatted output")
	return cmd
}

type runOpts struct {
	agent, cwd, permission, session, model, effort, prompt string
	images                                                 []string
	json                                                   bool
}

func runTask(cmd *cobra.Command, o runOpts) error {
	agentID, cwd, permission, sessionID, prompt, showJSON := o.agent, o.cwd, o.permission, o.session, o.prompt, o.json
	ctx := cmd.Context()
	out := cmd.OutOrStdout()

	cfg := config.Load()
	home, err := daemon.Home()
	if err != nil {
		return err
	}
	tokenBytes, err := os.ReadFile(filepath.Join(home, "token"))
	if err != nil {
		return fmt.Errorf("no daemon token (is capd started?): %w", err)
	}
	token := strings.TrimSpace(string(tokenBytes))

	addr := net.JoinHostPort(cfg.Host, fmt.Sprint(cfg.Port))
	conn, _, err := websocket.Dial(ctx, "ws://"+addr+"/ws?token="+token, nil)
	if err != nil {
		return fmt.Errorf("connect to capd at %s (is 'capd start' running?): %w", addr, err)
	}
	defer conn.CloseNow()
	// Agent turns stream for as long as they stream.
	conn.SetReadLimit(32 * 1024 * 1024)

	nextID := 0
	call := func(method string, params any) (json.RawMessage, error) {
		nextID++
		p, _ := json.Marshal(params)
		req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": nextID, "method": method, "params": json.RawMessage(p)})
		if err := conn.Write(ctx, websocket.MessageText, req); err != nil {
			return nil, err
		}
		// Responses and notifications interleave; collect events while
		// waiting for this call's response.
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return nil, err
			}
			var msg struct {
				ID     *int            `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
				Result json.RawMessage `json:"result"`
				Error  *protocol.Error `json:"error"`
			}
			if json.Unmarshal(data, &msg) != nil {
				continue
			}
			if msg.Method == protocol.MethodEvent {
				printEvent(out, msg.Params, showJSON)
				continue
			}
			if msg.ID != nil && *msg.ID == nextID {
				if msg.Error != nil {
					return nil, msg.Error
				}
				return msg.Result, nil
			}
		}
	}

	if _, err := call(protocol.MethodInitialize, protocol.InitializeParams{
		ProtocolVersion: protocol.Version,
		Client:          protocol.ClientInfo{Name: "capd-run", Version: daemon.Version},
	}); err != nil {
		return err
	}

	if sessionID == "" {
		if cwd == "" {
			cwd, _ = os.Getwd()
		}
		res, err := call(protocol.MethodSessionCreate, protocol.SessionCreateParams{
			AgentID: agentID, Cwd: cwd, PermissionMode: permission, Model: o.model, Effort: o.effort,
		})
		if err != nil {
			return err
		}
		var created protocol.SessionCreateResult
		json.Unmarshal(res, &created)
		sessionID = created.SessionID
		fmt.Fprintf(out, "session %s (%s)\n", sessionID, agentID)
	} else {
		if _, err := call(protocol.MethodSessionAttach, protocol.SessionAttachParams{
			SessionID: sessionID, FromSeq: ^uint64(0), // live tail only, no replay
		}); err != nil {
			return err
		}
	}

	var atts []protocol.Attachment
	for _, img := range o.images {
		abs, err := filepath.Abs(img)
		if err != nil {
			return err
		}
		atts = append(atts, protocol.Attachment{Type: "image", Path: abs})
	}
	if _, err := call(protocol.MethodTaskSend, protocol.TaskSendParams{SessionID: sessionID, Prompt: prompt, Attachments: atts}); err != nil {
		return err
	}

	// Stream events until the turn ends. Approvals are answered
	// interactively from stdin.
	stdin := bufio.NewReader(os.Stdin)
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var msg struct {
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if json.Unmarshal(data, &msg) != nil || msg.Method != protocol.MethodEvent {
			continue
		}
		var ev protocol.Event
		json.Unmarshal(msg.Params, &ev)
		if ev.Type == protocol.EventApprovalNeeded && !showJSON {
			aid, _ := ev.Data["approvalId"].(string)
			detail, _ := ev.Data["command"].(string)
			if detail == "" {
				detail, _ = ev.Data["kind"].(string)
			}
			fmt.Fprintf(out, "⚠ codex 请求批准: %s\n  允许? [y]es / [a]lways / [N]o: ", detail)
			line, _ := stdin.ReadString('\n')
			decision := protocol.DecisionDeny
			switch strings.ToLower(strings.TrimSpace(line)) {
			case "y", "yes":
				decision = protocol.DecisionApprove
			case "a", "always":
				decision = protocol.DecisionApproveAlways
			}
			nextID++
			p, _ := json.Marshal(protocol.ApprovalReplyParams{SessionID: sessionID, ApprovalID: aid, Decision: decision})
			req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": nextID, "method": protocol.MethodApprovalReply, "params": json.RawMessage(p)})
			if err := conn.Write(ctx, websocket.MessageText, req); err != nil {
				return err
			}
			fmt.Fprintf(out, "  → %s\n", decision)
			continue
		}
		if done := printEvent(out, msg.Params, showJSON); done {
			fmt.Fprintf(out, "\n(continue with: capd run --session %s \"...\")\n", sessionID)
			return nil
		}
	}
}

// printEvent renders one event; returns true when the turn is over.
func printEvent(out interface{ Write([]byte) (int, error) }, params json.RawMessage, showJSON bool) bool {
	var ev protocol.Event
	if json.Unmarshal(params, &ev) != nil {
		return false
	}
	if showJSON {
		fmt.Fprintf(out, "%s\n", params)
		return ev.Type == protocol.EventTaskDone
	}

	str := func(k string) string { v, _ := ev.Data[k].(string); return v }
	switch ev.Type {
	case protocol.EventOutputText:
		if d, _ := ev.Data["delta"].(bool); d {
			fmt.Fprint(out, str("text")) // typewriter
		} else if f, _ := ev.Data["final"].(bool); f {
			fmt.Fprintln(out) // deltas already printed the content
		} else {
			fmt.Fprintln(out, str("text"))
		}
	case protocol.EventOutputReasoning:
		// keep the terminal clean; reasoning is for UIs
	case protocol.EventToolUse:
		if c := str("command"); c != "" {
			fmt.Fprintf(out, "⏵ %s\n", c)
		} else {
			fmt.Fprintf(out, "⏵ [%s]\n", str("kind"))
		}
	case protocol.EventToolResult:
		if d, _ := ev.Data["delta"].(bool); !d {
			if o := str("output"); o != "" {
				fmt.Fprintf(out, "  %s\n", strings.TrimRight(o, "\n"))
			}
		}
	case protocol.EventApprovalNeeded:
		fmt.Fprintf(out, "⚠ approval needed (%s): %s\n  reply via a client with approvalId=%s\n",
			str("kind"), str("command"), str("approvalId"))
	case protocol.EventError:
		fmt.Fprintf(out, "✗ %s\n", str("message"))
	case protocol.EventTaskDone:
		ok, _ := ev.Data["ok"].(bool)
		if ok {
			fmt.Fprintln(out, "✓ done")
		} else {
			fmt.Fprintln(out, "✗ turn failed or canceled")
		}
		return true
	}
	return false
}
