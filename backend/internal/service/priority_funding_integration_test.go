//go:build integration

package service_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/migrations"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type priorityFixture struct {
	db                     *sql.DB
	client                 *dbent.Client
	repo                   service.UsageBillingRepository
	priority               service.PriorityFundingRepository
	preauthorizer          *service.BalancePreauthorizationService
	wallet                 service.BillingCache
	key                    *service.APIKey
	first, second, expired int64
}

func newPriorityFixture(t *testing.T, wallet bool) *priorityFixture {
	t.Helper()
	dsn := os.Getenv("SUB2API_FEATURE_TEST_DSN")
	if dsn == "" {
		t.Skip("SUB2API_FEATURE_TEST_DSN is required")
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	source, err := migrations.FS.ReadFile("248_priority_funding_reservations.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(source))
	require.NoError(t, err)
	ctx := context.Background()
	user, err := client.User.Create().SetEmail(fmt.Sprintf("split-%d@test.local", time.Now().UnixNano())).SetPasswordHash("test").SetBalance(10).Save(ctx)
	require.NoError(t, err)
	var planIDs []int64
	t.Cleanup(func() {
		for _, table := range []string{"billing_priority_reservations", "billing_balance_settlements", "billing_reservations", "api_keys", "user_subscriptions"} {
			_, cleanupErr := db.Exec("DELETE FROM "+table+" WHERE user_id = $1", user.ID)
			require.NoError(t, cleanupErr)
		}
		_, err := db.Exec("DELETE FROM users WHERE id = $1", user.ID)
		require.NoError(t, err)
		for _, id := range planIDs {
			_, err := db.Exec("UPDATE subscription_plans SET published_version_id = NULL WHERE id = $1", id)
			require.NoError(t, err)
			_, err = db.Exec("DELETE FROM subscription_plan_versions WHERE plan_id = $1", id)
			require.NoError(t, err)
			_, err = db.Exec("DELETE FROM subscription_plans WHERE id = $1", id)
			require.NoError(t, err)
		}
	})
	makeSub := func(daily bool, expired bool) int64 {
		plan, err := client.SubscriptionPlan.Create().SetName("Split funding test").Save(ctx)
		require.NoError(t, err)
		planIDs = append(planIDs, plan.ID)
		builder := client.SubscriptionPlanVersion.Create().SetPlanID(plan.ID).SetVersion(1).SetPrice(1).SetValidityDays(2)
		if daily {
			builder.SetDailyLimitUsd(1)
		} else {
			builder.SetTotalLimitUsd(0.2).SetCardMode("hours").SetValidityHours(12)
		}
		version, err := builder.Save(ctx)
		require.NoError(t, err)
		_, err = client.SubscriptionPlan.UpdateOneID(plan.ID).SetPublishedVersionID(version.ID).Save(ctx)
		require.NoError(t, err)
		subService := service.NewSubscriptionService(repository.NewUserSubscriptionRepository(client), client)
		sub, _, err := subService.AssignOrExtendSubscription(ctx,
			&service.AssignSubscriptionInput{UserID: user.ID, PlanID: plan.ID, PlanVersionID: version.ID})
		require.NoError(t, err)
		_, err = subService.EnsureWindowMaintenance(ctx, sub)
		require.NoError(t, err)
		if daily {
			_, err = client.UserSubscription.UpdateOneID(sub.ID).SetDailyUsageUsd(0.55).Save(ctx)
			require.NoError(t, err)
		}
		if expired {
			_, err = client.UserSubscription.UpdateOneID(sub.ID).SetExpiresAt(time.Now().Add(-time.Minute)).Save(ctx)
			require.NoError(t, err)
		}
		return sub.ID
	}
	f := &priorityFixture{db: db, client: client}
	f.first, f.second, f.expired = makeSub(true, false), makeSub(false, false), makeSub(false, true)
	ids := []int64{f.first, f.expired, f.second}
	key, err := client.APIKey.Create().SetUserID(user.ID).SetKey(fmt.Sprintf("split-key-%d", time.Now().UnixNano())).SetName("Split test").
		SetFundingSource(service.FundingSourceSubscription).SetSubscriptionID(f.first).SetSubscriptionPriority(ids).SetAllowWalletFallback(wallet).Save(ctx)
	require.NoError(t, err)
	f.key = &service.APIKey{ID: key.ID, UserID: user.ID, FundingSource: service.FundingSourceSubscription, SubscriptionID: &f.first, SubscriptionPriority: ids, AllowWalletFallback: wallet}
	f.repo = repository.NewUsageBillingRepository(client, db)
	f.priority = f.repo.(service.PriorityFundingRepository)
	redisServer := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cfg := &config.Config{}
	cfg.Billing.BalancePreauthorizationEnabled = true
	f.wallet = repository.NewBillingCache(rdb)
	cache := service.NewBillingCacheService(f.wallet, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(cache.Stop)
	f.preauthorizer = service.NewBalancePreauthorizationService(cfg, nil, nil, cache, f.repo, nil)
	return f
}

func (f *priorityFixture) command(id string, amount float64) *service.PriorityFundingCommand {
	return &service.PriorityFundingCommand{RequestID: id, APIKeyID: f.key.ID, UserID: f.key.UserID, PrimarySubscriptionID: f.first,
		AuthorizationFingerprint: "payload", Policy: service.PriorityFundingPolicy{SubscriptionIDs: f.key.SubscriptionPriority, WalletFallback: f.key.AllowWalletFallback}, Amount: amount}
}

func (f *priorityFixture) charge(id string, amount float64) *service.UsageBillingCommand {
	policy := f.command(id, amount).Policy
	return &service.UsageBillingCommand{RequestID: id, APIKeyID: f.key.ID, UserID: f.key.UserID, SubscriptionID: &f.first,
		SubscriptionCost: amount, PriorityFunding: &policy, Model: "split-test"}
}

func (f *priorityFixture) authorize(t *testing.T, id string, amount float64) *service.BalancePreauthorizationGuard {
	t.Helper()
	baseline := service.BuildBalancePreauthorizationBaselineKey(f.key.UserID, f.key.ID, "priority", 0, "")
	require.NoError(t, f.repo.(service.BalancePreauthorizationBaselineStore).RecordPreauthorizationBaseline(context.Background(), baseline, amount))
	guard, err := f.preauthorizer.PreauthorizeWithFunding(context.Background(), service.BalancePreauthorizationRequest{
		RequestID: id, APIKeyID: f.key.ID, UserID: f.key.UserID, SubscriptionID: f.first,
		BillingType: service.BillingTypeSubscription, AuthorizationFingerprint: "payload",
	}, f.key, nil)
	require.NoError(t, err)
	require.NotNil(t, guard)
	return guard
}

func (f *priorityFixture) assertReleased(t *testing.T) {
	t.Helper()
	for _, id := range []int64{f.first, f.second} {
		sub, err := f.client.UserSubscription.Get(context.Background(), id)
		require.NoError(t, err)
		require.Zero(t, sub.TotalReservedUsd)
		require.Zero(t, sub.DailyReservedUsd)
		require.Zero(t, sub.WeeklyReservedUsd)
		require.Zero(t, sub.MonthlyReservedUsd)
	}
}

func TestPriorityFundingSplitsActualCostAndDeduplicates(t *testing.T) {
	f := newPriorityFixture(t, true)
	ctx := context.Background()
	result, err := f.repo.Apply(ctx, f.charge("actual-split", 0.8))
	require.NoError(t, err)
	require.NotNil(t, result.PriorityFunding)
	require.Len(t, result.PriorityFunding.Allocations, 2)
	require.Equal(t, f.first, result.PriorityFunding.Allocations[0].SubscriptionID)
	require.Equal(t, 0.45, result.PriorityFunding.Allocations[0].Amount)
	require.Equal(t, f.second, result.PriorityFunding.Allocations[1].SubscriptionID)
	require.Equal(t, 0.2, result.PriorityFunding.Allocations[1].Amount)
	require.Equal(t, 0.15, result.PriorityFunding.WalletAmount)
	duplicate, err := f.repo.Apply(ctx, f.charge("actual-split", 0.8))
	require.NoError(t, err)
	require.False(t, duplicate.Applied)
	_, err = f.repo.Apply(ctx, f.charge("actual-split", 0.81))
	require.ErrorIs(t, err, service.ErrUsageBillingRequestConflict)
	_, err = repository.NewUsageBalanceSettlementRepository(f.db).FlushPendingBalanceSettlements(ctx, 1000)
	require.NoError(t, err)
	user, err := f.client.User.Get(ctx, f.key.UserID)
	require.NoError(t, err)
	require.Equal(t, 9.85, user.Balance)
	first, err := f.client.UserSubscription.Get(ctx, f.first)
	require.NoError(t, err)
	require.Equal(t, 1.0, first.DailyUsageUsd)
	second, err := f.client.UserSubscription.Get(ctx, f.second)
	require.NoError(t, err)
	require.Equal(t, 0.2, second.TotalUsageUsd)
}

func TestPriorityFundingReservationRefundAndSmallerActual(t *testing.T) {
	f := newPriorityFixture(t, true)
	ctx := context.Background()
	baseline := service.BuildBalancePreauthorizationBaselineKey(f.key.UserID, f.key.ID, "priority", 0, "")
	require.NoError(t, f.repo.(service.BalancePreauthorizationBaselineStore).RecordPreauthorizationBaseline(ctx, baseline, 0.8))
	request := service.BalancePreauthorizationRequest{RequestID: "reserved-split", APIKeyID: f.key.ID, UserID: f.key.UserID,
		SubscriptionID: f.first, BillingType: service.BillingTypeSubscription, AuthorizationFingerprint: "payload"}
	guard, err := f.preauthorizer.PreauthorizeWithFunding(ctx, request, f.key, nil)
	require.NoError(t, err)
	require.Equal(t, 0.8, guard.HoldAmount())
	record, err := f.priority.GetPriorityFunding(ctx, request.RequestID, f.key.ID)
	require.NoError(t, err)
	require.Equal(t, 0.15, record.WalletAmount)
	require.NotNil(t, record.WalletRecord)
	resume, err := f.preauthorizer.Resume(ctx, service.BalancePreauthorizationResumeRequest{RequestID: request.RequestID, APIKeyID: f.key.ID,
		UserID: f.key.UserID, SubscriptionID: f.first, BillingType: service.BillingTypeSubscription, AuthorizationFingerprint: "payload", HoldAmount: 0.8})
	require.NoError(t, err)
	cmd := f.charge(request.RequestID, 0.6)
	result, err := f.repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.Equal(t, 0.0, result.PriorityFunding.WalletAmount)
	require.NoError(t, resume.Finalize(ctx, 0.6, cmd.RequestFingerprint))
	record, err = f.priority.GetPriorityFunding(ctx, request.RequestID, f.key.ID)
	require.NoError(t, err)
	require.Equal(t, service.BalanceSettlementRefunded, record.WalletRecord.Status)
	second, err := f.client.UserSubscription.Get(ctx, f.second)
	require.NoError(t, err)
	require.Equal(t, 0.15, second.TotalUsageUsd)
	require.Zero(t, second.TotalReservedUsd)
	first, err := f.client.UserSubscription.Get(ctx, f.first)
	require.NoError(t, err)
	require.Zero(t, first.DailyReservedUsd)
}

func TestPriorityFundingConcurrentReservationsAndRefund(t *testing.T) {
	f := newPriorityFixture(t, false)
	ctx := context.Background()
	var wg sync.WaitGroup
	errs := make([]error, 12)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = f.priority.ReservePriorityFunding(ctx, f.command(fmt.Sprintf("concurrent-%d", i), 0.1))
		}(i)
	}
	wg.Wait()
	winners := 0
	for i, err := range errs {
		if err == nil {
			winners++
			require.NoError(t, f.priority.ReleasePriorityFunding(ctx, fmt.Sprintf("concurrent-%d", i), f.key.ID))
			require.NoError(t, f.priority.ReleasePriorityFunding(ctx, fmt.Sprintf("concurrent-%d", i), f.key.ID))
		} else {
			require.ErrorIs(t, err, service.ErrTotalLimitExceeded)
		}
	}
	require.Equal(t, 6, winners)
	first, err := f.client.UserSubscription.Get(ctx, f.first)
	require.NoError(t, err)
	require.Zero(t, first.DailyReservedUsd)
	second, err := f.client.UserSubscription.Get(ctx, f.second)
	require.NoError(t, err)
	require.Zero(t, second.TotalReservedUsd)
}

func TestPriorityFundingActualLargerThanHold(t *testing.T) {
	f := newPriorityFixture(t, true)
	ctx := context.Background()
	baseline := service.BuildBalancePreauthorizationBaselineKey(f.key.UserID, f.key.ID, "priority", 0, "")
	require.NoError(t, f.repo.(service.BalancePreauthorizationBaselineStore).RecordPreauthorizationBaseline(ctx, baseline, 0.1))
	guard, err := f.preauthorizer.PreauthorizeWithFunding(ctx, service.BalancePreauthorizationRequest{RequestID: "larger-actual",
		APIKeyID: f.key.ID, UserID: f.key.UserID, SubscriptionID: f.first, BillingType: service.BillingTypeSubscription, AuthorizationFingerprint: "payload"}, f.key, nil)
	require.NoError(t, err)
	cmd := f.charge("larger-actual", 0.8)
	result, err := f.repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.Equal(t, 0.15, result.PriorityFunding.WalletAmount)
	require.NoError(t, guard.Finalize(ctx, 0.8, cmd.RequestFingerprint))
	_, err = repository.NewUsageBalanceSettlementRepository(f.db).FlushPendingBalanceSettlements(ctx, 1000)
	require.NoError(t, err)
	user, err := f.client.User.Get(ctx, f.key.UserID)
	require.NoError(t, err)
	require.Equal(t, 9.85, user.Balance)
}

func TestPriorityFundingCancellationRefundsEverySource(t *testing.T) {
	f := newPriorityFixture(t, true)
	ctx := context.Background()
	guard := f.authorize(t, "cancel-split", 0.8)
	balance, exists, err := f.wallet.GetLiveBalance(ctx, f.key.UserID)
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, 9.85, balance)
	require.NoError(t, guard.Refund(ctx))
	require.NoError(t, guard.Refund(ctx))
	f.assertReleased(t)
	balance, _, err = f.wallet.GetLiveBalance(ctx, f.key.UserID)
	require.NoError(t, err)
	require.Equal(t, 10.0, balance)
}

