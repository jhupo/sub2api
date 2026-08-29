package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type guardedUsageBillingApplyRepoStub struct {
	UsageBillingRepository
	result  *UsageBillingApplyResult
	err     error
	lastCmd *UsageBillingCommand
}

func (s *guardedUsageBillingApplyRepoStub) Apply(_ context.Context, cmd *UsageBillingCommand) (*UsageBillingApplyResult, error) {
	s.lastCmd = cmd
	return s.result, s.err
}

func guardedUsageBillingParams(actual float64) (*UsageLog, *postUsageBillingParams, *billingDeps) {
	apiKey := &APIKey{ID: 7, UserID: 42, User: &User{ID: 42}}
	account := &Account{ID: 11}
	usageLog := &UsageLog{
		RequestID:    "request-1",
		UserID:       42,
		APIKeyID:     7,
		AccountID:    11,
		Model:        "gpt-test",
		BillingType:  BillingTypeBalance,
		InputTokens:  100,
		OutputTokens: 20,
		ActualCost:   actual,
	}
	params := &postUsageBillingParams{
		Cost:    &CostBreakdown{ActualCost: actual, TotalCost: actual},
		User:    apiKey.User,
		APIKey:  apiKey,
		Account: account,
	}
	deps := &billingDeps{deferredService: &DeferredService{}}
	return usageLog, params, deps
}

func TestApplyUsageBillingGuardedDuplicateStillFinalizesReservation(t *testing.T) {
	fixture := newPreauthorizationFixture()
	handlerGuard, err := fixture.service.Preauthorize(context.Background(), balancePreauthorizationTestRequest())
	require.NoError(t, err)
	workerGuard, ok := handlerGuard.TransferToWorker()
	require.True(t, ok)

	usageLog, params, deps := guardedUsageBillingParams(0.02)
	repo := &guardedUsageBillingApplyRepoStub{result: &UsageBillingApplyResult{Applied: false}}
	ctx := ContextWithBalancePreauthorizationGuard(context.Background(), workerGuard)
	applied, err := applyUsageBilling(ctx, "request-1", usageLog, params, deps, repo)

	require.NoError(t, err)
	require.False(t, applied)
	require.NotNil(t, repo.lastCmd)
	require.True(t, repo.lastCmd.BalancePreauthorized)
	require.InDelta(t, 0.02, fixture.repo.finalizedAmount, 1e-12)
	require.Equal(t, repo.lastCmd.RequestFingerprint, fixture.repo.finalizedFingerprint)
	require.InDelta(t, 0.02, fixture.wallet.lastActual, 1e-12)
	require.False(t, workerGuard.IsCurrentOwner())
	// The handler's stale defer must not refund a worker-owned settlement.
	require.NoError(t, handlerGuard.Refund(context.Background()))
	require.NotContains(t, fixture.recorder.snapshot(), "repo_begin_refund")
}

func TestApplyUsageBillingGuardedApplyErrorKeepsActiveHold(t *testing.T) {
	fixture := newPreauthorizationFixture()
	handlerGuard, err := fixture.service.Preauthorize(context.Background(), balancePreauthorizationTestRequest())
	require.NoError(t, err)
	workerGuard, ok := handlerGuard.TransferToWorker()
	require.True(t, ok)

	usageLog, params, deps := guardedUsageBillingParams(0.02)
	applyErr := errors.New("database transaction failed")
	repo := &guardedUsageBillingApplyRepoStub{err: applyErr}
	ctx := ContextWithBalancePreauthorizationGuard(context.Background(), workerGuard)
	applied, err := applyUsageBilling(ctx, "request-1", usageLog, params, deps, repo)

	require.False(t, applied)
	require.ErrorIs(t, err, applyErr)
	require.NotNil(t, repo.lastCmd)
	require.True(t, repo.lastCmd.BalancePreauthorized)
	require.Zero(t, fixture.wallet.finalizeCalls)
	require.True(t, workerGuard.IsCurrentOwner())
	require.NotContains(t, fixture.recorder.snapshot(), "repo_begin_refund")
}

func TestApplyUsageBillingGuardedNilApplyResultKeepsActiveHold(t *testing.T) {
	fixture := newPreauthorizationFixture()
	handlerGuard, err := fixture.service.Preauthorize(context.Background(), balancePreauthorizationTestRequest())
	require.NoError(t, err)
	workerGuard, ok := handlerGuard.TransferToWorker()
	require.True(t, ok)

	usageLog, params, deps := guardedUsageBillingParams(0.02)
	repo := &guardedUsageBillingApplyRepoStub{}
	ctx := ContextWithBalancePreauthorizationGuard(context.Background(), workerGuard)
	applied, err := applyUsageBilling(ctx, "request-1", usageLog, params, deps, repo)

	require.False(t, applied)
	require.ErrorIs(t, err, ErrBillingServiceUnavailable)
	require.NotNil(t, repo.lastCmd)
	require.Zero(t, fixture.wallet.finalizeCalls)
	require.Zero(t, fixture.wallet.refundCalls)
	require.True(t, workerGuard.IsCurrentOwner())
}

