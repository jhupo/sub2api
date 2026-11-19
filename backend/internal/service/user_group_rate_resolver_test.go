package service

import (
	"context"
	"testing"
	"time"

	gocache "github.com/patrickmn/go-cache"
	"github.com/stretchr/testify/require"
)

type userGroupRateResolverRepoStub struct {
	UserGroupRateRepository

	rate  *float64
	err   error
	calls int
}

type userGroupRateDiscountRepoStub struct {
	*userGroupRateResolverRepoStub
	factor        float64
	discountErr   error
	discountCalls int
}

func (s *userGroupRateDiscountRepoStub) GetUserGroupRate(ctx context.Context, userID, groupID int64, groupDefaultMultiplier float64) (UserGroupRate, error) {
	s.discountCalls++
	resolved := UserGroupRate{GroupRateMultiplier: groupDefaultMultiplier}
	if s.discountErr != nil {
		return resolved, s.discountErr
	}
	rate, err := s.GetByUserAndGroup(ctx, userID, groupID)
	if err != nil {
		return resolved, err
	}
	resolved.UserRateMultiplier = rate
	resolved.InviteeDiscountFactor = &s.factor
	return resolved, nil
}

func (s *userGroupRateResolverRepoStub) GetByUserAndGroup(ctx context.Context, userID, groupID int64) (*float64, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.rate, nil
}

func TestNewUserGroupRateResolver_Defaults(t *testing.T) {
	resolver := newUserGroupRateResolver(nil, nil, 0, nil, "")

	require.NotNil(t, resolver)
	require.NotNil(t, resolver.cache)
	require.Equal(t, defaultUserGroupRateCacheTTL, resolver.cacheTTL)
	require.NotNil(t, resolver.sf)
	require.Equal(t, "service.gateway", resolver.logComponent)
}

func TestUserGroupRateResolverResolve_FallbackForNilResolverAndInvalidIDs(t *testing.T) {
	var nilResolver *userGroupRateResolver
	require.Equal(t, 1.4, nilResolver.Resolve(context.Background(), 101, 202, 1.4))

	resolver := newUserGroupRateResolver(nil, nil, time.Second, nil, "service.test")
	require.Equal(t, 1.4, resolver.Resolve(context.Background(), 0, 202, 1.4))
	require.Equal(t, 1.4, resolver.Resolve(context.Background(), 101, 0, 1.4))
}

func TestUserGroupRateResolverResolve_InvalidCacheEntryLoadsRepoAndCaches(t *testing.T) {
	resetGatewayHotpathStatsForTest()

	rate := 1.7
	repo := &userGroupRateResolverRepoStub{rate: &rate}
	cache := gocache.New(time.Minute, time.Minute)
	cache.Set("101:202", "bad-cache", time.Minute)
	resolver := newUserGroupRateResolver(repo, cache, time.Minute, nil, "service.test")

	got := resolver.Resolve(context.Background(), 101, 202, 1.2)
	require.Equal(t, rate, got)
	require.Equal(t, 1, repo.calls)

	cached, ok := cache.Get("101:202")
	require.True(t, ok)
	require.Equal(t, UserGroupRate{GroupRateMultiplier: 1.2, UserRateMultiplier: &rate}, cached)

	hit, miss, load, _, fallback := GatewayUserGroupRateCacheStats()
	require.Equal(t, int64(0), hit)
	require.Equal(t, int64(1), miss)
	require.Equal(t, int64(1), load)
	require.Equal(t, int64(0), fallback)
}

func TestUserGroupRateResolverResolve_InvitationUsesGroupDefault(t *testing.T) {
	userRate := 0.8
	repo := &userGroupRateDiscountRepoStub{
		userGroupRateResolverRepoStub: &userGroupRateResolverRepoStub{rate: &userRate},
		factor:                        0.9,
	}
	resolver := newUserGroupRateResolver(repo, nil, time.Minute, nil, "service.test")

	require.InDelta(t, 1.08, resolver.Resolve(context.Background(), 101, 202, 1.2), 1e-9)
	require.Equal(t, 1, repo.calls)
	require.Equal(t, 1, repo.discountCalls)
}

func TestUserGroupRateResolverResolve_DiscountReadErrorIsNotCached(t *testing.T) {
	userRate := 0.8
	repo := &userGroupRateDiscountRepoStub{
		userGroupRateResolverRepoStub: &userGroupRateResolverRepoStub{rate: &userRate},
		discountErr:                   context.DeadlineExceeded,
	}
	resolver := newUserGroupRateResolver(repo, nil, time.Minute, nil, "service.test")

	require.Equal(t, 1.2, resolver.Resolve(context.Background(), 101, 202, 1.2))
	require.Equal(t, 1, repo.discountCalls)
	repo.discountErr = nil
	repo.factor = 0.9
	require.InDelta(t, 1.08, resolver.Resolve(context.Background(), 101, 202, 1.2), 1e-9)
	require.Equal(t, 2, repo.discountCalls)
}

func TestUserGroupRateResolverResolve_CachedInvitationFollowsCurrentGroupDefault(t *testing.T) {
	personal := 0.8
	repo := &userGroupRateDiscountRepoStub{
		userGroupRateResolverRepoStub: &userGroupRateResolverRepoStub{rate: &personal},
		factor:                        0.9,
	}
	resolver := newUserGroupRateResolver(repo, nil, time.Minute, nil, "service.test")
	group := &Group{NightRateEnabled: true, NightStart: "22:00", NightEnd: "06:00", NightRateMultiplier: 0.5}
	first := resolver.ResolveRate(context.Background(), 101, 202, 1)
	require.InDelta(t, 0.9, first.ResolveAt(group, at(23, 0)), 1e-9)
	require.InDelta(t, 0.9, first.ResolveAt(group, at(6, 0)), 1e-9)
	updated := resolver.ResolveRate(context.Background(), 101, 202, 2)
	require.InDelta(t, 1.8, updated.ResolveAt(group, at(23, 0)), 1e-9)
	require.InDelta(t, 1.8, updated.BaseRate(), 1e-9)
	require.Equal(t, 1, repo.discountCalls)
}

func TestGatewayServiceGetUserGroupRateMultiplier_FallbacksAndUsesExistingResolver(t *testing.T) {
	var nilSvc *GatewayService
	require.Equal(t, 1.3, nilSvc.getUserGroupRateMultiplier(context.Background(), 101, 202, 1.3))

	rate := 1.9
	repo := &userGroupRateResolverRepoStub{rate: &rate}
	resolver := newUserGroupRateResolver(repo, nil, time.Minute, nil, "service.gateway")
	svc := &GatewayService{userGroupRateResolver: resolver}

	got := svc.getUserGroupRateMultiplier(context.Background(), 101, 202, 1.2)
	require.Equal(t, rate, got)
	require.Equal(t, 1, repo.calls)
}
