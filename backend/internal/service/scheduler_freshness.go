package service

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// SchedulerFreshness is the durable subset that can invalidate an account
// copied from a scheduler snapshot.
type SchedulerFreshness struct {
	ID                      int64
	Platform                string
	Type                    string
	Status                  string
	Schedulable             bool
	ExpiresAt               *time.Time
	AutoPauseOnExpired      bool
	RateLimitedAt           *time.Time
	RateLimitResetAt        *time.Time
	OverloadUntil           *time.Time
	TempUnschedulableUntil  *time.Time
	TempUnschedulableReason string
	ParentAccountID         *int64
	PrivacyMode             string
	GroupIDs                []int64
}

type SchedulerFreshnessMetricsSnapshot struct {
	RequestTotal         int64
	BatchQueryTotal      int64
	BatchAccountTotal    int64
	BatchDurationMsTotal int64
	ProjectionErrorTotal int64
	MissingAccountTotal  int64
	FailedAccountTotal   int64
	ParentCacheHitTotal  int64
	ParentCacheMissTotal int64
	ParentCacheFailTotal int64
}

var schedulerFreshnessMetrics struct {
	requestTotal         atomic.Int64
	batchQueryTotal      atomic.Int64
	batchAccountTotal    atomic.Int64
	batchDurationMsTotal atomic.Int64
	projectionErrors     atomic.Int64
	missingAccounts      atomic.Int64
	failedAccounts       atomic.Int64
	parentHits           atomic.Int64
	parentMisses         atomic.Int64
	parentFailures       atomic.Int64
}

func SnapshotSchedulerFreshnessMetrics() SchedulerFreshnessMetricsSnapshot {
	return SchedulerFreshnessMetricsSnapshot{
		RequestTotal:         schedulerFreshnessMetrics.requestTotal.Load(),
		BatchQueryTotal:      schedulerFreshnessMetrics.batchQueryTotal.Load(),
		BatchAccountTotal:    schedulerFreshnessMetrics.batchAccountTotal.Load(),
		BatchDurationMsTotal: schedulerFreshnessMetrics.batchDurationMsTotal.Load(),
		ProjectionErrorTotal: schedulerFreshnessMetrics.projectionErrors.Load(),
		MissingAccountTotal:  schedulerFreshnessMetrics.missingAccounts.Load(),
		FailedAccountTotal:   schedulerFreshnessMetrics.failedAccounts.Load(),
		ParentCacheHitTotal:  schedulerFreshnessMetrics.parentHits.Load(),
		ParentCacheMissTotal: schedulerFreshnessMetrics.parentMisses.Load(),
		ParentCacheFailTotal: schedulerFreshnessMetrics.parentFailures.Load(),
	}
}

type schedulerFreshnessContextKey struct{}

type schedulerFreshnessRequest struct {
	mu          sync.Mutex
	accountRepo AccountRepository
	snapshot    *SchedulerSnapshotService
	ids         map[int64]struct{}
	loaded      map[int64]struct{}
	loading     map[int64]chan struct{}
	missing     map[int64]struct{}
	failed      map[int64]struct{}
	accounts    map[int64]SchedulerFreshness
	hydrated    map[int64]Account
}

func withSchedulerFreshness(ctx context.Context, accountRepo AccountRepository, snapshot *SchedulerSnapshotService, ids ...int64) context.Context {
	if existing := schedulerFreshnessFromContext(ctx); existing != nil && existing.enabled() {
		existing.addIDs(ids...)
		return ctx
	}
	return newSchedulerFreshnessContext(ctx, accountRepo, snapshot, ids...)
}

func refreshSchedulerFreshness(ctx context.Context, accountRepo AccountRepository, snapshot *SchedulerSnapshotService, ids ...int64) context.Context {
	return newSchedulerFreshnessContext(ctx, accountRepo, snapshot, ids...)
}

func newSchedulerFreshnessContext(ctx context.Context, accountRepo AccountRepository, snapshot *SchedulerSnapshotService, ids ...int64) context.Context {
	state := &schedulerFreshnessRequest{
		accountRepo: accountRepo,
		snapshot:    snapshot,
		ids:         make(map[int64]struct{}, len(ids)),
		loaded:      make(map[int64]struct{}, len(ids)),
		loading:     make(map[int64]chan struct{}),
		missing:     make(map[int64]struct{}),
		failed:      make(map[int64]struct{}),
		accounts:    make(map[int64]SchedulerFreshness),
		hydrated:    make(map[int64]Account),
	}
	schedulerFreshnessMetrics.requestTotal.Add(1)
	state.addIDs(ids...)
	return context.WithValue(ctx, schedulerFreshnessContextKey{}, state)
}

