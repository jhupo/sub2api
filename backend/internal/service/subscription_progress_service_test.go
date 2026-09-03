package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

type subscriptionProgressRepoStub struct {
	userSubRepoNoop
	sub  *UserSubscription
	rows []UserSubscription
}

func (r *subscriptionProgressRepoStub) GetByID(context.Context, int64) (*UserSubscription, error) {
	if r.sub == nil {
		return nil, ErrSubscriptionNotFound
	}
	cp := *r.sub
	return &cp, nil
}

func (r *subscriptionProgressRepoStub) ListActiveByUserID(context.Context, int64) ([]UserSubscription, error) {
	rows := make([]UserSubscription, len(r.rows))
	copy(rows, r.rows)
	return rows, nil
}

func testProgressSubscription(now time.Time, reserved float64) *UserSubscription {
	dailyStart := timezone.StartOfDay(now).AddDate(0, 0, -1)
	dailyLimit := 10.0
	return &UserSubscription{
		ID:               7,
		StartsAt:         now.Add(-3 * 24 * time.Hour),
		ExpiresAt:        now.Add(10 * 24 * time.Hour),
		Status:           SubscriptionStatusActive,
		DailyWindowStart: &dailyStart,
		DailyUsageUSD:    5,
		DailyReservedUSD: reserved,
		Plan: &SubscriptionPlan{
			Name:          "Pro",
			DailyLimitUSD: &dailyLimit,
		},
	}
}

func TestGetSubscriptionProgressNormalizesExpiredWindowInResponse(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	sub := testProgressSubscription(now, 0)
	repo := &subscriptionProgressRepoStub{sub: sub}
	svc := &SubscriptionService{userSubRepo: repo, now: func() time.Time { return now }}

	progress, err := svc.GetSubscriptionProgress(context.Background(), sub.ID)

	require.NoError(t, err)
	require.NotNil(t, progress.Daily)
	require.Equal(t, 0.0, progress.Daily.UsedUSD)
	require.Equal(t, 0.0, progress.Daily.ReservedUSD)
	require.Equal(t, timezone.StartOfDay(now), progress.Daily.WindowStart)
	// A progress read must not mutate the repository's source object.
	require.Equal(t, 5.0, sub.DailyUsageUSD)
}

func TestGetSubscriptionProgressPreservesExpiredWindowWithReservation(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	sub := testProgressSubscription(now, 1.25)
	repo := &subscriptionProgressRepoStub{sub: sub}
	svc := &SubscriptionService{userSubRepo: repo, now: func() time.Time { return now }}

	progress, err := svc.GetSubscriptionProgress(context.Background(), sub.ID)

	require.NoError(t, err)
	require.NotNil(t, progress.Daily)
	require.Equal(t, 5.0, progress.Daily.UsedUSD)
	require.Equal(t, 1.25, progress.Daily.ReservedUSD)
	require.Equal(t, *sub.DailyWindowStart, progress.Daily.WindowStart)
}

func TestGetUserSubscriptionsWithProgressNormalizesExpiredWindows(t *testing.T) {
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	sub := *testProgressSubscription(now, 0)
	repo := &subscriptionProgressRepoStub{rows: []UserSubscription{sub}}
	svc := &SubscriptionService{userSubRepo: repo, now: func() time.Time { return now }}

	progress, err := svc.GetUserSubscriptionsWithProgress(context.Background(), 42)

	require.NoError(t, err)
	require.Len(t, progress, 1)
	require.NotNil(t, progress[0].Daily)
	require.Equal(t, 0.0, progress[0].Daily.UsedUSD)
	require.Equal(t, timezone.StartOfDay(now), progress[0].Daily.WindowStart)
}
