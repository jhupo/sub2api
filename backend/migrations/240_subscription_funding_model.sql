-- Decouple subscription entitlements from routing groups.
--
-- The migration is intentionally additive. Legacy columns keep their original
-- values for rollback and audit purposes, while all new runtime relationships
-- are populated before their constraints are enabled.

ALTER TABLE subscription_plans
    ADD COLUMN IF NOT EXISTS published_version_id BIGINT;

CREATE TABLE IF NOT EXISTS subscription_plan_versions (
    id                  BIGSERIAL PRIMARY KEY,
    plan_id             BIGINT NOT NULL REFERENCES subscription_plans(id) ON DELETE CASCADE,
    version             INT NOT NULL,
    price               DECIMAL(20, 3) NOT NULL,
    original_price      DECIMAL(20, 3),
    currency            VARCHAR(3) NOT NULL DEFAULT '',
    validity_days       INT NOT NULL,
    validity_unit       VARCHAR(10) NOT NULL DEFAULT 'days',
    daily_limit_usd     DECIMAL(20, 10),
    weekly_limit_usd    DECIMAL(20, 10),
    monthly_limit_usd   DECIMAL(20, 10),
    published_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT subscription_plan_versions_plan_version_key UNIQUE (plan_id, version),
    CONSTRAINT subscription_plan_versions_id_plan_id_key UNIQUE (id, plan_id),
    CONSTRAINT subscription_plan_versions_price_check CHECK (price > 0),
    CONSTRAINT subscription_plan_versions_original_price_check CHECK (original_price IS NULL OR original_price >= 0),
    CONSTRAINT subscription_plan_versions_validity_check CHECK (validity_days > 0),
    CONSTRAINT subscription_plan_versions_daily_limit_check CHECK (daily_limit_usd IS NULL OR daily_limit_usd >= 0),
    CONSTRAINT subscription_plan_versions_weekly_limit_check CHECK (weekly_limit_usd IS NULL OR weekly_limit_usd >= 0),
    CONSTRAINT subscription_plan_versions_monthly_limit_check CHECK (monthly_limit_usd IS NULL OR monthly_limit_usd >= 0)
);

-- This table is a new runtime boundary just like billing_reservations. If a
-- previous interrupted release left a partial relation behind, fail closed
-- instead of silently accepting a shape that the Ent/runtime code cannot use.
DO $$
DECLARE
    existing_table REGCLASS;
    missing_columns TEXT;
    missing_constraints TEXT;
BEGIN
    existing_table := to_regclass(current_schema() || '.subscription_plan_versions');
    IF existing_table IS NULL THEN
        RETURN;
    END IF;

    SELECT string_agg(required.column_name, ', ' ORDER BY required.column_name)
    INTO missing_columns
    FROM (VALUES
        ('id'), ('plan_id'), ('version'), ('price'), ('original_price'),
        ('currency'), ('validity_days'), ('validity_unit'),
        ('daily_limit_usd'), ('weekly_limit_usd'), ('monthly_limit_usd'),
        ('published_at')
    ) AS required(column_name)
    WHERE NOT EXISTS (
        SELECT 1
        FROM information_schema.columns c
        WHERE c.table_schema = current_schema()
          AND c.table_name = 'subscription_plan_versions'
          AND c.column_name = required.column_name
    );

    IF missing_columns IS NOT NULL THEN
        RAISE EXCEPTION
            'subscription_plan_versions exists with an incomplete schema; missing required columns: %',
            missing_columns;
    END IF;

    SELECT string_agg(required.constraint_name, ', ' ORDER BY required.constraint_name)
    INTO missing_constraints
    FROM (VALUES
        ('subscription_plan_versions_pkey'),
        ('subscription_plan_versions_plan_id_fkey'),
        ('subscription_plan_versions_plan_version_key'),
        ('subscription_plan_versions_id_plan_id_key'),
        ('subscription_plan_versions_price_check'),
        ('subscription_plan_versions_original_price_check'),
        ('subscription_plan_versions_validity_check'),
        ('subscription_plan_versions_daily_limit_check'),
        ('subscription_plan_versions_weekly_limit_check'),
        ('subscription_plan_versions_monthly_limit_check')
    ) AS required(constraint_name)
    WHERE NOT EXISTS (
        SELECT 1
        FROM pg_constraint c
        WHERE c.conrelid = existing_table
          AND c.conname = required.constraint_name
    );

    IF missing_constraints IS NOT NULL THEN
        RAISE EXCEPTION
            'subscription_plan_versions exists without required constraints: %',
            missing_constraints;
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS idx_subscription_plan_versions_published_at
    ON subscription_plan_versions(published_at);
CREATE INDEX IF NOT EXISTS idx_subscription_plans_published_version_id
    ON subscription_plans(published_version_id);

-- Collect every legacy entitlement shape before creating catalog shells. The
-- product_name marker is migration-owned and gives reruns a deterministic key.
CREATE TEMP TABLE subscription_legacy_pairs (
    group_id BIGINT NOT NULL,
    validity_days INT NOT NULL,
    PRIMARY KEY (group_id, validity_days)
) ON COMMIT DROP;

INSERT INTO subscription_legacy_pairs (group_id, validity_days)
SELECT g.id, LEAST(36500, GREATEST(g.default_validity_days, 1))
FROM groups g
WHERE g.subscription_type = 'subscription'
ON CONFLICT DO NOTHING;

INSERT INTO subscription_legacy_pairs (group_id, validity_days)
SELECT
    us.group_id,
    LEAST(
        36500,
        GREATEST(1, CEIL(EXTRACT(EPOCH FROM (us.expires_at - us.starts_at)) / 86400.0)::INT)
    )
FROM user_subscriptions us
WHERE us.group_id IS NOT NULL
ON CONFLICT DO NOTHING;

