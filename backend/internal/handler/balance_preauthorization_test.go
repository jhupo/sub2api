package handler

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// preauthorizerStub captures the request the handler builds and returns a
// configurable outcome. It optionally reports RequiresPreauthorization=false to
// exercise the subscription/simple-mode skip.
type preauthorizerStub struct {
	captured       *service.BalancePreauthorizationRequest
	capturedAll    []service.BalancePreauthorizationRequest
	guard          *service.BalancePreauthorizationGuard
	err            error
	requires       bool
	requiresCalled bool
}

func (s *preauthorizerStub) Preauthorize(_ context.Context, req service.BalancePreauthorizationRequest) (*service.BalancePreauthorizationGuard, error) {
	captured := req
	s.captured = &captured
	s.capturedAll = append(s.capturedAll, captured)
	return s.guard, s.err
}

func (s *preauthorizerStub) RequiresPreauthorization(context.Context, int8) bool {
	s.requiresCalled = true
	return s.requires
}

// pricingProviderStub returns a deterministic CostInput without touching real
// pricing services.
type pricingProviderStub struct {
	lastModel     string
	lastPricingAt time.Time
	lastTier      string
	webSearchCost float64
	webSearchErr  error
	lastRateKind  service.BalancePreauthorizationRateKind
	audioCost     float64
	audioErr      error
	lastAudioMode string
	lastAudioUnit float64
}

func (s *pricingProviderStub) BalancePreauthorizationAudioCost(_ context.Context, _ *service.APIKey, _ string, mode string, units float64, _ time.Time) (float64, error) {
	s.lastAudioMode = mode
	s.lastAudioUnit = units
	return s.audioCost, s.audioErr
}

func (s *pricingProviderStub) BalancePreauthorizationSearchCost(_ context.Context, _ *service.APIKey, pricingAt time.Time) (float64, error) {
	s.lastPricingAt = pricingAt
	return s.webSearchCost, s.webSearchErr
}

func (s *pricingProviderStub) BalancePreauthorizationWebSearchCost(_ context.Context, _ *service.APIKey, pricingAt time.Time) (float64, error) {
	s.lastPricingAt = pricingAt
	return s.webSearchCost, s.webSearchErr
}

func (s *pricingProviderStub) BalancePreauthorizationCostInput(_ context.Context, _ *service.APIKey, model string, pricingAt time.Time, tier string, rateKind service.BalancePreauthorizationRateKind) service.CostInput {
	s.lastModel = model
	s.lastPricingAt = pricingAt
	s.lastTier = tier
	s.lastRateKind = rateKind
	return service.CostInput{Model: model, RateMultiplier: 1, PricingAt: pricingAt, ServiceTier: tier}
}

func TestPreauthorizeGrokVideoUsesUniqueServerHoldIDs(t *testing.T) {
	preauthorizer := &preauthorizerStub{requires: true}
	pricing := &pricingProviderStub{}
	body := []byte(`{"model":"grok-imagine-video","duration":10,"resolution":"720p"}`)
	before := time.Now()
	for range 2 {
		_, err := preauthorizeGrokVideoGatewayRequest(context.Background(), preauthorizer, pricing, perRequestTestAPIKey(), nil, body, "grok-imagine-video", time.Unix(1000, 0), 10, "720p")
		require.NoError(t, err)
	}
	require.Len(t, preauthorizer.capturedAll, 2)
	first, second := preauthorizer.capturedAll[0], preauthorizer.capturedAll[1]
	require.True(t, service.IsGrokVideoHoldRequestID(first.RequestID))
	require.NotEqual(t, first.RequestID, second.RequestID)
	require.Equal(t, service.BillingTypeBalance, first.BillingType)
	require.Equal(t, service.HashUsageRequestPayload(body), first.AuthorizationFingerprint)
	require.Equal(t, service.PreauthorizationEstimatePerRequest, first.EstimateKind)
	require.Equal(t, 10.0, first.PerRequestEstimate.UsageUnits)
	require.Equal(t, service.VideoBillingResolution720P, first.PerRequestEstimate.SizeTier)
	require.True(t, first.ExpiresAt.After(before.Add(23*time.Hour)))
	require.True(t, first.ExpiresAt.Before(before.Add(25*time.Hour)))
	require.Equal(t, service.BalancePreauthorizationRateVideo, pricing.lastRateKind)
}

