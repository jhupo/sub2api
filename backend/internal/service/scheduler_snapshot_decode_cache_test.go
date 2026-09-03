//go:build unit

package service

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type decodeCacheProbe struct {
	SchedulerCache
	snapshotCalls int
	setCalls      int
	mu            sync.Mutex
	hit           bool
	accounts      []*Account
	version       string
	getBarrier    chan struct{}
	getArrivals   int
}

func (c *decodeCacheProbe) GetSnapshot(context.Context, SchedulerBucket) ([]*Account, bool, error) {
	c.mu.Lock()
	c.snapshotCalls++
	barrier := c.getBarrier
	if barrier != nil {
		c.getArrivals++
		if c.getArrivals == 2 {
			close(barrier)
		}
	}
	accounts, hit := c.accounts, c.hit
	c.mu.Unlock()
	if barrier != nil {
		<-barrier
	}
	return accounts, hit, nil
}

func (c *decodeCacheProbe) GetSnapshotVersion(context.Context, SchedulerBucket) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.version, nil
}

func (c *decodeCacheProbe) CaptureBucketWriteToken(_ context.Context, bucket SchedulerBucket) (SchedulerBucketWriteToken, error) {
	return SchedulerBucketWriteToken{Bucket: bucket, Epoch: 1}, nil
}

func (c *decodeCacheProbe) SetSnapshot(_ context.Context, _ SchedulerBucket, _ SchedulerBucketWriteToken, accounts []Account) error {
	c.mu.Lock()
	c.setCalls++
	c.accounts = accountsToPointers(accounts)
	c.hit = true
	c.version = "v1"
	c.mu.Unlock()
	return nil
}

func TestSchedulerSnapshotDecodedCacheAvoidsRepeatedCacheReads(t *testing.T) {
	cache := &decodeCacheProbe{
		hit:     true,
		version: "v1",
		accounts: []*Account{{
			ID:       7,
			Platform: PlatformOpenAI,
			Status:   StatusActive,
		}},
	}
	svc := NewSchedulerSnapshotService(cache, nil, nil, nil, nil)

	first, _, err := svc.ListSchedulableAccounts(context.Background(), nil, PlatformOpenAI, false)
	require.NoError(t, err)
	second, _, err := svc.ListSchedulableAccounts(context.Background(), nil, PlatformOpenAI, false)
	require.NoError(t, err)
	require.Len(t, first, 1)
	require.Len(t, second, 1)
	require.Equal(t, first[0].ID, second[0].ID)
	cache.mu.Lock()
	calls := cache.snapshotCalls
	cache.mu.Unlock()
	require.Equal(t, 1, calls, "decoded cache should avoid a second SchedulerCache.GetSnapshot call")
}

func TestSchedulerSnapshotDecodedCacheDoesNotShareMutableAccountMaps(t *testing.T) {
	cache := &decodeCacheProbe{
		hit:     true,
		version: "v1",
		accounts: []*Account{{
			ID:          8,
			Platform:    PlatformOpenAI,
			Status:      StatusActive,
			Credentials: map[string]any{"nested": map[string]any{"token": "original"}},
			Extra:       map[string]any{"limits": map[string]any{"remaining": float64(3)}},
		}},
	}
	svc := NewSchedulerSnapshotService(cache, nil, nil, nil, nil)

	first, _, err := svc.ListSchedulableAccounts(context.Background(), nil, PlatformOpenAI, false)
	require.NoError(t, err)
	first[0].Credentials["nested"].(map[string]any)["token"] = "mutated"
	first[0].Extra["limits"].(map[string]any)["remaining"] = float64(0)

	second, _, err := svc.ListSchedulableAccounts(context.Background(), nil, PlatformOpenAI, false)
	require.NoError(t, err)
	require.Equal(t, "original", second[0].Credentials["nested"].(map[string]any)["token"])
	require.Equal(t, float64(3), second[0].Extra["limits"].(map[string]any)["remaining"])
}

type snapshotFallbackProbeRepo struct {
	AccountRepository
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	calls   int
}

func (r *snapshotFallbackProbeRepo) ListSchedulableUngroupedByPlatform(context.Context, string) ([]Account, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	select {
	case r.started <- struct{}{}:
	default:
	}
	<-r.release
	return []Account{{ID: 42, Platform: PlatformOpenAI, Status: StatusActive}}, nil
}

