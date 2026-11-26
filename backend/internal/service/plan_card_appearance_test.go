package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/domain"
	"github.com/stretchr/testify/require"
)

func TestPlanCardAppearanceValidation(t *testing.T) {
	for _, appearance := range []*domain.PlanCardAppearance{
		nil, {}, {Theme: "classic", Motion: "none"}, {Theme: "mint", Motion: "shine"}, {Theme: "ruby", Motion: "flow"},
	} {
		require.NoError(t, validatePlanCardAppearance(appearance))
	}
	for _, appearance := range []*domain.PlanCardAppearance{
		{Theme: "invalid"}, {Theme: "mint", Motion: "invalid"},
	} {
		require.Error(t, validatePlanRequired(CreatePlanRequest{CardAppearance: appearance}))
		require.Error(t, validatePlanPatch(UpdatePlanRequest{CardAppearance: appearance}))
	}
}

func TestPlanCardAppearancePersistsOnCreation(t *testing.T) {
	ctx := context.Background()
	client := newPaymentConfigServiceTestClient(t)
	config := &PaymentConfigService{entClient: client}
	initial := domain.PlanCardAppearance{Theme: "mint", Motion: "shine"}
	plan, err := config.CreatePlan(ctx, CreatePlanRequest{
		Name: "Jade", Price: 20, ValidityDays: 30, ValidityUnit: "days", ForSale: true, CardAppearance: &initial,
	})
	require.NoError(t, err)
	require.Equal(t, initial, plan.CardAppearance)
	listed, err := config.ListPlansForSale(ctx)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, initial, listed[0].CardAppearance)
	persisted, err := config.GetPlan(ctx, plan.ID)
	require.NoError(t, err)
	require.Equal(t, initial, persisted.CardAppearance)
}
