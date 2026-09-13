-- 243_preauthorization_baselines.sql
-- The previous provider-reported amount is the next request's admission hold.
-- Rows older than the application idle window are treated as a zero baseline.

CREATE TABLE IF NOT EXISTS billing_preauthorization_baselines (
    baseline_key TEXT PRIMARY KEY,
    last_actual_amount NUMERIC(20, 8) NOT NULL DEFAULT 0,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT billing_preauthorization_baselines_key_valid CHECK (BTRIM(baseline_key) <> ''),
    CONSTRAINT billing_preauthorization_baselines_amount_valid CHECK (
        last_actual_amount >= 0 AND last_actual_amount <> 'NaN'::numeric
    )
);

ALTER TABLE billing_balance_settlements
    ADD COLUMN IF NOT EXISTS baseline_key TEXT NOT NULL DEFAULT '';

ALTER TABLE billing_reservations
    ADD COLUMN IF NOT EXISTS baseline_key TEXT NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_billing_balance_settlements_baseline_key
    ON billing_balance_settlements (baseline_key)
    WHERE baseline_key <> '';

CREATE INDEX IF NOT EXISTS idx_billing_reservations_baseline_key
    ON billing_reservations (baseline_key)
    WHERE baseline_key <> '';
