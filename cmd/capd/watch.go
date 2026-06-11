package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/coder/websocket"
	"github.com/spf13/cobra"

	"github.com/codingagentprotocol/capd/internal/config"
	"github.com/codingagentprotocol/capd/internal/daemon"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

// newWatchCmd attaches to an existing session and streams its events without
// sending anything — the long-task companion: start work with `capd run`,
// disconnect freely, watch progress from anywhere later.
func newWatchCmd() *cobra.Command {
	var fromSeq uint64
	var tail bool
	var showJSON bool

	cmd := &cobra.Command{
		Use:   "watch <session-id>",
		Short: "Attach to a session and stream its events (replay + live)",
		Long: `Attach to a session and stream its events. By default the full history
is replayed before following live output; --tail skips the replay.

  capd run "refactor the whole package"   # long task, Ctrl-C any time
  capd sessions                           # find the session id
  capd watch s_ab12cd34                   # replay + follow until you Ctrl-C`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			out := cmd.OutOrStdout()
			sessionID := args[0]

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
			wsURL := daemonWSURL(cfg, token)
			conn, _, err := websocket.Dial(ctx, wsURL, nil)
			if err != nil {
				return daemonConnectError(cfg, token, err)
			}
			defer conn.CloseNow()
			conn.SetReadLimit(32 * 1024 * 1024)

			nextID := 0
			call := func(method string, params any) (json.RawMessage, error) {
				nextID++
				p, _ := json.Marshal(params)
				req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": nextID, "method": method, "params": json.RawMessage(p)})
				if err := conn.Write(ctx, websocket.MessageText, req); err != nil {
					return nil, err
				}
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
				Client:          protocol.ClientInfo{Name: "capd-watch", Version: daemon.Version},
			}); err != nil {
				return err
			}

			from := fromSeq
			if tail {
				from = ^uint64(0) // live only
			}
			if _, err := call(protocol.MethodSessionAttach, protocol.SessionAttachParams{
				SessionID: sessionID, FromSeq: from,
			}); err != nil {
				return err
			}

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
				printEvent(out, msg.Params, showJSON)
				var ev protocol.Event
				json.Unmarshal(msg.Params, &ev)
				if ev.Type == protocol.EventSessionEnded {
					return nil
				}
			}
		},
	}
	cmd.Flags().Uint64Var(&fromSeq, "from", 0, "replay events from this sequence number")
	cmd.Flags().BoolVar(&tail, "tail", false, "skip the replay, follow live output only")
	cmd.Flags().BoolVar(&showJSON, "json", false, "print raw event JSON")
	return cmd
}
