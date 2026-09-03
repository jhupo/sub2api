package service

import (
	"context"
	"errors"
	"strings"
	"time"
)

type BalancePreauthorizationRateKind uint8

const (
	BalancePreauthorizationRateText BalancePreauthorizationRateKind = iota
	BalancePreauthorizationRateImage
	BalancePreauthorizationRateVideo
	BalancePreauthorizationRateBase
)

// ResolveBalancePreauthorizationRequestID returns the same request key that
// usage billing will choose for normal HTTP gateway traffic. The request
// logger middleware always supplies a local request ID in production, so an
// upstream response ID cannot split the hold and settlement into two ledgers.
func ResolveBalancePreauthorizationRequestID(ctx context.Context) string {
	return resolveUsageBillingRequestID(ctx, "")
}

// BalancePreauthorizationBillingType mirrors the billing-mode decision in both
// gateway usage recorders.
func BalancePreauthorizationBillingType(apiKey *APIKey, subscription *UserSubscription) int8 {
	// The persisted API key funding source is authoritative. Do not downgrade a
	// subscription key to wallet preauthorization merely because a caller failed
	// to attach the subscription object; that would reserve and later charge the
	// user's wallet for a subscription-funded request.
	if apiKey != nil && apiKey.UsesSubscription() {
		return BillingTypeSubscription
	}
	return BillingTypeBalance
}

// BalancePreauthorizationBillingModel freezes the best model known before an
// upstream account is selected, using the same requested/channel mapping
// policy as post-usage billing. Upstream/response-model policies necessarily
// settle their exact difference after the provider response.
func BalancePreauthorizationBillingModel(requestedModel string, mapping ChannelMappingResult) string {
	requestedModel = strings.TrimSpace(requestedModel)
	switch mapping.BillingModelSource {
	case BillingModelSourceRequested:
		return requestedModel
	case BillingModelSourceChannelMapped:
		if mapped := strings.TrimSpace(mapping.MappedModel); mapped != "" {
			return mapped
		}
	}
	if mapping.Mapped {
		if mapped := strings.TrimSpace(mapping.MappedModel); mapped != "" {
			return mapped
		}
	}
	return requestedModel
}

// BalancePreauthorizationServiceTier resolves the customer-facing tier before
// account selection. A known OpenAI route follows the group's Force/Free Fast
// settlement policy. Non-OpenAI routes retain the client tier, while an
// unresolved composite route reserves conservatively at the higher tier.
func BalancePreauthorizationServiceTier(ctx context.Context, apiKey *APIKey, requestedTier string) string {
	tier := strings.TrimSpace(requestedTier)
	group := balancePreauthorizationAPIKeyGroup(apiKey)
	if group == nil || !groupSupportsOpenAIFast(group.Platform) {
		return tier
	}
	targetPlatform := QuotaPlatform(ctx, apiKey)
	if targetPlatform != "" && targetPlatform != PlatformOpenAI {
		return tier
	}
	if group.ForceOpenAIFast {
		tier = OpenAIFastTierPriority
	}
	// A composite route without a resolved target may still select a non-OpenAI
	// account. Keep the higher reservation until OpenAI is known, because Free
	// Fast is deliberately settled only for OpenAI credentials.
	if targetPlatform == PlatformOpenAI && group.FreeOpenAIFast {
		switch normalizeBillingServiceTier(tier) {
		case OpenAIFastTierPriority, "fast":
			return ""
		}
	}
	return tier
}

// BalancePreauthorizationCostInput returns the same pricing resolver,
// user/group multiplier, peak multiplier, and frozen price instant used by
// GatewayService.RecordUsage.
func (s *GatewayService) BalancePreauthorizationCostInput(
	ctx context.Context,
	apiKey *APIKey,
	model string,
	pricingAt time.Time,
	serviceTier string,
	rateKind BalancePreauthorizationRateKind,
) CostInput {
	multiplier := 1.0
	if s != nil && s.cfg != nil {
		multiplier = s.cfg.Default.RateMultiplier
	}
	if s != nil && apiKey != nil && apiKey.GroupID != nil && apiKey.Group != nil {
		multiplier = s.ResolveUserGroupRateMultiplier(ctx, balancePreauthorizationAPIKeyUserID(apiKey), *apiKey.GroupID, apiKey.Group.RateMultiplier)
	}
	textMultiplier, imageMultiplier := computePeakAwareMultipliers(apiKey, multiplier, pricingAt)
	videoMultiplier := resolveVideoRateMultiplier(apiKey, multiplier)
	multiplier = balancePreauthorizationRateMultiplier(rateKind, textMultiplier, imageMultiplier, videoMultiplier, multiplier)
	resolver := s.gatewayPricingResolver()
	var resolved *ResolvedPricing
	if resolver != nil {
		resolved = resolver.Resolve(ctx, PricingInput{
			Model: strings.TrimSpace(model), GroupID: apiKeyGroupID(apiKey), Group: balancePreauthorizationAPIKeyGroup(apiKey),
		})
		if rateKind != BalancePreauthorizationRateBase && resolved != nil && (resolved.Mode == "" || resolved.Mode == BillingModeToken) {
			multiplier = textMultiplier
		}
	}
	return CostInput{
		Ctx:            ctx,
		Model:          strings.TrimSpace(model),
		GroupID:        apiKeyGroupID(apiKey),
		Group:          balancePreauthorizationAPIKeyGroup(apiKey),
		RateMultiplier: multiplier,
		PricingAt:      pricingAt,
		ServiceTier:    strings.TrimSpace(serviceTier),
		Resolver:       resolver,
		Resolved:       resolved,
	}
}

