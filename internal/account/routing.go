package account

import (
	"fmt"
	"time"

	"github.com/codingagentprotocol/capd/pkg/protocol"
)

const (
	// QuotaRouteCacheTTL bounds how long an auto-route decision trusts a
	// cached quota value. Older rows are treated like missing quota so a stale
	// low-usage account does not dominate routing indefinitely.
	QuotaRouteCacheTTL = 30 * time.Minute
	quotaUnknownScore  = 75.0
)

// SelectQuotaRouteAccount picks the provider account with the lowest routing
// score. Fresh quota uses primary usage percent directly; missing or stale
// quota is assigned a conservative unknown score, and the current account wins
// exact ties to avoid needless runtime churn.
func SelectQuotaRouteAccount(st *Store, provider string) (Account, error) {
	if st == nil {
		return Account{}, fmt.Errorf("account store is required")
	}
	accounts, err := st.ListAccounts(provider)
	if err != nil {
		return Account{}, err
	}
	if len(accounts) == 0 {
		return Account{}, ErrUnknownAccount
	}
	current, _ := st.CurrentAccount(provider)
	now := time.Now()
	best := accounts[0]
	bestScore := quotaRouteScoreAt(st, best, current, now)
	for _, acc := range accounts[1:] {
		score := quotaRouteScoreAt(st, acc, current, now)
		if score < bestScore || (score == bestScore && acc.ID == current) {
			best = acc
			bestScore = score
		}
	}
	return best, nil
}

// SelectLowestQuotaAccount is kept for older callers; new code should use
// SelectQuotaRouteAccount to reflect the conservative score semantics.
func SelectLowestQuotaAccount(st *Store, provider string) (Account, error) {
	return SelectQuotaRouteAccount(st, provider)
}

// QuotaRouteScore is intentionally small and stable: lower is better.
func QuotaRouteScore(st *Store, acc Account, current string) float64 {
	return quotaRouteScoreAt(st, acc, current, time.Now())
}

// QuotaRouteEvidence returns the protocol-safe route evidence for an account.
// It is the shared source for CLI and JSON-RPC routing responses so score,
// quota freshness, and state labels cannot drift between surfaces.
func QuotaRouteEvidence(st *Store, acc Account) protocol.AccountRouteEvidence {
	evidence := protocol.AccountRouteEvidence{
		Score:      quotaUnknownScore,
		QuotaState: protocol.AccountQuotaStateMissing,
	}
	if st == nil {
		return evidence
	}
	current, _ := st.CurrentAccount(acc.Provider)
	evidence.Score = QuotaRouteScore(st, acc, current)
	if q, err := st.LoadQuota(acc.ID); err == nil {
		evidence.CheckedAt = q.CheckedAt
		evidence.PrimaryUsedPercent = &q.PrimaryUsedPercent
		if QuotaSnapshotFresh(q, time.Now()) {
			evidence.QuotaState = protocol.AccountQuotaStateFresh
			evidence.Fresh = true
		} else {
			evidence.QuotaState = protocol.AccountQuotaStateStale
		}
	}
	return evidence
}

// QuotaRouteReason gives a short human-readable explanation for auto account
// routing. It intentionally mirrors QuotaRouteEvidence's freshness semantics.
func QuotaRouteReason(st *Store, acc Account) string {
	if st != nil {
		if q, err := st.LoadQuota(acc.ID); err == nil && QuotaSnapshotFresh(q, time.Now()) {
			return fmt.Sprintf("auto account %s primary %.0f%%", acc.ID, q.PrimaryUsedPercent)
		}
	}
	return fmt.Sprintf("auto account %s without fresh cached quota", acc.ID)
}

func quotaRouteScoreAt(st *Store, acc Account, current string, now time.Time) float64 {
	score := quotaUnknownScore
	if q, err := st.LoadQuota(acc.ID); err == nil && QuotaSnapshotFresh(q, now) {
		score = q.PrimaryUsedPercent
	}
	if acc.ID == current {
		score -= 0.01
	}
	return score
}

func QuotaSnapshotFresh(q QuotaSnapshot, now time.Time) bool {
	if q.CheckedAt <= 0 {
		return false
	}
	age := now.Unix() - q.CheckedAt
	return age >= 0 && time.Duration(age)*time.Second <= QuotaRouteCacheTTL
}