func TestPriorityFundingInsufficientWalletRollsBackAllHolds(t *testing.T) {
	f := newPriorityFixture(t, true)
	ctx := context.Background()
	_, err := f.client.User.UpdateOneID(f.key.UserID).SetBalance(0.1).Save(ctx)
	require.NoError(t, err)
	baseline := service.BuildBalancePreauthorizationBaselineKey(f.key.UserID, f.key.ID, "priority", 0, "")
	require.NoError(t, f.repo.(service.BalancePreauthorizationBaselineStore).RecordPreauthorizationBaseline(ctx, baseline, 0.8))
	guard, err := f.preauthorizer.PreauthorizeWithFunding(ctx, service.BalancePreauthorizationRequest{
		RequestID: "insufficient-wallet", APIKeyID: f.key.ID, UserID: f.key.UserID, SubscriptionID: f.first,
		BillingType: service.BillingTypeSubscription, AuthorizationFingerprint: "payload",
	}, f.key, nil)
	require.ErrorIs(t, err, service.ErrBalanceWithholdingFailed)
	require.Nil(t, guard)
	f.assertReleased(t)
	balance, _, err := f.wallet.GetLiveBalance(ctx, f.key.UserID)
	require.NoError(t, err)
	require.Equal(t, 0.1, balance)
}

