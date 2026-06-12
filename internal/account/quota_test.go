package account

import (
	"math"
	"strings"
	"testing"
	"time"
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

func TestQuotaFromUsageNormalizesOutOfRangePercentsConservatively(t *testing.T) {
	q := QuotaFromUsage("codex-a", map[string]any{
		"rateLimits": map[string]any{
			"primary": map[string]any{
				"usedPercent": -4.0,
			},
			"secondary": map[string]any{
				"usedPercent": 140.0,
			},
			"codeReview": map[string]any{
				"usedPercent": math.NaN(),
			},
		},
	})
	if q.PrimaryUsedPercent != 100 || q.SecondaryUsedPercent != 100 || q.CodeReviewUsedPercent != 100 {
		t.Fatalf("quota percent bounds = %+v", q)
	}
}

func TestQuotaSnapshotFreshRejectsInvalidPrimaryPercent(t *testing.T) {
	now := time.Now()
	for _, percent := range []float64{-0.1, 100.1, math.NaN(), math.Inf(1)} {
		q := QuotaSnapshot{AccountID: "codex-a", PrimaryUsedPercent: percent, CheckedAt: now.Unix()}
		if QuotaSnapshotFresh(q, now) {
			t.Fatalf("percent %v unexpectedly fresh", percent)
		}
	}
	for _, percent := range []float64{0, 100} {
		q := QuotaSnapshot{AccountID: "codex-a", PrimaryUsedPercent: percent, CheckedAt: now.Unix()}
		if !QuotaSnapshotFresh(q, now) {
			t.Fatalf("percent %v unexpectedly not fresh", percent)
		}
	}
}
