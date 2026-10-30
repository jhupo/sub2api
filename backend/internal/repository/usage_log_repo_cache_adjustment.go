package repository

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func (r *usageLogRepository) GetCacheReadAdjustmentDaily(ctx context.Context, start, end time.Time, zone string, accountID int64) ([]service.CacheReadAdjustmentDay, error) {
	query := `SELECT TO_CHAR(created_at AT TIME ZONE $3, 'YYYY-MM-DD'), account_id,
		COUNT(*), COUNT(cache_read_adjustment->>'additional_actual_cost'),
		COALESCE(SUM((cache_read_adjustment->>'shifted_tokens')::bigint), 0),
		COALESCE(SUM((cache_read_adjustment->>'additional_actual_cost')::numeric), 0),
		COALESCE(SUM((cache_read_adjustment->>'additional_actual_cost')::numeric) FILTER (WHERE billing_type = 0), 0),
		COALESCE(SUM((cache_read_adjustment->>'additional_actual_cost')::numeric) FILTER (WHERE billing_type = 1), 0)
		FROM usage_logs WHERE created_at >= $1 AND created_at < $2
		AND cache_read_adjustment IS NOT NULL`
	args := []any{start, end, zone}
	if accountID > 0 {
		query += " AND account_id = $4"
		args = append(args, accountID)
	}
	query += " GROUP BY 1, account_id ORDER BY 1 DESC, account_id"
	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make([]service.CacheReadAdjustmentDay, 0)
	for rows.Next() {
		var day service.CacheReadAdjustmentDay
		if err := rows.Scan(&day.Date, &day.AccountID, &day.Requests, &day.PricedRequests,
			&day.ShiftedTokens, &day.AdditionalCost, &day.BalanceAdditionalCost, &day.SubscriptionAdditionalCost); err != nil {
			return nil, err
		}
		result = append(result, day)
	}
	return result, rows.Err()
}
