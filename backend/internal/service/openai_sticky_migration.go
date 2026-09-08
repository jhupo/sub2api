package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
)

const openAIStickyMigrationTTL = 90 * time.Second

type codexStickySelectionSnapshotKey struct{}
type codexStickySelectionSnapshot struct {
	groupID   int64
	session   string
	accountID int64
}

type OpenAIStickyMigration struct {
	SourceID int64
	TargetID int64
	Version  string
	Model    string
}

// This port coordinates only failover migration. Ordinary request execution
// stays concurrent, and response-ID ownership remains independent of affinity.
type OpenAIStickyMigrationCache interface {
	SetOpenAIStickySessionIfAbsent(context.Context, int64, string, int64, time.Duration) error
	GetOpenAIStickyMigration(context.Context, int64, string) (*OpenAIStickyMigration, error)
	ClaimOpenAIStickyMigration(context.Context, int64, string, OpenAIStickyMigration, time.Duration) (*OpenAIStickyMigration, error)
	RefreshOpenAIStickyMigration(context.Context, int64, string, string, time.Duration) (bool, error)
	AbortOpenAIStickyMigration(context.Context, int64, string, string) error
	CommitOpenAIStickyMigration(context.Context, int64, string, OpenAIStickyMigration, time.Duration) (bool, error)
}

func (s *OpenAIGatewayService) coordinateCodexStickySelection(ctx context.Context, req OpenAIAccountScheduleRequest, selection *AccountSelectionResult) (*AccountSelectionResult, error) {
	state := codexAdaptiveRequestFromContext(ctx)
	if state == nil || selection == nil || selection.Account == nil || req.PreviousResponseID != "" {
		return selection, nil
	}
	state.mu.Lock()
	pending, source := state.stickyMigrationPending, state.stickySourceID
	state.mu.Unlock()
	if !pending || source <= 0 || state.sessionHash == "" {
		return selection, nil
	}
	release := func() {
		if selection != nil && selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
	}
	cache, ok := s.cache.(OpenAIStickyMigrationCache)
	if !ok {
		release()
		return nil, errors.New("OpenAI sticky migration store unavailable")
	}
	key, group := s.openAISessionCacheKey(state.sessionHash), derefGroupID(req.GroupID)
	proposal := OpenAIStickyMigration{SourceID: source, TargetID: selection.Account.ID, Version: uuid.NewString(), Model: normalizeCodexAdaptiveModel(req.RequestedModel)}
	for attempt := 0; attempt < 3; attempt++ {
		migration, err := cache.ClaimOpenAIStickyMigration(ctx, group, key, proposal, openAIStickyMigrationTTL)
		if err != nil {
			release()
			return nil, err
		}
		if migration.Version != "" && migration.Model != proposal.Model {
			// A simultaneous model switch may need a different account. Do not
			// invalidate another model's migration or commit this temporary route.
			FinishCodexAdaptiveSchedulingRequest(ctx)
			state.mu.Lock()
			state.stickyMigrationPending = false
			state.stickySourceID = 0
			state.migration = OpenAIStickyMigration{}
			state.mu.Unlock()
			return selection, nil
		}
		if _, excluded := req.ExcludedIDs[migration.TargetID]; excluded {
			if migration.Version == "" {
				release()
				return nil, ErrNoAvailableAccounts
			}
			if err := cache.AbortOpenAIStickyMigration(ctx, group, key, migration.Version); err != nil {
				release()
				return nil, err
			}
			continue
		}
		if migration.TargetID != selection.Account.ID {
			release()
			req.StickyAccountID, req.PreserveStickyBinding = migration.TargetID, true
			req.StickyMigrationTarget = true
			scheduler := &defaultOpenAIAccountScheduler{service: s, stats: newOpenAIAccountRuntimeStats()}
			selection, _, err = scheduler.selectBySessionHash(ctx, req)
			if err != nil || selection == nil {
				if migration.Version != "" {
					_ = cache.AbortOpenAIStickyMigration(ctx, group, key, migration.Version)
				}
				if err == nil {
					err = ErrNoAvailableAccounts
				}
				return nil, err
			}
		}
		state.mu.Lock()
		state.stickySourceID = migration.SourceID
		state.migration = *migration
		state.mu.Unlock()
		if migration.Version != "" {
			s.keepCodexMigrationAlive(ctx, state, cache, group, key, migration.Version)
		}
		if selection.WaitPlan != nil {
			selection.WaitPlan.Timeout = boundCodexAdaptiveQueueTimeout(selection.WaitPlan.Timeout)
		}
		return selection, nil
	}
	release()
	return nil, fmt.Errorf("%w: session migration changed concurrently", ErrNoAvailableAccounts)
}

func (s *OpenAIGatewayService) keepCodexMigrationAlive(ctx context.Context, state *codexAdaptiveRequestState, cache OpenAIStickyMigrationCache, group int64, key, version string) {
	state.mu.Lock()
	if state.migrationCancel != nil {
		state.migrationCancel()
	}
	leaseCtx, cancel := context.WithCancel(ctx)
	state.migrationCancel = cancel
	state.mu.Unlock()
	go func() {
		ticker := time.NewTicker(openAIStickyMigrationTTL / 3)
		defer ticker.Stop()
		for {
			select {
			case <-leaseCtx.Done():
				return
			case <-ticker.C:
				refreshCtx, stop := context.WithTimeout(leaseCtx, codexAdaptiveRedisTimeout)
				owned, err := cache.RefreshOpenAIStickyMigration(refreshCtx, group, key, version, openAIStickyMigrationTTL)
				stop()
				if err != nil {
					slog.Warn("codex_sticky_migration_refresh_failed", "error", err)
				}
				if err == nil && !owned {
					return
				}
			}
		}
	}()
}

func FinishCodexAdaptiveSchedulingRequest(ctx context.Context) {
	if state := codexAdaptiveRequestFromContext(ctx); state != nil {
		state.mu.Lock()
		if state.migrationCancel != nil {
			state.migrationCancel()
			state.migrationCancel = nil
		}
		state.mu.Unlock()
	}
}
