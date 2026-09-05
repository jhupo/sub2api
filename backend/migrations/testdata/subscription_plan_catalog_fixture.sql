CREATE TEMP TABLE subscription_plans (
    id BIGINT PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    product_name VARCHAR(100) NOT NULL DEFAULT '',
    for_sale BOOLEAN NOT NULL DEFAULT FALSE,
    published_version_id BIGINT
);
INSERT INTO subscription_plans (id, name, description, product_name, for_sale, published_version_id) VALUES
    (1, 'Regular', '', '', TRUE, 101),
    (2, 'Private 32-day plan', '', '', FALSE, 102),
    (3, 'Monthly (legacy 32d)', 'Migrated legacy subscription entitlement', '__sub2api_legacy_group_1_days_32', FALSE, 103),
    (4, 'Monthly (legacy 30d)', '', '__sub2api_legacy_group_1_days_30', FALSE, 104),
    (5, 'Operator renamed', 'Operator notes', '__sub2api_legacy_group_2_days_7', FALSE, 105),
    (6, 'Promoted (legacy 30d)', '', '__sub2api_legacy_group_3_days_30', TRUE, 106),
    (7, 'Custom (legacy 32d)', '', '__sub2api_legacy_group_custom', FALSE, 107),
    (8, 'Mismatch (legacy 31d)', '', '__sub2api_legacy_group_4_days_32', FALSE, 108);

CREATE TEMP TABLE subscription_plan_versions (
    id BIGINT PRIMARY KEY, plan_id BIGINT REFERENCES subscription_plans(id),
    validity_days INT, monthly_limit_usd NUMERIC, price NUMERIC, version INT
);
INSERT INTO subscription_plan_versions VALUES (103, 3, 32, 220, 0.001, 1);
CREATE TEMP TABLE user_subscriptions (
    id BIGINT PRIMARY KEY, plan_id BIGINT REFERENCES subscription_plans(id),
    plan_version_id BIGINT REFERENCES subscription_plan_versions(id),
    starts_at TIMESTAMPTZ, expires_at TIMESTAMPTZ, monthly_usage_usd NUMERIC,
    monthly_reserved_usd NUMERIC
);
INSERT INTO user_subscriptions VALUES
    (1, 3, 103, '2026-08-20T08:00:00Z', '2026-09-21T08:00:00Z', 18.76, 2.5);
CREATE TEMP TABLE redeem_codes (id BIGINT PRIMARY KEY, plan_version_id BIGINT, status TEXT);
INSERT INTO redeem_codes VALUES (1, 103, 'unused');
CREATE TEMP TABLE payment_orders (id BIGINT PRIMARY KEY, plan_version_id BIGINT, entitlement_snapshot JSONB);
INSERT INTO payment_orders VALUES (1, 103, '{"validity_days":32,"monthly_limit_usd":220}');
CREATE TEMP TABLE users (id BIGINT PRIMARY KEY, balance NUMERIC);
INSERT INTO users VALUES (1, 51.2345);
CREATE TEMP TABLE settings (id BIGINT PRIMARY KEY, value JSONB);
INSERT INTO settings VALUES (1, '[{"plan_id":3}]');
CREATE TEMP TABLE announcements (id BIGINT PRIMARY KEY, targeting JSONB);
INSERT INTO announcements VALUES (1, '{"plan_ids":[3,4]}');
