package service

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"
)

// RepriceBeforeSend extends an unsent/retry turn without releasing its hold.
// Callers must never reuse this operation after exposing billable output.
func (s *BalancePreauthorizationService) RepriceBeforeSend(ctx context.Context, guard *BalancePreauthorizationGuard, request BalancePreauthorizationRequest) error {
	if guard == nil || guard.core == nil {
		return ErrInvalidBillingPreauthorizationEstimate
	}
	_, historical := s.repo.(balancePreauthorizationBaselineStore)
	estimate := balancePreauthorizationEstimate{}
	if !historical {
		if err := validateBalancePreauthorizationRequest(&request); err != nil {
			return err
		}
		var err error
		estimate, err = s.estimateHold(ctx, request)
		if err != nil {
			return err
		}
	}
	core := guard.core
	core.mu.Lock()
	defer core.mu.Unlock()
	if core.ownerToken != guard.ownerToken {
		return ErrBalancePreauthorizationOwnershipTransferred
	}
	if core.terminalState != balancePreauthorizationGuardActive {
		return ErrBalancePreauthorizationAlreadyFinalized
	}
	if core.requestID != request.RequestID || core.apiKeyID != request.APIKeyID {
		return ErrInvalidBillingPreauthorizationEstimate
	}
	if historical {
		// The initial hold is the previous actual amount. Repricing a retry must
		// not parse the payload or create a second token-based hold.
		return nil
	}
	if err := guard.topUpToLocked(ctx, estimate.HoldAmount); err != nil {
		return err
	}
	core.outputWindow = estimate.OutputWindow
	core.outputHoldTracker = NewBillingOutputHoldTracker(
		estimate.OutputWindow, estimate.OutputWindow, core.holdAmount, estimate.OutputUnitPrice, 1,
	)
	return nil
}

func openAIContinuationBudgetKey(keyID int64, responseID string) string {
	return fmt.Sprintf("openai:continuation-budget:%d:%s", keyID, HashUsageRequestPayload([]byte(responseID)))
}

// Only numeric context bounds are cached, never prompts or credentials. Scope
// by API key so a response ID from another customer cannot authorize a turn.
func (s *OpenAIGatewayService) OpenAIContinuationInputBound(ctx context.Context, key *APIKey, responseID string) (int, bool, error) {
	if s == nil || s.cache == nil || key == nil {
		return 0, false, nil
	}
	ctx, cancel := withOpenAIWSStateStoreRedisTimeout(ctx)
	defer cancel()
	n, err := s.cache.GetSessionAccountID(ctx, derefGroupID(key.GroupID), openAIContinuationBudgetKey(key.ID, responseID))
	if err != nil {
		return 0, false, err
	}
	if n <= 0 || n > math.MaxInt {
		return 0, false, nil
	}
	return int(n - 1), true, nil
}

func (s *OpenAIGatewayService) RememberOpenAIContinuationInputBound(ctx context.Context, key *APIKey, responseID string, tokens int) error {
	if s == nil || s.cache == nil || key == nil || strings.TrimSpace(responseID) == "" || tokens < 0 || tokens >= math.MaxInt {
		return nil
	}
	ctx, cancel := withOpenAIWSStateStoreRedisTimeout(ctx)
	defer cancel()
	return s.cache.SetSessionAccountID(ctx, derefGroupID(key.GroupID), openAIContinuationBudgetKey(key.ID, responseID), int64(tokens)+1, time.Hour)
}
