package repository

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

type rpmRedisCommandHook struct {
	timeCalls atomic.Int64
}

func (*rpmRedisCommandHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (h *rpmRedisCommandHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() == "time" {
			h.timeCalls.Add(1)
		}
		return next(ctx, cmd)
	}
}

func (*rpmRedisCommandHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func TestRPMCacheCurrentMinuteSuffixIsSharedAcrossConcurrentCalls(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	hook := &rpmRedisCommandHook{}
	client.AddHook(hook)

	cache := &RPMCacheImpl{rdb: client}
	const callers = 32
	suffixes := make([]string, callers)
	errs := make([]error, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			suffixes[index], errs[index] = cache.currentMinuteSuffix(context.Background())
		}(i)
	}
	wg.Wait()

	for i, suffix := range suffixes {
		require.NoError(t, errs[i])
		require.NotEmpty(t, suffix)
		require.Equal(t, suffixes[0], suffix)
	}
	require.Equal(t, int64(1), hook.timeCalls.Load())
}

func TestRPMCacheIncrementUsesCachedMinuteKey(t *testing.T) {
	mini := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mini.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	hook := &rpmRedisCommandHook{}
	client.AddHook(hook)

	cache := &RPMCacheImpl{rdb: client}
	first, err := cache.IncrementRPM(context.Background(), 42)
	require.NoError(t, err)
	second, err := cache.IncrementRPM(context.Background(), 42)
	require.NoError(t, err)
	require.Equal(t, 1, first)
	require.Equal(t, 2, second)
	require.Equal(t, int64(1), hook.timeCalls.Load())
}
