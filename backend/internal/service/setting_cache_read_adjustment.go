package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const SettingKeyCacheReadAdjustment = "cache_read_adjustment"

type CacheReadAdjustmentSettings struct {
	Enabled    bool    `json:"enabled"`
	Percent    int     `json:"percent"`
	AccountIDs []int64 `json:"account_ids"`
}

type CacheReadAdjustmentPatch struct {
	Enabled          *bool   `json:"enabled"`
	Percent          *int    `json:"percent"`
	AddAccountIDs    []int64 `json:"add_account_ids"`
	RemoveAccountIDs []int64 `json:"remove_account_ids"`
}

func (patch *CacheReadAdjustmentPatch) Validate() error {
	if patch == nil || (patch.Enabled == nil && patch.Percent == nil && len(patch.AddAccountIDs) == 0 && len(patch.RemoveAccountIDs) == 0) {
		return fmt.Errorf("at least one change is required")
	}
	if len(patch.AddAccountIDs) > 1000 || len(patch.RemoveAccountIDs) > 1000 {
		return fmt.Errorf("at most 1000 account IDs may be changed per request")
	}
	settings := defaultCacheReadAdjustment()
	if patch.Percent != nil {
		settings.Percent = *patch.Percent
	}
	settings.AccountIDs = append(append([]int64{}, patch.AddAccountIDs...), patch.RemoveAccountIDs...)
	if err := settings.Validate(); err != nil {
		return err
	}
	added := make(map[int64]struct{}, len(patch.AddAccountIDs))
	for _, id := range patch.AddAccountIDs {
		added[id] = struct{}{}
	}
	for _, id := range patch.RemoveAccountIDs {
		if _, ok := added[id]; ok {
			return fmt.Errorf("cannot add and remove the same account ID")
		}
	}
	return nil
}

// AtomicSettingUpdater serializes read-modify-write operations across server instances.
type AtomicSettingUpdater interface {
	UpdateValue(ctx context.Context, key string, update func(string) (string, error)) error
}

type cachedCacheReadAdjustment struct {
	settings  CacheReadAdjustmentSettings
	accounts  map[int64]struct{}
	expiresAt time.Time
}

func defaultCacheReadAdjustment() *CacheReadAdjustmentSettings {
	return &CacheReadAdjustmentSettings{Percent: 10, AccountIDs: []int64{}}
}

func (settings *CacheReadAdjustmentSettings) Validate() error {
	if settings == nil || settings.Percent < 0 || settings.Percent > 100 {
		return fmt.Errorf("percent must be an integer between 0 and 100")
	}
	if len(settings.AccountIDs) > 100000 {
		return fmt.Errorf("too many account IDs")
	}
	for _, id := range settings.AccountIDs {
		if id <= 0 {
			return fmt.Errorf("account IDs must be positive")
		}
	}
	return nil
}

func (s *SettingService) GetCacheReadAdjustmentSettings(ctx context.Context) (*CacheReadAdjustmentSettings, error) {
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyCacheReadAdjustment)
	if errors.Is(err, ErrSettingNotFound) || (err == nil && raw == "") {
		return defaultCacheReadAdjustment(), nil
	}
	if err != nil {
		return nil, err
	}
	return decodeCacheReadAdjustment(raw)
}

func decodeCacheReadAdjustment(raw string) (*CacheReadAdjustmentSettings, error) {
	settings := defaultCacheReadAdjustment()
	if raw == "" {
		return settings, nil
	}
	if err := json.Unmarshal([]byte(raw), settings); err != nil {
		return nil, fmt.Errorf("decode cache read adjustment settings: %w", err)
	}
	if err := settings.Validate(); err != nil {
		return nil, err
	}
	return settings, nil
}