// BalancePreauthorizationCostInput returns the same pricing resolver,
// user/group multiplier, peak multiplier, and frozen price instant used by
// OpenAIGatewayService.RecordUsage.
func (s *OpenAIGatewayService) BalancePreauthorizationCostInput(
	ctx context.Context,
	apiKey *APIKey,
	model string,
	pricingAt time.Time,
	serviceTier string,
	rateKind BalancePreauthorizationRateKind,
) CostInput {
	multiplier := 1.0
	if s != nil && s.cfg != nil {
		multiplier = s.cfg.Default.RateMultiplier
	}
	if s != nil && apiKey != nil && apiKey.GroupID != nil && apiKey.Group != nil {
		multiplier = s.ResolveUserGroupRateMultiplier(ctx, balancePreauthorizationAPIKeyUserID(apiKey), *apiKey.GroupID, apiKey.Group.RateMultiplier)
	}
	textMultiplier, imageMultiplier := computePeakAwareMultipliers(apiKey, multiplier, pricingAt)
	videoMultiplier := resolveVideoRateMultiplier(apiKey, multiplier)
	multiplier = balancePreauthorizationRateMultiplier(rateKind, textMultiplier, imageMultiplier, videoMultiplier, multiplier)
	resolver := s.openAIGatewayPricingResolver()
	var resolved *ResolvedPricing
	if resolver != nil {
		resolved = resolver.Resolve(ctx, PricingInput{
			Model: strings.TrimSpace(model), GroupID: apiKeyGroupID(apiKey), Group: balancePreauthorizationAPIKeyGroup(apiKey),
		})
		if rateKind != BalancePreauthorizationRateBase && resolved != nil && (resolved.Mode == "" || resolved.Mode == BillingModeToken) {
			multiplier = textMultiplier
		}
	}
	return CostInput{
		Ctx:            ctx,
		Model:          strings.TrimSpace(model),
		GroupID:        apiKeyGroupID(apiKey),
		Group:          balancePreauthorizationAPIKeyGroup(apiKey),
		RateMultiplier: multiplier,
		PricingAt:      pricingAt,
		ServiceTier:    strings.TrimSpace(serviceTier),
		Resolver:       resolver,
		Resolved:       resolved,
	}
}

func balancePreauthorizationRateMultiplier(kind BalancePreauthorizationRateKind, text, image, video, base float64) float64 {
	switch kind {
	case BalancePreauthorizationRateImage:
		return image
	case BalancePreauthorizationRateVideo:
		return video
	case BalancePreauthorizationRateBase:
		return base
	default:
		return text
	}
}

// BalancePreauthorizationWebSearchCost uses the same fixed-price calculator
// and base multiplier as alpha-search settlement.
func (s *OpenAIGatewayService) BalancePreauthorizationWebSearchCost(
	ctx context.Context,
	apiKey *APIKey,
	pricingAt time.Time,
) (float64, error) {
	if s == nil || s.billingService == nil || apiKey == nil {
		return 0, balancePreauthorizationUnavailable(errors.New("web search preauthorization pricing is unavailable"))
	}
	pricing := s.BalancePreauthorizationCostInput(
		ctx, apiKey, "", pricingAt, "", BalancePreauthorizationRateBase,
	)
	breakdown := s.billingService.CalculateWebSearchCost(
		1, webSearchPricePerCallFromAPIKey(apiKey), pricing.RateMultiplier,
	)
	if breakdown == nil || invalidNonnegativeMoney(breakdown.ActualCost) {
		return 0, balancePreauthorizationUnavailable(ErrInvalidBillingPreauthorizationEstimate)
	}
	return breakdown.ActualCost, nil
}

