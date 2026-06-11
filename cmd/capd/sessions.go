package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/coder/websocket"
	"github.com/spf13/cobra"

	"github.com/codingagentprotocol/capd/internal/config"
	"github.com/codingagentprotocol/capd/internal/daemon"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

func newSessionsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sessions",
		Short: "List sessions known to the daemon (live, stored, ended)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
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

			call := func(id int, method string, params any) (json.RawMessage, error) {
				p, _ := json.Marshal(params)
				req, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": json.RawMessage(p)})
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
						Result json.RawMessage `json:"result"`
						Error  *protocol.Error `json:"error"`
					}
					if json.Unmarshal(data, &msg) != nil || msg.ID == nil || *msg.ID != id {
						continue
					}
					if msg.Error != nil {
						return nil, msg.Error
					}
					return msg.Result, nil
				}
			}

			if _, err := call(1, protocol.MethodInitialize, protocol.InitializeParams{
				ProtocolVersion: protocol.Version,
				Client:          protocol.ClientInfo{Name: "capd-sessions", Version: daemon.Version},
			}); err != nil {
				return err
			}
			raw, err := call(2, protocol.MethodSessionList, struct{}{})
			if err != nil {
				return err
			}
			var result protocol.SessionListResult
			json.Unmarshal(raw, &result)

			w := tabwriter.NewWriter(cmd.OutOrStdout(), 2, 4, 2, ' ', 0)
			fmt.Fprintln(w, "SESSION\tAGENT\tSTATE\tCREATED\tCWD")
			for _, s := range result.Sessions {
				created := ""
				if s.CreatedAt > 0 {
					created = time.Unix(s.CreatedAt, 0).Format("01-02 15:04")
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", s.SessionID, s.AgentID, s.State, created, s.Cwd)
			}
			return w.Flush()
		},
	}
	return cmd
}
