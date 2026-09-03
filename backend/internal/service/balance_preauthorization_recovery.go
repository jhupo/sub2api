package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// RecoverBalancePreauthorization resumes one durable nonterminal record. It is
// safe to call concurrently across instances because every Redis operation and
// PG transition is idempotent. Callers should source records from
// ListRecoverableBalancePreauthorizations so active requests are not raced.
func (s *BalancePreauthorizationService) RecoverBalancePreauthorization(
	ctx context.Context,
	record BalancePreauthorizationRecord,
) error {
	if s == nil || s.wallet == nil || s.repo == nil {
		return balancePreauthorizationUnavailable(errors.New("balance preauthorization recovery dependency is unavailable"))
	}
	if strings.TrimSpace(record.RequestID) == "" || record.APIKeyID <= 0 || record.UserID <= 0 ||
		record.HoldAmount < 0 || record.Amount < 0 ||
		math.IsNaN(record.HoldAmount) || math.IsInf(record.HoldAmount, 0) ||
		math.IsNaN(record.Amount) || math.IsInf(record.Amount, 0) {
		return balancePreauthorizationUnavailable(ErrInvalidBillingPreauthorizationEstimate)
	}
	ctx = nonNilContext(ctx)
	switch record.Status {
	case BalanceSettlementPrepared:
		if err := s.repo.BeginBalancePreauthorizationRefund(ctx, record.RequestID, record.APIKeyID); err != nil {
			return balancePreauthorizationUnavailable(err)
		}
		return s.recoverBalancePreauthorizationRefund(ctx, record)
	case BalanceSettlementAuthorized:
		if IsGrokVideoHoldRequestID(record.RequestID) && strings.TrimSpace(record.AsyncTaskID) != "" && !record.ExpiresAt.IsZero() && !record.ExpiresAt.After(time.Now()) {
			if err := s.repo.BeginBalancePreauthorizationRefund(ctx, record.RequestID, record.APIKeyID); err != nil {
				return balancePreauthorizationUnavailable(err)
			}
			return s.recoverBalancePreauthorizationRefund(ctx, record)
		}
		// Redis authorization succeeded, but the process may have crashed after
		// returning a successful provider response and before its in-memory usage
		// task reached repo.Apply. The exact spend is unknowable here; refunding
		// would turn that crash window into a free call. Conservatively settle the
		// durable hold. Prepared remains the only state eligible for an abandoned
		// authorization refund.
		record.Amount = QuantizeUsageBillingAmount(record.HoldAmount)
		record.RequestFingerprint = balancePreauthorizationRecoveryHoldFingerprint(record)
		if err := s.repo.BeginBalancePreauthorizationFinalization(
			ctx, record.RequestID, record.APIKeyID, record.Amount, record.RequestFingerprint,
		); err != nil {
			return balancePreauthorizationUnavailable(err)
		}
		if record.Amount == 0 {
			return s.recoverBalancePreauthorizationRefund(ctx, record)
		}
		return s.recoverBalancePreauthorizationSettlement(ctx, record)
	case BalanceSettlementFinalizationPending:
		if QuantizeUsageBillingAmount(record.Amount) == 0 {
			return s.recoverBalancePreauthorizationRefund(ctx, record)
		}
		return s.recoverBalancePreauthorizationSettlement(ctx, record)
	default:
		return balancePreauthorizationUnavailable(fmt.Errorf("unsupported recoverable balance preauthorization status %d", record.Status))
	}
}

func (s *BalancePreauthorizationService) RecoverSubscriptionAllowance(
	ctx context.Context,
	record SubscriptionAllowanceReservation,
) error {
	if s == nil || s.subscriptionRepo == nil {
		return balancePreauthorizationUnavailable(errors.New("subscription allowance recovery dependency is unavailable"))
	}
	cmd := &SubscriptionAllowanceCommand{
		RequestID: record.RequestID, APIKeyID: record.APIKeyID, UserID: record.UserID,
		SubscriptionID: record.SubscriptionID, AuthorizationFingerprint: record.AuthorizationFingerprint,
		Amount: record.AuthorizedAmount, AuthorizedAt: record.UpdatedAt, ExpiresAt: record.ExpiresAt,
	}
	switch record.Status {
	case BillingReservationAuthorized:
		if IsGrokVideoHoldRequestID(record.RequestID) && strings.TrimSpace(record.AsyncTaskID) != "" {
			cmd.Amount = 0
			_, err := s.subscriptionRepo.ReleaseSubscriptionAllowance(ctx, cmd)
			return err
		}
		cmd.Amount = record.AuthorizedAmount
		fingerprint := subscriptionAllowanceRecoveryFingerprint(record)
		_, err := s.subscriptionRepo.CaptureSubscriptionAllowance(ctx, cmd, fingerprint)
		return err
	case BillingReservationFinalizing:
		if record.CapturedAmount < 0 || strings.TrimSpace(record.RequestFingerprint) == "" {
			return balancePreauthorizationUnavailable(ErrInvalidBillingPreauthorizationEstimate)
		}
		cmd.Amount = record.CapturedAmount
		_, err := s.subscriptionRepo.CaptureSubscriptionAllowance(ctx, cmd, record.RequestFingerprint)
		return err
	default:
		return balancePreauthorizationUnavailable(fmt.Errorf("unsupported recoverable subscription reservation status %s", record.Status))
	}
}