INSERT INTO subscription_legacy_pairs (group_id, validity_days)
SELECT
    rc.group_id,
    CASE
        WHEN rc.validity_days = 0 THEN 30
        ELSE LEAST(36500, GREATEST(rc.validity_days, 1))
    END
FROM redeem_codes rc
WHERE rc.type = 'subscription'
  AND rc.validity_days >= 0
  AND rc.group_id IS NOT NULL
ON CONFLICT DO NOTHING;

INSERT INTO subscription_legacy_pairs (group_id, validity_days)
SELECT sp.group_id, LEAST(36500, GREATEST(sp.validity_days, 1))
FROM subscription_plans sp
WHERE sp.group_id IS NOT NULL
ON CONFLICT DO NOTHING;

-- Older payment rows also carried the group and term directly. Include that
-- shape before creating catalog shells so an order can be recovered even when
-- its plan_id was not populated by the older writer.
INSERT INTO subscription_legacy_pairs (group_id, validity_days)
SELECT
    po.subscription_group_id,
    CASE
        WHEN COALESCE(po.subscription_days, 0) <= 0 THEN 30
        ELSE LEAST(36500, GREATEST(po.subscription_days, 1))
    END
FROM payment_orders po
WHERE po.order_type = 'subscription'
  AND po.subscription_group_id IS NOT NULL
ON CONFLICT DO NOTHING;

-- Settings are TEXT, so malformed operator-edited JSON must not abort the
-- release. Invalid entries were ignored by the old application and remain so.
DO $$
DECLARE
    setting_row RECORD;
    raw_items JSONB;
    item JSONB;
    legacy_group_id BIGINT;
    legacy_validity_days INT;
BEGIN
    FOR setting_row IN
        SELECT id, value
        FROM settings
        WHERE key = 'default_subscriptions'
           OR key LIKE 'auth_source_default_%_subscriptions'
    LOOP
        BEGIN
            raw_items := setting_row.value::JSONB;
        EXCEPTION WHEN OTHERS THEN
            CONTINUE;
        END;
        IF jsonb_typeof(raw_items) <> 'array' THEN
            CONTINUE;
        END IF;

        FOR item IN SELECT value FROM jsonb_array_elements(raw_items)
        LOOP
            IF jsonb_typeof(item) <> 'object'
               OR jsonb_typeof(item -> 'group_id') <> 'number'
               OR jsonb_typeof(item -> 'validity_days') <> 'number' THEN
                CONTINUE;
            END IF;
            BEGIN
                legacy_group_id := (item ->> 'group_id')::BIGINT;
                legacy_validity_days := (item ->> 'validity_days')::INT;
                IF legacy_validity_days <= 0 THEN
                    CONTINUE;
                END IF;
                legacy_validity_days := LEAST(36500, legacy_validity_days);
            EXCEPTION WHEN OTHERS THEN
                CONTINUE;
            END;
            INSERT INTO subscription_legacy_pairs (group_id, validity_days)
            SELECT g.id, legacy_validity_days
            FROM groups g
            WHERE g.id = legacy_group_id
            ON CONFLICT DO NOTHING;
        END LOOP;
    END LOOP;
END $$;

-- Announcement subscription conditions used group IDs. Include those groups
-- even if no entitlement was ever issued, so targeting does not disappear.
DO $$
DECLARE
    announcement_row RECORD;
    any_group JSONB;
    condition JSONB;
    raw_group_id JSONB;
    legacy_group_id BIGINT;
BEGIN
    FOR announcement_row IN
        SELECT targeting
        FROM announcements
        WHERE jsonb_typeof(targeting -> 'any_of') = 'array'
    LOOP
        FOR any_group IN SELECT value FROM jsonb_array_elements(announcement_row.targeting -> 'any_of')
        LOOP
            IF jsonb_typeof(any_group -> 'all_of') <> 'array' THEN
                CONTINUE;
            END IF;
            FOR condition IN SELECT value FROM jsonb_array_elements(any_group -> 'all_of')
            LOOP
                IF condition ->> 'type' <> 'subscription'
                   OR jsonb_typeof(condition -> 'group_ids') <> 'array' THEN
                    CONTINUE;
                END IF;
                FOR raw_group_id IN SELECT value FROM jsonb_array_elements(condition -> 'group_ids')
                LOOP
                    IF jsonb_typeof(raw_group_id) <> 'number' THEN
                        CONTINUE;
                    END IF;
                    BEGIN
                        legacy_group_id := raw_group_id::TEXT::BIGINT;
                    EXCEPTION WHEN OTHERS THEN
                        CONTINUE;
                    END;
                    INSERT INTO subscription_legacy_pairs (group_id, validity_days)
                    SELECT g.id, LEAST(36500, GREATEST(g.default_validity_days, 1))
                    FROM groups g
                    WHERE g.id = legacy_group_id
                    ON CONFLICT DO NOTHING;
                END LOOP;
            END LOOP;
        END LOOP;
    END LOOP;
END $$;

INSERT INTO subscription_plans (
    group_id, name, description, price, original_price, currency,
    validity_days, validity_unit, features, product_name, for_sale, sort_order,
    created_at, updated_at
)
SELECT
    pair.group_id,
    LEFT(g.name, 60) || ' (legacy ' || pair.validity_days || 'd)',
    'Migrated legacy subscription entitlement',
    0.001,
    NULL,
    '',
    pair.validity_days,
    'days',
    '',
    '__sub2api_legacy_group_' || pair.group_id || '_days_' || pair.validity_days,
    FALSE,
    g.sort_order,
    NOW(),
    NOW()
FROM subscription_legacy_pairs pair
JOIN groups g ON g.id = pair.group_id
WHERE NOT EXISTS (
    SELECT 1
    FROM subscription_plans existing
    WHERE existing.product_name = '__sub2api_legacy_group_' || pair.group_id || '_days_' || pair.validity_days
);

