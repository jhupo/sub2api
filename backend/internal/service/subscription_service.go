package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"entgo.io/ent/dialect"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/shopspring/decimal"
)

var MaxExpiresAt = time.Date(2099, 12, 31, 23, 59, 59, 0, time.UTC)

const MaxValidityDays = 36500

var (
	ErrSubscriptionNotFound        = infraerrors.NotFound("SUBSCRIPTION_NOT_FOUND", "subscription not found")
	ErrSubscriptionExpired         = infraerrors.Forbidden("SUBSCRIPTION_EXPIRED", "subscription has expired")
	ErrSubscriptionSuspended       = infraerrors.Forbidden("SUBSCRIPTION_SUSPENDED", "subscription is suspended")
	ErrSubscriptionNotStarted      = infraerrors.Forbidden("SUBSCRIPTION_NOT_STARTED", "subscription has not started")
	ErrSubscriptionBillingInvalid  = infraerrors.Forbidden("SUBSCRIPTION_INVALID", "subscription billing requires a matching subscription")
	ErrSubscriptionAlreadyExists   = infraerrors.Conflict("SUBSCRIPTION_ALREADY_EXISTS", "subscription already exists for this user and plan")
	ErrSubscriptionAssignConflict  = infraerrors.Conflict("SUBSCRIPTION_ASSIGN_CONFLICT", "subscription exists but request conflicts with the current assignment")
	ErrSubscriptionNotRevoked      = infraerrors.Conflict("SUBSCRIPTION_NOT_REVOKED", "subscription is not revoked")
	ErrSubscriptionRestoreConflict = infraerrors.Conflict("SUBSCRIPTION_RESTORE_CONFLICT", "subscription already exists for this user and plan")
	ErrSubscriptionNilInput        = infraerrors.BadRequest("SUBSCRIPTION_NIL_INPUT", "subscription is required")
	ErrInvalidInput                = infraerrors.BadRequest("INVALID_INPUT", "at least one quota window must be selected")
	ErrDailyLimitExceeded          = infraerrors.TooManyRequests("DAILY_LIMIT_EXCEEDED", "daily usage limit exceeded")
	ErrWeeklyLimitExceeded         = infraerrors.TooManyRequests("WEEKLY_LIMIT_EXCEEDED", "weekly usage limit exceeded")
	ErrMonthlyLimitExceeded        = infraerrors.TooManyRequests("MONTHLY_LIMIT_EXCEEDED", "monthly usage limit exceeded")
	ErrSubscriptionWindowBusy      = infraerrors.ServiceUnavailable("SUBSCRIPTION_WINDOW_BUSY", "subscription quota window is waiting for an earlier request to settle")
	ErrAdjustWouldExpire           = infraerrors.Conflict("SUBSCRIPTION_ADJUST_WOULD_EXPIRE", "subscription adjustment would expire the entitlement")
)

// SubscriptionService owns entitlement lifecycle and allowance-window state.
// Routing groups are intentionally absent from this service.
type SubscriptionService struct {
	userSubRepo UserSubscriptionRepository
	entClient   *dbent.Client
	// now is injectable for deterministic quota-window tests; production uses time.Now.
	now func() time.Time
}

func NewSubscriptionService(userSubRepo UserSubscriptionRepository, entClient *dbent.Client) *SubscriptionService {
	return &SubscriptionService{userSubRepo: userSubRepo, entClient: entClient, now: time.Now}
}

func (s *SubscriptionService) currentTime() time.Time {
	if s != nil && s.now != nil {
		return s.now()
	}
	return time.Now()
}

type AssignSubscriptionInput struct {
	UserID        int64
	PlanID        int64
	PlanVersionID int64
	ValidityDays  int
	AssignedBy    *int64
	Notes         string
	StartsAt      *time.Time
	ExpiresAt     *time.Time
	PlanVersion   *SubscriptionPlan
}

type BulkAssignSubscriptionInput struct {
	UserIDs       []int64
	PlanID        int64
	PlanVersionID int64
	ValidityDays  int
	AssignedBy    *int64
	Notes         string
}

