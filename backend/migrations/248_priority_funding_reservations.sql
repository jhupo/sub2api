CREATE TABLE IF NOT EXISTS billing_priority_reservations (
    request_id TEXT NOT NULL,
    api_key_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL,
    primary_subscription_id BIGINT NOT NULL,
    authorization_fingerprint TEXT NOT NULL,
    baseline_key TEXT NOT NULL DEFAULT '',
    policy JSONB NOT NULL,
    allocations JSONB NOT NULL DEFAULT '[]'::jsonb,
    authorized_amount NUMERIC(20,8) NOT NULL DEFAULT 0 CHECK (authorized_amount >= 0),
    actual_amount NUMERIC(20,8) NOT NULL DEFAULT 0 CHECK (actual_amount >= 0),
    wallet_amount NUMERIC(20,8) NOT NULL DEFAULT 0 CHECK (wallet_amount >= 0),
    uncovered_amount NUMERIC(20,8) NOT NULL DEFAULT 0 CHECK (uncovered_amount >= 0),
    request_fingerprint TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL CHECK (status IN ('authorized', 'captured', 'released')),
    expires_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    async_task_id TEXT,
    async_metadata JSONB,
    PRIMARY KEY (request_id, api_key_id)
);

CREATE INDEX IF NOT EXISTS billing_priority_reservations_recovery_idx
    ON billing_priority_reservations (expires_at)
    WHERE status = 'authorized';

CREATE INDEX IF NOT EXISTS billing_priority_reservations_user_pending_idx
    ON billing_priority_reservations (user_id) WHERE status = 'authorized';

CREATE INDEX IF NOT EXISTS billing_priority_reservations_async_task_idx
    ON billing_priority_reservations (async_task_id, api_key_id, user_id)
    WHERE async_task_id IS NOT NULL;
