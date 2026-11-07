//go:build unit

package handler

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/enttest"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type checkoutCardSettingsRepo struct{ service.SettingRepository }

func (*checkoutCardSettingsRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}

func TestGetCheckoutInfoIncludesQuotaCardTerms(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := sql.Open("sqlite", "file:checkout-quota-cards?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)
	_, err = db.Exec("PRAGMA foreign_keys = ON")
	require.NoError(t, err)
	client := enttest.NewClient(t, enttest.WithOptions(dbent.Driver(entsql.OpenDB(dialect.SQLite, db))))
	t.Cleanup(func() { _ = client.Close() })
	config := service.NewPaymentConfigService(client, &checkoutCardSettingsRepo{}, nil)
	ctx := context.Background()
	deadline := time.Now().UTC().Add(24 * time.Hour).Truncate(time.Second)
	total := 300.0
	prior := time.Now().Add(-time.Hour)
	for _, terms := range []service.CreatePlanRequest{
		{Name: "Deadline card", Price: 45, ForSale: true, CardMode: "deadline", DeadlineAt: &deadline, TotalLimitUSD: &total, SaleSchedule: &domain.PlanSaleSchedule{PriceSteps: []domain.PlanPriceStep{{At: prior, Price: 35}}}},
		{Name: "Hourly card", Price: 20, ForSale: true, CardMode: "hours", ValidityHours: 12, TotalLimitUSD: &total},
		{Name: "Recurring plan", Price: 20, ForSale: true, ValidityDays: 30, ValidityUnit: "days"},
	} {
		_, err := config.CreatePlan(ctx, terms)
		require.NoError(t, err)
	}
	_, err = config.CreatePlan(ctx, service.CreatePlanRequest{Name: "Off-sale card", Price: 20, ForSale: true, CardMode: "hours", ValidityHours: 12, TotalLimitUSD: &total, SaleSchedule: &domain.PlanSaleSchedule{EndsAt: &prior}})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	requestCtx, _ := gin.CreateTestContext(recorder)
	requestCtx.Request = httptest.NewRequest(http.MethodGet, "/api/v1/payment/checkout-info", nil)
	NewPaymentHandler(nil, config).GetCheckoutInfo(requestCtx)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	var response struct {
		Code int `json:"code"`
		Data struct {
			Plans []map[string]any `json:"plans"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	require.Zero(t, response.Code)
	require.Len(t, response.Data.Plans, 3)
	for _, plan := range response.Data.Plans {
		for _, field := range []string{"card_mode", "validity_hours", "deadline_at", "total_limit_usd"} {
			require.Contains(t, plan, field, "checkout must retain published quota card terms")
		}
		switch plan["name"] {
		case "Deadline card":
			require.Equal(t, 35.0, plan["price"])
			require.Equal(t, 45.0, plan["original_price"])
			require.Equal(t, deadline.Format(time.RFC3339), plan["sale_ends_at"])
			require.Equal(t, "deadline", plan["card_mode"])
			require.Equal(t, deadline.Format(time.RFC3339), plan["deadline_at"])
			require.Equal(t, total, plan["total_limit_usd"])
		case "Hourly card":
			require.Equal(t, "hours", plan["card_mode"])
			require.Equal(t, float64(12), plan["validity_hours"])
			require.Equal(t, total, plan["total_limit_usd"])
			require.Nil(t, plan["deadline_at"])
		case "Recurring plan":
			require.Equal(t, "", plan["card_mode"])
			require.Equal(t, float64(30), plan["validity_days"])
			require.Nil(t, plan["total_limit_usd"])
		}
	}
}
