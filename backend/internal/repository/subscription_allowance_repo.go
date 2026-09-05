package repository

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/shopspring/decimal"
)

const (
	defaultSubscriptionAllowanceTTL           = 6 * time.Hour
	defaultSubscriptionAllowanceRecoveryBatch = 500
	maxSubscriptionAllowanceRecoveryBatch     = 5000
	subscriptionAllowanceRecoveryLease        = time.Minute
)

type subscriptionAllowanceScanner interface {
	Scan(...any) error
}

func scanSubscriptionAllowance(scanner subscriptionAllowanceScanner) (*service.SubscriptionAllowanceReservation, error) {
	var (
		record                                service.SubscriptionAllowanceReservation
		dailyStart, weeklyStart, monthlyStart sql.NullTime
		requestFingerprint, asyncTaskID       sql.NullString
	)
	err := scanner.Scan(
		&record.RequestID,
		&record.APIKeyID,
		&record.UserID,
		&record.SubscriptionID,
		&record.AuthorizationFingerprint,
		&requestFingerprint,
		&record.AuthorizedAmount,
		&record.CapturedAmount,
		&record.Status,
		&dailyStart,
		&weeklyStart,
		&monthlyStart,
		&record.ExpiresAt,
		&record.UpdatedAt,
		&asyncTaskID,
	)
	if err != nil {
		return nil, err
	}
	record.RequestFingerprint = requestFingerprint.String
	record.AsyncTaskID = asyncTaskID.String
	if dailyStart.Valid {
		record.DailyWindowStart = &dailyStart.Time
	}
	if weeklyStart.Valid {
		record.WeeklyWindowStart = &weeklyStart.Time
	}
	if monthlyStart.Valid {
		record.MonthlyWindowStart = &monthlyStart.Time
	}
	return &record, nil
}

const subscriptionAllowanceReturning = `
	request_id,
	api_key_id,
	user_id,
	subscription_id,
	authorization_fingerprint,
	request_fingerprint,
	authorized_amount,
	captured_amount,
	status,
	daily_window_start,
	weekly_window_start,
	monthly_window_start,
	expires_at,
	updated_at,
	async_task_id
`

func normalizeSubscriptionAllowanceCommand(cmd *service.SubscriptionAllowanceCommand) error {
	if cmd == nil {
		return service.ErrInvalidBillingPreauthorizationEstimate
	}
	cmd.RequestID = strings.TrimSpace(cmd.RequestID)
	cmd.AuthorizationFingerprint = strings.TrimSpace(cmd.AuthorizationFingerprint)
	cmd.Amount = service.QuantizeUsageBillingAmount(cmd.Amount)
	if cmd.RequestID == "" || cmd.AuthorizationFingerprint == "" || cmd.APIKeyID <= 0 ||
		cmd.UserID <= 0 || cmd.SubscriptionID <= 0 || cmd.Amount < 0 ||
		math.IsNaN(cmd.Amount) || math.IsInf(cmd.Amount, 0) {
		return service.ErrInvalidBillingPreauthorizationEstimate
	}
	if cmd.AuthorizedAt.IsZero() {
		cmd.AuthorizedAt = time.Now()
	}
	if cmd.ExpiresAt.IsZero() {
		cmd.ExpiresAt = cmd.AuthorizedAt.Add(defaultSubscriptionAllowanceTTL)
	}
	return nil
}

