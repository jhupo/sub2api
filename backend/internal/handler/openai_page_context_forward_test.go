package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai_compat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const codexForwardTerminalText = "PowerShell current directory: C:\\workspace"
const codexForwardBrowserText = `<in-app-browser-context source="ambient-ui-state">
Example tab: https://example.com/
</in-app-browser-context>`

func normalizedCodexContextForwardBody(t *testing.T, history bool) []byte {
	t.Helper()
	var input []any
	if history {
		input = append(input, map[string]any{
			"type": "function_call", "name": "read_file", "call_id": "call_real", "arguments": "{}",
		})
	}
	input = append(input,
		map[string]any{
			"type": "function_call_output", "namespace": "agent", "name": "terminal_info",
			"id": "fco_terminal_context", "output": codexForwardTerminalText,
		},
		map[string]any{
			"type": "function_call_output", "namespace": "agent", "name": "browser_tabs",
			"id": "fco_browser_context", "output": codexForwardBrowserText,
		},
	)
	if history {
		input = append(input, map[string]any{
			"type": "function_call_output", "call_id": "call_real", "output": "file contents",
		})
	}
	input = append(input, map[string]any{"role": "user", "content": "continue"})
	body, err := json.Marshal(map[string]any{
		"model": "gpt-5.1", "stream": false, "instructions": "test",
		"tools": []any{map[string]any{"type": "function", "name": "read_file", "parameters": map[string]any{"type": "object"}}},
		"input": input,
	})
	require.NoError(t, err)
	normalized, changed := normalizeCodexUntrustedContext(body)
	require.True(t, changed)
	return normalized
}

type codexContextForwardUpstream struct {
	bodies [][]byte
}