-- Existing storefront plans retain their own identity. Legacy zero limits meant
-- unlimited, so only positive group limits become finite version allowances.
INSERT INTO subscription_plan_versions (
    plan_id, version, price, original_price, currency, validity_days,
    validity_unit, daily_limit_usd, weekly_limit_usd, monthly_limit_usd,
    published_at
)
SELECT
    sp.id,
    1,
    GREATEST(COALESCE(sp.price, 0), 0.001),
    sp.original_price,
    COALESCE(sp.currency, ''),
    GREATEST(sp.validity_days, 1),
    COALESCE(NULLIF(sp.validity_unit, ''), 'days'),
    CASE WHEN g.daily_limit_usd > 0 THEN g.daily_limit_usd ELSE NULL END,
    CASE WHEN g.weekly_limit_usd > 0 THEN g.weekly_limit_usd ELSE NULL END,
    CASE WHEN g.monthly_limit_usd > 0 THEN g.monthly_limit_usd ELSE NULL END,
    COALESCE(sp.created_at, NOW())
FROM subscription_plans sp
LEFT JOIN groups g ON g.id = sp.group_id
WHERE NOT EXISTS (
    SELECT 1
    FROM subscription_plan_versions existing
    WHERE existing.plan_id = sp.id
);

UPDATE subscription_plans sp
SET published_version_id = (
    SELECT spv.id
    FROM subscription_plan_versions spv
    WHERE spv.plan_id = sp.id
    ORDER BY spv.version DESC, spv.id DESC
    LIMIT 1
)
WHERE sp.published_version_id IS NULL;

ALTER TABLE user_subscriptions
    ADD COLUMN IF NOT EXISTS plan_id BIGINT,
    ADD COLUMN IF NOT EXISTS plan_version_id BIGINT,
    ADD COLUMN IF NOT EXISTS daily_reserved_usd DECIMAL(20, 10) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS weekly_reserved_usd DECIMAL(20, 10) NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS monthly_reserved_usd DECIMAL(20, 10) NOT NULL DEFAULT 0;

UPDATE user_subscriptions us
SET plan_id = legacy_plan.id,
    plan_version_id = legacy_plan.published_version_id
FROM subscription_plans legacy_plan
WHERE us.group_id IS NOT NULL
  AND legacy_plan.group_id = us.group_id
  AND legacy_plan.product_name =
      '__sub2api_legacy_group_' || us.group_id || '_days_' ||
      LEAST(
          36500,
          GREATEST(1, CEIL(EXTRACT(EPOCH FROM (us.expires_at - us.starts_at)) / 86400.0)::INT)
      )
  AND (us.plan_id IS NULL OR us.plan_version_id IS NULL);

-- Preserve rejection for keys on a legacy subscription group whose owner had
-- no entitlement. They receive an explicit expired entitlement instead of a
-- silent wallet fallback.
INSERT INTO user_subscriptions (
    user_id, group_id, plan_id, plan_version_id,
    starts_at, expires_at, status, assigned_at,
    daily_usage_usd, weekly_usage_usd, monthly_usage_usd,
    daily_reserved_usd, weekly_reserved_usd, monthly_reserved_usd,
    created_at, updated_at
)
SELECT DISTINCT ON (ak.user_id, ak.group_id)
    ak.user_id,
    ak.group_id,
    legacy_plan.id,
    legacy_plan.published_version_id,
    NOW(),
    NOW(),
    'expired',
    NOW(),
    0, 0, 0, 0, 0, 0,
    NOW(),
    NOW()
FROM api_keys ak
JOIN groups g
  ON g.id = ak.group_id
 AND g.subscription_type = 'subscription'
JOIN subscription_plans legacy_plan
  ON legacy_plan.group_id = g.id
 AND legacy_plan.product_name =
     '__sub2api_legacy_group_' || g.id || '_days_' ||
     LEAST(36500, GREATEST(g.default_validity_days, 1))
WHERE ak.deleted_at IS NULL
  AND NOT EXISTS (
      SELECT 1
      FROM user_subscriptions us
      WHERE us.user_id = ak.user_id
        AND us.group_id = ak.group_id
        AND us.deleted_at IS NULL
  )
ORDER BY ak.user_id, ak.group_id, ak.id;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM user_subscriptions
        WHERE plan_id IS NULL OR plan_version_id IS NULL
    ) THEN
        RAISE EXCEPTION 'subscription migration left user subscriptions without a plan version';
    END IF;
END $$;

ALTER TABLE user_subscriptions ALTER COLUMN group_id DROP NOT NULL;
ALTER TABLE user_subscriptions ALTER COLUMN plan_id SET NOT NULL;
ALTER TABLE user_subscriptions ALTER COLUMN plan_version_id SET NOT NULL;

