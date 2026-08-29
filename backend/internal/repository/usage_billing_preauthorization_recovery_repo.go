package repository

import (
	"context"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

const (
	defaultBalancePreauthorizationRecoveryBatch = 500
	maxBalancePreauthorizationRecoveryBatch     = 5000
	balancePreauthorizationRecoveryLease        = time.Minute
)

func (r *usageBillingRepository) ListRecoverableBalancePreauthorizations(
	ctx context.Context,
	authorizationExpiredBefore time.Time,
	finalizationStaleBefore time.Time,
	limit int,
) ([]service.BalancePreauthorizationRecord, error) {
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
		limit = defaultBalancePreauthorizationRecoveryBatch
	} else if limit > maxBalancePreauthorizationRecoveryBatch {
		limit = maxBalancePreauthorizationRecoveryBatch
	}

	rows, err := r.db.QueryContext(ctx, `
		WITH candidates AS (
			SELECT id, expires_at
			FROM billing_balance_settlements
			WHERE (
					status IN ($4, $5)
					AND expires_at <= $1
					AND updated_at <= NOW() - ($7 * INTERVAL '1 second')
				)
				OR (
					status = $6
					AND updated_at <= $2
				)
			ORDER BY CASE WHEN status = $6 THEN updated_at ELSE expires_at END, id
			LIMIT $3
			FOR UPDATE SKIP LOCKED
		), leased AS (
			UPDATE billing_balance_settlements AS settlement
			SET updated_at = NOW()
			FROM candidates
			WHERE settlement.id = candidates.id
			RETURNING settlement.request_id,
				settlement.api_key_id,
				settlement.user_id,
				settlement.request_fingerprint,
				settlement.authorization_fingerprint,
				settlement.hold_usd,
				settlement.amount_usd,
				settlement.status,
				settlement.expires_at,
				COALESCE(settlement.async_task_id, ''),
				settlement.updated_at
		)
		SELECT request_id,
			api_key_id,
			user_id,
			request_fingerprint,
			authorization_fingerprint,
			hold_usd,
			amount_usd,
			status,
			expires_at,
			async_task_id,
			updated_at
		FROM leased
	`, authorizationExpiredBefore, finalizationStaleBefore, limit,
		service.BalanceSettlementPrepared,
		service.BalanceSettlementAuthorized,
		service.BalanceSettlementFinalizationPending,
		int64(balancePreauthorizationRecoveryLease/time.Second),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	records := make([]service.BalancePreauthorizationRecord, 0, limit)
	for rows.Next() {
		var record service.BalancePreauthorizationRecord
		if err := rows.Scan(
			&record.RequestID,
			&record.APIKeyID,
			&record.UserID,
			&record.RequestFingerprint,
			&record.AuthorizationFingerprint,
			&record.HoldAmount,
			&record.Amount,
			&record.Status,
			&record.ExpiresAt,
			&record.AsyncTaskID,
			&record.UpdatedAt,
		); err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return records, nil
}
