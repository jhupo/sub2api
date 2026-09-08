package service

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type codexAdaptiveSettingRepoStub struct {
	SettingRepository
	value string
	block <-chan struct{}
}

func (s *codexAdaptiveSettingRepoStub) GetValue(ctx context.Context, key string) (string, error) {
	if key != SettingKeyCodexAdaptiveSchedulingSettings {
		return "", ErrSettingNotFound
	}
	if s.block != nil {
		select {
		case <-s.block:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	if s.value == "" {
		return "", ErrSettingNotFound
	}
	return s.value, nil
}

func (s *codexAdaptiveSettingRepoStub) Set(_ context.Context, key, value string) error {
	if key == SettingKeyCodexAdaptiveSchedulingSettings {
		s.value = value
	}
	return nil
}

type codexAdaptivePressureCacheStub struct {
	ConcurrencyCache
	pressureByModel map[string]map[int64]int
	batchCalls      map[string]int
	batchErr        error
	failures        int
	successes       int
}

func (s *codexAdaptivePressureCacheStub) ObserveCodexAdaptiveFailure(_ context.Context, accountID int64, model, _ string, _ time.Duration) (int, error) {
	s.failures++
	if s.pressureByModel == nil {
		s.pressureByModel = make(map[string]map[int64]int)
	}
	if s.pressureByModel[model] == nil {
		s.pressureByModel[model] = make(map[int64]int)
	}
	s.pressureByModel[model][accountID]++
	return s.pressureByModel[model][accountID], nil
}

func (s *codexAdaptivePressureCacheStub) ObserveCodexAdaptiveSuccess(_ context.Context, accountID int64, model, _ string, _ time.Duration) (int, error) {
	s.successes++
	if byAccount := s.pressureByModel[model]; byAccount != nil {
		delete(byAccount, accountID)
	}
	return 0, nil
}

func (s *codexAdaptivePressureCacheStub) GetCodexAdaptivePressureBatch(_ context.Context, accountIDs []int64, model string, _ time.Duration) (map[int64]int, error) {
	if s.batchCalls == nil {
		s.batchCalls = make(map[string]int)
	}
	s.batchCalls[model]++
	if s.batchErr != nil {
		return nil, s.batchErr
	}
	result := make(map[int64]int, len(accountIDs))
	for _, accountID := range accountIDs {
		result[accountID] = s.pressureByModel[model][accountID]
	}
	return result, nil
}

func codexAdaptivePolicyContext() context.Context {
	return context.WithValue(context.Background(), codexAdaptiveRequestContextKey{}, &codexAdaptiveRequestState{
		model:         "gpt-5.6-sol",
		sessionMember: "session-member",
		pressures:     make(map[codexAdaptivePressureScope]int),
	})
}

func codexAdaptiveCapacityShedError() *UpstreamFailoverError {
	return &UpstreamFailoverError{
		StatusCode:             http.StatusServiceUnavailable,
		ResponseBody:           []byte(`{"error":{"code":"server_is_overloaded"}}`),
		RequestScopedTransient: true,
		Reason:                 openAIUpstreamCapacityShedReason,
	}
}

func TestCodexAdaptiveAccountEligibility(t *testing.T) {
	tests := []struct {
		name    string
		account *Account
		want    bool
	}{
		{"oauth", &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}, true},
		{"setup token", &Account{Platform: PlatformOpenAI, Type: AccountTypeSetupToken}, true},
		{"official api key default URL", &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}, true},
		{"official api key explicit URL", &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://api.openai.com/v1"}}, true},
		{"official api key explicit TLS port", &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://api.openai.com:443/v1"}}, true},
		{"openai hostname over plaintext", &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "http://api.openai.com/v1"}}, false},
		{"openai hostname on custom port", &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://api.openai.com:8443/v1"}}, false},
		{"spoofed openai hostname", &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://api.openai.com.relay.example/v1"}}, false},
		{"third party api key", &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://relay.example/v1"}}, false},
		{"upstream account", &Account{Platform: PlatformOpenAI, Type: AccountTypeUpstream, Credentials: map[string]any{"base_url": "https://api.openai.com/v1"}}, false},
		{"other platform", &Account{Platform: PlatformAnthropic, Type: AccountTypeOAuth}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, codexAdaptiveAccountEligible(tt.account))
		})
	}
}

func TestCodexAdaptiveConcurrencyNeverRemovesLastSlot(t *testing.T) {
	require.Equal(t, 20, codexAdaptiveConcurrencyLimit(20, 0))
	require.Equal(t, 20, codexAdaptiveConcurrencyLimit(20, 1))
	require.Equal(t, 10, codexAdaptiveConcurrencyLimit(20, 2))
	require.Equal(t, 7, codexAdaptiveConcurrencyLimit(20, 3))
	require.Equal(t, 1, codexAdaptiveConcurrencyLimit(20, 100))
	require.Equal(t, 0, codexAdaptiveConcurrencyLimit(0, 100))
}

func TestCodexAdaptiveLoadFactorChangesOnlyWhenAdmissionIsReduced(t *testing.T) {
	loadFactor := 50
	account := &Account{
		ID:          9,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Concurrency: 20,
		LoadFactor:  &loadFactor,
	}
	svc := &OpenAIGatewayService{}

	require.Equal(t, 50, svc.codexAdaptiveEffectiveLoadFactor(context.Background(), account, "gpt-5.6-sol"))

	ctx := codexAdaptivePolicyContext()
	state := codexAdaptiveRequestFromContext(ctx)
	scope := codexAdaptivePressureScopeFor(account.ID, "gpt-5.6-sol")
	state.pressures[scope] = 1
	require.Equal(t, 50, svc.codexAdaptiveEffectiveLoadFactor(ctx, account, "gpt-5.6-sol"))

	state.pressures[scope] = 2
	require.Equal(t, 10, svc.codexAdaptiveEffectiveLoadFactor(ctx, account, "gpt-5.6-sol"))

	loadFactor = 5
	require.Equal(t, 5, svc.codexAdaptiveEffectiveLoadFactor(ctx, account, "gpt-5.6-sol"))
}

func TestCodexAdaptivePressureIsolatedByAccountAndModel(t *testing.T) {
	ctx := codexAdaptivePolicyContext()
	state := codexAdaptiveRequestFromContext(ctx)
	state.pressures[codexAdaptivePressureScopeFor(1, "gpt-5.6-sol")] = 2
	state.pressures[codexAdaptivePressureScopeFor(1, "gpt-5.6-terra")] = 3

	svc := &OpenAIGatewayService{}
	accountOne := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 20}
	accountTwo := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 20}

	require.Equal(t, 10, svc.codexAdaptiveEffectiveConcurrency(ctx, accountOne, "gpt-5.6-sol", accountOne.Concurrency))
	require.Equal(t, 7, svc.codexAdaptiveEffectiveConcurrency(ctx, accountOne, "gpt-5.6-terra", accountOne.Concurrency))
	require.Equal(t, 20, svc.codexAdaptiveEffectiveConcurrency(ctx, accountTwo, "gpt-5.6-sol", accountTwo.Concurrency))
}

