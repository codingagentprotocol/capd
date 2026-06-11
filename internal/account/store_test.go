package account

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	st, err := OpenStore(t.TempDir() + "/accounts.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestAccountStoreUpsertListAndCurrent(t *testing.T) {
	st := newStore(t)
	acc := Account{
		ID:        "codex-local",
		Provider:  "codex",
		AuthMode:  "oauth",
		Email:     "user@example.com",
		AccountID: "acct_1234567890abcdef",
		Plan:      "pro",
		SecretRef: "file://secret",
	}
	if err := st.UpsertAccount(acc); err != nil {
		t.Fatal(err)
	}
	if err := st.SetCurrentAccount("codex", acc.ID); err != nil {
		t.Fatal(err)
	}

	got, err := st.LoadAccount(acc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Email != acc.Email || got.AccountID != acc.AccountID || got.SecretRef != acc.SecretRef {
		t.Fatalf("account = %+v", got)
	}

	list, err := st.ListAccounts("codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != acc.ID {
		t.Fatalf("list = %+v", list)
	}

	current, err := st.CurrentAccount("codex")
	if err != nil {
		t.Fatal(err)
	}
	if current != acc.ID {
		t.Fatalf("current = %q", current)
	}
}

func TestAccountStoreTightensFilePermissions(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "accounts.db")
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
	assertAccountStoreFileMode(t, dbPath)
	if err := st.UpsertAccount(Account{ID: "codex-local", Provider: "codex", AuthMode: "oauth"}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		assertAccountStoreFileMode(t, path)
	}
}

