package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChannelPricingModelsUseNativeCatalog(t *testing.T) {
	catalog := &PricingService{pricingData: map[string]*LiteLLMModelPricing{
		"deepseek/deepseek-test-new": {}, "deepseek-test-new": {},
		"moonshot/kimi-test-new": {}, "zai/glm-test-new": {},
		"bedrock/deepseek-other": {}, "openrouter/glm-other": {},
		"claude-test": {}, "gpt-test": {},
	}}
	billing := NewBillingService(nil, catalog)
	for _, tc := range []struct{ platform, latest, fallback string }{
		{PlatformDeepseek, "deepseek-test-new", "deepseek-v4.1-flash"},
		{PlatformKimi, "kimi-test-new", "kimi-k3"},
		{PlatformZhipu, "glm-test-new", "glm-5.2"},
	} {
		t.Run(tc.platform, func(t *testing.T) {
			models := billing.ChannelPricingModelIDs(tc.platform)
			require.Contains(t, models, tc.latest)
			require.Contains(t, models, tc.fallback)
			require.NotContains(t, models, "claude-test")
			require.NotContains(t, models, "deepseek-other")
			require.NotContains(t, models, "glm-other")
			seen := map[string]bool{}
			for _, model := range models {
				require.True(t, isNativeChannelPricingModel(tc.platform, model), model)
				require.False(t, seen[model], model)
				seen[model] = true
			}
		})
	}
	require.Nil(t, billing.ChannelPricingModelIDs(PlatformComposite))
	require.Nil(t, billing.ChannelPricingModelIDs("unknown"))
}
