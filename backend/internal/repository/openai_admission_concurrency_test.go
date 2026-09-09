package repository

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newOpenAIAdmissionTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("SUB2API_TEST_REDIS_ADDR")
	if addr == "" {
		addr = miniredis.RunT(t).Addr()
	} else {
		require.True(t, strings.HasPrefix(addr, "127.0.0.1:"), "only an isolated local Redis is allowed")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	t.Cleanup(func() { _ = client.Close() })
	require.NoError(t, client.Ping(context.Background()).Err())
	return client
}

func TestOpenAIAdaptiveAdmissionConcurrentUsersAndAccounts(t *testing.T) {
	client := newOpenAIAdmissionTestRedis(t)
	cache, ok := NewConcurrencyCache(client, 15, 900).(*concurrencyCache)
	require.True(t, ok)
	ctx := context.Background()
	base := time.Now().UnixMicro()
	model := "admission-test"
	policy := service.AccountSlotAdmission{MaxConcurrency: 20, PressureModel: model, PressureWindow: 90 * time.Second}
	accounts := []int64{base, base + 1, base + 2}
	for i, pressure := range []int{0, 3, 25} {
		for session := 0; session < pressure; session++ {
			_, err := cache.ObserveCodexAdaptiveFailure(ctx, accounts[i], model, fmt.Sprint(session), policy.PressureWindow)
			require.NoError(t, err)
		}
	}
	var admitted [3]atomic.Int64
	var wg sync.WaitGroup
	start := make(chan struct{})
	errors := make(chan error, 300)
	for user := 0; user < 100; user++ {
		for i, accountID := range accounts {
			wg.Add(1)
			go func(user, i int, accountID int64) {
				defer wg.Done()
				<-start
				userID := base + int64(user)
				requestID := fmt.Sprintf("%d:%d", user, i)
				userOK, err := cache.AcquireUserSlot(ctx, userID, 3, requestID)
				if err != nil {
					errors <- err
					return
				}
				if !userOK {
					errors <- fmt.Errorf("unexpected user queue")
					return
				}
				ok, err := cache.AcquireAdaptiveAccountSlot(ctx, accountID, policy, requestID)
				if err != nil {
					errors <- err
				}
				if ok {
					admitted[i].Add(1)
				}
				if err := cache.ReleaseUserSlot(ctx, userID, requestID); err != nil {
					errors <- err
				}
			}(user, i, accountID)
		}
	}
	close(start)
	wg.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	for i, expected := range []int64{20, 7, 1} {
		require.Equal(t, expected, admitted[i].Load())
		count, err := cache.GetAccountConcurrency(ctx, accounts[i])
		require.NoError(t, err)
		require.Equal(t, int(expected), count)
		for user := 0; user < 100; user++ {
			require.NoError(t, cache.ReleaseAccountSlot(ctx, accounts[i], fmt.Sprintf("%d:%d", user, i)))
		}
	}
}

func TestOpenAIAdaptiveAdmissionPressureChangesWhileQueued(t *testing.T) {
	cache, ok := NewConcurrencyCache(newOpenAIAdmissionTestRedis(t), 15, 900).(*concurrencyCache)
	require.True(t, ok)
	ctx := context.Background()
	id := time.Now().UnixMicro()
	policy := service.AccountSlotAdmission{MaxConcurrency: 20, PressureModel: "queue-test", PressureWindow: 90 * time.Second}
	for slot := 0; slot < 10; slot++ {
		ok, err := cache.AcquireAdaptiveAccountSlot(ctx, id, policy, fmt.Sprint(slot))
		require.NoError(t, err)
		require.True(t, ok)
	}
	for session := 0; session < 3; session++ {
		_, err := cache.ObserveCodexAdaptiveFailure(ctx, id, policy.PressureModel, fmt.Sprint(session), policy.PressureWindow)
		require.NoError(t, err)
	}
	// A queue's stale cap (20) must be reduced atomically to seven.
	ok, err := cache.AcquireAdaptiveAccountSlot(ctx, id, policy, "queued")
	require.NoError(t, err)
	require.False(t, ok)
	for slot := 0; slot < 4; slot++ {
		require.NoError(t, cache.ReleaseAccountSlot(ctx, id, fmt.Sprint(slot)))
	}
	ok, err = cache.AcquireAdaptiveAccountSlot(ctx, id, policy, "queued")
	require.NoError(t, err)
	require.True(t, ok)
	// Another model shares the same physical account slots.
	otherModel := policy
	otherModel.MaxConcurrency, otherModel.PressureModel = 7, "other-model"
	ok, err = cache.AcquireAdaptiveAccountSlot(ctx, id, otherModel, "other")
	require.NoError(t, err)
	require.False(t, ok)
	for slot := 4; slot < 10; slot++ {
		require.NoError(t, cache.ReleaseAccountSlot(ctx, id, fmt.Sprint(slot)))
	}
	require.NoError(t, cache.ReleaseAccountSlot(ctx, id, "queued"))
}