// BalancePreauthorizationAudioCost uses the exact audio branch used by OpenAI
// usage settlement. Audio may have channel per-request pricing; otherwise it
// is priced from the group/default audio schedule.
func (s *OpenAIGatewayService) BalancePreauthorizationAudioCost(
	ctx context.Context,
	apiKey *APIKey,
	model string,
	mode string,
	units float64,
	pricingAt time.Time,
) (float64, error) {
	if s == nil || s.billingService == nil || apiKey == nil || apiKey.Group == nil || invalidNonnegativeMoney(units) {
		return 0, balancePreauthorizationUnavailable(ErrInvalidBillingPreauthorizationEstimate)
	}
	mode = strings.TrimSpace(mode)
	switch mode {
	case "tts", "stt", "realtime":
	default:
		return 0, balancePreauthorizationUnavailable(ErrInvalidBillingPreauthorizationEstimate)
	}
	pricing := s.BalancePreauthorizationCostInput(ctx, apiKey, model, pricingAt, "", BalancePreauthorizationRateBase)
	var breakdown *CostBreakdown
	var err error
	if pricing.Resolved != nil && pricing.Resolved.Mode == BillingModePerRequest {
		breakdown, err = s.billingService.CalculateCostUnified(CostInput{
			Ctx:            ctx,
			Model:          strings.TrimSpace(model),
			GroupID:        apiKey.GroupID,
			Group:          apiKey.Group,
			UsageUnits:     units,
			SizeTier:       mode,
			RateMultiplier: pricing.RateMultiplier,
			PricingAt:      pricingAt,
			Resolver:       pricing.Resolver,
			Resolved:       pricing.Resolved,
		})
	} else {
		breakdown = s.billingService.CalculateAudioCost(mode, units, groupAudioPriceConfigFromAPIKey(apiKey), pricing.RateMultiplier)
	}
	if err != nil || breakdown == nil || invalidNonnegativeMoney(breakdown.ActualCost) {
		if err == nil {
			err = ErrInvalidBillingPreauthorizationEstimate
		}
		return 0, balancePreauthorizationUnavailable(err)
	}
	return breakdown.ActualCost, nil
}

// BalancePreauthorizationSearchCost uses the same per-1k search calculator
// and peak-aware text multiplier as GatewayService.RecordUsage.
func (s *GatewayService) BalancePreauthorizationSearchCost(
	ctx context.Context,
	apiKey *APIKey,
	pricingAt time.Time,
) (float64, error) {
	if s == nil || s.billingService == nil || apiKey == nil {
		return 0, balancePreauthorizationUnavailable(errors.New("search preauthorization pricing is unavailable"))
	}
	pricing := s.BalancePreauthorizationCostInput(
		ctx, apiKey, "", pricingAt, "", BalancePreauthorizationRateText,
	)
	breakdown := s.billingService.CalculateSearchCost(
		1, groupSearchPricePer1kFromAPIKey(apiKey), pricing.RateMultiplier,
	)
	if breakdown == nil || invalidNonnegativeMoney(breakdown.ActualCost) {
		return 0, balancePreauthorizationUnavailable(ErrInvalidBillingPreauthorizationEstimate)
	}
	return breakdown.ActualCost, nil
}

func (s *GatewayService) gatewayPricingResolver() *ModelPricingResolver {
	if s == nil {
		return nil
	}
	return s.resolver
}

func (s *OpenAIGatewayService) openAIGatewayPricingResolver() *ModelPricingResolver {
	if s == nil {
		return nil
	}
	return s.resolver
}

func apiKeyGroupID(apiKey *APIKey) *int64 {
	if apiKey == nil {
		return nil
	}
	return apiKey.GroupID
}

func balancePreauthorizationAPIKeyGroup(apiKey *APIKey) *Group {
	if apiKey == nil {
		return nil
	}
	return apiKey.Group
}

func balancePreauthorizationAPIKeyUserID(apiKey *APIKey) int64 {
	if apiKey == nil {
		return 0
	}
	if apiKey.UserID > 0 {
		return apiKey.UserID
	}
	if apiKey.User != nil {
		return apiKey.User.ID
	}
	return 0
}

// TransferBalancePreauthorizationToUsageTask moves the sole active guard owner
// into the worker context. The old handler handle becomes a harmless stale
// owner, so its deferred Refund cannot undo worker-owned settlement.
func TransferBalancePreauthorizationToUsageTask(
	guard *BalancePreauthorizationGuard,
	task UsageRecordTask,
) (UsageRecordTask, bool) {
	if task == nil {
		return nil, false
	}
	if guard == nil {
		return task, true
	}
	workerGuard, ok := guard.TransferToWorker()
	if !ok {
		// A duplicate submission is an invariant violation, but dropping the
		// opaque task would also discard its non-money side effects. Keep the
		// stale guard attached: applyUsageBilling rejects it before repo.Apply,
		// while the rest of the task remains observable and runnable.
		return func(ctx context.Context) {
			task(ContextWithBalancePreauthorizationGuard(ctx, guard))
		}, false
	}
	return func(ctx context.Context) {
		task(ContextWithBalancePreauthorizationGuard(ctx, workerGuard))
	}, true
}
