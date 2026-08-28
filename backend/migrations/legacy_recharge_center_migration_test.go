package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRemoveLegacyRechargeCenterMenuMigrationIsScopedAndDefensive(t *testing.T) {
	content, err := FS.ReadFile("235_remove_legacy_recharge_center_menu.sql")
	require.NoError(t, err)

	sql := strings.Join(strings.Fields(string(content)), " ")
	upperSQL := strings.ToUpper(sql)

	require.Contains(t, sql, "WHERE key = 'custom_menu_items'")
	require.Contains(t, sql, "menu_item ->> 'id' IS DISTINCT FROM '322273f5aaa4d036'")
	require.Contains(t, upperSQL, "WITH ORDINALITY")
	require.Contains(t, sql, "jsonb_agg(menu_item ORDER BY ordinal)")
	require.Contains(t, sql, "jsonb_typeof(v_items) IS DISTINCT FROM 'array'")
	require.Contains(t, sql, "WHEN invalid_text_representation")
	require.Contains(t, sql, "IF v_filtered IS DISTINCT FROM v_items")
	require.NotContains(t, upperSQL, "DELETE FROM SETTINGS")
	require.NotContains(t, sql, "menu_item ->> 'id' = 'migrated_purchase_subscription'")

	for _, unrelatedSetting := range []string{
		"payment_enabled",
		"payment_balance_disabled",
		"purchase_subscription_enabled",
		"purchase_subscription_url",
	} {
		require.NotContains(t, sql, unrelatedSetting)
	}
}
