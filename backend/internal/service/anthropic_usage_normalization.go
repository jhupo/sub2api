package service

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/tidwall/gjson"
)

func mergeAnthropicUsage(dst *ClaudeUsage, src apicompat.AnthropicUsage) {
	if dst == nil {
		return
	}

	cacheReadTokens := max(src.CacheReadInputTokens, 0)
	hasCacheReadTokens := src.CacheReadInputTokensPresent || src.CacheReadInputTokens != 0
	if cacheReadTokens == 0 && (src.CachedTokensPresent || src.CachedTokens != 0) {
		cacheReadTokens = max(src.CachedTokens, 0)
		hasCacheReadTokens = true
	}
	if cacheReadTokens == 0 && src.PromptTokensDetails != nil && (src.PromptTokensDetails.CachedTokensPresent || src.PromptTokensDetails.CachedTokens != 0) {
		cacheReadTokens = max(src.PromptTokensDetails.CachedTokens, 0)
		hasCacheReadTokens = true
	}
	if cacheReadTokens == 0 && src.PromptCacheHitTokens != nil {
		cacheReadTokens = max(*src.PromptCacheHitTokens, 0)
		hasCacheReadTokens = true
	}
	mergeAnthropicCacheCreationUsage(dst, src)

	// Some Anthropic-compatible providers retain OpenAI-style prompt/cache
	// fields. Prefer those authoritative totals or hit/miss buckets over the
	// overloaded input_tokens field. This covers Kimi's changing stream
	// semantics as well as GLM/DeepSeek cache aliases.
	if src.PromptTokens <= 0 && src.PromptCacheMissTokens == nil && !dst.promptTotalPresent {
		// Without an authoritative prompt total or miss bucket, input_tokens is
		// provider-specific: it may already be the uncached bucket, or it may be
		// a total from an earlier event. Do not infer a subtraction merely because
		// a later event contains cache buckets; that would corrupt providers whose
		// stream uses independent input and cache fields.
		if src.InputTokens > 0 {
			dst.InputTokens = src.InputTokens
		}
	}
	if hasCacheReadTokens && (cacheReadTokens > 0 || dst.CacheReadInputTokens <= 0) {
		dst.CacheReadInputTokens = cacheReadTokens
	}
	normalizeAnthropicPromptTotal(dst, src.PromptTokens, src.PromptCacheMissTokens)
	if src.OutputTokens > 0 {
		dst.OutputTokens = src.OutputTokens
	}
}

func normalizeAnthropicPromptTotal(usage *ClaudeUsage, promptTokens int, promptCacheMissTokens *int) {
	if promptCacheMissTokens != nil {
		usage.promptTotalPresent = false
		usage.InputTokens = max(*promptCacheMissTokens, 0)
		return
	}
	if promptTokens > 0 {
		usage.promptTotalTokens = promptTokens
		usage.promptTotalPresent = true
	}
	if usage.promptTotalPresent {
		usage.InputTokens = max(usage.promptTotalTokens-usage.CacheReadInputTokens-usage.CacheCreationInputTokens, 0)
	}
}

func mergeAnthropicCacheCreationUsage(dst *ClaudeUsage, src apicompat.AnthropicUsage) {
	if src.HasCacheCreationInputTokens() && (src.CacheCreationInputTokens > 0 || dst.CacheCreationInputTokens <= 0) {
		dst.CacheCreationInputTokens = max(src.CacheCreationInputTokens, 0)
		dst.cacheCreationInputTokensPresent = true
		dst.cacheCreationInputTokensFromDetails = false
	}
	if src.CacheCreation != nil {
		if src.CacheCreation.Ephemeral5mInputTokens != nil {
			dst.CacheCreation5mTokens = max(*src.CacheCreation.Ephemeral5mInputTokens, 0)
		}
		if src.CacheCreation.Ephemeral1hInputTokens != nil {
			dst.CacheCreation1hTokens = max(*src.CacheCreation.Ephemeral1hInputTokens, 0)
		}
		if (!dst.cacheCreationInputTokensPresent || dst.cacheCreationInputTokensFromDetails || dst.CacheCreationInputTokens <= 0) && (src.CacheCreation.Ephemeral5mInputTokens != nil || src.CacheCreation.Ephemeral1hInputTokens != nil) {
			dst.CacheCreationInputTokens = dst.CacheCreation5mTokens + dst.CacheCreation1hTokens
			dst.cacheCreationInputTokensFromDetails = true
		}
	}
}

func mergeRawAnthropicCacheCreationUsage(node gjson.Result, dst *ClaudeUsage) {
	aggregate := node.Get("cache_creation_input_tokens")
	usage := apicompat.AnthropicUsage{
		CacheCreationInputTokens:        int(aggregate.Int()),
		CacheCreationInputTokensPresent: aggregate.Exists() && aggregate.Type != gjson.Null,
		CacheCreation:                   &apicompat.AnthropicCacheCreation{},
	}
	if tokens := node.Get("cache_creation.ephemeral_5m_input_tokens"); tokens.Exists() && tokens.Type != gjson.Null {
		value := int(tokens.Int())
		usage.CacheCreation.Ephemeral5mInputTokens = &value
	}
	if tokens := node.Get("cache_creation.ephemeral_1h_input_tokens"); tokens.Exists() && tokens.Type != gjson.Null {
		value := int(tokens.Int())
		usage.CacheCreation.Ephemeral1hInputTokens = &value
	}
	mergeAnthropicCacheCreationUsage(dst, usage)
}

func syncAnthropicResponsesUsage(state *apicompat.AnthropicEventToResponsesState, usage ClaudeUsage) {
	state.InputTokens = usage.InputTokens
	state.OutputTokens = usage.OutputTokens
	state.CacheReadInputTokens = usage.CacheReadInputTokens
	state.CacheCreationInputTokens = usage.CacheCreationInputTokens
}

func normalizeAnthropicEventUsageForResponses(event *apicompat.AnthropicStreamEvent, usage ClaudeUsage) {
	normalize := func(dst *apicompat.AnthropicUsage) {
		if dst == nil {
			return
		}
		dst.InputTokens = usage.InputTokens
		dst.OutputTokens = usage.OutputTokens
		dst.CacheReadInputTokens = usage.CacheReadInputTokens
		dst.CacheCreationInputTokens = usage.CacheCreationInputTokens
		dst.CacheCreationInputTokensPresent = true
		dst.CacheCreation = nil
	}
	normalize(event.Usage)
	if event.Message != nil {
		normalize(&event.Message.Usage)
	}
}
