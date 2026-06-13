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

type Profile struct {
	Provider    string
	Name        string
	Description string
	CreatedAt   int64
	UpdatedAt   int64
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
);
CREATE TABLE IF NOT EXISTS account_profiles (
	provider    TEXT NOT NULL,
	name        TEXT NOT NULL,
	description TEXT NOT NULL DEFAULT '',
	created_at  INTEGER NOT NULL DEFAULT (strftime('%s','now')),
	updated_at  INTEGER NOT NULL DEFAULT (strftime('%s','now')),
	PRIMARY KEY (provider, name)
);
CREATE TABLE IF NOT EXISTS account_profile_members (
	provider   TEXT NOT NULL,
	name       TEXT NOT NULL,
	account_id TEXT NOT NULL,
	created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
	PRIMARY KEY (provider, name, account_id)
);
CREATE TABLE IF NOT EXISTS account_profile_state (
	provider TEXT PRIMARY KEY,
	name     TEXT NOT NULL DEFAULT ''
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
	query := `
SELECT id, provider, auth_mode, email, account_id, plan, secret_ref, created_at, updated_at
FROM accounts`
	var args []any
	if provider != "" {
		query += " WHERE provider = ?"
		args = append(args, provider)
	}
	query += " ORDER BY provider, updated_at DESC, id"
	rows, err := st.db.Query(query, args...)
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

func (st *Store) DeleteAccount(id string) error {
	if id == "" {
		return fmt.Errorf("account id is required")
	}
	acc, err := st.LoadAccount(id)
	if err != nil {
		return err
	}
	tx, err := st.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM account_quota WHERE account_id = ?", id); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM session_accounts WHERE account_id = ?", id); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM account_profile_members WHERE account_id = ?", id); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM accounts WHERE id = ?", id); err != nil {
		return err
	}
	current, err := currentAccountTx(tx, acc.Provider)
	if err != nil {
		return err
	}
	if current == id {
		next, err := nextProviderAccountTx(tx, acc.Provider)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`
INSERT INTO account_state (provider, current_account_id) VALUES (?, ?)
ON CONFLICT(provider) DO UPDATE SET current_account_id = excluded.current_account_id`,
			acc.Provider, next); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return st.afterWrite(nil)
}

func (st *Store) UpsertProfile(profile Profile) error {
	if profile.Provider == "" {
		return fmt.Errorf("provider is required")
	}
	if profile.Name == "" {
		return fmt.Errorf("profile name is required")
	}
	_, err := st.db.Exec(`
INSERT INTO account_profiles (provider, name, description)
VALUES (?, ?, ?)
ON CONFLICT(provider, name) DO UPDATE SET
	description = excluded.description,
	updated_at = strftime('%s','now')`,
		profile.Provider, profile.Name, profile.Description)
	return st.afterWrite(err)
}

func (st *Store) ListProfiles(provider string) ([]Profile, error) {
	query := `
SELECT provider, name, description, created_at, updated_at
FROM account_profiles`
	var args []any
	if provider != "" {
		query += " WHERE provider = ?"
		args = append(args, provider)
	}
	query += " ORDER BY provider, name"
	rows, err := st.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Profile
	for rows.Next() {
		var profile Profile
		if err := rows.Scan(&profile.Provider, &profile.Name, &profile.Description, &profile.CreatedAt, &profile.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, profile)
	}
	return out, rows.Err()
}

func (st *Store) LoadProfile(provider, name string) (Profile, error) {
	var profile Profile
	err := st.db.QueryRow(`
SELECT provider, name, description, created_at, updated_at
FROM account_profiles WHERE provider = ? AND name = ?`, provider, name).Scan(
		&profile.Provider, &profile.Name, &profile.Description, &profile.CreatedAt, &profile.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return profile, ErrUnknownAccount
	}
	return profile, err
}

func (st *Store) DeleteProfile(provider, name string) error {
	if provider == "" {
		return fmt.Errorf("provider is required")
	}
	if name == "" {
		return fmt.Errorf("profile name is required")
	}
	tx, err := st.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM account_profile_members WHERE provider = ? AND name = ?", provider, name); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM account_profiles WHERE provider = ? AND name = ?", provider, name); err != nil {
		return err
	}
	current, err := currentProfileTx(tx, provider)
	if err != nil {
		return err
	}
	if current == name {
		next, err := nextProviderProfileTx(tx, provider)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`
INSERT INTO account_profile_state (provider, name) VALUES (?, ?)
ON CONFLICT(provider) DO UPDATE SET name = excluded.name`, provider, next); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	return st.afterWrite(nil)
}

