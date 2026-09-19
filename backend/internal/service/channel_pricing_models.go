package service

import (
	"sort"
	"strings"
)

// ChannelPricingModelIDs returns billing model names, not the Claude aliases
// advertised by the CN providers' Messages-compatible gateway.
func (s *BillingService) ChannelPricingModelIDs(platform string) []string {
	platform = strings.ToLower(strings.TrimSpace(platform))
	if platform == PlatformComposite {
		return nil
	}
	if platform != PlatformDeepseek && platform != PlatformKimi && platform != PlatformZhipu {
		return BuiltInModelIDsForPlatform(platform)
	}
	models := make(map[string]struct{})
	add := func(model string) {
		if provider, id, qualified := strings.Cut(model, "/"); qualified {
			// Only unwrap the original provider, never a reseller's catalog.
			provider = strings.ToLower(provider)
			if !((platform == PlatformDeepseek && provider == "deepseek") ||
				(platform == PlatformKimi && (provider == "moonshot" || provider == "moonshotai" || provider == "kimi")) ||
				(platform == PlatformZhipu && (provider == "zai" || provider == "zai_glm" || provider == "zhipu"))) {
				return
			}
			model = id
		}
		if isNativeChannelPricingModel(platform, model) {
			models[model] = struct{}{}
		}
	}
	for model := range s.fallbackPrices {
		add(model)
	}
	if s.pricingService != nil {
		s.pricingService.mu.RLock()
		for model := range s.pricingService.pricingData {
			add(model)
		}
		s.pricingService.mu.RUnlock()
	}
	// These upstream response names share the official deepseek-flash price card.
	if platform == PlatformDeepseek {
		add("deepseek-v4.1-flash")
		add("deepseek-v4.1-flash-0910")
	}
	ids := make([]string, 0, len(models))
	for model := range models {
		ids = append(ids, model)
	}
	sort.Strings(ids)
	return ids
}

func isNativeChannelPricingModel(platform, model string) bool {
	// Qualified names belong to other providers' hosting/region price cards.
	if strings.Contains(model, "/") {
		return false
	}
	model = strings.ToLower(model)
	switch platform {
	case PlatformDeepseek:
		return strings.HasPrefix(model, "deepseek-")
	case PlatformKimi:
		return strings.HasPrefix(model, "kimi-") || strings.HasPrefix(model, "moonshot-") || model == "k3" || model == "k3-256k"
	case PlatformZhipu:
		return strings.HasPrefix(model, "glm-")
	default:
		return false
	}
}
