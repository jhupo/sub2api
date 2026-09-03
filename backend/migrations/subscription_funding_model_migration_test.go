package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSubscriptionFundingModelMigrationAllowsVersionedPlanWrites(t *testing.T) {
	content, err := FS.ReadFile("240_subscription_funding_model.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")

	// The runtime catalog writes commercial terms exclusively to the immutable
	// version row. Every legacy term column omitted by the Ent create mutation
	// must therefore accept NULL after the backfill, including currency.
	require.Contains(t, sql, "ALTER COLUMN group_id DROP NOT NULL")
	require.Contains(t, sql, "ALTER COLUMN price DROP NOT NULL")
	require.Contains(t, sql, "ALTER COLUMN validity_days DROP NOT NULL")
	require.Contains(t, sql, "ALTER COLUMN validity_unit DROP NOT NULL")
	require.Contains(t, sql, "ALTER COLUMN currency DROP NOT NULL")
	// The legacy group reference is audit-only after the migration. Keeping its
	// old ON DELETE CASCADE would allow deleting a routing group to delete a
	// purchased entitlement as a side effect.
	require.Contains(t, sql, "DROP CONSTRAINT IF EXISTS user_subscriptions_group_id_fkey")
	require.Contains(t, sql, "CREATE TABLE IF NOT EXISTS subscription_plan_versions")
	require.Contains(t, sql, "subscription_plan_versions_original_price_check")
	require.Contains(t, sql, "subscription_plan_versions_plan_id_fkey")
	// Reservation state is a strict new boundary. An existing partial table must
	// fail migration instead of being populated with synthetic legacy identity.
	require.Contains(t, sql, "billing_reservations exists with an incomplete schema")
	require.Contains(t, sql, "billing_reservations exists without required constraints")
	require.NotContains(t, sql, "legacy:' || id")
	require.NotContains(t, sql, "ADD COLUMN IF NOT EXISTS authorization_fingerprint")
	require.Contains(t, sql, "user_subscriptions_plan_version_plan_id_fkey")
	require.Contains(t, sql, "payment_orders_plan_version_plan_id_fkey")
	require.Contains(t, sql, "payment_orders_fulfilled_subscription_user_fkey")
	require.Contains(t, sql, "orphaned or mismatched plan version")
	require.Contains(t, sql, "subscription payment order is missing plan_version_id")
	require.Contains(t, sql, "subscription payment order has an incomplete entitlement snapshot")
	require.Contains(t, sql, "spv.id = po.plan_version_id")
	require.Contains(t, sql, "po.subscription_group_id")
	require.Contains(t, sql, "pre-versioned payment flow")
	require.Contains(t, sql, "subscription redeem code is missing plan_version_id")
	require.Contains(t, sql, "subscription redeem code has an orphaned plan version")
	require.Contains(t, sql, "WHEN rc.validity_days = 0 THEN 30")
	require.Contains(t, sql, "cannot be represented by a plan version")
	require.Contains(t, sql, "AND status = 'unused'")
	require.Contains(t, sql, "item || jsonb_build_object('plan_id', mapped_plan_id)")
	require.Contains(t, sql, "condition := condition || jsonb_build_object('plan_ids', mapped_plan_ids)")
	require.Contains(t, sql, "billing_reservations_api_key_user_fkey")
	require.Contains(t, sql, "billing_reservations_subscription_user_fkey")
}