func TestPriorityFundingWalletPermissionAndUncoveredUsage(t *testing.T) {
	f := newPriorityFixture(t, false)
	ctx := context.Background()
	_, err := f.priority.ReservePriorityFunding(ctx, f.command("no-wallet-admission", 0.8))
	require.ErrorIs(t, err, service.ErrTotalLimitExceeded)
	f.assertReleased(t)
	guard := f.authorize(t, "no-wallet-actual", 0.1)
	cmd := f.charge("no-wallet-actual", 0.8)
	result, err := f.repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.Equal(t, 0.15, result.PriorityFunding.UncoveredAmount)
	require.Zero(t, result.PriorityFunding.WalletAmount)
	require.NoError(t, guard.Finalize(ctx, 0.8, cmd.RequestFingerprint))
	f.assertReleased(t)
	user, err := f.client.User.Get(ctx, f.key.UserID)
	require.NoError(t, err)
	require.Equal(t, 10.0, user.Balance)
}

func TestPriorityFundingWalletActualAboveEstimateAndZeroCost(t *testing.T) {
	for _, actual := range []float64{0, 0.95} {
		t.Run(fmt.Sprint(actual), func(t *testing.T) {
			f := newPriorityFixture(t, true)
			ctx := context.Background()
			guard := f.authorize(t, "wallet-reconcile", 0.8)
			cmd := f.charge("wallet-reconcile", actual)
			result, err := f.repo.Apply(ctx, cmd)
			require.NoError(t, err)
			expected := max(0, service.QuantizeUsageBillingAmount(actual-0.65))
			require.Equal(t, expected, result.PriorityFunding.WalletAmount)
			require.NoError(t, guard.Finalize(ctx, actual, cmd.RequestFingerprint))
			balance, _, err := f.wallet.GetLiveBalance(ctx, f.key.UserID)
			require.NoError(t, err)
			require.Equal(t, 10-expected, balance)
			_, err = repository.NewUsageBalanceSettlementRepository(f.db).FlushPendingBalanceSettlements(ctx, 1000)
			require.NoError(t, err)
			user, err := f.client.User.Get(ctx, f.key.UserID)
			require.NoError(t, err)
			require.Equal(t, 10-expected, user.Balance)
			f.assertReleased(t)
		})
	}
}

