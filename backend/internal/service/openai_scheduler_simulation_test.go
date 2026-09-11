package service

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Exercise the production scheduler and admission service with synchronized,
// request-ID-aware stores. No upstream traffic or production data is used.
type schedulingSimulationSlots struct {
	ConcurrencyCache
	pressure                                       map[codexAdaptivePressureScope]map[string]time.Time
	peakPressure                                   int
	mu                                             sync.Mutex
	slots                                          map[int64]map[string]bool
	waiters                                        map[int64]int
	peaks                                          map[int64]int
	peakTotal, peakWaiting, acquisitions, releases int
}

func (c *schedulingSimulationSlots) AcquireAccountSlot(ctx context.Context, id int64, limit int, request string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.acquireLocked(id, limit, request)
}

func (c *schedulingSimulationSlots) acquireLocked(id int64, limit int, request string) (bool, error) {
	if c.slots[id][request] {
		return true, nil
	}
	if len(c.slots[id]) >= limit {
		return false, nil
	}
	if c.slots[id] == nil {
		c.slots[id] = make(map[string]bool)
	}
	c.slots[id][request] = true
	c.acquisitions++
	c.peaks[id] = max(c.peaks[id], len(c.slots[id]))
	total := 0
	for _, slots := range c.slots {
		total += len(slots)
	}
	c.peakTotal = max(c.peakTotal, total)
	return true, nil
}

func (c *schedulingSimulationSlots) AcquireAdaptiveAccountSlot(ctx context.Context, id int64, policy AccountSlotAdmission, request string) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	limit := codexAdaptiveConcurrencyLimit(policy.MaxConcurrency, c.pressureLocked(id, policy.PressureModel))
	return c.acquireLocked(id, AccountSoftConcurrencyLimit(limit, policy.SoftLimitPercent), request)
}

func (c *schedulingSimulationSlots) pressureLocked(id int64, model string) int {
	sessions := c.pressure[codexAdaptivePressureScopeFor(id, model)]
	for session, until := range sessions {
		if time.Now().After(until) {
			delete(sessions, session)
		}
	}
	return len(sessions)
}

func (c *schedulingSimulationSlots) ObserveCodexAdaptiveFailure(_ context.Context, id int64, model, session string, window time.Duration) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pressure == nil {
		c.pressure = make(map[codexAdaptivePressureScope]map[string]time.Time)
	}
	scope := codexAdaptivePressureScopeFor(id, model)
	if c.pressure[scope] == nil {
		c.pressure[scope] = make(map[string]time.Time)
	}
	c.pressure[scope][session] = time.Now().Add(window)
	pressure := c.pressureLocked(id, model)
	c.peakPressure = max(c.peakPressure, pressure)
	return pressure, nil
}

func (c *schedulingSimulationSlots) ObserveCodexAdaptiveSuccess(_ context.Context, id int64, model, session string, _ time.Duration) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.pressure[codexAdaptivePressureScopeFor(id, model)], session)
	return c.pressureLocked(id, model), nil
}

func (c *schedulingSimulationSlots) GetCodexAdaptivePressureBatch(_ context.Context, ids []int64, model string, _ time.Duration) (map[int64]int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make(map[int64]int, len(ids))
	for _, id := range ids {
		result[id] = c.pressureLocked(id, model)
	}
	return result, nil
}

func (c *schedulingSimulationSlots) ReleaseAccountSlot(_ context.Context, id int64, request string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.slots[id][request] {
		delete(c.slots[id], request)
		c.releases++
	}
	return nil
}

func (c *schedulingSimulationSlots) GetAccountConcurrency(_ context.Context, id int64) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.slots[id]), nil
}

func (c *schedulingSimulationSlots) GetAccountsLoadBatch(_ context.Context, accounts []AccountWithConcurrency) (map[int64]*AccountLoadInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	loads := make(map[int64]*AccountLoadInfo, len(accounts))
	for _, account := range accounts {
		loads[account.ID] = &AccountLoadInfo{AccountID: account.ID, CurrentConcurrency: len(c.slots[account.ID]),
			WaitingCount: c.waiters[account.ID], LoadRate: len(c.slots[account.ID]) * 100 / max(1, account.MaxConcurrency)}
	}
	return loads, nil
}

func (c *schedulingSimulationSlots) IncrementAccountWaitCount(ctx context.Context, id int64, limit int) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.waiters[id] >= limit {
		return false, nil
	}
	c.waiters[id]++
	total := 0
	for _, n := range c.waiters {
		total += n
	}
	c.peakWaiting = max(c.peakWaiting, total)
	return true, nil
}