func TestCodexAdaptivePressureReducesAdmissionWithoutClosingAccount(t *testing.T) {
	cache := &codexAdaptivePressureCacheStub{pressureByModel: map[string]map[int64]int{
		"gpt-5.6-sol": {1: 3},
	}}
	svc := &OpenAIGatewayService{concurrencyService: NewConcurrencyService(cache)}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 20}
	ctx := codexAdaptivePolicyContext()

	require.Equal(t, 7, svc.codexAdaptiveEffectiveConcurrency(ctx, account, "gpt-5.6-sol", account.Concurrency))
	require.Equal(t, 1, cache.batchCalls["gpt-5.6-sol"])

	require.Equal(t, 7, svc.codexAdaptiveEffectiveConcurrency(ctx, account, "gpt-5.6-sol", account.Concurrency))
	compactErr := codexAdaptiveCapacityShedError()
	require.True(t, svc.ApplyCodexAdaptiveFailoverPolicy(ctx, account, "gpt-5.6-sol", compactErr))
	require.True(t, compactErr.RetryableOnSameAccount)
	require.Equal(t, 1, compactErr.SameAccountRetryMax)
	require.Equal(t, 1, cache.failures)

	require.Equal(t, 5, svc.codexAdaptiveEffectiveConcurrency(ctx, account, "gpt-5.6-sol", account.Concurrency))
}