-- group_id is retained only as a legacy/audit reference.  It is no longer the
-- ownership boundary for an entitlement: a routing-group deletion must not
-- cascade into a user's purchased subscription.  Drop the legacy CASCADE FK
-- after all rows have been mapped to an immutable plan version.
ALTER TABLE user_subscriptions DROP CONSTRAINT IF EXISTS user_subscriptions_group_id_fkey;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM user_subscriptions us
        LEFT JOIN subscription_plan_versions spv ON spv.id = us.plan_version_id
        WHERE spv.id IS NULL OR spv.plan_id IS DISTINCT FROM us.plan_id
    ) THEN
        RAISE EXCEPTION
            'subscription migration found user subscriptions with an orphaned or mismatched plan version';
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'subscription_plan_versions'::regclass
          AND conname = 'subscription_plan_versions_id_plan_id_key'
    ) THEN
        ALTER TABLE subscription_plan_versions
            ADD CONSTRAINT subscription_plan_versions_id_plan_id_key UNIQUE (id, plan_id);
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'user_subscriptions'::regclass
          AND conname = 'user_subscriptions_plan_id_fkey'
    ) THEN
        ALTER TABLE user_subscriptions
            ADD CONSTRAINT user_subscriptions_plan_id_fkey
            FOREIGN KEY (plan_id) REFERENCES subscription_plans(id) ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'user_subscriptions'::regclass
          AND conname = 'user_subscriptions_plan_version_id_fkey'
    ) THEN
        ALTER TABLE user_subscriptions
            ADD CONSTRAINT user_subscriptions_plan_version_id_fkey
            FOREIGN KEY (plan_version_id) REFERENCES subscription_plan_versions(id) ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'user_subscriptions'::regclass
          AND conname = 'user_subscriptions_plan_version_plan_id_fkey'
    ) THEN
        ALTER TABLE user_subscriptions
            ADD CONSTRAINT user_subscriptions_plan_version_plan_id_fkey
            FOREIGN KEY (plan_version_id, plan_id)
            REFERENCES subscription_plan_versions(id, plan_id)
            ON DELETE RESTRICT;
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'user_subscriptions'::regclass
          AND conname = 'user_subscriptions_id_user_id_key'
    ) THEN
        ALTER TABLE user_subscriptions
            ADD CONSTRAINT user_subscriptions_id_user_id_key UNIQUE (id, user_id);
    END IF;
END $$;

DROP INDEX IF EXISTS user_subscriptions_user_group_unique_active;
DROP INDEX IF EXISTS user_subscriptions_user_plan_unique_active;
CREATE UNIQUE INDEX IF NOT EXISTS user_subscriptions_user_plan_version_unique_active
    ON user_subscriptions(user_id, plan_version_id)
    WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_user_subscriptions_plan_id ON user_subscriptions(plan_id);
CREATE INDEX IF NOT EXISTS idx_user_subscriptions_plan_version_id ON user_subscriptions(plan_version_id);

ALTER TABLE api_keys
    ADD COLUMN IF NOT EXISTS funding_source VARCHAR(20) NOT NULL DEFAULT 'wallet',
    ADD COLUMN IF NOT EXISTS subscription_id BIGINT;

UPDATE api_keys ak
SET funding_source = 'subscription',
    subscription_id = (
        SELECT us.id
        FROM user_subscriptions us
        WHERE us.user_id = ak.user_id
          AND us.group_id = ak.group_id
          AND us.deleted_at IS NULL
        ORDER BY
            (us.status = 'active' AND us.expires_at > NOW()) DESC,
            us.expires_at DESC,
            us.id DESC
        LIMIT 1
    )
WHERE ak.deleted_at IS NULL
  AND EXISTS (
      SELECT 1
      FROM groups g
      WHERE g.id = ak.group_id
        AND g.subscription_type = 'subscription'
  );

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM api_keys ak
        JOIN groups g ON g.id = ak.group_id
        WHERE ak.deleted_at IS NULL
          AND g.subscription_type = 'subscription'
          AND (ak.funding_source <> 'subscription' OR ak.subscription_id IS NULL)
    ) THEN
        RAISE EXCEPTION 'subscription migration left legacy API keys without an entitlement';
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'api_keys'::regclass
          AND conname = 'api_keys_subscription_id_fkey'
    ) THEN
        ALTER TABLE api_keys
            ADD CONSTRAINT api_keys_subscription_id_fkey
            FOREIGN KEY (subscription_id) REFERENCES user_subscriptions(id) ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'api_keys'::regclass
          AND conname = 'api_keys_id_user_id_key'
    ) THEN
        ALTER TABLE api_keys
            ADD CONSTRAINT api_keys_id_user_id_key UNIQUE (id, user_id);
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'api_keys'::regclass
          AND conname = 'api_keys_subscription_user_fkey'
    ) THEN
        ALTER TABLE api_keys
            ADD CONSTRAINT api_keys_subscription_user_fkey
            FOREIGN KEY (subscription_id, user_id)
            REFERENCES user_subscriptions(id, user_id)
            ON DELETE RESTRICT;
    END IF;
END $$;

ALTER TABLE api_keys DROP CONSTRAINT IF EXISTS api_keys_funding_source_check;
ALTER TABLE api_keys
    ADD CONSTRAINT api_keys_funding_source_check
    CHECK (
        (funding_source = 'wallet' AND subscription_id IS NULL)
        OR (funding_source = 'subscription' AND subscription_id IS NOT NULL)
    );

CREATE INDEX IF NOT EXISTS idx_api_keys_subscription_id ON api_keys(subscription_id);
CREATE INDEX IF NOT EXISTS idx_api_keys_user_funding_source ON api_keys(user_id, funding_source);

-- Convert default grant settings to plan references. Existing plan_id entries
-- are retained, making this block safe to rerun during migration verification.
DO $$
DECLARE
    setting_row RECORD;
    raw_items JSONB;
    item JSONB;
    mapped_items JSONB;
    mapped_plan_id BIGINT;
    legacy_group_id BIGINT;
    legacy_validity_days INT;
    seen_plan_ids BIGINT[];
