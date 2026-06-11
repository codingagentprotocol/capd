package adapter

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/codingagentprotocol/capd/internal/proc"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

// ErrTurnInProgress is returned by Send while a previous turn is running.
var ErrTurnInProgress = errors.New("adapter: a turn is already in progress")

// EmitFunc publishes one unified event from a translator.
type EmitFunc func(t protocol.EventType, data map[string]any)

// TurnConfig describes how to drive a turn-based agent CLI: each Send spawns
// one process (codex exec, claude -p, ...), the process streams events on
// stdout and exits when the turn ends. Conversation continuity across turns
// uses the agent's native session/thread id via a resume flag.
type TurnConfig struct {
	// BuildSpec returns the command for one turn. nativeID is empty on the
	// first turn, afterwards it carries the id captured by Translate.
	BuildSpec func(opts SessionOpts, nativeID string, msg Message) proc.Spec
	// SupportsImages gates image attachments; Send rejects them otherwise.
	SupportsImages bool
	// Translate parses one stdout line and emits zero or more events.
	// Non-JSON lines (CLIs leak logs into stdout) must be tolerated.
	// It returns the agent-native session id when the line carries one,
	// or "" to keep the current id.
	Translate func(line string, emit EmitFunc) (nativeID string)
}

// TurnSession implements Session on top of TurnConfig.
type TurnSession struct {
	cfg  TurnConfig
	opts SessionOpts

	events chan protocol.Event
	wg     sync.WaitGroup

	mu         sync.Mutex
	nativeID   string
	cancelTurn context.CancelFunc
	busy       bool
	closed     bool
}

func NewTurnSession(cfg TurnConfig, opts SessionOpts) *TurnSession {
	return &TurnSession{
		cfg:    cfg,
		opts:   opts,
		events: make(chan protocol.Event, 256),
	}
}

// NewTurnSessionResumed starts a session that continues an existing
// agent-native conversation (the first turn already uses the resume path).
func NewTurnSessionResumed(cfg TurnConfig, opts SessionOpts, nativeID string) *TurnSession {
	s := NewTurnSession(cfg, opts)
	s.nativeID = nativeID
	return s
}

func (s *TurnSession) Events() <-chan protocol.Event { return s.events }

func (s *TurnSession) Send(_ context.Context, msg Message) error {
	if len(msg.Images) > 0 && !s.cfg.SupportsImages {
		return errors.New("adapter: this agent does not accept image attachments")
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errors.New("adapter: session closed")
	}
	if s.busy {
		s.mu.Unlock()
		return ErrTurnInProgress
	}
	// The turn belongs to the session, not to the request: a client
	// disconnect must not kill it, so it gets its own context.
	ctx, cancel := context.WithCancel(context.Background())
	s.busy = true
	s.cancelTurn = cancel
	nativeID := s.nativeID
	s.mu.Unlock()

	s.wg.Add(1)
	go s.runTurn(ctx, nativeID, msg)
	return nil
}

func (s *TurnSession) runTurn(ctx context.Context, nativeID string, msg Message) {
	defer s.wg.Done()

	// task.done is withheld until the turn slot is released: a client that
	// reacts to task.done must be able to Send the next turn immediately.
	var done *protocol.Event
	emit := func(t protocol.EventType, data map[string]any) {
		if t == protocol.EventTaskDone {
			done = &protocol.Event{Type: t, Data: data}
			return
		}
		select {
		case s.events <- protocol.Event{Type: t, Data: data}:
		case <-ctx.Done():
		}
	}

	spec := s.cfg.BuildSpec(s.opts, nativeID, msg)
	if p, err := proc.Start(ctx, spec); err != nil {
		if ctx.Err() == nil { // a canceled turn is not an error
			emit(protocol.EventError, map[string]any{"message": err.Error()})
		}
	} else {
		// Turn CLIs take the prompt as an argument; an open stdin pipe
		// makes some of them (codex) wait for more input.
		p.CloseStdin()
		for line := range p.Lines {
			if id := s.cfg.Translate(line, emit); id != "" {
				s.mu.Lock()
				s.nativeID = id
				s.mu.Unlock()
			}
		}
		waitErr := p.Wait()
		if done == nil {
			data := map[string]any{"ok": waitErr == nil}
			if ctx.Err() != nil {
				data["canceled"] = true
			} else if waitErr != nil {
				data["message"] = fmt.Sprintf("agent exited: %v", waitErr)
			}
			done = &protocol.Event{Type: protocol.EventTaskDone, Data: data}
		}
	}
	if done == nil {
		data := map[string]any{"ok": false}
		if ctx.Err() != nil {
			data["canceled"] = true // canceled before the process even spawned
		}
		done = &protocol.Event{Type: protocol.EventTaskDone, Data: data}
	}

	s.mu.Lock()
	s.busy = false
	s.cancelTurn = nil
	s.mu.Unlock()

	// Plain send is safe: the manager pump drains events until Close, and
	// Close waits for this goroutine before closing the channel.
	s.events <- *done
}

func (s *TurnSession) Cancel() {
	s.mu.Lock()
	cancel := s.cancelTurn
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (s *TurnSession) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	cancel := s.cancelTurn
	s.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	s.wg.Wait()
	close(s.events)
	return nil
}
