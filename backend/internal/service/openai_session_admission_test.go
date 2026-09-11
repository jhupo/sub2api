package service

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type sessionAdmissionCache struct {
	schedulerTestConcurrencyCache
	mu      sync.Mutex
	slots   map[int64]int
	waiters int
}

func (c *sessionAdmissionCache) AcquireAdaptiveAccountSlot(ctx context.Context, id int64, policy AccountSlotAdmission, requestID string) (bool, error) {
	return c.AcquireAccountSlot(ctx, id, AccountSoftConcurrencyLimit(policy.MaxConcurrency, policy.SoftLimitPercent), requestID)
}

func (c *sessionAdmissionCache) AcquireAccountSlot(ctx context.Context, id int64, limit int, _ string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.slots[id] >= limit {
		return false, nil
	}
	c.slots[id]++
	return true, nil
}

func (c *sessionAdmissionCache) ReleaseAccountSlot(_ context.Context, id int64, _ string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.slots[id]--
	return nil
}

func (c *sessionAdmissionCache) IncrementAccountWaitCount(_ context.Context, _ int64, limit int) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.waiters >= limit {
		return false, nil
	}
	c.waiters++
	return true, nil
}

func (c *sessionAdmissionCache) DecrementAccountWaitCount(context.Context, int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.waiters--
	return nil
}

func newSessionAdmissionScheduler(slots map[int64]int) (*defaultOpenAIAccountScheduler, *sessionAdmissionCache) {
	cfg := &config.Config{}
	cfg.Gateway.Scheduling.NewSessionSoftLimitPercent = 70
	cfg.Gateway.Scheduling.LoadBatchEnabled = true
	cfg.Gateway.OpenAIWS.LBTopK = 1
	cfg.Gateway.OpenAIWS.SchedulerScoreWeights.Priority = 100
	cache := &sessionAdmissionCache{slots: slots}
	return &defaultOpenAIAccountScheduler{service: &OpenAIGatewayService{
		cfg: cfg, concurrencyService: NewConcurrencyService(cache),
	}}, cache
}

func sessionAdmissionAccounts(count, limit int) []*Account {
	accounts := make([]*Account, count)
	for i := range accounts {
		accounts[i] = &Account{ID: int64(i + 1), Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
			Status: StatusActive, Schedulable: true, Concurrency: limit, Priority: i}
	}
	return accounts
}

func TestNewSessionAdmissionSoftSpilloverAndHardFallback(t *testing.T) {
	ctx := context.Background()
	scheduler, cache := newSessionAdmissionScheduler(map[int64]int{1: 7})
	accounts := sessionAdmissionAccounts(2, 10)
	req := OpenAIAccountScheduleRequest{Platform: PlatformOpenAI}
	// Deliberately stale loads: Redis admission must enforce the soft ceiling.
	result, err := scheduler.tryAcquireOpenAINewSession(ctx, req, accounts, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, int64(2), result.Account.ID, "idle overflow outside Top-K must be reached")
	result.ReleaseFunc()
	cache.slots[2] = 7
	result, err = scheduler.tryAcquireOpenAINewSession(ctx, req, accounts, nil)
	require.NoError(t, err)
	require.Nil(t, result)
	attempt := scheduler.trySelectByLoadBalancePool(ctx, req, accounts, nil, newOpenAISelectionProbeBudget())
	require.NoError(t, attempt.err)
	require.NotNil(t, attempt.result, "soft saturation must not prevent hard-capacity fallback")
	require.Equal(t, int64(1), attempt.result.Account.ID)
	attempt.result.ReleaseFunc()
}

func TestNewSessionAdmissionProtectsAffinityAndContinuation(t *testing.T) {
	scheduler, _ := newSessionAdmissionScheduler(map[int64]int{})
	for _, req := range []OpenAIAccountScheduleRequest{
		{Platform: PlatformOpenAI, StickyAccountID: 1},
		{Platform: PlatformOpenAI, StickyPreviousAccountID: 1},
		{Platform: PlatformOpenAI, PreviousResponseID: "response"},
		{Platform: PlatformOpenAI, GuardianParentAccountID: 1},
		{Platform: PlatformOpenAI, StickyMigrationTarget: true},
		{Platform: PlatformOpenAI, PreserveStickyBinding: true},
		{Platform: PlatformGrok},
	} {
		require.Zero(t, scheduler.service.newSessionSoftLimitPercent(context.Background(), req))
	}
	for limit, want := range map[int]int{0: 0, 1: 1, 2: 2, 3: 3, 4: 3, 5: 4, 10: 7, 20: 14} {
		require.Equal(t, want, AccountSoftConcurrencyLimit(limit, 70))
	}
}

