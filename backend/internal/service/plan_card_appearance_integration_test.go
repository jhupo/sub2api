//go:build integration

package service

import (
	"context"
	"database/sql"
	"os"
	"testing"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/migrations"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestPlanCardAppearanceUpdatePreservesCommercialTerms(t *testing.T) {
	dsn := os.Getenv("SUB2API_FEATURE_TEST_DSN")
	if dsn == "" {
		t.Skip("SUB2API_FEATURE_TEST_DSN is required")
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	migration, err := migrations.FS.ReadFile("249_plan_card_appearance.sql")
	require.NoError(t, err)
	_, err = db.Exec(string(migration))
	require.NoError(t, err)
	config := &PaymentConfigService{entClient: client}
	initial := domain.PlanCardAppearance{Theme: "mint", Motion: "shine"}
	plan, err := config.CreatePlan(ctx, CreatePlanRequest{
		Name: "Appearance regression", Price: 20, ValidityDays: 30, ValidityUnit: "days", ForSale: true, CardAppearance: &initial,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := db.Exec("UPDATE subscription_plans SET published_version_id = NULL WHERE id = $1", plan.ID)
		require.NoError(t, err)
		_, err = db.Exec("DELETE FROM subscription_plan_versions WHERE plan_id = $1", plan.ID)
		require.NoError(t, err)
		_, err = db.Exec("DELETE FROM subscription_plans WHERE id = $1", plan.ID)
		require.NoError(t, err)
	})
	replacement := domain.PlanCardAppearance{Theme: "ruby", Motion: "flow"}
	updated, err := config.UpdatePlan(ctx, plan.ID, UpdatePlanRequest{CardAppearance: &replacement})
	require.NoError(t, err)
	require.Equal(t, plan.PublishedVersionID, updated.PublishedVersionID)
	require.Equal(t, plan.Price, updated.Price)
	require.Equal(t, replacement, updated.CardAppearance)
	persisted, err := config.GetPlan(ctx, plan.ID)
	require.NoError(t, err)
	require.Equal(t, replacement, persisted.CardAppearance)
	_, err = config.UpdatePlan(ctx, plan.ID, UpdatePlanRequest{CardAppearance: &domain.PlanCardAppearance{Theme: "invalid"}})
	require.Error(t, err)
}
