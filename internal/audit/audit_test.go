package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppendAndRecentSanitizeSensitiveFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := Append(path, Event{
		Time:    123,
		Type:    "agents.route",
		Actor:   "cli",
		Outcome: "ok",
		Data: map[string]any{
			"agent":          "codex",
			"account":        "work",
			"token":          "tok-secret",
			"secretRef":      "file://hidden",
			"authJSON":       `{"access_token":"hidden"}`,
			"localPath":      "/Users/stark/.capd/auth.json",
			"rawPayload":     "hidden",
			"nestedPayload":  map[string]any{"token": "hidden"},
			"routeCandidate": "codex-low",
		},
	}); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"tok-secret", "file://hidden", "access_token", "/Users/stark", "hidden"} {
		if strings.Contains(string(raw), leaked) {
			t.Fatalf("audit log leaked %q: %s", leaked, raw)
		}
	}

	events, err := Recent(path, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	ev := events[0]
	if ev.Type != "agents.route" || ev.Actor != "cli" || ev.Outcome != "ok" || ev.Time != 123 {
		t.Fatalf("event = %+v", ev)
	}
	if ev.Data["agent"] != "codex" || ev.Data["account"] != "work" || ev.Data["routeCandidate"] != "codex-low" {
		t.Fatalf("safe data = %+v", ev.Data)
	}
	for _, key := range []string{"token", "secretRef", "authJSON", "localPath", "rawPayload", "nestedPayload"} {
		if _, ok := ev.Data[key]; ok {
			t.Fatalf("unsafe key %q kept in %+v", key, ev.Data)
		}
	}
}

func TestRecentReturnsTailAndSkipsInvalidLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	if err := os.WriteFile(path, []byte("{bad json}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := Append(path, Event{Type: "event", Data: map[string]any{"index": int64(i)}}); err != nil {
			t.Fatal(err)
		}
	}
	events, err := Recent(path, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Data["index"] != float64(3) || events[1].Data["index"] != float64(4) {
		t.Fatalf("tail events = %+v", events)
	}
}

func TestDefaultPathUsesCapdHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".capd", FileName)
	if path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	if err := Append("", Event{Type: "secretstore.check", Data: map[string]any{"backend": "native"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("missing default audit log: %v", err)
	}
}