BEGIN
    FOR setting_row IN
        SELECT id, value
        FROM settings
        WHERE key = 'default_subscriptions'
           OR key LIKE 'auth_source_default_%_subscriptions'
    LOOP
        BEGIN
            raw_items := setting_row.value::JSONB;
        EXCEPTION WHEN OTHERS THEN
            CONTINUE;
        END;
        IF jsonb_typeof(raw_items) <> 'array' THEN
            CONTINUE;
        END IF;

        mapped_items := '[]'::JSONB;
        seen_plan_ids := ARRAY[]::BIGINT[];
        FOR item IN SELECT value FROM jsonb_array_elements(raw_items)
        LOOP
            mapped_plan_id := NULL;
            IF jsonb_typeof(item) = 'object' AND jsonb_typeof(item -> 'plan_id') = 'number' THEN
                BEGIN
                    SELECT sp.id INTO mapped_plan_id
                    FROM subscription_plans sp
                    WHERE sp.id = (item ->> 'plan_id')::BIGINT;
                EXCEPTION WHEN OTHERS THEN
                    mapped_plan_id := NULL;
                END;
            ELSIF jsonb_typeof(item) = 'object'
                  AND jsonb_typeof(item -> 'group_id') = 'number'
                  AND jsonb_typeof(item -> 'validity_days') = 'number' THEN
                BEGIN
                    legacy_group_id := (item ->> 'group_id')::BIGINT;
                    legacy_validity_days := (item ->> 'validity_days')::INT;
                    IF legacy_validity_days <= 0 THEN
                        CONTINUE;
                    END IF;
                    legacy_validity_days := LEAST(36500, legacy_validity_days);
                    SELECT sp.id INTO mapped_plan_id
                    FROM subscription_plans sp
                    WHERE sp.group_id = legacy_group_id
                      AND sp.product_name =
                          '__sub2api_legacy_group_' || legacy_group_id || '_days_' || legacy_validity_days;
                EXCEPTION WHEN OTHERS THEN
                    mapped_plan_id := NULL;
                END;
            END IF;

            IF mapped_plan_id IS NOT NULL AND NOT (mapped_plan_id = ANY(seen_plan_ids)) THEN
                -- Keep the legacy keys alongside plan_id. The current runtime
                -- reads plan_id; an older binary can still inspect the original
                -- group/term values if the deployment is rolled back.
                mapped_items := mapped_items || jsonb_build_array(
                    item || jsonb_build_object('plan_id', mapped_plan_id)
                );
                seen_plan_ids := array_append(seen_plan_ids, mapped_plan_id);
            ELSIF mapped_plan_id IS NULL THEN
                -- Preserve malformed/unmapped entries for audit and rollback;
                -- the current parser ignores them because plan_id is absent.
                mapped_items := mapped_items || jsonb_build_array(item);
            END IF;
        END LOOP;

        UPDATE settings
        SET value = mapped_items::TEXT,
            updated_at = NOW()
        WHERE id = setting_row.id;
    END LOOP;
END $$;

-- Preserve announcement semantics by replacing each legacy group with every
-- plan that represents that group, including its migrated entitlement plans.
DO $$
DECLARE
    announcement_row RECORD;
    any_group JSONB;
    condition JSONB;
    raw_group_id JSONB;
    rewritten_targeting JSONB;
    rewritten_any_of JSONB;
    rewritten_all_of JSONB;
    mapped_plan_ids JSONB;
    seen_plan_ids BIGINT[];
    legacy_group_id BIGINT;
    plan_row RECORD;
BEGIN
    FOR announcement_row IN
        SELECT id, targeting
        FROM announcements
        WHERE jsonb_typeof(targeting -> 'any_of') = 'array'
    LOOP
        rewritten_any_of := '[]'::JSONB;
        FOR any_group IN SELECT value FROM jsonb_array_elements(announcement_row.targeting -> 'any_of')
        LOOP
            IF jsonb_typeof(any_group -> 'all_of') <> 'array' THEN
                rewritten_any_of := rewritten_any_of || jsonb_build_array(any_group);
                CONTINUE;
            END IF;

            rewritten_all_of := '[]'::JSONB;
            FOR condition IN SELECT value FROM jsonb_array_elements(any_group -> 'all_of')
            LOOP
                IF condition ->> 'type' = 'subscription'
                   AND jsonb_typeof(condition -> 'group_ids') = 'array' THEN
                    mapped_plan_ids := '[]'::JSONB;
                    seen_plan_ids := ARRAY[]::BIGINT[];
                    FOR raw_group_id IN SELECT value FROM jsonb_array_elements(condition -> 'group_ids')
                    LOOP
                        IF jsonb_typeof(raw_group_id) <> 'number' THEN
                            CONTINUE;
                        END IF;
                        BEGIN
                            legacy_group_id := raw_group_id::TEXT::BIGINT;
                        EXCEPTION WHEN OTHERS THEN
                            CONTINUE;
                        END;
                        FOR plan_row IN
                            SELECT sp.id
                            FROM subscription_plans sp
                            WHERE sp.group_id = legacy_group_id
                            ORDER BY sp.id
                        LOOP
                            IF NOT (plan_row.id = ANY(seen_plan_ids)) THEN
                                mapped_plan_ids := mapped_plan_ids || to_jsonb(plan_row.id);
                                seen_plan_ids := array_append(seen_plan_ids, plan_row.id);
                            END IF;
                        END LOOP;
                    END LOOP;
                    -- Keep group_ids for an older binary while the current
                    -- matcher consumes plan_ids.
                    condition := condition || jsonb_build_object('plan_ids', mapped_plan_ids);
                END IF;
                rewritten_all_of := rewritten_all_of || jsonb_build_array(condition);
            END LOOP;
            rewritten_any_of := rewritten_any_of ||
                jsonb_build_array(jsonb_set(any_group, '{all_of}', rewritten_all_of));
        END LOOP;
        rewritten_targeting := jsonb_set(announcement_row.targeting, '{any_of}', rewritten_any_of);
        UPDATE announcements
        SET targeting = rewritten_targeting,
            updated_at = NOW()
        WHERE id = announcement_row.id;
    END LOOP;
END $$;