func TestCodexAdaptiveSessionPressureIdentityIsolatedByAPIKey(t *testing.T) {
	repo := &codexAdaptiveSettingRepoStub{value: `{"enabled":true}`}
	settingsService := NewSettingService(repo, &config.Config{})
	settingsService.WarmCodexAdaptiveSchedulingSettings(context.Background())
	gateway := &OpenAIGatewayService{settingService: settingsService}

	first := codexAdaptiveRequestFromContext(gateway.PrepareCodexAdaptiveSchedulingRequest(
		context.Background(), 101, "shared-session", "gpt-5.6-sol",
	))
	sameTenant := codexAdaptiveRequestFromContext(gateway.PrepareCodexAdaptiveSchedulingRequest(
		context.Background(), 101, "shared-session", "gpt-5.6-sol",
	))
	otherTenant := codexAdaptiveRequestFromContext(gateway.PrepareCodexAdaptiveSchedulingRequest(
		context.Background(), 202, "shared-session", "gpt-5.6-sol",
	))

	require.NotNil(t, first)
	require.NotNil(t, sameTenant)
	require.NotNil(t, otherTenant)
	require.Equal(t, first.sessionMember, sameTenant.sessionMember)
	require.NotEqual(t, first.sessionMember, otherTenant.sessionMember)
}

