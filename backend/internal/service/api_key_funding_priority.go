package service

import (
	"context"
	"errors"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

func (s *APIKeyService) validateFundingPriority(ctx context.Context, userID int64, source string, primary *int64, ids []int64, fallback bool) error {
	if err := validateFundingSource(source, primary); err != nil {
		return err
	}
	if source != FundingSourceSubscription && (len(ids) != 0 || fallback) {
		return infraerrors.BadRequest("FUNDING_PRIORITY_INVALID", "priority and wallet fallback require subscription payment")
	}
	if len(ids) > 32 {
		return infraerrors.BadRequest("FUNDING_PRIORITY_INVALID", "at most 32 subscriptions may be selected")
	}
	seen := make(map[int64]bool, len(ids))
	for _, id := range ids {
		if id <= 0 || seen[id] {
			return infraerrors.BadRequest("FUNDING_PRIORITY_INVALID", "subscription priority contains invalid or duplicate IDs")
		}
		seen[id] = true
		if _, err := s.validateSubscriptionFunding(ctx, userID, source, &id, true); err != nil {
			return err
		}
	}
	return nil
}

func subscriptionFundingUnavailable(err error) bool {
	return errors.Is(err, ErrSubscriptionNotFound) || errors.Is(err, ErrSubscriptionExpired) ||
		errors.Is(err, ErrSubscriptionSuspended) || errors.Is(err, ErrSubscriptionNotStarted) ||
		errors.Is(err, ErrDailyLimitExceeded) || errors.Is(err, ErrWeeklyLimitExceeded) ||
		errors.Is(err, ErrMonthlyLimitExceeded) || errors.Is(err, ErrTotalLimitExceeded) ||
		errors.Is(err, ErrSubscriptionWindowBusy)
}

// ResolveGatewayFunding returns a request-local key. Cached authentication
// snapshots must never be mutated when a request chooses its funding source.
func (s *APIKeyService) ResolveGatewayFunding(ctx context.Context, key *APIKey) (*APIKey, *UserSubscription, error) {
	return s.resolveGatewayFunding(ctx, key, nil)
}

func (s *APIKeyService) resolveGatewayFunding(ctx context.Context, key *APIKey, excluded map[int64]error) (*APIKey, *UserSubscription, error) {
	if key == nil || !key.UsesSubscription() {
		return key, nil, nil
	}
	if s.userSubRepo == nil {
		return nil, nil, ErrSubscriptionBillingInvalid
	}
	copyKey := *key
	ids := key.SubscriptionPriority
	if len(ids) == 0 && key.SubscriptionID != nil {
		ids = []int64{*key.SubscriptionID}
	}
	lastErr := error(ErrSubscriptionBillingInvalid)
	subs := NewSubscriptionService(s.userSubRepo, nil)
	for _, id := range ids {
		if err := excluded[id]; err != nil {
			lastErr = err
			continue
		}
		sub, err := s.userSubRepo.GetByID(ctx, id)
		if err == nil && (sub == nil || sub.UserID != key.UserID) {
			return nil, nil, ErrSubscriptionBillingInvalid
		}
		if err == nil {
			var maintenance bool
			maintenance, err = subs.ValidateAndCheckLimits(sub)
			if maintenance && err == nil {
				sub, err = subs.EnsureWindowMaintenance(ctx, sub)
				if err == nil {
					_, err = subs.ValidateAndCheckLimits(sub)
				}
			}
		}
		if err == nil {
			copyKey.SubscriptionID, copyKey.Subscription = &sub.ID, sub
			return &copyKey, sub, nil
		}
		if !subscriptionFundingUnavailable(err) {
			return nil, nil, err
		}
		lastErr = err
	}
	if key.AllowWalletFallback {
		copyKey.FundingSource = FundingSourceWallet
		copyKey.SubscriptionID, copyKey.Subscription = nil, nil
		return &copyKey, nil, nil
	}
	return nil, nil, lastErr
}

// PreauthorizeWithFunding only advances payment sources before forwarding.
// A successful reservation fixes the funding identity for the entire request.
func (s *BalancePreauthorizationService) PreauthorizeWithFunding(ctx context.Context, request BalancePreauthorizationRequest, key *APIKey, subscription *UserSubscription) (*BalancePreauthorizationGuard, error) {
	if key == nil || !key.UsesSubscription() || (len(key.SubscriptionPriority) == 0 && !key.AllowWalletFallback) {
		return s.Preauthorize(ctx, request)
	}
	selectedKey, selectedSub := key, subscription
	excluded := make(map[int64]error)
	for {
		request.BillingType = BalancePreauthorizationBillingType(selectedKey, selectedSub)
		request.SubscriptionID = 0
		if selectedKey.UsesSubscription() && selectedKey.SubscriptionID != nil {
			request.SubscriptionID = *selectedKey.SubscriptionID
		}
		request.BaselineKey = BuildBalancePreauthorizationBaselineKey(request.UserID, request.APIKeyID,
			preauthorizationFundingSource(request.BillingType), request.SubscriptionID, request.BaselineSessionID)
		guard, err := s.Preauthorize(ctx, request)
		if err == nil {
			*key = *selectedKey
			if subscription != nil && selectedSub != nil {
				*subscription = *selectedSub
			}
			return guard, nil
		}
		if !selectedKey.UsesSubscription() || !subscriptionFundingUnavailable(err) || s.apiKeyService == nil {
			return nil, err
		}
		excluded[request.SubscriptionID] = err
		selectedKey, selectedSub, err = s.apiKeyService.resolveGatewayFunding(ctx, key, excluded)
		if err != nil {
			return nil, err
		}
	}
}
