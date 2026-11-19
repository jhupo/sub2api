//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAffiliateGroupDiscount_EffectiveRatesAndClear(t *testing.T) {
	tx := testEntTx(t)
	ctx := dbent.NewTxContext(context.Background(), tx)
	client := tx.Client()
	affiliateRepo := &affiliateRepository{client: client}
	rateRepo := &userGroupRateRepository{sql: client}

	createUser := func(name string) *service.User {
		return mustCreateUser(t, client, &service.User{
			Email:        fmt.Sprintf("discount-%s-%d@example.com", name, time.Now().UnixNano()),
			PasswordHash: "hash", Role: service.RoleUser, Status: service.StatusActive,
		})
	}
	inviter, invitee, unrelated := createUser("inviter"), createUser("invitee"), createUser("unrelated")
	group := mustCreateGroup(t, client, &service.Group{Name: "discounted", RateMultiplier: 1.2})
	otherGroup := mustCreateGroup(t, client, &service.Group{Name: "other", RateMultiplier: 2})
	factors := map[int64]float64{group.ID: 0.9, otherGroup.ID: 0.5}
	require.NoError(t, affiliateRepo.SetInviteeGroupDiscountFactors(ctx, inviter.ID, factors))
	bound, err := affiliateRepo.BindInviter(ctx, invitee.ID, inviter.ID)
	require.NoError(t, err)
	require.True(t, bound)

	rate, err := rateRepo.GetEffectiveByUserAndGroup(ctx, invitee.ID, group.ID, 1.2)
	require.NoError(t, err)
	require.InDelta(t, 1.08, rate, 1e-9)
	rate, err = rateRepo.GetEffectiveByUserAndGroup(ctx, inviter.ID, group.ID, 1.2)
	require.NoError(t, err)
	require.InDelta(t, 1.2, rate, 1e-9, "discount must not apply to inviter")
	rate, err = rateRepo.GetEffectiveByUserAndGroup(ctx, unrelated.ID, group.ID, 1.2)
	require.NoError(t, err)
	require.InDelta(t, 1.2, rate, 1e-9)

	personalRate := 0.8
	require.NoError(t, rateRepo.SyncUserGroupRates(ctx, invitee.ID, map[int64]*float64{group.ID: &personalRate}))
	rate, err = rateRepo.GetEffectiveByUserAndGroup(ctx, invitee.ID, group.ID, 1.2)
	require.NoError(t, err)
	require.InDelta(t, 1.08, rate, 1e-9)
	details, err := rateRepo.GetUserGroupRate(ctx, invitee.ID, group.ID, 1.2)
	require.NoError(t, err)
	require.InDelta(t, 0.8, *details.UserRateMultiplier, 1e-9)
	require.InDelta(t, 0.9, *details.InviteeDiscountFactor, 1e-9)
	require.InDelta(t, 1.08, details.Resolve(0.5), 1e-9, "invitation price ignores time discounts")
	shown, err := rateRepo.GetEffectiveByUserID(ctx, invitee.ID)
	require.NoError(t, err)
	require.InDelta(t, rate, shown[group.ID], 1e-9)
	require.InDelta(t, 1, shown[otherGroup.ID], 1e-9)
	configured, err := rateRepo.GetByUserID(ctx, invitee.ID)
	require.NoError(t, err)
	require.InDelta(t, 0.8, configured[group.ID], 1e-9, "admin override must remain unchanged")
	require.NoError(t, client.Group.UpdateOneID(group.ID).SetRateMultiplier(2).Exec(ctx))
	shown, err = rateRepo.GetEffectiveByUserID(ctx, invitee.ID)
	require.NoError(t, err)
	require.InDelta(t, 1.8, shown[group.ID], 1e-9, "stored discount follows a changed group rate")

	entries, _, err := affiliateRepo.ListUsersWithCustomSettings(ctx, service.AffiliateAdminFilter{Page: 1, PageSize: 200})
	require.NoError(t, err)
	found := false
	for _, entry := range entries {
		if entry.UserID == inviter.ID {
			found = true
			require.Equal(t, factors, entry.InviteeGroupDiscountFactors)
		}
	}
	require.True(t, found, "discount-only inviter must appear in custom settings")

	require.NoError(t, affiliateRepo.ClearUserAffiliateSettings(ctx, inviter.ID))
	rate, err = rateRepo.GetEffectiveByUserAndGroup(ctx, invitee.ID, group.ID, 1.2)
	require.NoError(t, err)
	require.InDelta(t, 0.8, rate, 1e-9)
}
