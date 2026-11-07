package service

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

func TestPlanSalePriceStepsAndDefaultDeadline(t *testing.T) {
	start := time.Date(2026, 11, 9, 10, 0, 0, 0, timezone.Location())
	deadline, saleEnd := start.Add(12*time.Hour), start.Add(10*time.Hour)
	plan := &SubscriptionPlan{ForSale: true, CardMode: "deadline", DeadlineAt: &deadline, Price: 45,
		SaleSchedule: domain.PlanSaleSchedule{PriceSteps: []domain.PlanPriceStep{{At: start.Add(time.Hour), Price: 35}, {At: start.Add(2 * time.Hour), Price: 25}}}}
	for _, tc := range []struct {
		at        time.Time
		price     float64
		available bool
	}{
		{start, 45, true}, {start.Add(time.Hour - time.Nanosecond), 45, true},
		{start.Add(time.Hour), 35, true}, {start.Add(2 * time.Hour), 25, true}, {deadline, 25, false},
	} {
		plan.ApplySaleAt(tc.at)
		require.Equal(t, tc.price, plan.CurrentPrice)
		require.Equal(t, tc.available, plan.SaleAvailable)
		require.Equal(t, deadline, *plan.SaleEndsAt)
		require.Equal(t, 45.0, plan.Price)
	}
	plan.SaleSchedule.EndsAt = &saleEnd
	plan.ApplySaleAt(saleEnd)
	require.False(t, plan.SaleAvailable)
	require.Equal(t, deadline, *plan.DeadlineAt)
	plan.ForSale = false
	plan.ApplySaleAt(start)
	require.False(t, plan.SaleAvailable)
}

func TestPlanDailySaleWindowsCrossMidnight(t *testing.T) {
	plan := &SubscriptionPlan{ForSale: true, CardMode: "hours", Price: 20, SaleSchedule: domain.PlanSaleSchedule{
		DailyWindows: []domain.PlanSaleWindow{{Start: "22:00", End: "08:00"}, {Start: "08:00", End: "09:00"}, {Start: "12:00", End: "13:00"}},
	}}
	for _, tc := range []struct {
		hour, minute     int
		available        bool
		endHour, nextDay int
	}{
		{21, 59, false, 0, 0}, {22, 0, true, 9, 1}, {0, 0, true, 9, 0}, {8, 59, true, 9, 0},
		{9, 0, false, 0, 0}, {12, 0, true, 13, 0}, {13, 0, false, 0, 0},
	} {
		at := time.Date(2026, 11, 9, tc.hour, tc.minute, 0, 0, timezone.Location())
		plan.ApplySaleAt(at)
		require.Equal(t, tc.available, plan.SaleAvailable, "%02d:%02d", tc.hour, tc.minute)
		if tc.available {
			require.Equal(t, time.Date(2026, 11, 9+tc.nextDay, tc.endHour, 0, 0, 0, timezone.Location()), *plan.SaleEndsAt)
		}
	}
	end := time.Date(2026, 11, 9, 23, 0, 0, 0, timezone.Location())
	plan.SaleSchedule.EndsAt = &end
	plan.ApplySaleAt(end.Add(-time.Minute))
	require.Equal(t, end, *plan.SaleEndsAt)
	plan.ApplySaleAt(end)
	require.False(t, plan.SaleAvailable)
}

func TestPlanSaleScheduleValidation(t *testing.T) {
	now := time.Now()
	deadline := now.Add(12 * time.Hour)
	for _, tc := range []struct {
		name, mode string
		schedule   domain.PlanSaleSchedule
	}{
		{"recurring", "", domain.PlanSaleSchedule{EndsAt: &now}},
		{"late removal", "deadline", domain.PlanSaleSchedule{EndsAt: ptrTime(deadline.Add(time.Hour))}},
		{"late price", "deadline", domain.PlanSaleSchedule{PriceSteps: []domain.PlanPriceStep{{At: deadline, Price: 5}}}},
		{"duplicate price", "hours", domain.PlanSaleSchedule{PriceSteps: []domain.PlanPriceStep{{At: now, Price: 5}, {At: now, Price: 6}}}},
		{"invalid price", "hours", domain.PlanSaleSchedule{PriceSteps: []domain.PlanPriceStep{{At: now, Price: math.NaN()}}}},
		{"invalid time", "hours", domain.PlanSaleSchedule{DailyWindows: []domain.PlanSaleWindow{{Start: "24:00", End: "08:00"}}}},
		{"equal time", "hours", domain.PlanSaleSchedule{DailyWindows: []domain.PlanSaleWindow{{Start: "08:00", End: "08:00"}}}},
		{"overlap", "hours", domain.PlanSaleSchedule{DailyWindows: []domain.PlanSaleWindow{{Start: "22:00", End: "08:00"}, {Start: "07:00", End: "09:00"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) { require.Error(t, validateSaleSchedule(tc.mode, &deadline, &tc.schedule)) })
	}
}

func TestPlanSalePersistenceAndPurchaseValidation(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	config := &PaymentConfigService{entClient: client}
	payments := &PaymentService{configService: config}
	deadline, prior, total := time.Now().Add(24*time.Hour), time.Now().Add(-time.Hour), 300.0
	created, err := config.CreatePlan(ctx, CreatePlanRequest{Name: "Scheduled card", Price: 45, ForSale: true, CardMode: "deadline", DeadlineAt: &deadline, TotalLimitUSD: &total,
		SaleSchedule: &domain.PlanSaleSchedule{PriceSteps: []domain.PlanPriceStep{{At: prior, Price: 35}}}})
	require.NoError(t, err)
	available, err := config.ListPlansForSale(ctx)
	require.NoError(t, err)
	require.Len(t, available, 1)
	require.Equal(t, 35.0, available[0].CurrentPrice)
	purchase, err := payments.validateSubOrder(ctx, CreateOrderRequest{PlanID: created.ID, Amount: 35})
	require.NoError(t, err)
	require.Equal(t, 35.0, purchase.Price)
	_, err = payments.validateSubOrder(ctx, CreateOrderRequest{PlanID: created.ID, Amount: 45})
	require.Error(t, err)
	_, err = client.SubscriptionPlan.UpdateOneID(created.ID).SetSaleSchedule(domain.PlanSaleSchedule{EndsAt: &prior}).Save(ctx)
	require.NoError(t, err)
	available, err = config.ListPlansForSale(ctx)
	require.NoError(t, err)
	require.Empty(t, available)
	_, err = payments.validateSubOrder(ctx, CreateOrderRequest{PlanID: created.ID, Amount: 35})
	require.Error(t, err)
	version, err := config.GetPlanVersion(ctx, created.PublishedVersionID)
	require.NoError(t, err)
	require.Equal(t, 45.0, version.Price)
	require.Equal(t, 300.0, *version.TotalLimitUSD)
	require.WithinDuration(t, deadline, *version.DeadlineAt, time.Millisecond)
}
