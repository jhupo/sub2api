//go:build unit

package handler

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type grokVoicePreauthorizationCacheStub struct {
	service.BillingCache
	topUpCalls int
}

func (s *grokVoicePreauthorizationCacheStub) GetUserBalance(context.Context, int64) (float64, error) {
	return 10, nil
}

func (s *grokVoicePreauthorizationCacheStub) GetLiveBalance(context.Context, int64) (float64, bool, error) {
	return 10, true, nil
}

func (s *grokVoicePreauthorizationCacheStub) AuthorizeExistingLiveBalance(
	_ context.Context,
	_ int64,
	_ string,
	holdAmount float64,
) (service.LiveBalanceResult, error) {
	return service.LiveBalanceResult{
		Outcome:        service.LiveBalanceOutcomeApplied,
		State:          service.LiveBalanceAttemptAuthorized,
		ReservedAmount: holdAmount,
	}, nil
}

func (s *grokVoicePreauthorizationCacheStub) AuthorizeLiveBalanceAtWatermark(
	_ context.Context,
	_ int64,
	_ string,
	_ float64,
	_ int64,
	holdAmount float64,
) (service.LiveBalanceResult, error) {
	return service.LiveBalanceResult{
		Outcome:        service.LiveBalanceOutcomeApplied,
		State:          service.LiveBalanceAttemptAuthorized,
		ReservedAmount: holdAmount,
	}, nil
}

func (s *grokVoicePreauthorizationCacheStub) AuthorizeLiveBalanceAtWatermarkIfSafe(
	_ context.Context,
	_ int64,
	_ string,
	_ float64,
	_ int64,
	holdAmount float64,
	_ bool,
) (service.LiveBalanceResult, error) {
	return service.LiveBalanceResult{
		Outcome:        service.LiveBalanceOutcomeApplied,
		State:          service.LiveBalanceAttemptAuthorized,
		ReservedAmount: holdAmount,
	}, nil
}

func (s *grokVoicePreauthorizationCacheStub) AuthorizeLiveBalanceAtSnapshotWatermarkIfSafe(
	_ context.Context,
	_ int64,
	_ string,
	_ float64,
	_ int64,
	holdAmount float64,
	_ bool,
) (service.LiveBalanceResult, error) {
	return service.LiveBalanceResult{
		Outcome:        service.LiveBalanceOutcomeApplied,
		State:          service.LiveBalanceAttemptAuthorized,
		ReservedAmount: holdAmount,
	}, nil
}

func (s *grokVoicePreauthorizationCacheStub) TopUpLiveBalanceAtSnapshotWatermark(
	_ context.Context,
	_ int64,
	_ string,
	_ int64,
	_ float64,
) (service.LiveBalanceResult, error) {
	s.topUpCalls++
	return service.LiveBalanceResult{
		Outcome: service.LiveBalanceOutcomeInsufficient,
		State:   service.LiveBalanceAttemptAuthorized,
	}, nil
}

func (s *grokVoicePreauthorizationCacheStub) RefundLiveBalance(
	_ context.Context,
	_ int64,
	_ string,
) (service.LiveBalanceResult, error) {
	return service.LiveBalanceResult{
		Outcome: service.LiveBalanceOutcomeApplied,
		State:   service.LiveBalanceAttemptRefunded,
	}, nil
}

type grokVoicePreauthorizationRepoStub struct {
	service.UsageBillingRepository
	applyCalls int
}

func (s *grokVoicePreauthorizationRepoStub) Apply(context.Context, *service.UsageBillingCommand) (*service.UsageBillingApplyResult, error) {
	s.applyCalls++
	return &service.UsageBillingApplyResult{}, nil
}

func (*grokVoicePreauthorizationRepoStub) LoadLiveBalanceInitializationSnapshot(
	context.Context,
	int64,
	string,
	int64,
) (service.LiveBalanceInitializationSnapshot, error) {
	return service.LiveBalanceInitializationSnapshot{Balance: 10}, nil
}

func (s *grokVoicePreauthorizationRepoStub) PrepareBalancePreauthorization(
	_ context.Context,
	cmd *service.BalancePreauthorizationCommand,
) (*service.BalancePreauthorizationRecord, error) {
	return &service.BalancePreauthorizationRecord{
		RequestID:  cmd.RequestID,
		APIKeyID:   cmd.APIKeyID,
		UserID:     cmd.UserID,
		HoldAmount: cmd.HoldAmount,
		Status:     service.BalanceSettlementPrepared,
	}, nil
}

func (*grokVoicePreauthorizationRepoStub) MarkBalancePreauthorizationAuthorized(context.Context, string, int64) error {
	return nil
}

type grokVoiceSuccessUpstreamStub struct {
	service.HTTPUpstream
	response *http.Response
}

func (s *grokVoiceSuccessUpstreamStub) Do(*http.Request, string, int64, int) (*http.Response, error) {
	return s.response, nil
}

