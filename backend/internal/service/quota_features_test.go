package service

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

func TestGroupNightRateCrossesMidnight(t *testing.T) {
	g := &Group{PeakRateEnabled: true, PeakStart: "14:00", PeakEnd: "18:00", PeakRateMultiplier: 2,
		NightRateEnabled: true, NightStart: "22:00", NightEnd: "08:00", NightRateMultiplier: 0.5}
	require.NoError(t, ValidateGroupTimeRates(g))
	for _, tc := range []struct {
		hour, minute int
		multiplier   float64
	}{
		{21, 59, 1}, {22, 0, 0.5}, {23, 59, 0.5}, {0, 0, 0.5}, {7, 59, 0.5}, {8, 0, 1}, {14, 0, 2}, {18, 0, 1},
	} {
		now := time.Date(2026, 11, 7, tc.hour, tc.minute, 0, 0, timezone.Location())
		require.Equal(t, tc.multiplier, g.TimeRateMultiplierAt(now))
	}
	g.PeakStart, g.PeakEnd = "07:00", "12:00"
	require.Error(t, ValidateGroupTimeRates(g))
	g.PeakStart = "08:00"
	require.NoError(t, ValidateGroupTimeRates(g))
	g.NightEnd = g.NightStart
	require.Error(t, ValidateGroupTimeRates(g))
	require.Error(t, ValidatePeakRateConfig(true, "22:00", "08:00", math.NaN()))
}

func TestUpstreamBillingProbeNightAndCrossMidnight(t *testing.T) {
	body := []byte(`{"object":"sub2api.key_billing","schema_version":1,"billing_scope":"token","group_rate_multiplier":0.8,"resolved_rate_multiplier":0.8,"peak_rate_enabled":false,"night_rate_enabled":true,"night_start":"22:00","night_end":"08:00","night_rate_multiplier":0.5,"applied_night_multiplier":0.5,"effective_rate_multiplier":0.4,"timezone":"UTC","observed_at":"2026-11-07T23:00:00Z"}`)
	data, err := parseUpstreamBillingProbeResponse(body)
	require.NoError(t, err)
	rate, ok := upstreamBillingRateAt(data, time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC))
	require.True(t, ok)
	require.Equal(t, 0.4, rate)
	rate, ok = upstreamBillingRateAt(data, time.Date(2026, 11, 8, 8, 0, 0, 0, time.UTC))
	require.True(t, ok)
	require.Equal(t, 0.8, rate)
}

func TestCacheReadGroupOverrideNeverStacks(t *testing.T) {
	s := newPanelRateLimitTestService(&panelRateLimitSettingRepo{})
	ctx := context.Background()
	settings := &CacheReadAdjustmentSettings{Enabled: true, Percent: 10, AccountIDs: []int64{7}}
	require.NoError(t, s.SetCacheReadAdjustmentSettings(ctx, settings))
	g := &Group{CacheReadAdjustmentMode: "custom", CacheReadAdjustmentPercent: 25}
	ctx = context.WithValue(ctx, ctxkey.Group, g)
	require.Equal(t, 25, s.CacheReadAdjustmentPercent(ctx, 7))
	require.Equal(t, 25, s.CacheReadAdjustmentPercent(ctx, 8))
	g.CacheReadAdjustmentPercent = 0
	require.Zero(t, s.CacheReadAdjustmentPercent(ctx, 7))
	g.CacheReadAdjustmentMode = "disabled"
	require.Zero(t, s.CacheReadAdjustmentPercent(ctx, 7))
	g.CacheReadAdjustmentMode = "inherit"
	require.Equal(t, 10, s.CacheReadAdjustmentPercent(ctx, 7))
	g.CacheReadAdjustmentMode = "custom"
	g.CacheReadAdjustmentPercent = 25
	settings.Enabled = false
	require.NoError(t, s.SetCacheReadAdjustmentSettings(ctx, settings))
	require.Zero(t, s.CacheReadAdjustmentPercent(ctx, 7))
}

