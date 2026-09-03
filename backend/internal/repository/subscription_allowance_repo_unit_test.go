package repository

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionAllowanceWindowAvailableIncludesReservedAmount(t *testing.T) {
	limit := 50.0

	require.True(t, subscriptionAllowanceWindowAvailable(20, 10, &limit, decimal.NewFromFloat(20)))
	require.False(t, subscriptionAllowanceWindowAvailable(20, 10, &limit, decimal.NewFromFloat(20.00000001)))
	require.True(t, subscriptionAllowanceWindowAvailable(20, 100, nil, decimal.NewFromFloat(1000)))
}

func TestEnsureSubscriptionAllowanceAvailableChecksAllWindows(t *testing.T) {
	daily := 50.0
	weekly := 200.0
	monthly := 500.0
	subscription := &service.UserSubscription{
		DailyUsageUSD:      30,
		DailyReservedUSD:   10,
		WeeklyUsageUSD:     100,
		WeeklyReservedUSD:  20,
		MonthlyUsageUSD:    200,
		MonthlyReservedUSD: 100,
		Plan: &service.SubscriptionPlan{
			DailyLimitUSD:   &daily,
			WeeklyLimitUSD:  &weekly,
			MonthlyLimitUSD: &monthly,
		},
	}
	transition := service.SubscriptionWindowTransition{}

	require.ErrorIs(t, ensureSubscriptionAllowanceAvailable(subscription, transition, 11), service.ErrDailyLimitExceeded)
	require.NoError(t, ensureSubscriptionAllowanceAvailable(subscription, transition, 10))

	subscription.DailyUsageUSD = 0
	subscription.DailyReservedUSD = 0
	subscription.Plan.DailyLimitUSD = nil
	require.ErrorIs(t, ensureSubscriptionAllowanceAvailable(subscription, transition, 81), service.ErrWeeklyLimitExceeded)
}

func TestEnsureSubscriptionAllowanceAvailableTreatsZeroLimitAsExhausted(t *testing.T) {
	zero := 0.0
	subscription := &service.UserSubscription{
		Plan: &service.SubscriptionPlan{DailyLimitUSD: &zero},
	}

	require.ErrorIs(
		t,
		ensureSubscriptionAllowanceAvailable(subscription, service.SubscriptionWindowTransition{}, 0.01),
		service.ErrDailyLimitExceeded,
	)
}

func TestTransitionedUsageResetsOnlyTheAdvancedWindow(t *testing.T) {
	require.Zero(t, transitionedUsage(12.5, true))
	require.Equal(t, 12.5, transitionedUsage(12.5, false))
}

func TestSubscriptionAllowanceAmountEqualUsesBillingScale(t *testing.T) {
	require.True(t, subscriptionAllowanceAmountEqual(0.123456789, 0.12345679))
	require.False(t, subscriptionAllowanceAmountEqual(0.12345678, 0.12345679))
}