type BulkAssignResult struct {
	SuccessCount  int
	CreatedCount  int
	ReusedCount   int
	FailedCount   int
	Subscriptions []UserSubscription
	Errors        []string
	Statuses      map[int64]string
}

func (s *SubscriptionService) resolvePlanVersion(ctx context.Context, input *AssignSubscriptionInput) (*SubscriptionPlan, error) {
	if input == nil {
		return nil, ErrSubscriptionNilInput
	}
	if input.PlanVersion != nil {
		return input.PlanVersion, nil
	}
	if s.entClient == nil {
		return nil, infraerrors.InternalServer("PLAN_STORE_UNAVAILABLE", "subscription plan store is unavailable")
	}
	configService := &PaymentConfigService{entClient: s.entClient}
	if input.PlanVersionID > 0 {
		return configService.GetPlanVersion(ctx, input.PlanVersionID)
	}
	if input.PlanID <= 0 {
		return nil, infraerrors.BadRequest("PLAN_REQUIRED", "subscription plan is required")
	}
	return configService.GetPlan(ctx, input.PlanID)
}

func normalizedSubscriptionTerm(input *AssignSubscriptionInput, plan *SubscriptionPlan, now time.Time) (time.Time, time.Time, error) {
	startsAt := now
	if input.StartsAt != nil {
		startsAt = *input.StartsAt
	}
	if input.ExpiresAt != nil {
		if !input.ExpiresAt.After(startsAt) {
			return time.Time{}, time.Time{}, infraerrors.BadRequest("INVALID_EXPIRY", "subscription expiry must be after its start")
		}
		return startsAt, *input.ExpiresAt, nil
	}
	days := input.ValidityDays
	if days <= 0 {
		days = psComputeValidityDays(plan.ValidityDays, plan.ValidityUnit)
	}
	if days <= 0 || days > MaxValidityDays {
		return time.Time{}, time.Time{}, infraerrors.BadRequest("INVALID_VALIDITY", "subscription validity is out of range")
	}
	expiresAt := startsAt.AddDate(0, 0, days)
	if expiresAt.After(MaxExpiresAt) {
		expiresAt = MaxExpiresAt
	}
	return startsAt, expiresAt, nil
}

func (s *SubscriptionService) AssignSubscription(ctx context.Context, input *AssignSubscriptionInput) (*UserSubscription, error) {
	sub, reused, err := s.assignOrExtendSubscription(ctx, input)
	if err != nil {
		return nil, err
	}
	if reused {
		return nil, ErrSubscriptionAlreadyExists
	}
	return sub, nil
}

func (s *SubscriptionService) AssignOrExtendSubscription(ctx context.Context, input *AssignSubscriptionInput) (*UserSubscription, bool, error) {
	return s.assignOrExtendSubscription(ctx, input)
}

func (s *SubscriptionService) assignOrExtendSubscription(ctx context.Context, input *AssignSubscriptionInput) (*UserSubscription, bool, error) {
	if input == nil || input.UserID <= 0 {
		return nil, false, infraerrors.BadRequest("INVALID_INPUT", "user and subscription plan are required")
	}
	// Assignment is used by payment fulfillment and code redemption, both of
	// which may already carry an Ent transaction. Direct admin/auth assignment
	// must get the same transaction boundary; otherwise two concurrent purchases
	// can both read the old expiry and one renewal is lost.
	if s.entClient != nil && dbent.TxFromContext(ctx) == nil {
		tx, err := s.entClient.Tx(ctx)
		if err != nil {
			return nil, false, fmt.Errorf("begin subscription assignment transaction: %w", err)
		}
		defer func() { _ = tx.Rollback() }()
		txCtx := dbent.NewTxContext(ctx, tx)
		sub, reused, err := s.assignOrExtendSubscriptionInTx(txCtx, input)
		if err != nil {
			return nil, false, err
		}
		if err := tx.Commit(); err != nil {
			return nil, false, fmt.Errorf("commit subscription assignment transaction: %w", err)
		}
		return sub, reused, nil
	}
	return s.assignOrExtendSubscriptionInTx(ctx, input)
}