func subscriptionAllowanceRecoveryFingerprint(record SubscriptionAllowanceReservation) string {
	cmd := &UsageBillingCommand{
		UserID: record.UserID, APIKeyID: record.APIKeyID,
		SubscriptionID: &record.SubscriptionID, BillingType: BillingTypeSubscription,
		SubscriptionCost:   record.AuthorizedAmount,
		RequestPayloadHash: "authorized-subscription-hold-recovery:v1:" + strings.TrimSpace(record.RequestID) + ":" + strings.TrimSpace(record.AuthorizationFingerprint),
	}
	cmd.Normalize()
	return cmd.RequestFingerprint
}

func balancePreauthorizationRecoveryHoldFingerprint(record BalancePreauthorizationRecord) string {
	cmd := &UsageBillingCommand{
		UserID:             record.UserID,
		APIKeyID:           record.APIKeyID,
		BillingType:        BillingTypeBalance,
		BalanceCost:        QuantizeUsageBillingAmount(record.HoldAmount),
		RequestPayloadHash: "authorized-hold-recovery:v1:" + strings.TrimSpace(record.RequestID) + ":" + strings.TrimSpace(record.AuthorizationFingerprint),
	}
	cmd.Normalize()
	return cmd.RequestFingerprint
}

func (s *BalancePreauthorizationService) recoverBalancePreauthorizationRefund(
	ctx context.Context,
	record BalancePreauthorizationRecord,
) error {
	result, err := s.wallet.RefundLiveBalance(
		ctx,
		record.UserID,
		BalancePreauthorizationAttemptID(record.RequestID, record.APIKeyID),
	)
	if err != nil {
		return balancePreauthorizationUnavailable(err)
	}
	// NotFound is safe here: live attempts no longer expire. It means no hold was
	// ever created (prepared crash) or Redis lost the whole wallet, while PG has
	// not deducted this request's amount.
	if result.Outcome != LiveBalanceOutcomeNotFound && !liveBalanceRefundSucceeded(result) {
		return balancePreauthorizationUnavailable(fmt.Errorf("recover refund returned outcome=%d state=%d", result.Outcome, result.State))
	}
	if err := s.repo.CompleteBalancePreauthorizationRefund(ctx, record.RequestID, record.APIKeyID); err != nil {
		return balancePreauthorizationUnavailable(err)
	}
	s.cleanupLiveBalanceAttempt(ctx, record.UserID, BalancePreauthorizationAttemptID(record.RequestID, record.APIKeyID))
	return nil
}

func (s *BalancePreauthorizationService) recoverBalancePreauthorizationSettlement(
	ctx context.Context,
	record BalancePreauthorizationRecord,
) error {
	actual := QuantizeUsageBillingAmount(record.Amount)
	if strings.TrimSpace(record.RequestFingerprint) == "" {
		return balancePreauthorizationUnavailable(ErrInvalidBillingPreauthorizationEstimate)
	}
	result, err := s.wallet.FinalizeLiveBalance(
		ctx,
		record.UserID,
		BalancePreauthorizationAttemptID(record.RequestID, record.APIKeyID),
		actual,
	)
	if err != nil {
		return balancePreauthorizationUnavailable(err)
	}
	if !liveBalanceFinalizationSucceeded(result, actual) {
		return balancePreauthorizationUnavailable(fmt.Errorf("recover settlement returned outcome=%d state=%d", result.Outcome, result.State))
	}
	if err := s.repo.CompleteBalancePreauthorizationSettlement(ctx, record.RequestID, record.APIKeyID); err != nil {
		return balancePreauthorizationUnavailable(err)
	}
	s.cleanupLiveBalanceAttempt(ctx, record.UserID, BalancePreauthorizationAttemptID(record.RequestID, record.APIKeyID))
	return nil
}
