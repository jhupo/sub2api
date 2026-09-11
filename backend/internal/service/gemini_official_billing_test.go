package service

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func officialGeminiBilling(t *testing.T) (*BillingService, *GatewayService, *APIKey) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "resources", "model-pricing", "model_prices_and_context_window.json"))
	require.NoError(t, err)
	catalog := &PricingService{}
	catalog.pricingData, err = catalog.parsePricingData(body)
	require.NoError(t, err)
	billing := NewBillingService(&config.Config{}, catalog)
	group := &Group{ID: 10, Platform: PlatformGemini, RateMultiplier: 2, LongContextPricingEnabled: true}
	key := &APIKey{GroupID: &group.ID, Group: group, UserID: 1}
	gateway := &GatewayService{billingService: billing, resolver: NewModelPricingResolver(nil, billing)}
	return billing, gateway, key
}

func TestGeminiOfficialPricingReasoningLevels(t *testing.T) {
	billing, _, _ := officialGeminiBilling(t)
	for _, model := range []string{"gemini-2.5-pro", "gemini-2.5-flash", "gemini-3.1-pro", "gemini-3.5-flash", "gemini-3.6-flash"} {
		base, err := billing.GetModelPricing(model)
		require.NoError(t, err)
		for _, suffix := range []string{"-low", "-medium", "-high", "-extra-low", "-extra-high", "-minimal", "-tiered"} {
			got, err := billing.GetModelPricing(model + suffix)
			require.NoError(t, err)
			require.Equal(t, base, got, model+suffix)
			require.True(t, billing.HasIdentifiedTokenPricing(model+suffix))
		}
	}
	require.False(t, billing.HasIdentifiedTokenPricing("gemini-pro-agent"))
	require.False(t, billing.HasIdentifiedTokenPricing("gemini-3.6-flash-unknown"))
}

func TestGeminiOfficialAudioUsageAndMultiplier(t *testing.T) {
	_, gateway, key := officialGeminiBilling(t)
	usage := extractGeminiUsage([]byte(`{"usageMetadata":{"promptTokenCount":1000,"cachedContentTokenCount":200,"candidatesTokenCount":100,"thoughtsTokenCount":50,"promptTokensDetails":[{"modality":"AUDIO","tokenCount":600}],"cacheTokensDetails":[{"modality":"AUDIO","tokenCount":100}]}}`))
	require.NotNil(t, usage)
	require.Equal(t, 800, usage.InputTokens)
	require.Equal(t, 500, usage.AudioInputTokens)
	require.Equal(t, 100, usage.AudioCacheReadTokens)
	require.Equal(t, 150, usage.OutputTokens)
	result := &ForwardResult{Usage: *usage, Model: "gemini-2.5-flash"}
	cost := gateway.calculateRecordUsageCost(context.Background(), result, key, result.Model, 2, 9, time.Now())
	want := 300*0.3e-6 + 500*1e-6 + 100*0.03e-6 + 100*0.1e-6 + 150*2.5e-6
	require.InDelta(t, want, cost.TotalCost, 1e-12)
	require.InDelta(t, want*2, cost.ActualCost, 1e-12)
}

func TestGeminiOfficialImageIncludesPromptAndThinking(t *testing.T) {
	_, gateway, key := officialGeminiBilling(t)
	result := &ForwardResult{Model: "gemini-3.1-flash-image", ImageCount: 1, ImageSize: "1K", Usage: ClaudeUsage{
		InputTokens: 1000, OutputTokens: 1220, ImageOutputTokens: 1120,
	}}
	cost := gateway.calculateRecordUsageCost(context.Background(), result, key, result.Model, 2, 9, time.Now())
	want := 1000*0.5e-6 + 100*3e-6 + 1120*60e-6
	require.InDelta(t, want, cost.TotalCost, 1e-12)
	require.InDelta(t, want*2, cost.ActualCost, 1e-12)
	require.Equal(t, string(BillingModeToken), cost.BillingMode)
}

func TestGeminiOfficialPreauthorizationMatchesSettlement(t *testing.T) {
	billing, gateway, key := officialGeminiBilling(t)
	for _, model := range []string{"gemini-3.1-pro-high", "gemini-3.5-flash-low", "gemini-3.6-flash-high", "gemini-3.1-flash-image"} {
		pricingAt := time.Now()
		input := gateway.BalancePreauthorizationCostInput(context.Background(), key, model, pricingAt, "", BalancePreauthorizationRateText)
		input.Tokens = UsageTokens{InputTokens: 300000, OutputTokens: 2000}
		reserved, err := billing.CalculateCostUnified(input)
		require.NoError(t, err)
		result := &ForwardResult{Model: model, Usage: ClaudeUsage{InputTokens: 300000, OutputTokens: 2000}}
		settled := gateway.calculateRecordUsageCost(context.Background(), result, key, model, 2, 9, pricingAt)
		require.InDelta(t, reserved.ActualCost, settled.ActualCost, 1e-12, model)
	}
}