func (s *SubscriptionService) assignOrExtendSubscriptionInTx(ctx context.Context, input *AssignSubscriptionInput) (*UserSubscription, bool, error) {
	plan, err := s.resolvePlanVersion(ctx, input)
	if err != nil {
		return nil, false, err
	}
	if input.PlanID > 0 && input.PlanID != plan.ID {
		return nil, false, infraerrors.BadRequest("PLAN_VERSION_MISMATCH", "plan version does not belong to the selected plan")
	}
	if err := lockSubscriptionAssignment(ctx, input.UserID, plan.PublishedVersionID); err != nil {
		return nil, false, err
	}
	now := s.currentTime()
	startsAt, expiresAt, err := normalizedSubscriptionTerm(input, plan, now)
	if err != nil {
		return nil, false, err
	}
	existing, err := s.userSubRepo.GetByUserIDAndPlanVersionID(ctx, input.UserID, plan.PublishedVersionID)
	if err != nil && !errors.Is(err, ErrSubscriptionNotFound) {
		return nil, false, err
	}
	if existing == nil || errors.Is(err, ErrSubscriptionNotFound) {
		sub := &UserSubscription{
			UserID: input.UserID, PlanID: plan.ID, PlanVersionID: plan.PublishedVersionID,
			StartsAt: startsAt, ExpiresAt: expiresAt, Status: SubscriptionStatusActive,
			AssignedBy: input.AssignedBy, AssignedAt: now, Notes: input.Notes, Plan: plan,
		}
		if err := s.userSubRepo.Create(ctx, sub); err != nil {
			return nil, false, err
		}
		return sub, false, nil
	}
	if existing.Status == SubscriptionStatusSuspended {
		return nil, true, ErrSubscriptionAssignConflict
	}
	// A purchased plan version is an immutable entitlement snapshot. Only an
	// entitlement for the exact same version can be extended; a newer catalog
	// version creates a separate subscription and leaves prior usage untouched.
	existing.Plan = plan
	if existing.ExpiresAt.After(now) {
		duration := expiresAt.Sub(startsAt)
		existing.ExpiresAt = existing.ExpiresAt.Add(duration)
	} else {
		existing.StartsAt = startsAt
		existing.ExpiresAt = expiresAt
		existing.DailyWindowStart = nil
		existing.WeeklyWindowStart = nil
		existing.MonthlyWindowStart = nil
		existing.DailyUsageUSD, existing.WeeklyUsageUSD, existing.MonthlyUsageUSD = 0, 0, 0
		existing.DailyReservedUSD, existing.WeeklyReservedUSD, existing.MonthlyReservedUSD = 0, 0, 0
	}
	existing.Status = SubscriptionStatusActive
	if strings.TrimSpace(input.Notes) != "" {
		existing.Notes = input.Notes
	}
	if err := s.userSubRepo.Update(ctx, existing); err != nil {
		return nil, true, err
	}
	return existing, true, nil
}

// lockSubscriptionAssignment serializes the only operation that cannot be
// protected by a row lock: creating the first entitlement for a user/version.
// The advisory lock is transaction-scoped and therefore released atomically on
// commit or rollback. PostgreSQL is the supported production database, so this
// lock stays in the repository-independent service transaction boundary.
func lockSubscriptionAssignment(ctx context.Context, userID, planVersionID int64) error {
	tx := dbent.TxFromContext(ctx)
	if tx == nil {
		return nil
	}
	client := tx.Client()
	if client.Driver().Dialect() != dialect.Postgres {
		return nil
	}
	if _, err := client.ExecContext(ctx,
		"SELECT pg_advisory_xact_lock($1::bigint, $2::bigint)", userID, planVersionID); err != nil {
		return fmt.Errorf("lock subscription assignment: %w", err)
	}
	return nil
}