func (c *schedulingSimulationSlots) DecrementAccountWaitCount(_ context.Context, id int64) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.waiters[id]--
	return nil
}

func (c *schedulingSimulationSlots) GetAccountWaitingCount(_ context.Context, id int64) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.waiters[id], nil
}

type schedulingSimulationRepo struct {
	AccountRepository
	mu       sync.Mutex
	accounts []Account
}

func (r *schedulingSimulationRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, account := range r.accounts {
		if account.ID == id {
			return &account, nil
		}
	}
	return nil, ErrAccountNotFound
}

func (r *schedulingSimulationRepo) ListSchedulableUngroupedByPlatform(_ context.Context, platform string) ([]Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]Account, 0, len(r.accounts))
	for _, account := range r.accounts {
		if account.Platform == platform {
			result = append(result, account)
		}
	}
	return result, nil
}

func (r *schedulingSimulationRepo) ReadSchedulerFreshness(_ context.Context, ids []int64) (map[int64]SchedulerFreshness, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make(map[int64]SchedulerFreshness, len(ids))
	for _, id := range ids {
		for i := range r.accounts {
			if r.accounts[i].ID == id {
				result[id] = schedulerFreshnessFromAccount(&r.accounts[i])
			}
		}
	}
	return result, nil
}

type schedulingSimulationSticky struct{ *codexMigrationTestCache }

func (c *schedulingSimulationSticky) SetSessionAccountID(ctx context.Context, group int64, key string, id int64, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stubGatewayCache.SetSessionAccountID(ctx, group, key, id, ttl)
}
func (c *schedulingSimulationSticky) DeleteSessionAccountID(ctx context.Context, group int64, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stubGatewayCache.DeleteSessionAccountID(ctx, group, key)
}
func (c *schedulingSimulationSticky) RefreshSessionTTL(context.Context, int64, string, time.Duration) error {
	return nil
}

