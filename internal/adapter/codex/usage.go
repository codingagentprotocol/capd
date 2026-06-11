package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/internal/proc"
)

// Usage queries account rate limits (plan, used percent, window reset times)
// through `codex app-server`: a short-lived stdio JSON-RPC conversation —
// initialize, then account/rateLimits/read.
func (a *Adapter) Usage(ctx context.Context) (map[string]any, error) {
	return a.UsageFor(ctx, adapter.SessionOpts{})
}

func (a *Adapter) UsageFor(ctx context.Context, opts adapter.SessionOpts) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	p, err := proc.Start(ctx, proc.Spec{Bin: binPath(), Args: []string{"app-server"}, Env: opts.Env})
	if err != nil {
		return nil, err
	}
	defer func() {
		cancel()    // kills the app-server
		go p.Wait() // reap it
	}()

	if err := p.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"clientInfo":{"name":"capd","title":"capd","version":"0.1"}}}` + "\n")); err != nil {
		return nil, err
	}

	for line := range p.Lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var msg struct {
			ID     int             `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		switch msg.ID {
		case 1: // initialized — now ask for the limits
			if err := p.Write([]byte(`{"jsonrpc":"2.0","id":2,"method":"account/rateLimits/read","params":{}}` + "\n")); err != nil {
				return nil, err
			}
		case 2:
			if msg.Error != nil {
				return nil, fmt.Errorf("codex: rateLimits/read: %s", msg.Error.Message)
			}
			var usage map[string]any
			if err := json.Unmarshal(msg.Result, &usage); err != nil {
				return nil, err
			}
			return usage, nil
		}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	return nil, errors.New("codex: app-server exited before answering")
}
