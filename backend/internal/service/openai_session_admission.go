package service

import (
	"context"
	"errors"
	"time"
)

type openAIContinuationSelectionKey struct{}
type openAIImmediateSelectionKey struct{}

func (s *OpenAIGatewayService) newSessionSoftLimitPercent(ctx context.Context, req OpenAIAccountScheduleRequest) int {
	if s.cfg == nil || s.concurrencyService == nil || NormalizeOpenAICompatiblePlatform(req.Platform) != PlatformOpenAI ||
		req.StickyAccountID != 0 || req.StickyPreviousAccountID != 0 || req.GuardianParentAccountID != 0 ||
		req.PreviousResponseID != "" || req.PreserveStickyBinding || req.StickyMigrationTarget {
		return 0
	}
	if continuation, _ := ctx.Value(openAIContinuationSelectionKey{}).(bool); continuation {
		return 0
	}
	if codexAdaptiveStickyMigrationPending(ctx) {
		return 0
	}
	percent := s.cfg.Gateway.Scheduling.NewSessionSoftLimitPercent
	if percent <= 0 || percent >= 100 {
		return 0
	}
	return percent
}

// Try soft admission across the eligible pool before spending hard-limit
// headroom. A separate probe budget ensures a failed soft pass cannot consume
// the hard fallback's budget. Redis, not the load snapshot, decides admission.
func (s *defaultOpenAIAccountScheduler) tryAcquireOpenAINewSession(
	ctx context.Context, req OpenAIAccountScheduleRequest, accounts []*Account, loads map[int64]*AccountLoadInfo,
) (*AccountSelectionResult, error) {
	percent := s.service.newSessionSoftLimitPercent(ctx, req)
	if percent == 0 {
		return nil, nil
	}
	softCtx := context.WithValue(ctx, accountSoftAdmissionKey{}, percent)
	pools := [][]*Account{accounts}
	if req.SubscriptionPriority {
		subscriptions, regular := partitionOpenAIChatGPTSubscriptionAccounts(accounts)
		pools = [][]*Account{subscriptions, regular}
	}
	for _, pool := range pools {
		plan := s.buildOpenAIAccountLoadPlan(softCtx, req, pool, loads)
		result, _, err := s.tryAcquireOpenAISelectionOrderInBatches(softCtx, req, plan.selectionOrder)
		if result != nil || err != nil {
			return result, err
		}
	}
	return nil, nil
}

// Keep the bounded queue on the affinity account, but periodically look for
// immediately usable capacity. Never queue on a second account while holding
// the first account's wait entry. Selection owns migration coordination.
func (s *OpenAIGatewayService) waitForOpenAIPoolCapacity(
	ctx context.Context, selection *AccountSelectionResult,
	trySpare func(context.Context) (*AccountSelectionResult, error),
) (*AccountSelectionResult, error) {
	plan := selection.WaitPlan
	deadline := time.Now().Add(plan.Timeout)
	probeSpare := func() (*AccountSelectionResult, error) {
		probeCtx, stopProbe := context.WithDeadline(context.WithValue(ctx, openAIImmediateSelectionKey{}, true), deadline)
		defer stopProbe()
		spare, err := trySpare(probeCtx)
		if errors.Is(err, ErrNoAvailableAccounts) || errors.Is(err, ErrNoAvailableCompactAccounts) ||
			(errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if spare == nil || !spare.Acquired {
			return nil, nil
		}
		if state := codexAdaptiveRequestFromContext(ctx); state != nil {
			state.mu.Lock()
			if state.stickySourceID > 0 && spare.Account.ID != state.stickySourceID {
				state.stickyMigrationPending = true
			}
			state.mu.Unlock()
		}
		return spare, nil
	}
	canWait, err := s.concurrencyService.IncrementAccountWaitCount(ctx, plan.AccountID, plan.MaxWaiting)
	if err != nil {
		return nil, err
	}
	if !canWait {
		if spare, err := probeSpare(); err != nil || spare != nil {
			return spare, err
		}
		return selection, nil
	}
	defer s.concurrencyService.DecrementAccountWaitCount(ctx, plan.AccountID)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			// The handler may still win its final immediate attempt, but must not
			// start another full wait after the service has spent this budget.
			plan.Timeout = time.Nanosecond
			return selection, nil
		}
		waitCtx, cancel := context.WithTimeout(ctx, min(750*time.Millisecond, remaining))
		release, acquired, waitErr := s.waitForCodexAffinitySlot(waitCtx, selection)
		cancel()
		if waitErr != nil && !errors.Is(waitErr, ErrNoAvailableAccounts) {
			return nil, waitErr
		}
		if acquired {
			selection.Acquired, selection.ReleaseFunc, selection.WaitPlan = true, release, nil
			return selection, nil
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if time.Until(deadline) <= 0 {
			continue
		}
		if spare, err := probeSpare(); err != nil || spare != nil {
			return spare, err
		}
	}
}