func TestOpenAISchedulerSimulation40Users6Accounts5Slots(t *testing.T) {
	for _, advanced := range []string{"false", "true"} {
		t.Run("advanced="+advanced, func(t *testing.T) {
			scheduler, _ := newSessionAdmissionScheduler(nil)
			svc := scheduler.service
			svc.cfg.Gateway.Scheduling.StickySessionWaitTimeout = 5 * time.Second
			svc.cfg.Gateway.Scheduling.StickySessionMaxWaiting = 80
			svc.cfg.Gateway.Scheduling.FallbackWaitTimeout = 5 * time.Second
			svc.cfg.Gateway.Scheduling.FallbackMaxWaiting = 80
			slots := &schedulingSimulationSlots{slots: make(map[int64]map[string]bool), waiters: make(map[int64]int), peaks: make(map[int64]int)}
			svc.concurrencyService = NewConcurrencyService(slots)
			repo := &schedulingSimulationRepo{}
			for _, account := range sessionAdmissionAccounts(6, 5) {
				account.Type = AccountTypeOAuth
				repo.accounts = append(repo.accounts, *account)
			}
			svc.accountRepo = repo
			sticky := &schedulingSimulationSticky{newCodexMigrationTestCache("unused", 0)}
			svc.cache = sticky
			svc.rateLimitService = newOpenAIAdvancedSchedulerRateLimitService(advanced)
			var succeeded, injected, retries, rejected, canceled atomic.Int64
			newContext := func(parent context.Context, user int) context.Context {
				state := &codexAdaptiveRequestState{model: "gpt-5.6-sol", sessionHash: fmt.Sprintf("user-%d", user),
					sessionMember: fmt.Sprintf("user-%d", user), pressures: make(map[codexAdaptivePressureScope]int)}
				ctx := context.WithValue(parent, codexAdaptiveRequestContextKey{}, state)
				return context.WithValue(ctx, accountSlotAdmissionKey{}, accountSlotAdmissionResolver(svc.resolveCodexAccountSlotAdmission))
			}
			selectAccount := func(ctx context.Context, user int, excluded map[int64]struct{}) (*AccountSelectionResult, error) {
				selection, _, err := svc.SelectAccountWithScheduler(ctx, nil, "", fmt.Sprintf("user-%d", user), "gpt-5.6-sol", excluded, OpenAIUpstreamTransportAny, false)
				if err != nil || selection == nil || selection.Acquired {
					return selection, err
				}
				if selection.WaitPlan == nil {
					return nil, errors.New("selection has neither slot nor wait plan")
				}
				// Mirror the handler's post-selection wait on the already selected
				// account. Pool spillover belongs INSIDE SelectAccountWithScheduler,
				// before migration coordination; repeating it here would bypass the
				// claimed target and exercise a path no production handler uses.
				plan := selection.WaitPlan
				canWait, err := svc.concurrencyService.IncrementAccountWaitCount(ctx, plan.AccountID, plan.MaxWaiting)
				if err != nil {
					return nil, err
				}
				if !canWait {
					return nil, errors.New("simulation account wait queue full")
				}
				defer svc.concurrencyService.DecrementAccountWaitCount(ctx, plan.AccountID)
				waitCtx, cancel := context.WithTimeout(ctx, plan.Timeout)
				defer cancel()
				release, acquired, err := svc.waitForCodexAffinitySlot(waitCtx, selection)
				if err != nil {
					return nil, err
				}
				if !acquired {
					return nil, waitCtx.Err()
				}
				selection.Acquired, selection.ReleaseFunc, selection.WaitPlan = true, release, nil
				return selection, nil
			}
			for round := 0; round < 5; round++ {
				if round == 4 {
					// Advance the fault window before validating restored capacity.
					slots.mu.Lock()
					for _, sessions := range slots.pressure {
						for key := range sessions {
							sessions[key] = time.Now().Add(-time.Second)
						}
					}
					slots.mu.Unlock()
				}
				rng := rand.New(rand.NewSource(int64(20260911 + round)))
				faults := rng.Perm(6)
				repo.mu.Lock()
				for i := range repo.accounts {
					repo.accounts[i].Status = StatusActive
					repo.accounts[i].RateLimitResetAt = nil
					repo.accounts[i].UpdatedAt = time.Now()
				}
				if round == 1 {
					for _, i := range faults[:2] {
						repo.accounts[i].Status = StatusError
					}
				}
				if round == 2 {
					until := time.Now().Add(time.Hour)
					for _, i := range faults[:2] {
						repo.accounts[i].RateLimitResetAt = &until
					}
				}
				if round == 3 {
					for i := range repo.accounts {
						repo.accounts[i].Status = StatusError
					}
				}
				repo.mu.Unlock()
				start, releaseSaturation := make(chan struct{}), make(chan struct{})
				if round != 0 {
					close(releaseSaturation)
				}
				errs := make(chan error, 40)
				var wg sync.WaitGroup
				for user := 0; user < 40; user++ {
					wg.Add(1)
					go func(user int) {
						defer wg.Done()
						<-start
						parent, cancel := context.WithTimeout(context.Background(), 10*time.Second)
						defer cancel()
						ctx := newContext(parent, user)
						defer FinishCodexAdaptiveSchedulingRequest(ctx)
						random := rand.New(rand.NewSource(int64(1000*round + user)))
						failFirst := (round == 1 || round == 2) && random.Intn(4) == 0
						excluded := make(map[int64]struct{})
						for attempt := 0; attempt < 2; attempt++ {
							selection, err := selectAccount(ctx, user, excluded)
							if round == 3 {
								if selection != nil && selection.ReleaseFunc != nil {
									selection.ReleaseFunc()
								}
								if !errors.Is(err, ErrNoAvailableAccounts) {
									errs <- fmt.Errorf("all unavailable: got %v", err)
								} else {
									rejected.Add(1)
								}
								return
							}
							if err != nil {
								errs <- fmt.Errorf("round=%d user=%d: %w", round, user, err)
								return
							}
							if selection == nil || !selection.Acquired {
								errs <- fmt.Errorf("round=%d user=%d never admitted", round, user)
								return
							}
							if _, failed := excluded[selection.Account.ID]; failed {
								selection.ReleaseFunc()
								errs <- errors.New("retried excluded account")
								return
							}
							latest, err := repo.GetByID(ctx, selection.Account.ID)
							if err != nil || latest.Status != StatusActive || (latest.RateLimitResetAt != nil && latest.RateLimitResetAt.After(time.Now())) {
								selection.ReleaseFunc()
								errs <- fmt.Errorf("round=%d user=%d admitted unavailable account %d", round, user, selection.Account.ID)
								return
							}
							state := codexAdaptiveRequestFromContext(ctx)
							state.mu.Lock()
							target := state.migration.TargetID
							state.mu.Unlock()
							if target > 0 && target != selection.Account.ID {
								selection.ReleaseFunc()
								errs <- fmt.Errorf("round=%d user=%d admitted account %d outside claimed migration target %d", round, user, selection.Account.ID, target)
								return
							}
							select {
							case <-releaseSaturation:
							case <-ctx.Done():
								selection.ReleaseFunc()
								errs <- ctx.Err()
								return
							}
							time.Sleep(time.Duration(5+random.Intn(15)) * time.Millisecond)
							if attempt == 0 && failFirst {
								injected.Add(1)
								selection.ReleaseFunc()
								svc.ApplyCodexAdaptiveFailoverPolicy(ctx, selection.Account, "gpt-5.6-sol", codexAdaptiveCapacityShedError())
								excluded[selection.Account.ID] = struct{}{}
								retries.Add(1)
								continue
							}
							if err := svc.CommitCodexAdaptiveStickyOnSuccess(ctx, nil, selection.Account, false); err != nil {
								errs <- err
							}
							svc.ObserveCodexAdaptiveSuccess(ctx, selection.Account, "gpt-5.6-sol")
							selection.ReleaseFunc()
							// Duplicate cleanup is normal when cancellation races completion.
							selection.ReleaseFunc()
							succeeded.Add(1)
							return
						}
					}(user)
				}
				close(start)
				if round == 0 {
					reachedCapacity := false
					deadline := time.Now().Add(5 * time.Second)
					for time.Now().Before(deadline) {
						slots.mu.Lock()
						reachedCapacity = slots.peakTotal == 30 && slots.peakWaiting >= 10
						slots.mu.Unlock()
						if reachedCapacity {
							break
						}
						time.Sleep(5 * time.Millisecond)
					}
					close(releaseSaturation)
					if !reachedCapacity {
						t.Error("did not exercise 30 occupied slots and 10 queued requests")
					}
				}
				wg.Wait()
				close(errs)
				for err := range errs {
					require.NoError(t, err)
				}
			}
			// After recovery and with spare capacity, the next request stays with
			// the successfully published account, not an earlier failed account.
			for user := 0; user < 40; user++ {
				ctx := newContext(context.Background(), user)
				before, err := sticky.GetSessionAccountID(ctx, 0, fmt.Sprintf("openai:user-%d", user))
				require.NoError(t, err)
				selection, err := selectAccount(ctx, user, nil)
				require.NoError(t, err)
				require.True(t, selection.Acquired)
				require.Equal(t, before, selection.Account.ID)
				selection.ReleaseFunc()
				FinishCodexAdaptiveSchedulingRequest(ctx)
			}
			// Saturate every account, then cancel 40 waiting requests together.
			holds := make([]func(), 0, 30)
			for id := int64(1); id <= 6; id++ {
				for n := 0; n < 5; n++ {
					result, err := svc.concurrencyService.AcquireAccountSlot(context.Background(), id, 5)
					require.NoError(t, err)
					require.True(t, result.Acquired)
					holds = append(holds, result.ReleaseFunc)
				}
			}
			parent, cancel := context.WithCancel(context.Background())
			var wg sync.WaitGroup
			for user := 0; user < 40; user++ {
				wg.Add(1)
				go func(user int) {
					defer wg.Done()
					ctx := newContext(parent, user)
					defer FinishCodexAdaptiveSchedulingRequest(ctx)
					selection, err := selectAccount(ctx, user, nil)
					if selection != nil && selection.ReleaseFunc != nil {
						selection.ReleaseFunc()
					}
					if errors.Is(err, context.Canceled) {
						canceled.Add(1)
					}
				}(user)
			}
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				slots.mu.Lock()
				waiting := 0
				for _, n := range slots.waiters {
					waiting += n
				}
				slots.mu.Unlock()
				if waiting == 40 {
					break
				}
				time.Sleep(5 * time.Millisecond)
			}
			cancel()
			wg.Wait()
			for _, release := range holds {
				release()
			}
			require.Equal(t, int64(160), succeeded.Load())
			require.Equal(t, int64(40), rejected.Load())
			require.Equal(t, int64(40), canceled.Load())
			require.Positive(t, injected.Load())
			require.Equal(t, injected.Load(), retries.Load())
			slots.mu.Lock()
			defer slots.mu.Unlock()
			for id := int64(1); id <= 6; id++ {
				require.Equal(t, 5, slots.peaks[id])
				require.Empty(t, slots.slots[id])
				require.Zero(t, slots.waiters[id])
			}
			require.Equal(t, slots.acquisitions, slots.releases)
			require.Equal(t, 30, slots.peakTotal)
			require.GreaterOrEqual(t, slots.peakPressure, 2, "independent failures must exercise adaptive admission")
			t.Logf("40 users / 6 OAuth accounts / 5 slots: completed=%d, overloaded retries=%d, all-unavailable rejected=%d, canceled=%d, peak concurrency=%d, peak queued=%d, peak independent failures=%d, slot acquisitions/releases=%d/%d", succeeded.Load(), retries.Load(), rejected.Load(), canceled.Load(), slots.peakTotal, slots.peakWaiting, slots.peakPressure, slots.acquisitions, slots.releases)
		})
	}
}
