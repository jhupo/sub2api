//go:build unit

package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSetAnthropicAPIKeyAuthHeaderDetectsOllamaCloud(t *testing.T) {
	account := &Account{Type: AccountTypeAPIKey, Platform: PlatformOpenAI, Credentials: map[string]any{
		"base_url": "https://ollama.com/v1/",
	}}
	header := make(http.Header)
	setAnthropicAPIKeyAuthHeader(header, account, "token", account.GetCredential("base_url"))
	require.Equal(t, "Bearer token", header.Get("Authorization"))
	require.Empty(t, header.Get("x-api-key"))
}

func TestOllamaAnthropicAuthUsesSelectedProtocol(t *testing.T) {
	for _, base := range []string{"https://ollama.com/", "https://api.deepseek.com/anthropic"} {
		t.Run(base, func(t *testing.T) {
			account := messagesClampProductionAccount(1)
			baseURLs, ok := account.Credentials["api_base_urls"].(map[string]any)
			require.True(t, ok)
			baseURLs[APIProtocolAnthropic] = base
			svc := &OpenAIGatewayService{cfg: messagesClampTestConfig()}
			target, err := svc.nativeAnthropicTargetURL(account)
			require.NoError(t, err)
			req, _, err := svc.buildNativeAnthropicUpstreamRequest(context.Background(), newMessagesClampTestContext(t), account,
				messagesClampBody("deepseek-v4-flash", 256000), "key", target)
			require.NoError(t, err)
			if base == "https://ollama.com/" {
				require.Equal(t, "Bearer key", req.Header.Get("Authorization"))
				require.Empty(t, req.Header.Get("x-api-key"))
			} else {
				require.Equal(t, "key", req.Header.Get("x-api-key"))
				require.Empty(t, req.Header.Get("Authorization"))
			}
		})
	}
}

func TestOllamaAnthropicAuthIncludesTokenCountAndModelList(t *testing.T) {
	account := messagesClampOllamaAccount(1, PlatformAnthropic)
	account.Credentials["base_url"] = "https://ollama.com/"
	svc := &GatewayService{cfg: messagesClampTestConfig()}
	c := newMessagesClampTestContext(t)
	body := messagesClampBody("deepseek-v4-flash", 1000)
	ctx := context.Background()
	passthrough, err := svc.buildCountTokensRequestAnthropicAPIKeyPassthrough(ctx, c, account, body, "sk-test")
	require.NoError(t, err)
	normal, _, err := svc.buildCountTokensRequest(ctx, c, account, body, "sk-test", "api_key", "deepseek-v4-flash", false)
	require.NoError(t, err)
	models, err := (&AccountTestService{cfg: messagesClampTestConfig()}).buildAnthropicUpstreamModelsRequest(ctx, account)
	require.NoError(t, err)
	for _, req := range []*http.Request{passthrough, normal, models} {
		require.Equal(t, "ollama.com", req.URL.Host)
		require.Equal(t, "Bearer sk-test", req.Header.Get("Authorization"))
		require.Empty(t, req.Header.Get("x-api-key"))
	}
}

func TestSetAnthropicAPIKeyAuthHeaderKeepsStandardAPIKeyAuth(t *testing.T) {
	account := &Account{Type: AccountTypeAPIKey, Platform: PlatformAnthropic, Credentials: map[string]any{
		"base_url": "https://api.anthropic.com",
	}}
	header := make(http.Header)
	setAnthropicAPIKeyAuthHeader(header, account, "token", account.GetCredential("base_url"))
	require.Equal(t, "token", header.Get("x-api-key"))
	require.Empty(t, header.Get("Authorization"))
}
