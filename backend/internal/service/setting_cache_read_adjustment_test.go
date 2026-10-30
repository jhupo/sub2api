package service

import (
	"context"
	"errors"
	"github.com/stretchr/testify/require"
	"sync"
	"testing"
)

func TestCacheReadAdjustmentSettingsSelectionAndDisable(t *testing.T) {
	ctx := context.Background()
	repo := &panelRateLimitSettingRepo{}
	s := newPanelRateLimitTestService(repo)
	settings, err := s.GetCacheReadAdjustmentSettings(ctx)
	require.NoError(t, err)
	require.False(t, settings.Enabled)
	require.Equal(t, 10, settings.Percent)
	require.Zero(t, s.CacheReadAdjustmentPercent(ctx, 7))

	err = s.SetCacheReadAdjustmentSettings(ctx, &CacheReadAdjustmentSettings{Enabled: true, Percent: 10, AccountIDs: []int64{7, 8, 7}})
	require.NoError(t, err)
	require.Equal(t, 10, s.CacheReadAdjustmentPercent(ctx, 7))
	require.Zero(t, s.CacheReadAdjustmentPercent(ctx, 9))
	settings, err = s.GetCacheReadAdjustmentSettings(ctx)
	require.NoError(t, err)
	require.Equal(t, []int64{7, 8}, settings.AccountIDs)
	settings.AccountIDs[0] = 99
	require.Equal(t, 10, s.CacheReadAdjustmentPercent(ctx, 7))
	settings.Enabled = false
	require.NoError(t, s.SetCacheReadAdjustmentSettings(ctx, settings))
	require.Zero(t, s.CacheReadAdjustmentPercent(ctx, 8))
}

func TestCacheReadAdjustmentConcurrentPatchPreservesAccounts(t *testing.T) {
	s := newPanelRateLimitTestService(&panelRateLimitSettingRepo{})
	ctx := context.Background()
	require.NoError(t, s.SetCacheReadAdjustmentSettings(ctx, &CacheReadAdjustmentSettings{Enabled: true, Percent: 25, AccountIDs: []int64{1}}))
	var wg sync.WaitGroup
	for id := int64(2); id <= 30; id++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.PatchCacheReadAdjustmentSettings(ctx, &CacheReadAdjustmentPatch{AddAccountIDs: []int64{id, id}})
			require.NoError(t, err)
		}()
	}
	wg.Wait()
	settings, err := s.GetCacheReadAdjustmentSettings(ctx)
	require.NoError(t, err)
	require.Len(t, settings.AccountIDs, 30)
	require.True(t, settings.Enabled)
	require.Equal(t, 25, settings.Percent)
	zero := 0
	settings, err = s.PatchCacheReadAdjustmentSettings(ctx, &CacheReadAdjustmentPatch{Percent: &zero, RemoveAccountIDs: []int64{1, 30}})
	require.NoError(t, err)
	require.Len(t, settings.AccountIDs, 28)
	require.Zero(t, settings.Percent)
	require.NotContains(t, settings.AccountIDs, int64(1))
	require.NotContains(t, settings.AccountIDs, int64(30))
}

func TestCacheReadAdjustmentRejectsInvalidSettings(t *testing.T) {
	s := newPanelRateLimitTestService(&panelRateLimitSettingRepo{})
	for _, settings := range []*CacheReadAdjustmentSettings{nil, {Percent: -1}, {Percent: 101}, {Percent: 10, AccountIDs: []int64{-1}}} {
		require.Error(t, s.SetCacheReadAdjustmentSettings(context.Background(), settings))
	}
	for _, repo := range []*panelRateLimitSettingRepo{
		{getValueErr: errors.New("db unavailable")},
		{values: map[string]string{SettingKeyCacheReadAdjustment: `{"enabled":true,"percent":101,"account_ids":[7]}`}},
	} {
		s := newPanelRateLimitTestService(repo)
		require.Zero(t, s.CacheReadAdjustmentPercent(context.Background(), 7))
	}
}