func (s *SubscriptionService) BulkAssignSubscription(ctx context.Context, input *BulkAssignSubscriptionInput) (*BulkAssignResult, error) {
	if input == nil || len(input.UserIDs) == 0 {
		return nil, infraerrors.BadRequest("INVALID_INPUT", "at least one user is required")
	}
	result := &BulkAssignResult{Subscriptions: make([]UserSubscription, 0, len(input.UserIDs)), Statuses: make(map[int64]string)}
	for _, userID := range input.UserIDs {
		sub, reused, err := s.AssignOrExtendSubscription(ctx, &AssignSubscriptionInput{
			UserID: userID, PlanID: input.PlanID, PlanVersionID: input.PlanVersionID,
			ValidityDays: input.ValidityDays, AssignedBy: input.AssignedBy, Notes: input.Notes,
		})
		if err != nil {
			result.FailedCount++
			result.Errors = append(result.Errors, fmt.Sprintf("user %d: %v", userID, err))
			result.Statuses[userID] = "failed"
			continue
		}
		result.SuccessCount++
		if reused {
			result.ReusedCount++
			result.Statuses[userID] = "extended"
		} else {
			result.CreatedCount++
			result.Statuses[userID] = "created"
		}
		result.Subscriptions = append(result.Subscriptions, *sub)
	}
	return result, nil
}

func (s *SubscriptionService) RevokeSubscription(ctx context.Context, id int64) error {
	if _, err := s.userSubRepo.GetByID(ctx, id); err != nil {
		return err
	}
	return s.userSubRepo.Delete(ctx, id)
}

func (s *SubscriptionService) RestoreSubscription(ctx context.Context, id int64) (*UserSubscription, error) {
	sub, err := s.userSubRepo.GetByIDIncludeDeleted(ctx, id)
	if err != nil {
		return nil, err
	}
	if sub.DeletedAt == nil {
		return nil, ErrSubscriptionNotRevoked
	}
	exists, err := s.userSubRepo.ExistsActiveByUserIDAndPlanVersionID(ctx, sub.UserID, sub.PlanVersionID)
	if err != nil {
		return nil, err
	}
	if exists {
		return nil, ErrSubscriptionRestoreConflict
	}
	status := sub.Status
	if sub.ExpiresAt.Before(s.currentTime()) {
		status = SubscriptionStatusExpired
	} else if status == SubscriptionStatusRevoked {
		status = SubscriptionStatusActive
	}
	return s.userSubRepo.Restore(ctx, id, status)
}

func (s *SubscriptionService) ExtendSubscription(ctx context.Context, id int64, days int) (*UserSubscription, error) {
	if days == 0 || math.Abs(float64(days)) > MaxValidityDays {
		return nil, infraerrors.BadRequest("INVALID_VALIDITY", "extension days are out of range")
	}
	sub, err := s.userSubRepo.GetByID(ctx, id)
	if err != nil {
		return nil, err
	}
	base := sub.ExpiresAt
	if days > 0 && base.Before(s.currentTime()) {
		base = s.currentTime()
	}
	expiresAt := base.AddDate(0, 0, days)
	if expiresAt.After(MaxExpiresAt) {
		expiresAt = MaxExpiresAt
	}
	if !expiresAt.After(sub.StartsAt) {
		return nil, ErrAdjustWouldExpire
	}
	sub.ExpiresAt = expiresAt
	if expiresAt.After(s.currentTime()) && sub.Status == SubscriptionStatusExpired {
		sub.Status = SubscriptionStatusActive
	}
	if err := s.userSubRepo.Update(ctx, sub); err != nil {
		return nil, err
	}
	return sub, nil
}

func (s *SubscriptionService) GetByID(ctx context.Context, id int64) (*UserSubscription, error) {
	return s.userSubRepo.GetByID(ctx, id)
}

func (s *SubscriptionService) GetActiveSubscription(ctx context.Context, subscriptionID, userID int64) (*UserSubscription, error) {
	sub, err := s.userSubRepo.GetByID(ctx, subscriptionID)
	if err != nil {
		return nil, err
	}
	if sub.UserID != userID {
		return nil, ErrSubscriptionNotFound
	}
	if err := s.ValidateSubscription(ctx, sub); err != nil {
		return nil, err
	}
	return sub, nil
}

func (s *SubscriptionService) ListUserSubscriptions(ctx context.Context, userID int64) ([]UserSubscription, error) {
	rows, err := s.userSubRepo.ListByUserID(ctx, userID)
	if err == nil {
		now := s.currentTime()
		normalizeSubscriptionStatus(rows, now)
		normalizeExpiredWindowsAt(rows, now)
	}
	return rows, err
}

