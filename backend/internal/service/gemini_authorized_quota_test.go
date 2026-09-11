package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/stretchr/testify/require"
)

func TestGeminiAntigravityDoesNotUseEstimatedRequestLimits(t *testing.T) {
	service := NewGeminiQuotaService(nil, nil)
	account := &Account{Platform: PlatformGemini, Type: AccountTypeOAuth, Credentials: map[string]any{
		"oauth_type": "antigravity", "tier_id": "google_ai_pro",
	}}
	_, ok := service.QuotaForAccount(context.Background(), account)
	require.False(t, ok)
}

func TestGeminiExplicitModelVariantCannotDowngrade(t *testing.T) {
	models := map[string]antigravity.ModelInfo{
		"gemini-3.1-pro-low": {}, "claude-opus-4-6": {},
	}
	for _, model := range []string{"gemini-3.1-pro-high", "claude-opus-4-6-thinking"} {
		_, ok := resolveRuntimeModel(models, model)
		require.False(t, ok, model)
	}
}

func TestAntigravityUnknownQuotaDoesNotMeanExhausted(t *testing.T) {
	var response antigravity.FetchAvailableModelsResponse
	require.NoError(t, json.Unmarshal([]byte(`{"models":{"unknown":{"quotaInfo":{}},"empty":{"quotaInfo":{"remainingFraction":0}},"full":{"quotaInfo":{"remainingFraction":1}}}}`), &response))
	info := (&AntigravityQuotaFetcher{}).buildUsageInfo(&response, "", "", nil)
	require.NotContains(t, info.AntigravityQuota, "unknown")
	require.Equal(t, 100, info.AntigravityQuota["empty"].Utilization)
	require.Equal(t, 0, info.AntigravityQuota["full"].Utilization)
	require.Nil(t, info.FiveHour)
}

func TestGeminiQuotaSummaryUsesTokenProviderAndPreservesWindows(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "Bearer fresh", r.Header.Get("Authorization"))
		if r.URL.Path == "/v1internal:loadCodeAssist" {
			_, _ = w.Write([]byte(`{"paidTier":{"id":"g1-pro-tier"}}`))
			return
		}
		require.Equal(t, "/v1internal:retrieveUserQuotaSummary", r.URL.Path)
		_, _ = w.Write([]byte(`{"groups":[{"displayName":"Gemini","buckets":[{"bucketId":"short","window":"five_hours","remainingFraction":0.8},{"bucketId":"week","window":"weekly","remainingFraction":0}]}]}`))
	}))
	defer server.Close()
	oldURLs, oldAvailability := antigravity.BaseURLs, antigravity.DefaultURLAvailability
	antigravity.BaseURLs = []string{server.URL}
	antigravity.DefaultURLAvailability = antigravity.NewURLAvailability(time.Minute)
	t.Cleanup(func() { antigravity.BaseURLs, antigravity.DefaultURLAvailability = oldURLs, oldAvailability })
	provider := NewGeminiTokenProvider(nil, &cachedGeminiTokenStub{token: "fresh"}, nil)
	fetcher := NewAntigravityQuotaFetcher(nil, nil, provider, nil)
	account := &Account{ID: 17, Platform: PlatformGemini, Type: AccountTypeOAuth, Credentials: map[string]any{
		"oauth_type": GeminiOAuthTypeAntigravity, "project_id": "project", "access_token": "expired",
	}}
	result, err := fetcher.FetchQuota(context.Background(), account, "")
	require.NoError(t, err)
	require.Len(t, result.UsageInfo.AntigravityQuotaGroups, 1)
	require.Len(t, result.UsageInfo.AntigravityQuotaGroups[0].Buckets, 2)
	require.Equal(t, "PRO", result.UsageInfo.SubscriptionTier)
	require.Nil(t, result.UsageInfo.FiveHour)
	tiers := usageQuotaTiers(result.UsageInfo)
	require.Len(t, tiers, 2)
	require.Equal(t, "weekly", tiers[1].Window)
	require.Equal(t, 100.0, tiers[1].UsedPercent)
}
