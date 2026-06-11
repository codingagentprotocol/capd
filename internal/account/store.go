package account

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	_ "modernc.org/sqlite" // CGO-free driver; same portability profile as session store.
)

var ErrUnknownAccount = errors.New("account: unknown account")

type Store struct {
	db   *sql.DB
	path string
}

type Account struct {
	ID        string
	Provider  string
	AuthMode  string
	Email     string
	AccountID string
	Plan      string
	SecretRef string
	CreatedAt int64
	UpdatedAt int64
}

type QuotaSnapshot struct {
	AccountID             string
	Plan                  string
	PrimaryUsedPercent    float64
	PrimaryResetAt        string
	SecondaryUsedPercent  float64
	SecondaryResetAt      string
	CodeReviewUsedPercent float64
	CheckedAt             int64
	RawJSON               string
}

const schema = `
CREATE TABLE IF NOT EXISTS accounts (
	id          TEXT PRIMARY KEY,
	provider    TEXT NOT NULL,
	auth_mode   TEXT NOT NULL,
	email       TEXT NOT NULL DEFAULT '',
	account_id  TEXT NOT NULL DEFAULT '',
	plan        TEXT NOT NULL DEFAULT '',
	secret_ref  TEXT NOT NULL DEFAULT '',
	created_at  INTEGER NOT NULL DEFAULT (strftime('%s','now')),
	updated_at  INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
CREATE TABLE IF NOT EXISTS account_state (
	provider           TEXT PRIMARY KEY,
	current_account_id TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS account_quota (
	account_id                TEXT PRIMARY KEY,
	plan                      TEXT NOT NULL DEFAULT '',
	primary_used_percent      REAL NOT NULL DEFAULT 0,
	primary_reset_at          TEXT NOT NULL DEFAULT '',
	secondary_used_percent    REAL NOT NULL DEFAULT 0,
	secondary_reset_at        TEXT NOT NULL DEFAULT '',
	code_review_used_percent  REAL NOT NULL DEFAULT 0,
	checked_at                INTEGER NOT NULL DEFAULT (strftime('%s','now')),
	raw_json                  TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS session_accounts (
	session_id TEXT PRIMARY KEY,
	account_id TEXT NOT NULL,
	created_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);`

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("account: open store: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("account: enable WAL: %w", err)
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("account: create schema: %w", err)
	}
	st := &Store{db: db, path: path}
	if err := st.tightenFilePermissions(); err != nil {
		db.Close()
		return nil, err
	}
	return st, nil
}

func (st *Store) Close() error { return st.db.Close() }

func (st *Store) UpsertAccount(acc Account) error {
	if acc.ID == "" {
		return fmt.Errorf("account id is required")
	}
	if acc.Provider == "" {
		return fmt.Errorf("provider is required")
	}
	_, err := st.db.Exec(`
INSERT INTO accounts (id, provider, auth_mode, email, account_id, plan, secret_ref)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	provider = excluded.provider,
	auth_mode = excluded.auth_mode,
	email = excluded.email,
	account_id = excluded.account_id,
	plan = excluded.plan,
	secret_ref = excluded.secret_ref,
	updated_at = strftime('%s','now')`,
		acc.ID, acc.Provider, acc.AuthMode, acc.Email, acc.AccountID, acc.Plan, acc.SecretRef)
	return st.afterWrite(err)
}

func (st *Store) LoadAccount(id string) (Account, error) {
	var acc Account
	err := st.db.QueryRow(`
SELECT id, provider, auth_mode, email, account_id, plan, secret_ref, created_at, updated_at
FROM accounts WHERE id = ?`, id).Scan(
		&acc.ID, &acc.Provider, &acc.AuthMode, &acc.Email, &acc.AccountID, &acc.Plan,
		&acc.SecretRef, &acc.CreatedAt, &acc.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return acc, ErrUnknownAccount
	}
	return acc, err
}

