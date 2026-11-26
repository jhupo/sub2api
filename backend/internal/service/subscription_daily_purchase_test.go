package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

type dailyPurchaseRepo struct {
	userSubRepoNoop
	rows []*UserSubscription
}

func (r *dailyPurchaseRepo) Create(_ context.Context, sub *UserSubscription) error {
	sub.ID = int64(len(r.rows) + 1)
	r.rows = append(r.rows, sub)
	return nil
}

func (r *dailyPurchaseRepo) GetByUserIDAndPlanVersionID(context.Context, int64, int64) (*UserSubscription, error) {
	if len(r.rows) == 0 {
		return nil, ErrSubscriptionNotFound
	}
	return r.rows[0], nil
}

func (r *dailyPurchaseRepo) Update(context.Context, *UserSubscription) error { return nil }

func TestDailyCardSecondPurchasePreservesFirstAllowanceAcrossMidnight(t *testing.T) {
	ctx := context.Background()
	base := timezone.StartOfDay(time.Date(2026, 8, 6, 12, 0, 0, 0, timezone.Location()))
	now := base.Add(20 * time.Hour)
	limit := 100.0
	plan := &SubscriptionPlan{ID: 5, PublishedVersionID: 7, ValidityDays: 1, ValidityUnit: "days", DailyLimitUSD: &limit}
	repo := &dailyPurchaseRepo{}
	svc := NewSubscriptionService(repo, nil)
	svc.now = func() time.Time { return now }
	input := &AssignSubscriptionInput{UserID: 10, PlanID: plan.ID, PlanVersion: plan}

	first, extended, err := svc.AssignOrExtendSubscription(ctx, input)
	require.NoError(t, err)
	require.False(t, extended)
	first.DailyWindowStart = &base
	first.DailyUsageUSD = 50
	firstExpiry := first.ExpiresAt

	now = now.Add(time.Hour)
	second, extended, err := svc.AssignOrExtendSubscription(ctx, input)
	require.NoError(t, err)
	require.False(t, extended)
	require.NotEqual(t, first.ID, second.ID)
	require.True(t, first.IsQuotaCard)
	require.True(t, second.IsQuotaCard)
	require.Equal(t, firstExpiry, first.ExpiresAt)
	require.Equal(t, now.AddDate(0, 0, 1), second.ExpiresAt)
	second.DailyWindowStart = &base

	now = base.AddDate(0, 0, 1).Add(time.Minute)
	require.NoError(t, svc.CheckAndResetWindows(ctx, first))
	require.NoError(t, svc.CheckAndResetWindows(ctx, second))
	require.Equal(t, 50.0, first.DailyUsageUSD)
	require.Zero(t, second.DailyUsageUSD)
	require.Equal(t, 50.0, *first.Plan.DailyLimitUSD-first.DailyUsageUSD)
	require.Equal(t, 100.0, *second.Plan.DailyLimitUSD-second.DailyUsageUSD)
}

func TestMonthlySubscriptionStillRenewsExistingEntitlement(t *testing.T) {
	now := time.Date(2026, 8, 6, 20, 0, 0, 0, time.UTC)
	plan := &SubscriptionPlan{ID: 5, PublishedVersionID: 7, ValidityDays: 1, ValidityUnit: "months"}
	repo := &dailyPurchaseRepo{}
	svc := NewSubscriptionService(repo, nil)
	svc.now = func() time.Time { return now }
	input := &AssignSubscriptionInput{UserID: 10, PlanID: plan.ID, PlanVersion: plan}
	first, _, err := svc.AssignOrExtendSubscription(context.Background(), input)
	require.NoError(t, err)
	first.DailyUsageUSD = 50
	firstExpiry := first.ExpiresAt
	second, extended, err := svc.AssignOrExtendSubscription(context.Background(), input)
	require.NoError(t, err)
	require.True(t, extended)
	require.Equal(t, first.ID, second.ID)
	require.False(t, second.IsQuotaCard)
	require.Equal(t, firstExpiry.AddDate(0, 0, 30), second.ExpiresAt)
	require.Equal(t, 50.0, second.DailyUsageUSD)
}

func TestLegacyMergedDailyCardsResetAfterActivationDayInsteadOfMidnight(t *testing.T) {
	base := timezone.StartOfDay(time.Date(2026, 8, 6, 12, 0, 0, 0, timezone.Location()))
	start := base.Add(20 * time.Hour)
	now := base.AddDate(0, 0, 1).Add(time.Minute)
	limit := 100.0
	sub := &UserSubscription{
		StartsAt: start, ExpiresAt: start.AddDate(0, 0, 2), RenewalCount: 1,
		DailyWindowStart: &base, DailyUsageUSD: 50,
		Plan: &SubscriptionPlan{ValidityDays: 1, ValidityUnit: "days", DailyLimitUSD: &limit},
	}
	repo := &dailyMidnightResetRepo{}
	svc := NewSubscriptionService(repo, nil)
	svc.now = func() time.Time { return now }
	require.NoError(t, svc.CheckAndResetWindows(context.Background(), sub))
	require.False(t, repo.resetCalled)
	require.Equal(t, 50.0, sub.DailyUsageUSD)
	require.Equal(t, start.Add(24*time.Hour), *sub.DailyResetTime())
	now = start.Add(24*time.Hour + time.Minute)
	require.NoError(t, svc.CheckAndResetWindows(context.Background(), sub))
	require.True(t, repo.resetCalled)
	require.Equal(t, start.Add(24*time.Hour), repo.newWindowStart)
	require.Zero(t, sub.DailyUsageUSD)

	// If an older version already granted day two at midnight, preserve its
	// usage and do not grant day two a second time at the aligned boundary.
	oldMidnight := base.AddDate(0, 0, 1)
	sub.DailyWindowStart = &oldMidnight
	sub.DailyUsageUSD = 50
	repo.resetCalled = false
	require.Equal(t, sub.ExpiresAt, *sub.DailyResetTime())
	require.NoError(t, svc.CheckAndResetWindows(context.Background(), sub))
	require.False(t, repo.resetCalled)
	require.Equal(t, 50.0, sub.DailyUsageUSD)
}