func (s *SubscriptionService) ListActiveUserSubscriptions(ctx context.Context, userID int64) ([]UserSubscription, error) {
	rows, err := s.userSubRepo.ListActiveByUserID(ctx, userID)
	if err == nil {
		normalizeExpiredWindowsAt(rows, s.currentTime())
	}
	return rows, err
}

func (s *SubscriptionService) ListPlanSubscriptions(ctx context.Context, planID int64, page, pageSize int) ([]UserSubscription, *pagination.PaginationResult, error) {
	return s.userSubRepo.ListByPlanID(ctx, planID, pagination.PaginationParams{Page: page, PageSize: pageSize})
}

func (s *SubscriptionService) List(ctx context.Context, page, pageSize int, userID, planID *int64, status, sortBy, sortOrder string) ([]UserSubscription, *pagination.PaginationResult, error) {
	rows, result, err := s.userSubRepo.List(ctx, pagination.PaginationParams{Page: page, PageSize: pageSize}, userID, planID, status, sortBy, sortOrder)
	if err == nil {
		now := s.currentTime()
		normalizeSubscriptionStatus(rows, now)
		normalizeExpiredWindowsAt(rows, now)
	}
	return rows, result, err
}

func normalizeSubscriptionStatus(rows []UserSubscription, now time.Time) {
	for i := range rows {
		if rows[i].Status == SubscriptionStatusActive && !now.Before(rows[i].ExpiresAt) {
			rows[i].Status = SubscriptionStatusExpired
		}
	}
}

func normalizeExpiredWindowsAt(rows []UserSubscription, now time.Time) {
	for i := range rows {
		if start, ok := rows[i].automaticDailyWindowStartAt(now); ok && rows[i].DailyReservedUSD == 0 {
			rows[i].DailyWindowStart, rows[i].DailyUsageUSD, rows[i].DailyReservedUSD = &start, 0, 0
		}
		if start, ok := rows[i].automaticWindowStartAt(rows[i].WeeklyWindowStart, 7*24*time.Hour, now); ok && rows[i].WeeklyReservedUSD == 0 {
			rows[i].WeeklyWindowStart, rows[i].WeeklyUsageUSD, rows[i].WeeklyReservedUSD = &start, 0, 0
		}
		if start, ok := rows[i].automaticWindowStartAt(rows[i].MonthlyWindowStart, 30*24*time.Hour, now); ok && rows[i].MonthlyReservedUSD == 0 {
			rows[i].MonthlyWindowStart, rows[i].MonthlyUsageUSD, rows[i].MonthlyReservedUSD = &start, 0, 0
		}
	}
}

func (s *SubscriptionService) CheckAndActivateWindow(ctx context.Context, sub *UserSubscription) error {
	if sub == nil {
		return ErrSubscriptionNilInput
	}
	if sub.IsWindowActivated() {
		return nil
	}
	now := s.currentTime()
	if err := s.userSubRepo.ActivateWindows(ctx, sub.ID, timezone.StartOfDay(now), now); err != nil {
		return err
	}
	return nil
}

func (s *SubscriptionService) AdminResetQuota(ctx context.Context, id int64, daily, weekly, monthly bool) (*UserSubscription, error) {
	if !daily && !weekly && !monthly {
		return nil, ErrInvalidInput
	}
	now := s.currentTime()
	if err := s.userSubRepo.ResetUsageWindows(ctx, id, daily, weekly, monthly, timezone.StartOfDay(now), now); err != nil {
		return nil, err
	}
	return s.userSubRepo.GetByID(ctx, id)
}

func (s *SubscriptionService) CheckAndResetWindows(ctx context.Context, sub *UserSubscription) error {
	if sub == nil {
		return ErrSubscriptionNilInput
	}
	now := s.currentTime()
	if start, ok := sub.automaticDailyWindowStartAt(now); ok {
		if err := s.userSubRepo.ResetDailyUsage(ctx, sub.ID, sub.DailyWindowStart, start); err != nil {
			return err
		}
		sub.DailyWindowStart, sub.DailyUsageUSD, sub.DailyReservedUSD = &start, 0, 0
	}
	if start, ok := sub.automaticWindowStartAt(sub.WeeklyWindowStart, 7*24*time.Hour, now); ok {
		if err := s.userSubRepo.ResetWeeklyUsage(ctx, sub.ID, sub.WeeklyWindowStart, start); err != nil {
			return err
		}
		sub.WeeklyWindowStart, sub.WeeklyUsageUSD, sub.WeeklyReservedUSD = &start, 0, 0
	}
	if start, ok := sub.automaticWindowStartAt(sub.MonthlyWindowStart, 30*24*time.Hour, now); ok {
		if err := s.userSubRepo.ResetMonthlyUsage(ctx, sub.ID, sub.MonthlyWindowStart, start); err != nil {
			return err
		}
		sub.MonthlyWindowStart, sub.MonthlyUsageUSD, sub.MonthlyReservedUSD = &start, 0, 0
	}
	return nil
}