func TestTransferBalancePreauthorizationToUsageTaskAttachesOnlyWorkerOwner(t *testing.T) {
	fixture := newPreauthorizationFixture()
	handlerGuard, err := fixture.service.Preauthorize(context.Background(), balancePreauthorizationTestRequest())
	require.NoError(t, err)

	var taskGuard *BalancePreauthorizationGuard
	wrapped, ok := TransferBalancePreauthorizationToUsageTask(handlerGuard, func(ctx context.Context) {
		taskGuard, _ = BalancePreauthorizationGuardFromContext(ctx)
	})
	require.True(t, ok)
	wrapped(context.Background())

	require.NotNil(t, taskGuard)
	require.True(t, taskGuard.IsCurrentOwner())
	require.True(t, handlerGuard.IsTransferred())
	require.NoError(t, handlerGuard.Refund(context.Background()))
	require.NotContains(t, fixture.recorder.snapshot(), "repo_begin_refund")
}

func TestDuplicateUsageTaskTransferRemainsRunnableWithStaleGuard(t *testing.T) {
	fixture := newPreauthorizationFixture()
	handlerGuard, err := fixture.service.Preauthorize(context.Background(), balancePreauthorizationTestRequest())
	require.NoError(t, err)

	first, ok := TransferBalancePreauthorizationToUsageTask(handlerGuard, func(context.Context) {})
	require.True(t, ok)
	require.NotNil(t, first)

	var duplicateGuard *BalancePreauthorizationGuard
	duplicateRan := false
	duplicate, ok := TransferBalancePreauthorizationToUsageTask(handlerGuard, func(ctx context.Context) {
		duplicateRan = true
		duplicateGuard, _ = BalancePreauthorizationGuardFromContext(ctx)
	})
	require.False(t, ok)
	require.NotNil(t, duplicate, "duplicate side effects must not be silently dropped")
	duplicate(context.Background())

	require.True(t, duplicateRan)
	require.Same(t, handlerGuard, duplicateGuard)
	require.False(t, duplicateGuard.IsCurrentOwner())
}

func TestApplyUsageBillingZeroUsageRefundsHold(t *testing.T) {
	fixture := newPreauthorizationFixture()
	handlerGuard, err := fixture.service.Preauthorize(context.Background(), balancePreauthorizationTestRequest())
	require.NoError(t, err)
	workerGuard, ok := handlerGuard.TransferToWorker()
	require.True(t, ok)

	usageLog, params, deps := guardedUsageBillingParams(0)
	usageLog.InputTokens = 0
	usageLog.OutputTokens = 0
	repo := &guardedUsageBillingApplyRepoStub{result: &UsageBillingApplyResult{Applied: false}}
	ctx := ContextWithBalancePreauthorizationGuard(context.Background(), workerGuard)
	_, err = applyUsageBilling(ctx, "request-1", usageLog, params, deps, repo)

	require.NoError(t, err)
	require.NotNil(t, repo.lastCmd)
	require.Greater(t, handlerGuard.HoldAmount(), 0.0)
	require.Zero(t, repo.lastCmd.BalanceCost)
	require.Zero(t, usageLog.ActualCost)
	require.Equal(t, 1, fixture.wallet.refundCalls)
	require.Zero(t, fixture.wallet.finalizeCalls)
}

func TestApplyUsageBillingZeroUsageRefundsStreamingTopUps(t *testing.T) {
	fixture := newPreauthorizationFixture()
	fixture.calculator.outputUnitPrice = 0.001
	handlerGuard, err := fixture.service.Preauthorize(context.Background(), balancePreauthorizationTestRequest())
	require.NoError(t, err)
	initialHold := handlerGuard.HoldAmount()

	for i := 0; i < 24; i++ {
		require.NoError(t, handlerGuard.ObserveStreamingOutput(context.Background(), 64))
	}
	require.Greater(t, handlerGuard.HoldAmount(), initialHold)
	require.Greater(t, fixture.wallet.topUpCalls, 0)

	workerGuard, ok := handlerGuard.TransferToWorker()
	require.True(t, ok)
	usageLog, params, deps := guardedUsageBillingParams(0)
	usageLog.InputTokens = 0
	usageLog.OutputTokens = 0
	repo := &guardedUsageBillingApplyRepoStub{result: &UsageBillingApplyResult{Applied: false}}
	ctx := ContextWithBalancePreauthorizationGuard(context.Background(), workerGuard)

	_, err = applyUsageBilling(ctx, "request-1", usageLog, params, deps, repo)

	require.NoError(t, err)
	require.Zero(t, repo.lastCmd.BalanceCost)
	require.Zero(t, usageLog.ActualCost)
	require.Equal(t, 1, fixture.wallet.refundCalls)
	require.Zero(t, fixture.wallet.finalizeCalls)
}

