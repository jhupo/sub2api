package service

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	codexAdaptivePressureWindow = 90 * time.Second
	codexAdaptiveRedisTimeout   = 300 * time.Millisecond
	codexAdaptiveQueueTimeout   = 3 * time.Second
)

type codexAdaptiveRequestState struct {
	apiKeyID         int64
	sessionHash      string
	model            string
	legacyCompact    bool
	sessionMember    string
	admissionRequest *OpenAIAccountScheduleRequest

	mu                     sync.Mutex
	pressures              map[codexAdaptivePressureScope]int
	stickyMigrationPending bool
	stickySourceID         int64
	migration              OpenAIStickyMigration
	migrationCancel        context.CancelFunc
}

type codexAdaptiveRequestContextKey struct{}

type codexAdaptivePressureScope struct {
	accountID int64
	model     string
}

// PrepareCodexAdaptiveSchedulingRequest freezes the runtime policy for one
// request. This prevents a settings refresh in the middle of a failover loop
// from changing its admission semantics.
func (s *OpenAIGatewayService) PrepareCodexAdaptiveSchedulingRequest(
	ctx context.Context,
	apiKeyID int64,
	sessionHash string,
	model string,
) context.Context {
	if ctx == nil || s == nil {
		return ctx
	}
	ctx = withOpenAIRequestAttemptBudget(ctx)
	if s.settingService == nil {
		return ctx
	}
	settings := s.settingService.codexAdaptiveSchedulingSnapshot()
	if !settings.Enabled {
		return ctx
	}
	state := &codexAdaptiveRequestState{
		apiKeyID:    apiKeyID,
		sessionHash: strings.TrimSpace(sessionHash),
		model:       normalizeCodexAdaptiveModel(model),
		pressures:   make(map[codexAdaptivePressureScope]int),
	}
	if state.apiKeyID > 0 && state.sessionHash != "" {
		digest := sha256.Sum256([]byte(fmt.Sprintf("%d|%s", state.apiKeyID, state.sessionHash)))
		state.sessionMember = fmt.Sprintf("%x", digest[:])
	}
	ctx = context.WithValue(ctx, codexAdaptiveRequestContextKey{}, state)
	return context.WithValue(ctx, accountSlotAdmissionKey{}, accountSlotAdmissionResolver(s.resolveCodexAccountSlotAdmission))
}

func (s *OpenAIGatewayService) resolveCodexAccountSlotAdmission(ctx context.Context, accountID int64) (*AccountSlotAdmission, error) {
	state := codexAdaptiveRequestFromContext(ctx)
	if state == nil {
		return nil, nil
	}
	var account *Account
	var err error
	if s.schedulerSnapshot != nil {
		account, err = s.schedulerSnapshot.GetAccount(ctx, accountID)
	} else if s.accountRepo != nil {
		account, err = s.accountRepo.GetByID(ctx, accountID)
	}
	if err != nil {
		return nil, err
	}
	if account == nil {
		return nil, ErrNoAvailableAccounts
	}
	if !codexAdaptiveAccountEligible(account) {
		return nil, nil
	}
	if codexQuotaOverdraftCandidateKnown(ctx, accountID) {
		var allowed bool
		account, allowed = normalizeCodexQuotaOverdraftHydratedAccount(ctx, account, time.Now())
		if !allowed {
			return nil, ErrNoAvailableAccounts
		}
	}
	state.mu.Lock()
	model := state.model
	legacyCompact := state.legacyCompact
	var admissionRequest *OpenAIAccountScheduleRequest
	if state.admissionRequest != nil {
		request := *state.admissionRequest
		request.RequestedModel, request.RequireCompact = model, legacyCompact
		admissionRequest = &request
	}
	state.mu.Unlock()
	ctx = s.withOpenAIQuotaAutoPauseContext(ctx)
	// Endpoint capabilities are checked by selection. Native compaction is a
	// Responses turn, not a requirement for the legacy /responses/compact route.
	if !isOpenAICompatibleAccountEligibleForRequestBeforeProfit(ctx, account, PlatformOpenAI, model, false, "") ||
		s.isOpenAIAccountBlockedBySchedulingThreshold(ctx, account) ||
		s.isOpenAIAccountRequestRuntimeBlocked(account, model) {
		return nil, ErrNoAvailableAccounts
	}
	if admissionRequest != nil {
		scheduler := defaultOpenAIAccountScheduler{service: s}
		if (admissionRequest.GroupID != nil && !openAIStickyAccountMatchesGroup(account, admissionRequest.GroupID)) ||
			!s.isOpenAIAccountTransportCompatible(account, admissionRequest.RequiredTransport) ||
			(legacyCompact && openAICompactSupportTier(account) == 0) ||
			!scheduler.isAccountRequestCompatible(ctx, account, *admissionRequest) {
			return nil, ErrNoAvailableAccounts
		}
	}
	return &AccountSlotAdmission{
		MaxConcurrency: account.Concurrency,
		PressureModel:  normalizeCodexAdaptiveModel(resolveOpenAIAccountUpstreamModelForRequest(account, model, legacyCompact)),
		PressureWindow: codexAdaptivePressureWindow,
	}, nil
}

func boundCodexAdaptiveQueueTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 || timeout > codexAdaptiveQueueTimeout {
		return codexAdaptiveQueueTimeout
	}
	return timeout
}

func (s *OpenAIGatewayService) waitForCodexAffinitySlot(ctx context.Context, selection *AccountSelectionResult) (func(), bool, error) {
	ticker := time.NewTicker(75 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, false, nil
		case <-ticker.C:
			result, err := s.tryAcquireAccountSlot(ctx, selection.Account.ID, selection.WaitPlan.MaxConcurrency)
			if err != nil {
				if ctx.Err() != nil {
					return nil, false, nil
				}
				return nil, false, err
			}
			if result != nil && result.Acquired {
				return result.ReleaseFunc, true, nil
			}
		}
	}
}

func codexAdaptiveRequestFromContext(ctx context.Context) *codexAdaptiveRequestState {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(codexAdaptiveRequestContextKey{}).(*codexAdaptiveRequestState)
	return state
}

func SetCodexAdaptiveTurnModel(ctx context.Context, model string) {
	if state := codexAdaptiveRequestFromContext(ctx); state != nil && model != "" {
		state.mu.Lock()
		state.model = normalizeCodexAdaptiveModel(model)
		state.legacyCompact = false
		clear(state.pressures)
		state.mu.Unlock()
	}
}

// RefreshCodexAdaptivePressureSnapshot starts a new admission snapshot for a
// later WebSocket turn. Policy values remain frozen for the connection, while
// the short-lived Redis pressure window is re-read so an idle connection does
// not retain expired pressure or miss pressure observed by other sessions.
func (s *OpenAIGatewayService) RefreshCodexAdaptivePressureSnapshot(ctx context.Context) {
	state := codexAdaptiveRequestFromContext(ctx)
	if state == nil {
		return
	}
	state.mu.Lock()
	clear(state.pressures)
	state.mu.Unlock()
}

func normalizeCodexAdaptiveModel(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}

func codexAdaptiveAccountEligible(account *Account) bool {
	if account == nil || account.Platform != PlatformOpenAI {
		return false
	}
	if account.IsOpenAIOAuthLike() {
		return true
	}
	if account.Type != AccountTypeAPIKey {
		return false
	}
	parsed, err := url.Parse(strings.TrimSpace(account.GetOpenAIBaseURL()))
	if err != nil || !strings.EqualFold(parsed.Scheme, "https") || !strings.EqualFold(parsed.Hostname(), "api.openai.com") {
		return false
	}
	port := parsed.Port()
	return port == "" || port == "443"
}

func codexAdaptivePressureScopeFor(accountID int64, model string) codexAdaptivePressureScope {
	return codexAdaptivePressureScope{accountID: accountID, model: normalizeCodexAdaptiveModel(model)}
}

func (s *OpenAIGatewayService) codexAdaptivePressure(ctx context.Context, account *Account, model string) int {
	state := codexAdaptiveRequestFromContext(ctx)
	if state == nil || !codexAdaptiveAccountEligible(account) {
		return 0
	}
	state.mu.Lock()
	model = normalizeCodexAdaptiveModel(firstNonEmpty(model, state.model))
	scope := codexAdaptivePressureScopeFor(account.ID, model)
	pressure, ok := state.pressures[scope]
	state.mu.Unlock()
	if ok {
		return pressure
	}
	if s == nil || s.concurrencyService == nil {
		return 0
	}

	redisCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), codexAdaptiveRedisTimeout)
	defer cancel()
	pressures, err := s.concurrencyService.GetCodexAdaptivePressureBatch(redisCtx, []int64{account.ID}, model, codexAdaptivePressureWindow)
	if err != nil {
		slog.Warn("codex_adaptive_pressure_read_failed", "account_id", account.ID, "model", model, "error", err)
		state.mu.Lock()
		state.pressures[scope] = 0
		state.mu.Unlock()
		return 0
	}
	pressure = pressures[account.ID]
	state.mu.Lock()
	state.pressures[scope] = pressure
	state.mu.Unlock()
	return pressure
}

