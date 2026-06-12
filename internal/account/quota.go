package account

import (
	"encoding/json"
	"math"
	"time"
)

const maxQuotaRawJSONBytes = 1 << 20

// QuotaFromUsage converts adapter-specific usage payloads into the small
// scheduler-facing cache stored in SQLite. Unknown shapes are preserved as
// RawJSON; known Codex rate-limit shapes fill the common fields.
func QuotaFromUsage(accountID string, usage map[string]any) QuotaSnapshot {
	raw, _ := json.Marshal(usage)
	q := QuotaSnapshot{
		AccountID: accountID,
		Plan:      stringFrom(usage, "planType", "plan_type", "plan"),
		CheckedAt: time.Now().Unix(),
		RawJSON:   string(raw),
	}
	if len(raw) > maxQuotaRawJSONBytes {
		q.RawJSON = ""
	}
	if rl, ok := usage["rateLimits"].(map[string]any); ok {
		readWindow(rl["primary"], &q.PrimaryUsedPercent, &q.PrimaryResetAt)
		readWindow(rl["primaryWindow"], &q.PrimaryUsedPercent, &q.PrimaryResetAt)
		readWindow(rl["primary_window"], &q.PrimaryUsedPercent, &q.PrimaryResetAt)
		readWindow(rl["secondary"], &q.SecondaryUsedPercent, &q.SecondaryResetAt)
		readWindow(rl["secondaryWindow"], &q.SecondaryUsedPercent, &q.SecondaryResetAt)
		readWindow(rl["secondary_window"], &q.SecondaryUsedPercent, &q.SecondaryResetAt)
		readCodeReview(rl["codeReview"], &q.CodeReviewUsedPercent)
		readCodeReview(rl["code_review"], &q.CodeReviewUsedPercent)
	}
	if rl, ok := usage["rate_limit"].(map[string]any); ok {
		readWindow(rl["primary_window"], &q.PrimaryUsedPercent, &q.PrimaryResetAt)
		readWindow(rl["secondary_window"], &q.SecondaryUsedPercent, &q.SecondaryResetAt)
		readCodeReview(rl["code_review_rate_limit"], &q.CodeReviewUsedPercent)
	}
	return q
}

func readWindow(v any, used *float64, reset *string) {
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	if n, ok := numberFrom(m, "usedPercent", "used_percent"); ok {
		*used = conservativePercent(n)
	}
	if s := stringFrom(m, "resetsAt", "resetAt", "reset_at"); s != "" {
		*reset = s
	}
}

func readCodeReview(v any, used *float64) {
	m, ok := v.(map[string]any)
	if !ok {
		return
	}
	if n, ok := numberFrom(m, "usedPercent", "used_percent"); ok {
		*used = conservativePercent(n)
	}
}

func stringFrom(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key].(string); ok {
			return v
		}
	}
	return ""
}

func numberFrom(m map[string]any, keys ...string) (float64, bool) {
	for _, key := range keys {
		switch v := m[key].(type) {
		case float64:
			return v, true
		case int:
			return float64(v), true
		case int64:
			return float64(v), true
		}
	}
	return 0, false
}

func conservativePercent(n float64) float64 {
	if math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 100 {
		return 100
	}
	return n
}

func quotaPercentUsable(n float64) bool {
	return !math.IsNaN(n) && !math.IsInf(n, 0) && n >= 0 && n <= 100
}