ALTER TABLE payment_orders
    ADD COLUMN IF NOT EXISTS plan_version_id BIGINT,
    ADD COLUMN IF NOT EXISTS fulfilled_subscription_id BIGINT,
    ADD COLUMN IF NOT EXISTS entitlement_snapshot JSONB NOT NULL DEFAULT '{}'::JSONB;

-- Recover orders written by the pre-versioned payment flow. A non-existent
-- legacy plan_id is treated like a missing one only when the old group/term
-- pair is present; otherwise the migration fails closed below rather than
-- guessing which commercial terms were purchased.
UPDATE payment_orders po
SET plan_id = legacy_plan.id
FROM subscription_plans legacy_plan
WHERE po.order_type = 'subscription'
  AND (po.plan_id IS NULL OR NOT EXISTS (
      SELECT 1 FROM subscription_plans existing WHERE existing.id = po.plan_id
  ))
  AND po.subscription_group_id IS NOT NULL
  AND legacy_plan.group_id = po.subscription_group_id
  AND legacy_plan.product_name =
      '__sub2api_legacy_group_' || po.subscription_group_id || '_days_' ||
      CASE
          WHEN COALESCE(po.subscription_days, 0) <= 0 THEN 30
          ELSE LEAST(36500, GREATEST(po.subscription_days, 1))
      END;

UPDATE payment_orders po
SET plan_version_id = sp.published_version_id
FROM subscription_plans sp
WHERE po.plan_id = sp.id
  AND po.order_type = 'subscription'
  AND po.plan_version_id IS NULL;

-- Rebuild an incomplete snapshot from the version already frozen on the
-- order. This is deliberately separate from the version backfill: if a prior
-- run wrote plan_version_id and was interrupted before writing the snapshot,
-- the order must keep that historical version rather than following the
-- plan's current published version.
UPDATE payment_orders po
SET entitlement_snapshot = jsonb_build_object(
        'plan_id', sp.id,
        'plan_version_id', spv.id,
        'version', spv.version,
        'validity_days', spv.validity_days,
        'validity_unit', spv.validity_unit,
        'daily_limit_usd', spv.daily_limit_usd,
        'weekly_limit_usd', spv.weekly_limit_usd,
        'monthly_limit_usd', spv.monthly_limit_usd
    )
FROM subscription_plans sp
JOIN subscription_plan_versions spv
  ON spv.plan_id = sp.id
WHERE po.plan_id = sp.id
  AND po.plan_version_id = spv.id
  AND po.order_type = 'subscription'
  AND (
      po.entitlement_snapshot IS NULL
      OR jsonb_typeof(po.entitlement_snapshot) <> 'object'
      OR NOT (po.entitlement_snapshot ?& ARRAY[
          'plan_id', 'plan_version_id', 'version', 'validity_days',
          'validity_unit', 'daily_limit_usd', 'weekly_limit_usd',
          'monthly_limit_usd'
      ]::TEXT[])
      OR COALESCE(po.entitlement_snapshot ->> 'plan_id', '') <> sp.id::TEXT
      OR COALESCE(po.entitlement_snapshot ->> 'plan_version_id', '') <> spv.id::TEXT
  );

-- Collapse pre-versioned fulfillment evidence into the new durable assignment
-- link. Historical workers could commit the entitlement before updating the
-- order; exact order notes and success/assignment audits are authoritative
-- evidence that replay must not extend the entitlement again.
WITH recovered_assignments AS (
    SELECT po.id AS order_id,
           (
               SELECT us.id
               FROM user_subscriptions us
               WHERE us.user_id = po.user_id
                 AND us.plan_version_id = po.plan_version_id
                 AND (
                     EXISTS (
                         SELECT 1
                         FROM regexp_split_to_table(replace(COALESCE(us.notes, ''), E'\r\n', E'\n'), E'\n') AS note_line
                         WHERE btrim(note_line) = 'payment order ' || po.id::TEXT
                     )
                     OR EXISTS (
                         SELECT 1
                         FROM payment_audit_logs pal
                         WHERE pal.order_id = po.id::TEXT
                           AND pal.action IN ('SUBSCRIPTION_ASSIGNED', 'SUBSCRIPTION_SUCCESS')
                     )
                 )
               ORDER BY (us.deleted_at IS NULL) DESC, us.updated_at DESC, us.id DESC
               LIMIT 1
           ) AS subscription_id
    FROM payment_orders po
    WHERE po.order_type = 'subscription'
      AND po.fulfilled_subscription_id IS NULL
)
UPDATE payment_orders po
SET fulfilled_subscription_id = recovered_assignments.subscription_id
FROM recovered_assignments
WHERE po.id = recovered_assignments.order_id
  AND recovered_assignments.subscription_id IS NOT NULL;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM payment_orders
        WHERE order_type = 'subscription'
          AND plan_id IS NULL
    ) THEN
        RAISE EXCEPTION 'subscription payment order is missing plan_id';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM payment_orders
        WHERE order_type = 'subscription'
          AND plan_version_id IS NULL
    ) THEN
        RAISE EXCEPTION 'subscription payment order is missing plan_version_id';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM payment_orders po
        LEFT JOIN subscription_plan_versions spv ON spv.id = po.plan_version_id
        WHERE po.order_type = 'subscription'
          AND (spv.id IS NULL OR spv.plan_id IS DISTINCT FROM po.plan_id)
    ) THEN
        RAISE EXCEPTION 'subscription payment order has an orphaned or mismatched plan version';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM payment_orders po
        JOIN subscription_plan_versions spv
          ON spv.id = po.plan_version_id
         AND spv.plan_id = po.plan_id
        WHERE po.order_type = 'subscription'
          AND (
              po.entitlement_snapshot IS NULL
              OR jsonb_typeof(po.entitlement_snapshot) <> 'object'
              OR NOT (po.entitlement_snapshot ?& ARRAY[
                  'plan_id', 'plan_version_id', 'version', 'validity_days',
                  'validity_unit', 'daily_limit_usd', 'weekly_limit_usd',
                  'monthly_limit_usd'
              ]::TEXT[])
              OR COALESCE(po.entitlement_snapshot ->> 'plan_id', '') <> po.plan_id::TEXT
              OR COALESCE(po.entitlement_snapshot ->> 'plan_version_id', '') <> po.plan_version_id::TEXT
          )
    ) THEN
        RAISE EXCEPTION 'subscription payment order has an incomplete entitlement snapshot';
    END IF;
