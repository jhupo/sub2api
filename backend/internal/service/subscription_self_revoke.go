package service

import (
	"context"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/billingreservation"
	"github.com/Wei-Shaw/sub2api/ent/paymentorder"
	"github.com/Wei-Shaw/sub2api/ent/usersubscription"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

var ErrSubscriptionSelfRevoke = infraerrors.Conflict("SUBSCRIPTION_SELF_REVOKE_UNAVAILABLE", "only an exhausted, unrenewed subscription without pending requests or renewal orders can be relinquished")

func (s *UserSubscription) CanSelfRevokeAt(now time.Time) bool {
	if s == nil || s.IsQuotaCard || s.Plan == nil || s.DeletedAt != nil || s.RenewalCount != 0 ||
		s.Status != SubscriptionStatusActive || now.Before(s.StartsAt) || !now.Before(s.ExpiresAt) ||
		s.TotalReservedUSD != 0 || s.DailyReservedUSD != 0 || s.WeeklyReservedUSD != 0 || s.MonthlyReservedUSD != 0 {
		return false
	}
	transition, err := s.AllowanceWindowTransitionAt(now)
	if err != nil {
		return false
	}
	return (!transition.ResetDaily && s.Plan.DailyLimitUSD != nil && s.DailyUsageUSD >= *s.Plan.DailyLimitUSD) ||
		(!transition.ResetWeekly && s.Plan.WeeklyLimitUSD != nil && s.WeeklyUsageUSD >= *s.Plan.WeeklyLimitUSD) ||
		(!transition.ResetMonthly && s.Plan.MonthlyLimitUSD != nil && s.MonthlyUsageUSD >= *s.Plan.MonthlyLimitUSD)
}

func (s *SubscriptionService) SelfRevokeSubscription(ctx context.Context, userID, id int64) error {
	if s.entClient == nil {
		return ErrSubscriptionSelfRevoke
	}
	initial, err := s.userSubRepo.GetByID(ctx, id)
	if err != nil || initial.UserID != userID {
		return ErrSubscriptionNotFound
	}
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	txCtx := dbent.NewTxContext(ctx, tx)
	if err := lockSubscriptionAssignment(txCtx, userID, initial.PlanVersionID); err != nil {
		return err
	}
	if _, err := tx.UserSubscription.Query().Where(usersubscription.IDEQ(id), usersubscription.UserIDEQ(userID), usersubscription.DeletedAtIsNil()).ForUpdate().Only(txCtx); err != nil {
		return ErrSubscriptionNotFound
	}
	sub, err := s.userSubRepo.GetByID(txCtx, id)
	if err != nil {
		return err
	}
	if !sub.CanSelfRevokeAt(s.currentTime()) {
		return ErrSubscriptionSelfRevoke
	}
	busy, err := tx.BillingReservation.Query().Where(billingreservation.SubscriptionIDEQ(id), billingreservation.StatusIn(BillingReservationAuthorized, BillingReservationFinalizing)).Exist(txCtx)
	if err != nil {
		return err
	}
	if busy {
		return ErrSubscriptionSelfRevoke
	}
	pending, err := tx.PaymentOrder.Query().Where(paymentorder.UserIDEQ(userID), paymentorder.PlanIDEQ(sub.PlanID), paymentorder.OrderTypeEQ("subscription"), paymentorder.Or(paymentorder.StatusIn(OrderStatusPaid, OrderStatusRecharging), paymentorder.And(paymentorder.StatusEQ(OrderStatusPending), paymentorder.ExpiresAtGT(s.currentTime())))).Exist(txCtx)
	if err != nil {
		return err
	}
	if pending {
		return ErrSubscriptionSelfRevoke
	}
	now := s.currentTime()
	if _, err := tx.UserSubscription.UpdateOneID(id).SetDeletedAt(now).SetStatus(SubscriptionStatusRevoked).Save(txCtx); err != nil {
		return err
	}
	// Pause dependent keys rather than silently changing the customer's payment source.
	if _, err := tx.Client().ExecContext(txCtx, `UPDATE api_keys SET status = 'inactive', updated_at = NOW()
		WHERE user_id = $1 AND deleted_at IS NULL AND funding_source = 'subscription'
		AND (subscription_id = $2 OR subscription_priority @> jsonb_build_array($2::bigint))`, userID, id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
	}
	return nil
}
