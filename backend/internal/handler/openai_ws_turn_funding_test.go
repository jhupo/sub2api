package handler

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type wsFundingAuthorizer struct {
	requests []service.BalancePreauthorizationRequest
	enabled  bool
}

func (a *wsFundingAuthorizer) RequiresPreauthorization(_ context.Context, billing int8) bool {
	return a.enabled || billing == service.BillingTypeSubscription
}
func (a *wsFundingAuthorizer) Preauthorize(_ context.Context, request service.BalancePreauthorizationRequest) (*service.BalancePreauthorizationGuard, error) {
	a.requests = append(a.requests, request)
	return nil, nil
}

func TestOpenAIWSFundingBusinessTurnIdentityAndContinuation(t *testing.T) {
	a := &wsFundingAuthorizer{enabled: true}
	h := &OpenAIGatewayHandler{balancePreauthorizer: a, gatewayService: &service.OpenAIGatewayService{}}
	key := &service.APIKey{ID: 1, UserID: 2, FundingSource: service.FundingSourceWallet}
	f := &openAIWSTurnFunding{}
	ctx, at := context.Background(), time.Now()
	body := []byte(`{"type":"response.create","model":"gpt-test","input":"hello"}`)
	require.NoError(t, f.prepare(ctx, h, 1, key, nil, body, "gpt-test", at))
	require.NoError(t, f.observe(ctx, []byte(`{"type":"response.created","response":{"id":"resp_1"}}`)))
	require.NoError(t, f.prepare(ctx, h, 1, key, nil, body, "gpt-test", at.Add(time.Minute)))
	require.Equal(t, a.requests[0].RequestID, a.requests[1].RequestID)
	require.Equal(t, at, a.requests[1].CostInput.PricingAt)
	require.NoError(t, f.observe(ctx, []byte(`{"type":"response.output_text.delta","delta":"hello"}`)))
	require.Error(t, f.prepare(ctx, h, 1, key, nil, body, "gpt-test", at))
	first := f.finish(1, &service.OpenAIForwardResult{RequestID: "resp_1", Usage: service.OpenAIUsage{InputTokens: 1000, OutputTokens: 50}})
	require.NotNil(t, first)
	require.Nil(t, f.finish(1, &service.OpenAIForwardResult{}))
	body = []byte(`{"type":"response.create","model":"gpt-other","previous_response_id":"resp_1","input":"next"}`)
	require.NoError(t, f.prepare(ctx, h, 2, key, nil, body, "gpt-other", at))
	last := a.requests[len(a.requests)-1]
	require.NotEqual(t, first.id, last.RequestID)
	require.GreaterOrEqual(t, last.EstimatedInputTokens, 1050)
	require.Equal(t, "gpt-other", last.CostInput.Model)
	require.NotEqual(t, first.fingerprint, last.AuthorizationFingerprint)
}

func TestOpenAIWSFundingWalletSwitchDoesNotDisableSubscription(t *testing.T) {
	for _, source := range []string{service.FundingSourceWallet, service.FundingSourceSubscription} {
		t.Run(source, func(t *testing.T) {
			a := &wsFundingAuthorizer{}
			h := &OpenAIGatewayHandler{balancePreauthorizer: a, gatewayService: &service.OpenAIGatewayService{}}
			subID := int64(8)
			key := &service.APIKey{ID: 1, UserID: 2, FundingSource: source}
			if source == service.FundingSourceSubscription {
				key.SubscriptionID = &subID
			}
			f := &openAIWSTurnFunding{}
			require.NoError(t, f.prepare(context.Background(), h, 1, key, nil, []byte(`{"model":"gpt-test","input":"hi"}`), "gpt-test", time.Now()))
			if source == service.FundingSourceWallet {
				require.Empty(t, a.requests)
			} else {
				require.Len(t, a.requests, 1)
				require.Equal(t, service.BillingTypeSubscription, a.requests[0].BillingType)
				require.Equal(t, subID, a.requests[0].SubscriptionID)
			}
		})
	}
}

func TestOpenAIWSFundingRejectsUnverifiedContinuationAndCancellation(t *testing.T) {
	a := &wsFundingAuthorizer{enabled: true}
	h := &OpenAIGatewayHandler{balancePreauthorizer: a, gatewayService: &service.OpenAIGatewayService{}}
	key := &service.APIKey{ID: 1, UserID: 2, FundingSource: service.FundingSourceWallet}
	f := &openAIWSTurnFunding{}
	err := f.prepare(context.Background(), h, 1, key, nil, []byte(`{"previous_response_id":"foreign","input":"next"}`), "gpt-test", time.Now())
	require.Error(t, err)
	require.Empty(t, a.requests)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, f.observe(ctx, []byte(`{"type":"response.output_text.delta","delta":"x"}`)), context.Canceled)
}
