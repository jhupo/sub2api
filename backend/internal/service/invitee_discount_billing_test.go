//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCalculateCostUnified_InviteeDiscountUsesCurrentGroupRate(t *testing.T) {
	for _, tt := range []struct {
		name    string
		user    float64
		invite  *float64
		night   float64
		peak    float64
		channel float64
		want    float64
	}{
		{name: "ignores night discount", user: 1.5, invite: ratePtr(0.9), night: 0.5, peak: 1, channel: 0.6, want: 1.8},
		{name: "uses only the invitation factor", user: 1.95, invite: ratePtr(0.9), night: 0.95, peak: 1, channel: 0.98, want: 1.8},
		{name: "ignores cheaper personal pricing", user: 0.6, invite: ratePtr(0.9), night: 0.5, peak: 1, channel: 0.6, want: 1.8},
		{name: "ignores channel discount", user: 1.5, invite: ratePtr(0.9), night: 0.6, peak: 1, channel: 0.5, want: 1.8},
		{name: "ignores peak surcharge", user: 1.5, invite: ratePtr(0.9), night: 1, peak: 1.5, channel: 0.5, want: 1.8},
		{name: "ignores group and channel surcharges", user: 1.5, invite: ratePtr(0.9), night: 1, peak: 1.5, channel: 2, want: 1.8},
		{name: "ignores zero group time pricing", user: 1.5, invite: ratePtr(0.9), night: 0, peak: 1, channel: 0.6, want: 1.8},
		{name: "legacy user keeps existing pricing", user: 1.5, night: 0.5, peak: 1, channel: 0.6, want: 0.45},
	} {
		t.Run(tt.name, func(t *testing.T) {
			group := &Group{RateMultiplier: 2,
				NightRateEnabled: true, NightStart: "22:00", NightEnd: "06:00", NightRateMultiplier: tt.night,
				PeakRateEnabled: true, PeakStart: "22:00", PeakEnd: "06:00", PeakRateMultiplier: tt.peak,
			}
			rate := UserGroupRate{GroupRateMultiplier: 2, UserRateMultiplier: &tt.user, InviteeDiscountFactor: tt.invite}
			pricingAt := time.Date(2026, 11, 19, 23, 0, 0, 0, time.UTC)
			resolved := channelTimeResolvedForTest(&ModelPricing{InputPricePerToken: 0.001}, nil)
			resolved.channelPricing.TimePricing = &ChannelTimePricing{Timezone: "UTC", Periods: []ChannelTimePricingPeriod{{StartTime: "22:00", EndTime: "23:59", Multiplier: tt.channel}}}
			billing := NewBillingService(&config.Config{}, nil)
			cost, err := billing.CalculateCostUnified(CostInput{
				Ctx: context.Background(), Model: "model", Group: group,
				Tokens: UsageTokens{InputTokens: 1000}, PricingAt: pricingAt,
				RateMultiplier: rate.ResolveAt(group, pricingAt), UserGroupRate: &rate,
				Resolver: &ModelPricingResolver{}, Resolved: resolved,
			})
			require.NoError(t, err)
			wantTotal := tt.channel
			if tt.invite != nil {
				wantTotal = 1
			}
			require.InDelta(t, wantTotal, cost.TotalCost, 1e-9, "invitation pricing retains channel base unit prices without time factors")
			require.InDelta(t, tt.want, cost.ActualCost, 1e-9)
		})
	}
}
