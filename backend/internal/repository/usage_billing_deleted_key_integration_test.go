//go:build integration

package repository

import (
	"context"
	"database/sql"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestUsageBillingDeletedKeyCompletesAndDeduplicatesSettlement(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{
		Email: "deleted-key-" + uuid.NewString() + "@example.test", PasswordHash: "hash", Balance: 100,
	})
	key := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID, Key: "sk-deleted-" + uuid.NewString(), Name: "in-flight", Quota: 5,
	})
	account := mustCreateAccount(t, client, &service.Account{
		Name: "deleted-key-account-" + uuid.NewString(), Type: service.AccountTypeAPIKey,
	})
	_, err := integrationDB.ExecContext(ctx, "UPDATE api_keys SET deleted_at = NOW() WHERE id = $1", key.ID)
	require.NoError(t, err)
	repo := NewUsageBillingRepository(client, integrationDB)
	cmd := &service.UsageBillingCommand{
		RequestID: uuid.NewString(), APIKeyID: key.ID, UserID: user.ID,
		AccountID: account.ID, AccountType: service.AccountTypeAPIKey,
		BalanceCost: 1.25, APIKeyQuotaCost: 1.25, APIKeyRateLimitCost: 1.25, AccountQuotaCost: 0.8,
	}
	first, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.True(t, first.Applied)
	require.False(t, first.APIKeyQuotaExhausted)
	second, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.False(t, second.Applied)
	requireSingleBalanceSettlement(t, ctx, user.ID, 1.25)
	var balance, accountUsed, keyQuota, keyRate float64
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id = $1", user.ID).Scan(&balance))
	require.InDelta(t, 98.75, balance, 0.000001)
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT (extra->>'quota_used')::numeric FROM accounts WHERE id = $1", account.ID).Scan(&accountUsed))
	require.InDelta(t, 0.8, accountUsed, 0.000001)
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT quota_used, usage_5h FROM api_keys WHERE id = $1", key.ID).Scan(&keyQuota, &keyRate))
	require.Zero(t, keyQuota)
	require.Zero(t, keyRate)
}

