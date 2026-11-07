//go:build integration

package service

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	_ "github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func TestSaleOrderLocksPriceAndPreservesEntitlement(t *testing.T) {
	dsn := os.Getenv("SUB2API_FEATURE_TEST_DSN")
	if dsn == "" {
		t.Skip("SUB2API_FEATURE_TEST_DSN is required")
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	ctx := context.Background()
	user, err := client.User.Create().SetEmail(fmt.Sprintf("sale-order-%d@test.local", time.Now().UnixNano())).SetPasswordHash("local-test").Save(ctx)
	require.NoError(t, err)
	config := &PaymentConfigService{entClient: client}
	service := &PaymentService{entClient: client, configService: config}
	prior, saleEnd, total := time.Now().Add(-time.Hour), time.Now().Add(time.Hour), 300.0
	plan, err := config.CreatePlan(ctx, CreatePlanRequest{Name: "Sale order regression", Price: 45, CardMode: "hours", ValidityHours: 12, TotalLimitUSD: &total, ForSale: true,
		SaleSchedule: &domain.PlanSaleSchedule{EndsAt: &saleEnd, PriceSteps: []domain.PlanPriceStep{{At: prior, Price: 35}}}})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := db.Exec("DELETE FROM payment_orders WHERE user_id = $1", user.ID)
		require.NoError(t, err)
		_, err = db.Exec("DELETE FROM users WHERE id = $1", user.ID)
		require.NoError(t, err)
		_, err = db.Exec("UPDATE subscription_plans SET published_version_id = NULL WHERE id = $1", plan.ID)
		require.NoError(t, err)
		_, err = db.Exec("DELETE FROM subscription_plan_versions WHERE plan_id = $1", plan.ID)
		require.NoError(t, err)
		_, err = db.Exec("DELETE FROM subscription_plans WHERE id = $1", plan.ID)
		require.NoError(t, err)
	})
	request := CreateOrderRequest{UserID: user.ID, PlanID: plan.ID, OrderType: payment.OrderTypeSubscription, Amount: 35, PaymentType: payment.TypeAlipay}
	quoted, err := service.validateSubOrder(ctx, request)
	require.NoError(t, err)
	createOrder := func() (*dbent.PaymentOrder, error) {
		return service.createOrderInTx(ctx, request, &User{ID: user.ID, Email: user.Email}, quoted, &PaymentConfig{MaxPendingOrders: 5, OrderTimeoutMin: 30}, 35, 35, 0, 35, nil)
	}
	order, err := createOrder()
	require.NoError(t, err)
	require.Equal(t, 35.0, order.Amount)
	require.Equal(t, 35.0, order.PayAmount)
	require.Equal(t, 12.0, order.EntitlementSnapshot["validity_hours"])
	_, err = config.UpdatePlan(ctx, plan.ID, UpdatePlanRequest{SaleSchedule: &domain.PlanSaleSchedule{PriceSteps: []domain.PlanPriceStep{{At: prior, Price: 25}}}})
	require.NoError(t, err)
	_, err = createOrder()
	require.ErrorContains(t, err, "PLAN_PRICE_CHANGED")
	_, err = config.UpdatePlan(ctx, plan.ID, UpdatePlanRequest{SaleSchedule: &domain.PlanSaleSchedule{EndsAt: &prior}})
	require.NoError(t, err)
	_, err = createOrder()
	require.ErrorContains(t, err, "PLAN_NOT_AVAILABLE")
	// A sale ending affects new orders, not an order's already captured price or terms.
	stored, err := client.PaymentOrder.Get(ctx, order.ID)
	require.NoError(t, err)
	require.Equal(t, 35.0, stored.Amount)
	require.Equal(t, 300.0, stored.EntitlementSnapshot["total_limit_usd"])
	version, err := config.GetPlanVersion(ctx, plan.PublishedVersionID)
	require.NoError(t, err)
	_, expires, err := normalizedSubscriptionTerm(&AssignSubscriptionInput{}, version, time.Now())
	require.NoError(t, err)
	require.WithinDuration(t, time.Now().Add(12*time.Hour), expires, time.Second)
}
