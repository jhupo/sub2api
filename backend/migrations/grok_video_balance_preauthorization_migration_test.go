package migrations

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGrokVideoBalancePreauthorizationMigrationAddsDurableBinding(t *testing.T) {
	content, err := FS.ReadFile("236_grok_video_balance_preauthorization.sql")
	require.NoError(t, err)
	sql := strings.Join(strings.Fields(string(content)), " ")

	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS async_task_id TEXT")
	require.Contains(t, sql, "ADD COLUMN IF NOT EXISTS async_metadata JSONB")
	require.NotContains(t, sql, "CREATE UNIQUE INDEX")

	indexContent, err := FS.ReadFile("237_grok_video_balance_preauthorization_index_notx.sql")
	require.NoError(t, err)
	indexSQL := strings.Join(strings.Fields(string(indexContent)), " ")
	require.Contains(t, indexSQL, "CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS idx_billing_balance_settlements_async_task_api_key")
	require.Contains(t, indexSQL, "ON billing_balance_settlements (async_task_id, api_key_id)")
	require.Contains(t, indexSQL, "WHERE async_task_id IS NOT NULL")
}
