package service

func (usage ClaudeUsage) billingUsageTokens() UsageTokens {
	creationTokens := usage.CacheCreationInputTokens
	if creationTokens <= 0 {
		creationTokens = max(usage.CacheCreation5mTokens, 0) + max(usage.CacheCreation1hTokens, 0)
	}
	return UsageTokens{
		InputTokens:           usage.InputTokens,
		OutputTokens:          usage.OutputTokens,
		CacheCreationTokens:   creationTokens,
		CacheCreation5mTokens: usage.CacheCreation5mTokens,
		CacheCreation1hTokens: usage.CacheCreation1hTokens,
		CacheReadTokens:       usage.CacheReadInputTokens,
		ImageOutputTokens:     usage.ImageOutputTokens,
		AudioInputTokens:      usage.AudioInputTokens,
		AudioCacheReadTokens:  usage.AudioCacheReadTokens,
	}
}

func (usage OpenAIUsage) billingUsageTokens() UsageTokens {
	creationTokens := usage.CacheCreationInputTokens
	if creationTokens <= 0 {
		creationTokens = max(usage.CacheCreation5mTokens, 0) + max(usage.CacheCreation1hTokens, 0)
	}
	return UsageTokens{
		InputTokens:           max(usage.InputTokens-usage.CacheReadInputTokens-creationTokens, 0),
		ImageInputTokens:      usage.ImageInputTokens,
		OutputTokens:          usage.OutputTokens,
		CacheCreationTokens:   creationTokens,
		CacheCreation5mTokens: usage.CacheCreation5mTokens,
		CacheCreation1hTokens: usage.CacheCreation1hTokens,
		CacheReadTokens:       usage.CacheReadInputTokens,
		ImageOutputTokens:     usage.ImageOutputTokens,
	}
}
