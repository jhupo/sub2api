package repository

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

const preauthorizationBaselineIdleSeconds = 3600

func (r *usageBillingRepository) LoadPreauthorizationBaseline(ctx context.Context, key string) (float64, error) {
	if r == nil || r.db == nil {
		return 0, errors.New("usage billing repository db is nil")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return 0, service.ErrInvalidBillingPreauthorizationEstimate
	}
	var amount float64
	err := r.db.QueryRowContext(ctx, `
		SELECT CASE WHEN updated_at > NOW() - ($2 * INTERVAL '1 second')
			THEN last_actual_amount ELSE 0 END
		FROM billing_preauthorization_baselines
		WHERE baseline_key = $1
	`, key, preauthorizationBaselineIdleSeconds).Scan(&amount)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	if amount < 0 || math.IsNaN(amount) || math.IsInf(amount, 0) {
		return 0, service.ErrInvalidBillingPreauthorizationEstimate
	}
	return service.QuantizeUsageBillingAmount(amount), nil
}

func (r *usageBillingRepository) RecordPreauthorizationBaseline(ctx context.Context, key string, amount float64) error {
	if r == nil || r.db == nil {
		return errors.New("usage billing repository db is nil")
	}
	key = strings.TrimSpace(key)
	amount = service.QuantizeUsageBillingAmount(amount)
	if key == "" || amount < 0 || math.IsNaN(amount) || math.IsInf(amount, 0) {
		return service.ErrInvalidBillingPreauthorizationEstimate
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO billing_preauthorization_baselines (baseline_key, last_actual_amount, updated_at)
		VALUES ($1, $2, NOW())
		ON CONFLICT (baseline_key) DO UPDATE
		SET last_actual_amount = EXCLUDED.last_actual_amount, updated_at = NOW()
	`, key, amount)
	return err
}