END $$;

ALTER TABLE redeem_codes
    ADD COLUMN IF NOT EXISTS plan_version_id BIGINT;

UPDATE redeem_codes rc
SET plan_version_id = legacy_plan.published_version_id
FROM subscription_plans legacy_plan
WHERE rc.type = 'subscription'
  AND rc.validity_days >= 0
  AND rc.group_id IS NOT NULL
  AND legacy_plan.group_id = rc.group_id
  AND legacy_plan.product_name =
      '__sub2api_legacy_group_' || rc.group_id || '_days_' ||
      CASE
          WHEN rc.validity_days = 0 THEN 30
          ELSE LEAST(36500, GREATEST(rc.validity_days, 1))
      END
  AND rc.plan_version_id IS NULL;

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM redeem_codes
        WHERE type = 'subscription'
          AND status = 'unused'
          AND validity_days < 0
    ) THEN
        RAISE EXCEPTION
            'unused subscription redeem code uses negative validity_days and cannot be represented by a plan version';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM redeem_codes
        WHERE type = 'subscription'
          AND status = 'unused'
          AND validity_days >= 0
          AND plan_version_id IS NULL
    ) THEN
        RAISE EXCEPTION 'subscription redeem code is missing plan_version_id';
    END IF;

    IF EXISTS (
        SELECT 1
        FROM redeem_codes rc
        LEFT JOIN subscription_plan_versions spv ON spv.id = rc.plan_version_id
        WHERE rc.type = 'subscription'
          AND rc.status = 'unused'
          AND rc.validity_days >= 0
          AND spv.id IS NULL
    ) THEN
        RAISE EXCEPTION 'subscription redeem code has an orphaned plan version';
    END IF;
END $$;

-- New writers no longer populate these legacy fields. Keep the columns and old
-- values, but remove only the constraints that would reject the new model.
-- They intentionally become nullable and have no defaults: a newly-created
-- catalog shell must not silently create a second, stale set of commercial
-- terms in the legacy columns. Existing values remain untouched for audit and
-- controlled rollback of the pre-versioned application.
ALTER TABLE subscription_plans
    ALTER COLUMN group_id DROP NOT NULL,
    ALTER COLUMN price DROP NOT NULL,
    ALTER COLUMN validity_days DROP NOT NULL,
    ALTER COLUMN validity_days DROP DEFAULT,
    ALTER COLUMN validity_unit DROP NOT NULL,
    ALTER COLUMN validity_unit DROP DEFAULT,
    ALTER COLUMN currency DROP NOT NULL,
    ALTER COLUMN currency DROP DEFAULT;

-- The reservation table is a new runtime boundary. Do not silently upgrade a
-- development/legacy table with an unknown shape: any existing relation must
-- already expose the complete schema and constraints below.
DO $$
DECLARE
    existing_table REGCLASS;
    missing_columns TEXT;
    missing_constraints TEXT;
BEGIN
    existing_table := to_regclass(current_schema() || '.billing_reservations');
    IF existing_table IS NULL THEN
        RETURN;
    END IF;

    SELECT string_agg(required.column_name, ', ' ORDER BY required.column_name)
    INTO missing_columns
    FROM (VALUES
        ('id'), ('request_id'), ('api_key_id'), ('user_id'), ('funding_source'),
        ('subscription_id'), ('authorized_amount'), ('captured_amount'), ('status'),
        ('authorization_fingerprint'), ('request_fingerprint'),
        ('daily_window_start'), ('weekly_window_start'), ('monthly_window_start'),
        ('async_task_id'), ('async_metadata'), ('expires_at'), ('created_at'), ('updated_at')
    ) AS required(column_name)
    WHERE NOT EXISTS (
        SELECT 1
        FROM information_schema.columns c
        WHERE c.table_schema = current_schema()
          AND c.table_name = 'billing_reservations'
          AND c.column_name = required.column_name
    );

    IF missing_columns IS NOT NULL THEN
        RAISE EXCEPTION
            'billing_reservations exists with an incomplete schema; missing required columns: %',
            missing_columns;
    END IF;

    SELECT string_agg(required.constraint_name, ', ' ORDER BY required.constraint_name)
    INTO missing_constraints
    FROM (VALUES
        ('billing_reservations_pkey'),
        ('billing_reservations_request_key'),
        ('billing_reservations_api_key_id_fkey'),
        ('billing_reservations_api_key_user_fkey'),
        ('billing_reservations_user_id_fkey'),
        ('billing_reservations_subscription_id_fkey'),
        ('billing_reservations_subscription_user_fkey'),
        ('billing_reservations_funding_source_check'),
        ('billing_reservations_amount_check'),
        ('billing_reservations_status_check')
    ) AS required(constraint_name)
    WHERE NOT EXISTS (
        SELECT 1
        FROM pg_constraint c
        WHERE c.conrelid = existing_table
          AND c.conname = required.constraint_name
    );

    IF missing_constraints IS NOT NULL THEN
        RAISE EXCEPTION
            'billing_reservations exists without required constraints: %',
            missing_constraints;
    END IF;
END $$;

