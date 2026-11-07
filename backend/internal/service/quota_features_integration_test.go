//go:build integration

package service_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	_ "github.com/Wei-Shaw/sub2api/ent/runtime"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

// This suite uses a migrated, disposable database supplied by the caller.
func TestQuotaFeaturesDatabaseLifecycle(t *testing.T) {
	dsn := os.Getenv("SUB2API_FEATURE_TEST_DSN")
	if dsn == "" {
		t.Skip("SUB2API_FEATURE_TEST_DSN is required")
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	user, err := client.User.Create().SetEmail(fmt.Sprintf("quota-feature-%d@test.local", time.Now().UnixNano())).SetPasswordHash("local-test").Save(ctx)
	require.NoError(t, err)
	plan, err := client.SubscriptionPlan.Create().SetName("Integration hourly card").Save(ctx)
	require.NoError(t, err)
	total := 100.0
	version, err := client.SubscriptionPlanVersion.Create().SetPlanID(plan.ID).SetVersion(1).SetPrice(1).SetValidityDays(1).SetCardMode("hours").SetValidityHours(12).SetTotalLimitUsd(total).Save(ctx)
	require.NoError(t, err)
	_, err = client.SubscriptionPlan.UpdateOneID(plan.ID).SetPublishedVersionID(version.ID).Save(ctx)
	require.NoError(t, err)
	subRepo := repository.NewUserSubscriptionRepository(client)
	subs := service.NewSubscriptionService(subRepo, client)
	input := &service.AssignSubscriptionInput{UserID: user.ID, PlanID: plan.ID, PlanVersionID: version.ID}
	first, reused, err := subs.AssignOrExtendSubscription(ctx, input)
	require.NoError(t, err)
	require.False(t, reused)
	second, reused, err := subs.AssignOrExtendSubscription(ctx, input)
	require.NoError(t, err)
	require.False(t, reused)
	require.NotEqual(t, first.ID, second.ID)
	require.InDelta(t, (12 * time.Hour).Seconds(), first.ExpiresAt.Sub(first.StartsAt).Seconds(), 0.1)
	key, err := client.APIKey.Create().SetUserID(user.ID).SetKey(fmt.Sprintf("quota-test-%d", time.Now().UnixNano())).SetName("Quota priority test").SetFundingSource(service.FundingSourceSubscription).SetSubscriptionID(first.ID).SetSubscriptionPriority([]int64{first.ID, second.ID}).Save(ctx)
	require.NoError(t, err)
	billingRepo := repository.NewUsageBillingRepository(client, db)
	allowance := billingRepo.(service.SubscriptionAllowanceRepository)
	commands := make([]*service.SubscriptionAllowanceCommand, 2)
	for i := range commands {
		commands[i] = &service.SubscriptionAllowanceCommand{RequestID: fmt.Sprintf("quota-feature-%d-%d", key.ID, i), APIKeyID: key.ID, UserID: user.ID, SubscriptionID: first.ID, Amount: 60, AuthorizationFingerprint: "integration", AuthorizedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range commands {
		wg.Add(1)
		go func(i int) { defer wg.Done(); _, errs[i] = allowance.AuthorizeSubscriptionAllowance(ctx, commands[i]) }(i)
	}
	wg.Wait()
	winner := -1
	for i, err := range errs {
		if err == nil {
			require.Equal(t, -1, winner)
			winner = i
		} else {
			require.ErrorIs(t, err, service.ErrTotalLimitExceeded)
		}
	}
	require.NotEqual(t, -1, winner)
	current, err := subRepo.GetByID(ctx, first.ID)
	require.NoError(t, err)
	require.Equal(t, 60.0, current.TotalReservedUSD)
	_, err = allowance.CaptureSubscriptionAllowance(ctx, commands[winner], "integration-capture")
	require.NoError(t, err)
	current, err = subRepo.GetByID(ctx, first.ID)
	require.NoError(t, err)
	require.Equal(t, 60.0, current.TotalUsageUSD)
	require.Zero(t, current.TotalReservedUSD)
	keyService := service.NewAPIKeyService(repository.NewAPIKeyRepository(client, db), nil, nil, subRepo, nil, nil, nil)
	requestKey := &service.APIKey{ID: key.ID, UserID: user.ID, FundingSource: service.FundingSourceSubscription, SubscriptionID: &first.ID, SubscriptionPriority: []int64{first.ID, second.ID}}
	baseline := service.BuildBalancePreauthorizationBaselineKey(user.ID, key.ID, service.FundingSourceSubscription, first.ID, "priority-session")
	store := billingRepo.(service.BalancePreauthorizationBaselineStore)
	require.NoError(t, store.RecordPreauthorizationBaseline(ctx, baseline, 60))
	require.NoError(t, store.RecordPreauthorizationBaseline(ctx, service.BuildBalancePreauthorizationBaselineKey(user.ID, key.ID, service.FundingSourceSubscription, second.ID, "priority-session"), 60))
	preauthorizer := service.NewBalancePreauthorizationService(nil, nil, nil, nil, billingRepo, keyService)
	guard, err := preauthorizer.PreauthorizeWithFunding(ctx, service.BalancePreauthorizationRequest{RequestID: fmt.Sprintf("quota-priority-%d", key.ID), APIKeyID: key.ID, UserID: user.ID, BillingType: service.BillingTypeSubscription, SubscriptionID: first.ID, AuthorizationFingerprint: "priority", BaselineSessionID: "priority-session"}, requestKey, current)
	require.NoError(t, err)
	require.Equal(t, second.ID, *guard.SubscriptionID(), "a card with insufficient remaining allowance must advance before forwarding")
	require.Equal(t, 60.0, guard.HoldAmount())
	require.Equal(t, second.ID, current.ID, "the usage snapshot must charge the reserved card")
	require.Equal(t, service.BuildBalancePreauthorizationBaselineKey(user.ID, key.ID, service.FundingSourceSubscription, second.ID, "priority-session"), guard.BaselineKey())
	require.NoError(t, guard.Refund(ctx))
	priority := []int64{second.ID, first.ID}
	updated, err := keyService.Update(ctx, key.ID, user.ID, service.UpdateAPIKeyRequest{SubscriptionPriority: &priority})
	require.NoError(t, err)
	require.Equal(t, priority, updated.SubscriptionPriority)
	reloaded, err := keyService.GetByID(ctx, key.ID)
	require.NoError(t, err)
	require.Equal(t, priority, reloaded.SubscriptionPriority)
	duplicate := []int64{first.ID, first.ID}
	_, err = keyService.Update(ctx, key.ID, user.ID, service.UpdateAPIKeyRequest{SubscriptionPriority: &duplicate})
	require.Error(t, err)
	other, err := client.User.Create().SetEmail(fmt.Sprintf("other-quota-%d@test.local", time.Now().UnixNano())).SetPasswordHash("local-test").Save(ctx)
	require.NoError(t, err)
	otherSub, _, err := subs.AssignOrExtendSubscription(ctx, &service.AssignSubscriptionInput{UserID: other.ID, PlanID: plan.ID, PlanVersionID: version.ID})
	require.NoError(t, err)
	foreign := []int64{otherSub.ID}
	_, err = keyService.Update(ctx, key.ID, user.ID, service.UpdateAPIKeyRequest{SubscriptionPriority: &foreign})
	require.Error(t, err, "another user's card must never enter the funding order")
	source := service.FundingSourceSubscription
	updated, err = keyService.Update(ctx, key.ID, user.ID, service.UpdateAPIKeyRequest{FundingSource: &source, SubscriptionID: &first.ID})
	require.NoError(t, err)
	require.Empty(t, updated.SubscriptionPriority, "changing the primary without a list must clear stale priority")
	_, err = client.UserSubscription.UpdateOneID(first.ID).SetTotalUsageUsd(100).Save(ctx)
	require.NoError(t, err)
	requestKey.SubscriptionID = &first.ID
	_, selected, err := keyService.ResolveGatewayFunding(ctx, requestKey)
	require.NoError(t, err)
	require.Equal(t, second.ID, selected.ID)
	// Normal subscriptions keep renewal behavior and user revocation safeguards.
	regular, err := client.SubscriptionPlan.Create().SetName("Integration daily plan").Save(ctx)
	require.NoError(t, err)
	regularVersion, err := client.SubscriptionPlanVersion.Create().SetPlanID(regular.ID).SetVersion(1).SetPrice(1).SetValidityDays(30).SetDailyLimitUsd(10).Save(ctx)
	require.NoError(t, err)
	_, err = client.SubscriptionPlan.UpdateOneID(regular.ID).SetPublishedVersionID(regularVersion.ID).Save(ctx)
	require.NoError(t, err)
	regularInput := &service.AssignSubscriptionInput{UserID: user.ID, PlanID: regular.ID, PlanVersionID: regularVersion.ID}
	regularSub, _, err := subs.AssignOrExtendSubscription(ctx, regularInput)
	require.NoError(t, err)
	regularSub, err = subs.EnsureWindowMaintenance(ctx, regularSub)
	require.NoError(t, err)
	_, err = client.UserSubscription.UpdateOneID(regularSub.ID).SetDailyUsageUsd(10).Save(ctx)
	require.NoError(t, err)
	_, err = client.APIKey.UpdateOneID(key.ID).SetSubscriptionID(regularSub.ID).SetSubscriptionPriority([]int64{}).Save(ctx)
	require.NoError(t, err)
	zero := &service.SubscriptionAllowanceCommand{RequestID: fmt.Sprintf("quota-zero-%d", key.ID), APIKeyID: key.ID, UserID: user.ID, SubscriptionID: regularSub.ID, Amount: 0, AuthorizationFingerprint: "zero", AuthorizedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	_, err = allowance.AuthorizeSubscriptionAllowance(ctx, zero)
	require.NoError(t, err)
	require.ErrorIs(t, subs.SelfRevokeSubscription(ctx, user.ID, regularSub.ID), service.ErrSubscriptionSelfRevoke)
	_, err = allowance.ReleaseSubscriptionAllowance(ctx, zero)
	require.NoError(t, err)
	require.NoError(t, subs.SelfRevokeSubscription(ctx, user.ID, regularSub.ID))
	paused, err := client.APIKey.Get(ctx, key.ID)
	require.NoError(t, err)
	require.Equal(t, "inactive", paused.Status)
	require.Equal(t, service.FundingSourceSubscription, paused.FundingSource)
	history, err := subRepo.GetByIDIncludeDeleted(ctx, regularSub.ID)
	require.NoError(t, err)
	require.NotNil(t, history.DeletedAt)
	newSub, _, err := subs.AssignOrExtendSubscription(ctx, regularInput)
	require.NoError(t, err)
	require.NotEqual(t, regularSub.ID, newSub.ID)
	renewed, reused, err := subs.AssignOrExtendSubscription(ctx, regularInput)
	require.NoError(t, err)
	require.True(t, reused)
	require.Equal(t, 1, renewed.RenewalCount)
	require.True(t, errors.Is(subs.SelfRevokeSubscription(ctx, user.ID, renewed.ID), service.ErrSubscriptionSelfRevoke))
}

func TestFixedDeadlineCardEnforcesTotalAllowanceAndExpiry(t *testing.T) {
	dsn := os.Getenv("SUB2API_FEATURE_TEST_DSN")
	if dsn == "" {
		t.Skip("SUB2API_FEATURE_TEST_DSN is required")
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	user, err := client.User.Create().SetEmail(fmt.Sprintf("deadline-card-%d@test.local", time.Now().UnixNano())).SetPasswordHash("local-test").Save(ctx)
	require.NoError(t, err)
	plan, err := client.SubscriptionPlan.Create().SetName("Deadline quota regression").Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		for _, query := range []string{
			"DELETE FROM billing_reservations WHERE user_id = $1",
			"DELETE FROM api_keys WHERE user_id = $1",
			"DELETE FROM user_subscriptions WHERE user_id = $1",
			"DELETE FROM users WHERE id = $1",
		} {
			_, cleanupErr := db.Exec(query, user.ID)
			require.NoError(t, cleanupErr)
		}
		_, err := db.Exec("UPDATE subscription_plans SET published_version_id = NULL WHERE id = $1", plan.ID)
		require.NoError(t, err)
		_, err = db.Exec("DELETE FROM subscription_plan_versions WHERE plan_id = $1", plan.ID)
		require.NoError(t, err)
		_, err = db.Exec("DELETE FROM subscription_plans WHERE id = $1", plan.ID)
		require.NoError(t, err)
	})
	deadline := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	version, err := client.SubscriptionPlanVersion.Create().SetPlanID(plan.ID).SetVersion(1).SetPrice(20).SetValidityDays(1).SetCardMode("deadline").SetDeadlineAt(deadline).SetTotalLimitUsd(300).Save(ctx)
	require.NoError(t, err)
	_, err = client.SubscriptionPlan.UpdateOneID(plan.ID).SetPublishedVersionID(version.ID).Save(ctx)
	require.NoError(t, err)
	subs := service.NewSubscriptionService(repository.NewUserSubscriptionRepository(client), client)
	sub, _, err := subs.AssignOrExtendSubscription(ctx, &service.AssignSubscriptionInput{UserID: user.ID, PlanID: plan.ID, PlanVersionID: version.ID})
	require.NoError(t, err)
	require.True(t, sub.IsQuotaCard)
	require.True(t, deadline.Equal(sub.ExpiresAt))
	require.Equal(t, 300.0, *sub.Plan.TotalLimitUSD)
	key, err := client.APIKey.Create().SetUserID(user.ID).SetKey(fmt.Sprintf("deadline-quota-%d", user.ID)).SetName("Deadline test").SetFundingSource(service.FundingSourceSubscription).SetSubscriptionID(sub.ID).Save(ctx)
	require.NoError(t, err)
	allowance := repository.NewUsageBillingRepository(client, db).(service.SubscriptionAllowanceRepository)
	command := func(name string, amount float64) *service.SubscriptionAllowanceCommand {
		return &service.SubscriptionAllowanceCommand{RequestID: fmt.Sprintf("deadline-%d-%s", key.ID, name), APIKeyID: key.ID, UserID: user.ID, SubscriptionID: sub.ID, Amount: amount, AuthorizationFingerprint: name, AuthorizedAt: time.Now(), ExpiresAt: deadline}
	}
	first := command("first", 250)
	_, err = allowance.AuthorizeSubscriptionAllowance(ctx, first)
	require.NoError(t, err)
	_, err = allowance.AuthorizeSubscriptionAllowance(ctx, command("over-reserved", 51))
	require.ErrorIs(t, err, service.ErrTotalLimitExceeded)
	_, err = allowance.CaptureSubscriptionAllowance(ctx, first, "first-capture")
	require.NoError(t, err)
	last := command("remaining", 50)
	_, err = allowance.AuthorizeSubscriptionAllowance(ctx, last)
	require.NoError(t, err)
	_, err = allowance.CaptureSubscriptionAllowance(ctx, last, "remaining-capture")
	require.NoError(t, err)
	_, err = allowance.AuthorizeSubscriptionAllowance(ctx, command("exhausted", 1))
	require.ErrorIs(t, err, service.ErrTotalLimitExceeded)
	_, err = client.UserSubscription.UpdateOneID(sub.ID).SetExpiresAt(time.Now().Add(-time.Second)).Save(ctx)
	require.NoError(t, err)
	_, err = allowance.AuthorizeSubscriptionAllowance(ctx, command("expired", 0))
	require.ErrorIs(t, err, service.ErrSubscriptionExpired)
}