func TestTimedQuotaCardTermAndNoDailyRefill(t *testing.T) {
	start := time.Date(2026, 11, 7, 23, 0, 0, 0, timezone.Location())
	total := 100.0
	plan := &SubscriptionPlan{CardMode: "hours", ValidityHours: 12, TotalLimitUSD: &total}
	from, to, err := normalizedSubscriptionTerm(&AssignSubscriptionInput{}, plan, start)
	require.NoError(t, err)
	require.Equal(t, start, from)
	require.Equal(t, 12*time.Hour, to.Sub(from))
	sub := &UserSubscription{StartsAt: from, ExpiresAt: to, Status: SubscriptionStatusActive, Plan: plan, TotalUsageUSD: 100}
	transition, err := sub.AllowanceWindowTransitionAt(start.Add(2 * time.Hour))
	require.NoError(t, err)
	require.True(t, transition.ResetDaily)
	require.ErrorIs(t, NewSubscriptionService(nil, nil).CheckUsageLimits(context.Background(), sub, 0), ErrTotalLimitExceeded)
	plan.CardMode, plan.ValidityHours, plan.DeadlineAt = "deadline", 0, &to
	_, deadline, err := normalizedSubscriptionTerm(&AssignSubscriptionInput{}, plan, start)
	require.NoError(t, err)
	require.Equal(t, to, deadline)
	_, _, err = normalizedSubscriptionTerm(&AssignSubscriptionInput{}, plan, to)
	require.Error(t, err)
}

type prioritySubscriptionRepo struct {
	UserSubscriptionRepository
	rows map[int64]*UserSubscription
}

func (r *prioritySubscriptionRepo) GetByID(_ context.Context, id int64) (*UserSubscription, error) {
	if sub := r.rows[id]; sub != nil {
		copySub := *sub
		return &copySub, nil
	}
	return nil, ErrSubscriptionNotFound
}

func TestFundingPriorityOrderAndExplicitWalletFallback(t *testing.T) {
	now := time.Now()
	day := timezone.StartOfDay(now)
	total := 100.0
	r := &prioritySubscriptionRepo{rows: map[int64]*UserSubscription{}}
	for _, id := range []int64{1, 2, 3} {
		r.rows[id] = &UserSubscription{ID: id, UserID: 7, StartsAt: now.Add(-time.Hour), ExpiresAt: now.Add(time.Hour), Status: SubscriptionStatusActive,
			DailyWindowStart: &day, WeeklyWindowStart: &now, MonthlyWindowStart: &now, Plan: &SubscriptionPlan{TotalLimitUSD: &total}}
	}
	s := &APIKeyService{userSubRepo: r}
	key := &APIKey{UserID: 7, FundingSource: FundingSourceSubscription, SubscriptionID: &r.rows[1].ID, SubscriptionPriority: []int64{3, 1, 2}}
	resolved, sub, err := s.ResolveGatewayFunding(context.Background(), key)
	require.NoError(t, err)
	require.Equal(t, int64(3), sub.ID)
	require.Equal(t, int64(1), *key.SubscriptionID)
	require.NotSame(t, key, resolved)
	r.rows[3].TotalUsageUSD = 100
	_, sub, err = s.ResolveGatewayFunding(context.Background(), key)
	require.NoError(t, err)
	require.Equal(t, int64(1), sub.ID)
	r.rows[1].TotalReservedUSD = 100
	r.rows[2].ExpiresAt = now.Add(-time.Second)
	_, _, err = s.ResolveGatewayFunding(context.Background(), key)
	require.Error(t, err)
	key.AllowWalletFallback = true
	resolved, sub, err = s.ResolveGatewayFunding(context.Background(), key)
	require.NoError(t, err)
	require.Nil(t, sub)
	require.Equal(t, FundingSourceWallet, resolved.FundingSource)
	require.Equal(t, FundingSourceSubscription, key.FundingSource)
	r.rows[3].UserID = 8
	_, _, err = s.ResolveGatewayFunding(context.Background(), key)
	require.ErrorIs(t, err, ErrSubscriptionBillingInvalid)
}

func TestSelfRevokeEligibilityUsesCommittedCurrentWindow(t *testing.T) {
	now := time.Now()
	day := timezone.StartOfDay(now)
	limit := 10.0
	sub := &UserSubscription{StartsAt: now.Add(-time.Hour), ExpiresAt: now.Add(7 * 24 * time.Hour), Status: SubscriptionStatusActive,
		DailyWindowStart: &day, WeeklyWindowStart: &now, MonthlyWindowStart: &now, DailyUsageUSD: 10, Plan: &SubscriptionPlan{DailyLimitUSD: &limit}}
	require.True(t, sub.CanSelfRevokeAt(now))
	sub.RenewalCount = 1
	require.False(t, sub.CanSelfRevokeAt(now))
	sub.RenewalCount = 0
	sub.DailyReservedUSD = 0.1
	require.False(t, sub.CanSelfRevokeAt(now))
	sub.DailyReservedUSD = 0
	sub.DailyUsageUSD = 9.9
	require.False(t, sub.CanSelfRevokeAt(now))
	sub.DailyUsageUSD = 10
	yesterday := day.AddDate(0, 0, -1)
	sub.DailyWindowStart = &yesterday
	require.False(t, sub.CanSelfRevokeAt(now))
}