func TestUsageBillingDeletedKeyCompletesPreauthorizedBalanceSettlementOnce(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{
		Email: "deleted-key-preauthorized-" + uuid.NewString() + "@example.test", PasswordHash: "hash", Balance: 100,
	})
	key := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID, Key: "sk-deleted-preauthorized-" + uuid.NewString(), Name: "in-flight", Quota: 5,
	})
	account := mustCreateAccount(t, client, &service.Account{
		Name: "deleted-key-preauthorized-account-" + uuid.NewString(), Type: service.AccountTypeAPIKey,
		Extra: map[string]any{"quota_daily_limit": 0.5},
	})
	repo := NewUsageBillingRepository(client, integrationDB)
	requestID := uuid.NewString()
	authorizationFingerprint := uuid.NewString()
	prepared, err := repo.PrepareBalancePreauthorization(ctx, &service.BalancePreauthorizationCommand{
		RequestID: requestID, APIKeyID: key.ID, UserID: user.ID,
		AuthorizationFingerprint: authorizationFingerprint, HoldAmount: 2,
	})
	require.NoError(t, err)
	require.Equal(t, service.BalanceSettlementPrepared, prepared.Status)
	require.NoError(t, repo.MarkBalancePreauthorizationAuthorized(ctx, requestID, key.ID))
	_, err = integrationDB.ExecContext(ctx, "UPDATE api_keys SET deleted_at = NOW() WHERE id = $1", key.ID)
	require.NoError(t, err)

	cmd := &service.UsageBillingCommand{
		RequestID: requestID, APIKeyID: key.ID, UserID: user.ID,
		AccountID: account.ID, AccountType: service.AccountTypeAPIKey,
		BalanceCost: 1.25, BalancePreauthorized: true,
		APIKeyQuotaCost: 1.25, APIKeyRateLimitCost: 1.25, AccountQuotaCost: 0.8,
	}
	first, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.True(t, first.Applied)
	require.True(t, first.BalanceFinalizationPending)
	require.False(t, first.APIKeyQuotaExhausted)
	require.NotNil(t, first.QuotaState)
	require.InDelta(t, 0.8, first.QuotaState.DailyUsed, 0.000001)

	assertPersistentState := func(wantStatus int16, wantPreapplied bool, wantBalance float64, wantAttempts int) {
		t.Helper()
		var status int16
		var amount, hold float64
		var fingerprint, authorization string
		var preapplied bool
		var attempts int
		var appliedAt sql.NullTime
		require.NoError(t, integrationDB.QueryRowContext(ctx, `
			SELECT status, amount_usd, hold_usd, request_fingerprint,
				authorization_fingerprint, wallet_preapplied, attempts, applied_at
			FROM billing_balance_settlements WHERE request_id = $1 AND api_key_id = $2
		`, requestID, key.ID).Scan(&status, &amount, &hold, &fingerprint, &authorization, &preapplied, &attempts, &appliedAt))
		require.Equal(t, wantStatus, status)
		require.InDelta(t, 1.25, amount, 0.000001)
		require.InDelta(t, 2, hold, 0.000001)
		require.Equal(t, cmd.RequestFingerprint, fingerprint)
		require.Equal(t, authorizationFingerprint, authorization)
		require.Equal(t, wantPreapplied, preapplied)
		require.Equal(t, wantAttempts, attempts)
		require.Equal(t, wantStatus == service.BalanceSettlementApplied, appliedAt.Valid)

		var balance, accountUsed, dailyUsed, keyQuota, keyRate float64
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT balance FROM users WHERE id = $1", user.ID).Scan(&balance))
		require.InDelta(t, wantBalance, balance, 0.000001)
		require.NoError(t, integrationDB.QueryRowContext(ctx, `
			SELECT (extra->>'quota_used')::numeric, (extra->>'quota_daily_used')::numeric
			FROM accounts WHERE id = $1
		`, account.ID).Scan(&accountUsed, &dailyUsed))
		require.InDelta(t, 0.8, accountUsed, 0.000001)
		require.InDelta(t, 0.8, dailyUsed, 0.000001)
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT quota_used, usage_5h FROM api_keys WHERE id = $1", key.ID).Scan(&keyQuota, &keyRate))
		require.Zero(t, keyQuota)
		require.Zero(t, keyRate)

		var settlementCount, dedupCount, schedulerEvents, walletEvents int
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM billing_balance_settlements WHERE request_id = $1 AND api_key_id = $2", requestID, key.ID).Scan(&settlementCount))
		require.Equal(t, 1, settlementCount)
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id = $1 AND api_key_id = $2", requestID, key.ID).Scan(&dedupCount))
		require.Equal(t, 1, dedupCount)
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM scheduler_outbox WHERE account_id = $1 AND event_type = $2", account.ID, service.SchedulerOutboxEventAccountChanged).Scan(&schedulerEvents))
		require.Equal(t, 1, schedulerEvents)
		require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM live_balance_adjustment_outbox WHERE user_id = $1", user.ID).Scan(&walletEvents))
		require.Zero(t, walletEvents, "preauthorized database catch-up must not enqueue another wallet debit")
	}

	replay, err := repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.False(t, replay.Applied)
	settlements := NewUsageBalanceSettlementRepository(integrationDB)
	pending, err := settlements.FlushPendingBalanceSettlements(ctx, 1000)
	require.NoError(t, err)
	require.Empty(t, pending, "finalization_pending must not be consumed as a completed authorization")
	assertPersistentState(service.BalanceSettlementFinalizationPending, false, 100, 0)

	// Exercise the repository completion acknowledgement, without modeling the live wallet.
	require.NoError(t, repo.CompleteBalancePreauthorizationSettlement(ctx, requestID, key.ID))
	require.NoError(t, repo.CompleteBalancePreauthorizationSettlement(ctx, requestID, key.ID))
	assertPersistentState(service.BalanceSettlementPending, true, 100, 0)
	applied, err := settlements.FlushPendingBalanceSettlements(ctx, 1000)
	require.NoError(t, err)
	require.Len(t, applied, 1)
	require.Equal(t, user.ID, applied[0].UserID)
	require.Equal(t, 1, applied[0].EventCount)
	require.InDelta(t, 1.25, applied[0].Amount, 0.000001)
	require.InDelta(t, 98.75, applied[0].NewBalance, 0.000001)

	replay, err = repo.Apply(ctx, cmd)
	require.NoError(t, err)
	require.False(t, replay.Applied)
	require.NoError(t, repo.CompleteBalancePreauthorizationSettlement(ctx, requestID, key.ID))
	applied, err = settlements.FlushPendingBalanceSettlements(ctx, 1000)
	require.NoError(t, err)
	require.Empty(t, applied)
	assertPersistentState(service.BalanceSettlementApplied, true, 98.75, 1)
}
