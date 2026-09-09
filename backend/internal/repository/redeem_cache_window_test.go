package repository

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestRedeemAttemptWindowDoesNotSlide(t *testing.T) {
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := NewRedeemCache(rdb)
	ctx := context.Background()
	key := redeemRateLimitKey(42)
	require.NoError(t, cache.IncrementRedeemAttemptCount(ctx, 42, 10*time.Minute))
	server.FastForward(9 * time.Minute)
	require.NoError(t, cache.IncrementRedeemAttemptCount(ctx, 42, 10*time.Minute))
	require.Equal(t, time.Minute, server.TTL(key))
	server.FastForward(time.Minute)
	count, err := cache.GetRedeemAttemptCount(ctx, 42)
	require.NoError(t, err)
	require.Zero(t, count)
	require.NoError(t, server.Set(key, "12"))
	require.NoError(t, cache.IncrementRedeemAttemptCount(ctx, 42, 10*time.Minute))
	require.Equal(t, 10*time.Minute, server.TTL(key), "repair a counter without an expiry")
}

func TestRedeemAttemptWindowConcurrentUsers(t *testing.T) {
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = rdb.Close() })
	cache := NewRedeemCache(rdb)
	ctx := context.Background()
	var wg sync.WaitGroup
	errCh := make(chan error, 100)
	for i := 0; i < 100; i++ {
		wg.Go(func() { errCh <- cache.IncrementRedeemAttemptCount(ctx, 42, 10*time.Minute) })
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		require.NoError(t, err)
	}
	count, err := cache.GetRedeemAttemptCount(ctx, 42)
	require.NoError(t, err)
	require.Equal(t, 100, count)
	other, err := cache.GetRedeemAttemptCount(ctx, 43)
	require.NoError(t, err)
	require.Zero(t, other)
	require.Equal(t, 10*time.Minute, server.TTL(redeemRateLimitKey(42)))
}
