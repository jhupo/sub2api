package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

func TestGeminiUsageWindowsKeepBillingPerspectives(t *testing.T) {
	now := time.Now()
	stats := []usagestats.ModelStat{
		{Model: "gemini-3-pro", Requests: 4, TotalTokens: 100, Cost: 10, AccountCost: 5, ActualCost: 20},
		{Model: "gemini-3-flash", Requests: 3, TotalTokens: 50, Cost: 2, AccountCost: 1, ActualCost: 4},
		{Model: "gemini-2.5-flash-lite", Requests: 1, TotalTokens: 10, Cost: 1, AccountCost: .5, ActualCost: 2},
	}
	shared, pro, flash := buildGeminiUsageWindows(stats, 100, 0, 0, now.Add(time.Hour), now)
	require.Nil(t, pro)
	require.Nil(t, flash)
	require.Equal(t, int64(8), shared.UsedRequests)
	require.Equal(t, int64(100), shared.LimitRequests)
	require.Equal(t, 8.0, shared.Utilization)
	require.Equal(t, &WindowStats{Requests: 8, Tokens: 160, Cost: 6.5, StandardCost: 13, UserCost: 26}, shared.WindowStats)
	shared, pro, flash = buildGeminiUsageWindows(stats, 0, 2, 50, now.Add(time.Minute), now)
	require.Nil(t, shared)
	require.Equal(t, 200.0, pro.Utilization)
	require.Equal(t, &WindowStats{Requests: 4, Tokens: 100, Cost: 5, StandardCost: 10, UserCost: 20}, pro.WindowStats)
	require.Equal(t, &WindowStats{Requests: 4, Tokens: 60, Cost: 1.5, StandardCost: 3, UserCost: 6}, flash.WindowStats)
	shared, pro, flash = buildGeminiUsageWindows(nil, 0, 0, 0, now, now)
	require.Nil(t, shared)
	require.Nil(t, pro)
	require.Nil(t, flash)
}

func TestGeminiQuotaFetchRequiresMatchingOAuthIdentity(t *testing.T) {
	fetcher := &AntigravityQuotaFetcher{}
	for _, oauthType := range []string{"", "google_one", GeminiOAuthTypeCodeAssist, GeminiOAuthTypeAIStudio, GeminiOAuthTypeAntigravity} {
		account := &Account{Platform: PlatformGemini, Type: AccountTypeOAuth, Credentials: map[string]any{
			"oauth_type": oauthType, "access_token": "test-token",
		}}
		require.Equal(t, oauthType == GeminiOAuthTypeAntigravity, fetcher.CanFetch(account), oauthType)
		if oauthType == "" || oauthType == "google_one" {
			usage, err := (&AccountUsageService{}).getGeminiUsage(context.Background(), account)
			require.NoError(t, err)
			require.True(t, usage.NeedsReauth)
			require.Nil(t, usage.GeminiSharedDaily)
		}
	}
	require.False(t, fetcher.CanFetch(nil))
}

func TestGeminiUsageKeepsAccountQuotaCachesSeparate(t *testing.T) {
	cache := NewUsageCache()
	svc := &AccountUsageService{cache: cache, antigravityQuotaFetcher: &AntigravityQuotaFetcher{}}
	for _, id := range []int64{11, 12} {
		cached := &UsageInfo{AntigravityQuota: map[string]*AntigravityModelQuota{
			"gemini-3-pro": {Utilization: int(id)},
		}}
		cache.antigravityCache.Store(id, &antigravityUsageCache{usageInfo: cached, timestamp: time.Now()})
		usage, err := svc.getGeminiUsage(context.Background(), &Account{
			ID: id, Platform: PlatformGemini, Type: AccountTypeOAuth,
			Credentials: map[string]any{"oauth_type": GeminiOAuthTypeAntigravity, "access_token": "test-token"},
		})
		require.NoError(t, err)
		require.Equal(t, int(id), usage.AntigravityQuota["gemini-3-pro"].Utilization)
		require.NotSame(t, cached, usage)
		usage.GeminiSharedDaily = &UsageProgress{UsedRequests: 3}
		require.Nil(t, cached.GeminiSharedDaily)
	}
}
