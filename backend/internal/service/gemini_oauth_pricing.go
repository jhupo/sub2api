package service

import (
	"context"
	"fmt"
)

// ValidateGeminiOAuthPricing is a read-only preflight, independent of the
// preauthorization switch. It uses the settlement candidates and calculator;
// discovering a model must not authorize unpriced token traffic.
func (s *GatewayService) ValidateGeminiOAuthPricing(ctx context.Context, apiKey *APIKey, account *Account, requested, forwarded string, mapping ChannelMappingResult) error {
	if account == nil || !account.IsGeminiAntigravity() {
		return nil
	}
	if s == nil || s.billingService == nil || apiKey == nil {
		return fmt.Errorf("%w: Gemini OAuth pricing service is unavailable", ErrModelPricingUnavailable)
	}
	upstream := account.GetMappedModel(forwarded)
	// Non-token image backends retain their separate per-image pricing.
	if (isImageGenerationModel(forwarded) || isImageGenerationModel(upstream)) &&
		!isGeminiTokenImageModel(forwarded) && !isGeminiTokenImageModel(upstream) {
		return nil
	}
	model := forwarded
	if mapping.BillingModelSource == BillingModelSourceRequested {
		model = requested
	} else if mapping.BillingModelSource == BillingModelSourceChannelMapped && mapping.MappedModel != "" {
		model = mapping.MappedModel
	} else if mapping.BillingModelSource == BillingModelSourceUpstream && upstream != "" {
		model = upstream
	}
	if apiKey.Group != nil && apiKey.Group.Platform == PlatformComposite {
		model = s.compositeBillableModel(ctx, apiKey, model, forwarded)
	}
	model = s.geminiOAuthBillableModel(ctx, apiKey, model, upstream, forwarded)
	if identified, _ := s.hasIdentifiedResponseModelPricing(ctx, model, apiKey); !identified {
		return fmt.Errorf("%w: configure an explicit price for Gemini OAuth model %s", ErrModelPricingUnavailable, model)
	}
	_, err := s.billingService.CalculateTokenCostForRequest(TokenCostRequest{
		Ctx: ctx, Model: model, Group: apiKey.Group,
		Tokens: UsageTokens{InputTokens: 1, OutputTokens: 1}, RateMultiplier: 1,
		PricingAt: GatewayTokenRequestPricingAtFromContext(ctx), Resolver: s.resolver,
	})
	return err
}

// Gemini OAuth aliases must not stop at a family-name guess when an actual
// mapped model has a known price. Preflight and settlement share this rule.
func (s *GatewayService) geminiOAuthBillableModel(ctx context.Context, apiKey *APIKey, selected string, fallbacks ...string) string {
	for _, model := range append([]string{selected}, fallbacks...) {
		if identified, _ := s.hasIdentifiedResponseModelPricing(ctx, model, apiKey); identified {
			return model
		}
	}
	return selected
}

func isGeminiTokenImageModel(model string) bool {
	switch normalizeModelNameForPricing(model) {
	case "gemini-3.1-flash-image", "gemini-3.1-flash-image-preview",
		"gemini-3-pro-image", "gemini-3-pro-image-preview", "gemini-2.5-flash-image":
		return true
	default:
		return false
	}
}
