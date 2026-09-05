package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

const defaultBalancePreauthorizationTTL = 6 * time.Hour

func (r *usageBillingRepository) PrepareBalancePreauthorization(
	ctx context.Context,
	cmd *service.BalancePreauthorizationCommand,
) (*service.BalancePreauthorizationRecord, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("usage billing repository db is nil")
	}
	if cmd == nil {
		return nil, errors.New("balance preauthorization command is nil")
	}
	requestID := strings.TrimSpace(cmd.RequestID)
	fingerprint := strings.TrimSpace(cmd.AuthorizationFingerprint)
	holdAmount := service.QuantizeUsageBillingAmount(cmd.HoldAmount)
	if requestID == "" || fingerprint == "" || cmd.APIKeyID <= 0 || cmd.UserID <= 0 ||
		holdAmount < 0 || math.IsNaN(holdAmount) || math.IsInf(holdAmount, 0) {
		return nil, service.ErrInvalidBillingPreauthorizationEstimate
	}
	expiresAt := cmd.ExpiresAt
	if expiresAt.IsZero() {
		expiresAt = time.Now().Add(defaultBalancePreauthorizationTTL)
	} else if !expiresAt.After(time.Now()) {
		return nil, service.ErrInvalidBillingPreauthorizationEstimate
	}

	record := &service.BalancePreauthorizationRecord{}
	err := r.db.QueryRowContext(ctx, `
		INSERT INTO billing_balance_settlements (
			request_id,
			api_key_id,
			request_fingerprint,
			authorization_fingerprint,
			user_id,
			amount_usd,
			hold_usd,
			status,
			expires_at
		)
		SELECT $1, $2, '', $3, $4, 0, $5, $6, $7
		WHERE NOT EXISTS (
			SELECT 1 FROM usage_billing_dedup
			WHERE request_id = $1 AND api_key_id = $2
		)
		AND NOT EXISTS (
			SELECT 1 FROM usage_billing_dedup_archive
			WHERE request_id = $1 AND api_key_id = $2
		)
		ON CONFLICT (request_id, api_key_id) DO UPDATE
		SET request_id = billing_balance_settlements.request_id
		WHERE billing_balance_settlements.authorization_fingerprint = EXCLUDED.authorization_fingerprint
			AND billing_balance_settlements.user_id = EXCLUDED.user_id
			AND billing_balance_settlements.hold_usd = EXCLUDED.hold_usd
			AND (
				billing_balance_settlements.status NOT IN ($8, $9)
				OR billing_balance_settlements.expires_at > NOW()
			)
		RETURNING request_id,
			api_key_id,
			user_id,
			request_fingerprint,
			authorization_fingerprint,
			hold_usd,
			amount_usd,
			status,
			expires_at,
			updated_at
	`, requestID, cmd.APIKeyID, fingerprint, cmd.UserID, holdAmount,
		service.BalanceSettlementPrepared,
		expiresAt,
		service.BalanceSettlementPrepared,
		service.BalanceSettlementAuthorized,
	).Scan(
		&record.RequestID,
		&record.APIKeyID,
		&record.UserID,
		&record.RequestFingerprint,
		&record.AuthorizationFingerprint,
		&record.HoldAmount,
		&record.Amount,
		&record.Status,
		&record.ExpiresAt,
		&record.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, service.ErrUsageBillingRequestConflict
	}
	if err != nil {
		return nil, err
	}
	return record, nil
}

