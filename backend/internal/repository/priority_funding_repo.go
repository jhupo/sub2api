package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
	"github.com/shopspring/decimal"
)

const priorityFundingColumns = `request_id, api_key_id, user_id, primary_subscription_id,
 authorization_fingerprint, baseline_key, policy, allocations, authorized_amount,
 actual_amount, wallet_amount, uncovered_amount, request_fingerprint, status, expires_at, updated_at`

func scanPriorityFunding(row subscriptionAllowanceScanner) (*service.PriorityFundingReservation, error) {
	var record service.PriorityFundingReservation
	var policy, allocations []byte
	err := row.Scan(&record.RequestID, &record.APIKeyID, &record.UserID, &record.PrimarySubscriptionID,
		&record.AuthorizationFingerprint, &record.BaselineKey, &policy, &allocations, &record.Amount,
		&record.ActualAmount, &record.WalletAmount, &record.UncoveredAmount, &record.RequestFingerprint,
		&record.Status, &record.ExpiresAt, &record.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(policy, &record.Policy); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(allocations, &record.Allocations); err != nil {
		return nil, err
	}
	return &record, nil
}

func priorityFundingRecord(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, requestID string, keyID int64, lock bool) (*service.PriorityFundingReservation, error) {
	query := `SELECT ` + priorityFundingColumns + ` FROM billing_priority_reservations WHERE request_id = $1 AND api_key_id = $2`
	if lock {
		query += ` FOR UPDATE`
	}
	record, err := scanPriorityFunding(q.QueryRowContext(ctx, query, requestID, keyID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return record, err
}

func (r *usageBillingRepository) GetPriorityFunding(ctx context.Context, requestID string, keyID int64) (*service.PriorityFundingReservation, error) {
	record, err := priorityFundingRecord(ctx, r.db, requestID, keyID, false)
	if err != nil || record == nil {
		return record, err
	}
	wallet := &service.BalancePreauthorizationRecord{}
	err = r.db.QueryRowContext(ctx, `SELECT request_id, api_key_id, user_id, baseline_key,
 request_fingerprint, authorization_fingerprint, hold_usd, amount_usd, status, expires_at, updated_at
 FROM billing_balance_settlements WHERE request_id = $1 AND api_key_id = $2`,
		service.PriorityWalletRequestID(requestID), keyID).Scan(&wallet.RequestID, &wallet.APIKeyID, &wallet.UserID,
		&wallet.BaselineKey, &wallet.RequestFingerprint, &wallet.AuthorizationFingerprint, &wallet.HoldAmount,
		&wallet.Amount, &wallet.Status, &wallet.ExpiresAt, &wallet.UpdatedAt)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		record.WalletRecord = wallet
	}
	return record, nil
}

func validatePriorityCommand(cmd *service.PriorityFundingCommand) error {
	if cmd == nil || cmd.RequestID == "" || cmd.AuthorizationFingerprint == "" || cmd.APIKeyID <= 0 ||
		cmd.UserID <= 0 || cmd.PrimarySubscriptionID <= 0 || len(cmd.Policy.SubscriptionIDs) == 0 || len(cmd.Policy.SubscriptionIDs) > 32 {
		return service.ErrInvalidBillingPreauthorizationEstimate
	}
	if cmd.Amount < 0 || invalidPriorityAmount(cmd.Amount) {
		return service.ErrInvalidBillingPreauthorizationEstimate
	}
	seen := map[int64]bool{}
	for _, id := range cmd.Policy.SubscriptionIDs {
		if id <= 0 || seen[id] {
			return service.ErrInvalidBillingPreauthorizationEstimate
		}
		seen[id] = true
	}
	if !seen[cmd.PrimarySubscriptionID] {
		return service.ErrUsageBillingRequestConflict
	}
	cmd.Amount = service.QuantizeUsageBillingAmount(cmd.Amount)
	if cmd.ExpiresAt.IsZero() {
		cmd.ExpiresAt = time.Now().Add(defaultSubscriptionAllowanceTTL)
	}
	return nil
}

func invalidPriorityAmount(amount float64) bool {
	return math.IsNaN(amount) || math.IsInf(amount, 0) || amount > 1e12
}

func lockPriorityRequest(ctx context.Context, tx *sql.Tx, requestID string, keyID int64) error {
	_, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`,
		fmt.Sprintf("priority:%d:%s", keyID, requestID))
	return err
}

func validatePriorityKey(ctx context.Context, tx *sql.Tx, cmd *service.PriorityFundingCommand) error {
	var primary sql.NullInt64
	var encoded []byte
	var fallback bool
	err := tx.QueryRowContext(ctx, `SELECT subscription_id, subscription_priority, allow_wallet_fallback
 FROM api_keys WHERE id = $1 AND user_id = $2 AND funding_source = 'subscription' AND deleted_at IS NULL`,
		cmd.APIKeyID, cmd.UserID).Scan(&primary, &encoded, &fallback)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return service.ErrSubscriptionBillingInvalid
		}
		return err
	}
	var ids []int64
	if len(encoded) > 0 {
		if err := json.Unmarshal(encoded, &ids); err != nil {
			return err
		}
	}
	if len(ids) == 0 && primary.Valid {
		ids = []int64{primary.Int64}
	}
	if !reflect.DeepEqual(ids, cmd.Policy.SubscriptionIDs) || fallback != cmd.Policy.WalletFallback {
		return service.ErrUsageBillingRequestConflict
	}
	return nil
}

// All requests lock subscriptions by ID, independently of their spend order.
func lockPrioritySubscriptions(ctx context.Context, tx *sql.Tx, cmd *service.PriorityFundingCommand) (map[int64]*service.UserSubscription, error) {
	ids := append([]int64(nil), cmd.Policy.SubscriptionIDs...)
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	rows, err := tx.QueryContext(ctx, `SELECT id FROM user_subscriptions WHERE id = ANY($1) ORDER BY id FOR UPDATE`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return nil, err
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	subs := make(map[int64]*service.UserSubscription, len(ids))
	for _, id := range ids {
		sub, err := lockSubscriptionAllowance(ctx, tx, &service.SubscriptionAllowanceCommand{
			APIKeyID: cmd.APIKeyID, UserID: cmd.UserID, SubscriptionID: id, AuthorizedAt: time.Now(),
		})
		if err != nil {
			if errors.Is(err, service.ErrSubscriptionNotFound) || errors.Is(err, service.ErrSubscriptionExpired) ||
				errors.Is(err, service.ErrSubscriptionSuspended) || errors.Is(err, service.ErrSubscriptionNotStarted) {
				continue
			}
			return nil, err
		}
		subs[id] = sub
	}
	return subs, nil
}

func priorityAllocationMap(allocations []service.FundingAllocation) map[int64]service.FundingAllocation {
	result := make(map[int64]service.FundingAllocation, len(allocations))
	for _, allocation := range allocations {
		result[allocation.SubscriptionID] = allocation
	}
	return result
}

func priorityWindowEqual(left, right *time.Time) bool {
	return left == nil && right == nil || left != nil && right != nil && left.Equal(*right)
}

func priorityAvailable(sub *service.UserSubscription, old service.FundingAllocation) (decimal.Decimal, service.SubscriptionWindowTransition, bool) {
	if sub == nil {
		return decimal.Zero, service.SubscriptionWindowTransition{}, false
	}
	transition, err := sub.AllowanceWindowTransitionAt(time.Now())
	if err != nil {
		return decimal.Zero, transition, false
	}
	if old.Amount > 0 && (!priorityWindowEqual(old.DailyStart, &transition.DailyStart) ||
		!priorityWindowEqual(old.WeeklyStart, &transition.WeeklyStart) || !priorityWindowEqual(old.MonthlyStart, &transition.MonthlyStart)) {
		return decimal.Zero, transition, false
	}
	available := decimal.NewFromFloat(1e12)
	windows := []struct {
		used, reserved float64
		limit          *float64
	}{
		{sub.TotalUsageUSD, sub.TotalReservedUSD, sub.Plan.TotalLimitUSD},
		{transitionedUsage(sub.DailyUsageUSD, transition.ResetDaily), sub.DailyReservedUSD, sub.Plan.DailyLimitUSD},
		{transitionedUsage(sub.WeeklyUsageUSD, transition.ResetWeekly), sub.WeeklyReservedUSD, sub.Plan.WeeklyLimitUSD},
		{transitionedUsage(sub.MonthlyUsageUSD, transition.ResetMonthly), sub.MonthlyReservedUSD, sub.Plan.MonthlyLimitUSD},
	}
	for _, window := range windows {
		if window.limit != nil {
			remaining := decimal.NewFromFloat(*window.limit).Sub(decimal.NewFromFloat(window.used)).Sub(decimal.NewFromFloat(window.reserved))
			available = decimal.Min(available, remaining)
		}
	}
	return decimal.Max(decimal.Zero, available), transition, true
}

func (r *usageBillingRepository) ReservePriorityFunding(ctx context.Context, cmd *service.PriorityFundingCommand) (*service.PriorityFundingReservation, error) {
	if err := validatePriorityCommand(cmd); err != nil {
		return nil, err
	}
	if !cmd.ExpiresAt.After(time.Now()) {
		return nil, service.ErrInvalidBillingPreauthorizationEstimate
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockPriorityRequest(ctx, tx, cmd.RequestID, cmd.APIKeyID); err != nil {
		return nil, err
	}
	record, err := priorityFundingRecord(ctx, tx, cmd.RequestID, cmd.APIKeyID, true)
	if err != nil {
		return nil, err
	}
	if record == nil {
		if err := validatePriorityKey(ctx, tx, cmd); err != nil {
			return nil, err
		}
		record = &service.PriorityFundingReservation{PriorityFundingCommand: *cmd, Status: service.BillingReservationAuthorized}
		record.Amount = 0
	} else if record.UserID != cmd.UserID || record.PrimarySubscriptionID != cmd.PrimarySubscriptionID ||
		record.AuthorizationFingerprint != cmd.AuthorizationFingerprint || !reflect.DeepEqual(record.Policy, cmd.Policy) ||
		record.Status != service.BillingReservationAuthorized || !record.ExpiresAt.After(time.Now()) {
		return nil, service.ErrUsageBillingRequestConflict
	}
	subs, err := lockPrioritySubscriptions(ctx, tx, cmd)
	if err != nil {
		return nil, err
	}
	remaining := decimal.NewFromFloat(max(cmd.Amount, record.Amount))
	old := priorityAllocationMap(record.Allocations)
	for _, allocation := range record.Allocations {
		remaining = remaining.Sub(decimal.NewFromFloat(allocation.Amount))
	}
	allocations := make([]service.FundingAllocation, 0, len(cmd.Policy.SubscriptionIDs))
	for _, id := range cmd.Policy.SubscriptionIDs {
		allocation := old[id]
		allocation.SubscriptionID = id
		available, transition, eligible := priorityAvailable(subs[id], allocation)
		additional := decimal.Max(decimal.Zero, decimal.Min(remaining, available))
		if additional.IsPositive() && eligible {
			amount, _ := additional.Float64()
			if err := applySubscriptionWindowReservation(ctx, tx, id, transition, amount); err != nil {
				return nil, err
			}
			allocation.Amount = service.QuantizeUsageBillingAmount(allocation.Amount + amount)
			allocation.DailyStart, allocation.WeeklyStart, allocation.MonthlyStart = &transition.DailyStart, &transition.WeeklyStart, &transition.MonthlyStart
			remaining = remaining.Sub(additional)
		}
		if allocation.Amount > 0 {
			allocations = append(allocations, allocation)
		}
	}
	remaining = decimal.Max(decimal.Zero, remaining)
	if remaining.IsPositive() && !cmd.Policy.WalletFallback {
		return nil, service.ErrTotalLimitExceeded
	}
	record.Allocations = allocations
	record.Amount = max(record.Amount, cmd.Amount)
	record.WalletAmount, _ = remaining.Float64()
	policyJSON, err := json.Marshal(record.Policy)
	if err != nil {
		return nil, err
	}
	allocJSON, err := json.Marshal(record.Allocations)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO billing_priority_reservations
 (request_id, api_key_id, user_id, primary_subscription_id, authorization_fingerprint, baseline_key,
 policy, allocations, authorized_amount, wallet_amount, status, expires_at)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,'authorized',$11)
 ON CONFLICT (request_id, api_key_id) DO UPDATE SET allocations = EXCLUDED.allocations,
 authorized_amount = EXCLUDED.authorized_amount, wallet_amount = EXCLUDED.wallet_amount, updated_at = NOW()`,
		record.RequestID, record.APIKeyID, record.UserID, record.PrimarySubscriptionID, record.AuthorizationFingerprint,
		record.BaselineKey, string(policyJSON), string(allocJSON), record.Amount, record.WalletAmount, record.ExpiresAt)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return record, nil
}

func releasePriorityAllocation(ctx context.Context, tx *sql.Tx, allocation service.FundingAllocation, capture float64) error {
	result, err := tx.ExecContext(ctx, `UPDATE user_subscriptions SET
 total_reserved_usd = total_reserved_usd - $2, daily_reserved_usd = daily_reserved_usd - $2,
 weekly_reserved_usd = weekly_reserved_usd - $2, monthly_reserved_usd = monthly_reserved_usd - $2,
 total_usage_usd = total_usage_usd + $3, daily_usage_usd = daily_usage_usd + $3,
 weekly_usage_usd = weekly_usage_usd + $3, monthly_usage_usd = monthly_usage_usd + $3, updated_at = NOW()
 WHERE id = $1 AND total_reserved_usd >= $2 AND daily_reserved_usd >= $2
 AND weekly_reserved_usd >= $2 AND monthly_reserved_usd >= $2
 AND daily_window_start IS NOT DISTINCT FROM $4::timestamptz
 AND weekly_window_start IS NOT DISTINCT FROM $5::timestamptz
 AND monthly_window_start IS NOT DISTINCT FROM $6::timestamptz`, allocation.SubscriptionID, allocation.Amount, capture,
		allocation.DailyStart, allocation.WeeklyStart, allocation.MonthlyStart)
	if err != nil {
		return err
	}
	return requireOneBillingRow(result, service.ErrUsageBillingRequestConflict)
}

func (r *usageBillingRepository) ReleasePriorityFunding(ctx context.Context, requestID string, keyID int64) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockPriorityRequest(ctx, tx, requestID, keyID); err != nil {
		return err
	}
	record, err := priorityFundingRecord(ctx, tx, requestID, keyID, true)
	if err != nil || record == nil {
		return err
	}
	if record.Status == service.BillingReservationCaptured {
		return service.ErrBalancePreauthorizationAlreadyFinalized
	}
	if record.Status == service.BillingReservationReleased {
		return nil
	}
	if _, err := lockPrioritySubscriptions(ctx, tx, &record.PriorityFundingCommand); err != nil {
		return err
	}
	allocations := append([]service.FundingAllocation(nil), record.Allocations...)
	sort.Slice(allocations, func(i, j int) bool { return allocations[i].SubscriptionID < allocations[j].SubscriptionID })
	for _, allocation := range allocations {
		if err := releasePriorityAllocation(ctx, tx, allocation, 0); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE billing_priority_reservations SET status = 'released', updated_at = NOW()
 WHERE request_id = $1 AND api_key_id = $2`, requestID, keyID); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *usageBillingRepository) ListRecoverablePriorityFunding(ctx context.Context, before time.Time, limit int) ([]service.PriorityFundingReservation, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+priorityFundingColumns+` FROM billing_priority_reservations
 WHERE status = 'authorized' AND expires_at <= $1 ORDER BY expires_at LIMIT $2`, before, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var records []service.PriorityFundingReservation
	for rows.Next() {
		record, err := scanPriorityFunding(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, *record)
	}
	return records, rows.Err()
}

func (r *usageBillingRepository) applyPriorityFunding(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand, result *service.UsageBillingApplyResult) error {
	if cmd.SubscriptionCost < 0 || invalidPriorityAmount(cmd.SubscriptionCost) || cmd.BalanceCost != 0 || cmd.BalancePreauthorized || cmd.SubscriptionPreauthorized {
		return service.ErrInvalidBillingPreauthorizationEstimate
	}
	if err := lockPriorityRequest(ctx, tx, cmd.RequestID, cmd.APIKeyID); err != nil {
		return err
	}
	record, err := priorityFundingRecord(ctx, tx, cmd.RequestID, cmd.APIKeyID, true)
	if err != nil {
		return err
	}
	if record == nil {
		record = &service.PriorityFundingReservation{PriorityFundingCommand: service.PriorityFundingCommand{
			RequestID: cmd.RequestID, APIKeyID: cmd.APIKeyID, UserID: cmd.UserID,
			PrimarySubscriptionID: valueOrZeroInt64(cmd.SubscriptionID), Policy: *cmd.PriorityFunding,
			AuthorizationFingerprint: cmd.RequestFingerprint, BaselineKey: cmd.BaselineKey, ExpiresAt: time.Now(),
		}}
		if err := validatePriorityCommand(&record.PriorityFundingCommand); err != nil {
			return err
		}
		if err := validatePriorityKey(ctx, tx, &record.PriorityFundingCommand); err != nil {
			return err
		}
	} else if record.Status != service.BillingReservationAuthorized || record.UserID != cmd.UserID ||
		record.PrimarySubscriptionID != valueOrZeroInt64(cmd.SubscriptionID) || !reflect.DeepEqual(record.Policy, *cmd.PriorityFunding) {
		return service.ErrUsageBillingRequestConflict
	}
	subs, err := lockPrioritySubscriptions(ctx, tx, &record.PriorityFundingCommand)
	if err != nil {
		return err
	}
	remaining := decimal.NewFromFloat(cmd.SubscriptionCost)
	old := priorityAllocationMap(record.Allocations)
	allocations := make([]service.FundingAllocation, 0, len(record.Policy.SubscriptionIDs))
	for _, id := range record.Policy.SubscriptionIDs {
		allocation := old[id]
		allocation.SubscriptionID = id
		available, transition, eligible := priorityAvailable(subs[id], allocation)
		capacity := decimal.NewFromFloat(allocation.Amount).Add(available)
		capture := decimal.Min(remaining, capacity)
		amount, _ := capture.Float64()
		if allocation.Amount == 0 && amount > 0 && eligible {
			if err := applySubscriptionWindowReservation(ctx, tx, id, transition, 0); err != nil {
				return err
			}
			allocation.DailyStart, allocation.WeeklyStart, allocation.MonthlyStart = &transition.DailyStart, &transition.WeeklyStart, &transition.MonthlyStart
		}
		if allocation.Amount > 0 || amount > 0 {
			if err := releasePriorityAllocation(ctx, tx, allocation, amount); err != nil {
				return err
			}
		}
		allocation.Amount = amount
		if amount > 0 {
			allocations = append(allocations, allocation)
		}
		remaining = remaining.Sub(capture)
	}
	record.Allocations, record.ActualAmount = allocations, cmd.SubscriptionCost
	if record.Policy.WalletFallback {
		record.WalletAmount, _ = remaining.Float64()
	} else {
		record.WalletAmount = 0
		record.UncoveredAmount, _ = remaining.Float64()
	}
	walletPreauthorized, err := settlePriorityWallet(ctx, tx, cmd, record)
	if err != nil {
		return err
	}
	result.BalanceFinalizationPending = walletPreauthorized
	record.Status, record.RequestFingerprint = service.BillingReservationCaptured, cmd.RequestFingerprint
	policyJSON, err := json.Marshal(record.Policy)
	if err != nil {
		return err
	}
	allocJSON, err := json.Marshal(allocations)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO billing_priority_reservations
 (request_id, api_key_id, user_id, primary_subscription_id, authorization_fingerprint, baseline_key,
 policy, allocations, authorized_amount, actual_amount, wallet_amount, uncovered_amount, request_fingerprint, status, expires_at)
 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,'captured',$14)
 ON CONFLICT (request_id, api_key_id) DO UPDATE SET allocations = EXCLUDED.allocations,
 actual_amount = EXCLUDED.actual_amount, wallet_amount = EXCLUDED.wallet_amount,
 uncovered_amount = EXCLUDED.uncovered_amount, request_fingerprint = EXCLUDED.request_fingerprint,
 status = 'captured', updated_at = NOW()`, record.RequestID, record.APIKeyID, record.UserID,
		record.PrimarySubscriptionID, record.AuthorizationFingerprint, record.BaselineKey, string(policyJSON), string(allocJSON),
		record.Amount, record.ActualAmount, record.WalletAmount, record.UncoveredAmount, record.RequestFingerprint, record.ExpiresAt)
	if err != nil {
		return err
	}
	result.PriorityFunding = record
	return nil
}

func settlePriorityWallet(ctx context.Context, tx *sql.Tx, cmd *service.UsageBillingCommand, record *service.PriorityFundingReservation) (bool, error) {
	walletID := service.PriorityWalletRequestID(cmd.RequestID)
	var status int16
	err := tx.QueryRowContext(ctx, `SELECT status FROM billing_balance_settlements WHERE request_id = $1 AND api_key_id = $2 FOR UPDATE`,
		walletID, cmd.APIKeyID).Scan(&status)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	walletCmd := *cmd
	walletCmd.RequestID, walletCmd.BalanceCost = walletID, record.WalletAmount
	if err == nil && status == service.BalanceSettlementAuthorized {
		return true, beginUsageBillingBalanceFinalization(ctx, tx, &walletCmd)
	}
	if err == nil && status == service.BalanceSettlementPrepared {
		return false, service.ErrUsageBillingRequestConflict
	}
	if record.WalletAmount == 0 {
		return false, nil
	}
	// Delivered usage must be recorded even if an attempted wallet top-up was
	// refused. The normal settlement worker also records any resulting debt.
	walletCmd.RequestID += ":settlement"
	return false, enqueueUsageBillingBalance(ctx, tx, &walletCmd)
}
