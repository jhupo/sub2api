package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
	"github.com/gin-gonic/gin"
)

const (
	upstreamStateRefreshTimeout     = 50 * time.Second
	upstreamStatePreparationTimeout = 25 * time.Second
	upstreamStateRequestTimeout     = 20 * time.Second
	upstreamStateRefreshLockTTL     = time.Minute
	upstreamStateRunnerInterval     = time.Minute
	upstreamStateRunnerLockTTL      = 5 * time.Minute
	upstreamStateRunnerMaxPerCycle  = 4
	upstreamStateErrorBodyLimit     = 64 << 10
)

var ErrUpstreamStateRefreshBusy = errors.New("state refresh is already running for this account and model")
var ErrUpstreamStateRefreshFailed = errors.New("state refresh failed")

type UpstreamStateActionResult struct {
	AccountID         int64  `json:"account_id"`
	Model             string `json:"model"`
	Length            int    `json:"length"`
	Digest            string `json:"digest"`
	IssuedAt          int64  `json:"issued_at"`
	UpstreamExpiresAt int64  `json:"upstream_expires_at"`
	RotationAt        int64  `json:"rotation_at"`
	Validation        string `json:"validation"`
	ExpirySource      string `json:"expiry_source,omitempty"`
}

func (s *OpenAIGatewayService) managedUpstreamStateScope(ctx context.Context, account *Account, model string, cfg UpstreamStateSettings) (*upstreamStateScope, *gin.Context, error) {
	if s == nil || s.settingService == nil || account == nil {
		return nil, nil, errors.New("state management is unavailable")
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, nil, errors.New("model is required")
	}
	c := &gin.Context{Request: &http.Request{Header: make(http.Header), URL: &url.URL{Path: "/v1/responses"}}}
	if err := s.prepareCodexAttemptIdentity(ctx, c, account, nil); err != nil {
		return nil, nil, err
	}
	scope := s.newUpstreamStateScopeWithConfig(c, account, model, "http", cfg)
	if scope == nil || scope.record.ID == "" {
		return nil, nil, errors.New("account has no stable OAuth identity for state management")
	}
	return scope, c, nil
}

func (s *OpenAIGatewayService) validateManagedUpstreamStatePair(ctx context.Context, accountID int64, model string) (*Account, UpstreamStateSettings, error) {
	if s == nil || s.settingService == nil || s.accountRepo == nil {
		return nil, UpstreamStateSettings{}, errors.New("state management is unavailable")
	}
	cfg, err := s.settingService.GetUpstreamStateSettings(ctx)
	if err != nil {
		return nil, cfg, err
	}
	model = strings.TrimSpace(model)
	if !cfg.manages(accountID, model) {
		return nil, cfg, errors.New("state management is not enabled for this account and model")
	}
	account, err := s.accountRepo.GetByID(ctx, accountID)
	if err != nil {
		return nil, cfg, err
	}
	if account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth {
		return nil, cfg, errors.New("only OpenAI OAuth accounts are supported")
	}
	return account, cfg, nil
}

func (s *OpenAIGatewayService) lockManagedUpstreamState(ctx context.Context, accountID int64, model string) (func(), error) {
	store := s.settingService.upstreamStateStore
	if store == nil {
		return nil, errors.New("state cache is unavailable")
	}
	key := upstreamStateDigest(fmt.Sprintf("pair:%d:%s", accountID, strings.TrimSpace(model)))
	owner, acquired, err := store.TryLock(ctx, key, upstreamStateRefreshLockTTL)
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, ErrUpstreamStateRefreshBusy
	}
	return func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = store.Unlock(unlockCtx, key, owner)
	}, nil
}

func upstreamStateActionResult(record UpstreamStateRecord) *UpstreamStateActionResult {
	return &UpstreamStateActionResult{
		AccountID: record.AccountID, Model: record.Model, Length: len(record.State),
		Digest: upstreamStateDigest(record.State)[:12], IssuedAt: record.IssuedAt,
		UpstreamExpiresAt: record.UpstreamExpiresAt, RotationAt: record.RotationAt,
		Validation: record.Validation, ExpirySource: record.ExpirySource,
	}
}

