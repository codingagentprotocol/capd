package account

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/codingagentprotocol/capd/internal/account/secret"
	"github.com/codingagentprotocol/capd/pkg/protocol"
)

const (
	// QuotaRouteCacheTTL bounds how long an auto-route decision trusts a
	// cached quota value. Older rows are treated like missing quota so a stale
	// low-usage account does not dominate routing indefinitely.
	QuotaRouteCacheTTL        = 30 * time.Minute
	RouteRecentFailureTTL     = 24 * time.Hour
	quotaUnknownScore         = 75.0
	currentTieBreak           = 0.01
	recentFailureScorePenalty = 10.0
)

const (
	TaskClassDefault     = "default"
	TaskClassReview      = "review"
	TaskClassLongRunning = "long-running"
	TaskClassInteractive = "interactive"
	TaskClassVision      = "vision"
)

// RoutePolicy is the account-aware scheduler policy. It is intentionally small
// while routing is still deterministic, but gives future model/task-aware
// schedulers one place to tune freshness, unknown-risk, and stability weights.
type RoutePolicy struct {
	FreshTTL               time.Duration
	UnknownScore           float64
	CurrentAccountTieBreak float64
	RecentFailureTTL       time.Duration
	RecentFailurePenalty   float64
	TaskClass              string
}

// DefaultQuotaRoutePolicy is conservative: stale or missing quota receives a
// moderately high unknown score, and the current account gets only a tiny exact
// tie-break to avoid needless runtime churn.
var DefaultQuotaRoutePolicy = RoutePolicy{
	FreshTTL:               QuotaRouteCacheTTL,
	UnknownScore:           quotaUnknownScore,
	CurrentAccountTieBreak: currentTieBreak,
	RecentFailureTTL:       RouteRecentFailureTTL,
	RecentFailurePenalty:   recentFailureScorePenalty,
}

func DefaultRoutePolicyEvidence() protocol.AccountRoutePolicy {
	return DefaultQuotaRoutePolicy.EvidenceSummary()
}

func RoutePolicyForTaskClass(taskClass string) RoutePolicy {
	policy := DefaultQuotaRoutePolicy
	policy.TaskClass = NormalizeRouteTaskClass(taskClass)
	return policy
}

func NormalizeRouteTaskClass(taskClass string) string {
	switch strings.ToLower(strings.TrimSpace(taskClass)) {
	case "", TaskClassDefault:
		return ""
	case "code-review", "pr-review", "review":
		return TaskClassReview
	case "long", "long-running", "long_task", "long-task":
		return TaskClassLongRunning
	case "interactive", "chat", "quick":
		return TaskClassInteractive
	case "image", "images", "vision":
		return TaskClassVision
	default:
		return ""
	}
}

func InferRouteTaskClass(prompt string, capabilities protocol.AgentCapabilities, attachments []protocol.Attachment) string {
	if capabilities.Review {
		return TaskClassReview
	}
	if capabilities.Images || len(attachments) > 0 {
		return TaskClassVision
	}
	text := strings.ToLower(prompt)
	for _, token := range []string{"code review", "review this", "review", "pull request", "pr ", "diff"} {
		if strings.Contains(text, token) {
			return TaskClassReview
		}
	}
	for _, token := range []string{"long task", "long-running", "deep", "thorough", "全面", "深度", "长任务", "完整测试", "性能", "安全"} {
		if strings.Contains(text, token) {
			return TaskClassLongRunning
		}
	}
	for _, token := range []string{"today", "weather", "quick", "status", "现在", "今天", "状态"} {
		if strings.Contains(text, token) {
			return TaskClassInteractive
		}
	}
	return ""
}

func (p RoutePolicy) EvidenceSummary() protocol.AccountRoutePolicy {
	p = p.normalized()
	return protocol.AccountRoutePolicy{
		Name:                    "conservative-quota-pressure",
		Scoring:                 "lowest fresh limiting quota pressure plus recent-failure health penalty; stale or missing quota uses unknownScore; current account receives a small tie-break",
		TaskClass:               p.TaskClass,
		TaskClassScoring:        routeTaskClassScoring(p.TaskClass),
		QuotaWindows:            []string{"primary", "secondary", "code_review"},
		FreshTTLSeconds:         int64(p.FreshTTL / time.Second),
		UnknownScore:            p.UnknownScore,
		CurrentAccountTieBreak:  p.CurrentAccountTieBreak,
		RecentFailurePenalty:    p.RecentFailurePenalty,
		RecentFailureTTLSeconds: int64(p.RecentFailureTTL / time.Second),
	}
}