func TestIsExpectedGrokRealtimeClose(t *testing.T) {
	for _, status := range []coderws.StatusCode{
		coderws.StatusNormalClosure,
		coderws.StatusGoingAway,
		coderws.StatusNoStatusRcvd,
		coderws.StatusAbnormalClosure,
	} {
		if !isExpectedGrokRealtimeClose(coderws.CloseError{Code: status}) {
			t.Fatalf("status %v should be treated as an expected session close", status)
		}
	}
	if isExpectedGrokRealtimeClose(coderws.CloseError{Code: coderws.StatusPolicyViolation}) {
		t.Fatal("policy violations must not be treated as billable normal closes")
	}
}

func TestGrokRealtimeBillingResultRequiresObservedAudio(t *testing.T) {
	if grokRealtimeBillingResult("grok-voice-latest", time.Second, false) != nil {
		t.Fatal("a session without observed audio must not be billed")
	}
	if grokRealtimeBillingResult("grok-voice-latest", 0, true) != nil {
		t.Fatal("zero-duration sessions must not be billed")
	}
}

func TestGrokRealtimeBillingResultUsesForcedUniqueID(t *testing.T) {
	first := grokRealtimeBillingResult("grok-voice-latest", 90*time.Second, true)
	second := grokRealtimeBillingResult("grok-voice-latest", 90*time.Second, true)
	if first == nil || second == nil {
		t.Fatal("observed audio sessions should be billable")
	}
	if first.RequestID == "" {
		t.Fatalf("unexpected billing request ID %q", first.RequestID)
	}
	if first.RequestID == second.RequestID {
		t.Fatal("independent realtime connections must not share a billing request ID")
	}
	if first.AudioUsage == nil || first.AudioUsage.Mode != "realtime" || first.AudioUsage.DurationOrUnits != 1.5 {
		t.Fatalf("unexpected audio usage: %#v", first.AudioUsage)
	}
}

func TestGrokSTTTopUpFailureDoesNotCommitBufferedSuccess(t *testing.T) {
	t.Setenv(xai.EnvAllowUnsafeURLOverrides, "true")
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{
		RunMode: config.RunModeStandard,
		Billing: config.BillingConfig{BalancePreauthorizationEnabled: true},
	}
	cache := &grokVoicePreauthorizationCacheStub{}
	billingCache := service.NewBillingCacheService(cache, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCache.Stop)
	repo := &grokVoicePreauthorizationRepoStub{}
	billing := service.NewBillingService(cfg, nil)
	preauthorizer := service.NewBalancePreauthorizationService(cfg, nil, billing, billingCache, repo)

	groupID := int64(71)
	sttPrice := 1.0
	apiKey := &service.APIKey{
		ID:      72,
		UserID:  73,
		GroupID: &groupID,
		User:    &service.User{ID: 73},
		Group: &service.Group{
			ID:                   groupID,
			Platform:             service.PlatformGrok,
			RateMultiplier:       1,
			AudioSTTPricePerHour: &sttPrice,
		},
	}
	requestBody := []byte(`{"audio":"small"}`)
	pricingAt := time.Unix(1_000, 0)
	guard, err := preauthorizeGrokAudioGatewayRequest(
		context.Background(), preauthorizer, &pricingProviderStub{audioCost: 0.01},
		apiKey, nil, requestBody, "stt", pricingAt,
	)
	require.NoError(t, err)
	require.NotNil(t, guard)
	require.Less(t, guard.HoldAmount(), sttPrice)

	upstream := &grokVoiceSuccessUpstreamStub{response: &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"text":"upstream transcription","duration_seconds":3600}`,
		)),
	}}
	gateway := service.NewOpenAIGatewayService(
		nil, nil, repo, nil, nil, nil, nil, cfg, nil, nil,
		billing, nil, billingCache, upstream, &service.DeferredService{}, nil, nil,
		nil, nil, nil, nil, nil,
	)
	account := &service.Account{
		ID:          74,
		Platform:    service.PlatformGrok,
		Type:        service.AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":  "test-key",
			"base_url": "https://xai.test/v1",
		},
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/stt", bytes.NewReader(requestBody))
	c.Request = c.Request.WithContext(service.ContextWithBalancePreauthorizationGuard(c.Request.Context(), guard))

	result, err := gateway.ForwardGrokVoice(c.Request.Context(), c, account, "stt", requestBody, "application/json")
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.AudioUsage)
	require.InDelta(t, 1, result.AudioUsage.DurationOrUnits, 1e-12)
	require.False(t, c.Writer.Written())

	h := &OpenAIGatewayHandler{gatewayService: gateway, apiKeyService: &service.APIKeyService{}}
	h.finalizeGrokVoiceForwardSuccess(c, zap.NewNop(), apiKey, account, nil, "stt", requestBody, result, pricingAt)

	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.Contains(t, recorder.Body.String(), "Insufficient balance")
	require.NotContains(t, recorder.Body.String(), "upstream transcription")
	require.True(t, guard.IsCurrentOwner(), "top-up failure must leave deferred refund ownership with the handler")
	require.Equal(t, 1, cache.topUpCalls)
	require.Zero(t, repo.applyCalls, "usage recording must not run after a failed top-up")
	require.Error(t, gateway.CommitGrokVoiceResponse(c, result), "the billing error must prevent the buffered success from committing")
}
