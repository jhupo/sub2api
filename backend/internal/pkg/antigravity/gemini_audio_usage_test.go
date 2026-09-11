package antigravity

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGeminiAudioUsageSurvivesProtocolConversion(t *testing.T) {
	response := `{"response":{"candidates":[{"content":{"parts":[{"text":"ok"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1000,"cachedContentTokenCount":200,"candidatesTokenCount":100,"thoughtsTokenCount":50,"promptTokensDetails":[{"modality":"AUDIO","tokenCount":600}],"cacheTokensDetails":[{"modality":"AUDIO","tokenCount":100}]}}}`
	_, direct, err := TransformGeminiToClaude([]byte(response), "gemini-2.5-flash")
	require.NoError(t, err)
	processor := NewStreamingProcessor("gemini-2.5-flash")
	processor.ProcessLine("data: " + response)
	_, streamed := processor.Finish()
	for _, usage := range []*ClaudeUsage{direct, streamed} {
		require.Equal(t, 800, usage.InputTokens)
		require.Equal(t, 200, usage.CacheReadInputTokens)
		require.Equal(t, 500, usage.AudioInputTokens)
		require.Equal(t, 100, usage.AudioCacheReadTokens)
		require.Equal(t, 150, usage.OutputTokens)
	}
}

func TestGeminiAudioUsageBounds(t *testing.T) {
	usage := &GeminiUsageMetadata{PromptTokenCount: 100, CachedContentTokenCount: 20,
		PromptTokensDetails: []GeminiTokenDetail{{Modality: "AUDIO", TokenCount: 999}},
		CacheTokensDetails:  []GeminiTokenDetail{{Modality: "AUDIO", TokenCount: 999}},
	}
	input, cached := usage.AudioTokenUsage()
	require.Equal(t, 80, input)
	require.Equal(t, 20, cached)
}
