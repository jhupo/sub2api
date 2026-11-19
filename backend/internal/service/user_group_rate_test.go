package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestUserGroupRate_InvitationFollowsGroupDefault(t *testing.T) {
	for _, tt := range []struct {
		name       string
		group      float64
		personal   *float64
		invitation *float64
		factors    []float64
		want       float64
	}{
		{name: "invitation", group: 2, invitation: ratePtr(0.9), want: 1.8},
		{name: "personal does not override invitation", group: 2, personal: ratePtr(1.5), invitation: ratePtr(0.9), want: 1.8},
		{name: "invitation uses group", group: 2, personal: ratePtr(1.9), invitation: ratePtr(0.9), want: 1.8},
		{name: "night does not change invitation", group: 1, invitation: ratePtr(0.9), factors: []float64{0.5}, want: 0.9},
		{name: "personal and night ignored", group: 2, personal: ratePtr(1.5), invitation: ratePtr(0.9), factors: []float64{0.5}, want: 1.8},
		{name: "channel and night ignored", group: 2, personal: ratePtr(1.5), invitation: ratePtr(0.9), factors: []float64{0.5, 0.6}, want: 1.8},
		{name: "peak does not change invitation", group: 2, personal: ratePtr(1.5), invitation: ratePtr(0.9), factors: []float64{1.5}, want: 1.8},
		{name: "all extra factors ignored", group: 2, invitation: ratePtr(0.9), factors: []float64{1.5, 0.5, 2}, want: 1.8},
		{name: "full price follows group", group: 2, personal: ratePtr(1.5), invitation: ratePtr(1), factors: []float64{0.5}, want: 2},
		{name: "zero time offer ignored", group: 2, invitation: ratePtr(0.9), factors: []float64{0, 2}, want: 1.8},
		{name: "zero personal ignored", group: 2, personal: ratePtr(0), invitation: ratePtr(0.9), factors: []float64{2}, want: 1.8},
		{name: "free group", group: 0, personal: ratePtr(1.5), invitation: ratePtr(0.9), factors: []float64{0.5, 2}, want: 0},
		{name: "legacy personal", group: 2, personal: ratePtr(1.5), factors: []float64{0.5, 0.6}, want: 0.45},
		{name: "legacy default", group: 2, factors: []float64{0.5, 0.6}, want: 0.6},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rate := UserGroupRate{GroupRateMultiplier: tt.group, UserRateMultiplier: tt.personal, InviteeDiscountFactor: tt.invitation}
			require.InDelta(t, tt.want, rate.Resolve(tt.factors...), 1e-9)
		})
	}
}

func ratePtr(rate float64) *float64 { return &rate }