func (s *OpenAIGatewayService) replaceManagedUpstreamState(ctx context.Context, scope *upstreamStateScope, state string, ticket UpstreamStateTicket) (*UpstreamStateActionResult, error) {
	store := s.settingService.upstreamStateStore
	currentConfig, err := scope.settings.GetUpstreamStateSettings(ctx)
	if err != nil {
		return nil, err
	}
	if !currentConfig.manages(scope.record.AccountID, scope.record.Model) ||
		currentConfig.StateRevision != scope.config.StateRevision ||
		currentConfig.pairRevision(scope.record.AccountID, scope.record.Model) != scope.record.PairRevision {
		return nil, ErrUpstreamStateReplaceRejected
	}
	record := buildUpstreamStateObservation(scope.record, state, currentConfig, time.Now())
	record.Sequence = ticket.Sequence
	record.LastRefreshAt = record.CheckedAt
	if record.Validation != upstreamStateValidationNormal {
		return nil, fmt.Errorf("upstream returned %s state (%d bytes, expected %d)", record.Validation, record.ObservedLength, scope.config.ExpectedLength)
	}
	if err = store.Replace(ctx, record, ticket); err != nil {
		return nil, err
	}
	return upstreamStateActionResult(record), nil
}

func (s *OpenAIGatewayService) recordManagedUpstreamStateFailure(ctx context.Context, scope *upstreamStateScope, ticket UpstreamStateTicket, message string, observedLength int) {
	if scope == nil || scope.record.ID == "" || scope.settings.upstreamStateStore == nil {
		return
	}
	record := scope.record
	now := time.Now()
	record.CheckedAt = now.UnixMilli()
	record.LastRefreshAt = record.CheckedAt
	record.Validation = "refresh_error"
	record.ObservedLength = observedLength
	record.LastError = message
	record.PurgeAt = now.Add(upstreamStateRecordRetention).UnixMilli()
	storeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	record.Sequence = ticket.Sequence
	if err := scope.settings.upstreamStateStore.Save(storeCtx, record, ticket); err != nil {
		slog.WarnContext(ctx, "upstream state refresh result could not be saved", "account_id", record.AccountID, "model", record.Model)
	}
}

