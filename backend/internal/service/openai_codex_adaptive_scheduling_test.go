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

func codexAdaptivePolicyContext(compact bool) context.Context {
	return context.WithValue(context.Background(), codexAdaptiveRequestContextKey{}, &codexAdaptiveRequestState{
		model:         "gpt-5.6-sol",
		compact:       compact,
		sessionMember: "session-member",
		settings: CodexAdaptiveSchedulingSettings{
			Enabled:                             true,
			NormalFirstOutputTimeoutSeconds:     90,
			HighEffortFirstOutputTimeoutSeconds: 240,
		},
		pressures: make(map[codexAdaptivePressureScope]int),
	})
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

func TestCodexAdaptiveFirstOutputTimeoutSkipsCompressionAndThirdPartyKeys(t *testing.T) {
	ctx := codexAdaptivePolicyContext(false)
	oauth := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	officialAPIKey := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	thirdParty := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{"base_url": "https://relay.example/v1"}}

	require.Equal(t, 90*time.Second, codexAdaptiveFirstOutputTimeout(ctx, oauth, "gpt-5.6-sol", "medium"))
	require.Equal(t, 240*time.Second, codexAdaptiveFirstOutputTimeout(ctx, oauth, "gpt-5.6-sol", "high"))
	require.Equal(t, 90*time.Second, codexAdaptiveFirstOutputTimeout(ctx, officialAPIKey, "gpt-5.6-sol", "medium"))
	require.Zero(t, codexAdaptiveFirstOutputTimeout(codexAdaptivePolicyContext(true), oauth, "gpt-5.6-sol", "high"))
	require.Zero(t, codexAdaptiveFirstOutputTimeout(ctx, thirdParty, "gpt-5.6-sol", "high"))
	compactFrame := []byte(`{"type":"response.create","model":"gpt-5.6-sol","input":[{"type":"compaction_trigger"}]}`)
	require.Zero(t, codexAdaptiveWSFirstOutputTimeout(ctx, oauth, compactFrame, "gpt-5.6-sol", "high"))
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

	ctx := codexAdaptivePolicyContext(false)
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
	ctx := codexAdaptivePolicyContext(false)
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

func TestCodexAdaptiveSessionPressureIdentityIsolatedByAPIKey(t *testing.T) {
	repo := &codexAdaptiveSettingRepoStub{value: `{"enabled":true,"normal_first_output_timeout_seconds":90,"high_effort_first_output_timeout_seconds":240}`}
	settingsService := NewSettingService(repo, &config.Config{})
	settingsService.WarmCodexAdaptiveSchedulingSettings(context.Background())
	gateway := &OpenAIGatewayService{settingService: settingsService}

	first := codexAdaptiveRequestFromContext(gateway.PrepareCodexAdaptiveSchedulingRequest(
		context.Background(), 101, "shared-session", "gpt-5.6-sol", false,
	))
	sameTenant := codexAdaptiveRequestFromContext(gateway.PrepareCodexAdaptiveSchedulingRequest(
		context.Background(), 101, "shared-session", "gpt-5.6-sol", false,
	))
	otherTenant := codexAdaptiveRequestFromContext(gateway.PrepareCodexAdaptiveSchedulingRequest(
		context.Background(), 202, "shared-session", "gpt-5.6-sol", false,
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
	ctx := codexAdaptivePolicyContext(false)
	svc.ObserveCodexAdaptiveSuccess(ctx, account, "gpt-5.6-sol")
	require.Zero(t, cache.successes, "the no-pressure success path must not write Redis")

	ordinary := &UpstreamFailoverError{StatusCode: http.StatusTooManyRequests, ResponseBody: []byte(`{"error":{"code":"rate_limit_exceeded"}}`)}
	require.False(t, svc.ApplyCodexAdaptiveFailoverPolicy(ctx, account, "gpt-5.6-sol", ordinary))
	require.Zero(t, cache.failures)

	overload := &UpstreamFailoverError{
		StatusCode:             http.StatusServiceUnavailable,
		ResponseBody:           []byte(`{"error":{"code":"server_is_overloaded"}}`),
		RequestScopedTransient: true,
		Reason:                 openAIUpstreamCapacityShedReason,
	}
	require.True(t, svc.ApplyCodexAdaptiveFailoverPolicy(ctx, account, "gpt-5.6-sol", overload))
	require.True(t, overload.RetryableOnSameAccount)
	require.Equal(t, 1, overload.SameAccountRetryMax)
	require.Equal(t, 1, cache.failures)

	timeout := &UpstreamFailoverError{Reason: CodexFirstOutputTimeoutReason}
	require.True(t, svc.ApplyCodexAdaptiveFailoverPolicy(ctx, account, "gpt-5.6-sol", timeout))
	require.False(t, timeout.RetryableOnSameAccount)
	require.Zero(t, timeout.SameAccountRetryMax)
	require.Equal(t, 2, cache.failures)

	compactTimeout := &UpstreamFailoverError{Reason: CodexFirstOutputTimeoutReason}
	require.False(t, svc.ApplyCodexAdaptiveFailoverPolicy(codexAdaptivePolicyContext(true), account, "gpt-5.6-sol", compactTimeout))
	require.Equal(t, 2, cache.failures)
	compactOverload := &UpstreamFailoverError{
		StatusCode:             http.StatusServiceUnavailable,
		ResponseBody:           []byte(`{"error":{"code":"server_is_overloaded"}}`),
		RequestScopedTransient: true,
		Reason:                 openAIUpstreamCapacityShedReason,
	}
	require.True(t, svc.ApplyCodexAdaptiveFailoverPolicy(codexAdaptivePolicyContext(true), account, "gpt-5.6-sol", compactOverload))
	require.Equal(t, 3, cache.failures, "compact requests only contribute explicit upstream capacity signals")

	svc.ObserveCodexAdaptiveSuccess(ctx, account, "gpt-5.6-sol")
	require.Equal(t, 1, cache.successes)
}

func TestCodexAdaptiveWebSocketCapacityFailureRecordsPressure(t *testing.T) {
	cache := &codexAdaptivePressureCacheStub{}
	svc := &OpenAIGatewayService{concurrencyService: NewConcurrencyService(cache)}
	account := &Account{ID: 12, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 20}
	ctx := codexAdaptivePolicyContext(false)
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
	ctx := codexAdaptivePolicyContext(false)
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
	ctx := codexAdaptivePolicyContext(false)
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
	ctx := codexAdaptivePolicyContext(false)
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}

	require.Equal(t, 2, svc.codexAdaptivePressure(ctx, account, "gpt-5.6-sol"))
	delete(cache.pressureByModel["gpt-5.6-sol"], account.ID)
	require.Equal(t, 2, svc.codexAdaptivePressure(ctx, account, "gpt-5.6-sol"), "one turn keeps its frozen pressure snapshot")

	svc.RefreshCodexAdaptivePressureSnapshot(ctx)
	require.Zero(t, svc.codexAdaptivePressure(ctx, account, "gpt-5.6-sol"))
	require.Equal(t, 2, cache.batchCalls["gpt-5.6-sol"])
}

func TestCodexAdaptiveSettingsPublishRequestScopedSnapshot(t *testing.T) {
	repo := &codexAdaptiveSettingRepoStub{value: `{"enabled":true,"normal_first_output_timeout_seconds":90,"high_effort_first_output_timeout_seconds":240}`}
	settingsService := NewSettingService(repo, &config.Config{})
	warmed := settingsService.WarmCodexAdaptiveSchedulingSettings(context.Background())
	require.True(t, warmed.Enabled)

	gateway := &OpenAIGatewayService{settingService: settingsService}
	ctx := gateway.PrepareCodexAdaptiveSchedulingRequest(context.Background(), 7, "session", "gpt-5.6-sol", false)
	state := codexAdaptiveRequestFromContext(ctx)
	require.NotNil(t, state)
	require.Equal(t, 90, state.settings.NormalFirstOutputTimeoutSeconds)

	require.NoError(t, settingsService.SetCodexAdaptiveSchedulingSettings(context.Background(), DefaultCodexAdaptiveSchedulingSettings()))
	require.Nil(t, codexAdaptiveRequestFromContext(gateway.PrepareCodexAdaptiveSchedulingRequest(
		context.Background(), 7, "session", "gpt-5.6-sol", false,
	)))
	require.True(t, state.settings.Enabled, "an in-flight request keeps its frozen policy")
}

func TestCodexAdaptiveHotPathServesStaleSnapshotWithoutBlocking(t *testing.T) {
	block := make(chan struct{})
	repo := &codexAdaptiveSettingRepoStub{
		value: `{"enabled":false,"normal_first_output_timeout_seconds":90,"high_effort_first_output_timeout_seconds":240}`,
		block: block,
	}
	settingsService := NewSettingService(repo, &config.Config{})
	settingsService.codexAdaptiveSchedulingCache.Store(&cachedCodexAdaptiveSchedulingSettings{
		settings: CodexAdaptiveSchedulingSettings{
			Enabled:                             true,
			NormalFirstOutputTimeoutSeconds:     90,
			HighEffortFirstOutputTimeoutSeconds: 240,
		},
		expiresAt: time.Now().Add(-time.Second).UnixNano(),
	})

	started := time.Now()
	snapshot := settingsService.codexAdaptiveSchedulingSnapshot()
	require.Less(t, time.Since(started), 100*time.Millisecond)
	require.True(t, snapshot.Enabled)
	close(block)
}
