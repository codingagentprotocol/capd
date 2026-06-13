package account

import (
	"fmt"
	"sort"
	"time"

	"github.com/codingagentprotocol/capd/internal/account/secret"
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
		if score < bestScore || (score == bestScore && routeTieBeats(acc, best, current)) {
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
	if st == nil {
		return protocol.AccountRouteEvidence{
			AccountID:     acc.ID,
			SecretBackend: routeSecretBackend(acc),
			Score:         quotaUnknownScore,
			QuotaState:    protocol.AccountQuotaStateMissing,
			Reason:        "missing cached quota",
		}
	}
	current, _ := st.CurrentAccount(acc.Provider)
	return quotaRouteEvidenceAt(st, acc, current, time.Now())
}

// QuotaRouteCandidates returns every provider account with the same
// protocol-safe evidence used by auto routing, sorted in the order the router
// would consider them: lowest score first, then the stable tie-breaker.
func QuotaRouteCandidates(st *Store, provider string) ([]protocol.AccountRouteEvidence, error) {
	if st == nil {
		return nil, fmt.Errorf("account store is required")
	}
	accounts, err := st.ListAccounts(provider)
	if err != nil {
		return nil, err
	}
	if len(accounts) == 0 {
		return nil, ErrUnknownAccount
	}
	current, _ := st.CurrentAccount(provider)
	now := time.Now()
	sort.Slice(accounts, func(i, j int) bool {
		leftScore := quotaRouteScoreAt(st, accounts[i], current, now)
		rightScore := quotaRouteScoreAt(st, accounts[j], current, now)
		if leftScore != rightScore {
			return leftScore < rightScore
		}
		return routeTieBeats(accounts[i], accounts[j], current)
	})
	candidates := make([]protocol.AccountRouteEvidence, 0, len(accounts))
	for _, acc := range accounts {
		candidates = append(candidates, quotaRouteEvidenceAt(st, acc, current, now))
	}
	return candidates, nil
}

// QuotaRouteReason gives a short human-readable explanation for auto account
// routing. It intentionally mirrors QuotaRouteEvidence's freshness semantics.
func QuotaRouteReason(st *Store, acc Account) string {
	if st == nil {
		return fmt.Sprintf("auto account %s without fresh cached quota", acc.ID)
	}
	current, _ := st.CurrentAccount(acc.Provider)
	return quotaRouteReasonAt(st, acc, current, time.Now())
}

func quotaRouteEvidenceAt(st *Store, acc Account, current string, now time.Time) protocol.AccountRouteEvidence {
	evidence := protocol.AccountRouteEvidence{
		AccountID:     acc.ID,
		SecretBackend: routeSecretBackend(acc),
		Score:         quotaRouteScoreAt(st, acc, current, now),
		QuotaState:    protocol.AccountQuotaStateMissing,
		Reason:        quotaRouteReasonAt(st, acc, current, now),
	}
	if q, err := st.LoadQuota(acc.ID); err == nil {
		evidence.CheckedAt = q.CheckedAt
		evidence.PrimaryUsedPercent = usablePercentPtr(q.PrimaryUsedPercent)
		evidence.SecondaryUsedPercent = usablePercentPtr(q.SecondaryUsedPercent)
		evidence.CodeReviewUsedPercent = usablePercentPtr(q.CodeReviewUsedPercent)
		if limiting, dimension, ok := quotaRoutePressure(q); ok {
			evidence.LimitingUsedPercent = &limiting
			evidence.LimitingQuotaDimension = dimension
		}
		if QuotaSnapshotFresh(q, now) {
			evidence.QuotaState = protocol.AccountQuotaStateFresh
			evidence.Fresh = true
		} else {
			evidence.QuotaState = protocol.AccountQuotaStateStale
		}
	}
	return evidence
}

func routeSecretBackend(acc Account) string {
	if acc.SecretRef == "" {
		return ""
	}
	ref, err := secret.ParseRef(acc.SecretRef)
	if err != nil {
		return ""
	}
	return ref.Backend
}

func quotaRouteReasonAt(st *Store, acc Account, current string, now time.Time) string {
	suffix := ""
	if acc.ID == current {
		suffix = "; current account tie-break"
	}
	if st != nil {
		if q, err := st.LoadQuota(acc.ID); err == nil {
			if QuotaSnapshotFresh(q, now) {
				pressure, dimension, _ := quotaRoutePressure(q)
				if dimension == "primary" {
					return fmt.Sprintf("auto account %s primary %.0f%%%s", acc.ID, pressure, suffix)
				}
				return fmt.Sprintf("auto account %s limiting %s %.0f%% (primary %.0f%%, secondary %.0f%%, code_review %.0f%%)%s",
					acc.ID, dimension, pressure, q.PrimaryUsedPercent, q.SecondaryUsedPercent, q.CodeReviewUsedPercent, suffix)
			}
			return fmt.Sprintf("auto account %s without fresh cached quota%s", acc.ID, suffix)
		}
	}
	return fmt.Sprintf("auto account %s without fresh cached quota%s", acc.ID, suffix)
}

func quotaRouteScoreAt(st *Store, acc Account, current string, now time.Time) float64 {
	score := quotaUnknownScore
	if q, err := st.LoadQuota(acc.ID); err == nil && QuotaSnapshotFresh(q, now) {
		score, _, _ = quotaRoutePressure(q)
	}
	if acc.ID == current {
		score -= 0.01
	}
	return score
}

func routeTieBeats(candidate, incumbent Account, current string) bool {
	if candidate.ID == current && incumbent.ID != current {
		return true
	}
	if incumbent.ID == current && candidate.ID != current {
		return false
	}
	return candidate.ID < incumbent.ID
}

func QuotaSnapshotFresh(q QuotaSnapshot, now time.Time) bool {
	if q.CheckedAt <= 0 {
		return false
	}
	if !quotaPercentUsable(q.PrimaryUsedPercent) ||
		!quotaPercentUsable(q.SecondaryUsedPercent) ||
		!quotaPercentUsable(q.CodeReviewUsedPercent) {
		return false
	}
	age := now.Unix() - q.CheckedAt
	return age >= 0 && time.Duration(age)*time.Second <= QuotaRouteCacheTTL
}

func usablePercentPtr(n float64) *float64 {
	if !quotaPercentUsable(n) {
		return nil
	}
	return &n
}

func quotaRoutePressure(q QuotaSnapshot) (float64, string, bool) {
	if !quotaPercentUsable(q.PrimaryUsedPercent) ||
		!quotaPercentUsable(q.SecondaryUsedPercent) ||
		!quotaPercentUsable(q.CodeReviewUsedPercent) {
		return 0, "", false
	}
	pressure := q.PrimaryUsedPercent
	dimension := "primary"
	if q.SecondaryUsedPercent > pressure {
		pressure = q.SecondaryUsedPercent
		dimension = "secondary"
	}
	if q.CodeReviewUsedPercent > pressure {
		pressure = q.CodeReviewUsedPercent
		dimension = "code_review"
	}
	return pressure, dimension, true
}
