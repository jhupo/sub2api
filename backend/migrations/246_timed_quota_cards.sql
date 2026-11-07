ALTER TABLE subscription_plan_versions
    ADD COLUMN card_mode VARCHAR(16) NOT NULL DEFAULT '',
    ADD COLUMN validity_hours INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN deadline_at TIMESTAMPTZ,
    ADD COLUMN total_limit_usd DECIMAL(20,10),
    ADD CONSTRAINT subscription_card_terms_check CHECK (
        card_mode = '' OR
        (total_limit_usd IS NOT NULL AND total_limit_usd > 0 AND daily_limit_usd IS NULL AND weekly_limit_usd IS NULL AND monthly_limit_usd IS NULL
            AND ((card_mode = 'hours' AND validity_hours BETWEEN 1 AND 87600 AND deadline_at IS NULL)
                OR (card_mode = 'deadline' AND deadline_at IS NOT NULL AND validity_hours = 0)))
    );

ALTER TABLE user_subscriptions
    ADD COLUMN is_quota_card BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN renewal_count INTEGER NOT NULL DEFAULT 0,
    ADD COLUMN total_usage_usd DECIMAL(20,10) NOT NULL DEFAULT 0,
    ADD COLUMN total_reserved_usd DECIMAL(20,10) NOT NULL DEFAULT 0;

UPDATE user_subscriptions us SET total_reserved_usd = COALESCE((
    SELECT SUM(br.authorized_amount) FROM billing_reservations br
    WHERE br.subscription_id = us.id AND br.funding_source = 'subscription'
        AND br.status IN ('authorized', 'finalizing')
), 0);

-- Legacy rows whose term was extended are conservatively protected from self-revocation.
UPDATE user_subscriptions us SET renewal_count = 1
FROM subscription_plan_versions pv
WHERE pv.id = us.plan_version_id AND us.expires_at > us.starts_at +
    make_interval(days => pv.validity_days * CASE WHEN pv.validity_unit IN ('week', 'weeks') THEN 7 WHEN pv.validity_unit IN ('month', 'months') THEN 30 ELSE 1 END)
    + INTERVAL '1 minute';

DROP INDEX user_subscriptions_user_plan_version_unique_active;
CREATE UNIQUE INDEX user_subscriptions_user_plan_version_unique_active
    ON user_subscriptions(user_id, plan_version_id)
    WHERE deleted_at IS NULL AND is_quota_card = FALSE;

ALTER TABLE api_keys
    ADD COLUMN subscription_priority JSONB,
    ADD COLUMN allow_wallet_fallback BOOLEAN NOT NULL DEFAULT FALSE,
    ADD CONSTRAINT api_keys_subscription_priority_array CHECK (subscription_priority IS NULL OR jsonb_typeof(subscription_priority) = 'array');