CREATE TABLE IF NOT EXISTS billing_reservations (
    id                        BIGSERIAL PRIMARY KEY,
    request_id                VARCHAR(128) NOT NULL,
    api_key_id                BIGINT NOT NULL REFERENCES api_keys(id) ON DELETE RESTRICT,
    user_id                   BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    funding_source            VARCHAR(20) NOT NULL,
    subscription_id           BIGINT REFERENCES user_subscriptions(id) ON DELETE RESTRICT,
    authorized_amount         DECIMAL(20, 10) NOT NULL DEFAULT 0,
    captured_amount           DECIMAL(20, 10) NOT NULL DEFAULT 0,
    status                    VARCHAR(20) NOT NULL DEFAULT 'authorized',
    authorization_fingerprint VARCHAR(128) NOT NULL,
    request_fingerprint       VARCHAR(128) NOT NULL DEFAULT '',
    daily_window_start        TIMESTAMPTZ,
    weekly_window_start       TIMESTAMPTZ,
    monthly_window_start      TIMESTAMPTZ,
    async_task_id             VARCHAR(255),
    async_metadata            JSONB,
    expires_at                TIMESTAMPTZ NOT NULL,
    created_at                TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at                TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT billing_reservations_request_key UNIQUE (request_id, api_key_id),
    CONSTRAINT billing_reservations_api_key_user_fkey
        FOREIGN KEY (api_key_id, user_id)
        REFERENCES api_keys(id, user_id)
        ON DELETE RESTRICT,
    CONSTRAINT billing_reservations_subscription_user_fkey
        FOREIGN KEY (subscription_id, user_id)
        REFERENCES user_subscriptions(id, user_id)
        ON DELETE RESTRICT,
    CONSTRAINT billing_reservations_funding_source_check CHECK (
        (funding_source = 'wallet' AND subscription_id IS NULL)
        OR (funding_source = 'subscription' AND subscription_id IS NOT NULL)
    ),
    CONSTRAINT billing_reservations_amount_check CHECK (
        authorized_amount >= 0 AND captured_amount >= 0
    ),
    CONSTRAINT billing_reservations_status_check CHECK (
        status IN ('authorized', 'finalizing', 'captured', 'released')
    )
);

CREATE INDEX IF NOT EXISTS idx_billing_reservations_status_expires
    ON billing_reservations(status, expires_at);
CREATE INDEX IF NOT EXISTS idx_billing_reservations_subscription_status
    ON billing_reservations(subscription_id, status);
CREATE INDEX IF NOT EXISTS idx_billing_reservations_user_status
    ON billing_reservations(user_id, status);
CREATE UNIQUE INDEX IF NOT EXISTS idx_billing_reservations_async_task_api_key
    ON billing_reservations(async_task_id, api_key_id)
    WHERE async_task_id IS NOT NULL;

ALTER TABLE user_subscriptions DROP CONSTRAINT IF EXISTS user_subscriptions_reserved_nonnegative_check;
ALTER TABLE user_subscriptions
    ADD CONSTRAINT user_subscriptions_reserved_nonnegative_check
    CHECK (
        daily_reserved_usd >= 0
        AND weekly_reserved_usd >= 0
        AND monthly_reserved_usd >= 0
    );

DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'subscription_plans'::regclass
          AND conname = 'subscription_plans_published_version_id_fkey'
    ) THEN
        ALTER TABLE subscription_plans
            ADD CONSTRAINT subscription_plans_published_version_id_fkey
            FOREIGN KEY (published_version_id) REFERENCES subscription_plan_versions(id) ON DELETE SET NULL;
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'payment_orders'::regclass
          AND conname = 'payment_orders_plan_version_id_fkey'
    ) THEN
        ALTER TABLE payment_orders
            ADD CONSTRAINT payment_orders_plan_version_id_fkey
            FOREIGN KEY (plan_version_id) REFERENCES subscription_plan_versions(id) ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'payment_orders'::regclass
          AND conname = 'payment_orders_plan_version_plan_id_fkey'
    ) THEN
        ALTER TABLE payment_orders
            ADD CONSTRAINT payment_orders_plan_version_plan_id_fkey
            FOREIGN KEY (plan_version_id, plan_id)
            REFERENCES subscription_plan_versions(id, plan_id)
            ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'payment_orders'::regclass
          AND conname = 'payment_orders_fulfilled_subscription_id_fkey'
    ) THEN
        ALTER TABLE payment_orders
            ADD CONSTRAINT payment_orders_fulfilled_subscription_id_fkey
            FOREIGN KEY (fulfilled_subscription_id) REFERENCES user_subscriptions(id) ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'payment_orders'::regclass
          AND conname = 'payment_orders_fulfilled_subscription_user_fkey'
    ) THEN
        ALTER TABLE payment_orders
            ADD CONSTRAINT payment_orders_fulfilled_subscription_user_fkey
            FOREIGN KEY (fulfilled_subscription_id, user_id)
            REFERENCES user_subscriptions(id, user_id)
            ON DELETE RESTRICT;
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'redeem_codes'::regclass
          AND conname = 'redeem_codes_plan_version_id_fkey'
    ) THEN
        ALTER TABLE redeem_codes
            ADD CONSTRAINT redeem_codes_plan_version_id_fkey
            FOREIGN KEY (plan_version_id) REFERENCES subscription_plan_versions(id) ON DELETE RESTRICT;
    END IF;

    IF NOT EXISTS (
        SELECT 1
        FROM pg_constraint
        WHERE conrelid = 'subscription_plans'::regclass
          AND conname = 'subscription_plans_published_version_plan_id_fkey'
    ) THEN
        ALTER TABLE subscription_plans
            ADD CONSTRAINT subscription_plans_published_version_plan_id_fkey
            FOREIGN KEY (published_version_id, id)
            REFERENCES subscription_plan_versions(id, plan_id)
            ON DELETE SET NULL (published_version_id);
    END IF;
END $$;