func schedulerFreshnessFromContext(ctx context.Context) *schedulerFreshnessRequest {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(schedulerFreshnessContextKey{}).(*schedulerFreshnessRequest)
	return state
}

func (r *schedulerFreshnessRequest) enabled() bool {
	return r != nil && r.snapshot != nil && r.accountRepo != nil
}

func schedulerHydratedAccount(ctx context.Context, accountID int64) (*Account, bool) {
	state := schedulerFreshnessFromContext(ctx)
	if state == nil || !state.enabled() || accountID <= 0 {
		return nil, false
	}
	state.mu.Lock()
	account, ok := state.hydrated[accountID]
	state.mu.Unlock()
	if !ok {
		return nil, false
	}
	clone := cloneSnapshotAccount(&account)
	return &clone, true
}

func rememberSchedulerHydratedAccount(ctx context.Context, account *Account) {
	state := schedulerFreshnessFromContext(ctx)
	if state == nil || !state.enabled() || account == nil || account.ID <= 0 {
		return
	}
	clone := cloneSnapshotAccount(account)
	state.mu.Lock()
	state.hydrated[account.ID] = clone
	state.mu.Unlock()
}

func withSchedulerFreshnessAccounts(ctx context.Context, accountRepo AccountRepository, snapshot *SchedulerSnapshotService, accounts []Account) context.Context {
	state := schedulerFreshnessFromContext(ctx)
	if state == nil {
		ctx = withSchedulerFreshness(ctx, accountRepo, snapshot)
		state = schedulerFreshnessFromContext(ctx)
	}
	if state == nil || !state.enabled() {
		return ctx
	}
	for i := range accounts {
		state.addAccount(&accounts[i])
	}
	state.prime(ctx)
	return ctx
}

func applySchedulerFreshnessAccounts(ctx context.Context, accounts []Account) []Account {
	state := schedulerFreshnessFromContext(ctx)
	if state == nil || !state.enabled() || len(accounts) == 0 {
		return accounts
	}
	filtered := make([]Account, 0, len(accounts))
	for i := range accounts {
		fresh, ok := state.apply(ctx, &accounts[i])
		if ok {
			filtered = append(filtered, *fresh)
		}
	}
	return filtered
}

func (r *schedulerFreshnessRequest) addIDs(ids ...int64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range ids {
		if id > 0 {
			r.ids[id] = struct{}{}
		}
	}
}

func (r *schedulerFreshnessRequest) addAccount(account *Account) {
	if account == nil {
		return
	}
	r.addIDs(account.ID)
	if account.ParentAccountID != nil {
		r.addIDs(*account.ParentAccountID)
	}
}

func (r *schedulerFreshnessRequest) prime(ctx context.Context) {
	if !r.enabled() {
		return
	}
	for {
		r.mu.Lock()
		ids := make([]int64, 0, len(r.ids))
		waiters := make([]chan struct{}, 0)
		for id := range r.ids {
			if _, ok := r.loaded[id]; ok {
				continue
			}
			if done, ok := r.loading[id]; ok {
				waiters = append(waiters, done)
				continue
			}
			done := make(chan struct{})
			r.loading[id] = done
			ids = append(ids, id)
		}
		r.mu.Unlock()

		if len(ids) > 0 {
			r.loadBatch(ctx, ids)
		}
		for _, done := range waiters {
			select {
			case <-done:
			case <-ctx.Done():
				return
			}
		}
		return
	}
}

