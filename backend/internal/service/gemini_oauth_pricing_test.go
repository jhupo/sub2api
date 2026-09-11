package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestGeminiOAuthPricingPreflight(t *testing.T) {
	account := &Account{Platform: PlatformGemini, Type: AccountTypeOAuth, Credentials: map[string]any{
		"oauth_type": GeminiOAuthTypeAntigravity,
	}}
	apiKey := &APIKey{Group: &Group{Platform: PlatformGemini}}
	billing := NewBillingService(&config.Config{}, nil)
	svc := &GatewayService{billingService: billing}
	ctx := context.Background()
	require.ErrorIs(t, svc.ValidateGeminiOAuthPricing(ctx, apiKey, account, "unknown-runtime", "unknown-runtime", ChannelMappingResult{}), ErrModelPricingUnavailable)
	require.ErrorIs(t, svc.ValidateGeminiOAuthPricing(ctx, apiKey, account, "gpt-oss-120b", "gpt-oss-120b", ChannelMappingResult{}), ErrModelPricingUnavailable)
	require.NoError(t, svc.ValidateGeminiOAuthPricing(ctx, apiKey, account, "gemini-3.1-pro-low", "gemini-3.1-pro-low", ChannelMappingResult{}))
	// Existing public aliases remain billable through the configured source.
	require.NoError(t, svc.ValidateGeminiOAuthPricing(ctx, apiKey, account, "gemini-3.1-pro", "gemini-pro-agent", ChannelMappingResult{BillingModelSource: BillingModelSourceRequested}))
	require.ErrorIs(t, svc.ValidateGeminiOAuthPricing(ctx, apiKey, account, "unknown-runtime", "unknown-runtime", ChannelMappingResult{BillingModelSource: BillingModelSourceRequested}), ErrModelPricingUnavailable)
	// Explicit pricing, including deliberately free pricing, is authoritative.
	zero := 0.0
	apiKey.Group.ModelPricing = []ChannelModelPricing{{Models: []string{"unknown-runtime"}, InputPrice: &zero, OutputPrice: &zero}}
	svc.resolver = NewModelPricingResolver(nil, billing)
	require.NoError(t, svc.ValidateGeminiOAuthPricing(ctx, apiKey, account, "unknown-runtime", "unknown-runtime", ChannelMappingResult{}))
	account.Type = AccountTypeAPIKey
	require.NoError(t, svc.ValidateGeminiOAuthPricing(ctx, apiKey, account, "unknown-runtime", "unknown-runtime", ChannelMappingResult{}))
}