func TestCodexAdaptivePolicyRecordsOnlyRecognizedPressure(t *testing.T) {
	cache := &codexAdaptivePressureCacheStub{}
	svc := &OpenAIGatewayService{concurrencyService: NewConcurrencyService(cache)}
	account := &Account{ID: 11, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 20}
	ctx := codexAdaptivePolicyContext()
	svc.ObserveCodexAdaptiveSuccess(ctx, account, "gpt-5.6-sol")
	require.Zero(t, cache.successes, "the no-pressure success path must not write Redis")

	ordinary := &UpstreamFailoverError{StatusCode: http.StatusTooManyRequests, ResponseBody: []byte(`{"error":{"code":"rate_limit_exceeded"}}`)}
	require.False(t, svc.ApplyCodexAdaptiveFailoverPolicy(ctx, account, "gpt-5.6-sol", ordinary))
	require.Zero(t, cache.failures)

	overload := codexAdaptiveCapacityShedError()
	require.True(t, svc.ApplyCodexAdaptiveFailoverPolicy(ctx, account, "gpt-5.6-sol", overload))
	require.True(t, overload.RetryableOnSameAccount)
	require.Equal(t, 1, overload.SameAccountRetryMax)
	require.Equal(t, 1, cache.failures)

	ordinaryServerError := &UpstreamFailoverError{StatusCode: http.StatusServiceUnavailable, Reason: GatewayFailureReason("upstream_server_error")}
	require.False(t, svc.ApplyCodexAdaptiveFailoverPolicy(ctx, account, "gpt-5.6-sol", ordinaryServerError))
	require.Equal(t, 1, cache.failures)

	compactOverload := codexAdaptiveCapacityShedError()
	require.True(t, svc.ApplyCodexAdaptiveFailoverPolicy(codexAdaptivePolicyContext(), account, "gpt-5.6-sol", compactOverload))
	require.Equal(t, 2, cache.failures)

	thirdParty := &Account{ID: 12, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://relay.example/v1"}}
	require.False(t, svc.ApplyCodexAdaptiveFailoverPolicy(ctx, thirdParty, "gpt-5.6-sol", codexAdaptiveCapacityShedError()))
	require.Equal(t, 2, cache.failures, "custom relay API keys must not change adaptive pressure")

	svc.ObserveCodexAdaptiveSuccess(ctx, account, "gpt-5.6-sol")
	require.Equal(t, 1, cache.successes)
}

func TestCodexAdaptiveWebSocketCapacityFailureRecordsPressure(t *testing.T) {
	cache := &codexAdaptivePressureCacheStub{}
	svc := &OpenAIGatewayService{concurrencyService: NewConcurrencyService(cache)}
	account := &Account{ID: 12, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 20}
	ctx := codexAdaptivePolicyContext()
	payload := []byte(`{"type":"response.failed","response":{"error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}}`)

	require.True(t, svc.handleOpenAIWSFailureAccountSideEffects(ctx, account, "gpt-5.6-sol", nil, payload))
	require.Equal(t, 1, cache.failures)
	require.Equal(t, 0, cache.successes)
}

func TestCodexAdaptivePressurePrefetchBatchesByMappedModel(t *testing.T) {
	cache := &codexAdaptivePressureCacheStub{pressureByModel: map[string]map[int64]int{
		"gpt-5.6-sol":   {1: 2, 2: 3},
		"gpt-5.6-terra": {3: 4},
	}}
	svc := &OpenAIGatewayService{concurrencyService: NewConcurrencyService(cache)}
	ctx := codexAdaptivePolicyContext()
	accounts := []*Account{
		{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth},
		{ID: 3, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{
			"model_mapping": map[string]any{"gpt-5.6-sol": "gpt-5.6-terra"},
		}},
	}

	svc.prefetchCodexAdaptivePressures(ctx, accounts, "gpt-5.6-sol")
	require.Equal(t, 1, cache.batchCalls["gpt-5.6-sol"])
	require.Equal(t, 1, cache.batchCalls["gpt-5.6-terra"])
	require.Equal(t, 2, svc.codexAdaptivePressure(ctx, accounts[0], "gpt-5.6-sol"))
	require.Equal(t, 3, svc.codexAdaptivePressure(ctx, accounts[1], "gpt-5.6-sol"))
	require.Equal(t, 4, svc.codexAdaptivePressure(ctx, accounts[2], "gpt-5.6-terra"))
	require.Equal(t, 1, cache.batchCalls["gpt-5.6-sol"], "cached snapshot must avoid per-account reads")
}

func TestCodexAdaptivePressurePrefetchFailsOpenOncePerRequest(t *testing.T) {
	cache := &codexAdaptivePressureCacheStub{batchErr: errors.New("redis unavailable")}
	svc := &OpenAIGatewayService{concurrencyService: NewConcurrencyService(cache)}
	ctx := codexAdaptivePolicyContext()
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	svc.prefetchCodexAdaptivePressures(ctx, []*Account{account}, "gpt-5.6-sol")
	require.Zero(t, svc.codexAdaptivePressure(ctx, account, "gpt-5.6-sol"))
	require.Equal(t, 1, cache.batchCalls["gpt-5.6-sol"], "a failed batch must not fan out into per-account retries")
}

func TestCodexAdaptivePressureSnapshotRefreshesBetweenWebSocketTurns(t *testing.T) {
	cache := &codexAdaptivePressureCacheStub{pressureByModel: map[string]map[int64]int{
		"gpt-5.6-sol": {1: 2},
	}}
	svc := &OpenAIGatewayService{concurrencyService: NewConcurrencyService(cache)}
	ctx := codexAdaptivePolicyContext()
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	require.Equal(t, 2, svc.codexAdaptivePressure(ctx, account, "gpt-5.6-sol"))
	delete(cache.pressureByModel["gpt-5.6-sol"], account.ID)
	require.Equal(t, 2, svc.codexAdaptivePressure(ctx, account, "gpt-5.6-sol"), "one turn keeps its frozen pressure snapshot")

	svc.RefreshCodexAdaptivePressureSnapshot(ctx)
	require.Zero(t, svc.codexAdaptivePressure(ctx, account, "gpt-5.6-sol"))
	require.Equal(t, 2, cache.batchCalls["gpt-5.6-sol"])
}

func TestCodexAdaptiveSettingsPublishRequestScopedSnapshot(t *testing.T) {
	repo := &codexAdaptiveSettingRepoStub{value: `{"enabled":true}`}
	settingsService := NewSettingService(repo, &config.Config{})
	warmed := settingsService.WarmCodexAdaptiveSchedulingSettings(context.Background())
	require.True(t, warmed.Enabled)

	gateway := &OpenAIGatewayService{settingService: settingsService}
	ctx := gateway.PrepareCodexAdaptiveSchedulingRequest(context.Background(), 7, "session", "gpt-5.6-sol")
	state := codexAdaptiveRequestFromContext(ctx)
	require.NotNil(t, state)

	require.NoError(t, settingsService.SetCodexAdaptiveSchedulingSettings(context.Background(), DefaultCodexAdaptiveSchedulingSettings()))
	require.Nil(t, codexAdaptiveRequestFromContext(gateway.PrepareCodexAdaptiveSchedulingRequest(
		context.Background(), 7, "session", "gpt-5.6-sol",
	)))
	require.Same(t, state, codexAdaptiveRequestFromContext(ctx), "an in-flight request keeps its frozen policy state")
}

func TestCodexAdaptiveHotPathServesStaleSnapshotWithoutBlocking(t *testing.T) {
	block := make(chan struct{})
	repo := &codexAdaptiveSettingRepoStub{
		value: `{"enabled":false}`,
		block: block,
	}
	settingsService := NewSettingService(repo, &config.Config{})
	settingsService.codexAdaptiveSchedulingCache.Store(&cachedCodexAdaptiveSchedulingSettings{
		settings:  CodexAdaptiveSchedulingSettings{Enabled: true},
		expiresAt: time.Now().Add(-time.Second).UnixNano(),
	})

	started := time.Now()
	snapshot := settingsService.codexAdaptiveSchedulingSnapshot()
	require.Less(t, time.Since(started), 100*time.Millisecond)
	require.True(t, snapshot.Enabled)
	close(block)
}

func TestCodexAdaptiveStickyMigrationCommitsOnlyAfterSuccessfulFailover(t *testing.T) {
	const sessionHash = "adaptive-migration"
	cache := newCodexMigrationTestCache(sessionHash, 1)
	svc := &OpenAIGatewayService{cache: cache}
	ctx := codexAdaptivePolicyContext()
	state := codexAdaptiveRequestFromContext(ctx)
	state.sessionHash = sessionHash
	accountA := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	accountB := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	require.True(t, svc.ApplyCodexAdaptiveFailoverPolicy(ctx, accountA, "gpt-5.6-sol", codexAdaptiveCapacityShedError()))
	require.True(t, codexAdaptiveStickyMigrationPending(ctx))
	require.NoError(t, svc.BindStickySessionAfterProfitAdmission(ctx, nil, sessionHash, accountB.ID))
	require.Equal(t, accountA.ID, cache.sessionBindings["openai:"+sessionHash], "candidate selection must not migrate before success")
	selection, err := svc.coordinateCodexStickySelection(ctx, OpenAIAccountScheduleRequest{SessionHash: sessionHash}, &AccountSelectionResult{Account: accountB})
	require.NoError(t, err)
	require.Equal(t, accountB.ID, selection.Account.ID)
	t.Cleanup(func() { FinishCodexAdaptiveSchedulingRequest(ctx) })

	require.NoError(t, svc.CommitCodexAdaptiveStickyOnSuccess(ctx, nil, accountB, false))
	require.Equal(t, accountB.ID, cache.sessionBindings["openai:"+sessionHash])
	require.False(t, codexAdaptiveStickyMigrationPending(ctx))
}

func TestCodexAdaptiveStickyMigrationFailureKeepsOriginalBinding(t *testing.T) {
	const sessionHash = "adaptive-failed-candidate"
	cache := &stubGatewayCache{sessionBindings: map[string]int64{"openai:" + sessionHash: 1}}
	svc := &OpenAIGatewayService{cache: cache}
	ctx := codexAdaptivePolicyContext()
	state := codexAdaptiveRequestFromContext(ctx)
	state.sessionHash = sessionHash

	require.True(t, svc.ApplyCodexAdaptiveFailoverPolicy(ctx, openAIFirstOutputTestAccount(1), "gpt-5.6-sol", codexAdaptiveCapacityShedError()))
	require.NoError(t, svc.BindStickySessionAfterProfitAdmission(ctx, nil, sessionHash, 2))
	require.Equal(t, int64(1), cache.sessionBindings["openai:"+sessionHash])
	require.True(t, codexAdaptiveStickyMigrationPending(ctx), "a failed candidate must not consume the pending migration")
}

func TestCodexAdaptiveStickyMigrationContinuationNeverRebinds(t *testing.T) {
	const sessionHash = "adaptive-continuation"
	cache := &stubGatewayCache{sessionBindings: map[string]int64{"openai:" + sessionHash: 1}}
	svc := &OpenAIGatewayService{cache: cache}
	ctx := codexAdaptivePolicyContext()
	state := codexAdaptiveRequestFromContext(ctx)
	state.sessionHash = sessionHash

	require.True(t, svc.ApplyCodexAdaptiveFailoverPolicy(ctx, openAIFirstOutputTestAccount(1), "gpt-5.6-sol", codexAdaptiveCapacityShedError()))
	require.NoError(t, svc.CommitCodexAdaptiveStickyOnSuccess(ctx, nil, openAIFirstOutputTestAccount(2), true))
	require.Equal(t, int64(1), cache.sessionBindings["openai:"+sessionHash])
	require.False(t, codexAdaptiveStickyMigrationPending(ctx), "a successful continuation must consume request-local migration state")
}

func TestCodexAdaptiveStickyMigrationIsNotArmedByOrdinaryFailures(t *testing.T) {
	ctx := codexAdaptivePolicyContext()
	svc := &OpenAIGatewayService{}
	account := openAIFirstOutputTestAccount(1)

	require.False(t, svc.ApplyCodexAdaptiveFailoverPolicy(ctx, account, "gpt-5.6-sol", &UpstreamFailoverError{
		StatusCode: http.StatusTooManyRequests,
		Reason:     GatewayFailureReason("rate_limit_exceeded"),
	}))
	require.False(t, codexAdaptiveStickyMigrationPending(ctx), "ordinary 429 and slow output must not migrate sticky routing")
}