func (r *schedulerFreshnessRequest) loadBatch(ctx context.Context, ids []int64) {
	started := time.Now()
	schedulerFreshnessMetrics.batchQueryTotal.Add(1)
	schedulerFreshnessMetrics.batchAccountTotal.Add(int64(len(ids)))
	fresh, err := r.accountRepo.ReadSchedulerFreshness(ctx, ids)
	if err != nil {
		schedulerFreshnessMetrics.projectionErrors.Add(1)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	for _, id := range ids {
		if err != nil {
			r.failed[id] = struct{}{}
			schedulerFreshnessMetrics.failedAccounts.Add(1)
		} else if value, ok := fresh[id]; ok {
			r.accounts[id] = value
		} else {
			r.missing[id] = struct{}{}
			schedulerFreshnessMetrics.missingAccounts.Add(1)
		}
		r.loaded[id] = struct{}{}
		done := r.loading[id]
		delete(r.loading, id)
		if done != nil {
			close(done)
		}
	}
	schedulerFreshnessMetrics.batchDurationMsTotal.Add(time.Since(started).Milliseconds())
}

func schedulerFreshnessFromAccount(account *Account) SchedulerFreshness {
	return SchedulerFreshness{
		ID:                      account.ID,
		Platform:                account.Platform,
		Type:                    account.Type,
		Status:                  account.Status,
		Schedulable:             account.Schedulable,
		ExpiresAt:               account.ExpiresAt,
		AutoPauseOnExpired:      account.AutoPauseOnExpired,
		RateLimitedAt:           account.RateLimitedAt,
		RateLimitResetAt:        account.RateLimitResetAt,
		OverloadUntil:           account.OverloadUntil,
		TempUnschedulableUntil:  account.TempUnschedulableUntil,
		TempUnschedulableReason: account.TempUnschedulableReason,
		ParentAccountID:         account.ParentAccountID,
		PrivacyMode:             account.getExtraString("privacy_mode"),
		GroupIDs:                append([]int64(nil), account.GroupIDs...),
	}
}

func (r *schedulerFreshnessRequest) apply(ctx context.Context, account *Account) (*Account, bool) {
	if account == nil {
		return nil, false
	}
	if !r.enabled() {
		return account, true
	}
	r.addAccount(account)
	r.prime(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, failed := r.failed[account.ID]; failed {
		return nil, false
	}
	fresh, ok := r.accounts[account.ID]
	if !ok {
		return nil, false
	}
	clone := *account
	clone.Platform = fresh.Platform
	clone.Type = fresh.Type
	clone.Status = fresh.Status
	clone.Schedulable = fresh.Schedulable
	clone.ExpiresAt = fresh.ExpiresAt
	clone.AutoPauseOnExpired = fresh.AutoPauseOnExpired
	clone.RateLimitedAt = fresh.RateLimitedAt
	clone.RateLimitResetAt = fresh.RateLimitResetAt
	clone.OverloadUntil = fresh.OverloadUntil
	clone.TempUnschedulableUntil = fresh.TempUnschedulableUntil
	clone.TempUnschedulableReason = fresh.TempUnschedulableReason
	clone.ParentAccountID = fresh.ParentAccountID
	clone.GroupIDs = append([]int64(nil), fresh.GroupIDs...)
	clone.AccountGroups = make([]AccountGroup, 0, len(clone.GroupIDs))
	for _, groupID := range clone.GroupIDs {
		clone.AccountGroups = append(clone.AccountGroups, AccountGroup{AccountID: clone.ID, GroupID: groupID})
	}
	clone.Extra = cloneSnapshotJSONMap(clone.Extra)
	if clone.Extra == nil {
		clone.Extra = make(map[string]any)
	}
	delete(clone.Extra, "privacy_mode")
	if fresh.PrivacyMode != "" {
		clone.Extra["privacy_mode"] = fresh.PrivacyMode
	}
	return &clone, true
}

func schedulerFreshnessLookupResult(ctx context.Context, accountID int64) (*Account, bool) {
	state := schedulerFreshnessFromContext(ctx)
	if state == nil || !state.enabled() || accountID <= 0 {
		return nil, false
	}
	state.addIDs(accountID)
	state.prime(ctx)
	state.mu.Lock()
	fresh, ok := state.accounts[accountID]
	_, failed := state.failed[accountID]
	state.mu.Unlock()
	if failed || !ok {
		schedulerFreshnessMetrics.parentMisses.Add(1)
		if failed {
			schedulerFreshnessMetrics.parentFailures.Add(1)
		}
		return nil, true
	}
	schedulerFreshnessMetrics.parentHits.Add(1)
	return schedulerFreshnessAccount(fresh), true
}

func schedulerFreshnessAccount(fresh SchedulerFreshness) *Account {
	account := &Account{
		ID:                      fresh.ID,
		Platform:                fresh.Platform,
		Type:                    fresh.Type,
		Status:                  fresh.Status,
		Schedulable:             fresh.Schedulable,
		ExpiresAt:               fresh.ExpiresAt,
		AutoPauseOnExpired:      fresh.AutoPauseOnExpired,
		RateLimitedAt:           fresh.RateLimitedAt,
		RateLimitResetAt:        fresh.RateLimitResetAt,
		OverloadUntil:           fresh.OverloadUntil,
		TempUnschedulableUntil:  fresh.TempUnschedulableUntil,
		TempUnschedulableReason: fresh.TempUnschedulableReason,
		ParentAccountID:         fresh.ParentAccountID,
	}
	account.GroupIDs = append([]int64(nil), fresh.GroupIDs...)
	account.AccountGroups = make([]AccountGroup, 0, len(account.GroupIDs))
	for _, groupID := range account.GroupIDs {
		account.AccountGroups = append(account.AccountGroups, AccountGroup{AccountID: account.ID, GroupID: groupID})
	}
	if fresh.PrivacyMode != "" {
		account.Extra = map[string]any{"privacy_mode": fresh.PrivacyMode}
	}
	return account
}
