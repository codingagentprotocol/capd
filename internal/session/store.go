package session

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	_ "modernc.org/sqlite" // CGO-free driver; keeps cross-compilation trivial

	"github.com/codingagentprotocol/capd/pkg/protocol"
)

// ErrSessionUnknown is returned by LoadSession for ids the store never saw.
var ErrSessionUnknown = errors.New("session: unknown id")

// Store persists sessions and their event logs so that a daemon restart
// loses nothing: the agent-native id lets a revived session keep its
// conversation, and the event log serves attach replays.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS sessions (
	id         TEXT PRIMARY KEY,
	agent_id   TEXT NOT NULL,
	native_id  TEXT NOT NULL DEFAULT '',
	cwd        TEXT NOT NULL DEFAULT '',
	ended      INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE TABLE IF NOT EXISTS events (
	session_id TEXT NOT NULL,
	seq        INTEGER NOT NULL,
	type       TEXT NOT NULL,
	data       TEXT NOT NULL DEFAULT '{}',
	PRIMARY KEY (session_id, seq)
);`

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("session: open store: %w", err)
	}
	// SQLite allows one writer; serializing through a single connection
	// avoids SQLITE_BUSY without a retry dance.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("session: enable WAL: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("session: create schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (st *Store) Close() error { return st.db.Close() }

// SessionRecord is the persisted identity of a session.
type SessionRecord struct {
	ID        string
	AgentID   string
	NativeID  string
	Cwd       string
	Ended     bool
	CreatedAt int64
}

// LoadSessions returns the most recent sessions, newest first.
func (st *Store) LoadSessions(limit int) ([]SessionRecord, error) {
	rows, err := st.db.Query(
		"SELECT id, agent_id, native_id, cwd, ended, created_at FROM sessions ORDER BY created_at DESC, id DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SessionRecord
	for rows.Next() {
		var rec SessionRecord
		var ended int
		if err := rows.Scan(&rec.ID, &rec.AgentID, &rec.NativeID, &rec.Cwd, &ended, &rec.CreatedAt); err != nil {
			return nil, err
		}
		rec.Ended = ended != 0
		out = append(out, rec)
	}
	return out, rows.Err()
}

func (st *Store) SaveSession(rec SessionRecord) error {
	_, err := st.db.Exec(
		"INSERT OR REPLACE INTO sessions (id, agent_id, native_id, cwd, ended) VALUES (?, ?, ?, ?, ?)",
		rec.ID, rec.AgentID, rec.NativeID, rec.Cwd, boolInt(rec.Ended))
	return err
}

func (st *Store) SetNativeID(id, nativeID string) error {
	_, err := st.db.Exec("UPDATE sessions SET native_id = ? WHERE id = ?", nativeID, id)
	return err
}

func (st *Store) MarkEnded(id string) error {
	_, err := st.db.Exec("UPDATE sessions SET ended = 1 WHERE id = ?", id)
	return err
}

func (st *Store) LoadSession(id string) (SessionRecord, error) {
	rec := SessionRecord{ID: id}
	var ended int
	err := st.db.QueryRow(
		"SELECT agent_id, native_id, cwd, ended FROM sessions WHERE id = ?", id,
	).Scan(&rec.AgentID, &rec.NativeID, &rec.Cwd, &ended)
	if errors.Is(err, sql.ErrNoRows) {
		return rec, ErrSessionUnknown
	}
	rec.Ended = ended != 0
	return rec, err
}

func (st *Store) AppendEvent(ev protocol.Event) error {
	data, err := json.Marshal(ev.Data)
	if err != nil {
		data = []byte("{}")
	}
	_, err = st.db.Exec(
		"INSERT OR IGNORE INTO events (session_id, seq, type, data) VALUES (?, ?, ?, ?)",
		ev.SessionID, ev.Seq, string(ev.Type), string(data))
	return err
}

// LoadEvents returns up to limit events from fromSeq onward, in seq order.
func (st *Store) LoadEvents(sessionID string, fromSeq uint64, limit int) ([]protocol.Event, error) {
	rows, err := st.db.Query(
		"SELECT seq, type, data FROM events WHERE session_id = ? AND seq >= ? ORDER BY seq LIMIT ?",
		sessionID, fromSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []protocol.Event
	for rows.Next() {
		ev := protocol.Event{SessionID: sessionID}
		var typ, data string
		if err := rows.Scan(&ev.Seq, &typ, &data); err != nil {
			return nil, err
		}
		ev.Type = protocol.EventType(typ)
		json.Unmarshal([]byte(data), &ev.Data)
		out = append(out, ev)
	}
	return out, rows.Err()
}

// LoadRecentEvents returns the newest limit events, still ordered by seq.
func (st *Store) LoadRecentEvents(sessionID string, limit int) ([]protocol.Event, error) {
	rows, err := st.db.Query(
		"SELECT seq, type, data FROM (SELECT seq, type, data FROM events WHERE session_id = ? ORDER BY seq DESC LIMIT ?) ORDER BY seq",
		sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []protocol.Event
	for rows.Next() {
		ev := protocol.Event{SessionID: sessionID}
		var typ, data string
		if err := rows.Scan(&ev.Seq, &typ, &data); err != nil {
			return nil, err
		}
		ev.Type = protocol.EventType(typ)
		json.Unmarshal([]byte(data), &ev.Data)
		out = append(out, ev)
	}
	return out, rows.Err()
}

// NextSeq returns the next sequence number after the stored event log.
func (st *Store) NextSeq(sessionID string) (uint64, error) {
	var next sql.NullInt64
	if err := st.db.QueryRow("SELECT MAX(seq) + 1 FROM events WHERE session_id = ?", sessionID).Scan(&next); err != nil {
		return 0, err
	}
	if !next.Valid {
		return 0, nil
	}
	return uint64(next.Int64), nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