func TestNewSessionAdmissionConcurrent30Users50Accounts(t *testing.T) {
	scheduler, cache := newSessionAdmissionScheduler(map[int64]int{})
	start := make(chan struct{})
	results := make(chan *AccountSelectionResult, 30)
	errs := make(chan error, 30)
	var wg sync.WaitGroup
	for user := 0; user < 30; user++ {
		wg.Add(1)
		go func(user int) {
			defer wg.Done()
			<-start
			// Accounts are request-local, including their lazy model caches.
			result, err := scheduler.tryAcquireOpenAINewSession(context.Background(), OpenAIAccountScheduleRequest{
				Platform: PlatformOpenAI, SessionHash: fmt.Sprint(user), PreserveStickyBinding: false,
			}, sessionAdmissionAccounts(50, 10), nil)
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}(user)
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	for _, count := range cache.slots {
		require.LessOrEqual(t, count, 7)
	}
	require.GreaterOrEqual(t, len(cache.slots), 5)
	admitted := 0
	for result := range results {
		require.NotNil(t, result)
		admitted++
		result.ReleaseFunc()
	}
	require.Equal(t, 30, admitted)
	for _, count := range cache.slots {
		require.Zero(t, count)
	}
}

func TestOpenAIPoolWaitRechecksAndReleasesQueue(t *testing.T) {
	for _, cancelRequest := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelRequest), func(t *testing.T) {
			scheduler, cache := newSessionAdmissionScheduler(map[int64]int{1: 10})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			selection := &AccountSelectionResult{Account: sessionAdmissionAccounts(1, 10)[0], WaitPlan: &AccountWaitPlan{
				AccountID: 1, MaxConcurrency: 10, MaxWaiting: 3, Timeout: 2 * time.Second,
			}}
			if cancelRequest {
				cancel()
			}
			calls := 0
			result, err := scheduler.service.waitForOpenAIPoolCapacity(ctx, selection, func(context.Context) (*AccountSelectionResult, error) {
				calls++
				if calls == 1 {
					return nil, nil
				}
				return &AccountSelectionResult{Account: &Account{ID: 2}, Acquired: true, ReleaseFunc: func() {}}, nil
			})
			if cancelRequest {
				require.ErrorIs(t, err, context.Canceled)
				require.Zero(t, calls)
			} else {
				require.NoError(t, err)
				require.Equal(t, int64(2), result.Account.ID)
				require.Equal(t, 2, calls)
			}
			require.Zero(t, cache.waiters)
		})
	}
}

func TestOpenAIPoolWaitDoesNotHideStoreFailure(t *testing.T) {
	scheduler, cache := newSessionAdmissionScheduler(map[int64]int{1: 10})
	want := errors.New("store unavailable")
	_, err := scheduler.service.waitForOpenAIPoolCapacity(context.Background(), &AccountSelectionResult{
		Account: sessionAdmissionAccounts(1, 10)[0], WaitPlan: &AccountWaitPlan{AccountID: 1, MaxConcurrency: 10, MaxWaiting: 3, Timeout: time.Second},
	}, func(context.Context) (*AccountSelectionResult, error) { return nil, want })
	require.ErrorIs(t, err, want)
	require.Zero(t, cache.waiters)
}

func TestOpenAIPoolWaitFullQueueStillUsesSpare(t *testing.T) {
	scheduler, cache := newSessionAdmissionScheduler(map[int64]int{1: 10})
	cache.waiters = 3
	selection, err := scheduler.service.waitForOpenAIPoolCapacity(context.Background(), &AccountSelectionResult{
		Account: sessionAdmissionAccounts(1, 10)[0], WaitPlan: &AccountWaitPlan{AccountID: 1, MaxConcurrency: 10, MaxWaiting: 3, Timeout: time.Second},
	}, func(context.Context) (*AccountSelectionResult, error) {
		return &AccountSelectionResult{Account: &Account{ID: 2}, Acquired: true}, nil
	})
	require.NoError(t, err)
	require.Equal(t, int64(2), selection.Account.ID)
	require.Equal(t, 3, cache.waiters)
}

