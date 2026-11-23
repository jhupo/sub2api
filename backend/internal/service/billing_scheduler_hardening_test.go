//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestGatewaySelectionHydrationFailureReleasesAcquiredSlot(t *testing.T) {
	svc := &GatewayService{schedulerSnapshot: NewSchedulerSnapshotService(nil, nil, nil, nil, nil)}
	released := 0
	result, err := svc.newSelectionResult(context.Background(), &Account{ID: 7}, true, func() { released++ }, nil)
	require.ErrorIs(t, err, ErrSchedulerCacheNotReady)
	require.Nil(t, result)
	require.Equal(t, 1, released)

	result, err = svc.newSelectionResult(context.Background(), &Account{ID: 8}, false, nil, nil)
	require.ErrorIs(t, err, ErrSchedulerCacheNotReady)
	require.Nil(t, result)
}

func TestGatewaySelectionSuccessfulResultRetainsReleaseOwnership(t *testing.T) {
	svc := &GatewayService{}
	released := 0
	result, err := svc.newSelectionResult(context.Background(), &Account{ID: 7}, true, func() { released++ }, nil)
	require.NoError(t, err)
	require.Zero(t, released)
	require.NotNil(t, result.ReleaseFunc)
	result.ReleaseFunc()
	require.Equal(t, 1, released)
}

type cooldownMembershipRepo struct {
	AccountRepository
	account Account
	calls   int
}

func (r *cooldownMembershipRepo) ListModelAvailabilityCandidates(_ context.Context, groupID *int64, platforms []string, includeGrouped bool) ([]Account, error) {
	r.calls++
	if groupID != nil || len(platforms) != 1 || platforms[0] != PlatformOpenAI || includeGrouped {
		return nil, errors.New("unexpected snapshot pool scope")
	}
	return []Account{r.account}, nil
}

func TestSchedulerSnapshotCooldownExpiryRestoresMembershipWithoutRebuild(t *testing.T) {
	for _, field := range []string{"rate_limit", "overload", "temporary"} {
		t.Run(field, func(t *testing.T) {
			until := time.Now().Add(150 * time.Millisecond)
			account := Account{ID: 7, Platform: PlatformOpenAI, Status: StatusActive, Schedulable: true}
			switch field {
			case "rate_limit":
				account.RateLimitResetAt = &until
			case "overload":
				account.OverloadUntil = &until
			case "temporary":
				account.TempUnschedulableUntil = &until
			}
			repo := &cooldownMembershipRepo{account: account}
			cache := &decodeCacheProbe{}
			svc := NewSchedulerSnapshotService(cache, nil, repo, nil, nil)
			accounts, _, err := svc.ListSchedulableAccounts(context.Background(), nil, PlatformOpenAI, false)
			require.NoError(t, err)
			require.Empty(t, accounts)
			require.Len(t, cache.accounts, 1, "cooling members must remain in the stored snapshot")
			reads := cache.snapshotCalls
			time.Sleep(time.Until(until) + 10*time.Millisecond)
			accounts, _, err = svc.ListSchedulableAccounts(context.Background(), nil, PlatformOpenAI, false)
			require.NoError(t, err)
			require.Len(t, accounts, 1)
			require.Equal(t, int64(7), accounts[0].ID)
			require.Equal(t, 1, repo.calls)
			require.Equal(t, reads, cache.snapshotCalls, "decoded snapshot must reevaluate cooldown expiry")
		})
	}
}

func TestAdminCreateUserSnapshotsSystemPlatformQuotaDefaults(t *testing.T) {
	for _, tc := range []struct {
		name     string
		role     string
		quotas   string
		writeErr error
		wantRows int
	}{
		{name: "explicit zero and finite", quotas: `{"openai":{"daily":0,"weekly":7},"grok":{"monthly":3},"gemini":{},"deepseek":null}`, wantRows: 2},
		{name: "unlimited", quotas: `{"openai":{"daily":null}}`},
		{name: "admin", role: RoleAdmin, quotas: `{"openai":{"daily":2}}`},
		{name: "best effort failure", quotas: `{"openai":{"daily":2}}`, writeErr: errors.New("storage unavailable"), wantRows: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &userRepoStub{nextID: 40}
			quotas := &userPlatformQuotaRepoStub{bulkInsertErr: tc.writeErr}
			svc := &adminServiceImpl{
				userRepo: repo, userPlatformQuotaRepo: quotas,
				settingService: NewSettingService(&settingRepoStub{values: map[string]string{
					SettingKeyDefaultPlatformQuotas: tc.quotas,
				}}, &config.Config{}),
			}
			user, err := svc.CreateUser(context.Background(), &CreateUserInput{Email: "quota@example.test", Password: "strong-pass", Role: tc.role})
			require.NoError(t, err)
			require.Equal(t, int64(40), user.ID)
			if tc.wantRows == 0 {
				require.Empty(t, quotas.bulkInsertCalls)
				return
			}
			require.Len(t, quotas.bulkInsertCalls, 1)
			require.Len(t, quotas.bulkInsertCalls[0], tc.wantRows)
			for _, record := range quotas.bulkInsertCalls[0] {
				require.Equal(t, user.ID, record.UserID)
				if tc.name == "explicit zero and finite" && record.Platform == PlatformOpenAI {
					require.NotNil(t, record.DailyLimitUSD)
					require.Zero(t, *record.DailyLimitUSD)
					require.Equal(t, 7.0, *record.WeeklyLimitUSD)
				}
			}
		})
	}
}

func TestModelPlazaGroupFixedPriceWinsOverImageFallback(t *testing.T) {
	for _, mode := range []BillingMode{BillingModeImage, BillingModePerRequest} {
		t.Run(string(mode), func(t *testing.T) {
			fixed, fallback := 0.25, 2.0
			group := &Group{ImagePrice1K: &fallback, ModelPricing: []ChannelModelPricing{{
				Models: []string{"image-model"}, BillingMode: mode, PerRequestPrice: &fixed,
			}}}
			model := &PlazaModel{Name: "image-model", Pricing: &ChannelModelPricing{BillingMode: BillingModeImage, PerRequestPrice: &fallback}}
			(&ModelPlazaService{}).fillDisplayPricing(context.Background(), model, group)
			require.Equal(t, mode, model.Pricing.BillingMode)
			require.Equal(t, fixed, *model.Pricing.PerRequestPrice)
			require.Empty(t, model.Pricing.Intervals)
			model.Pricing.Models[0] = "changed-model"
			require.Equal(t, "image-model", group.ModelPricing[0].Models[0], "display pricing must not mutate shared group pricing")
		})
	}
}
