package account

import (
	"strings"
	"testing"
)

func TestQuotaFromUsageParsesCodexShapes(t *testing.T) {
	q := QuotaFromUsage("codex-a", map[string]any{
		"planType": "pro",
		"rateLimits": map[string]any{
			"primary": map[string]any{
				"usedPercent": 42.5,
				"resetsAt":    "2026-06-11T20:00:00Z",
			},
			"secondary_window": map[string]any{
				"used_percent": 7.0,
				"reset_at":     "2026-06-12T00:00:00Z",
			},
			"code_review": map[string]any{
				"used_percent": 3.0,
			},
		},
	})
	if q.AccountID != "codex-a" || q.Plan != "pro" || q.PrimaryUsedPercent != 42.5 {
		t.Fatalf("quota = %+v", q)
	}
	if q.SecondaryUsedPercent != 7 || q.CodeReviewUsedPercent != 3 || q.RawJSON == "" {
		t.Fatalf("quota = %+v", q)
	}
}

func TestQuotaFromUsageDropsOversizedRawJSON(t *testing.T) {
	q := QuotaFromUsage("codex-a", map[string]any{
		"planType": "pro",
		"rateLimits": map[string]any{
			"primary": map[string]any{
				"usedPercent": 42.5,
			},
		},
		"debug": strings.Repeat("x", maxQuotaRawJSONBytes+1),
	})
	if q.Plan != "pro" || q.PrimaryUsedPercent != 42.5 {
		t.Fatalf("quota fields were not preserved: %+v", q)
	}
	if q.RawJSON != "" {
		t.Fatalf("RawJSON length = %d, want dropped", len(q.RawJSON))
	}
}