func TestAccountStoreQuotaAndSessionBinding(t *testing.T) {
	st := newStore(t)
	if err := st.UpsertAccount(Account{ID: "codex-local", Provider: "codex", AuthMode: "oauth"}); err != nil {
		t.Fatal(err)
	}
	q := QuotaSnapshot{
		AccountID:             "codex-local",
		Plan:                  "plus",
		PrimaryUsedPercent:    12.5,
		PrimaryResetAt:        "2026-06-11T16:00:00Z",
		SecondaryUsedPercent:  0.5,
		SecondaryResetAt:      "2026-06-11T18:00:00Z",
		CodeReviewUsedPercent: 3,
		RawJSON:               `{"ok":true}`,
	}
	if err := st.SaveQuota(q); err != nil {
		t.Fatal(err)
	}
	got, err := st.LoadQuota(q.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PrimaryUsedPercent != q.PrimaryUsedPercent || got.RawJSON != q.RawJSON {
		t.Fatalf("quota = %+v", got)
	}

	if err := st.BindSessionAccount("s_1", q.AccountID); err != nil {
		t.Fatal(err)
	}
	accountID, err := st.SessionAccount("s_1")
	if err != nil {
		t.Fatal(err)
	}
	if accountID != q.AccountID {
		t.Fatalf("session account = %q", accountID)
	}

	if err := st.BindSessionAccount("s_1", "missing"); !errors.Is(err, ErrUnknownAccount) {
		t.Fatalf("missing account err = %v", err)
	}
	if err := st.BindSessionAccount("", q.AccountID); err == nil || !strings.Contains(err.Error(), "session id and account id are required") {
		t.Fatalf("empty session err = %v", err)
	}
	accountID, err = st.SessionAccount("s_1")
	if err != nil {
		t.Fatal(err)
	}
	if accountID != q.AccountID {
		t.Fatalf("session account after failed bind = %q", accountID)
	}
}

func TestSaveQuotaValidatesAccount(t *testing.T) {
	st := newStore(t)
	if err := st.SaveQuota(QuotaSnapshot{AccountID: ""}); err == nil || !strings.Contains(err.Error(), "account id is required") {
		t.Fatalf("empty account err = %v", err)
	}
	if err := st.SaveQuota(QuotaSnapshot{AccountID: "missing", PrimaryUsedPercent: 1}); !errors.Is(err, ErrUnknownAccount) {
		t.Fatalf("missing account err = %v", err)
	}
	if _, err := st.LoadQuota("missing"); !errors.Is(err, ErrUnknownAccount) {
		t.Fatalf("quota err = %v", err)
	}
}

func assertAccountStoreFileMode(t *testing.T, path string) {
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

func TestAccountStoreUnknownAccount(t *testing.T) {
	st := newStore(t)
	if _, err := st.LoadAccount("missing"); !errors.Is(err, ErrUnknownAccount) {
		t.Fatalf("err = %v", err)
	}
}

func TestSetCurrentAccountValidatesAccount(t *testing.T) {
	st := newStore(t)
	if err := st.UpsertAccount(Account{ID: "codex-one", Provider: "codex", AuthMode: "oauth"}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertAccount(Account{ID: "gemini-one", Provider: "gemini", AuthMode: "oauth"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetCurrentAccount("codex", "codex-one"); err != nil {
		t.Fatal(err)
	}

	if err := st.SetCurrentAccount("codex", "missing"); !errors.Is(err, ErrUnknownAccount) {
		t.Fatalf("missing account err = %v", err)
	}
	if err := st.SetCurrentAccount("codex", ""); err == nil || !strings.Contains(err.Error(), "account id is required") {
		t.Fatalf("empty account err = %v", err)
	}
	if err := st.SetCurrentAccount("codex", "gemini-one"); err == nil || !strings.Contains(err.Error(), `belongs to provider "gemini"`) {
		t.Fatalf("provider mismatch err = %v", err)
	}

	current, err := st.CurrentAccount("codex")
	if err != nil {
		t.Fatal(err)
	}
	if current != "codex-one" {
		t.Fatalf("current = %q", current)
	}
}

func TestSelectLowestQuotaAccount(t *testing.T) {
	st := newStore(t)
	for _, acc := range []Account{
		{ID: "high", Provider: "codex", AuthMode: "oauth"},
		{ID: "low", Provider: "codex", AuthMode: "oauth"},
		{ID: "missing", Provider: "codex", AuthMode: "oauth"},
	} {
		if err := st.UpsertAccount(acc); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SaveQuota(QuotaSnapshot{AccountID: "high", PrimaryUsedPercent: 90}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveQuota(QuotaSnapshot{AccountID: "low", PrimaryUsedPercent: 10}); err != nil {
		t.Fatal(err)
	}
	got, err := SelectLowestQuotaAccount(st, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "low" {
		t.Fatalf("selected = %+v", got)
	}
}

func TestSelectLowestQuotaAccountIgnoresStaleQuota(t *testing.T) {
	st := newStore(t)
	for _, acc := range []Account{
		{ID: "stale-low", Provider: "codex", AuthMode: "oauth"},
		{ID: "fresh-mid", Provider: "codex", AuthMode: "oauth"},
	} {
		if err := st.UpsertAccount(acc); err != nil {
			t.Fatal(err)
		}
	}
	staleAt := time.Now().Add(-QuotaRouteCacheTTL - time.Minute).Unix()
	if err := st.SaveQuota(QuotaSnapshot{AccountID: "stale-low", PrimaryUsedPercent: 1, CheckedAt: staleAt}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveQuota(QuotaSnapshot{AccountID: "fresh-mid", PrimaryUsedPercent: 20}); err != nil {
		t.Fatal(err)
	}
	got, err := SelectLowestQuotaAccount(st, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "fresh-mid" {
		t.Fatalf("selected = %+v", got)
	}
}

func TestSelectLowestQuotaAccountTiePrefersCurrent(t *testing.T) {
	st := newStore(t)
	for _, id := range []string{"a", "b"} {
		if err := st.UpsertAccount(Account{ID: id, Provider: "codex", AuthMode: "oauth"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetCurrentAccount("codex", "b"); err != nil {
		t.Fatal(err)
	}
	got, err := SelectLowestQuotaAccount(st, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "b" {
		t.Fatalf("selected = %+v", got)
	}
}
