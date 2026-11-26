package service

import (
	"context"
	"fmt"
	"time"
)

// PriorityFundingPolicy is frozen for one request, including asynchronous work.
type PriorityFundingPolicy struct {
	SubscriptionIDs []int64 `json:"subscription_ids"`
	WalletFallback  bool    `json:"wallet_fallback"`
}

type FundingAllocation struct {
	SubscriptionID int64      `json:"subscription_id"`
	Amount         float64    `json:"amount"`
	DailyStart     *time.Time `json:"daily_start,omitempty"`
	WeeklyStart    *time.Time `json:"weekly_start,omitempty"`
	MonthlyStart   *time.Time `json:"monthly_start,omitempty"`
}

type PriorityFundingCommand struct {
	RequestID, AuthorizationFingerprint, BaselineKey string
	APIKeyID, UserID, PrimarySubscriptionID          int64
	Policy                                           PriorityFundingPolicy
	Amount                                           float64
	ExpiresAt                                        time.Time
}

type PriorityFundingReservation struct {
	PriorityFundingCommand
	Allocations        []FundingAllocation
	Status             string
	ActualAmount       float64
	WalletAmount       float64
	UncoveredAmount    float64
	RequestFingerprint string
	WalletRecord       *BalancePreauthorizationRecord
	UpdatedAt          time.Time
}

type PriorityFundingRepository interface {
	ReservePriorityFunding(context.Context, *PriorityFundingCommand) (*PriorityFundingReservation, error)
	GetPriorityFunding(context.Context, string, int64) (*PriorityFundingReservation, error)
	ReleasePriorityFunding(context.Context, string, int64) error
	ListRecoverablePriorityFunding(context.Context, time.Time, int) ([]PriorityFundingReservation, error)
}

func PriorityWalletRequestID(requestID string) string {
	return "priority-wallet:" + HashUsageRequestPayload([]byte(requestID))
}

func priorityFundingPolicy(key *APIKey) PriorityFundingPolicy {
	policy := PriorityFundingPolicy{SubscriptionIDs: append([]int64(nil), key.SubscriptionPriority...), WalletFallback: key.AllowWalletFallback}
	if len(policy.SubscriptionIDs) == 0 && key.SubscriptionID != nil {
		policy.SubscriptionIDs = []int64{*key.SubscriptionID}
	}
	return policy
}

type priorityFundingReservation struct {
	service *BalancePreauthorizationService
	repo    PriorityFundingRepository
	cmd     PriorityFundingCommand
	wallet  *BalancePreauthorizationGuard
}

func (r *priorityFundingReservation) FundingSource() string  { return FundingSourceSubscription }
func (r *priorityFundingReservation) SubscriptionID() *int64 { return &r.cmd.PrimarySubscriptionID }

func (r *priorityFundingReservation) TopUp(ctx context.Context, target float64) error {
	cmd := r.cmd
	cmd.Amount = target
	record, err := r.repo.ReservePriorityFunding(ctx, &cmd)
	if err != nil {
		return err
	}
	if record.WalletAmount <= 0 {
		return nil
	}
	if r.wallet == nil {
		latest, err := r.repo.GetPriorityFunding(ctx, cmd.RequestID, cmd.APIKeyID)
		if err != nil {
			return err
		}
		if latest != nil && latest.WalletRecord != nil && latest.WalletRecord.Status == BalanceSettlementAuthorized {
			wallet := latest.WalletRecord
			r.wallet, err = r.service.Resume(ctx, BalancePreauthorizationResumeRequest{
				RequestID: wallet.RequestID, APIKeyID: wallet.APIKeyID, UserID: wallet.UserID,
				BillingType: BillingTypeBalance, AuthorizationFingerprint: wallet.AuthorizationFingerprint,
				BaselineKey: wallet.BaselineKey, HoldAmount: wallet.HoldAmount,
			})
			if err != nil {
				return err
			}
		}
	}
	if r.wallet != nil {
		return r.wallet.TopUpTo(ctx, record.WalletAmount)
	}
	amount := record.WalletAmount
	walletRequest := BalancePreauthorizationRequest{
		RequestID: PriorityWalletRequestID(cmd.RequestID), UserID: cmd.UserID, APIKeyID: cmd.APIKeyID,
		AuthorizationFingerprint: cmd.AuthorizationFingerprint, BillingType: BillingTypeBalance,
		BaselineKey: cmd.BaselineKey + ":wallet", ExpiresAt: record.ExpiresAt,
		fundingHoldAmount: &amount,
	}
	r.wallet, err = r.service.Preauthorize(ctx, walletRequest)
	return err
}

func (r *priorityFundingReservation) Capture(ctx context.Context, _, actual float64, fingerprint string) error {
	record, err := r.repo.GetPriorityFunding(ctx, r.cmd.RequestID, r.cmd.APIKeyID)
	if err != nil {
		return err
	}
	if record == nil || record.Status != BillingReservationCaptured ||
		record.RequestFingerprint != fingerprint || record.ActualAmount != actual {
		return ErrUsageBillingRequestConflict
	}
	if record.WalletRecord != nil && record.WalletRecord.Status == BalanceSettlementFinalizationPending {
		return r.service.RecoverBalancePreauthorization(ctx, *record.WalletRecord)
	}
	return nil
}

