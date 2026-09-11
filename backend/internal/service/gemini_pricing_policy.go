package service

import (
	"encoding/json"
	"strings"
)

// Catalog prices are public Standard API rates, excluding launch promotions.
// Apply before the operator's override file and group/channel price cards.
// Source (2026-09-11): https://ai.google.dev/gemini-api/docs/pricing
func applyGeminiStandardCatalogPricing(entries map[string]json.RawMessage) {
	for name, entry := range entries {
		var policy json.RawMessage
		switch normalizeModelNameForPricing(strings.ToLower(name)) {
		case "gemini-3.6-flash":
			policy = json.RawMessage(`{"input_cost_per_token":0.0000015,"output_cost_per_token":0.0000075,"cache_read_input_token_cost":0.00000015,"input_cost_per_token_priority":0.0000027,"output_cost_per_token_priority":0.0000135,"cache_read_input_token_cost_priority":0.00000027}`)
		case "gemini-2.5-flash":
			policy = json.RawMessage(`{"input_cost_per_audio_token":0.000001,"cache_read_input_token_cost_per_audio_token":0.0000001}`)
		default:
			continue
		}
		if merged, ok := mergePricingOverrideEntry(entry, policy); ok {
			entries[name] = merged
		}
	}
}
