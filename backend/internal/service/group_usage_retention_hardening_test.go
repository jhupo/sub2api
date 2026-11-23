//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

type groupUsageRetentionProbe struct {
	DashboardAggregationRepository
	usageCutoff time.Time
	dedupCutoff time.Time
}

func (*groupUsageRetentionProbe) CleanupAggregates(context.Context, time.Time, time.Time) error {
	return nil
}
func (r *groupUsageRetentionProbe) CleanupUsageLogs(_ context.Context, cutoff time.Time) error {
	r.usageCutoff = cutoff
	return nil
}
func (r *groupUsageRetentionProbe) CleanupUsageBillingDedup(_ context.Context, cutoff time.Time) error {
	r.dedupCutoff = cutoff
	return nil
}

func TestGroupUsageRetentionUsesWholeDaysAndProtectsBillingDedup(t *testing.T) {
	previous := timezone.Name()
	require.NoError(t, timezone.Init("Asia/Shanghai"))
	t.Cleanup(func() { require.NoError(t, timezone.Init(previous)) })
	for _, dedupDays := range []int{30, 90, 365} {
		repo := &groupUsageRetentionProbe{}
		svc := NewDashboardAggregationService(repo, nil, &config.Config{DashboardAgg: config.DashboardAggregationConfig{
			Retention: config.DashboardAggregationRetentionConfig{UsageLogsDays: 90, UsageBillingDedupDays: dedupDays},
		}})
		now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
		svc.maybeCleanupRetention(context.Background(), now)
		require.Equal(t, time.Date(2026, 6, 16, 16, 0, 0, 0, time.UTC), repo.usageCutoff)
		require.False(t, repo.dedupCutoff.After(repo.usageCutoff), "billing dedup must cover the entire retained usage window")
	}
}

func TestGroupUsageDayStartHandlesMidnightDSTTransitions(t *testing.T) {
	previous := timezone.Name()
	t.Cleanup(func() { require.NoError(t, timezone.Init(previous)) })
	for _, tc := range []struct{ zone, day string }{
		{zone: "America/Santiago", day: "2026-09-06"},
		{zone: "Asia/Gaza", day: "2020-10-24"},
	} {
		t.Run(tc.zone, func(t *testing.T) {
			require.NoError(t, timezone.Init(tc.zone))
			start, err := ParseGroupUsageDate(tc.day)
			require.NoError(t, err)
			require.Equal(t, tc.day, GroupUsageDate(start))
			require.NotEqual(t, tc.day, GroupUsageDate(start.Add(-time.Nanosecond)))
			require.True(t, GroupUsageTodayStart(start.Add(90*time.Minute)).Equal(start))
		})
	}
}