func TestOpenAIPoolWaitExhaustsOneBudget(t *testing.T) {
	scheduler, cache := newSessionAdmissionScheduler(map[int64]int{1: 10})
	selection := &AccountSelectionResult{Account: sessionAdmissionAccounts(1, 10)[0], WaitPlan: &AccountWaitPlan{
		AccountID: 1, MaxConcurrency: 10, MaxWaiting: 3, Timeout: 20 * time.Millisecond,
	}}
	result, err := scheduler.service.waitForOpenAIPoolCapacity(context.Background(), selection, func(context.Context) (*AccountSelectionResult, error) {
		t.Fatal("no probe may start after the wait budget expired")
		return nil, nil
	})
	require.NoError(t, err)
	require.Same(t, selection, result)
	require.Equal(t, time.Nanosecond, result.WaitPlan.Timeout)
	require.Zero(t, cache.waiters)
}

func TestNewSessionAdmissionDoesNotMutateResolvedPolicy(t *testing.T) {
	cache := &sessionAdmissionCache{slots: map[int64]int{1: 14}}
	svc := NewConcurrencyService(cache)
	policy := &AccountSlotAdmission{MaxConcurrency: 20, PressureModel: "model", PressureWindow: time.Minute}
	ctx := context.WithValue(context.Background(), accountSlotAdmissionKey{}, accountSlotAdmissionResolver(func(context.Context, int64) (*AccountSlotAdmission, error) { return policy, nil }))
	softCtx := context.WithValue(ctx, accountSoftAdmissionKey{}, 70)
	result, err := svc.AcquireAccountSlot(softCtx, 1, 20)
	require.NoError(t, err)
	require.False(t, result.Acquired)
	require.Zero(t, policy.SoftLimitPercent)
	result, err = svc.AcquireAccountSlot(ctx, 1, 20)
	require.NoError(t, err)
	require.True(t, result.Acquired)
	result.ReleaseFunc()
}

func TestOpenAIPoolCapacityMigrationCommitsAfterSuccess(t *testing.T) {
	for _, advanced := range []string{"false", "true"} {
		t.Run(advanced, func(t *testing.T) {
			scheduler, slots := newSessionAdmissionScheduler(map[int64]int{1: 10})
			svc := scheduler.service
			svc.cfg.Gateway.Scheduling.StickySessionWaitTimeout = 3 * time.Second
			svc.cfg.Gateway.Scheduling.StickySessionMaxWaiting = 3
			accounts := sessionAdmissionAccounts(2, 10)
			for _, account := range accounts {
				account.Type = AccountTypeOAuth
			}
			svc.accountRepo = schedulerTestOpenAIAccountRepo{accounts: []Account{*accounts[0], *accounts[1]}}
			cache := newCodexMigrationTestCache("capacity-session", 1)
			svc.cache = cache
			svc.rateLimitService = newOpenAIAdvancedSchedulerRateLimitService(advanced)
			ctx := codexAdaptivePolicyContext()
			codexAdaptiveRequestFromContext(ctx).sessionHash = "capacity-session"
			ctx = context.WithValue(ctx, accountSlotAdmissionKey{}, accountSlotAdmissionResolver(svc.resolveCodexAccountSlotAdmission))
			defer FinishCodexAdaptiveSchedulingRequest(ctx)
			selection, _, err := svc.SelectAccountWithScheduler(ctx, nil, "", "capacity-session", "gpt-5.6-sol", nil, OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			require.NotNil(t, selection)
			require.True(t, selection.Acquired)
			require.Equal(t, int64(2), selection.Account.ID)
			require.True(t, codexAdaptiveStickyMigrationPending(ctx))
			require.Equal(t, int64(1), cache.sessionBindings["openai:capacity-session"])
			require.Zero(t, slots.waiters)
			require.NoError(t, svc.CommitCodexAdaptiveStickyOnSuccess(ctx, nil, selection.Account, false))
			selection.ReleaseFunc()
			require.Equal(t, int64(2), cache.sessionBindings["openai:capacity-session"])
			// A becoming idle must not pull the session back after a successful migration.
			slots.slots[1] = 0
			next, _, err := svc.SelectAccountWithScheduler(ctx, nil, "", "capacity-session", "gpt-5.6-sol", nil, OpenAIUpstreamTransportAny, false)
			require.NoError(t, err)
			require.Equal(t, int64(2), next.Account.ID)
			next.ReleaseFunc()
		})
	}
}