// SelectQuotaRouteAccount picks the provider account with the lowest routing
// score. Fresh quota uses the highest pressure across known quota windows;
// missing or stale quota is assigned a conservative unknown score, and the
// current account wins exact ties to avoid needless runtime churn.
func SelectQuotaRouteAccount(st *Store, provider string) (Account, error) {
	return SelectQuotaRouteAccountWithPolicy(st, provider, DefaultQuotaRoutePolicy)
}

// SelectQuotaRouteAccountWithPolicy is the policy-injection seam for tests and
// future task/model-aware schedulers.
func SelectQuotaRouteAccountWithPolicy(st *Store, provider string, policy RoutePolicy) (Account, error) {
	if st == nil {
		return Account{}, fmt.Errorf("account store is required")
	}
	policy = policy.normalized()
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
	bestScore := quotaRouteScoreAt(st, best, current, now, policy)
	for _, acc := range accounts[1:] {
		score := quotaRouteScoreAt(st, acc, current, now, policy)
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
	return DefaultQuotaRoutePolicy.Score(st, acc, current, time.Now())
}

func QuotaRouteScoreWithPolicy(st *Store, acc Account, current string, policy RoutePolicy) float64 {
	return policy.Score(st, acc, current, time.Now())
}

// QuotaRouteEvidence returns the protocol-safe route evidence for an account.
// It is the shared source for CLI and JSON-RPC routing responses so score,
// quota freshness, and state labels cannot drift between surfaces.
func QuotaRouteEvidence(st *Store, acc Account) protocol.AccountRouteEvidence {
	return QuotaRouteEvidenceWithPolicy(st, acc, DefaultQuotaRoutePolicy)
}

func QuotaRouteEvidenceWithPolicy(st *Store, acc Account, policy RoutePolicy) protocol.AccountRouteEvidence {
	if st == nil {
		policy = policy.normalized()
		return protocol.AccountRouteEvidence{
			AccountID:     acc.ID,
			SecretBackend: routeSecretBackend(acc),
			TaskClass:     policy.TaskClass,
			Score:         policy.UnknownScore,
			QuotaState:    protocol.AccountQuotaStateMissing,
			Reason:        "missing cached quota",
		}
	}
	current, _ := st.CurrentAccount(acc.Provider)
	return policy.Evidence(st, acc, current, time.Now())
}

// QuotaRouteCandidates returns every provider account with the same
// protocol-safe evidence used by auto routing, sorted in the order the router
// would consider them: lowest score first, then the stable tie-breaker.
func QuotaRouteCandidates(st *Store, provider string) ([]protocol.AccountRouteEvidence, error) {
	return QuotaRouteCandidatesWithPolicy(st, provider, DefaultQuotaRoutePolicy)
}

func QuotaRouteCandidatesWithPolicy(st *Store, provider string, policy RoutePolicy) ([]protocol.AccountRouteEvidence, error) {
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
	policy = policy.normalized()
	sort.Slice(accounts, func(i, j int) bool {
		leftScore := quotaRouteScoreAt(st, accounts[i], current, now, policy)
		rightScore := quotaRouteScoreAt(st, accounts[j], current, now, policy)
		if leftScore != rightScore {
			return leftScore < rightScore
		}
		return routeTieBeats(accounts[i], accounts[j], current)
	})
	candidates := make([]protocol.AccountRouteEvidence, 0, len(accounts))
	for _, acc := range accounts {
		candidates = append(candidates, policy.Evidence(st, acc, current, now))
	}
	return candidates, nil
}

// QuotaRouteReason gives a short human-readable explanation for auto account
// routing. It intentionally mirrors QuotaRouteEvidence's freshness semantics.
func QuotaRouteReason(st *Store, acc Account) string {
	return QuotaRouteReasonWithPolicy(st, acc, DefaultQuotaRoutePolicy)
}

func QuotaRouteReasonWithPolicy(st *Store, acc Account, policy RoutePolicy) string {
	if st == nil {
		return fmt.Sprintf("auto account %s without fresh cached quota", acc.ID)
	}
	current, _ := st.CurrentAccount(acc.Provider)
	return policy.Reason(st, acc, current, time.Now())
}

func (p RoutePolicy) Evidence(st *Store, acc Account, current string, now time.Time) protocol.AccountRouteEvidence {
	p = p.normalized()
	evidence := protocol.AccountRouteEvidence{
		AccountID:     acc.ID,
		SecretBackend: routeSecretBackend(acc),
		TaskClass:     p.TaskClass,
		Score:         p.Score(st, acc, current, now),
		QuotaState:    protocol.AccountQuotaStateMissing,
		Reason:        p.Reason(st, acc, current, now),
	}
	if health, penalty := p.healthPenalty(st, acc, now); penalty > 0 {
		evidence.RecentFailures = health.RecentFailures
		evidence.LastFailureAt = health.LastFailureAt
		evidence.HealthPenalty = penalty
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
		if p.QuotaSnapshotFresh(q, now) {
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

func (p RoutePolicy) Reason(st *Store, acc Account, current string, now time.Time) string {
	p = p.normalized()
	suffixes := []string{}
	if acc.ID == current {
		suffixes = append(suffixes, "current account tie-break")
	}
	if health, penalty := p.healthPenalty(st, acc, now); penalty > 0 {
		suffixes = append(suffixes, fmt.Sprintf("recent failures %d penalty +%.0f", health.RecentFailures, penalty))
	}
	suffix := routeReasonSuffix(suffixes)
	if st != nil {
		if q, err := st.LoadQuota(acc.ID); err == nil {
			if p.QuotaSnapshotFresh(q, now) {
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

func (p RoutePolicy) Score(st *Store, acc Account, current string, now time.Time) float64 {
	p = p.normalized()
	return quotaRouteScoreAt(st, acc, current, now, p)
}

func quotaRouteScoreAt(st *Store, acc Account, current string, now time.Time, policy RoutePolicy) float64 {
	score := policy.UnknownScore
	if q, err := st.LoadQuota(acc.ID); err == nil && policy.QuotaSnapshotFresh(q, now) {
		score, _, _ = quotaRoutePressure(q)
		if policy.TaskClass == TaskClassReview && quotaPercentUsable(q.CodeReviewUsedPercent) && q.CodeReviewUsedPercent+5 > score {
			score = q.CodeReviewUsedPercent + 5
		}
	} else {
		score += routeTaskClassUnknownPenalty(policy.TaskClass)
	}
	_, healthPenalty := policy.healthPenalty(st, acc, now)
	score += healthPenalty
	if acc.ID == current {
		score -= policy.CurrentAccountTieBreak
	}
	return score
}

func routeReasonSuffix(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	return "; " + strings.Join(parts, "; ")
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
	return DefaultQuotaRoutePolicy.QuotaSnapshotFresh(q, now)
}

func (p RoutePolicy) QuotaSnapshotFresh(q QuotaSnapshot, now time.Time) bool {
	p = p.normalized()
	if q.CheckedAt <= 0 {
		return false
	}
	if !quotaPercentUsable(q.PrimaryUsedPercent) ||
		!quotaPercentUsable(q.SecondaryUsedPercent) ||
		!quotaPercentUsable(q.CodeReviewUsedPercent) {
		return false
	}
	age := now.Unix() - q.CheckedAt
	return age >= 0 && time.Duration(age)*time.Second <= p.FreshTTL
}

func (p RoutePolicy) normalized() RoutePolicy {
	if p.FreshTTL <= 0 {
		p.FreshTTL = QuotaRouteCacheTTL
	}
	if p.UnknownScore <= 0 {
		p.UnknownScore = quotaUnknownScore
	}
	if p.CurrentAccountTieBreak <= 0 {
		p.CurrentAccountTieBreak = currentTieBreak
	}
	if p.RecentFailureTTL <= 0 {
		p.RecentFailureTTL = RouteRecentFailureTTL
	}
	if p.RecentFailurePenalty <= 0 {
		p.RecentFailurePenalty = recentFailureScorePenalty
	}
	p.TaskClass = NormalizeRouteTaskClass(p.TaskClass)
	return p
}

func (p RoutePolicy) healthPenalty(st *Store, acc Account, now time.Time) (AccountHealth, float64) {
	if st == nil {
		return AccountHealth{AccountID: acc.ID}, 0
	}
	health, err := st.LoadAccountHealth(acc.ID)
	if err != nil || health.RecentFailures <= 0 || health.LastFailureAt <= 0 {
		return AccountHealth{AccountID: acc.ID}, 0
	}
	age := now.Unix() - health.LastFailureAt
	if age < 0 || time.Duration(age)*time.Second > p.RecentFailureTTL {
		return health, 0
	}
	penalty := float64(health.RecentFailures) * p.RecentFailurePenalty
	if p.TaskClass == TaskClassInteractive {
		penalty *= 2
	}
	return health, penalty
}

func routeTaskClassUnknownPenalty(taskClass string) float64 {
	switch NormalizeRouteTaskClass(taskClass) {
	case TaskClassReview, TaskClassLongRunning:
		return 5
	default:
		return 0
	}
}

func routeTaskClassScoring(taskClass string) string {
	switch NormalizeRouteTaskClass(taskClass) {
	case TaskClassReview:
		return "review tasks add pressure to accounts with high code_review quota usage and penalize unknown quota"
	case TaskClassLongRunning:
		return "long-running tasks penalize stale or missing quota to prefer accounts with fresh evidence"
	case TaskClassInteractive:
		return "interactive tasks double recent-failure penalty to prefer stable accounts"
	case TaskClassVision:
		return "vision tasks keep conservative quota pressure while exposing task intent"
	default:
		return ""
	}
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