func TestGeminiOfficialGroupBasePriceAppliesToEffort(t *testing.T) {
	_, gateway, key := officialGeminiBilling(t)
	price := 10e-6
	key.Group.ModelPricing = []ChannelModelPricing{{Models: []string{"gemini-3.6-flash"}, InputPrice: &price}}
	resolved := gateway.resolver.Resolve(context.Background(), PricingInput{Model: "gemini-3.6-flash-high", Group: key.Group, GroupID: key.GroupID})
	require.Equal(t, PricingSourceGroup, resolved.Source)
	require.Equal(t, price, resolved.BasePricing.InputPricePerToken)
	key.Group.ModelPricing[0].Models = []string{"gemini-3.1-pro"}
	resolved = gateway.resolver.Resolve(context.Background(), PricingInput{Model: "gemini-3.1-pro-high", Group: key.Group, GroupID: key.GroupID})
	require.Equal(t, PricingSourceGroup, resolved.Source)
	require.Equal(t, price, resolved.BasePricing.InputPricePerToken)
	key.Group.ModelPricing[0].Models = []string{"gemini-3.6-flash-high"}
	resolved = gateway.resolver.Resolve(context.Background(), PricingInput{Model: "gemini-3.6-flash-low", Group: key.Group, GroupID: key.GroupID})
	require.NotEqual(t, PricingSourceGroup, resolved.Source)
}

func TestGeminiOfficialStandardPricesIgnoreCatalogPromotion(t *testing.T) {
	catalog := &PricingService{}
	data, err := catalog.parsePricingData([]byte(`{"gemini-3.6-flash":{"input_cost_per_token":0.00000075,"output_cost_per_token":0.00000375,"cache_read_input_token_cost":0.000000075}}`))
	require.NoError(t, err)
	require.Equal(t, 1.5e-6, data["gemini-3.6-flash"].InputCostPerToken)
	require.Equal(t, 7.5e-6, data["gemini-3.6-flash"].OutputCostPerToken)
	require.Equal(t, 0.15e-6, data["gemini-3.6-flash"].CacheReadInputTokenCost)
}

func TestGeminiOfficialAliasDoesNotUseFamilyGuess(t *testing.T) {
	_, gateway, key := officialGeminiBilling(t)
	alias := "gemini-3.1-pro-private-alias"
	account := &Account{Platform: PlatformGemini, Type: AccountTypeOAuth, Credentials: map[string]any{
		"oauth_type": GeminiOAuthTypeAntigravity, "model_mapping": map[string]any{alias: "gemini-3.6-flash"},
	}}
	require.Equal(t, "gemini-3.6-flash", gateway.geminiOAuthBillableModel(context.Background(), key, alias, "gemini-3.6-flash"))
	require.NoError(t, gateway.ValidateGeminiOAuthPricing(context.Background(), key, account, alias, alias, ChannelMappingResult{}))
}

func TestGeminiOfficialWalletAndSubscriptionUseActualCost(t *testing.T) {
	_, _, key := officialGeminiBilling(t)
	params := &postUsageBillingParams{Cost: &CostBreakdown{TotalCost: 1, ActualCost: 2},
		User: &User{ID: 1}, APIKey: key, Account: &Account{ID: 5, Platform: PlatformGemini, Type: AccountTypeOAuth}, AccountRateMultiplier: 9,
	}
	wallet := buildUsageBillingCommand("wallet", nil, params)
	require.Equal(t, 2.0, wallet.BalanceCost)
	require.Zero(t, wallet.SubscriptionCost)
	params.IsSubscriptionBill = true
	params.Subscription = &UserSubscription{ID: 7}
	plan := buildUsageBillingCommand("plan", nil, params)
	require.Equal(t, 2.0, plan.SubscriptionCost)
	require.Zero(t, plan.BalanceCost)
}

func TestGeminiOfficialMultimodalReservationEstimate(t *testing.T) {
	for _, tc := range []struct {
		size   string
		tokens int
	}{{"0.5K", 747}, {"1K", 1120}, {"2K", 1680}, {"4K", 2520}, {"", 2520}} {
		body := []byte(`{"generationConfig":{"imageConfig":{"imageSize":"` + tc.size + `"}}}`)
		require.Equal(t, tc.tokens, GeminiImageReservationTokens("gemini-3.1-flash-image", body))
	}
	estimate := EstimateBalancePreauthorizationTokens([]byte(`{"contents":[{"parts":[{"inlineData":{"mimeType":"audio/wav","data":"AAAA"}}]}]}`))
	require.Positive(t, estimate.AudioInputTokens)
	require.Equal(t, estimate.InputTokens, estimate.AudioInputTokens)
	text := EstimateBalancePreauthorizationTokens([]byte(`{"messages":[{"content":"audio/wav is only text"}]}`))
	require.Zero(t, text.AudioInputTokens)
	tool := EstimateBalancePreauthorizationTokens([]byte(`{"messages":[{"content":"hello"}],"tools":[{"parameters":{"example":{"mimeType":"audio/wav"}}}]}`))
	require.Zero(t, tool.AudioInputTokens)
}
