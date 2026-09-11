package handler

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type wsTurnKeyReader struct {
	key *service.APIKey
	err error
}

type wsTurnAPIKeyRepo struct {
	service.APIKeyRepository
	key *service.APIKey
}

func (r *wsTurnAPIKeyRepo) GetByID(context.Context, int64) (*service.APIKey, error) {
	key := *r.key
	return &key, nil
}

func (r wsTurnKeyReader) GetByID(context.Context, int64) (*service.APIKey, error) {
	return r.key, r.err
}

func TestOpenAIWSTurnAdmissionRefreshesAuthorization(t *testing.T) {
	groupID := int64(9)
	newKey := func() *service.APIKey {
		return &service.APIKey{ID: 1, UserID: 2, Status: service.StatusActive, FundingSource: service.FundingSourceWallet,
			User: &service.User{ID: 2, Status: service.StatusActive, Balance: 1}, GroupID: &groupID,
			Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive}}
	}
	for _, tc := range []struct {
		name   string
		mutate func(*service.APIKey)
	}{
		{"disabled key", func(k *service.APIKey) { k.Status = service.StatusAPIKeyDisabled }},
		{"disabled user", func(k *service.APIKey) { k.User.Status = "disabled" }},
		{"expired", func(k *service.APIKey) { at := time.Now().Add(-time.Second); k.ExpiresAt = &at }},
		{"quota", func(k *service.APIKey) { k.Quota, k.QuotaUsed = 1, 1 }},
		{"5h limit", func(k *service.APIKey) { now := time.Now(); k.Window5hStart = &now; k.RateLimit5h, k.Usage5h = 1, 1 }},
		{"group removed", func(k *service.APIKey) { k.Group = nil }},
		{"group disabled", func(k *service.APIKey) { k.Group.Status = "disabled" }},
		{"group switched", func(k *service.APIKey) { id := int64(99); k.GroupID = &id }},
		{"payment switched", func(k *service.APIKey) { k.FundingSource = service.FundingSourceSubscription }},
		{"exclusive access revoked", func(k *service.APIKey) { k.Group.IsExclusive = true }},
		{"public whitelist revoked", func(k *service.APIKey) { k.User.RestrictPublicGroups = true }},
		{"platform changed", func(k *service.APIKey) { k.Group.Platform = service.PlatformAnthropic }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			initial, latest := newKey(), newKey()
			tc.mutate(latest)
			key, err := refreshOpenAIWSTurnKey(context.Background(), wsTurnKeyReader{key: latest}, initial, "127.0.0.1", false)
			require.Error(t, err)
			require.Nil(t, key)
			require.False(t, shouldReportOpenAIWSProxyAccountFailure(err), "local admission failures must not penalize a shared upstream account")
		})
	}
	initial, latest := newKey(), newKey()
	latest.User.Concurrency = 3
	key, err := refreshOpenAIWSTurnKey(context.Background(), wsTurnKeyReader{key: latest}, initial, "127.0.0.1", false)
	require.NoError(t, err)
	require.Same(t, latest, key)
	require.Zero(t, initial.User.Concurrency, "the previous usage snapshot must stay immutable")
	latest.Group.IsExclusive = true
	latest.User.AllowedGroups = []int64{groupID}
	_, err = refreshOpenAIWSTurnKey(context.Background(), wsTurnKeyReader{key: latest}, initial, "127.0.0.1", false)
	require.NoError(t, err, "authorized exclusive groups must remain usable on later turns")
}

type wsTurnBalanceCache struct {
	service.BillingCache
	balance float64
}

func (c *wsTurnBalanceCache) GetUserBalance(context.Context, int64) (float64, error) {
	return c.balance, nil
}
func (c *wsTurnBalanceCache) GetLiveBalance(context.Context, int64) (float64, bool, error) {
	return 0, false, nil
}

type wsTurnRPMCache struct {
	service.UserRPMCache
	calls int
}

func (c *wsTurnRPMCache) IncrementUserRPM(context.Context, int64) (int, error) {
	c.calls++
	return c.calls, nil
}

func TestOpenAIWSTurnBillingRechecksBalanceAndRPM(t *testing.T) {
	cfg := &config.Config{}
	cache, rpm := &wsTurnBalanceCache{balance: 1}, &wsTurnRPMCache{}
	billing := service.NewBillingCacheService(cache, nil, nil, rpm, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	h := &OpenAIGatewayHandler{billingCacheService: billing, cfg: cfg}
	key := &service.APIKey{ID: 1, UserID: 2, User: &service.User{ID: 2, Balance: 1, RPMLimit: 2}}
	require.NoError(t, h.checkOpenAIWSTurnBilling(context.Background(), key))
	cache.balance = 0
	balanceErr := h.checkOpenAIWSTurnBilling(context.Background(), key)
	require.ErrorIs(t, balanceErr, service.ErrInsufficientBalance)
	require.False(t, shouldReportOpenAIWSProxyAccountFailure(balanceErr))
	require.Equal(t, 1, rpm.calls, "unfunded turns must not consume RPM")
	cache.balance = 1
	require.NoError(t, h.checkOpenAIWSTurnBilling(context.Background(), key))
	rpmErr := h.checkOpenAIWSTurnBilling(context.Background(), key)
	require.ErrorIs(t, rpmErr, service.ErrUserRPMExceeded)
	require.False(t, shouldReportOpenAIWSProxyAccountFailure(rpmErr))
}

func TestOpenAIWSUsageBarrierWaitsForSettlementAndPreservesFailure(t *testing.T) {
	var barrier openAIWSUsageBarrier
	require.NoError(t, barrier.wait(context.Background()))
	complete := barrier.start()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, barrier.wait(ctx), context.Canceled)
	settlementErr := errors.New("billing unavailable")
	complete(settlementErr)
	complete(nil)
	for i := 0; i < 2; i++ {
		require.ErrorIs(t, barrier.wait(context.Background()), settlementErr)
	}
	complete = barrier.start()
	complete(nil)
	require.NoError(t, barrier.wait(context.Background()))
}
