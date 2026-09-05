//go:build integration

package repository

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/stretchr/testify/require"
)

func TestSubscriptionPlanCatalogMigration(t *testing.T) {
	ctx := context.Background()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })
	fixture, err := os.ReadFile(filepath.Join("..", "..", "migrations", "testdata", "subscription_plan_catalog_fixture.sql"))
	require.NoError(t, err)
	_, err = tx.ExecContext(ctx, string(fixture))
	require.NoError(t, err)

	protected := []string{"subscription_plan_versions", "user_subscriptions", "redeem_codes", "payment_orders", "users", "settings", "announcements"}
	snapshot := func(table string) string {
		var data string
		err := tx.QueryRowContext(ctx, fmt.Sprintf("SELECT jsonb_agg(to_jsonb(t) ORDER BY id)::text FROM %s t", table)).Scan(&data)
		require.NoError(t, err)
		return data
	}
	before := make(map[string]string)
	for _, table := range protected {
		before[table] = snapshot(table)
	}
	migration, err := dbmigrations.FS.ReadFile("241_subscription_plan_catalog.sql")
	require.NoError(t, err)
	for run := 0; run < 2; run++ {
		_, err = tx.ExecContext(ctx, string(migration))
		require.NoError(t, err)
		for _, expected := range []struct {
			id         int64
			name       string
			historical bool
		}{
			{1, "Regular", false}, {2, "Private 32-day plan", false},
			{3, "Monthly", true}, {4, "Monthly", true},
			{5, "Operator renamed", true}, {6, "Promoted", false},
			{7, "Custom (legacy 32d)", false}, {8, "Mismatch (legacy 31d)", true},
		} {
			var name string
			var historical bool
			var versionID int64
			err = tx.QueryRowContext(ctx, "SELECT name, is_historical, published_version_id FROM subscription_plans WHERE id = $1", expected.id).Scan(&name, &historical, &versionID)
			require.NoError(t, err)
			require.Equal(t, expected.name, name)
			require.Equal(t, expected.historical, historical)
			require.Equal(t, 100+expected.id, versionID)
		}
		for _, table := range protected {
			require.Equal(t, before[table], snapshot(table), table)
		}
	}
}
