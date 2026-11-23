package service

import (
	"context"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	gocache "github.com/patrickmn/go-cache"
	"golang.org/x/sync/singleflight"
)

type userGroupRateResolver struct {
	repo         UserGroupRateRepository
	cache        *gocache.Cache
	cacheTTL     time.Duration
	sf           *singleflight.Group
	logComponent string
}

type userGroupRateReader interface {
	GetByUserAndGroup(ctx context.Context, userID, groupID int64) (*float64, error)
}

type userGroupPricingReader interface {
	GetUserGroupRate(ctx context.Context, userID, groupID int64, groupDefaultMultiplier float64) (UserGroupRate, error)
}

func loadUserGroupRate(ctx context.Context, repo userGroupRateReader, userID, groupID int64, groupDefaultMultiplier float64) (UserGroupRate, error) {
	if pricingRepo, ok := repo.(userGroupPricingReader); ok {
		return pricingRepo.GetUserGroupRate(ctx, userID, groupID, groupDefaultMultiplier)
	}
	rate := UserGroupRate{GroupRateMultiplier: groupDefaultMultiplier}
	userRate, err := repo.GetByUserAndGroup(ctx, userID, groupID)
	if err != nil {
		return rate, err
	}
	rate.UserRateMultiplier = userRate
	return rate, nil
}

func newUserGroupRateResolver(repo UserGroupRateRepository, cache *gocache.Cache, cacheTTL time.Duration, sf *singleflight.Group, logComponent string) *userGroupRateResolver {
	if cacheTTL <= 0 {
		cacheTTL = defaultUserGroupRateCacheTTL
	}
	if cache == nil {
		cache = gocache.New(cacheTTL, time.Minute)
	}
	if logComponent == "" {
		logComponent = "service.gateway"
	}
	if sf == nil {
		sf = &singleflight.Group{}
	}

	return &userGroupRateResolver{
		repo:         repo,
		cache:        cache,
		cacheTTL:     cacheTTL,
		sf:           sf,
		logComponent: logComponent,
	}
}

func (r *userGroupRateResolver) Resolve(ctx context.Context, userID, groupID int64, groupDefaultMultiplier float64) float64 {
	return r.ResolveRate(ctx, userID, groupID, groupDefaultMultiplier).BaseRate()
}

func cachedUserGroupRate(value any, groupDefaultMultiplier float64) (UserGroupRate, bool) {
	switch cached := value.(type) {
	case UserGroupRate:
		// Group defaults are request snapshots, not user-specific cached state.
		cached.GroupRateMultiplier = groupDefaultMultiplier
		return cached, true
	case float64:
		return UserGroupRate{GroupRateMultiplier: groupDefaultMultiplier, UserRateMultiplier: &cached}, true
	default:
		return UserGroupRate{}, false
	}
}

func (r *userGroupRateResolver) ResolveRate(ctx context.Context, userID, groupID int64, groupDefaultMultiplier float64) UserGroupRate {
	fallback := UserGroupRate{GroupRateMultiplier: groupDefaultMultiplier}
	if r == nil || userID <= 0 || groupID <= 0 {
		return fallback
	}

	key := fmt.Sprintf("%d:%d", userID, groupID)
	if r.cache != nil {
		if cached, ok := r.cache.Get(key); ok {
			if rate, castOK := cachedUserGroupRate(cached, groupDefaultMultiplier); castOK {
				userGroupRateCacheHitTotal.Add(1)
				return rate
			}
		}
	}
	if r.repo == nil {
		return fallback
	}
	userGroupRateCacheMissTotal.Add(1)

	value, err, shared := r.sf.Do(key, func() (any, error) {
		if r.cache != nil {
			if cached, ok := r.cache.Get(key); ok {
				if rate, castOK := cachedUserGroupRate(cached, groupDefaultMultiplier); castOK {
					userGroupRateCacheHitTotal.Add(1)
					return rate, nil
				}
			}
		}

		userGroupRateCacheLoadTotal.Add(1)
		rate, repoErr := loadUserGroupRate(ctx, r.repo, userID, groupID, groupDefaultMultiplier)
		if repoErr != nil {
			return nil, repoErr
		}
		if r.cache != nil {
			r.cache.Set(key, rate, r.cacheTTL)
		}
		return rate, nil
	})
	if shared {
		userGroupRateCacheSFSharedTotal.Add(1)
	}
	if err != nil {
		userGroupRateCacheFallbackTotal.Add(1)
		logger.LegacyPrintf(r.logComponent, "get user group rate failed, fallback to group default: user=%d group=%d err=%v", userID, groupID, err)
		return fallback
	}

	rate, ok := cachedUserGroupRate(value, groupDefaultMultiplier)
	if !ok {
		userGroupRateCacheFallbackTotal.Add(1)
		return fallback
	}
	return rate
}
