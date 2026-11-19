//go:build unit

package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
)

func TestInviteeDiscountRecordUsageGatewayAndOpenAI(t *testing.T) {
	for _, engine := range []string{"gateway", "openai"} {
		for _, tc := range []struct {
			name          string
			hour          int
			channelFactor float64
		}{
			{name: "ignores personal and night offers", hour: 23, channelFactor: 0.7},
			{name: "ignores channel offer", hour: 23, channelFactor: 0.4},
			{name: "ignores peak and channel surcharges", hour: 15, channelFactor: 1.5},
		} {
			t.Run(engine+"/"+tc.name, func(t *testing.T) {
				const groupID int64 = 904
				const model = "gpt-5.1"
				personal := 0.8
				rateRepo := &userGroupRateDiscountRepoStub{
					userGroupRateResolverRepoStub: &userGroupRateResolverRepoStub{rate: &personal},
					factor:                        0.9,
				}
				usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
				userRepo := &openAIRecordUsageUserRepoStub{}
				group := &Group{
					ID: groupID, RateMultiplier: 1,
					PeakRateEnabled: true, PeakStart: "14:00", PeakEnd: "18:00", PeakRateMultiplier: 2,
					NightRateEnabled: true, NightStart: "22:00", NightEnd: "08:00", NightRateMultiplier: 0.5,
				}
				apiKey := &APIKey{ID: 804, UserID: 604, GroupID: i64p(groupID), Group: group}
				pricingAt := time.Date(2026, time.July, 14, tc.hour, 0, 0, 0, timezone.Location())
				resolver := newOpenAITokenImageChannelPricingResolverWithTimeForTest(t, groupID, model, &ChannelTimePricing{
					Timezone: "UTC",
					Periods:  []ChannelTimePricingPeriod{{StartTime: "00:00", EndTime: "23:59:59", Multiplier: tc.channelFactor}},
				})
				var record func(string) error
				if engine == "gateway" {
					svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{})
					svc.resolver = resolver
					svc.userGroupRateResolver = newUserGroupRateResolver(rateRepo, nil, time.Minute, nil, "service.test")
					record = func(requestID string) error {
						return svc.RecordUsage(context.Background(), &RecordUsageInput{
							Result: &ForwardResult{RequestID: requestID, Model: model, Usage: ClaudeUsage{InputTokens: 1000, OutputTokens: 500}},
							APIKey: apiKey, User: &User{ID: 604}, Account: &Account{ID: 704}, PricingAt: pricingAt,
						})
					}
				} else {
					svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, &openAIRecordUsageSubRepoStub{}, rateRepo)
					svc.resolver = resolver
					record = func(requestID string) error {
						return svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
							Result: &OpenAIForwardResult{RequestID: requestID, Model: model, Usage: OpenAIUsage{InputTokens: 1000, OutputTokens: 500}},
							APIKey: apiKey, User: &User{ID: 604}, Account: &Account{ID: 704}, PricingAt: pricingAt,
						})
					}
				}
				for index, groupRate := range []float64{1, 2} {
					group.RateMultiplier = groupRate
					require.NoError(t, record(fmt.Sprintf("invitee_%s_%d", engine, index)))
					require.NotNil(t, usageRepo.lastLog)
					baseCost := 1000*3e-6 + 500*15e-6
					wantRate := groupRate * 0.9
					wantActual := baseCost * wantRate
					require.InDelta(t, baseCost, usageRepo.lastLog.TotalCost, 1e-12)
					require.InDelta(t, wantActual, usageRepo.lastLog.ActualCost, 1e-12)
					require.InDelta(t, wantRate, usageRepo.lastLog.RateMultiplier, 1e-12)
					require.Equal(t, index+1, userRepo.deductCalls)
					require.InDelta(t, wantActual, userRepo.lastAmount, 1e-12)
					require.Equal(t, 1, rateRepo.discountCalls, "changing the group default must not depend on invitation cache expiry")
				}
			})
		}
	}
}