func (u *codexContextForwardUpstream) Do(req *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	u.bodies = append(u.bodies, body)
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"id":"resp_context","output":[],"usage":{"input_tokens":1,"output_tokens":1,"input_tokens_details":{"cached_tokens":0}}}`,
		)),
	}, nil
}

func (u *codexContextForwardUpstream) DoWithTLS(req *http.Request, proxy string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, accountID, concurrency)
}

func TestNormalizeCodexContextSurvivesResponsesForward(t *testing.T) {
	for _, tc := range []struct {
		name    string
		oauth   bool
		compact bool
	}{
		{name: "api key"},
		{name: "oauth", oauth: true},
		{name: "oauth compact", oauth: true, compact: true},
	} {
		for _, history := range []bool{false, true} {
			name := tc.name + "/standalone"
			if history {
				name = tc.name + "/with tool history"
			}
			t.Run(name, func(t *testing.T) {
				body := normalizedCodexContextForwardBody(t, history)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				path := "/v1/responses"
				if tc.compact {
					path += "/compact"
				}
				c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
				c.Request.Header.Set("Content-Type", "application/json")
				c.Request.Header.Set("User-Agent", "curl/8.0")
				service.SetOpenAIClientTransport(c, service.OpenAIClientTransportHTTP)
				account := &service.Account{
					ID: 5108, Name: "context-test", Platform: service.PlatformOpenAI,
					Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 1,
					Credentials: map[string]any{"api_key": "sk-test", "base_url": "https://compat.example"},
					Extra:       map[string]any{openai_compat.ExtraKeyResponsesSupported: true},
				}
				if tc.oauth {
					account.Type = service.AccountTypeOAuth
					account.Credentials = map[string]any{"access_token": "oauth-token", "chatgpt_account_id": "chatgpt-account"}
				}
				upstream := &codexContextForwardUpstream{}
				cfg := &config.Config{Security: config.SecurityConfig{URLAllowlist: config.URLAllowlistConfig{Enabled: false}}}
				gateway := service.NewOpenAIGatewayService(
					nil, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil,
					upstream, nil, nil, nil, nil, nil, nil, nil, nil,
				)
				result, err := gateway.Forward(context.Background(), c, account, body)
				require.NoError(t, err)
				require.NotNil(t, result)
				require.Len(t, upstream.bodies, 1)
				forwarded := upstream.bodies[0]
				require.NotContains(t, string(forwarded), "fco_terminal_context")
				require.NotContains(t, string(forwarded), "fco_browser_context")
				items := gjson.GetBytes(forwarded, "input").Array()
				contextIndex := 0
				if history {
					require.Len(t, items, 5)
					contextIndex = 1
					expectedCallID := "call_real"
					if tc.oauth {
						expectedCallID = "fc_real"
					}
					require.Equal(t, "function_call", items[0].Get("type").String())
					require.Equal(t, expectedCallID, items[0].Get("call_id").String())
					require.Equal(t, "function_call_output", items[3].Get("type").String())
					require.Equal(t, items[0].Get("call_id").String(), items[3].Get("call_id").String())
					require.Equal(t, "file contents", items[3].Get("output").String())
				} else {
					require.Len(t, items, 3)
				}
				for i, expected := range []string{
					"<external_terminal_info>" + codexForwardTerminalText + "</external_terminal_info>",
					"<external_browser_tabs>" + codexForwardBrowserText + "</external_browser_tabs>",
				} {
					item := items[contextIndex+i]
					require.Equal(t, "message", item.Get("type").String())
					require.Equal(t, "user", item.Get("role").String())
					require.Equal(t, expected, item.Get("content.0.text").String())
					for _, field := range []string{"id", "call_id", "name", "namespace"} {
						require.False(t, item.Get(field).Exists(), "context must not retain %s", field)
					}
				}
			})
		}
	}
}

func TestNormalizeCodexContextSurvivesProtocolBridges(t *testing.T) {
	body := normalizedCodexContextForwardBody(t, true)
	var req apicompat.ResponsesRequest
	require.NoError(t, json.Unmarshal(body, &req))
	t.Run("chat completions", func(t *testing.T) {
		converted, err := apicompat.ResponsesToChatCompletionsRequest(&req)
		require.NoError(t, err)
		encoded, err := json.Marshal(converted)
		require.NoError(t, err)
		calls, results := 0, 0
		var userTexts []string
		for _, message := range gjson.GetBytes(encoded, "messages").Array() {
			for _, call := range message.Get("tool_calls").Array() {
				require.Equal(t, "call_real", call.Get("id").String())
				calls++
			}
			if message.Get("role").String() == "tool" {
				require.Equal(t, "call_real", message.Get("tool_call_id").String())
				results++
			}
			if message.Get("role").String() == "user" {
				userTexts = append(userTexts, codexContextMessageText(message.Get("content")))
			}
		}
		require.Equal(t, 1, calls)
		require.Equal(t, 1, results)
		assertCodexForwardContextText(t, strings.Join(userTexts, "\n"))
	})
	t.Run("anthropic", func(t *testing.T) {
		converted, err := apicompat.ResponsesToAnthropicRequest(&req)
		require.NoError(t, err)
		encoded, err := json.Marshal(converted)
		require.NoError(t, err)
		var callIDs, resultIDs []string
		var userTexts []string
		for _, message := range gjson.GetBytes(encoded, "messages").Array() {
			for _, block := range message.Get("content").Array() {
				switch block.Get("type").String() {
				case "tool_use":
					callIDs = append(callIDs, block.Get("id").String())
				case "tool_result":
					resultIDs = append(resultIDs, block.Get("tool_use_id").String())
				}
			}
			if message.Get("role").String() == "user" {
				userTexts = append(userTexts, codexContextMessageText(message.Get("content")))
			}
		}
		require.Len(t, callIDs, 1)
		require.NotEmpty(t, callIDs[0])
		require.Equal(t, callIDs, resultIDs)
		assertCodexForwardContextText(t, strings.Join(userTexts, "\n"))
	})
}

func codexContextMessageText(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}
	var texts []string
	for _, part := range content.Array() {
		if part.Get("type").String() == "text" {
			texts = append(texts, part.Get("text").String())
		}
	}
	return strings.Join(texts, "\n")
}

func assertCodexForwardContextText(t *testing.T, text string) {
	t.Helper()
	require.Contains(t, text, "<external_terminal_info>"+codexForwardTerminalText+"</external_terminal_info>")
	require.Contains(t, text, "<external_browser_tabs>"+codexForwardBrowserText+"</external_browser_tabs>")
}