func (s *SubscriptionService) EnsureWindowMaintenance(ctx context.Context, sub *UserSubscription) (*UserSubscription, error) {
	if err := s.CheckAndActivateWindow(ctx, sub); err != nil {
		return nil, err
	}
	latest, err := s.userSubRepo.GetByID(ctx, sub.ID)
	if err != nil {
		return nil, err
	}
	if err := s.CheckAndResetWindows(ctx, latest); err != nil {
		return nil, err
	}
	return s.userSubRepo.GetByID(ctx, sub.ID)
}

func (s *SubscriptionService) CheckUsageLimits(_ context.Context, sub *UserSubscription, additionalCost float64) error {
	if sub == nil || sub.Plan == nil {
		return ErrSubscriptionNotFound
	}
	additional := decimal.NewFromFloat(additionalCost)
	if !subscriptionWindowAllows(sub.DailyUsageUSD, sub.DailyReservedUSD, sub.Plan.DailyLimitUSD, additional) {
		return ErrDailyLimitExceeded
	}
	if !subscriptionWindowAllows(sub.WeeklyUsageUSD, sub.WeeklyReservedUSD, sub.Plan.WeeklyLimitUSD, additional) {
		return ErrWeeklyLimitExceeded
	}
	if !subscriptionWindowAllows(sub.MonthlyUsageUSD, sub.MonthlyReservedUSD, sub.Plan.MonthlyLimitUSD, additional) {
		return ErrMonthlyLimitExceeded
	}
	return nil
}

func subscriptionWindowAllows(committed, reserved float64, limit *float64, additional decimal.Decimal) bool {
	if limit == nil {
		return true
	}
	used := decimal.NewFromFloat(committed).Add(decimal.NewFromFloat(reserved)).Add(additional)
	return used.LessThanOrEqual(decimal.NewFromFloat(*limit))
}

func (s *SubscriptionService) ValidateAndCheckLimits(sub *UserSubscription) (bool, error) {
	now := s.currentTime()
	if err := validateSubscriptionAt(sub, now); err != nil {
		return false, err
	}
	transition, err := sub.AllowanceWindowTransitionAt(now)
	if err != nil {
		return false, err
	}
	if transition.ResetDaily || transition.ResetWeekly || transition.ResetMonthly {
		return true, nil
	}
	return false, s.CheckUsageLimits(context.Background(), sub, 0)
}

type SubscriptionProgress struct {
	ID            int64                `json:"id"`
	PlanName      string               `json:"plan_name"`
	ExpiresAt     time.Time            `json:"expires_at"`
	ExpiresInDays int                  `json:"expires_in_days"`
	Daily         *UsageWindowProgress `json:"daily,omitempty"`
	Weekly        *UsageWindowProgress `json:"weekly,omitempty"`
	Monthly       *UsageWindowProgress `json:"monthly,omitempty"`
}

type UsageWindowProgress struct {
	LimitUSD        float64   `json:"limit_usd"`
	UsedUSD         float64   `json:"used_usd"`
	ReservedUSD     float64   `json:"reserved_usd"`
	RemainingUSD    float64   `json:"remaining_usd"`
	Percentage      float64   `json:"percentage"`
	WindowStart     time.Time `json:"window_start"`
	ResetsAt        time.Time `json:"resets_at"`
	ResetsInSeconds int64     `json:"resets_in_seconds"`
}

