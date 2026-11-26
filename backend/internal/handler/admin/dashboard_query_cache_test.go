package admin

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUseLiveDashboardTrendCache(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	start, end := now.Add(-24*time.Hour), now.Add(12*time.Hour)
	f := false
	requestType := int16(1)
	billingType := int8(1)
	tests := []struct {
		name    string
		filters dashboardTrendCacheKey
	}{
		{"user", dashboardTrendCacheKey{UserID: 1}},
		{"key", dashboardTrendCacheKey{APIKeyID: 1}},
		{"account", dashboardTrendCacheKey{AccountID: 1}},
		{"group", dashboardTrendCacheKey{GroupID: 1}},
		{"model", dashboardTrendCacheKey{Model: "test-model"}},
		{"request type", dashboardTrendCacheKey{RequestType: &requestType}},
		{"stream false", dashboardTrendCacheKey{Stream: &f}},
		{"native compaction false", dashboardTrendCacheKey{NativeCompactionV2: &f}},
		{"billing type", dashboardTrendCacheKey{BillingType: &billingType}},
		{"mismatch false", dashboardTrendCacheKey{UpstreamModelMismatch: &f}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.filters.Granularity = "hour"
			require.False(t, useLiveDashboardTrendCache(true, tt.filters, start, end, now), "filtered reports must keep their longer scan cache")
		})
	}
	for _, granularity := range []string{"hour", "day"} {
		filters := dashboardTrendCacheKey{Granularity: granularity}
		require.True(t, useLiveDashboardTrendCache(true, filters, start, end, now))
		require.False(t, useLiveDashboardTrendCache(false, filters, start, end, now))
		require.False(t, useLiveDashboardTrendCache(true, filters, start, now, now))
		require.False(t, useLiveDashboardTrendCache(true, filters, end, end.Add(24*time.Hour), now))
	}
	require.False(t, useLiveDashboardTrendCache(true, dashboardTrendCacheKey{Granularity: "minute"}, start, end, now))
}

type liveTrendAggregationRepoStub struct {
	service.DashboardAggregationRepository
}

func TestDashboardLiveTrendCacheDoesNotInheritOldReportCache(t *testing.T) {
	oldLiveCache, oldReportCache := dashboardLiveTrendCache, dashboardTrendCache
	dashboardLiveTrendCache = newSnapshotCache(time.Minute)
	dashboardTrendCache = newSnapshotCache(5 * time.Minute)
	t.Cleanup(func() {
		dashboardLiveTrendCache, dashboardTrendCache = oldLiveCache, oldReportCache
	})
	repo := &dashboardUsageRepoCacheProbe{}
	svc := service.NewDashboardService(repo, &liveTrendAggregationRepoStub{}, nil, nil)
	handler := NewDashboardHandler(svc, nil)
	now := time.Now()
	start, end := now.Add(-time.Hour), now.Add(time.Hour)
	key := mustMarshalDashboardCacheKey(dashboardTrendCacheKey{
		StartTime: start.UTC().Format(time.RFC3339), EndTime: end.UTC().Format(time.RFC3339), Granularity: "hour",
	})
	dashboardTrendCache.Set(key, []usagestats.TrendDataPoint{{TotalTokens: 999}})
	read := func() (bool, error) {
		trend, hit, err := handler.getUsageTrendCached(context.Background(), start, end, "hour", 0, 0, 0, 0, "", nil, nil, nil, nil, nil)
		if err == nil {
			require.Equal(t, int64(2), trend[0].TotalTokens)
		}
		return hit, err
	}
	hit, err := read()
	require.NoError(t, err)
	require.False(t, hit)
	hit, err = read()
	require.NoError(t, err)
	require.True(t, hit)
	require.Equal(t, int32(1), repo.trendCalls.Load(), "repeated panel reads must share one aggregate query")

	dashboardLiveTrendCache.mu.Lock()
	entry := dashboardLiveTrendCache.items[key]
	entry.ExpiresAt = time.Now().Add(-time.Second)
	dashboardLiveTrendCache.items[key] = entry
	dashboardLiveTrendCache.mu.Unlock()
	hit, err = read()
	require.NoError(t, err)
	require.False(t, hit)
	require.Equal(t, int32(2), repo.trendCalls.Load(), "the live trend must reload after its own TTL instead of reading the old five-minute report")
}
