package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func openAIResetTestScheduler(reset float64) *defaultOpenAIAccountScheduler {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights = config.GatewayOpenAIWSSchedulerScoreWeights{
		Priority:      1.0,
		Load:          1.0,
		Queue:         0.7,
		ErrorRate:     0.8,
		TTFT:          0.5,
		Reset:         reset,
		QuotaHeadroom: 0,
	}
	return &defaultOpenAIAccountScheduler{service: &OpenAIGatewayService{cfg: cfg}}
}

func openAIQuotaHeadroomTestScheduler(quotaHeadroom float64) *defaultOpenAIAccountScheduler {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights = config.GatewayOpenAIWSSchedulerScoreWeights{
		QuotaHeadroom: quotaHeadroom,
	}
	return &defaultOpenAIAccountScheduler{service: &OpenAIGatewayService{cfg: cfg}}
}

func openAIPlanScores(plan openAIAccountLoadPlan) map[int64]float64 {
	scores := make(map[int64]float64, len(plan.candidates))
	for _, c := range plan.candidates {
		scores[c.account.ID] = c.score
	}
	return scores
}

// Reset 权重 > 0 时，规范 Codex 5h 窗口最早重置的账号应获得更高分。
func TestBuildOpenAIAccountLoadPlan_ResetWeightPrefersSoonestReset(t *testing.T) {
	now := time.Now()
	soon := now.Add(1 * time.Hour)
	later := now.Add(20 * time.Hour)
	filtered := []*Account{
		{ID: 1, Priority: 0, Extra: map[string]any{"codex_5h_reset_at": later.Format(time.RFC3339)}},
		{ID: 2, Priority: 0, Extra: map[string]any{"codex_5h_reset_at": soon.Format(time.RFC3339)}},
	}
	sched := openAIResetTestScheduler(5.0)

	plan := sched.buildOpenAIAccountLoadPlan(context.Background(), OpenAIAccountScheduleRequest{}, filtered, map[int64]*AccountLoadInfo{})
	scores := openAIPlanScores(plan)
	require.Greater(t, scores[2], scores[1], "重置时间最早的账号（ID=2）得分更高")
}

// Reset 权重为 0（默认）时，窗口重置时间不应影响打分，保持原有行为。
func TestBuildOpenAIAccountLoadPlan_ResetWeightZeroNoEffect(t *testing.T) {
	now := time.Now()
	soon := now.Add(1 * time.Hour)
	later := now.Add(20 * time.Hour)
	filtered := []*Account{
		{ID: 1, Priority: 0, SessionWindowEnd: &later},
		{ID: 2, Priority: 0, SessionWindowEnd: &soon},
	}
	sched := openAIResetTestScheduler(0.0)

	plan := sched.buildOpenAIAccountLoadPlan(context.Background(), OpenAIAccountScheduleRequest{}, filtered, map[int64]*AccountLoadInfo{})
	scores := openAIPlanScores(plan)
	require.Equal(t, scores[1], scores[2], "Reset 权重为 0 时两账号得分相同")
}

// 无活跃窗口的账号 reset 因子为 0，应低于拥有未来窗口的账号。
func TestBuildOpenAIAccountLoadPlan_ResetWeightIgnoresNilWindow(t *testing.T) {
	now := time.Now()
	soon := now.Add(2 * time.Hour)
	filtered := []*Account{
		{ID: 1, Priority: 0},
		{ID: 2, Priority: 0, Extra: map[string]any{"codex_5h_reset_at": soon.Format(time.RFC3339)}},
	}
	sched := openAIResetTestScheduler(5.0)

	plan := sched.buildOpenAIAccountLoadPlan(context.Background(), OpenAIAccountScheduleRequest{}, filtered, map[int64]*AccountLoadInfo{})
	scores := openAIPlanScores(plan)
	require.Greater(t, scores[2], scores[1], "拥有活跃窗口的账号得分高于无窗口账号")
}

func TestOpenAIQuotaHeadroomFactor_SevenDayUsedPercent(t *testing.T) {
	now := time.Date(2026, 3, 11, 10, 0, 0, 0, time.UTC)
	account := &Account{
		Extra: map[string]any{
			"codex_7d_used_percent":  20.0,
			"codex_7d_reset_at":      now.Add(24 * time.Hour).Format(time.RFC3339),
			"codex_usage_updated_at": now.Add(-time.Minute).Format(time.RFC3339),
		},
	}

	require.InDelta(t, 0.8, openAIQuotaHeadroomFactor(account, now), 0.0001)
}

func TestOpenAIQuotaHeadroomFactor_SevenDayMissingIsNeutral(t *testing.T) {
	now := time.Date(2026, 3, 11, 10, 0, 0, 0, time.UTC)
	account := &Account{
		Extra: map[string]any{
			"codex_usage_updated_at": now.Add(-time.Minute).Format(time.RFC3339),
		},
	}

	require.Equal(t, openAIQuotaHeadroomNeutralFactor, openAIQuotaHeadroomFactor(account, now))
}

