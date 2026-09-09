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

func TestCodexAdaptivePressureTracksIndependentSessionsAndRecovery(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	cache := NewConcurrencyCache(client, 15, 900)
	pressure, ok := cache.(service.CodexAdaptivePressureCache)
	require.True(t, ok)
	ctx := context.Background()
	const (
		accountID = int64(42)
		model     = "gpt-5.6-sol"
	)
	window := 90 * time.Second

	count, err := pressure.ObserveCodexAdaptiveFailure(ctx, accountID, model, "session-a", window)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	count, err = pressure.ObserveCodexAdaptiveFailure(ctx, accountID, model, "session-a", window)
	require.NoError(t, err)
	require.Equal(t, 1, count, "the same session must not amplify pressure")
	count, err = pressure.ObserveCodexAdaptiveFailure(ctx, accountID, model, "session-b", window)
	require.NoError(t, err)
	require.Equal(t, 2, count)

	batch, err := pressure.GetCodexAdaptivePressureBatch(ctx, []int64{accountID, 99}, model, window)
	require.NoError(t, err)
	require.Equal(t, 2, batch[accountID])
	require.Zero(t, batch[99])

	count, err = pressure.ObserveCodexAdaptiveSuccess(ctx, accountID, model, "session-a", window)
	require.NoError(t, err)
	require.Equal(t, 1, count)
	count, err = pressure.ObserveCodexAdaptiveSuccess(ctx, accountID, model, "session-b", window)
	require.NoError(t, err)
	require.Zero(t, count)
}

func TestCodexAdaptivePressureExpiresOutsideWindow(t *testing.T) {
	redisServer := miniredis.RunT(t)
	now := time.Unix(1_800_000_000, 0)
	redisServer.SetTime(now)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	cache := NewConcurrencyCache(client, 15, 900)
	pressure, ok := cache.(service.CodexAdaptivePressureCache)
	require.True(t, ok)
	ctx := context.Background()

	_, err := pressure.ObserveCodexAdaptiveFailure(ctx, 7, "gpt-5.6", "session", 90*time.Second)
	require.NoError(t, err)
	redisServer.SetTime(now.Add(91 * time.Second))

	batch, err := pressure.GetCodexAdaptivePressureBatch(ctx, []int64{7}, "gpt-5.6", 90*time.Second)
	require.NoError(t, err)
	require.Zero(t, batch[7])
}

func TestCodexAdaptivePressureIsolatedAcrossAccountsAndModels(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	pressure, ok := NewConcurrencyCache(client, 15, 900).(service.CodexAdaptivePressureCache)
	require.True(t, ok)
	ctx := context.Background()
	window := 90 * time.Second

	_, err := pressure.ObserveCodexAdaptiveFailure(ctx, 1, "gpt-5.6-sol", "session-a", window)
	require.NoError(t, err)
	_, err = pressure.ObserveCodexAdaptiveFailure(ctx, 1, "gpt-5.6-sol", "session-b", window)
	require.NoError(t, err)
	_, err = pressure.ObserveCodexAdaptiveFailure(ctx, 1, "gpt-5.6-terra", "session-a", window)
	require.NoError(t, err)
	_, err = pressure.ObserveCodexAdaptiveFailure(ctx, 2, "gpt-5.6-sol", "session-a", window)
	require.NoError(t, err)

	sol, err := pressure.GetCodexAdaptivePressureBatch(ctx, []int64{1, 2}, "gpt-5.6-sol", window)
	require.NoError(t, err)
	require.Equal(t, 2, sol[1])
	require.Equal(t, 1, sol[2])
	terra, err := pressure.GetCodexAdaptivePressureBatch(ctx, []int64{1, 2}, "gpt-5.6-terra", window)
	require.NoError(t, err)
	require.Equal(t, 1, terra[1])
	require.Zero(t, terra[2])
}
