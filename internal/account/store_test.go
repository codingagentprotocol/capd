package account

import (
	"errors"
	"testing"
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

func TestAccountStoreQuotaAndSessionBinding(t *testing.T) {
	st := newStore(t)
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
}

func TestAccountStoreUnknownAccount(t *testing.T) {
	st := newStore(t)
	if _, err := st.LoadAccount("missing"); !errors.Is(err, ErrUnknownAccount) {
		t.Fatalf("err = %v", err)
	}
}