func perRequestTestAPIKey() *service.APIKey {
	groupID := int64(9)
	return &service.APIKey{ID: 7, UserID: 42, GroupID: &groupID}
}

func TestPreauthorizeTextPassesRequestLocalTokenEstimate(t *testing.T) {
	preauthorizer := &preauthorizerStub{requires: true}
	pricing := &pricingProviderStub{}
	body := []byte(`{"model":"gpt-5","input":"hello","max_output_tokens":1536}`)

	_, err := preauthorizeTextGatewayRequest(
		context.Background(), preauthorizer, pricing,
		perRequestTestAPIKey(), nil, body, "gpt-5", time.Unix(1000, 0), "",
	)

	require.NoError(t, err)
	require.NotNil(t, preauthorizer.captured)
	require.Equal(t, len(body), preauthorizer.captured.BillableInputBytes)
	require.GreaterOrEqual(t, preauthorizer.captured.EstimatedInputTokens, service.DefaultBalancePreauthorizationInputTokens)
	require.Equal(t, 1536, preauthorizer.captured.InitialOutputWindowTokens)
}

func TestPreauthorizeTextUsesGroupFastCustomerTier(t *testing.T) {
	tests := []struct {
		name      string
		platform  string
		target    string
		forceFast bool
		freeFast  bool
		requested string
		want      string
	}{
		{name: "forced Fast reserves priority", platform: service.PlatformOpenAI, forceFast: true, want: service.OpenAIFastTierPriority},
		{name: "free forced Fast reserves Standard", platform: service.PlatformOpenAI, forceFast: true, freeFast: true, want: ""},
		{name: "free Composite OpenAI Fast reserves Standard", platform: service.PlatformComposite, target: service.PlatformOpenAI, freeFast: true, requested: "fast", want: ""},
		{name: "Composite non-OpenAI route ignores Fast policy", platform: service.PlatformComposite, target: service.PlatformAnthropic, forceFast: true, freeFast: true, requested: "fast", want: "fast"},
		{name: "unresolved Composite route reserves conservatively", platform: service.PlatformComposite, forceFast: true, freeFast: true, want: service.OpenAIFastTierPriority},
		{name: "paid client priority stays priority", platform: service.PlatformOpenAI, requested: "priority", want: "priority"},
		{name: "unsupported platform ignores policy", platform: service.PlatformAnthropic, forceFast: true, requested: "flex", want: "flex"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preauthorizer := &preauthorizerStub{requires: true}
			pricing := &pricingProviderStub{}
			apiKey := perRequestTestAPIKey()
			apiKey.Group = &service.Group{
				Platform: tt.platform, ForceOpenAIFast: tt.forceFast, FreeOpenAIFast: tt.freeFast,
			}

			ctx := context.Background()
			if tt.target != "" {
				ctx = service.WithResolvedTargetPlatform(ctx, tt.target)
			}
			_, err := preauthorizeTextGatewayRequest(
				ctx, preauthorizer, pricing, apiKey, nil,
				[]byte(`{"model":"gpt-5","input":"hello"}`), "gpt-5", time.Unix(1000, 0), tt.requested,
			)

			require.NoError(t, err)
			require.Equal(t, tt.want, pricing.lastTier)
			require.Equal(t, tt.want, preauthorizer.captured.CostInput.ServiceTier)
		})
	}
}

func TestPreauthorizeInputOnlyDisablesOutputReservation(t *testing.T) {
	preauthorizer := &preauthorizerStub{requires: true}
	pricing := &pricingProviderStub{}

	_, err := preauthorizeInputOnlyGatewayRequest(
		context.Background(), preauthorizer, pricing,
		perRequestTestAPIKey(), nil, []byte(`{"model":"text-embedding-3-small","input":"hello"}`),
		"text-embedding-3-small", time.Unix(1000, 0), "",
	)

	require.NoError(t, err)
	require.NotNil(t, preauthorizer.captured)
	require.True(t, preauthorizer.captured.DisableOutputReservation)
	require.Zero(t, preauthorizer.captured.InitialOutputWindowTokens)
}

