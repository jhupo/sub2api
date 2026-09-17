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
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	upstreamStateRefreshTimeout    = 20 * time.Second
	upstreamStateRefreshLockTTL    = 30 * time.Second
	upstreamStateRunnerInterval    = time.Minute
	upstreamStateRunnerLockTTL     = 2 * time.Minute
	upstreamStateRefreshRetryDelay = 5 * time.Minute
	upstreamStateRunnerMaxPerCycle = 4
	upstreamStateErrorBodyLimit    = 64 << 10
)

var ErrUpstreamStateRefreshBusy = errors.New("state refresh is already running for this account and model")

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

func (s *OpenAIGatewayService) replaceManagedUpstreamState(ctx context.Context, scope *upstreamStateScope, state string) (*UpstreamStateActionResult, error) {
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
	current, ticket, err := store.Begin(ctx, scope.record.ID)
	if err != nil {
		return nil, err
	}
	record := buildUpstreamStateObservation(scope.record, state, scope.config, time.Now())
	record.Sequence = ticket.Sequence
	if record.Validation != upstreamStateValidationNormal {
		if current != nil && current.State != "" {
			record.State = ""
		}
		_ = store.Save(ctx, record, ticket)
		return nil, fmt.Errorf("upstream returned %s state (%d bytes, expected %d)", record.Validation, record.ObservedLength, scope.config.ExpectedLength)
	}
	if err = store.Replace(ctx, record, ticket); err != nil {
		return nil, err
	}
	return upstreamStateActionResult(record), nil
}

func (s *OpenAIGatewayService) recordManagedUpstreamStateFailure(ctx context.Context, scope *upstreamStateScope, message string) {
	if scope == nil || scope.record.ID == "" || scope.settings.upstreamStateStore == nil {
		return
	}
	message = strings.TrimSpace(message)
	if len(message) > 300 {
		message = message[:300]
	}
	record := scope.record
	now := time.Now()
	record.CheckedAt = now.UnixMilli()
	record.Validation = "refresh_error"
	record.LastError = message
	record.PurgeAt = now.Add(upstreamStateObservationTTL).UnixMilli()
	storeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	_, ticket, err := scope.settings.upstreamStateStore.Begin(storeCtx, record.ID)
	if err != nil {
		return
	}
	record.Sequence = ticket.Sequence
	_ = scope.settings.upstreamStateStore.Save(storeCtx, record, ticket)
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
	return s.replaceManagedUpstreamState(ctx, scope, state)
}

// RefreshManagedUpstreamState sends one real SSE request without a prior state.
// Response headers arrive before the SSE body, so the body is closed as soon as
// x-codex-turn-state is captured.
func (s *OpenAIGatewayService) RefreshManagedUpstreamState(ctx context.Context, accountID int64, model string) (*UpstreamStateActionResult, error) {
	account, cfg, err := s.validateManagedUpstreamStatePair(ctx, accountID, model)
	if err != nil {
		return nil, err
	}
	release, err := s.lockManagedUpstreamState(ctx, accountID, model)
	if err != nil {
		return nil, err
	}
	defer release()

	refreshCtx, cancel := context.WithTimeout(ctx, upstreamStateRefreshTimeout)
	defer cancel()
	payload, err := json.Marshal(createOpenAITestPayload(strings.TrimSpace(model), true))
	if err != nil {
		return nil, err
	}
	scope, c, err := s.managedUpstreamStateScope(refreshCtx, account, model, cfg)
	if err != nil {
		return nil, err
	}
	if err = s.prepareCodexAttemptIdentity(refreshCtx, c, account, payload); err != nil {
		return nil, err
	}
	token, _, err := s.GetAccessToken(refreshCtx, account)
	if err != nil {
		s.recordManagedUpstreamStateFailure(refreshCtx, scope, err.Error())
		return nil, err
	}
	req, err := s.buildUpstreamRequest(refreshCtx, c, account, payload, token, true, "", true)
	if err != nil {
		s.recordManagedUpstreamStateFailure(refreshCtx, scope, err.Error())
		return nil, err
	}
	// A refresh request must not echo the currently cached state.
	req.Header.Del(openAICodexTurnStateHeader)
	req = req.WithContext(context.WithValue(req.Context(), upstreamStateContextKey{}, (*upstreamStateScope)(nil)))
	proxyURL := ""
	if cfg.WebshareEnabled {
		proxyURL, err = fetchWebshareRotatingProxyURL(refreshCtx, cfg, upstreamStateWebshareAPIBaseURL, &http.Client{Timeout: 10 * time.Second})
		if err != nil {
			s.recordManagedUpstreamStateFailure(refreshCtx, scope, err.Error())
			return nil, err
		}
	} else if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	resp, err := s.doOpenAIUpstream(req, proxyURL, account)
	if err != nil {
		s.recordManagedUpstreamStateFailure(refreshCtx, scope, err.Error())
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, upstreamStateErrorBodyLimit))
		message := strings.TrimSpace(ExtractUpstreamErrorMessage(body))
		if message == "" {
			message = http.StatusText(resp.StatusCode)
		}
		err = fmt.Errorf("upstream refresh returned HTTP %d: %s", resp.StatusCode, message)
		s.recordManagedUpstreamStateFailure(refreshCtx, scope, err.Error())
		return nil, err
	}
	result, err := s.replaceManagedUpstreamState(refreshCtx, scope, resp.Header.Get(openAICodexTurnStateHeader))
	if err != nil {
		s.recordManagedUpstreamStateFailure(refreshCtx, scope, err.Error())
		return nil, err
	}
	return result, nil
}

func upstreamStateRecordDue(record UpstreamStateRecord, exists bool, now time.Time) bool {
	if !exists {
		return true
	}
	if record.CheckedAt > now.Add(-upstreamStateRefreshRetryDelay).UnixMilli() && (record.State == "" || record.RotationAt <= now.UnixMilli()) {
		return false
	}
	return record.State == "" || record.UpstreamExpiresAt <= now.UnixMilli() || record.RotationAt <= now.UnixMilli()
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
		if upstreamStateRecordDue(record, ok, now) {
			due = append(due, pair)
			if len(due) == limit {
				break
			}
		}
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
		if _, refreshErr := s.RefreshManagedUpstreamState(ctx, pair.AccountID, pair.Model); refreshErr != nil && !errors.Is(refreshErr, ErrUpstreamStateRefreshBusy) {
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