func (s *SettingService) PatchCacheReadAdjustmentSettings(ctx context.Context, patch *CacheReadAdjustmentPatch) (*CacheReadAdjustmentSettings, error) {
	if err := patch.Validate(); err != nil {
		return nil, infraerrors.BadRequest("INVALID_CACHE_ADJUSTMENT", err.Error())
	}
	s.cacheReadAdjustmentMu.Lock()
	defer s.cacheReadAdjustmentMu.Unlock()
	var next *CacheReadAdjustmentSettings
	update := func(raw string) (string, error) {
		settings, err := decodeCacheReadAdjustment(raw)
		if err != nil {
			return "", err
		}
		if patch.Enabled != nil {
			settings.Enabled = *patch.Enabled
		}
		if patch.Percent != nil {
			settings.Percent = *patch.Percent
		}
		removed := make(map[int64]struct{}, len(patch.RemoveAccountIDs))
		for _, id := range patch.RemoveAccountIDs {
			removed[id] = struct{}{}
		}
		settings.AccountIDs = slices.DeleteFunc(settings.AccountIDs, func(id int64) bool {
			_, ok := removed[id]
			return ok
		})
		settings.AccountIDs = append(settings.AccountIDs, patch.AddAccountIDs...)
		slices.Sort(settings.AccountIDs)
		settings.AccountIDs = slices.Compact(settings.AccountIDs)
		if err := settings.Validate(); err != nil {
			return "", infraerrors.BadRequest("INVALID_CACHE_ADJUSTMENT", err.Error())
		}
		data, err := json.Marshal(settings)
		next = settings
		return string(data), err
	}
	if repo, ok := s.settingRepo.(AtomicSettingUpdater); ok {
		if err := repo.UpdateValue(ctx, SettingKeyCacheReadAdjustment, update); err != nil {
			return nil, err
		}
	} else {
		raw, err := s.settingRepo.GetValue(ctx, SettingKeyCacheReadAdjustment)
		if err != nil && !errors.Is(err, ErrSettingNotFound) {
			return nil, err
		}
		value, err := update(raw)
		if err != nil {
			return nil, err
		}
		if err := s.settingRepo.Set(ctx, SettingKeyCacheReadAdjustment, value); err != nil {
			return nil, err
		}
	}
	s.storeCacheReadAdjustment(*next, 5*time.Second)
	return next, nil
}

func (s *SettingService) SetCacheReadAdjustmentSettings(ctx context.Context, settings *CacheReadAdjustmentSettings) error {
	if err := settings.Validate(); err != nil {
		return err
	}
	next := *settings
	next.AccountIDs = append([]int64{}, settings.AccountIDs...)
	slices.Sort(next.AccountIDs)
	next.AccountIDs = slices.Compact(next.AccountIDs)
	data, err := json.Marshal(next)
	if err != nil {
		return err
	}
	// Serialize reads and writes so an in-flight stale read cannot undo a save.
	s.cacheReadAdjustmentMu.Lock()
	defer s.cacheReadAdjustmentMu.Unlock()
	if err := s.settingRepo.Set(ctx, SettingKeyCacheReadAdjustment, string(data)); err != nil {
		return err
	}
	s.storeCacheReadAdjustment(next, 5*time.Second)
	return nil
}

func (s *SettingService) CacheReadAdjustmentPercent(ctx context.Context, accountID int64) int {
	if s == nil || s.settingRepo == nil || accountID <= 0 {
		return 0
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cached, _ := s.cacheReadAdjustmentCache.Load().(*cachedCacheReadAdjustment)
	if cached == nil || time.Now().After(cached.expiresAt) {
		s.cacheReadAdjustmentMu.Lock()
		defer s.cacheReadAdjustmentMu.Unlock()
		cached, _ = s.cacheReadAdjustmentCache.Load().(*cachedCacheReadAdjustment)
		if cached == nil || time.Now().After(cached.expiresAt) {
			dbCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			settings, err := s.GetCacheReadAdjustmentSettings(dbCtx)
			cancel()
			ttl := 5 * time.Second
			if err != nil {
				slog.Warn("cache read adjustment settings unavailable", "error", err)
				settings, ttl = defaultCacheReadAdjustment(), time.Second
			}
			cached = s.storeCacheReadAdjustment(*settings, ttl)
		}
	}
	if !cached.settings.Enabled {
		return 0
	}
	if _, ok := cached.accounts[accountID]; !ok {
		return 0
	}
	return cached.settings.Percent
}

func (s *SettingService) storeCacheReadAdjustment(settings CacheReadAdjustmentSettings, ttl time.Duration) *cachedCacheReadAdjustment {
	cached := &cachedCacheReadAdjustment{settings: settings, accounts: make(map[int64]struct{}, len(settings.AccountIDs)), expiresAt: time.Now().Add(ttl)}
	for _, id := range settings.AccountIDs {
		cached.accounts[id] = struct{}{}
	}
	s.cacheReadAdjustmentCache.Store(cached)
	return cached
}