func TestPreauthorizeWebSearchUsesTrustedFixedCost(t *testing.T) {
	preauthorizer := &preauthorizerStub{requires: true}
	pricing := &pricingProviderStub{webSearchCost: 0.025}
	pricingAt := time.Unix(1000, 0)

	_, err := preauthorizeWebSearchGatewayRequest(
		context.Background(), preauthorizer, pricing,
		perRequestTestAPIKey(), nil, []byte(`{"model":"gpt-5"}`), pricingAt, "",
	)

	require.NoError(t, err)
	require.NotNil(t, preauthorizer.captured)
	require.Equal(t, service.PreauthorizationEstimateFixed, preauthorizer.captured.EstimateKind)
	require.Equal(t, 0.025, preauthorizer.captured.FixedAmount)
	require.Equal(t, pricingAt, pricing.lastPricingAt)
}

func TestPreauthorizeSearchUsesExplicitSettlementRequestID(t *testing.T) {
	preauthorizer := &preauthorizerStub{requires: true}
	pricing := &pricingProviderStub{webSearchCost: 0.005}

	_, err := preauthorizeSearchGatewayRequest(
		context.Background(), preauthorizer, pricing,
		perRequestTestAPIKey(), nil, []byte("query"), time.Unix(1000, 0), "x_search:request-1",
	)

	require.NoError(t, err)
	require.NotNil(t, preauthorizer.captured)
	require.Equal(t, "x_search:request-1", preauthorizer.captured.RequestID)
	require.Equal(t, service.PreauthorizationEstimateFixed, preauthorizer.captured.EstimateKind)
	require.Equal(t, 0.005, preauthorizer.captured.FixedAmount)
}

// TestPreauthorizePerRequestPassesBillingUnits proves the per-request helper
// forwards the parsed count/size tier and per-request estimate kind so image
// endpoints reserve the exact request price rather than a token upper bound.
func TestPreauthorizePerRequestPassesBillingUnits(t *testing.T) {
	preauthorizer := &preauthorizerStub{requires: true}
	pricing := &pricingProviderStub{}
	pricingAt := time.Unix(1000, 0)

	_, err := preauthorizePerRequestGatewayRequest(
		context.Background(), preauthorizer, pricing,
		perRequestTestAPIKey(), nil, []byte(`{"model":"gpt-image-1","n":3}`),
		"gpt-image-1", pricingAt,
		service.PerRequestPreauthorizationEstimate{RequestCount: 3, SizeTier: "2K"},
		service.BalancePreauthorizationRateImage,
	)
	require.NoError(t, err)
	require.NotNil(t, preauthorizer.captured)
	require.Equal(t, service.PreauthorizationEstimatePerRequest, preauthorizer.captured.EstimateKind)
	require.Equal(t, 3, preauthorizer.captured.PerRequestEstimate.RequestCount)
	require.Equal(t, "2K", preauthorizer.captured.PerRequestEstimate.SizeTier)
	require.Equal(t, int64(7), preauthorizer.captured.APIKeyID)
	require.Equal(t, int64(42), preauthorizer.captured.UserID)
	require.Equal(t, service.BillingTypeBalance, preauthorizer.captured.BillingType)
	require.Equal(t, "gpt-image-1", pricing.lastModel)
	require.Equal(t, pricingAt, pricing.lastPricingAt)
}

// TestPreauthorizePerRequestPropagatesWithholdingFailure proves an insufficient
// balance surfaces as the 403 ErrBalanceWithholdingFailed, not a generic error.
func TestPreauthorizePerRequestPropagatesWithholdingFailure(t *testing.T) {
	preauthorizer := &preauthorizerStub{requires: true, err: service.ErrBalanceWithholdingFailed}
	pricing := &pricingProviderStub{}

	_, err := preauthorizePerRequestGatewayRequest(
		context.Background(), preauthorizer, pricing,
		perRequestTestAPIKey(), nil, []byte(`{"model":"gpt-image-1"}`),
		"gpt-image-1", time.Unix(1, 0),
		service.PerRequestPreauthorizationEstimate{RequestCount: 1, SizeTier: "1K"},
		service.BalancePreauthorizationRateImage,
	)
	require.ErrorIs(t, err, service.ErrBalanceWithholdingFailed)
	require.True(t, infraerrors.IsForbidden(err), "withholding failure must map to HTTP 403")
}