func TestOpenAIQuotaHeadroomFactor_SevenDayResetExpiredIsNeutral(t *testing.T) {
	now := time.Date(2026, 3, 11, 10, 0, 0, 0, time.UTC)
	account := &Account{
		Extra: map[string]any{
			"codex_7d_used_percent":  20.0,
			"codex_7d_reset_at":      now.Add(-time.Minute).Format(time.RFC3339),
			"codex_usage_updated_at": now.Add(-time.Minute).Format(time.RFC3339),
		},
	}

	require.Equal(t, openAIQuotaHeadroomNeutralFactor, openAIQuotaHeadroomFactor(account, now))
}

func TestOpenAIQuotaHeadroomFactor_FiveHourLowHeadroomDiscountsSevenDay(t *testing.T) {
	now := time.Date(2026, 3, 11, 10, 0, 0, 0, time.UTC)
	account := &Account{
		Extra: map[string]any{
			"codex_7d_used_percent":  20.0,
			"codex_7d_reset_at":      now.Add(24 * time.Hour).Format(time.RFC3339),
			"codex_5h_used_percent":  95.0,
			"codex_5h_reset_at":      now.Add(time.Hour).Format(time.RFC3339),
			"codex_usage_updated_at": now.Add(-time.Minute).Format(time.RFC3339),
		},
	}

	require.InDelta(t, 0.4, openAIQuotaHeadroomFactor(account, now), 0.0001)
}

func TestBuildOpenAIAccountLoadPlan_QuotaHeadroomPrefersHigher7dRemaining(t *testing.T) {
	now := time.Now()
	filtered := []*Account{
		{
			ID:       1,
			Priority: 0,
			Extra: map[string]any{
				"codex_7d_used_percent":  80.0,
				"codex_7d_reset_at":      now.Add(24 * time.Hour).Format(time.RFC3339),
				"codex_usage_updated_at": now.Add(-time.Minute).Format(time.RFC3339),
			},
		},
		{
			ID:       2,
			Priority: 0,
			Extra: map[string]any{
				"codex_7d_used_percent":  20.0,
				"codex_7d_reset_at":      now.Add(24 * time.Hour).Format(time.RFC3339),
				"codex_usage_updated_at": now.Add(-time.Minute).Format(time.RFC3339),
			},
		},
	}
	sched := openAIQuotaHeadroomTestScheduler(1.0)

	plan := sched.buildOpenAIAccountLoadPlan(context.Background(), OpenAIAccountScheduleRequest{}, filtered, map[int64]*AccountLoadInfo{})
	scores := openAIPlanScores(plan)
	require.Greater(t, scores[2], scores[1], "7d 剩余额度更高的账号得分应更高")
}

func TestBuildOpenAIAccountLoadPlan_QuotaHeadroomZeroNoEffect(t *testing.T) {
	now := time.Now()
	filtered := []*Account{
		{
			ID:       1,
			Priority: 0,
			Extra: map[string]any{
				"codex_7d_used_percent":  80.0,
				"codex_7d_reset_at":      now.Add(24 * time.Hour).Format(time.RFC3339),
				"codex_usage_updated_at": now.Add(-time.Minute).Format(time.RFC3339),
			},
		},
		{
			ID:       2,
			Priority: 0,
			Extra: map[string]any{
				"codex_7d_used_percent":  20.0,
				"codex_7d_reset_at":      now.Add(24 * time.Hour).Format(time.RFC3339),
				"codex_usage_updated_at": now.Add(-time.Minute).Format(time.RFC3339),
			},
		},
	}
	sched := openAIResetTestScheduler(0)

	plan := sched.buildOpenAIAccountLoadPlan(context.Background(), OpenAIAccountScheduleRequest{}, filtered, map[int64]*AccountLoadInfo{})
	scores := openAIPlanScores(plan)
	require.Equal(t, scores[1], scores[2], "quota_headroom 权重为 0 时不应影响打分")
}

func TestOpenAICodexWindowResetAtRelativeCountdownRequiresSnapshotTime(t *testing.T) {
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	resetAt, ok := openAICodexWindowResetAt(map[string]any{
		"codex_5h_reset_after_seconds": 3600,
		"codex_usage_updated_at":       now.Add(-30 * time.Minute).Format(time.RFC3339),
	}, "5h")
	require.True(t, ok)
	require.Equal(t, now.Add(30*time.Minute), resetAt)

	_, ok = openAICodexWindowResetAt(map[string]any{
		"codex_5h_reset_after_seconds": 3600,
	}, "5h")
	require.False(t, ok)
}

func TestOpenAISchedulingResetWindowEndUsesCanonicalCodexWindow(t *testing.T) {
	now := time.Now()
	end := now.Add(time.Hour)
	account := &Account{Extra: map[string]any{
		"codex_5h_reset_at": end.Format(time.RFC3339),
	}}
	got, ok := openAISchedulingResetWindowEnd(account, now)
	require.True(t, ok)
	require.WithinDuration(t, end, got, time.Second)

	sessionEnd := now.Add(2 * time.Hour)
	account.SessionWindowEnd = &sessionEnd
	delete(account.Extra, "codex_5h_reset_at")
	_, ok = openAISchedulingResetWindowEnd(account, now)
	require.False(t, ok)
}
