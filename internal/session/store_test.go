package session

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/codingagentprotocol/capd/internal/adapter"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

func TestStoreRoundTrip(t *testing.T) {
	st, err := OpenStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	rec := SessionRecord{ID: "s_1", AgentID: "codex", Cwd: "/tmp"}
	if err := st.SaveSession(rec); err != nil {
		t.Fatal(err)
	}
	if err := st.SetNativeID("s_1", "thread-42"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		st.AppendEvent(protocol.Event{
			SessionID: "s_1", Seq: uint64(i), Type: protocol.EventOutputText,
			Data: map[string]any{"text": "x"},
		})
	}

	got, err := st.LoadSession("s_1")
	if err != nil {
		t.Fatal(err)
	}
	if got.AgentID != "codex" || got.NativeID != "thread-42" || got.Cwd != "/tmp" || got.Ended {
		t.Fatalf("got %+v", got)
	}

	events, err := st.LoadEvents("s_1", 1, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Seq != 1 || events[1].Seq != 2 {
		t.Fatalf("events = %+v", events)
	}

	if _, err := st.LoadSession("nope"); err != ErrSessionUnknown {
		t.Fatalf("want ErrSessionUnknown, got %v", err)
	}
}

// fakeAdapter records the SessionOpts it was started with and emits a
// scripted event stream.
type fakeAdapter struct {
	id       string
	lastOpts adapter.SessionOpts
}

func (f *fakeAdapter) ID() string { return f.id }
func (f *fakeAdapter) Probe(context.Context) (protocol.AgentInfo, error) {
	return protocol.AgentInfo{ID: f.id, Available: true}, nil
}
func (f *fakeAdapter) StartSession(_ context.Context, opts adapter.SessionOpts) (adapter.Session, error) {
	f.lastOpts = opts
	return &fakeSession{events: make(chan protocol.Event, 8)}, nil
}

type fakeSession struct{ events chan protocol.Event }

func (s *fakeSession) Send(_ context.Context, _ adapter.Message) error {
	s.events <- protocol.Event{Type: protocol.EventSessionStarted, Data: map[string]any{"nativeSessionId": "native-7"}}
	s.events <- protocol.Event{Type: protocol.EventTaskDone, Data: map[string]any{"ok": true}}
	return nil
}
func (s *fakeSession) Cancel()                        {}
func (s *fakeSession) Events() <-chan protocol.Event  { return s.events }
func (s *fakeSession) Close() error                   { close(s.events); return nil }

// TestReviveAfterRestart simulates a daemon restart: a second Manager over
// the same store must revive the session with the stored native id and
// replay its history.
func TestReviveAfterRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	st, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}

	fake := &fakeAdapter{id: "fake"}
	m1 := NewManager(adapter.NewRegistry(fake), st)
	sess, err := m1.Create(context.Background(), "fake", adapter.SessionOpts{Cwd: "/work"})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Send(context.Background(), adapter.Message{Prompt: "hello"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		evs, _ := st.LoadEvents(sess.ID, 0, 10)
		return len(evs) >= 2
	})

	// "Restart": fresh manager, same store.
	m2 := NewManager(adapter.NewRegistry(fake), st)
	revived, err := m2.Resolve(context.Background(), sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if fake.lastOpts.Resume != "native-7" {
		t.Fatalf("revive Resume = %q, want native-7", fake.lastOpts.Resume)
	}
	if fake.lastOpts.Cwd != "/work" {
		t.Fatalf("revive Cwd = %q", fake.lastOpts.Cwd)
	}

	ch, nextSeq, cancel := revived.Subscribe(0)
	defer cancel()
	if nextSeq != 2 {
		t.Fatalf("nextSeq = %d, want 2", nextSeq)
	}
	first := <-ch
	if first.Seq != 0 || first.Type != protocol.EventSessionStarted {
		t.Fatalf("replayed first = %+v", first)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}
