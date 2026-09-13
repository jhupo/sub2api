package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
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
	if record.BaselineKey == "" {
		record.BaselineKey = BuildBalancePreauthorizationBaselineKey(record.UserID, record.APIKeyID,
			FundingSourceWallet, 0, "")
	}
	switch record.Status {
	case BalanceSettlementPrepared:
		if err := s.repo.BeginBalancePreauthorizationRefund(ctx, record.RequestID, record.APIKeyID); err != nil {
			return balancePreauthorizationUnavailable(err)
		}
		return s.recoverBalancePreauthorizationRefund(ctx, record)
	case BalanceSettlementAuthorized:
		// Authorization proves only that funds were reserved, not that the
		// provider ran. Only repo.Apply's finalization state contains actual
		// consumption. Never manufacture usage from an abandoned estimate.
		if err := s.repo.BeginBalancePreauthorizationRefund(ctx, record.RequestID, record.APIKeyID); err != nil {
			return balancePreauthorizationUnavailable(err)
		}
		if err := s.recoverBalancePreauthorizationRefund(ctx, record); err != nil {
			return err
		}
		slog.WarnContext(ctx, "billing.authorized_hold_released_without_usage",
			"request_id", record.RequestID, "api_key_id", record.APIKeyID,
			"funding_source", FundingSourceWallet, "held_amount", record.HoldAmount)
		return nil
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
		BaselineKey: record.BaselineKey,
		Amount:      record.AuthorizedAmount, AuthorizedAt: record.UpdatedAt, ExpiresAt: record.ExpiresAt,
	}
	if cmd.BaselineKey == "" {
		cmd.BaselineKey = BuildBalancePreauthorizationBaselineKey(record.UserID, record.APIKeyID,
			FundingSourceSubscription, record.SubscriptionID, "")
	}
	switch record.Status {
	case BillingReservationAuthorized:
		cmd.Amount = 0
		if _, err := s.subscriptionRepo.ReleaseSubscriptionAllowance(ctx, cmd); err != nil {
			return err
		}
		slog.WarnContext(ctx, "billing.authorized_hold_released_without_usage",
			"request_id", record.RequestID, "api_key_id", record.APIKeyID,
			"funding_source", FundingSourceSubscription, "held_amount", record.AuthorizedAmount)
		return nil
	case BillingReservationFinalizing:
		if record.CapturedAmount < 0 || record.ActualAmount < record.CapturedAmount || strings.TrimSpace(record.RequestFingerprint) == "" {
			return balancePreauthorizationUnavailable(ErrInvalidBillingPreauthorizationEstimate)
		}
		cmd.Amount = record.CapturedAmount
		cmd.ActualAmount = &record.ActualAmount
		_, err := s.subscriptionRepo.CaptureSubscriptionAllowance(ctx, cmd, record.RequestFingerprint)
		if err != nil {
			return err
		}
		if store, ok := s.repo.(balancePreauthorizationBaselineStore); ok && cmd.BaselineKey != "" {
			if err := store.RecordPreauthorizationBaseline(ctx, cmd.BaselineKey, record.ActualAmount); err != nil {
				slog.WarnContext(ctx, "billing.preauthorization_baseline_update_failed",
					"request_id", record.RequestID, "baseline_key", cmd.BaselineKey, "error", err)
			}
		}
		return nil
	default:
		return balancePreauthorizationUnavailable(fmt.Errorf("unsupported recoverable subscription reservation status %s", record.Status))
	}
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
	if record.BaselineKey != "" {
		if store, ok := s.repo.(balancePreauthorizationBaselineStore); ok {
			if err := store.RecordPreauthorizationBaseline(ctx, record.BaselineKey, actual); err != nil {
				slog.WarnContext(ctx, "billing.preauthorization_baseline_update_failed",
					"request_id", record.RequestID, "baseline_key", record.BaselineKey, "error", err)
			}
		}
	}
	s.cleanupLiveBalanceAttempt(ctx, record.UserID, BalancePreauthorizationAttemptID(record.RequestID, record.APIKeyID))
	return nil
}
