//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIWSTurnModelEnforcesChannelPricingSources(t *testing.T) {
	for _, source := range []string{BillingModelSourceRequested, BillingModelSourceChannelMapped, BillingModelSourceUpstream} {
		t.Run(source, func(t *testing.T) {
			groupID := int64(10)
			allowed := "gpt-5.6-sol"
			if source == BillingModelSourceChannelMapped {
				allowed = "channel-sol"
			}
			if source == BillingModelSourceUpstream {
				allowed = "upstream-sol"
			}
			channel := Channel{ID: 1, Status: StatusActive, GroupIDs: []int64{groupID}, RestrictModels: true, BillingModelSource: source,
				ModelPricing: []ChannelModelPricing{{Platform: PlatformOpenAI, Models: []string{allowed}}},
				ModelMapping: map[string]map[string]string{PlatformOpenAI: {"gpt-5.6-sol": "channel-sol", "gpt-5.6-terra": "channel-terra"}}}
			svc := &OpenAIGatewayService{channelService: newTestChannelService(makeStandardRepo(channel, map[int64]string{groupID: PlatformOpenAI}))}
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Credentials: map[string]any{
				"model_mapping": map[string]any{"channel-sol": "upstream-sol", "channel-terra": "upstream-terra"},
			}}
			for _, model := range []string{"gpt-5.6-sol", "gpt-5.6-terra"} {
				mapping, _ := svc.ResolveChannelMappingAndRestrict(context.Background(), &groupID, model)
				err := svc.ValidateOpenAIWSTurnModel(context.Background(), &groupID, account, model, mapping)
				if model == "gpt-5.6-sol" {
					require.NoError(t, err)
				} else {
					require.ErrorIs(t, err, ErrNoAvailableAccounts)
				}
			}
		})
	}
}
