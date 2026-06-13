// Package audit writes local append-only security-relevant metadata without
// storing secrets, local paths, or raw payloads.
package audit

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const FileName = "audit.jsonl"

type Event struct {
	Time    int64          `json:"time"`
	Type    string         `json:"type"`
	Actor   string         `json:"actor,omitempty"`
	Outcome string         `json:"outcome,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
}

func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".capd")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", err
	}
	return filepath.Join(dir, FileName), nil
}

func Append(path string, ev Event) error {
	if strings.TrimSpace(path) == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return err
		}
	}
	ev = sanitizeEvent(ev)
	if ev.Type == "" {
		return fmt.Errorf("audit event type is required")
	}
	if ev.Time == 0 {
		ev.Time = time.Now().Unix()
	}
	data, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func Recent(path string, limit int) ([]Event, error) {
	if strings.TrimSpace(path) == "" {
		var err error
		path, err = DefaultPath()
		if err != nil {
			return nil, err
		}
	}
	if limit <= 0 {
		limit = 50
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	events := make([]Event, 0, limit)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var ev Event
		if err := json.Unmarshal(scanner.Bytes(), &ev); err != nil {
			continue
		}
		ev = sanitizeEvent(ev)
		if ev.Type == "" {
			continue
		}
		if len(events) == limit {
			copy(events, events[1:])
			events[len(events)-1] = ev
			continue
		}
		events = append(events, ev)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func sanitizeEvent(ev Event) Event {
	ev.Type = safeToken(ev.Type)
	ev.Actor = safeToken(ev.Actor)
	ev.Outcome = safeToken(ev.Outcome)
	ev.Data = sanitizeData(ev.Data)
	return ev
}

func sanitizeData(data map[string]any) map[string]any {
	if len(data) == 0 {
		return nil
	}
	keys := make([]string, 0, len(data))
	for key := range data {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make(map[string]any, len(data))
	for _, key := range keys {
		if unsafeKey(key) {
			continue
		}
		if value, ok := safeValue(data[key]); ok {
			out[safeToken(key)] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func unsafeKey(key string) bool {
	k := strings.ToLower(key)
	for _, term := range []string{"token", "secret", "auth", "credential", "password", "ref", "path", "payload", "raw"} {
		if strings.Contains(k, term) {
			return true
		}
	}
	return false
}

func safeValue(value any) (any, bool) {
	switch v := value.(type) {
	case nil:
		return nil, true
	case bool:
		return v, true
	case string:
		return safeText(v), true
	case int:
		return v, true
	case int64:
		return v, true
	case float64:
		return v, true
	case fmt.Stringer:
		return safeText(v.String()), true
	default:
		return nil, false
	}
}

func safeToken(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 120 {
		value = value[:120]
	}
	return value
}

func safeText(value string) string {
	value = safeToken(value)
	if strings.Contains(value, "/") || strings.Contains(value, "\\") {
		return ""
	}
	return value
}
