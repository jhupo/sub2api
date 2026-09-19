package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestDeepSeekFlashPricingFallbackAndUsage(t *testing.T) {
	billing := NewBillingService(&config.Config{}, nil)
	gateway := &OpenAIGatewayService{billingService: billing}
	for _, model := range []string{"deepseek-flash", "deepseek-v4.1-flash", "deepseek-v4.1-flash-0910", "provider/DeepSeek-V4.1-Flash"} {
		t.Run(model, func(t *testing.T) {
			require.True(t, billing.HasIdentifiedTokenPricing(model))
			pricing, err := billing.GetModelPricing(model)
			require.NoError(t, err)
			require.InDelta(t, 0.30e-6, pricing.InputPricePerToken, 1e-15)
			require.InDelta(t, 1.20e-6, pricing.OutputPricePerToken, 1e-15)
			require.InDelta(t, 0.006e-6, pricing.CacheReadPricePerToken, 1e-15)
			// Actual token counts from the zero-cost request on September 17.
			cost, err := gateway.calculateOpenAIRecordUsageCost(context.Background(), &OpenAIForwardResult{}, &APIKey{}, []string{model}, 2, 2, 2, 2,
				UsageTokens{InputTokens: 199, OutputTokens: 208, CacheReadTokens: 179456}, "", nil, time.Now())
			require.NoError(t, err)
			require.InDelta(t, 0.001386036, cost.TotalCost, 1e-12)
			require.InDelta(t, 0.002772072, cost.ActualCost, 1e-12)
		})
	}
	for _, model := range []string{"deepseek-v4.1-pro", "deepseek-v4.1-flash-unknown", "deepseek-v9-flash"} {
		_, err := billing.GetModelPricing(model)
		require.ErrorIs(t, err, ErrModelPricingUnavailable)
		require.False(t, billing.HasIdentifiedTokenPricing(model))
	}
}

func TestDeepSeekFlashPricingDynamicAndExplicitPrecedence(t *testing.T) {
	canonical := &LiteLLMModelPricing{InputCostPerToken: 0.4e-6, OutputCostPerToken: 1.5e-6, CacheReadInputTokenCost: 0.01e-6}
	catalog := &PricingService{pricingData: map[string]*LiteLLMModelPricing{"deepseek-flash": canonical}}
	billing := NewBillingService(&config.Config{}, catalog)
	for _, model := range []string{"deepseek-flash", "deepseek-v4.1-flash", "deepseek-v4.1-flash-0910", "provider/DeepSeek-V4.1-Flash"} {
		require.Same(t, canonical, catalog.GetIdentifiedModelPricing(model))
		pricing, err := billing.GetModelPricing(model)
		require.NoError(t, err)
		require.Equal(t, canonical.InputCostPerToken, pricing.InputPricePerToken, "dynamic rates must take priority over fallback")
	}
	// An explicitly priced variant must not be overridden by the canonical card.
	explicit := &LiteLLMModelPricing{InputCostPerToken: 0.5e-6, OutputCostPerToken: 2e-6}
	catalog.pricingData["deepseek-v4.1-flash-0910"] = explicit
	require.Same(t, explicit, catalog.GetModelPricing("deepseek-v4.1-flash-0910"))
	input, output, cache := 3e-6, 8e-6, 0.2e-6
	pricing, err := billing.GetModelPricingWithChannel("deepseek-v4.1-flash", &ChannelModelPricing{InputPrice: &input, OutputPrice: &output, CacheReadPrice: &cache})
	require.NoError(t, err)
	require.InDelta(t, 3e-6, pricing.InputPricePerToken, 1e-15)
	require.InDelta(t, 8e-6, pricing.OutputPricePerToken, 1e-15)
	require.InDelta(t, 0.2e-6, pricing.CacheReadPricePerToken, 1e-15)
}
