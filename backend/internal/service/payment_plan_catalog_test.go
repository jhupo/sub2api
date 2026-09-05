package service

import (
	"context"
	"testing"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/stretchr/testify/require"
)

func TestPaymentPlanCatalogKeepsHistoricalReferences(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	svc := &PaymentConfigService{entClient: client}
	create := func(name string, historical, forSale bool, days int) (*dbent.SubscriptionPlan, *dbent.SubscriptionPlanVersion) {
		plan := client.SubscriptionPlan.Create().SetName(name).SetIsHistorical(historical).SetForSale(forSale).SaveX(ctx)
		version := client.SubscriptionPlanVersion.Create().SetPlanID(plan.ID).SetVersion(1).
			SetPrice(10).SetValidityDays(days).SetValidityUnit("days").SetMonthlyLimitUsd(220).SaveX(ctx)
		plan = client.SubscriptionPlan.UpdateOne(plan).SetPublishedVersionID(version.ID).SaveX(ctx)
		return plan, version
	}
	sale, _ := create("Monthly", false, true, 30)
	private, privateVersion := create("Private grant", false, false, 7)
	historical, oldVersion := create("Monthly", true, false, 32)
	// The name and product label do not determine catalog visibility.
	_, err := client.SubscriptionPlan.UpdateOne(private).SetProductName("__sub2api_legacy_group_custom").Save(ctx)
	require.NoError(t, err)

	catalog, err := svc.ListPlans(ctx)
	require.NoError(t, err)
	require.Len(t, catalog, 2)
	require.Equal(t, sale.ID, catalog[0].ID)
	require.Equal(t, private.ID, catalog[1].ID)
	all, err := svc.ListPlansIncludingHistorical(ctx)
	require.NoError(t, err)
	require.Len(t, all, 3)
	require.True(t, all[2].IsHistorical)
	storefront, err := svc.ListPlansForSale(ctx)
	require.NoError(t, err)
	require.Len(t, storefront, 1)
	require.Equal(t, sale.ID, storefront[0].ID)

	resolved, err := svc.GetPlanVersion(ctx, oldVersion.ID)
	require.NoError(t, err)
	require.Equal(t, historical.ID, resolved.ID)
	require.Equal(t, 32, resolved.ValidityDays)
	require.Equal(t, 220.0, *resolved.MonthlyLimitUSD)
	// Existing codes and default grants resolve their original entitlement even
	// though new issuance must use the catalog.
	subscriptions := &SubscriptionService{entClient: client}
	for _, input := range []*AssignSubscriptionInput{
		{PlanVersionID: oldVersion.ID},
		{PlanID: historical.ID},
	} {
		plan, err := subscriptions.resolvePlanVersion(ctx, input)
		require.NoError(t, err)
		require.Equal(t, oldVersion.ID, plan.PublishedVersionID)
		require.Equal(t, 32, plan.ValidityDays)
	}
	require.NoError(t, validateRedeemPlanVersion(ctx, client, RedeemTypeSubscription, &privateVersion.ID))
	require.ErrorContains(t, validateRedeemPlanVersion(ctx, client, RedeemTypeSubscription, &oldVersion.ID), "historical")
	payments := &PaymentService{configService: svc}
	_, err = payments.validateSubOrder(ctx, CreateOrderRequest{PlanID: sale.ID})
	require.NoError(t, err)
	// Direct purchase by ID must also enforce the catalog boundary.
	_, err = client.SubscriptionPlan.UpdateOneID(historical.ID).SetForSale(true).Save(ctx)
	require.NoError(t, err)
	_, err = payments.validateSubOrder(ctx, CreateOrderRequest{PlanID: historical.ID})
	require.Error(t, err)
}