func (st *Store) AddProfileAccount(provider, name, accountID string) error {
	if _, err := st.LoadProfile(provider, name); err != nil {
		return err
	}
	acc, err := st.LoadAccount(accountID)
	if err != nil {
		return err
	}
	if acc.Provider != provider {
		return fmt.Errorf("account %q belongs to provider %q, not %q", accountID, acc.Provider, provider)
	}
	_, err = st.db.Exec(`
INSERT INTO account_profile_members (provider, name, account_id)
VALUES (?, ?, ?)
ON CONFLICT(provider, name, account_id) DO NOTHING`, provider, name, accountID)
	return st.afterWrite(err)
}

func (st *Store) RemoveProfileAccount(provider, name, accountID string) error {
	if provider == "" {
		return fmt.Errorf("provider is required")
	}
	if name == "" {
		return fmt.Errorf("profile name is required")
	}
	if accountID == "" {
		return fmt.Errorf("account id is required")
	}
	_, err := st.db.Exec(`
DELETE FROM account_profile_members
WHERE provider = ? AND name = ? AND account_id = ?`, provider, name, accountID)
	return st.afterWrite(err)
}

func (st *Store) ProfileAccounts(provider, name string) ([]Account, error) {
	if _, err := st.LoadProfile(provider, name); err != nil {
		return nil, err
	}
	rows, err := st.db.Query(`
SELECT a.id, a.provider, a.auth_mode, a.email, a.account_id, a.plan, a.secret_ref, a.created_at, a.updated_at
FROM accounts a
JOIN account_profile_members m ON m.account_id = a.id
WHERE m.provider = ? AND m.name = ?
ORDER BY a.provider, a.id`, provider, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Account
	for rows.Next() {
		var acc Account
		if err := rows.Scan(&acc.ID, &acc.Provider, &acc.AuthMode, &acc.Email, &acc.AccountID, &acc.Plan, &acc.SecretRef, &acc.CreatedAt, &acc.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, acc)
	}
	return out, rows.Err()
}

func (st *Store) SetCurrentProfile(provider, name string) error {
	if provider == "" {
		return fmt.Errorf("provider is required")
	}
	if name == "" {
		return fmt.Errorf("profile name is required")
	}
	if _, err := st.LoadProfile(provider, name); err != nil {
		return err
	}
	_, err := st.db.Exec(`
INSERT INTO account_profile_state (provider, name) VALUES (?, ?)
ON CONFLICT(provider) DO UPDATE SET name = excluded.name`, provider, name)
	return st.afterWrite(err)
}

func (st *Store) CurrentProfile(provider string) (string, error) {
	return currentProfileQuery(st.db, provider)
}

func (st *Store) SetCurrentAccount(provider, accountID string) error {
	if provider == "" {
		return fmt.Errorf("provider is required")
	}
	if accountID == "" {
		return fmt.Errorf("account id is required")
	}
	acc, err := st.LoadAccount(accountID)
	if err != nil {
		return err
	}
	if acc.Provider != provider {
		return fmt.Errorf("account %q belongs to provider %q, not %q", accountID, acc.Provider, provider)
	}
	_, err = st.db.Exec(`
INSERT INTO account_state (provider, current_account_id) VALUES (?, ?)
ON CONFLICT(provider) DO UPDATE SET current_account_id = excluded.current_account_id`,
		provider, accountID)
	return st.afterWrite(err)
}

func (st *Store) CurrentAccount(provider string) (string, error) {
	return currentAccountQuery(st.db, provider)
}

func (st *Store) SaveQuota(q QuotaSnapshot) error {
	if q.AccountID == "" {
		return fmt.Errorf("account id is required")
	}
	if _, err := st.LoadAccount(q.AccountID); err != nil {
		return err
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
	if _, err := st.LoadAccount(accountID); err != nil {
		return err
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

type queryer interface {
	QueryRow(query string, args ...any) *sql.Row
}

func currentAccountTx(tx *sql.Tx, provider string) (string, error) {
	return currentAccountQuery(tx, provider)
}

func currentProfileTx(tx *sql.Tx, provider string) (string, error) {
	return currentProfileQuery(tx, provider)
}

func currentAccountQuery(q queryer, provider string) (string, error) {
	var id string
	err := q.QueryRow("SELECT current_account_id FROM account_state WHERE provider = ?", provider).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func currentProfileQuery(q queryer, provider string) (string, error) {
	var name string
	err := q.QueryRow("SELECT name FROM account_profile_state WHERE provider = ?", provider).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return name, err
}

func nextProviderAccountTx(tx *sql.Tx, provider string) (string, error) {
	var id string
	err := tx.QueryRow(`
SELECT id FROM accounts
WHERE provider = ?
ORDER BY updated_at DESC, id
LIMIT 1`, provider).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func nextProviderProfileTx(tx *sql.Tx, provider string) (string, error) {
	var name string
	err := tx.QueryRow(`
SELECT name FROM account_profiles
WHERE provider = ?
ORDER BY updated_at DESC, name
LIMIT 1`, provider).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return name, err
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