// prefetchCodexAdaptivePressures publishes one immutable pressure snapshot for
// the candidate set into the request state. Accounts are grouped by their
// mapped upstream model so a normal pool selection costs one Redis pipeline,
// rather than one round trip per candidate.
func (s *OpenAIGatewayService) prefetchCodexAdaptivePressures(ctx context.Context, accounts []*Account, requestedModel string) {
	state := codexAdaptiveRequestFromContext(ctx)
	if state == nil || s == nil || s.concurrencyService == nil || len(accounts) == 0 {
		return
	}

	groups := make(map[string][]int64)
	state.mu.Lock()
	legacyCompact := state.legacyCompact
	state.mu.Unlock()
	for _, account := range accounts {
		if !codexAdaptiveAccountEligible(account) {
			continue
		}
		model := normalizeCodexAdaptiveModel(resolveOpenAIAccountUpstreamModelForRequest(account, requestedModel, legacyCompact))
		scope := codexAdaptivePressureScopeFor(account.ID, model)
		state.mu.Lock()
		_, loaded := state.pressures[scope]
		state.mu.Unlock()
		if !loaded {
			groups[model] = append(groups[model], account.ID)
		}
	}
	if len(groups) == 0 {
		return
	}

	redisCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), codexAdaptiveRedisTimeout)
	defer cancel()
	for model, accountIDs := range groups {
		pressures, err := s.concurrencyService.GetCodexAdaptivePressureBatch(
			redisCtx, accountIDs, model, codexAdaptivePressureWindow,
		)
		if err != nil {
			slog.Warn("codex_adaptive_pressure_batch_read_failed", "model", model, "account_count", len(accountIDs), "error", err)
			state.mu.Lock()
			for _, accountID := range accountIDs {
				state.pressures[codexAdaptivePressureScopeFor(accountID, model)] = 0
			}
			state.mu.Unlock()
			continue
		}
		state.mu.Lock()
		for _, accountID := range accountIDs {
			state.pressures[codexAdaptivePressureScopeFor(accountID, model)] = pressures[accountID]
		}
		state.mu.Unlock()
	}
}

func codexAdaptiveConcurrencyLimit(configured, pressure int) int {
	if configured <= 1 || pressure < 2 {
		return configured
	}
	limit := (configured + pressure - 1) / pressure
	if limit < 1 {
		return 1
	}
	return limit
}

func (s *OpenAIGatewayService) codexAdaptiveEffectiveConcurrency(ctx context.Context, account *Account, model string, configured int) int {
	return codexAdaptiveConcurrencyLimit(configured, s.codexAdaptivePressure(ctx, account, model))
}

// CodexAdaptiveEffectiveConcurrency returns the request-scoped admission limit
// for a selected account. Callers that reacquire slots outside the scheduler,
// such as subsequent WebSocket turns, must use the same frozen policy.
func (s *OpenAIGatewayService) CodexAdaptiveEffectiveConcurrency(ctx context.Context, account *Account, model string) int {
	if account == nil {
		return 0
	}
	return s.codexAdaptiveEffectiveConcurrency(ctx, account, model, account.Concurrency)
}

func (s *OpenAIGatewayService) codexAdaptiveEffectiveLoadFactor(ctx context.Context, account *Account, model string) int {
	if account == nil {
		return 0
	}
	loadFactor := account.EffectiveLoadFactor()
	concurrency := s.codexAdaptiveEffectiveConcurrency(ctx, account, model, account.Concurrency)
	if account.Concurrency <= 0 || concurrency <= 0 || concurrency >= account.Concurrency {
		return loadFactor
	}
	if concurrency > 0 && (loadFactor <= 0 || concurrency < loadFactor) {
		return concurrency
	}
	return loadFactor
}

