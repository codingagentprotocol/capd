package account

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codingagentprotocol/capd/pkg/protocol"
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

func TestAccountStoreListAccountsAllProviders(t *testing.T) {
	st := newStore(t)
	for _, acc := range []Account{
		{ID: "codex-local", Provider: "codex", AuthMode: "oauth"},
		{ID: "gemini-local", Provider: "gemini", AuthMode: "oauth"},
	} {
		if err := st.UpsertAccount(acc); err != nil {
			t.Fatal(err)
		}
	}
	list, err := st.ListAccounts("")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Provider != "codex" || list[1].Provider != "gemini" {
		t.Fatalf("list = %+v", list)
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

func TestDeleteAccountRemovesRelatedStateAndPromotesCurrent(t *testing.T) {
	st := newStore(t)
	for _, acc := range []Account{
		{ID: "codex-one", Provider: "codex", AuthMode: "oauth"},
		{ID: "codex-two", Provider: "codex", AuthMode: "oauth"},
	} {
		if err := st.UpsertAccount(acc); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetCurrentAccount("codex", "codex-one"); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveQuota(QuotaSnapshot{AccountID: "codex-one", PrimaryUsedPercent: 5}); err != nil {
		t.Fatal(err)
	}
	if err := st.BindSessionAccount("s_1", "codex-one"); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProfile(Profile{Provider: "codex", Name: "work", Description: "Work accounts"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddProfileAccount("codex", "work", "codex-one"); err != nil {
		t.Fatal(err)
	}

	if err := st.DeleteAccount("codex-one"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.LoadAccount("codex-one"); !errors.Is(err, ErrUnknownAccount) {
		t.Fatalf("deleted account err = %v", err)
	}
	if _, err := st.LoadQuota("codex-one"); !errors.Is(err, ErrUnknownAccount) {
		t.Fatalf("deleted quota err = %v", err)
	}
	sessionAccount, err := st.SessionAccount("s_1")
	if err != nil {
		t.Fatal(err)
	}
	if sessionAccount != "" {
		t.Fatalf("session account = %q", sessionAccount)
	}
	members, err := st.ProfileAccounts("codex", "work")
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 0 {
		t.Fatalf("profile members after delete = %+v", members)
	}
	current, err := st.CurrentAccount("codex")
	if err != nil {
		t.Fatal(err)
	}
	if current != "codex-two" {
		t.Fatalf("current = %q", current)
	}

	if err := st.DeleteAccount("codex-two"); err != nil {
		t.Fatal(err)
	}
	current, err = st.CurrentAccount("codex")
	if err != nil {
		t.Fatal(err)
	}
	if current != "" {
		t.Fatalf("current after deleting last account = %q", current)
	}
}

func TestAccountStoreProfiles(t *testing.T) {
	st := newStore(t)
	for _, acc := range []Account{
		{ID: "codex-one", Provider: "codex", AuthMode: "oauth", Email: "one@example.com"},
		{ID: "codex-two", Provider: "codex", AuthMode: "oauth", Email: "two@example.com"},
		{ID: "gemini-one", Provider: "gemini", AuthMode: "oauth"},
	} {
		if err := st.UpsertAccount(acc); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.UpsertProfile(Profile{Provider: "codex", Name: "work", Description: "Work pool"}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertProfile(Profile{Provider: "codex", Name: "personal"}); err != nil {
		t.Fatal(err)
	}
	if err := st.AddProfileAccount("codex", "work", "codex-one"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddProfileAccount("codex", "work", "codex-two"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddProfileAccount("codex", "work", "gemini-one"); err == nil || !strings.Contains(err.Error(), "belongs to provider") {
		t.Fatalf("cross-provider add err = %v", err)
	}
	members, err := st.ProfileAccounts("codex", "work")
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 || members[0].ID != "codex-one" || members[1].ID != "codex-two" {
		t.Fatalf("members = %+v", members)
	}
	if err := st.SetCurrentProfile("codex", "work"); err != nil {
		t.Fatal(err)
	}
	current, err := st.CurrentProfile("codex")
	if err != nil {
		t.Fatal(err)
	}
	if current != "work" {
		t.Fatalf("current profile = %q", current)
	}
	if err := st.RemoveProfileAccount("codex", "work", "codex-one"); err != nil {
		t.Fatal(err)
	}
	members, err = st.ProfileAccounts("codex", "work")
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0].ID != "codex-two" {
		t.Fatalf("members after remove = %+v", members)
	}
	if err := st.DeleteProfile("codex", "work"); err != nil {
		t.Fatal(err)
	}
	current, err = st.CurrentProfile("codex")
	if err != nil {
		t.Fatal(err)
	}
	if current != "personal" {
		t.Fatalf("current after delete = %q", current)
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

func TestSelectQuotaRouteAccount(t *testing.T) {
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
	got, err := SelectQuotaRouteAccount(st, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "low" {
		t.Fatalf("selected = %+v", got)
	}
}

func TestSelectQuotaRouteAccountIgnoresStaleQuota(t *testing.T) {
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
	got, err := SelectQuotaRouteAccount(st, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "fresh-mid" {
		t.Fatalf("selected = %+v", got)
	}
}

func TestSelectQuotaRouteAccountUsesLimitingQuotaWindow(t *testing.T) {
	st := newStore(t)
	for _, acc := range []Account{
		{ID: "primary-low-secondary-high", Provider: "codex", AuthMode: "oauth"},
		{ID: "balanced", Provider: "codex", AuthMode: "oauth"},
	} {
		if err := st.UpsertAccount(acc); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SaveQuota(QuotaSnapshot{
		AccountID:            "primary-low-secondary-high",
		PrimaryUsedPercent:   4,
		SecondaryUsedPercent: 92,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveQuota(QuotaSnapshot{
		AccountID:             "balanced",
		PrimaryUsedPercent:    30,
		SecondaryUsedPercent:  5,
		CodeReviewUsedPercent: 7,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := SelectQuotaRouteAccount(st, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "balanced" {
		t.Fatalf("selected = %+v", got)
	}
	pressure := QuotaRouteEvidence(st, Account{ID: "primary-low-secondary-high", Provider: "codex"})
	if pressure.Score != 92 || pressure.LimitingUsedPercent == nil || *pressure.LimitingUsedPercent != 92 || pressure.LimitingQuotaDimension != "secondary" {
		t.Fatalf("pressure evidence = %+v", pressure)
	}
	if pressure.SecondaryUsedPercent == nil || *pressure.SecondaryUsedPercent != 92 {
		t.Fatalf("secondary evidence = %+v", pressure)
	}
	if pressure.Reason != "auto account primary-low-secondary-high limiting secondary 92% (primary 4%, secondary 92%, code_review 0%)" {
		t.Fatalf("reason = %q", pressure.Reason)
	}
}

func TestRoutePolicyCanTuneFreshnessAndTieBreak(t *testing.T) {
	st := newStore(t)
	for _, id := range []string{"current", "older"} {
		if err := st.UpsertAccount(Account{ID: id, Provider: "codex", AuthMode: "oauth"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetCurrentAccount("codex", "current"); err != nil {
		t.Fatal(err)
	}
	oldCheckedAt := time.Now().Add(-2 * time.Hour).Unix()
	if err := st.SaveQuota(QuotaSnapshot{AccountID: "current", PrimaryUsedPercent: 40, CheckedAt: oldCheckedAt}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveQuota(QuotaSnapshot{AccountID: "older", PrimaryUsedPercent: 20, CheckedAt: oldCheckedAt}); err != nil {
		t.Fatal(err)
	}

	defaultEvidence := QuotaRouteEvidence(st, Account{ID: "older", Provider: "codex"})
	if defaultEvidence.Fresh || defaultEvidence.Score != quotaUnknownScore {
		t.Fatalf("default evidence = %+v", defaultEvidence)
	}

	policy := RoutePolicy{
		FreshTTL:               3 * time.Hour,
		UnknownScore:           88,
		CurrentAccountTieBreak: 0.5,
	}
	got, err := SelectQuotaRouteAccountWithPolicy(st, "codex", policy)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "older" {
		t.Fatalf("selected = %+v", got)
	}
	currentEvidence := policy.Evidence(st, Account{ID: "current", Provider: "codex"}, "current", time.Now())
	if !currentEvidence.Fresh || currentEvidence.Score != 39.5 {
		t.Fatalf("current evidence = %+v", currentEvidence)
	}
	missingEvidence := policy.Evidence(st, Account{ID: "missing", Provider: "codex"}, "", time.Now())
	if missingEvidence.Score != 88 || missingEvidence.QuotaState != protocol.AccountQuotaStateMissing {
		t.Fatalf("missing evidence = %+v", missingEvidence)
	}
}

func TestRoutePolicyPenalizesRecentAccountFailures(t *testing.T) {
	st := newStore(t)
	for _, id := range []string{"healthy", "failed"} {
		if err := st.UpsertAccount(Account{ID: id, Provider: "codex", AuthMode: "oauth"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SaveQuota(QuotaSnapshot{AccountID: "healthy", PrimaryUsedPercent: 18}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveQuota(QuotaSnapshot{AccountID: "failed", PrimaryUsedPercent: 10}); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordAccountFailure("failed", time.Now().Unix()); err != nil {
		t.Fatal(err)
	}

	got, err := SelectQuotaRouteAccount(st, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "healthy" {
		t.Fatalf("selected = %+v", got)
	}
	evidence := QuotaRouteEvidence(st, Account{ID: "failed", Provider: "codex"})
	if evidence.Score != 20 || evidence.RecentFailures != 1 || evidence.HealthPenalty != recentFailureScorePenalty || !strings.Contains(evidence.Reason, "recent failures 1 penalty +10") {
		t.Fatalf("failed evidence = %+v", evidence)
	}

	if err := st.ClearAccountFailures("failed"); err != nil {
		t.Fatal(err)
	}
	got, err = SelectQuotaRouteAccount(st, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "failed" {
		t.Fatalf("selected after clear = %+v", got)
	}
}

func TestRoutePolicyEvidenceSummary(t *testing.T) {
	summary := (RoutePolicy{
		FreshTTL:               45 * time.Minute,
		UnknownScore:           82,
		CurrentAccountTieBreak: 0.25,
		RecentFailureTTL:       2 * time.Hour,
		RecentFailurePenalty:   7,
	}).EvidenceSummary()
	if summary.Name != "conservative-quota-pressure" || summary.FreshTTLSeconds != 2700 || summary.UnknownScore != 82 || summary.CurrentAccountTieBreak != 0.25 || summary.RecentFailurePenalty != 7 || summary.RecentFailureTTLSeconds != 7200 {
		t.Fatalf("summary = %+v", summary)
	}
	if strings.Join(summary.QuotaWindows, ",") != "primary,secondary,code_review" {
		t.Fatalf("quota windows = %+v", summary.QuotaWindows)
	}
	if !strings.Contains(summary.Scoring, "limiting quota pressure") {
		t.Fatalf("scoring = %q", summary.Scoring)
	}
}

func TestSelectQuotaRouteAccountTreatsInvalidQuotaAsUnknown(t *testing.T) {
	st := newStore(t)
	for _, acc := range []Account{
		{ID: "invalid-negative", Provider: "codex", AuthMode: "oauth"},
		{ID: "fresh-mid", Provider: "codex", AuthMode: "oauth"},
	} {
		if err := st.UpsertAccount(acc); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SaveQuota(QuotaSnapshot{AccountID: "invalid-negative", PrimaryUsedPercent: -1}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveQuota(QuotaSnapshot{AccountID: "fresh-mid", PrimaryUsedPercent: 20}); err != nil {
		t.Fatal(err)
	}

	got, err := SelectQuotaRouteAccount(st, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "fresh-mid" {
		t.Fatalf("selected = %+v", got)
	}
	evidence := QuotaRouteEvidence(st, Account{ID: "invalid-negative", Provider: "codex"})
	if evidence.Fresh || evidence.Score != quotaUnknownScore || evidence.PrimaryUsedPercent != nil {
		t.Fatalf("invalid evidence = %+v", evidence)
	}
}

func TestConcurrentQuotaRefreshAndRouting(t *testing.T) {
	st := newStore(t)
	ids := []string{"codex-a", "codex-b", "codex-c"}
	for _, id := range ids {
		if err := st.UpsertAccount(Account{ID: id, Provider: "codex", AuthMode: "oauth"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetCurrentAccount("codex", "codex-c"); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, len(ids)*40+80)
	for i, id := range ids {
		i, id := i, id
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 40; n++ {
				if err := st.SaveQuota(QuotaSnapshot{AccountID: id, PrimaryUsedPercent: float64(i*10 + n%10)}); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	for n := 0; n < 80; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := SelectQuotaRouteAccount(st, "codex"); err != nil {
				errs <- err
				return
			}
			candidates, err := QuotaRouteCandidates(st, "codex")
			if err != nil {
				errs <- err
				return
			}
			if len(candidates) != len(ids) {
				errs <- errors.New("missing route candidate during concurrent quota refresh")
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}

	candidates, err := QuotaRouteCandidates(st, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != len(ids) {
		t.Fatalf("candidates = %+v", candidates)
	}
	for _, candidate := range candidates {
		if !candidate.Fresh || candidate.QuotaState != protocol.AccountQuotaStateFresh || candidate.PrimaryUsedPercent == nil {
			t.Fatalf("candidate after concurrent refresh = %+v", candidate)
		}
	}
}

func TestSelectQuotaRouteAccountTiePrefersCurrent(t *testing.T) {
	st := newStore(t)
	for _, id := range []string{"a", "b"} {
		if err := st.UpsertAccount(Account{ID: id, Provider: "codex", AuthMode: "oauth"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetCurrentAccount("codex", "b"); err != nil {
		t.Fatal(err)
	}
	got, err := SelectQuotaRouteAccount(st, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "b" {
		t.Fatalf("selected = %+v", got)
	}
}

func TestSelectQuotaRouteAccountTieFallsBackToID(t *testing.T) {
	st := newStore(t)
	for _, id := range []string{"b", "a"} {
		if err := st.UpsertAccount(Account{ID: id, Provider: "codex", AuthMode: "oauth"}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := SelectQuotaRouteAccount(st, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "a" {
		t.Fatalf("selected = %+v", got)
	}

	// Metadata refreshes update account.updated_at, but equal-score auto
	// routing should not start bouncing between unknown accounts.
	a, err := st.LoadAccount("a")
	if err != nil {
		t.Fatal(err)
	}
	a.Email = "a@example.com"
	if err := st.UpsertAccount(a); err != nil {
		t.Fatal(err)
	}
	b, err := st.LoadAccount("b")
	if err != nil {
		t.Fatal(err)
	}
	b.Email = "b@example.com"
	if err := st.UpsertAccount(b); err != nil {
		t.Fatal(err)
	}
	got, err = SelectQuotaRouteAccount(st, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "a" {
		t.Fatalf("selected after metadata updates = %+v", got)
	}
}

func TestQuotaRouteEvidenceAndReason(t *testing.T) {
	st := newStore(t)
	for _, id := range []string{"fresh", "stale", "missing"} {
		if err := st.UpsertAccount(Account{ID: id, Provider: "codex", AuthMode: "oauth"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetCurrentAccount("codex", "missing"); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveQuota(QuotaSnapshot{AccountID: "fresh", PrimaryUsedPercent: 12}); err != nil {
		t.Fatal(err)
	}
	staleAt := time.Now().Add(-QuotaRouteCacheTTL - time.Minute).Unix()
	if err := st.SaveQuota(QuotaSnapshot{AccountID: "stale", PrimaryUsedPercent: 3, CheckedAt: staleAt}); err != nil {
		t.Fatal(err)
	}

	fresh := QuotaRouteEvidence(st, Account{ID: "fresh", Provider: "codex", SecretRef: "file:fresh"})
	if fresh.AccountID != "fresh" || fresh.SecretBackend != "file" || fresh.QuotaState != protocol.AccountQuotaStateFresh || !fresh.Fresh || fresh.PrimaryUsedPercent == nil || *fresh.PrimaryUsedPercent != 12 || fresh.Score != 12 || fresh.Reason != "auto account fresh primary 12%" {
		t.Fatalf("fresh evidence = %+v", fresh)
	}
	if fresh.LimitingUsedPercent == nil || *fresh.LimitingUsedPercent != 12 || fresh.LimitingQuotaDimension != "primary" {
		t.Fatalf("fresh limiting evidence = %+v", fresh)
	}
	if got := QuotaRouteReason(st, Account{ID: "fresh", Provider: "codex"}); got != "auto account fresh primary 12%" {
		t.Fatalf("fresh reason = %q", got)
	}

	stale := QuotaRouteEvidence(st, Account{ID: "stale", Provider: "codex", SecretRef: "native:stale"})
	if stale.AccountID != "stale" || stale.SecretBackend != "native" || stale.QuotaState != protocol.AccountQuotaStateStale || stale.Fresh || stale.CheckedAt != staleAt || stale.PrimaryUsedPercent == nil || *stale.PrimaryUsedPercent != 3 || stale.Score != quotaUnknownScore || stale.Reason != "auto account stale without fresh cached quota" {
		t.Fatalf("stale evidence = %+v", stale)
	}

	missing := QuotaRouteEvidence(st, Account{ID: "missing", Provider: "codex", SecretRef: "bad-ref"})
	if missing.AccountID != "missing" || missing.SecretBackend != "" || missing.QuotaState != protocol.AccountQuotaStateMissing || missing.Fresh || missing.PrimaryUsedPercent != nil || missing.Score != quotaUnknownScore-0.01 || missing.Reason != "auto account missing without fresh cached quota; current account tie-break" {
		t.Fatalf("missing evidence = %+v", missing)
	}
	if got := QuotaRouteReason(st, Account{ID: "missing", Provider: "codex"}); got != "auto account missing without fresh cached quota; current account tie-break" {
		t.Fatalf("missing reason = %q", got)
	}

	candidates, err := QuotaRouteCandidates(st, "codex")
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 3 {
		t.Fatalf("candidates = %+v", candidates)
	}
	if candidates[0].AccountID != "fresh" || candidates[0].SecretBackend != "" || !candidates[0].Fresh || candidates[0].Score != 12 || candidates[0].Reason != "auto account fresh primary 12%" {
		t.Fatalf("first candidate = %+v", candidates[0])
	}
	if candidates[1].AccountID != "missing" || candidates[1].QuotaState != protocol.AccountQuotaStateMissing || candidates[1].Score != quotaUnknownScore-0.01 || candidates[1].Reason != "auto account missing without fresh cached quota; current account tie-break" {
		t.Fatalf("second candidate = %+v", candidates[1])
	}
	if candidates[2].AccountID != "stale" || candidates[2].QuotaState != protocol.AccountQuotaStateStale || candidates[2].Score != quotaUnknownScore || candidates[2].Reason != "auto account stale without fresh cached quota" {
		t.Fatalf("third candidate = %+v", candidates[2])
	}
}