func managedUpstreamStateErrorMessage(err error, secrets ...string) string {
	message := err.Error()
	for _, secret := range secrets {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	message = logredact.RedactText(sanitizeErrorMessage(message), "authorization", "state", "x-codex-turn-state", "api_key")
	runes := []rune(strings.Join(strings.Fields(message), " "))
	if len(runes) > 300 {
		runes = append(runes[:300], '…')
	}
	return string(runes)
}

// SetManagedUpstreamState validates and atomically replaces the state for one
// enabled OAuth account/model pair. The raw token never leaves this service.
func (s *OpenAIGatewayService) SetManagedUpstreamState(ctx context.Context, accountID int64, model, state string) (*UpstreamStateActionResult, error) {
	account, cfg, err := s.validateManagedUpstreamStatePair(ctx, accountID, model)
	if err != nil {
		return nil, err
	}
	release, err := s.lockManagedUpstreamState(ctx, accountID, model)
	if err != nil {
		return nil, err
	}
	defer release()
	scope, _, err := s.managedUpstreamStateScope(ctx, account, model, cfg)
	if err != nil {
		return nil, err
	}
	_, ticket, err := s.settingService.upstreamStateStore.Begin(ctx, scope.record.ID)
	if err != nil {
		return nil, err
	}
	return s.replaceManagedUpstreamState(ctx, scope, state, ticket)
}

// RefreshManagedUpstreamState sends one real SSE request without a prior state.
// Response headers arrive before the SSE body, so the body is closed as soon as
// x-codex-turn-state is captured.
func (s *OpenAIGatewayService) RefreshManagedUpstreamState(ctx context.Context, accountID int64, model string) (*UpstreamStateActionResult, error) {
	return s.refreshManagedUpstreamState(ctx, accountID, model, false)
}

func (s *OpenAIGatewayService) refreshManagedUpstreamState(ctx context.Context, accountID int64, model string, automatic bool) (result *UpstreamStateActionResult, err error) {
	account, cfg, err := s.validateManagedUpstreamStatePair(ctx, accountID, model)
	if err != nil {
		return nil, err
	}
	release, err := s.lockManagedUpstreamState(ctx, accountID, model)
	if err != nil {
		return nil, err
	}
	defer release()

	refreshCtx, finish := context.WithTimeout(ctx, upstreamStateRefreshTimeout)
	defer finish()
	payload, err := json.Marshal(createOpenAITestPayload(strings.TrimSpace(model), true))
	if err != nil {
		return nil, err
	}
	scope, c, err := s.managedUpstreamStateScope(refreshCtx, account, model, cfg)
	if err != nil {
		return nil, err
	}
	// Take the cache epoch before sending: a clear while the request is in
	// flight must reject both its replacement and its failure observation.
	current, ticket, err := s.settingService.upstreamStateStore.Begin(refreshCtx, scope.record.ID)
	if err != nil {
		return nil, err
	}
	// Recheck under the pair lock: a manual operation may have replaced this
	// state while an earlier account/model was being processed by the runner.
	if automatic && (!cfg.AutoReplaceEnabled || (current != nil && !upstreamStateRecordDue(*current, true, cfg, time.Now()))) {
		return nil, nil
	}
	stage, egress := "access_token", "direct"
	status, observedLength := 0, 0
	var token, proxyURL, upstreamRequestID string
	secrets := []string{cfg.WebshareAPIKey}
	defer func() {
		if err == nil || errors.Is(err, ErrUpstreamStateReplaceRejected) {
			return
		}
		message := managedUpstreamStateErrorMessage(err, append(secrets, token, proxyURL)...)
		err = fmt.Errorf("%w (%s): %s", ErrUpstreamStateRefreshFailed, stage, message)
		s.recordManagedUpstreamStateFailure(ctx, scope, ticket, err.Error(), observedLength)
		slog.WarnContext(ctx, "upstream state refresh failed", "account_id", accountID, "model", model,
			"stage", stage, "egress", egress, "upstream_status", status,
			"observed_length", observedLength, "upstream_request_id", upstreamRequestID,
			"request_id", ctx.Value(ctxkey.RequestID), "error", err.Error())
	}()
	// Credential and proxy lookup have their own budget, separate from SSE.
	preparationCtx, finishPreparation := context.WithTimeout(refreshCtx, upstreamStatePreparationTimeout)
	defer finishPreparation()
	token, _, err = s.GetAccessToken(preparationCtx, account)
	if err != nil {
		return nil, err
	}
	if cfg.WebshareEnabled {
		stage, egress = "webshare_lookup", "webshare"
		proxyURL, err = fetchWebshareRotatingProxyURL(preparationCtx, cfg, upstreamStateWebshareAPIBaseURL, &http.Client{Timeout: 10 * time.Second})
		if err != nil {
			return nil, err
		}
	} else if account.ProxyID != nil && account.Proxy != nil {
		egress = "account_proxy"
		proxyURL = account.Proxy.URL()
	}
	if proxy, parseErr := url.Parse(proxyURL); parseErr == nil && proxy.User != nil {
		password, _ := proxy.User.Password()
		secrets = append(secrets, proxy.User.String(), password)
	}
	if err = preparationCtx.Err(); err != nil {
		return nil, err
	}
	finishPreparation()
	// Cancel the SSE as soon as headers arrive without cancelling persistence.
	networkCtx, cancel := context.WithTimeout(refreshCtx, upstreamStateRequestTimeout)
	defer cancel()
	stage = "build_request"
	req, err := s.buildUpstreamRequest(networkCtx, c, account, payload, token, true, "", true)
	if err != nil {
		return nil, err
	}
	// A refresh request must not echo the currently cached state.
	req.Header.Del(openAICodexTurnStateHeader)
	// Avoid reading a compression header just to inspect response headers.
	req.Header.Set("Accept-Encoding", "identity")
	req = req.WithContext(context.WithValue(req.Context(), upstreamStateContextKey{}, (*upstreamStateScope)(nil)))
	stage = "request"
	resp, err := s.doOpenAIUpstream(req, proxyURL, account)
	if err != nil {
		return nil, err
	}
	status = resp.StatusCode
	upstreamRequestID = resp.Header.Get("X-Request-Id")
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		stage = "response_status"
		body, _ := io.ReadAll(io.LimitReader(resp.Body, upstreamStateErrorBodyLimit))
		cancel()
		_ = resp.Body.Close()
		message := strings.TrimSpace(ExtractUpstreamErrorMessage(body))
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		err = fmt.Errorf("upstream refresh returned HTTP %d: %s", resp.StatusCode, message)
		return nil, err
	}
	state := resp.Header.Get(openAICodexTurnStateHeader)
	observedLength = len(strings.TrimSpace(state))
	cancel()
	_ = resp.Body.Close()
	stage = "state_header"
	if observedLength == 0 {
		return nil, fmt.Errorf("upstream returned HTTP %d without X-Codex-Turn-State; no replacement was saved", status)
	}
	stage = "validate_and_store"
	return s.replaceManagedUpstreamState(refreshCtx, scope, state, ticket)
}

