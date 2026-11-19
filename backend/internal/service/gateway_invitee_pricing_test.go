//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

type gatewayInviteePricingRepo struct {
	UserGroupRateRepository
	userRate      *float64
	inviteeFactor *float64
	calls         int
}

func (r *gatewayInviteePricingRepo) GetByUserAndGroup(context.Context, int64, int64) (*float64, error) {
	return r.userRate, nil
}

func (r *gatewayInviteePricingRepo) GetUserGroupRate(_ context.Context, _, _ int64, groupDefaultMultiplier float64) (UserGroupRate, error) {
	r.calls++
	return UserGroupRate{
		GroupRateMultiplier:   groupDefaultMultiplier,
		UserRateMultiplier:    r.userRate,
		InviteeDiscountFactor: r.inviteeFactor,
	}, nil
}

type balancePreauthorizationPricer interface {
	BalancePreauthorizationCostInput(context.Context, *APIKey, string, time.Time, string, BalancePreauthorizationRateKind) CostInput
}

func newInviteePreauthorizationPricer(name string, repo UserGroupRateRepository, pricing *ModelPricingResolver) balancePreauthorizationPricer {
	rateResolver := newUserGroupRateResolver(repo, nil, time.Minute, nil, "test.invitee_pricing")
	if name == "openai" {
		return &OpenAIGatewayService{userGroupRateResolver: rateResolver, resolver: pricing}
	}
	return &GatewayService{userGroupRateResolver: rateResolver, resolver: pricing}
}

func TestBalancePreauthorizationInviteePricingAndRateScopes(t *testing.T) {
	personalRate, inviteeFactor := 0.8, 0.6
	inside := time.Date(2026, time.January, 15, 8, 30, 0, 0, timezone.Location())
	outside := inside.Add(2 * time.Hour)
	for _, serviceName := range []string{"gateway", "openai"} {
		for _, tc := range []struct {
			name          string
			userRate      *float64
			inviteeFactor *float64
			timeFactor    float64
			wantText      float64
			wantBase      float64
		}{
			{name: "invitation overrides group time discounts", inviteeFactor: &inviteeFactor, timeFactor: 0.5, wantText: 1.2, wantBase: 1.2},
			{name: "invitation overrides cheaper personal pricing", userRate: &personalRate, inviteeFactor: &inviteeFactor, timeFactor: 0.5, wantText: 1.2, wantBase: 1.2},
			{name: "invitation overrides time surcharges", userRate: &personalRate, inviteeFactor: &inviteeFactor, timeFactor: 1.5, wantText: 1.2, wantBase: 1.2},
			{name: "ordinary users keep stacked time discounts", userRate: &personalRate, timeFactor: 0.5, wantText: 0.4, wantBase: 0.8},
		} {
			t.Run(serviceName+"/"+tc.name, func(t *testing.T) {
				repo := &gatewayInviteePricingRepo{userRate: tc.userRate, inviteeFactor: tc.inviteeFactor}
				pricer := newInviteePreauthorizationPricer(serviceName, repo, nil)
				group := &Group{
					ID: 100, RateMultiplier: 2,
					PeakRateEnabled: true, PeakStart: "08:00", PeakEnd: "09:00", PeakRateMultiplier: tc.timeFactor,
					ImageRateIndependent: true, ImageRateMultiplier: 0.25,
					VideoRateIndependent: true, VideoRateMultiplier: 0.3,
				}
				apiKey := &APIKey{UserID: 42, GroupID: &group.ID, Group: group}
				for _, scope := range []struct {
					kind BalancePreauthorizationRateKind
					want float64
				}{
					{BalancePreauthorizationRateText, tc.wantText},
					{BalancePreauthorizationRateBase, tc.wantBase},
					{BalancePreauthorizationRateImage, 0.25},
					{BalancePreauthorizationRateVideo, 0.3},
				} {
					input := pricer.BalancePreauthorizationCostInput(context.Background(), apiKey, "model", inside, "", scope.kind)
					require.InDelta(t, scope.want, input.RateMultiplier, 1e-12)
					require.Equal(t, inside, input.PricingAt)
					if scope.kind == BalancePreauthorizationRateText && tc.inviteeFactor != nil {
						require.NotNil(t, input.UserGroupRate)
						require.Equal(t, tc.inviteeFactor, input.UserGroupRate.InviteeDiscountFactor)
					} else {
						require.Nil(t, input.UserGroupRate)
					}
				}
				later := pricer.BalancePreauthorizationCostInput(context.Background(), apiKey, "model", outside, "", BalancePreauthorizationRateText)
				require.InDelta(t, tc.wantBase, later.RateMultiplier, 1e-12)
				require.Equal(t, 1, repo.calls, "only rate components may be cached across pricing instants")
			})
		}
	}
}