func TestApplyUsageBillingKeepsTrulyFreeZeroHoldFree(t *testing.T) {
	fixture := newPreauthorizationFixture()
	fixture.calculator.zero = true
	handlerGuard, err := fixture.service.Preauthorize(context.Background(), balancePreauthorizationTestRequest())
	require.NoError(t, err)
	require.Zero(t, handlerGuard.HoldAmount())
	workerGuard, ok := handlerGuard.TransferToWorker()
	require.True(t, ok)

	usageLog, params, deps := guardedUsageBillingParams(0)
	repo := &guardedUsageBillingApplyRepoStub{result: &UsageBillingApplyResult{Applied: false}}
	ctx := ContextWithBalancePreauthorizationGuard(context.Background(), workerGuard)
	_, err = applyUsageBilling(ctx, "request-1", usageLog, params, deps, repo)

	require.NoError(t, err)
	require.Zero(t, repo.lastCmd.BalanceCost)
	require.Zero(t, usageLog.ActualCost)
	require.Equal(t, 1, fixture.wallet.refundCalls)
	require.Zero(t, fixture.wallet.finalizeCalls)
}

func TestApplyUsageBillingStaleGuardRejectsBeforeRepositoryApply(t *testing.T) {
	fixture := newPreauthorizationFixture()
	handlerGuard, err := fixture.service.Preauthorize(context.Background(), balancePreauthorizationTestRequest())
	require.NoError(t, err)
	workerGuard, ok := handlerGuard.TransferToWorker()
	require.True(t, ok)

	usageLog, params, deps := guardedUsageBillingParams(0.02)
	repo := &guardedUsageBillingApplyRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	staleCtx := ContextWithBalancePreauthorizationGuard(context.Background(), handlerGuard)
	_, err = applyUsageBilling(staleCtx, "request-1", usageLog, params, deps, repo)

	require.ErrorIs(t, err, ErrBalancePreauthorizationOwnershipTransferred)
	require.Nil(t, repo.lastCmd)
	require.True(t, workerGuard.IsCurrentOwner())
	require.Zero(t, fixture.wallet.finalizeCalls)
	require.Zero(t, fixture.wallet.refundCalls)
}

func TestApplyUsageBillingGuardedTopUpPrecedesRepositoryApply(t *testing.T) {
	fixture := newPreauthorizationFixture()
	handlerGuard, err := fixture.service.Preauthorize(context.Background(), balancePreauthorizationTestRequest())
	require.NoError(t, err)
	workerGuard, ok := handlerGuard.TransferToWorker()
	require.True(t, ok)

	usageLog, params, deps := guardedUsageBillingParams(workerGuard.HoldAmount() + 0.25)
	repo := &guardedUsageBillingApplyRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	_, err = applyUsageBilling(ContextWithBalancePreauthorizationGuard(context.Background(), workerGuard), "request-1", usageLog, params, deps, repo)

	require.NoError(t, err)
	require.Equal(t, 1, fixture.wallet.topUpCalls)
	require.NotNil(t, repo.lastCmd)
	require.InDelta(t, repo.lastCmd.BalanceCost, fixture.wallet.lastTopUpTarget, 1e-12)
	require.Equal(t, 1, fixture.wallet.finalizeCalls)
}

func TestApplyUsageBillingGuardedInsufficientTopUpSkipsApplyAndFinalization(t *testing.T) {
	fixture := newPreauthorizationFixture()
	fixture.wallet.topUp = []LiveBalanceResult{{Outcome: LiveBalanceOutcomeInsufficient, State: LiveBalanceAttemptAuthorized}}
	handlerGuard, err := fixture.service.Preauthorize(context.Background(), balancePreauthorizationTestRequest())
	require.NoError(t, err)
	workerGuard, ok := handlerGuard.TransferToWorker()
	require.True(t, ok)

	usageLog, params, deps := guardedUsageBillingParams(workerGuard.HoldAmount() + 0.25)
	repo := &guardedUsageBillingApplyRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	_, err = applyUsageBilling(ContextWithBalancePreauthorizationGuard(context.Background(), workerGuard), "request-1", usageLog, params, deps, repo)

	require.ErrorIs(t, err, ErrBalanceWithholdingFailed)
	require.Nil(t, repo.lastCmd)
	require.Zero(t, fixture.wallet.finalizeCalls)
	require.True(t, workerGuard.IsCurrentOwner())
}