func TestCaptureSubscriptionAllowanceZeroAmountSurvivesWindowAdvance(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	expiresAt := time.Now().UTC().Add(time.Hour)
	updatedAt := time.Now().UTC()
	rows := sqlmock.NewRows([]string{
		"request_id", "api_key_id", "user_id", "subscription_id", "authorization_fingerprint",
		"request_fingerprint", "authorized_amount", "captured_amount", "status",
		"daily_window_start", "weekly_window_start", "monthly_window_start", "expires_at",
		"updated_at", "async_task_id",
	}).AddRow("request", 7, 42, 99, "authorization", "", 0.0, 0.0,
		service.BillingReservationAuthorized, time.Now().UTC().Add(-24*time.Hour),
		time.Now().UTC().Add(-7*24*time.Hour), time.Now().UTC().Add(-30*24*time.Hour), expiresAt, updatedAt, "")
	// The first read is deliberately stale: after this zero-cost request was
	// authorized, its subscription window is allowed to advance because no
	// reserved counter was incremented.
	mock.ExpectQuery(`(?s)SELECT .*FROM billing_reservations.*WHERE request_id = \$1 AND api_key_id = \$2 AND funding_source = \$3`).
		WithArgs("request", int64(7), service.FundingSourceSubscription).
		WillReturnRows(rows)
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id\s+FROM user_subscriptions\s+WHERE id = \$1\s+FOR UPDATE`).
		WithArgs(int64(99)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(99)))
	mock.ExpectQuery(`(?s)SELECT .*FROM billing_reservations.*FOR UPDATE`).
		WithArgs("request", int64(7), service.FundingSourceSubscription).
		WillReturnRows(sqlmock.NewRows([]string{
			"request_id", "api_key_id", "user_id", "subscription_id", "authorization_fingerprint",
			"request_fingerprint", "authorized_amount", "captured_amount", "status",
			"daily_window_start", "weekly_window_start", "monthly_window_start", "expires_at",
			"updated_at", "async_task_id",
		}).AddRow("request", 7, 42, 99, "authorization", "", 0.0, 0.0,
			service.BillingReservationAuthorized, time.Now().UTC().Add(-24*time.Hour),
			time.Now().UTC().Add(-7*24*time.Hour), time.Now().UTC().Add(-30*24*time.Hour), expiresAt, updatedAt, ""))
	mock.ExpectQuery(`(?s)UPDATE billing_reservations\s+SET captured_amount = \$3, status = \$4`).
		WithArgs("request", int64(7), 0.0, service.BillingReservationCaptured, "capture-fingerprint",
			service.FundingSourceSubscription, service.BillingReservationAuthorized, service.BillingReservationFinalizing).
		WillReturnRows(sqlmock.NewRows([]string{
			"request_id", "api_key_id", "user_id", "subscription_id", "authorization_fingerprint",
			"request_fingerprint", "authorized_amount", "captured_amount", "status",
			"daily_window_start", "weekly_window_start", "monthly_window_start", "expires_at",
			"updated_at", "async_task_id",
		}).AddRow("request", 7, 42, 99, "authorization", "capture-fingerprint", 0.0, 0.0,
			service.BillingReservationCaptured, nil, nil, nil, expiresAt, updatedAt, ""))
	mock.ExpectCommit()

	repo := &usageBillingRepository{db: db}
	record, err := repo.CaptureSubscriptionAllowance(context.Background(), &service.SubscriptionAllowanceCommand{
		RequestID: "request", APIKeyID: 7, UserID: 42, SubscriptionID: 99,
		AuthorizationFingerprint: "authorization", Amount: 0,
		AuthorizedAt: time.Now().UTC(), ExpiresAt: expiresAt,
	}, "capture-fingerprint")
	require.NoError(t, err)
	require.Equal(t, service.BillingReservationCaptured, record.Status)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestTopUpSubscriptionAllowanceZeroAmountRebindsAdvancedWindow(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	now := time.Now().UTC().Truncate(time.Microsecond)
	expiresAt := now.Add(20 * 24 * time.Hour)
	oldDaily := now.Add(-24 * time.Hour)
	oldWeekly := now.Add(-8 * 24 * time.Hour)
	oldMonthly := now.Add(-31 * 24 * time.Hour)
	reservationColumns := []string{
		"request_id", "api_key_id", "user_id", "subscription_id", "authorization_fingerprint",
		"request_fingerprint", "authorized_amount", "captured_amount", "status",
		"daily_window_start", "weekly_window_start", "monthly_window_start", "expires_at",
		"updated_at", "async_task_id",
	}
	addReservation := func(amount float64, daily, weekly, monthly any) *sqlmock.Rows {
		return sqlmock.NewRows(reservationColumns).AddRow(
			"request", 7, 42, 99, "authorization", "", amount, 0.0,
			service.BillingReservationAuthorized, daily, weekly, monthly, expiresAt, now, "",
		)
	}

	mock.ExpectQuery(`(?s)SELECT .*FROM billing_reservations.*WHERE request_id = \$1 AND api_key_id = \$2 AND funding_source = \$3`).
		WithArgs("request", int64(7), service.FundingSourceSubscription).
		WillReturnRows(addReservation(0, oldDaily, oldWeekly, oldMonthly))
	mock.ExpectBegin()
	mock.ExpectQuery(`(?s)SELECT id\s+FROM user_subscriptions\s+WHERE id = \$1\s+FOR UPDATE`).
		WithArgs(int64(99)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(99)))
	mock.ExpectQuery(`(?s)SELECT .*FROM billing_reservations.*FOR UPDATE`).
		WithArgs("request", int64(7), service.FundingSourceSubscription).
		WillReturnRows(addReservation(0, oldDaily, oldWeekly, oldMonthly))
	mock.ExpectQuery(`(?s)SELECT us\.starts_at, us\.expires_at, us\.status,.*FROM user_subscriptions us.*FOR UPDATE OF us`).
		WithArgs(int64(99)).
		WillReturnRows(sqlmock.NewRows([]string{
			"starts_at", "expires_at", "status", "daily_window_start", "weekly_window_start", "monthly_window_start",
			"daily_usage_usd", "weekly_usage_usd", "monthly_usage_usd", "daily_reserved_usd", "weekly_reserved_usd", "monthly_reserved_usd",
			"daily_limit_usd", "weekly_limit_usd", "monthly_limit_usd",
		}).AddRow(
			now.Add(-2*24*time.Hour), expiresAt, service.SubscriptionStatusActive,
			oldDaily, oldWeekly, oldMonthly, 3.0, 10.0, 20.0, 0.0, 0.0, 0.0,
			10.0, 100.0, 300.0,
		))
	mock.ExpectExec(`(?s)UPDATE user_subscriptions\s+SET daily_window_start = \$2,`).
		WithArgs(int64(99), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), 0.5).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`(?s)UPDATE billing_reservations\s+SET daily_window_start = \$3,`).
		WithArgs("request", int64(7), sqlmock.AnyArg(), sqlmock.AnyArg(), sqlmock.AnyArg(), service.FundingSourceSubscription, service.BillingReservationAuthorized).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`(?s)UPDATE billing_reservations\s+SET authorized_amount = \$3, updated_at = NOW\(\)`).
		WithArgs("request", int64(7), 0.5, service.BillingReservationAuthorized, service.FundingSourceSubscription).
		WillReturnRows(addReservation(0.5, now, now, now))
	mock.ExpectCommit()

	repo := &usageBillingRepository{db: db}
	record, err := repo.TopUpSubscriptionAllowance(context.Background(), &service.SubscriptionAllowanceCommand{
		RequestID: "request", APIKeyID: 7, UserID: 42, SubscriptionID: 99,
		AuthorizationFingerprint: "authorization", Amount: 0.5,
		AuthorizedAt: now, ExpiresAt: expiresAt,
	})
	require.NoError(t, err)
	require.Equal(t, 0.5, record.AuthorizedAmount)
	require.Equal(t, service.BillingReservationAuthorized, record.Status)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestListRecoverableSubscriptionAllowancesLeasesSubscriptionRows(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	authorizationCutoff := time.Now().UTC()
	finalizationCutoff := authorizationCutoff.Add(-30 * time.Second)
	expiresAt := authorizationCutoff.Add(-time.Minute)
	updatedAt := finalizationCutoff.Add(-time.Second)
	mock.ExpectQuery(`(?s)WITH candidates AS .*FROM billing_reservations.*funding_source = \$1.*updated_at <= NOW\(\) - \(\$6 \* INTERVAL '1 second'\).*FOR UPDATE SKIP LOCKED.*UPDATE billing_reservations AS reservation.*SET updated_at = NOW\(\).*FROM leased`).
		WithArgs(service.FundingSourceSubscription, service.BillingReservationAuthorized, authorizationCutoff,
			service.BillingReservationFinalizing, finalizationCutoff, int64(60), 500).
		WillReturnRows(sqlmock.NewRows([]string{
			"request_id", "api_key_id", "user_id", "subscription_id", "authorization_fingerprint",
			"request_fingerprint", "authorized_amount", "captured_amount", "status",
			"daily_window_start", "weekly_window_start", "monthly_window_start", "expires_at",
			"updated_at", "async_task_id",
		}).AddRow("request", 7, 42, 99, "authorization", "", "0.50", "0", service.BillingReservationAuthorized,
			nil, nil, nil, expiresAt, updatedAt, ""))

	repo := &usageBillingRepository{db: db}
	records, err := repo.ListRecoverableSubscriptionAllowances(
		context.Background(), authorizationCutoff, finalizationCutoff, 0,
	)
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, int64(99), records[0].SubscriptionID)
	require.NoError(t, mock.ExpectationsWereMet())
}