func (r *priorityFundingReservation) Release(ctx context.Context) error {
	if err := r.repo.ReleasePriorityFunding(ctx, r.cmd.RequestID, r.cmd.APIKeyID); err != nil {
		return err
	}
	if r.wallet != nil {
		return r.wallet.Refund(ctx)
	}
	record, err := r.repo.GetPriorityFunding(ctx, r.cmd.RequestID, r.cmd.APIKeyID)
	if err != nil {
		return err
	}
	if record != nil && record.WalletRecord != nil && (record.WalletRecord.Status == BalanceSettlementPrepared || record.WalletRecord.Status == BalanceSettlementAuthorized) {
		return r.service.RecoverBalancePreauthorization(ctx, *record.WalletRecord)
	}
	return nil
}

func (s *BalancePreauthorizationService) resumePriorityFunding(ctx context.Context, request BalancePreauthorizationResumeRequest, repo PriorityFundingRepository) (*BalancePreauthorizationGuard, bool, error) {
	record, err := repo.GetPriorityFunding(ctx, request.RequestID, request.APIKeyID)
	if err != nil || record == nil {
		return nil, false, err
	}
	if record.UserID != request.UserID || record.PrimarySubscriptionID != request.SubscriptionID ||
		record.AuthorizationFingerprint != request.AuthorizationFingerprint || record.Amount != request.HoldAmount {
		return nil, true, ErrUsageBillingRequestConflict
	}
	if record.Status == BillingReservationCaptured {
		return nil, true, ErrBalancePreauthorizationAlreadyFinalized
	}
	if record.Status == BillingReservationReleased {
		return nil, true, ErrBalancePreauthorizationAlreadyRefunded
	}
	if !record.ExpiresAt.After(time.Now()) {
		return nil, true, ErrUsageBillingRequestConflict
	}
	reservation := &priorityFundingReservation{service: s, repo: repo, cmd: record.PriorityFundingCommand}
	return &BalancePreauthorizationGuard{core: &balancePreauthorizationGuardCore{
		reservation: reservation, requestID: request.RequestID, apiKeyID: request.APIKeyID,
		holdAmount: record.Amount, ownerToken: 1,
	}, ownerToken: 1}, true, nil
}

func (s *BalancePreauthorizationService) preauthorizePriorityFunding(ctx context.Context, request BalancePreauthorizationRequest, key *APIKey, repo PriorityFundingRepository) (*BalancePreauthorizationGuard, error) {
	if !s.RequiresPreauthorization(ctx, request.BillingType) {
		return nil, nil
	}
	if err := validateBalancePreauthorizationRequest(&request); err != nil {
		return nil, err
	}
	baselineKey := BuildBalancePreauthorizationBaselineKey(request.UserID, request.APIKeyID, "priority", 0, request.BaselineSessionID)
	estimate := balancePreauthorizationEstimate{}
	if store, ok := s.repo.(balancePreauthorizationBaselineStore); ok {
		amount, err := store.LoadPreauthorizationBaseline(ctx, baselineKey)
		if err != nil {
			return nil, err
		}
		if invalidNonnegativeMoney(amount) {
			return nil, ErrInvalidBillingPreauthorizationEstimate
		}
		estimate.HoldAmount = QuantizeUsageBillingAmount(amount)
	} else {
		var err error
		estimate, err = s.estimateHold(ctx, request)
		if err != nil {
			return nil, err
		}
	}
	reservation := &priorityFundingReservation{service: s, repo: repo, cmd: PriorityFundingCommand{
		RequestID: request.RequestID, APIKeyID: request.APIKeyID, UserID: request.UserID,
		PrimarySubscriptionID: request.SubscriptionID, AuthorizationFingerprint: request.AuthorizationFingerprint,
		BaselineKey: baselineKey, Policy: priorityFundingPolicy(key), ExpiresAt: request.ExpiresAt,
	}}
	if err := reservation.TopUp(ctx, estimate.HoldAmount); err != nil {
		cleanupCtx, cancel := detachedBillingContext(ctx)
		defer cancel()
		if releaseErr := reservation.Release(cleanupCtx); releaseErr != nil {
			return nil, fmt.Errorf("priority funding authorization: %w; release: %v", err, releaseErr)
		}
		return nil, err
	}
	core := &balancePreauthorizationGuardCore{reservation: reservation, requestID: request.RequestID,
		apiKeyID: request.APIKeyID, holdAmount: estimate.HoldAmount, ownerToken: 1}
	if !s.UsesHistoricalPreauthorization() {
		core.outputWindow = estimate.OutputWindow
		core.outputHoldTracker = NewBillingOutputHoldTracker(estimate.OutputWindow, estimate.OutputWindow, estimate.HoldAmount, estimate.OutputUnitPrice, 1)
	}
	return &BalancePreauthorizationGuard{core: core, ownerToken: 1}, nil
}