func TestApplyUsageBillingFinalizationPendingRetryUsesDurableActualHold(t *testing.T) {
	fixture := newPreauthorizationFixture()
	fixture.repo.prepareRecord = &BalancePreauthorizationRecord{
		RequestID:                "request-1",
		APIKeyID:                 7,
		UserID:                   42,
		AuthorizationFingerprint: "auth-fingerprint",
		HoldAmount:               0.03,
		Amount:                   0.08,
		Status:                   BalanceSettlementFinalizationPending,
	}
	guard, err := fixture.service.Resume(context.Background(), BalancePreauthorizationResumeRequest{
		RequestID: "request-1", APIKeyID: 7, UserID: 42,
		AuthorizationFingerprint: "auth-fingerprint", HoldAmount: 0.03,
	})
	require.NoError(t, err)
	require.InDelta(t, 0.08, guard.HoldAmount(), 1e-12)

	usageLog, params, deps := guardedUsageBillingParams(0.08)
	repo := &guardedUsageBillingApplyRepoStub{result: &UsageBillingApplyResult{Applied: false}}
	_, err = applyUsageBilling(ContextWithBalancePreauthorizationGuard(context.Background(), guard), "request-1", usageLog, params, deps, repo)

	require.NoError(t, err)
	require.Zero(t, fixture.wallet.topUpCalls)
	require.NotNil(t, repo.lastCmd)
	require.Equal(t, 1, fixture.wallet.finalizeCalls)
	require.Equal(t, 1, fixture.repo.completeSettlementCalls)
}

func TestBalancePreauthorizationSearchCostsMatchSettlementCalculators(t *testing.T) {
	groupID := int64(9)
	webSearchPrice := 0.02
	searchPricePer1k := 7.5
	group := &Group{
		ID:                    groupID,
		RateMultiplier:        2.5,
		WebSearchPricePerCall: &webSearchPrice,
		SearchPricePer1k:      &searchPricePer1k,
		SubscriptionType:      "subscription",
		PeakRateEnabled:       true,
		PeakStart:             "00:00",
		PeakEnd:               "23:59",
		PeakRateMultiplier:    3,
	}
	apiKey := &APIKey{ID: 7, UserID: 42, GroupID: &groupID, Group: group, User: &User{ID: 42}}
	cfg := &config.Config{}
	billing := NewBillingService(cfg, nil)
	pricingAt := time.Date(2026, 6, 29, 12, 0, 0, 0, time.Local)

	openAI := &OpenAIGatewayService{cfg: cfg, billingService: billing}
	webSearchCost, err := openAI.BalancePreauthorizationWebSearchCost(context.Background(), apiKey, pricingAt)
	require.NoError(t, err)
	require.InDelta(t, 0.05, webSearchCost, 1e-12)
	require.InDelta(t,
		billing.CalculateWebSearchCost(1, &webSearchPrice, group.RateMultiplier).ActualCost,
		webSearchCost, 1e-12,
	)

	gateway := &GatewayService{cfg: cfg, billingService: billing}
	searchCost, err := gateway.BalancePreauthorizationSearchCost(context.Background(), apiKey, pricingAt)
	require.NoError(t, err)
	textMultiplier, _ := computePeakAwareMultipliers(apiKey, group.RateMultiplier, pricingAt)
	require.InDelta(t, 0.05625, searchCost, 1e-12)
	require.InDelta(t,
		billing.CalculateSearchCost(1, &searchPricePer1k, textMultiplier).ActualCost,
		searchCost, 1e-12,
	)
}

func TestBalancePreauthorizationAudioCostMatchesAudioSettlement(t *testing.T) {
	groupID := int64(9)
	ttsPrice := 15.0
	group := &Group{ID: groupID, RateMultiplier: 2, AudioTTSPricePerMillionChars: &ttsPrice}
	apiKey := &APIKey{ID: 7, UserID: 42, GroupID: &groupID, Group: group, User: &User{ID: 42}}
	cfg := &config.Config{}
	billing := NewBillingService(cfg, nil)
	openAI := &OpenAIGatewayService{cfg: cfg, billingService: billing}

	units := 5.0 / 1_000_000.0
	actual, err := openAI.BalancePreauthorizationAudioCost(context.Background(), apiKey, "tts", "tts", units, time.Unix(1000, 0))
	require.NoError(t, err)
	want := billing.CalculateAudioCost("tts", units, groupAudioPriceConfigFromAPIKey(apiKey), group.RateMultiplier)
	require.InDelta(t, want.ActualCost, actual, 1e-12)
	_, err = openAI.BalancePreauthorizationAudioCost(context.Background(), apiKey, "tts", "invalid", units, time.Unix(1000, 0))
	require.Error(t, err)
}
