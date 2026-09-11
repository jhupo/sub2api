package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyAuthCacheNamespaceDoesNotReuseOrDeleteUpstreamData(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	ctx := context.Background()
	cache := NewAPIKeyCache(client)
	require.NoError(t, client.Set(ctx, "apikey:auth:hash", `{"snapshot":{"version":23}}`, time.Hour).Err())
	require.NoError(t, client.Set(ctx, "unrelated:balance", "100", time.Hour).Err())
	_, err := cache.GetAuthCache(ctx, "hash")
	require.ErrorIs(t, err, redis.Nil)
	entry := &service.APIKeyAuthCacheEntry{Snapshot: &service.APIKeyAuthSnapshot{Version: 24, FundingSource: service.FundingSourceWallet}}
	require.NoError(t, cache.SetAuthCache(ctx, "hash", entry, time.Hour))
	got, err := cache.GetAuthCache(ctx, "hash")
	require.NoError(t, err)
	require.Equal(t, 24, got.Snapshot.Version)
	require.NoError(t, cache.DeleteAuthCache(ctx, "hash"))
	require.True(t, server.Exists("apikey:auth:hash"))
	require.True(t, server.Exists("unrelated:balance"))
	require.False(t, server.Exists("jhupo:apikey:auth:hash"))
}
