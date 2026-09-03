package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestApplyOpenAIWSRetryPayloadStrategy_KeepPromptCacheKey(t *testing.T) {
	payload := []byte(`{"model":"gpt-5.3-codex","prompt_cache_key":"pcache_123","include":["reasoning.encrypted_content"],"text":{"verbosity":"low"},"tools":[{"type":"function"}]}`)

	payload, strategy, removed, err := applyOpenAIWSRetryPayloadStrategyRaw(payload, 3)
	require.NoError(t, err)
	require.Equal(t, "trim_optional_fields", strategy)
	require.Contains(t, removed, "include")
	require.NotContains(t, removed, "prompt_cache_key")
	require.Equal(t, "pcache_123", gjson.GetBytes(payload, "prompt_cache_key").String())
	require.False(t, gjson.GetBytes(payload, "include").Exists())
	require.True(t, gjson.GetBytes(payload, "text").Exists())
}

func TestApplyOpenAIWSRetryPayloadStrategy_AttemptSixKeepsSemanticFields(t *testing.T) {
	payload := []byte(`{"prompt_cache_key":"pcache_456","instructions":"long instructions","tools":[{"type":"function"}],"parallel_tool_calls":true,"tool_choice":"auto","include":["reasoning.encrypted_content"],"text":{"verbosity":"high"}}`)

	payload, strategy, removed, err := applyOpenAIWSRetryPayloadStrategyRaw(payload, 6)
	require.NoError(t, err)
	require.Equal(t, "trim_optional_fields", strategy)
	require.Contains(t, removed, "include")
	require.NotContains(t, removed, "prompt_cache_key")
	require.Equal(t, "pcache_456", gjson.GetBytes(payload, "prompt_cache_key").String())
	for _, key := range []string{"instructions", "tools", "tool_choice", "parallel_tool_calls", "text"} {
		require.True(t, gjson.GetBytes(payload, key).Exists(), key)
	}
}