func TestPriorityFundingMinimumRemainingWindowAndFractionalCent(t *testing.T) {
	f := newPriorityFixture(t, true)
	ctx := context.Background()
	first, err := f.client.UserSubscription.Get(ctx, f.first)
	require.NoError(t, err)
	_, err = f.client.SubscriptionPlanVersion.UpdateOneID(first.PlanVersionID).SetWeeklyLimitUsd(0.00000001).Save(ctx)
	require.NoError(t, err)
	result, err := f.repo.Apply(ctx, f.charge("fractional-cent", 0.20000002))
	require.NoError(t, err)
	require.Len(t, result.PriorityFunding.Allocations, 2)
	require.Equal(t, 0.00000001, result.PriorityFunding.Allocations[0].Amount)
	require.Equal(t, 0.2, result.PriorityFunding.Allocations[1].Amount)
	require.Equal(t, 0.00000001, result.PriorityFunding.WalletAmount)
}

func TestPriorityFundingAsyncResumeAndExpiredRecovery(t *testing.T) {
	f := newPriorityFixture(t, true)
	ctx := context.Background()
	id := service.GrokVideoHoldRequestID("split-video")
	f.authorize(t, id, 0.8)
	pending := service.GrokVideoPendingBilling{Model: "grok-imagine-video", PreauthorizationRequestID: id,
		PreauthorizationHoldAmount: 0.8, AuthorizationFingerprint: "payload", FundingSource: service.FundingSourceSubscription,
		SubscriptionID: &f.first, PriorityFunding: true}
	store := f.repo.(service.GrokVideoPendingBillingRepository)
	require.NoError(t, store.BindGrokVideoPendingBilling(ctx, service.GrokVideoPendingBillingBinding{
		TaskID: "split-video-task", UserID: f.key.UserID, APIKeyID: f.key.ID, Pending: pending,
	}))
	loaded, err := store.LoadGrokVideoPendingBilling(ctx, "split-video-task", f.key.UserID, f.key.ID)
	require.NoError(t, err)
	require.Equal(t, pending, *loaded)
	resume, err := f.preauthorizer.Resume(ctx, service.BalancePreauthorizationResumeRequest{RequestID: loaded.PreauthorizationRequestID,
		APIKeyID: f.key.ID, UserID: f.key.UserID, SubscriptionID: *loaded.SubscriptionID, BillingType: service.BillingTypeSubscription,
		AuthorizationFingerprint: loaded.AuthorizationFingerprint, HoldAmount: loaded.PreauthorizationHoldAmount})
	require.NoError(t, err)
	require.NoError(t, resume.Refund(ctx))
	f.assertReleased(t)
	// A process that disappears before handing off must leave recoverable holds.
	f.authorize(t, "expired-parent", 0.8)
	_, err = f.db.Exec(`UPDATE billing_priority_reservations SET expires_at = NOW() - INTERVAL '1 minute' WHERE api_key_id = $1 AND request_id = 'expired-parent'`, f.key.ID)
	require.NoError(t, err)
	records, err := f.priority.ListRecoverablePriorityFunding(ctx, time.Now(), 500)
	require.NoError(t, err)
	found := false
	for _, record := range records {
		if record.APIKeyID == f.key.ID && record.RequestID == "expired-parent" {
			found = true
			require.NoError(t, f.priority.ReleasePriorityFunding(ctx, record.RequestID, record.APIKeyID))
		}
	}
	require.True(t, found)
	f.assertReleased(t)
	record, err := f.priority.GetPriorityFunding(ctx, "expired-parent", f.key.ID)
	require.NoError(t, err)
	require.NoError(t, f.preauthorizer.RecoverBalancePreauthorization(ctx, *record.WalletRecord))
	balance, _, err := f.wallet.GetLiveBalance(ctx, f.key.UserID)
	require.NoError(t, err)
	require.Equal(t, 10.0, balance)
}