func TestSchedulerSnapshotFallbackSingleflightPerBucket(t *testing.T) {
	cache := &decodeCacheProbe{hit: false, getBarrier: make(chan struct{})}
	repo := &snapshotFallbackProbeRepo{started: make(chan struct{}, 2), release: make(chan struct{})}
	svc := NewSchedulerSnapshotService(cache, nil, repo, nil, nil)

	results := make(chan error, 2)
	for range 2 {
		go func() {
			_, _, err := svc.ListSchedulableAccounts(context.Background(), nil, PlatformOpenAI, false)
			results <- err
		}()
	}
	select {
	case <-repo.started:
	case <-time.After(time.Second):
		t.Fatal("fallback query did not start")
	}
	close(repo.release)
	for range 2 {
		require.NoError(t, <-results)
	}
	repo.mu.Lock()
	calls := repo.calls
	repo.mu.Unlock()
	require.Equal(t, 1, calls, "concurrent bucket misses should share one DB fallback")
	cache.mu.Lock()
	sets := cache.setCalls
	cache.mu.Unlock()
	require.Equal(t, 1, sets, "concurrent bucket misses should publish the cache once")
}

type schedulerLateSnapshotCache struct {
	SchedulerCache
	mu    sync.Mutex
	calls int
}

func (c *schedulerLateSnapshotCache) GetSnapshotVersion(context.Context, SchedulerBucket) (string, error) {
	return "v1", nil
}

func (c *schedulerLateSnapshotCache) GetSnapshot(context.Context, SchedulerBucket) ([]*Account, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.calls == 1 {
		return nil, false, nil
	}
	return []*Account{{ID: 44, Platform: PlatformOpenAI, Status: StatusActive}}, true, nil
}

func TestSchedulerSnapshotFallbackRechecksCacheBeforeLimiter(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.DbFallbackEnabled = true
	cfg.Gateway.Scheduling.DbFallbackMaxQPS = 1
	cache := &schedulerLateSnapshotCache{}
	svc := NewSchedulerSnapshotService(cache, nil, nil, nil, cfg)
	require.True(t, svc.fallbackLimit.Allow())

	accounts, _, err := svc.ListSchedulableAccounts(context.Background(), nil, PlatformOpenAI, false)
	require.NoError(t, err)
	require.Len(t, accounts, 1)
	require.Equal(t, int64(44), accounts[0].ID)
	cache.mu.Lock()
	calls := cache.calls
	cache.mu.Unlock()
	require.Equal(t, 2, calls, "shared fallback must recheck cache before consuming the limiter")
}

func TestSchedulerSnapshotFallbackSingleflightDoesNotShareMutableAccountMaps(t *testing.T) {
	cache := &decodeCacheProbe{hit: false, getBarrier: make(chan struct{})}
	repo := &snapshotFallbackProbeRepoWithMaps{started: make(chan struct{}), release: make(chan struct{})}
	svc := NewSchedulerSnapshotService(cache, nil, repo, nil, nil)

	type result struct {
		accounts []Account
		err      error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			accounts, _, err := svc.ListSchedulableAccounts(context.Background(), nil, PlatformOpenAI, false)
			results <- result{accounts: accounts, err: err}
		}()
	}
	select {
	case <-repo.started:
	case <-time.After(time.Second):
		t.Fatal("fallback query did not start")
	}
	close(repo.release)

	first, second := <-results, <-results
	require.NoError(t, first.err)
	require.NoError(t, second.err)
	first.accounts[0].Credentials["nested"].(map[string]any)["token"] = "mutated"
	first.accounts[0].Extra["limits"].(map[string]any)["remaining"] = float64(0)
	require.Equal(t, "original", second.accounts[0].Credentials["nested"].(map[string]any)["token"])
	require.Equal(t, float64(3), second.accounts[0].Extra["limits"].(map[string]any)["remaining"])
}

type snapshotFallbackProbeRepoWithMaps struct {
	AccountRepository
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *snapshotFallbackProbeRepoWithMaps) ListSchedulableUngroupedByPlatform(context.Context, string) ([]Account, error) {
	r.once.Do(func() { close(r.started) })
	<-r.release
	return []Account{{
		ID:          43,
		Platform:    PlatformOpenAI,
		Status:      StatusActive,
		Credentials: map[string]any{"nested": map[string]any{"token": "original"}},
		Extra:       map[string]any{"limits": map[string]any{"remaining": float64(3)}},
	}}, nil
}
