package session

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sync"
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

	rec := SessionRecord{ID: "s_1", AgentID: "codex", Cwd: "/tmp", Env: []string{"CODEX_HOME=/tmp/capd-codex"}}
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
	if len(got.Env) != 1 || got.Env[0] != "CODEX_HOME=/tmp/capd-codex" {
		t.Fatalf("env = %#v", got.Env)
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

func TestStoreTightensFilePermissions(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "capd.db")
	if err := os.WriteFile(dbPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dbPath, 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := OpenStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	assertStoreFileMode(t, dbPath)
	if err := st.SaveSession(SessionRecord{ID: "s_1", AgentID: "codex", Env: []string{"CODEX_HOME=/tmp/capd-codex"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendEvent(protocol.Event{SessionID: "s_1", Seq: 1, Type: protocol.EventOutputText, Data: map[string]any{"text": "hello"}}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		assertStoreFileMode(t, path)
	}
}

func TestOpenStoreMigratesEnvColumn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
CREATE TABLE sessions (
	id TEXT PRIMARY KEY,
	agent_id TEXT NOT NULL,
	native_id TEXT NOT NULL DEFAULT '',
	cwd TEXT NOT NULL DEFAULT '',
	ended INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE TABLE events (
	session_id TEXT NOT NULL,
	seq INTEGER NOT NULL,
	type TEXT NOT NULL,
	data TEXT NOT NULL DEFAULT '{}',
	PRIMARY KEY (session_id, seq)
);`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.SaveSession(SessionRecord{ID: "s_1", AgentID: "codex", Env: []string{"CODEX_HOME=/tmp/codex"}}); err != nil {
		t.Fatal(err)
	}
	rec, err := st.LoadSession("s_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Env) != 1 || rec.Env[0] != "CODEX_HOME=/tmp/codex" {
		t.Fatalf("env = %#v", rec.Env)
	}
}

func assertStoreFileMode(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("%s mode = %o", path, info.Mode().Perm())
	}
}

// fakeAdapter records the SessionOpts it was started with and emits a
// scripted event stream.
type fakeAdapter struct {
	mu          sync.Mutex
	id          string
	lastOpts    adapter.SessionOpts
	lastSession *fakeSession
	startCount  int
	startDelay  time.Duration
}

func (f *fakeAdapter) ID() string { return f.id }
func (f *fakeAdapter) Probe(context.Context) (protocol.AgentInfo, error) {
	return protocol.AgentInfo{ID: f.id, Available: true}, nil
}
func (f *fakeAdapter) StartSession(_ context.Context, opts adapter.SessionOpts) (adapter.Session, error) {
	if f.startDelay > 0 {
		time.Sleep(f.startDelay)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastOpts = opts
	f.startCount++
	f.lastSession = &fakeSession{events: make(chan protocol.Event, 8)}
	return f.lastSession, nil
}

func (f *fakeAdapter) snapshot() (adapter.SessionOpts, *fakeSession, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastOpts, f.lastSession, f.startCount
}

type fakeSession struct {
	events chan protocol.Event
	once   sync.Once
}

func (s *fakeSession) Send(_ context.Context, _ adapter.Message) error {
	s.events <- protocol.Event{Type: protocol.EventSessionStarted, Data: map[string]any{"nativeSessionId": "native-7"}}
	s.events <- protocol.Event{Type: protocol.EventTaskDone, Data: map[string]any{"ok": true}}
	return nil
}
func (s *fakeSession) Cancel()                       {}
func (s *fakeSession) Events() <-chan protocol.Event { return s.events }
func (s *fakeSession) Close() error                  { s.crash(); return nil }
func (s *fakeSession) crash()                        { s.once.Do(func() { close(s.events) }) }

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
	sess, err := m1.Create(context.Background(), "fake", adapter.SessionOpts{Cwd: "/work", Env: []string{"CODEX_HOME=/tmp/capd-codex"}})
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
	lastOpts, _, _ := fake.snapshot()
	if lastOpts.Resume != "native-7" {
		t.Fatalf("revive Resume = %q, want native-7", lastOpts.Resume)
	}
	if lastOpts.Cwd != "/work" {
		t.Fatalf("revive Cwd = %q", lastOpts.Cwd)
	}
	if len(lastOpts.Env) != 1 || lastOpts.Env[0] != "CODEX_HOME=/tmp/capd-codex" {
		t.Fatalf("revive Env = %#v", lastOpts.Env)
	}

	ch, nextSeq, cancel, _ := revived.Subscribe(0)
	defer cancel()
	if nextSeq != 2 {
		t.Fatalf("nextSeq = %d, want 2", nextSeq)
	}
	first := <-ch
	if first.Seq != 0 || first.Type != protocol.EventSessionStarted {
		t.Fatalf("replayed first = %+v", first)
	}
}

func TestReviveAfterLongHistoryKeepsNextSeq(t *testing.T) {
	st, err := OpenStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := st.SaveSession(SessionRecord{ID: "s_long", AgentID: "fake", Cwd: "/work", NativeID: "native-long"}); err != nil {
		t.Fatal(err)
	}
	eventCount := maxBuffer + 7
	for i := 0; i < eventCount; i++ {
		if err := st.AppendEvent(protocol.Event{
			SessionID: "s_long", Seq: uint64(i), Type: protocol.EventOutputText,
			Data: map[string]any{"text": "x"},
		}); err != nil {
			t.Fatal(err)
		}
	}

	fake := &fakeAdapter{id: "fake"}
	m := NewManager(adapter.NewRegistry(fake), st)
	revived, err := m.Resolve(context.Background(), "s_long")
	if err != nil {
		t.Fatal(err)
	}
	lastOpts, _, _ := fake.snapshot()
	if lastOpts.Resume != "native-long" {
		t.Fatalf("revive Resume = %q, want native-long", lastOpts.Resume)
	}

	ch, nextSeq, cancel, _ := revived.Subscribe(0)
	defer cancel()
	if nextSeq != uint64(eventCount) {
		t.Fatalf("nextSeq = %d, want %d", nextSeq, eventCount)
	}
	for i := 0; i < eventCount; i++ {
		ev := <-ch
		if ev.Seq != uint64(i) {
			t.Fatalf("replay event %d seq = %d", i, ev.Seq)
		}
	}
}

func TestReviveAfterAdapterStreamEndsInSameManager(t *testing.T) {
	st, err := OpenStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	fake := &fakeAdapter{id: "fake"}
	m := NewManager(adapter.NewRegistry(fake), st)
	sess, err := m.Create(context.Background(), "fake", adapter.SessionOpts{Cwd: "/work"})
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Send(context.Background(), adapter.Message{Prompt: "hello"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool {
		rec, err := st.LoadSession(sess.ID)
		return err == nil && rec.NativeID == "native-7"
	})

	_, lastSession, _ := fake.snapshot()
	lastSession.crash()
	waitFor(t, func() bool {
		list := m.List(10)
		return len(list) == 1 && list[0].SessionID == sess.ID && list[0].State == protocol.SessionStateStored
	})

	if _, err := m.Resolve(context.Background(), sess.ID); err != nil {
		t.Fatal(err)
	}
	lastOpts, _, startCount := fake.snapshot()
	if startCount != 2 {
		t.Fatalf("startCount = %d, want 2", startCount)
	}
	if lastOpts.Resume != "native-7" {
		t.Fatalf("revive Resume = %q, want native-7", lastOpts.Resume)
	}
}

func TestConcurrentResolveRevivesOnce(t *testing.T) {
	st, err := OpenStore(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := st.SaveSession(SessionRecord{ID: "s_concurrent", AgentID: "fake", Cwd: "/work", NativeID: "native-c"}); err != nil {
		t.Fatal(err)
	}
	fake := &fakeAdapter{id: "fake", startDelay: 50 * time.Millisecond}
	m := NewManager(adapter.NewRegistry(fake), st)

	const callers = 8
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := m.Resolve(context.Background(), "s_concurrent")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	lastOpts, _, startCount := fake.snapshot()
	if startCount != 1 {
		t.Fatalf("startCount = %d, want 1", startCount)
	}
	if lastOpts.Resume != "native-c" {
		t.Fatalf("revive Resume = %q, want native-c", lastOpts.Resume)
	}
}

func TestSubscriberOverflowSignals(t *testing.T) {
	fake := &fakeAdapter{id: "fake"}
	m := NewManager(adapter.NewRegistry(fake), nil)
	sess, err := m.Create(context.Background(), "fake", adapter.SessionOpts{})
	if err != nil {
		t.Fatal(err)
	}
	_, _, cancel, overflow := sess.Subscribe(0)
	defer cancel()
	_, lastSession, _ := fake.snapshot()

	for i := 0; i < subBuffer+1; i++ {
		lastSession.events <- protocol.Event{
			Type: protocol.EventOutputText,
			Data: map[string]any{"text": "x"},
		}
	}

	select {
	case <-overflow:
	case <-time.After(2 * time.Second):
		t.Fatal("overflow was not signaled")
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