// TestPreauthorizePerRequestSkipsWhenNotRequired proves subscription/simple
// mode short-circuits before pricing so those requests are never charged a hold.
func TestPreauthorizePerRequestSkipsWhenNotRequired(t *testing.T) {
	preauthorizer := &preauthorizerStub{requires: false}
	pricing := &pricingProviderStub{}

	guard, err := preauthorizePerRequestGatewayRequest(
		context.Background(), preauthorizer, pricing,
		perRequestTestAPIKey(), nil, []byte(`{"model":"gpt-image-1"}`),
		"gpt-image-1", time.Unix(1, 0),
		service.PerRequestPreauthorizationEstimate{RequestCount: 1, SizeTier: "1K"},
		service.BalancePreauthorizationRateImage,
	)
	require.NoError(t, err)
	require.Nil(t, guard)
	require.True(t, preauthorizer.requiresCalled)
	require.Nil(t, preauthorizer.captured)
	require.Empty(t, pricing.lastModel)
}

func TestPreauthorizeGrokAudioUsesServerHoldAndEstimatedUnits(t *testing.T) {
	preauthorizer := &preauthorizerStub{requires: true}
	pricing := &pricingProviderStub{audioCost: 0.025}
	body := []byte(`{"input":"hello"}`)

	_, err := preauthorizeGrokAudioGatewayRequest(context.Background(), preauthorizer, pricing, perRequestTestAPIKey(), nil, body, "tts", time.Unix(1000, 0))
	require.NoError(t, err)
	require.NotNil(t, preauthorizer.captured)
	require.Equal(t, service.PreauthorizationEstimateFixed, preauthorizer.captured.EstimateKind)
	require.Equal(t, 0.025, preauthorizer.captured.FixedAmount)
	require.Contains(t, preauthorizer.captured.RequestID, "grok_audio:")
	require.Equal(t, "tts", pricing.lastAudioMode)
	require.InDelta(t, 5.0/1_000_000.0, pricing.lastAudioUnit, 1e-12)
}

func TestPreauthorizeGrokAudioSkipsCustomVoicesAndFeatureOff(t *testing.T) {
	pricing := &pricingProviderStub{audioCost: 1}
	custom := &preauthorizerStub{requires: true}
	guard, err := preauthorizeGrokAudioGatewayRequest(context.Background(), custom, pricing, perRequestTestAPIKey(), nil, []byte(`{}`), "custom-voices", time.Unix(1000, 0))
	require.NoError(t, err)
	require.Nil(t, guard)
	require.Nil(t, custom.captured)

	off := &preauthorizerStub{requires: false}
	guard, err = preauthorizeGrokAudioGatewayRequest(context.Background(), off, pricing, perRequestTestAPIKey(), nil, []byte(`{"input":"billable"}`), "tts", time.Unix(1000, 0))
	require.NoError(t, err)
	require.Nil(t, guard)
	require.Nil(t, off.captured)
}

func TestPreauthorizeGrokRealtimeUsesMinuteHoldAndServerID(t *testing.T) {
	preauthorizer := &preauthorizerStub{requires: true}
	pricing := &pricingProviderStub{audioCost: 0.05}
	model := "grok-voice-latest"
	sessionID := "session-1"

	_, err := preauthorizeGrokRealtimeGatewayRequest(context.Background(), preauthorizer, pricing, perRequestTestAPIKey(), nil, model, sessionID, time.Unix(1000, 0))
	require.NoError(t, err)
	require.NotNil(t, preauthorizer.captured)
	require.Contains(t, preauthorizer.captured.RequestID, "grok_realtime:")
	require.Equal(t, service.PreauthorizationEstimateFixed, preauthorizer.captured.EstimateKind)
	require.Equal(t, 0.05, preauthorizer.captured.FixedAmount)
	require.Equal(t, "realtime", pricing.lastAudioMode)
	require.Equal(t, 1.0, pricing.lastAudioUnit)
	require.Equal(t, service.HashUsageRequestPayload([]byte("grok_realtime:"+model+":"+sessionID)), preauthorizer.captured.AuthorizationFingerprint)
}

func TestPreauthorizeGrokRealtimeSkipsWhenNotRequired(t *testing.T) {
	preauthorizer := &preauthorizerStub{requires: false}
	pricing := &pricingProviderStub{audioCost: 0.05}
	guard, err := preauthorizeGrokRealtimeGatewayRequest(context.Background(), preauthorizer, pricing, perRequestTestAPIKey(), nil, "grok-voice-latest", "", time.Unix(1000, 0))
	require.NoError(t, err)
	require.Nil(t, guard)
	require.Nil(t, preauthorizer.captured)
	require.Empty(t, pricing.lastAudioMode)
}
