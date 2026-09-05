-- Separate imported entitlement snapshots from products available for new
-- issuance. Existing subscription/version IDs, terms and expiry dates stay intact.
ALTER TABLE subscription_plans
    ADD COLUMN IF NOT EXISTS is_historical BOOLEAN NOT NULL DEFAULT FALSE;

WITH imported AS (
    SELECT id,
           ' (legacy ' || substring(product_name FROM '_days_([0-9]+)$') || 'd)' AS suffix
    FROM subscription_plans
    WHERE product_name ~ '^__sub2api_legacy_group_[0-9]+_days_[0-9]+$'
)
UPDATE subscription_plans sp
SET is_historical = NOT sp.for_sale,
    name = CASE
        WHEN right(sp.name, length(imported.suffix)) = imported.suffix
        THEN left(sp.name, length(sp.name) - length(imported.suffix))
        ELSE sp.name
    END,
    description = CASE
        WHEN sp.description = 'Migrated legacy subscription entitlement' THEN ''
        ELSE sp.description
    END
FROM imported
WHERE sp.id = imported.id;
