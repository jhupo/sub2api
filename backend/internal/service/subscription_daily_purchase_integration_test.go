//go:build integration

package service_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestDailyCardPurchasesPersistSeparateAllowances(t *testing.T) {
	dsn := os.Getenv("SUB2API_FEATURE_TEST_DSN")
	if dsn == "" {
		t.Skip("SUB2API_FEATURE_TEST_DSN is required")
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	user, err := client.User.Create().SetEmail(fmt.Sprintf("daily-purchase-%d@test.local", time.Now().UnixNano())).SetPasswordHash("test").Save(ctx)
	require.NoError(t, err)
	plan, err := client.SubscriptionPlan.Create().SetName("Daily purchase regression").Save(ctx)
	require.NoError(t, err)
	version, err := client.SubscriptionPlanVersion.Create().SetPlanID(plan.ID).SetVersion(1).SetPrice(1).SetValidityDays(1).SetValidityUnit("days").SetDailyLimitUsd(100).Save(ctx)
	require.NoError(t, err)
	_, err = client.SubscriptionPlan.UpdateOneID(plan.ID).SetPublishedVersionID(version.ID).Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		for _, query := range []string{
			"DELETE FROM billing_reservations WHERE user_id = $1",
			"DELETE FROM api_keys WHERE user_id = $1",
			"DELETE FROM user_subscriptions WHERE user_id = $1",
			"DELETE FROM users WHERE id = $1",
		} {
			_, err := db.Exec(query, user.ID)
			require.NoError(t, err)
		}
		_, err := db.Exec("UPDATE subscription_plans SET published_version_id = NULL WHERE id = $1", plan.ID)
		require.NoError(t, err)
		_, err = db.Exec("DELETE FROM subscription_plan_versions WHERE plan_id = $1", plan.ID)
		require.NoError(t, err)
		_, err = db.Exec("DELETE FROM subscription_plans WHERE id = $1", plan.ID)
		require.NoError(t, err)
	})
	svc := service.NewSubscriptionService(repository.NewUserSubscriptionRepository(client), client)
	input := &service.AssignSubscriptionInput{UserID: user.ID, PlanID: plan.ID, PlanVersionID: version.ID}
	first, _, err := svc.AssignOrExtendSubscription(ctx, input)
	require.NoError(t, err)
	_, err = client.UserSubscription.UpdateOneID(first.ID).SetDailyUsageUsd(50).SetTotalUsageUsd(50).Save(ctx)
	require.NoError(t, err)
	second, extended, err := svc.AssignOrExtendSubscription(ctx, input)
	require.NoError(t, err)
	require.False(t, extended)
	require.NotEqual(t, first.ID, second.ID)
	require.True(t, second.IsQuotaCard)
	persisted, err := client.UserSubscription.Get(ctx, first.ID)
	require.NoError(t, err)
	require.Equal(t, 50.0, persisted.DailyUsageUsd)
	require.WithinDuration(t, first.ExpiresAt, persisted.ExpiresAt, time.Microsecond)
	require.InDelta(t, 24, second.ExpiresAt.Sub(second.StartsAt).Hours(), 0.1)

	// The SQL billing path must load the old card's original plan duration
	// and renewal count too; fixing only the display would still reset it.
	start := time.Date(2027, 1, 5, 20, 0, 0, 0, time.UTC)
	midnight := time.Date(2027, 1, 5, 0, 0, 0, 0, time.UTC)
	legacy, err := client.UserSubscription.Create().SetUserID(user.ID).SetPlanID(plan.ID).SetPlanVersionID(version.ID).
		SetStartsAt(start).SetExpiresAt(start.Add(48 * time.Hour)).SetStatus(service.SubscriptionStatusActive).
		SetRenewalCount(1).SetDailyWindowStart(midnight).SetDailyUsageUsd(50).SetTotalUsageUsd(50).Save(ctx)
	require.NoError(t, err)
	key, err := client.APIKey.Create().SetUserID(user.ID).SetKey(fmt.Sprintf("daily-key-%d", time.Now().UnixNano())).
		SetName("Daily regression").SetFundingSource(service.FundingSourceSubscription).SetSubscriptionID(legacy.ID).Save(ctx)
	require.NoError(t, err)
	allowance := repository.NewUsageBillingRepository(client, db).(service.SubscriptionAllowanceRepository)
	for i, at := range []time.Time{start.Add(4*time.Hour + time.Minute), start.Add(24*time.Hour + time.Minute)} {
		if i == 1 {
			_, err = client.UserSubscription.UpdateOneID(legacy.ID).SetDailyWindowStart(midnight.Add(24 * time.Hour)).Save(ctx)
			require.NoError(t, err)
		}
		cmd := &service.SubscriptionAllowanceCommand{RequestID: fmt.Sprintf("daily-legacy-%d-%d", key.ID, i), APIKeyID: key.ID,
			UserID: user.ID, SubscriptionID: legacy.ID, Amount: 1, AuthorizationFingerprint: "daily-test",
			AuthorizedAt: at, ExpiresAt: at.Add(time.Hour)}
		_, err = allowance.AuthorizeSubscriptionAllowance(ctx, cmd)
		require.NoError(t, err)
		row, err := client.UserSubscription.Get(ctx, legacy.ID)
		require.NoError(t, err)
		require.Equal(t, 50.0, row.DailyUsageUsd, "midnight must not reset or duplicate an already granted daily allowance")
		_, err = allowance.ReleaseSubscriptionAllowance(ctx, cmd)
		require.NoError(t, err)
	}
}