func upstreamStateRecordDue(record UpstreamStateRecord, exists bool, cfg UpstreamStateSettings, now time.Time) bool {
	if !exists {
		return true
	}
	rotationAt := upstreamStateRotationAt(record, cfg)
	// Back off failed attempts and successful acquisitions already inside the
	// rotation window. Do not let a long retry setting postpone a healthy
	// token's scheduled rotation or expiry.
	needsBackoff := record.LastError != "" || record.State == "" || record.AcquiredAt >= rotationAt
	if needsBackoff && record.LastRefreshAt > now.Add(-time.Duration(cfg.RetryIntervalMinutes)*time.Minute).UnixMilli() {
		return false
	}
	return record.State == "" || record.UpstreamExpiresAt <= now.UnixMilli() || rotationAt <= now.UnixMilli()
}

func dueManagedUpstreamStatePairs(cfg UpstreamStateSettings, records []UpstreamStateRecord, now time.Time, limit int) []UpstreamStatePair {
	if !cfg.Enabled || !cfg.AutoReplaceEnabled || len(cfg.Pairs) == 0 || limit <= 0 {
		return nil
	}
	latest := make(map[UpstreamStatePair]UpstreamStateRecord, len(records))
	for _, record := range records {
		pair := UpstreamStatePair{AccountID: record.AccountID, Model: record.Model}
		if record.Revision != cfg.StateRevision || record.PairRevision != cfg.pairRevision(pair.AccountID, pair.Model) {
			continue
		}
		if previous, ok := latest[pair]; !ok || record.CheckedAt > previous.CheckedAt {
			latest[pair] = record
		}
	}
	due := make([]UpstreamStatePair, 0, min(len(cfg.Pairs), limit))
	for _, pair := range cfg.Pairs {
		record, ok := latest[pair]
		if upstreamStateRecordDue(record, ok, cfg, now) {
			due = append(due, pair)
		}
	}
	// Unattempted/oldest pairs go first, so repeated failures near the front
	// of the configured list cannot starve accounts beyond this cycle's limit.
	sort.SliceStable(due, func(i, j int) bool {
		return latest[due[i]].LastRefreshAt < latest[due[j]].LastRefreshAt
	})
	if len(due) > limit {
		due = due[:limit]
	}
	return due
}

func (s *OpenAIGatewayService) runDueManagedUpstreamStates(ctx context.Context) {
	if s == nil || s.settingService == nil || s.settingService.upstreamStateStore == nil {
		return
	}
	cfg, err := s.settingService.GetUpstreamStateSettings(ctx)
	if err != nil || !cfg.Enabled || !cfg.AutoReplaceEnabled || len(cfg.Pairs) == 0 {
		return
	}
	store := s.settingService.upstreamStateStore
	owner, acquired, err := store.TryLock(ctx, "runner", upstreamStateRunnerLockTTL)
	if err != nil || !acquired {
		return
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = store.Unlock(unlockCtx, "runner", owner)
	}()
	records, err := store.List(ctx)
	if err != nil {
		return
	}
	due := dueManagedUpstreamStatePairs(cfg, records, time.Now(), upstreamStateRunnerMaxPerCycle)
	for _, pair := range due {
		if ctx.Err() != nil {
			return
		}
		current, settingsErr := s.settingService.GetUpstreamStateSettings(ctx)
		if settingsErr != nil || !current.Enabled || !current.AutoReplaceEnabled {
			return
		}
		if _, refreshErr := s.refreshManagedUpstreamState(ctx, pair.AccountID, pair.Model, true); refreshErr != nil && !errors.Is(refreshErr, ErrUpstreamStateRefreshBusy) && !errors.Is(refreshErr, ErrUpstreamStateRefreshFailed) {
			slog.Warn("upstream state replacement failed", "account_id", pair.AccountID, "model", pair.Model, "error", refreshErr)
		}
	}
}

func (s *OpenAIGatewayService) StartManagedUpstreamStateRunner() {
	if s == nil {
		return
	}
	s.upstreamStateRunnerMu.Lock()
	defer s.upstreamStateRunnerMu.Unlock()
	if s.upstreamStateRunnerCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.upstreamStateRunnerCancel = cancel
	s.upstreamStateRunnerWG.Add(1)
	go func() {
		defer s.upstreamStateRunnerWG.Done()
		ticker := time.NewTicker(upstreamStateRunnerInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.runDueManagedUpstreamStates(ctx)
			}
		}
	}()
}

func (s *OpenAIGatewayService) StopManagedUpstreamStateRunner() {
	if s == nil {
		return
	}
	s.upstreamStateRunnerMu.Lock()
	cancel := s.upstreamStateRunnerCancel
	s.upstreamStateRunnerCancel = nil
	s.upstreamStateRunnerMu.Unlock()
	if cancel != nil {
		cancel()
		s.upstreamStateRunnerWG.Wait()
	}
}
