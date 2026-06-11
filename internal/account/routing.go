package account

import (
	"fmt"
	"time"
)

const (
	// QuotaRouteCacheTTL bounds how long an auto-route decision trusts a
	// cached quota value. Older rows are treated like missing quota so a stale
	// low-usage account does not dominate routing indefinitely.
	QuotaRouteCacheTTL = 30 * time.Minute
	quotaUnknownScore  = 75.0
)

// SelectLowestQuotaAccount picks the provider account with the lowest fresh
// cached primary quota usage. Accounts without fresh quota are treated
// conservatively, and the current account wins exact ties to avoid needless
// runtime churn.
func SelectLowestQuotaAccount(st *Store, provider string) (Account, error) {
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

// QuotaRouteScore is intentionally small and stable: lower is better.
func QuotaRouteScore(st *Store, acc Account, current string) float64 {
	return quotaRouteScoreAt(st, acc, current, time.Now())
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