func (s *OpenAIGatewayService) ApplyCodexAdaptiveFailoverPolicy(ctx context.Context, account *Account, model string, failoverErr *UpstreamFailoverError) bool {
	recordOpenAIRequestFailure(ctx, failoverErr)
	state := codexAdaptiveRequestFromContext(ctx)
	if state == nil || failoverErr == nil || !failoverErr.ShouldRetryNextAccount() || !codexAdaptiveAccountEligible(account) || !failoverErr.IsOpenAICapacityShed() {
		return false
	}
	state.mu.Lock()
	model = normalizeCodexAdaptiveModel(firstNonEmpty(model, state.model))
	state.stickyMigrationPending = true
	if state.stickySourceID == 0 {
		state.stickySourceID = account.ID
	}
	state.mu.Unlock()
	// Explicit capacity shedding is commonly request/thread-local, so one quick
	// same-account retry preserves cache affinity before switching accounts.
	failoverErr.RetryableOnSameAccount = true
	failoverErr.RequestScopedTransient = true
	failoverErr.SameAccountRetryMax = 1
	if state.sessionMember == "" || s.concurrencyService == nil {
		return true
	}
	scope := codexAdaptivePressureScopeFor(account.ID, model)
	redisCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), codexAdaptiveRedisTimeout)
	pressure, err := s.concurrencyService.ObserveCodexAdaptiveFailure(
		redisCtx, account.ID, model, state.sessionMember, codexAdaptivePressureWindow,
	)
	cancel()
	if err != nil {
		slog.Warn("codex_adaptive_failure_record_failed", "account_id", account.ID, "model", model, "error", err)
		return true
	}
	state.mu.Lock()
	state.pressures[scope] = pressure
	state.mu.Unlock()
	slog.Info("codex_adaptive_pressure_observed",
		"account_id", account.ID,
		"model", model,
		"independent_sessions", pressure,
		"effective_concurrency", codexAdaptiveConcurrencyLimit(account.Concurrency, pressure),
		"failure_reason", failoverErr.Reason,
	)
	return true
}

// codexAdaptiveStickyMigrationPending prevents failover candidate selection
// from eagerly replacing the durable session binding. The final successful
// account commits the migration instead.
func codexAdaptiveStickyMigrationPending(ctx context.Context) bool {
	state := codexAdaptiveRequestFromContext(ctx)
	if state == nil {
		return false
	}
	state.mu.Lock()
	pending := state.stickyMigrationPending
	state.mu.Unlock()
	return pending
}

// CommitCodexAdaptiveStickyOnSuccess moves a normal sticky session to the
// account that recovered an explicit capacity-shed failover. A Responses
// continuation remains owned by the account that produced its response ID,
// so requests carrying previous_response_id never rewrite the session binding.
func (s *OpenAIGatewayService) CommitCodexAdaptiveStickyOnSuccess(
	ctx context.Context,
	groupID *int64,
	account *Account,
	hasPreviousResponseID bool,
) error {
	state := codexAdaptiveRequestFromContext(ctx)
	if state == nil {
		return nil
	}
	state.mu.Lock()
	pending, source, migration := state.stickyMigrationPending, state.stickySourceID, state.migration
	state.mu.Unlock()
	if !pending || account == nil || account.ID <= 0 {
		return nil
	}
	committed := false
	if !hasPreviousResponseID && state.sessionHash != "" && account.ID != source {
		if migration.TargetID != account.ID {
			return fmt.Errorf("successful account has no matching sticky migration")
		}
		if migration.Version != "" {
			cache, ok := s.cache.(OpenAIStickyMigrationCache)
			if !ok {
				return fmt.Errorf("OpenAI sticky migration store unavailable")
			}
			var err error
			committed, err = cache.CommitOpenAIStickyMigration(ctx, derefGroupID(groupID), s.openAISessionCacheKey(state.sessionHash), migration, s.openAIWSSessionStickyTTL())
			if err != nil {
				return err
			}
		}
	}
	// An in-flight source request may finish after another request claimed a
	// target. Its success must not abort that shared lease or move the binding back.
	FinishCodexAdaptiveSchedulingRequest(ctx)
	state.mu.Lock()
	state.stickyMigrationPending = false
	state.stickySourceID = 0
	state.migration = OpenAIStickyMigration{}
	model := state.model
	state.mu.Unlock()
	if committed {
		slog.Info("codex_adaptive_sticky_migrated", "account_id", account.ID, "model", model)
	}
	return nil
}

func (s *OpenAIGatewayService) ObserveCodexAdaptiveSuccess(ctx context.Context, account *Account, model string) {
	state := codexAdaptiveRequestFromContext(ctx)
	if state == nil || state.sessionMember == "" || !codexAdaptiveAccountEligible(account) || s == nil || s.concurrencyService == nil {
		return
	}
	state.mu.Lock()
	model = normalizeCodexAdaptiveModel(firstNonEmpty(model, state.model))
	scope := codexAdaptivePressureScopeFor(account.ID, model)
	pressure, known := state.pressures[scope]
	state.mu.Unlock()
	if !known || pressure <= 0 {
		return
	}
	redisCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), codexAdaptiveRedisTimeout)
	remainingPressure, err := s.concurrencyService.ObserveCodexAdaptiveSuccess(
		redisCtx, account.ID, model, state.sessionMember, codexAdaptivePressureWindow,
	)
	cancel()
	if err != nil {
		slog.Warn("codex_adaptive_success_record_failed", "account_id", account.ID, "model", model, "error", err)
		return
	}
	state.mu.Lock()
	state.pressures[scope] = remainingPressure
	state.mu.Unlock()
}