func TestBalancePreauthorizationCarriesInviteePolicyIntoChannelTokenCosts(t *testing.T) {
	pricingAt := time.Date(2026, time.January, 15, 8, 30, 0, 0, timezone.Location())
	for _, serviceName := range []string{"gateway", "openai"} {
		for _, tc := range []struct {
			name          string
			channelFactor float64
			wantActual    float64
		}{
			{name: "invitation ignores channel time discounts", channelFactor: 0.3, wantActual: 1.2},
			{name: "invitation ignores channel time surcharges", channelFactor: 2, wantActual: 1.2},
		} {
			t.Run(serviceName+"/"+tc.name, func(t *testing.T) {
				price, personalRate, inviteeFactor := 0.001, 0.8, 0.6
				billing, pricing := newTokenCostTestEnv(t, PlatformOpenAI, []ChannelModelPricing{{
					Platform: PlatformOpenAI, Models: []string{"invitee-token-model"}, BillingMode: BillingModeToken,
					InputPrice: &price,
					TimePricing: &ChannelTimePricing{
						Timezone: timezone.Location().String(),
						Periods:  []ChannelTimePricingPeriod{{StartTime: "08:00", EndTime: "09:00", Multiplier: tc.channelFactor}},
					},
				}}, nil)
				repo := &gatewayInviteePricingRepo{userRate: &personalRate, inviteeFactor: &inviteeFactor}
				pricer := newInviteePreauthorizationPricer(serviceName, repo, pricing)
				group := &Group{
					ID: 100, Platform: PlatformOpenAI, RateMultiplier: 2,
					PeakRateEnabled: true, PeakStart: "08:00", PeakEnd: "09:00", PeakRateMultiplier: 0.5,
					ImageRateIndependent: true, ImageRateMultiplier: 0.25,
				}
				apiKey := &APIKey{UserID: 42, GroupID: &group.ID, Group: group}
				for _, kind := range []BalancePreauthorizationRateKind{BalancePreauthorizationRateText, BalancePreauthorizationRateImage} {
					input := pricer.BalancePreauthorizationCostInput(context.Background(), apiKey, "invitee-token-model", pricingAt, "", kind)
					require.NotNil(t, input.Resolved)
					require.Equal(t, BillingModeToken, input.Resolved.Mode)
					require.NotNil(t, input.UserGroupRate, "resolved token billing must carry the policy even for a media entry point")
					require.InDelta(t, 1.2, input.RateMultiplier, 1e-12)
					input.Tokens = UsageTokens{InputTokens: 1000}
					cost, err := billing.CalculateCostUnified(input)
					require.NoError(t, err)
					require.InDelta(t, 1, cost.TotalCost, 1e-12)
					require.InDelta(t, tc.wantActual, cost.ActualCost, 1e-12)
				}
				base := pricer.BalancePreauthorizationCostInput(context.Background(), apiKey, "invitee-token-model", pricingAt, "", BalancePreauthorizationRateBase)
				require.Nil(t, base.UserGroupRate, "base-only tools must not acquire token time pricing")
				require.InDelta(t, 1.2, base.RateMultiplier, 1e-12)
			})
		}
	}
}

func TestProfitControlInviteePricingIgnoresRequestTimeOffers(t *testing.T) {
	personalRate, inviteeFactor := 0.8, 0.6
	inside := time.Date(2026, time.January, 15, 8, 30, 0, 0, timezone.Location())
	outside := inside.Add(2 * time.Hour)
	for _, serviceName := range []string{"gateway", "openai"} {
		for _, tc := range []struct {
			name          string
			userRate      *float64
			inviteeFactor *float64
			timeFactor    float64
			wantText      float64
			wantBase      float64
		}{
			{name: "invitation overrides group time discounts", inviteeFactor: &inviteeFactor, timeFactor: 0.5, wantText: 1.2, wantBase: 1.2},
			{name: "invitation overrides cheaper personal pricing", userRate: &personalRate, inviteeFactor: &inviteeFactor, timeFactor: 0.5, wantText: 1.2, wantBase: 1.2},
			{name: "invitation overrides time surcharges", userRate: &personalRate, inviteeFactor: &inviteeFactor, timeFactor: 1.5, wantText: 1.2, wantBase: 1.2},
			{name: "ordinary users keep stacked time discounts", userRate: &personalRate, timeFactor: 0.5, wantText: 0.4, wantBase: 0.8},
		} {
			t.Run(serviceName+"/"+tc.name, func(t *testing.T) {
				repo := &gatewayInviteePricingRepo{userRate: tc.userRate, inviteeFactor: tc.inviteeFactor}
				rateResolver := newUserGroupRateResolver(repo, nil, time.Minute, nil, "test.profit_invitee")
				group := &Group{
					ID: 100, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true,
					RateMultiplier: 2, ProfitControlEnabled: true, ProfitMinMargin: 0.25,
					PeakRateEnabled: true, PeakStart: "08:00", PeakEnd: "09:00", PeakRateMultiplier: tc.timeFactor,
				}
				var resolveGate func(time.Time) *openAIProfitControlGate
				if serviceName == "openai" {
					svc := &OpenAIGatewayService{userGroupRateResolver: rateResolver}
					resolveGate = func(at time.Time) *openAIProfitControlGate {
						ctx := context.WithValue(context.Background(), ctxkey.Group, group)
						ctx = context.WithValue(ctx, ctxkey.UserID, int64(42))
						ctx = context.WithValue(ctx, openAIPricingAtCtxKey{}, at)
						return svc.resolveOpenAIProfitControlGate(ctx, &group.ID)
					}
				} else {
					svc := &GatewayService{userGroupRateResolver: rateResolver}
					resolveGate = func(at time.Time) *openAIProfitControlGate {
						ctx := context.WithValue(context.Background(), ctxkey.Group, group)
						ctx = context.WithValue(ctx, ctxkey.UserID, int64(42))
						ctx = context.WithValue(ctx, gatewayTokenRequestPricingAtCtxKey{}, at)
						ctx = svc.withGatewayProfitControlGate(ctx, &group.ID)
						gate, _ := ctx.Value(openAIProfitControlGateCtxKey{}).(*openAIProfitControlGate)
						return gate
					}
				}
				gate := resolveGate(inside)
				require.NotNil(t, gate)
				require.InDelta(t, tc.wantText*0.75, gate.threshold, 1e-12)
				require.Equal(t, inside, gate.pricingAt)
				later := resolveGate(outside)
				require.NotNil(t, later)
				require.InDelta(t, tc.wantBase*0.75, later.threshold, 1e-12)
				require.Equal(t, outside, later.pricingAt)
				require.Equal(t, 1, repo.calls, "gate pricing instants must not enter the rate cache")
			})
		}
	}
}
