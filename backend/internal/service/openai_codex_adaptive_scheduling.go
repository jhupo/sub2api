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
)

const CodexFirstOutputTimeoutReason = GatewayFailureReason("codex_first_output_timeout")

type codexAdaptiveRequestState struct {
	apiKeyID      int64
	sessionHash   string
	model         string
	compact       bool
	settings      CodexAdaptiveSchedulingSettings
	sessionMember string

	mu        sync.Mutex
	pressures map[codexAdaptivePressureScope]int
}

type codexAdaptiveRequestContextKey struct{}

type codexAdaptivePressureScope struct {
	accountID int64
	model     string
}

// PrepareCodexAdaptiveSchedulingRequest freezes the runtime policy for one
// request. This prevents a settings refresh in the middle of a failover loop
// from changing its timeout or admission semantics.
func (s *OpenAIGatewayService) PrepareCodexAdaptiveSchedulingRequest(
	ctx context.Context,
	apiKeyID int64,
	sessionHash string,
	model string,
	compact bool,
) context.Context {
	if ctx == nil || s == nil || s.settingService == nil {
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
		compact:     compact,
		settings:    settings,
		pressures:   make(map[codexAdaptivePressureScope]int),
	}
	if state.apiKeyID > 0 && state.sessionHash != "" {
		digest := sha256.Sum256([]byte(fmt.Sprintf("%d|%s", state.apiKeyID, state.sessionHash)))
		state.sessionMember = fmt.Sprintf("%x", digest[:])
	}
	return context.WithValue(ctx, codexAdaptiveRequestContextKey{}, state)
}

func codexAdaptiveRequestFromContext(ctx context.Context) *codexAdaptiveRequestState {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(codexAdaptiveRequestContextKey{}).(*codexAdaptiveRequestState)
	return state
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

func codexAdaptiveFirstOutputTimeout(ctx context.Context, account *Account, model, reasoningEffort string) time.Duration {
	state := codexAdaptiveRequestFromContext(ctx)
	if state == nil || state.compact || !codexAdaptiveAccountEligible(account) {
		return 0
	}
	seconds := state.settings.NormalFirstOutputTimeoutSeconds
	switch strings.ToLower(strings.TrimSpace(reasoningEffort)) {
	case "high", "xhigh", "max", "ultra":
		seconds = state.settings.HighEffortFirstOutputTimeoutSeconds
	}
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func codexAdaptiveWSFirstOutputTimeout(ctx context.Context, account *Account, payload []byte, model, reasoningEffort string) time.Duration {
	if HasCompactionTriggerInInput(payload) {
		return 0
	}
	return codexAdaptiveFirstOutputTimeout(ctx, account, model, reasoningEffort)
}

func (s *OpenAIGatewayService) codexAdaptivePressure(ctx context.Context, account *Account, model string) int {
	state := codexAdaptiveRequestFromContext(ctx)
	if state == nil || !codexAdaptiveAccountEligible(account) {
		return 0
	}
	model = normalizeCodexAdaptiveModel(firstNonEmpty(model, state.model))
	scope := codexAdaptivePressureScopeFor(account.ID, model)
	state.mu.Lock()
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
	for _, account := range accounts {
		if !codexAdaptiveAccountEligible(account) {
			continue
		}
		model := normalizeCodexAdaptiveModel(canonicalOpenAIAccountSchedulingModel(account, requestedModel))
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
	state := codexAdaptiveRequestFromContext(ctx)
	if state == nil || failoverErr == nil || !codexAdaptiveAccountEligible(account) {
		return false
	}
	capacityShed := failoverErr.IsOpenAICapacityShed()
	if !capacityShed && (state.compact || failoverErr.Reason != CodexFirstOutputTimeoutReason) {
		return false
	}
	// Explicit capacity shedding is commonly request/thread-local, so one quick
	// same-account retry preserves cache affinity. A first-output timeout has
	// already consumed the configured wait budget; repeating that full wait on
	// the same account only inflates user-visible TTFT, so it switches accounts.
	failoverErr.RetryableOnSameAccount = capacityShed
	failoverErr.RequestScopedTransient = true
	if capacityShed {
		failoverErr.SameAccountRetryMax = 1
	} else {
		failoverErr.SameAccountRetryMax = 0
	}
	if state.sessionMember == "" || s.concurrencyService == nil {
		return true
	}
	model = normalizeCodexAdaptiveModel(firstNonEmpty(model, state.model))
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

func (s *OpenAIGatewayService) ObserveCodexAdaptiveSuccess(ctx context.Context, account *Account, model string) {
	state := codexAdaptiveRequestFromContext(ctx)
	if state == nil || state.sessionMember == "" || !codexAdaptiveAccountEligible(account) || s == nil || s.concurrencyService == nil {
		return
	}
	model = normalizeCodexAdaptiveModel(firstNonEmpty(model, state.model))
	scope := codexAdaptivePressureScopeFor(account.ID, model)
	state.mu.Lock()
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