func (r *usageBillingRepository) BindGrokVideoPendingBilling(
	ctx context.Context,
	binding service.GrokVideoPendingBillingBinding,
) error {
	if r == nil || r.db == nil {
		return errors.New("usage billing repository db is nil")
	}
	taskID := strings.TrimSpace(binding.TaskID)
	pending := binding.Pending
	pending.PreauthorizationRequestID = strings.TrimSpace(pending.PreauthorizationRequestID)
	pending.AuthorizationFingerprint = strings.TrimSpace(pending.AuthorizationFingerprint)
	pending.PreauthorizationHoldAmount = service.QuantizeUsageBillingAmount(pending.PreauthorizationHoldAmount)
	if taskID == "" || binding.APIKeyID <= 0 || binding.UserID <= 0 ||
		!service.IsGrokVideoHoldRequestID(pending.PreauthorizationRequestID) ||
		pending.AuthorizationFingerprint == "" || pending.PreauthorizationHoldAmount < 0 ||
		math.IsNaN(pending.PreauthorizationHoldAmount) || math.IsInf(pending.PreauthorizationHoldAmount, 0) {
		return service.ErrInvalidBillingPreauthorizationEstimate
	}
	metadata, err := json.Marshal(pending)
	if err != nil {
		return err
	}
	var (
		boundRequestID string
		query          string
		status         any
	)
	switch pending.FundingSource {
	case service.FundingSourceWallet:
		query = `
			UPDATE billing_balance_settlements
			SET async_task_id = $1, async_metadata = $5::jsonb, updated_at = NOW()
			WHERE request_id = $2 AND api_key_id = $3 AND user_id = $4
				AND status = $6 AND expires_at > NOW() AND $7::bigint IS NULL
				AND (async_task_id IS NULL OR (async_task_id = $1 AND async_metadata = $5::jsonb))
			RETURNING request_id`
		status = service.BalanceSettlementAuthorized
	case service.FundingSourceSubscription:
		if pending.SubscriptionID == nil || *pending.SubscriptionID <= 0 {
			return service.ErrInvalidBillingPreauthorizationEstimate
		}
		query = `
			UPDATE billing_reservations
			SET async_task_id = $1, async_metadata = $5::jsonb, updated_at = NOW()
			WHERE request_id = $2 AND api_key_id = $3 AND user_id = $4
				AND funding_source = $8 AND subscription_id = $7 AND status = $6 AND expires_at > NOW()
				AND (async_task_id IS NULL OR (async_task_id = $1 AND async_metadata = $5::jsonb))
			RETURNING request_id`
		status = service.BillingReservationAuthorized
	default:
		return service.ErrInvalidBillingPreauthorizationEstimate
	}
	var subscriptionID any
	if pending.SubscriptionID != nil {
		subscriptionID = *pending.SubscriptionID
	}
	args := []any{taskID, pending.PreauthorizationRequestID, binding.APIKeyID, binding.UserID,
		string(metadata), status, subscriptionID}
	if pending.FundingSource == service.FundingSourceSubscription {
		args = append(args, service.FundingSourceSubscription)
	}
	err = r.db.QueryRowContext(ctx, query, args...).Scan(&boundRequestID)
	if errors.Is(err, sql.ErrNoRows) {
		return service.ErrUsageBillingRequestConflict
	}
	return err
}

func (r *usageBillingRepository) LoadGrokVideoPendingBilling(
	ctx context.Context,
	taskID string,
	userID, apiKeyID int64,
) (*service.GrokVideoPendingBilling, error) {
	if r == nil || r.db == nil {
		return nil, errors.New("usage billing repository db is nil")
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" || userID <= 0 || apiKeyID <= 0 {
		return nil, service.ErrInvalidBillingPreauthorizationEstimate
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT async_metadata, request_id, authorization_fingerprint, hold_amount,
			funding_source, subscription_id
		FROM (
			SELECT async_metadata, request_id, authorization_fingerprint,
				hold_usd AS hold_amount, $4::varchar AS funding_source,
				NULL::bigint AS subscription_id, async_task_id, api_key_id, user_id
			FROM billing_balance_settlements
			UNION ALL
			SELECT async_metadata, request_id, authorization_fingerprint,
				authorized_amount AS hold_amount, funding_source,
				subscription_id, async_task_id, api_key_id, user_id
			FROM billing_reservations
		) reservations
		WHERE async_task_id = $1 AND api_key_id = $2 AND user_id = $3
	`, taskID, apiKeyID, userID, service.FundingSourceWallet)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var (
		metadata, foundMetadata             []byte
		requestID, authorization, source    string
		foundRequestID, foundAuthorization  string
		foundSource                         string
		holdAmount, foundHoldAmount         float64
		subscriptionID, foundSubscriptionID sql.NullInt64
		matches                             int
	)
	for rows.Next() {
		if err := rows.Scan(&metadata, &requestID, &authorization, &holdAmount, &source, &subscriptionID); err != nil {
			return nil, err
		}
		matches++
		foundMetadata = append(foundMetadata[:0], metadata...)
		foundRequestID, foundAuthorization, foundHoldAmount = requestID, authorization, holdAmount
		foundSource, foundSubscriptionID = source, subscriptionID
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if matches == 0 {
		return nil, nil
	}
	if matches != 1 {
		return nil, service.ErrUsageBillingRequestConflict
	}
	var pending service.GrokVideoPendingBilling
	if err := json.Unmarshal(foundMetadata, &pending); err != nil {
		return nil, err
	}
	foundHoldAmount = service.QuantizeUsageBillingAmount(foundHoldAmount)
	if pending.PreauthorizationRequestID != foundRequestID ||
		pending.AuthorizationFingerprint != foundAuthorization ||
		service.QuantizeUsageBillingAmount(pending.PreauthorizationHoldAmount) != foundHoldAmount ||
		pending.FundingSource != foundSource ||
		valueOrZeroInt64(pending.SubscriptionID) != foundSubscriptionID.Int64 ||
		!service.IsGrokVideoHoldRequestID(foundRequestID) {
		return nil, service.ErrUsageBillingRequestConflict
	}
	return &pending, nil
}

func valueOrZeroInt64(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}
