//go:build unit

package service

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAccountTestService_TextPromptReachesUpstream(t *testing.T) {
	tests := []struct {
		name       string
		platform   string
		kind       string
		model      string
		protocol   string
		responses  bool
		promptPath string
		response   func() *http.Response
	}{
		{"claude oauth", PlatformAnthropic, AccountTypeOAuth, "claude-sonnet-4-6", "", false, "messages.0.content.0.text", adaptiveCNAnthropicTestResponse},
		{"claude api key", PlatformAnthropic, AccountTypeAPIKey, "claude-sonnet-4-6", "", false, "messages.0.content.0.text", adaptiveCNAnthropicTestResponse},
		{"bedrock", PlatformAnthropic, AccountTypeBedrock, "claude-sonnet-4-5", "", false, "messages.0.content.0.text", func() *http.Response {
			return newJSONResponse(http.StatusOK, `{"content":[{"text":"ok"}]}`)
		}},
		{"openai oauth", PlatformOpenAI, AccountTypeOAuth, "gpt-5.4", "", true, "input.0.content.0.text", adaptiveCNResponsesTestResponse},
		{"openai responses", PlatformOpenAI, AccountTypeAPIKey, "gpt-5.4", "", true, "input.0.content.0.text", adaptiveCNResponsesTestResponse},
		{"openai chat", PlatformOpenAI, AccountTypeAPIKey, "gpt-5.4", "", false, "messages.0.content", adaptiveCNChatTestResponse},
		{"cn anthropic", PlatformDeepseek, AccountTypeAPIKey, "deepseek-chat", APIProtocolAnthropic, false, "messages.0.content.0.text", adaptiveCNAnthropicTestResponse},
		{"gemini", PlatformGemini, AccountTypeAPIKey, "gemini-2.5-flash", "", false, "contents.0.parts.0.text", func() *http.Response {
			return newJSONResponse(http.StatusOK, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}]}\n\n")
		}},
		{"grok", PlatformGrok, AccountTypeAPIKey, "grok", "", false, "input", adaptiveCNResponsesTestResponse},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, prompt := range []string{"  Only reply TEST-21\nsecond line  ", "", " \n\t"} {
				account := &Account{
					ID: 42, Platform: tt.platform, Type: tt.kind, Status: StatusActive, Concurrency: 1,
					Credentials: map[string]any{
						"access_token": "test-token", "api_key": "test-key", "base_url": "https://upstream.example",
						"api_protocol": tt.protocol, "aws_region": "us-east-1", "auth_mode": "apikey",
					},
					Extra: map[string]any{openai_compat.ExtraKeyResponsesSupported: tt.responses},
				}
				svc, upstream := adaptiveCNAccountTestService(account, tt.response())
				c, _ := newTestContext()
				require.NoError(t, svc.TestAccountConnection(c, account.ID, tt.model, prompt, AccountTestModeDefault))
				require.Len(t, upstream.bodies, 1)
				expected := "Only reply TEST-21\nsecond line"
				if prompt != "  Only reply TEST-21\nsecond line  " {
					expected = "hi"
				}
				require.Equal(t, expected, gjson.GetBytes(upstream.bodies[0], tt.promptPath).String())
			}
		})
	}
}

func TestAccountTestService_AdaptivePreservesPromptAcrossEndpoints(t *testing.T) {
	account := adaptiveCNAccountTestAccount(42, PlatformDeepseek)
	svc, upstream := adaptiveCNAccountTestService(account, adaptiveCNChatTestResponse(), adaptiveCNAnthropicTestResponse(), adaptiveCNResponsesTestResponse())
	c, _ := newTestContext()
	require.NoError(t, svc.TestAccountConnection(c, account.ID, "deepseek-chat", "  Only reply TEST-21  ", AccountTestModeDefault))
	require.Len(t, upstream.bodies, 3)
	for i, path := range []string{"messages.0.content", "messages.0.content.0.text", "input.0.content.0.text"} {
		require.Equal(t, "Only reply TEST-21", gjson.GetBytes(upstream.bodies[i], path).String())
	}
}

func TestAccountTestService_VertexPayloadPreservesPrompt(t *testing.T) {
	payload, err := createTestPayload("claude-sonnet-4-6", "Only reply TEST-21")
	require.NoError(t, err)
	body, err := json.Marshal(payload)
	require.NoError(t, err)
	body, err = buildVertexAnthropicRequestBody(body)
	require.NoError(t, err)
	require.Equal(t, "Only reply TEST-21", gjson.GetBytes(body, "messages.0.content.0.text").String())
	require.NotEmpty(t, gjson.GetBytes(body, "system.0.text").String())
}
