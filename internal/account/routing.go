package account

import "fmt"

// SelectLowestQuotaAccount picks the provider account with the lowest cached
// primary quota usage. Accounts without quota are treated conservatively, and
// the current account wins exact ties to avoid needless runtime churn.
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
	best := accounts[0]
	bestScore := QuotaRouteScore(st, best, current)
	for _, acc := range accounts[1:] {
		score := QuotaRouteScore(st, acc, current)
		if score < bestScore || (score == bestScore && acc.ID == current) {
			best = acc
			bestScore = score
		}
	}
	return best, nil
}

// QuotaRouteScore is intentionally small and stable: lower is better.
func QuotaRouteScore(st *Store, acc Account, current string) float64 {
	score := 75.0
	if q, err := st.LoadQuota(acc.ID); err == nil {
		score = q.PrimaryUsedPercent
	}
	if acc.ID == current {
		score -= 0.01
	}
	return score
}