func (st *Store) ListAccounts(provider string) ([]Account, error) {
	rows, err := st.db.Query(`
SELECT id, provider, auth_mode, email, account_id, plan, secret_ref, created_at, updated_at
FROM accounts WHERE provider = ? ORDER BY updated_at DESC, id`, provider)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Account
	for rows.Next() {
		var acc Account
		if err := rows.Scan(&acc.ID, &acc.Provider, &acc.AuthMode, &acc.Email, &acc.AccountID,
			&acc.Plan, &acc.SecretRef, &acc.CreatedAt, &acc.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, acc)
	}
	return out, rows.Err()
}

func (st *Store) SetCurrentAccount(provider, accountID string) error {
	if provider == "" {
		return fmt.Errorf("provider is required")
	}
	_, err := st.db.Exec(`
INSERT INTO account_state (provider, current_account_id) VALUES (?, ?)
ON CONFLICT(provider) DO UPDATE SET current_account_id = excluded.current_account_id`,
		provider, accountID)
	return st.afterWrite(err)
}

func (st *Store) CurrentAccount(provider string) (string, error) {
	var id string
	err := st.db.QueryRow("SELECT current_account_id FROM account_state WHERE provider = ?", provider).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func (st *Store) SaveQuota(q QuotaSnapshot) error {
	if q.AccountID == "" {
		return fmt.Errorf("account id is required")
	}
	if q.CheckedAt == 0 {
		q.CheckedAt = time.Now().Unix()
	}
	_, err := st.db.Exec(`
INSERT INTO account_quota (
	account_id, plan, primary_used_percent, primary_reset_at,
	secondary_used_percent, secondary_reset_at, code_review_used_percent, checked_at, raw_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(account_id) DO UPDATE SET
	plan = excluded.plan,
	primary_used_percent = excluded.primary_used_percent,
	primary_reset_at = excluded.primary_reset_at,
	secondary_used_percent = excluded.secondary_used_percent,
	secondary_reset_at = excluded.secondary_reset_at,
	code_review_used_percent = excluded.code_review_used_percent,
	checked_at = excluded.checked_at,
	raw_json = excluded.raw_json`,
		q.AccountID, q.Plan, q.PrimaryUsedPercent, q.PrimaryResetAt,
		q.SecondaryUsedPercent, q.SecondaryResetAt, q.CodeReviewUsedPercent, q.CheckedAt, q.RawJSON)
	return st.afterWrite(err)
}

func (st *Store) LoadQuota(accountID string) (QuotaSnapshot, error) {
	var q QuotaSnapshot
	err := st.db.QueryRow(`
SELECT account_id, plan, primary_used_percent, primary_reset_at,
	secondary_used_percent, secondary_reset_at, code_review_used_percent, checked_at, raw_json
FROM account_quota WHERE account_id = ?`, accountID).Scan(
		&q.AccountID, &q.Plan, &q.PrimaryUsedPercent, &q.PrimaryResetAt,
		&q.SecondaryUsedPercent, &q.SecondaryResetAt, &q.CodeReviewUsedPercent,
		&q.CheckedAt, &q.RawJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return q, ErrUnknownAccount
	}
	return q, err
}

func (st *Store) BindSessionAccount(sessionID, accountID string) error {
	if sessionID == "" || accountID == "" {
		return fmt.Errorf("session id and account id are required")
	}
	_, err := st.db.Exec(`
INSERT INTO session_accounts (session_id, account_id) VALUES (?, ?)
ON CONFLICT(session_id) DO UPDATE SET account_id = excluded.account_id`, sessionID, accountID)
	return st.afterWrite(err)
}

func (st *Store) SessionAccount(sessionID string) (string, error) {
	var accountID string
	err := st.db.QueryRow("SELECT account_id FROM session_accounts WHERE session_id = ?", sessionID).Scan(&accountID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return accountID, err
}

func (st *Store) afterWrite(err error) error {
	if err != nil {
		return err
	}
	return st.tightenFilePermissions()
}

func (st *Store) tightenFilePermissions() error {
	if st.path == "" {
		return nil
	}
	for _, path := range []string{st.path, st.path + "-wal", st.path + "-shm"} {
		if err := os.Chmod(path, 0o600); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("account: chmod store file %q: %w", path, err)
		}
	}
	return nil
}