func (r *usageBillingRepository) findSubscriptionAllowance(
	ctx context.Context,
	queryer interface {
		QueryRowContext(context.Context, string, ...any) *sql.Row
	},
	requestID string,
	apiKeyID int64,
	forUpdate bool,
) (*service.SubscriptionAllowanceReservation, error) {
	query := `SELECT ` + subscriptionAllowanceReturning + `
		FROM billing_reservations
		WHERE request_id = $1 AND api_key_id = $2 AND funding_source = $3`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	record, err := scanSubscriptionAllowance(queryer.QueryRowContext(
		ctx, query, requestID, apiKeyID, service.FundingSourceSubscription,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return record, err
}

func validateSubscriptionAllowanceIdentity(record *service.SubscriptionAllowanceReservation, cmd *service.SubscriptionAllowanceCommand) error {
	if record == nil || cmd == nil || record.RequestID != cmd.RequestID || record.APIKeyID != cmd.APIKeyID ||
		record.UserID != cmd.UserID || record.SubscriptionID != cmd.SubscriptionID ||
		record.AuthorizationFingerprint != cmd.AuthorizationFingerprint {
		return service.ErrUsageBillingRequestConflict
	}
	return nil
}

func (r *usageBillingRepository) AuthorizeSubscriptionAllowance(
	ctx context.Context,
	cmd *service.SubscriptionAllowanceCommand,
) (_ *service.SubscriptionAllowanceReservation, err error) {
	if r == nil || r.db == nil {
		return nil, errors.New("usage billing repository db is nil")
	}
	if err := normalizeSubscriptionAllowanceCommand(cmd); err != nil {
		return nil, err
	}
	// Only a new authorization requires a future expiry. Capture/release and
	// recovery must be allowed to settle an already-authorized row after its
	// lease/TTL has elapsed; those paths validate the row state separately.
	if !cmd.ExpiresAt.After(cmd.AuthorizedAt) || !cmd.ExpiresAt.After(time.Now()) {
		return nil, service.ErrInvalidBillingPreauthorizationEstimate
	}
	if existing, err := r.findSubscriptionAllowance(ctx, r.db, cmd.RequestID, cmd.APIKeyID, false); err != nil {
		return nil, err
	} else if existing != nil {
		if err := validateSubscriptionAllowanceIdentity(existing, cmd); err != nil || !subscriptionAllowanceAmountEqual(existing.AuthorizedAmount, cmd.Amount) {
			if err != nil {
				return nil, err
			}
			return nil, service.ErrUsageBillingRequestConflict
		}
		return existing, nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if tx != nil {
			_ = tx.Rollback()
		}
	}()

	subscription, err := lockSubscriptionAllowance(ctx, tx, cmd)
	if err != nil {
		return nil, err
	}
	// A same-request contender can have committed while this transaction waited
	// on the subscription row. Recheck before changing reserved counters.
	if existing, findErr := r.findSubscriptionAllowance(ctx, tx, cmd.RequestID, cmd.APIKeyID, true); findErr != nil {
		return nil, findErr
	} else if existing != nil {
		if err := validateSubscriptionAllowanceIdentity(existing, cmd); err != nil || !subscriptionAllowanceAmountEqual(existing.AuthorizedAmount, cmd.Amount) {
			if err != nil {
				return nil, err
			}
			return nil, service.ErrUsageBillingRequestConflict
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		tx = nil
		return existing, nil
	}

	transition, err := subscription.AllowanceWindowTransitionAt(cmd.AuthorizedAt)
	if err != nil {
		return nil, err
	}
	if err := ensureSubscriptionAllowanceAvailable(subscription, transition, cmd.Amount); err != nil {
		return nil, err
	}
	if err := applySubscriptionWindowReservation(ctx, tx, subscription.ID, transition, cmd.Amount); err != nil {
		return nil, err
	}
	record, err := scanSubscriptionAllowance(tx.QueryRowContext(ctx, `
		INSERT INTO billing_reservations (
			request_id, api_key_id, user_id, funding_source, subscription_id,
			authorized_amount, captured_amount, status,
			authorization_fingerprint, request_fingerprint,
			daily_window_start, weekly_window_start, monthly_window_start,
			expires_at
		)
		VALUES ($1, $2, $3, $4, $5, $6, 0, $7, $8, '', $9, $10, $11, $12)
		RETURNING `+subscriptionAllowanceReturning,
		cmd.RequestID, cmd.APIKeyID, cmd.UserID, service.FundingSourceSubscription,
		cmd.SubscriptionID, cmd.Amount, service.BillingReservationAuthorized,
		cmd.AuthorizationFingerprint, transition.DailyStart, transition.WeeklyStart,
		transition.MonthlyStart, cmd.ExpiresAt,
	))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	tx = nil
	return record, nil
}

func lockSubscriptionAllowance(ctx context.Context, tx *sql.Tx, cmd *service.SubscriptionAllowanceCommand) (*service.UserSubscription, error) {
	var (
		sub                                   service.UserSubscription
		status                                string
		dailyStart, weeklyStart, monthlyStart sql.NullTime
		dailyLimit, weeklyLimit, monthlyLimit sql.NullFloat64
	)
	err := tx.QueryRowContext(ctx, `
		SELECT us.id, us.user_id, us.plan_id, us.plan_version_id,
			us.starts_at, us.expires_at, us.status,
			us.daily_window_start, us.weekly_window_start, us.monthly_window_start,
			us.daily_usage_usd, us.weekly_usage_usd, us.monthly_usage_usd,
			us.daily_reserved_usd, us.weekly_reserved_usd, us.monthly_reserved_usd,
			spv.daily_limit_usd, spv.weekly_limit_usd, spv.monthly_limit_usd
		FROM user_subscriptions us
		JOIN subscription_plan_versions spv ON spv.id = us.plan_version_id
		JOIN api_keys ak ON ak.id = $2
			AND ak.user_id = us.user_id
			AND ak.subscription_id = us.id
			AND ak.funding_source = $3
			AND ak.deleted_at IS NULL
		WHERE us.id = $1 AND us.user_id = $4 AND us.deleted_at IS NULL
		FOR UPDATE OF us
	`, cmd.SubscriptionID, cmd.APIKeyID, service.FundingSourceSubscription, cmd.UserID).Scan(
		&sub.ID, &sub.UserID, &sub.PlanID, &sub.PlanVersionID,
		&sub.StartsAt, &sub.ExpiresAt, &status,
		&dailyStart, &weeklyStart, &monthlyStart,
		&sub.DailyUsageUSD, &sub.WeeklyUsageUSD, &sub.MonthlyUsageUSD,
		&sub.DailyReservedUSD, &sub.WeeklyReservedUSD, &sub.MonthlyReservedUSD,
		&dailyLimit, &weeklyLimit, &monthlyLimit,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrSubscriptionNotFound
	}
	if err != nil {
		return nil, err
	}
	sub.Status = status
	if dailyStart.Valid {
		sub.DailyWindowStart = &dailyStart.Time
	}
	if weeklyStart.Valid {
		sub.WeeklyWindowStart = &weeklyStart.Time
	}
	if monthlyStart.Valid {
		sub.MonthlyWindowStart = &monthlyStart.Time
	}
	sub.Plan = &service.SubscriptionPlan{}
	if dailyLimit.Valid {
		sub.Plan.DailyLimitUSD = &dailyLimit.Float64
	}
	if weeklyLimit.Valid {
		sub.Plan.WeeklyLimitUSD = &weeklyLimit.Float64
	}
	if monthlyLimit.Valid {
		sub.Plan.MonthlyLimitUSD = &monthlyLimit.Float64
	}
	if status == service.SubscriptionStatusSuspended {
		return nil, service.ErrSubscriptionSuspended
	}
	if status != service.SubscriptionStatusActive || !cmd.AuthorizedAt.Before(sub.ExpiresAt) {
		return nil, service.ErrSubscriptionExpired
	}
	if cmd.AuthorizedAt.Before(sub.StartsAt) {
		return nil, service.ErrSubscriptionNotStarted
	}
	return &sub, nil
}

func transitionedUsage(current float64, reset bool) float64 {
	if reset {
		return 0
	}
	return current
}

func ensureSubscriptionAllowanceAvailable(sub *service.UserSubscription, transition service.SubscriptionWindowTransition, amount float64) error {
	if sub == nil || sub.Plan == nil {
		return service.ErrSubscriptionNotFound
	}
	additional := decimal.NewFromFloat(amount)
	if !subscriptionAllowanceWindowAvailable(transitionedUsage(sub.DailyUsageUSD, transition.ResetDaily), sub.DailyReservedUSD, sub.Plan.DailyLimitUSD, additional) {
		return service.ErrDailyLimitExceeded
	}
	if !subscriptionAllowanceWindowAvailable(transitionedUsage(sub.WeeklyUsageUSD, transition.ResetWeekly), sub.WeeklyReservedUSD, sub.Plan.WeeklyLimitUSD, additional) {
		return service.ErrWeeklyLimitExceeded
	}
	if !subscriptionAllowanceWindowAvailable(transitionedUsage(sub.MonthlyUsageUSD, transition.ResetMonthly), sub.MonthlyReservedUSD, sub.Plan.MonthlyLimitUSD, additional) {
		return service.ErrMonthlyLimitExceeded
	}
	return nil
}

func subscriptionAllowanceWindowAvailable(used, reserved float64, limit *float64, amount decimal.Decimal) bool {
	if limit == nil {
		return true
	}
	return decimal.NewFromFloat(used).Add(decimal.NewFromFloat(reserved)).Add(amount).
		LessThanOrEqual(decimal.NewFromFloat(*limit))
}

func applySubscriptionWindowReservation(ctx context.Context, tx *sql.Tx, subscriptionID int64, transition service.SubscriptionWindowTransition, amount float64) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE user_subscriptions
		SET daily_window_start = $2,
			weekly_window_start = $3,
			monthly_window_start = $4,
			daily_usage_usd = CASE WHEN $5 THEN 0 ELSE daily_usage_usd END,
			weekly_usage_usd = CASE WHEN $6 THEN 0 ELSE weekly_usage_usd END,
			monthly_usage_usd = CASE WHEN $7 THEN 0 ELSE monthly_usage_usd END,
			daily_reserved_usd = daily_reserved_usd + $8,
			weekly_reserved_usd = weekly_reserved_usd + $8,
			monthly_reserved_usd = monthly_reserved_usd + $8,
			updated_at = NOW()
		WHERE id = $1 AND deleted_at IS NULL
	`, subscriptionID, transition.DailyStart, transition.WeeklyStart, transition.MonthlyStart,
		transition.ResetDaily, transition.ResetWeekly, transition.ResetMonthly, amount)
	if err != nil {
		return err
	}
	return requireOneBillingRow(result, service.ErrSubscriptionNotFound)
}

func (r *usageBillingRepository) ResumeSubscriptionAllowance(ctx context.Context, cmd *service.SubscriptionAllowanceCommand) (*service.SubscriptionAllowanceReservation, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("usage billing repository db is nil")
	}
	if err := normalizeSubscriptionAllowanceCommand(cmd); err != nil {
		return nil, err
	}
	record, err := r.findSubscriptionAllowance(ctx, r.db, cmd.RequestID, cmd.APIKeyID, false)
	if err != nil {
		return nil, err
	}
	if err := validateSubscriptionAllowanceIdentity(record, cmd); err != nil {
		return nil, err
	}
	if !subscriptionAllowanceAmountEqual(record.AuthorizedAmount, cmd.Amount) {
		return nil, service.ErrUsageBillingRequestConflict
	}
	return record, nil
}

func (r *usageBillingRepository) TopUpSubscriptionAllowance(ctx context.Context, cmd *service.SubscriptionAllowanceCommand) (_ *service.SubscriptionAllowanceReservation, err error) {
	if r == nil || r.db == nil {
		return nil, errors.New("usage billing repository db is nil")
	}
	if err := normalizeSubscriptionAllowanceCommand(cmd); err != nil {
		return nil, err
	}
	candidate, err := r.findSubscriptionAllowance(ctx, r.db, cmd.RequestID, cmd.APIKeyID, false)
	if err != nil {
		return nil, err
	}
	if err := validateSubscriptionAllowanceIdentity(candidate, cmd); err != nil {
		return nil, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockSubscriptionAllowanceRow(ctx, tx, candidate.SubscriptionID); err != nil {
		return nil, err
	}
	record, err := r.findSubscriptionAllowance(ctx, tx, cmd.RequestID, cmd.APIKeyID, true)
	if err != nil {
		return nil, err
	}
	if err := validateSubscriptionAllowanceIdentity(record, cmd); err != nil {
		return nil, err
	}
	if record.Status != service.BillingReservationAuthorized || !record.ExpiresAt.After(time.Now()) {
		return nil, service.ErrUsageBillingRequestConflict
	}
	if cmd.Amount <= record.AuthorizedAmount {
		return record, nil
	}
	delta := service.QuantizeUsageBillingAmount(cmd.Amount - record.AuthorizedAmount)
	if err := lockAndTopUpSubscriptionAllowance(ctx, tx, record, delta, time.Now()); err != nil {
		return nil, err
	}
	record, err = scanSubscriptionAllowance(tx.QueryRowContext(ctx, `
		UPDATE billing_reservations
		SET authorized_amount = $3, updated_at = NOW()
		WHERE request_id = $1 AND api_key_id = $2
			AND funding_source = $5 AND status = $4
		RETURNING `+subscriptionAllowanceReturning,
		cmd.RequestID, cmd.APIKeyID, cmd.Amount, service.BillingReservationAuthorized,
		service.FundingSourceSubscription,
	))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return record, nil
}

func lockSubscriptionAllowanceRow(ctx context.Context, tx *sql.Tx, subscriptionID int64) error {
	var id int64
	err := tx.QueryRowContext(ctx, `
		SELECT id
		FROM user_subscriptions
		WHERE id = $1
		FOR UPDATE
	`, subscriptionID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrSubscriptionNotFound
	}
	return err
}

func lockAndTopUpSubscriptionAllowance(ctx context.Context, tx *sql.Tx, record *service.SubscriptionAllowanceReservation, delta float64, now time.Time) error {
	if record == nil {
		return service.ErrUsageBillingRequestConflict
	}
	var (
		dailyUsed, weeklyUsed, monthlyUsed, dailyReserved, weeklyReserved, monthlyReserved float64
		dailyStart, weeklyStart, monthlyStart                                              sql.NullTime
		startsAt, expiresAt                                                                time.Time
		status                                                                             string
	)
	var dailyLimit, weeklyLimit, monthlyLimit sql.NullFloat64
	err := tx.QueryRowContext(ctx, `
		SELECT us.starts_at, us.expires_at, us.status,
			us.daily_window_start, us.weekly_window_start, us.monthly_window_start,
			us.daily_usage_usd, us.weekly_usage_usd, us.monthly_usage_usd,
			us.daily_reserved_usd, us.weekly_reserved_usd, us.monthly_reserved_usd,
			spv.daily_limit_usd, spv.weekly_limit_usd, spv.monthly_limit_usd
		FROM user_subscriptions us
		JOIN subscription_plan_versions spv ON spv.id = us.plan_version_id
		WHERE us.id = $1 AND us.deleted_at IS NULL
		FOR UPDATE OF us
	`, record.SubscriptionID).Scan(
		&startsAt, &expiresAt, &status, &dailyStart, &weeklyStart, &monthlyStart,
		&dailyUsed, &weeklyUsed, &monthlyUsed, &dailyReserved, &weeklyReserved, &monthlyReserved,
		&dailyLimit, &weeklyLimit, &monthlyLimit,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrUsageBillingRequestConflict
	}
	if err != nil {
		return err
	}
	if status != service.SubscriptionStatusActive || now.Before(startsAt) || !now.Before(expiresAt) {
		return service.ErrUsageBillingRequestConflict
	}
	// A positive reservation must settle against the exact window in which it
	// was authorized. A zero-cost reservation is different: it did not increase
	// any reserved counter, so maintenance was allowed to move the window while
	// the request was still running. Recompute that transition while the row is
	// locked and move the reservation snapshot before adding its first charge.
	current := &service.UserSubscription{
		ID:                 record.SubscriptionID,
		StartsAt:           startsAt,
		ExpiresAt:          expiresAt,
		Status:             status,
		DailyUsageUSD:      dailyUsed,
		WeeklyUsageUSD:     weeklyUsed,
		MonthlyUsageUSD:    monthlyUsed,
		DailyReservedUSD:   dailyReserved,
		WeeklyReservedUSD:  weeklyReserved,
		MonthlyReservedUSD: monthlyReserved,
		Plan: &service.SubscriptionPlan{
			DailyLimitUSD:   nullableFloatPointer(dailyLimit),
			WeeklyLimitUSD:  nullableFloatPointer(weeklyLimit),
			MonthlyLimitUSD: nullableFloatPointer(monthlyLimit),
		},
	}
	if dailyStart.Valid {
		current.DailyWindowStart = &dailyStart.Time
	}
	if weeklyStart.Valid {
		current.WeeklyWindowStart = &weeklyStart.Time
	}
	if monthlyStart.Valid {
		current.MonthlyWindowStart = &monthlyStart.Time
	}
	transition := service.SubscriptionWindowTransition{}
	if record.AuthorizedAmount == 0 {
		transition, err = current.AllowanceWindowTransitionAt(now)
		if err != nil {
			return err
		}
	} else {
		if !subscriptionAllowanceWindowMatches(current.DailyWindowStart, record.DailyWindowStart) ||
			!subscriptionAllowanceWindowMatches(current.WeeklyWindowStart, record.WeeklyWindowStart) ||
			!subscriptionAllowanceWindowMatches(current.MonthlyWindowStart, record.MonthlyWindowStart) {
			return service.ErrUsageBillingRequestConflict
		}
		if current.DailyWindowStart != nil {
			transition.DailyStart = *current.DailyWindowStart
		}
		if current.WeeklyWindowStart != nil {
			transition.WeeklyStart = *current.WeeklyWindowStart
		}
		if current.MonthlyWindowStart != nil {
			transition.MonthlyStart = *current.MonthlyWindowStart
		}
	}
	if err := ensureSubscriptionAllowanceAvailable(current, transition, delta); err != nil {
		return err
	}
	if err := applySubscriptionWindowReservation(ctx, tx, record.SubscriptionID, transition, delta); err != nil {
		return err
	}
	if record.AuthorizedAmount == 0 {
		result, err := tx.ExecContext(ctx, `
			UPDATE billing_reservations
			SET daily_window_start = $3,
				weekly_window_start = $4,
				monthly_window_start = $5,
				updated_at = NOW()
			WHERE request_id = $1 AND api_key_id = $2
				AND funding_source = $6 AND status = $7
		`, record.RequestID, record.APIKeyID, transition.DailyStart, transition.WeeklyStart,
			transition.MonthlyStart, service.FundingSourceSubscription, service.BillingReservationAuthorized)
		if err != nil {
			return err
		}
		if err := requireOneBillingRow(result, service.ErrUsageBillingRequestConflict); err != nil {
			return err
		}
	}
	return nil
}

func nullableFloatPointer(value sql.NullFloat64) *float64 {
	if !value.Valid {
		return nil
	}
	result := value.Float64
	return &result
}

func subscriptionAllowanceWindowMatches(current, snapshot *time.Time) bool {
	if current == nil || snapshot == nil {
		return current == nil && snapshot == nil
	}
	return current.Equal(*snapshot)
}

func (r *usageBillingRepository) CaptureSubscriptionAllowance(ctx context.Context, cmd *service.SubscriptionAllowanceCommand, requestFingerprint string) (*service.SubscriptionAllowanceReservation, error) {
	return r.finishSubscriptionAllowance(ctx, cmd, strings.TrimSpace(requestFingerprint), true)
}

func (r *usageBillingRepository) ReleaseSubscriptionAllowance(ctx context.Context, cmd *service.SubscriptionAllowanceCommand) (*service.SubscriptionAllowanceReservation, error) {
	return r.finishSubscriptionAllowance(ctx, cmd, "", false)
}

func (r *usageBillingRepository) finishSubscriptionAllowance(
	ctx context.Context,
	cmd *service.SubscriptionAllowanceCommand,
	requestFingerprint string,
	capture bool,
) (_ *service.SubscriptionAllowanceReservation, err error) {
	if r == nil || r.db == nil {
		return nil, errors.New("usage billing repository db is nil")
	}
	if err := normalizeSubscriptionAllowanceCommand(cmd); err != nil {
		return nil, err
	}
	if capture && requestFingerprint == "" {
		return nil, service.ErrInvalidBillingPreauthorizationEstimate
	}
	candidate, err := r.findSubscriptionAllowance(ctx, r.db, cmd.RequestID, cmd.APIKeyID, false)
	if err != nil {
		return nil, err
	}
	if err := validateSubscriptionAllowanceIdentity(candidate, cmd); err != nil {
		return nil, err
	}
	wantedStatus := service.BillingReservationReleased
	if capture {
		wantedStatus = service.BillingReservationCaptured
	}
	if candidate.Status == wantedStatus {
		if capture && (!subscriptionAllowanceAmountEqual(candidate.CapturedAmount, cmd.Amount) || candidate.RequestFingerprint != requestFingerprint) {
			return nil, service.ErrUsageBillingRequestConflict
		}
		return candidate, nil
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockSubscriptionAllowanceRow(ctx, tx, candidate.SubscriptionID); err != nil {
		return nil, err
	}
	record, err := r.findSubscriptionAllowance(ctx, tx, cmd.RequestID, cmd.APIKeyID, true)
	if err != nil {
		return nil, err
	}
	if err := validateSubscriptionAllowanceIdentity(record, cmd); err != nil {
		return nil, err
	}
	if record.Status == wantedStatus {
		if capture && (!subscriptionAllowanceAmountEqual(record.CapturedAmount, cmd.Amount) || record.RequestFingerprint != requestFingerprint) {
			return nil, service.ErrUsageBillingRequestConflict
		}
		return record, nil
	}
	if capture && record.Status == service.BillingReservationFinalizing {
		if !subscriptionAllowanceAmountEqual(record.CapturedAmount, cmd.Amount) || record.RequestFingerprint != requestFingerprint {
			return nil, service.ErrUsageBillingRequestConflict
		}
	} else if record.Status != service.BillingReservationAuthorized {
		return nil, service.ErrUsageBillingRequestConflict
	}
	if cmd.Amount > record.AuthorizedAmount {
		return nil, service.ErrUsageBillingRequestConflict
	}
	actual := 0.0
	if capture {
		actual = cmd.Amount
	}
	// A zero-amount authorization does not occupy any allowance. It may remain
	// authorized across a calendar/rolling-window transition because the
	// counters stay at zero and the window is therefore free to advance. There
	// is no subscription usage mutation to reconcile in that case; requiring the
	// old window snapshot would strand the reservation in finalizing forever.
	if record.AuthorizedAmount != 0 {
		result, err := tx.ExecContext(ctx, `
			UPDATE user_subscriptions
			SET daily_reserved_usd = daily_reserved_usd - $2,
				weekly_reserved_usd = weekly_reserved_usd - $2,
				monthly_reserved_usd = monthly_reserved_usd - $2,
				daily_usage_usd = daily_usage_usd + $3,
				weekly_usage_usd = weekly_usage_usd + $3,
				monthly_usage_usd = monthly_usage_usd + $3,
				updated_at = NOW()
			WHERE id = $1
				AND daily_window_start IS NOT DISTINCT FROM $4
				AND weekly_window_start IS NOT DISTINCT FROM $5
				AND monthly_window_start IS NOT DISTINCT FROM $6
				AND daily_reserved_usd >= $2
				AND weekly_reserved_usd >= $2
				AND monthly_reserved_usd >= $2
		`, record.SubscriptionID, record.AuthorizedAmount, actual,
			record.DailyWindowStart, record.WeeklyWindowStart, record.MonthlyWindowStart)
		if err != nil {
			return nil, err
		}
		if err := requireOneBillingRow(result, service.ErrUsageBillingRequestConflict); err != nil {
			return nil, err
		}
	}
	record, err = scanSubscriptionAllowance(tx.QueryRowContext(ctx, `
		UPDATE billing_reservations
		SET captured_amount = $3, status = $4, request_fingerprint = $5, updated_at = NOW()
		WHERE request_id = $1 AND api_key_id = $2
			AND funding_source = $6 AND status IN ($7, $8)
		RETURNING `+subscriptionAllowanceReturning,
		cmd.RequestID, cmd.APIKeyID, actual, wantedStatus, requestFingerprint,
		service.FundingSourceSubscription, service.BillingReservationAuthorized,
		service.BillingReservationFinalizing,
	))
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return record, nil
}

func (r *usageBillingRepository) ListRecoverableSubscriptionAllowances(ctx context.Context, authorizationExpiredBefore, finalizationStaleBefore time.Time, limit int) ([]service.SubscriptionAllowanceReservation, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("usage billing repository db is nil")
	}
	if authorizationExpiredBefore.IsZero() {
		authorizationExpiredBefore = time.Now()
	}
	if finalizationStaleBefore.IsZero() {
		finalizationStaleBefore = time.Now()
	}
	if limit <= 0 {
		limit = defaultSubscriptionAllowanceRecoveryBatch
	} else if limit > maxSubscriptionAllowanceRecoveryBatch {
		limit = maxSubscriptionAllowanceRecoveryBatch
	}
	rows, err := r.db.QueryContext(ctx, `
		WITH candidates AS (
			SELECT id
			FROM billing_reservations
			WHERE funding_source = $1
				AND (
					(status = $2 AND expires_at <= $3
						AND updated_at <= NOW() - ($6 * INTERVAL '1 second'))
					OR (status = $4 AND updated_at <= $5)
				)
			ORDER BY CASE WHEN status = $4 THEN updated_at ELSE expires_at END, id
			LIMIT $7
			FOR UPDATE SKIP LOCKED
		), leased AS (
			UPDATE billing_reservations AS reservation
			SET updated_at = NOW()
			FROM candidates
			WHERE reservation.id = candidates.id
			RETURNING reservation.request_id,
				reservation.api_key_id,
				reservation.user_id,
				reservation.subscription_id,
				reservation.authorization_fingerprint,
				reservation.request_fingerprint,
				reservation.authorized_amount,
				reservation.captured_amount,
				reservation.status,
				reservation.daily_window_start,
				reservation.weekly_window_start,
				reservation.monthly_window_start,
				reservation.expires_at,
				reservation.updated_at,
				reservation.async_task_id
		)
		SELECT `+subscriptionAllowanceReturning+`
		FROM leased
	`, service.FundingSourceSubscription, service.BillingReservationAuthorized, authorizationExpiredBefore,
		service.BillingReservationFinalizing, finalizationStaleBefore,
		int64(subscriptionAllowanceRecoveryLease/time.Second), limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	records := make([]service.SubscriptionAllowanceReservation, 0)
	for rows.Next() {
		record, err := scanSubscriptionAllowance(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, *record)
	}
	return records, rows.Err()
}

func subscriptionAllowanceAmountEqual(left, right float64) bool {
	return service.QuantizeUsageBillingAmount(left) == service.QuantizeUsageBillingAmount(right)
}

func requireOneBillingRow(result sql.Result, notFound error) error {
	if result == nil {
		return notFound
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return notFound
	}
	return nil
}