func (s *SubscriptionService) GetSubscriptionProgress(ctx context.Context, id int64) (*SubscriptionProgress, error) {
	sub, err := s.userSubRepo.GetByID(ctx, id)
	if err != nil {
		return nil, ErrSubscriptionNotFound
	}
	now := s.currentTime()
	// Progress reads must reflect the current quota window even when the
	// maintenance worker has not persisted the rollover yet. The normalization
	// only mutates the in-memory response and deliberately preserves windows
	// with outstanding reservations for later settlement.
	rows := []UserSubscription{*sub}
	normalizeExpiredWindowsAt(rows, now)
	return calculateSubscriptionProgressAt(&rows[0], now), nil
}

func calculateSubscriptionProgressAt(sub *UserSubscription, now time.Time) *SubscriptionProgress {
	if sub == nil {
		return nil
	}
	progress := &SubscriptionProgress{ID: sub.ID, ExpiresAt: sub.ExpiresAt, ExpiresInDays: sub.daysRemainingAt(now)}
	if sub.Plan == nil {
		return progress
	}
	progress.PlanName = sub.Plan.Name
	progress.Daily = buildWindowProgress(sub.Plan.DailyLimitUSD, sub.DailyUsageUSD, sub.DailyReservedUSD, sub.DailyWindowStart, sub.DailyResetTime(), now)
	progress.Weekly = buildWindowProgress(sub.Plan.WeeklyLimitUSD, sub.WeeklyUsageUSD, sub.WeeklyReservedUSD, sub.WeeklyWindowStart, sub.WeeklyResetTime(), now)
	progress.Monthly = buildWindowProgress(sub.Plan.MonthlyLimitUSD, sub.MonthlyUsageUSD, sub.MonthlyReservedUSD, sub.MonthlyWindowStart, sub.MonthlyResetTime(), now)
	return progress
}

func buildWindowProgress(limit *float64, used, reserved float64, start, resetsAt *time.Time, now time.Time) *UsageWindowProgress {
	if limit == nil || start == nil || resetsAt == nil {
		return nil
	}
	consumed := used + reserved
	remaining := math.Max(*limit-consumed, 0)
	percentage := 100.0
	if *limit > 0 {
		percentage = math.Min(consumed / *limit * 100, 100)
	}
	seconds := int64(resetsAt.Sub(now).Seconds())
	if seconds < 0 {
		seconds = 0
	}
	return &UsageWindowProgress{
		LimitUSD: *limit, UsedUSD: used, ReservedUSD: reserved, RemainingUSD: remaining,
		Percentage: percentage, WindowStart: *start, ResetsAt: *resetsAt, ResetsInSeconds: seconds,
	}
}

func (s *SubscriptionService) GetUserSubscriptionsWithProgress(ctx context.Context, userID int64) ([]SubscriptionProgress, error) {
	rows, err := s.userSubRepo.ListActiveByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	now := s.currentTime()
	normalizeExpiredWindowsAt(rows, now)
	result := make([]SubscriptionProgress, 0, len(rows))
	for i := range rows {
		result = append(result, *calculateSubscriptionProgressAt(&rows[i], now))
	}
	return result, nil
}

func (s *SubscriptionService) ValidateSubscription(ctx context.Context, sub *UserSubscription) error {
	now := s.currentTime()
	err := validateSubscriptionAt(sub, now)
	if errors.Is(err, ErrSubscriptionExpired) && sub != nil && sub.Status != SubscriptionStatusExpired && !now.Before(sub.ExpiresAt) && s.userSubRepo != nil {
		_ = s.userSubRepo.UpdateStatus(ctx, sub.ID, SubscriptionStatusExpired)
	}
	return err
}

func validateSubscriptionAt(sub *UserSubscription, now time.Time) error {
	if sub == nil || sub.DeletedAt != nil {
		return ErrSubscriptionNotFound
	}
	if sub.Status == SubscriptionStatusExpired {
		return ErrSubscriptionExpired
	}
	if sub.Status == SubscriptionStatusSuspended {
		return ErrSubscriptionSuspended
	}
	if now.Before(sub.StartsAt) {
		return ErrSubscriptionNotStarted
	}
	if !now.Before(sub.ExpiresAt) {
		return ErrSubscriptionExpired
	}
	return nil
}
