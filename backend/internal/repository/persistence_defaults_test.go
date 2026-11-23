package repository

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestGroupRepositoryCacheReadAdjustmentDefaults(t *testing.T) {
	for _, tt := range []struct {
		name string
		mode string
		want string
	}{
		{name: "omitted", want: "inherit"},
		{name: "inherit", mode: "inherit", want: "inherit"},
		{name: "custom", mode: "custom", want: "custom"},
		{name: "disabled", mode: "disabled", want: "disabled"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			keyRepo, client := newAPIKeyRepoSQLite(t)
			repo := newGroupRepositoryWithSQL(client, keyRepo.sql)
			group := &service.Group{
				Name:                    "defaults-" + tt.name,
				Platform:                service.PlatformOpenAI,
				Status:                  service.StatusActive,
				RateMultiplier:          1,
				CacheReadAdjustmentMode: tt.mode,
			}

			require.NoError(t, repo.Create(ctx, group))
			require.Equal(t, tt.want, group.CacheReadAdjustmentMode)
			got, err := repo.GetByID(ctx, group.ID)
			require.NoError(t, err)
			require.Equal(t, tt.want, got.CacheReadAdjustmentMode)

			group.CacheReadAdjustmentMode = tt.mode
			group.Name += "-updated"
			require.NoError(t, repo.Update(ctx, group))
			require.Equal(t, tt.want, group.CacheReadAdjustmentMode)
			got, err = repo.GetByID(ctx, group.ID)
			require.NoError(t, err)
			require.Equal(t, tt.want, got.CacheReadAdjustmentMode)
		})
	}
}

func TestAPIKeyRepositorySubscriptionPriorityCreateDefaults(t *testing.T) {
	for _, tt := range []struct {
		name     string
		priority []int64
	}{
		{name: "omitted"},
		{name: "empty", priority: []int64{}},
		{name: "ordered", priority: []int64{42, 41}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			repo, client := newAPIKeyRepoSQLite(t)
			user := mustCreateAPIKeyRepoUser(t, ctx, client, tt.name+"-priority@test.com")
			key := &service.APIKey{
				UserID:               user.ID,
				Key:                  "sk-priority-" + tt.name,
				Status:               service.StatusActive,
				SubscriptionPriority: tt.priority,
			}

			require.NoError(t, repo.Create(ctx, key))
			require.Equal(t, service.FundingSourceWallet, key.FundingSource)
			assertStoredSubscriptionPriority(t, ctx, repo, key.ID, tt.priority)
		})
	}
}

func TestAPIKeyRepositorySubscriptionPriorityUpdateDefaults(t *testing.T) {
	ctx := context.Background()
	repo, client := newAPIKeyRepoSQLite(t)
	user := mustCreateAPIKeyRepoUser(t, ctx, client, "update-priority@test.com")
	key := &service.APIKey{
		UserID:               user.ID,
		Key:                  "sk-update-priority",
		Status:               service.StatusActive,
		SubscriptionPriority: []int64{41},
	}
	require.NoError(t, repo.Create(ctx, key))

	key.SubscriptionPriority = nil
	key.Name = "rename-only"
	require.NoError(t, repo.Update(ctx, key, service.APIKeyUpdateFields{Name: true}))
	assertStoredSubscriptionPriority(t, ctx, repo, key.ID, []int64{41})

	for _, priority := range [][]int64{nil, {}, {42, 41}} {
		key.SubscriptionPriority = priority
		require.NoError(t, repo.Update(ctx, key, service.APIKeyUpdateFields{FundingSource: true}))
		assertStoredSubscriptionPriority(t, ctx, repo, key.ID, priority)
	}
}

func assertStoredSubscriptionPriority(t *testing.T, ctx context.Context, repo *apiKeyRepository, id int64, want []int64) {
	t.Helper()
	var stored sql.NullString
	require.NoError(t, scanSingleRow(ctx, repo.sql, "SELECT subscription_priority FROM api_keys WHERE id = $1", []any{id}, &stored))
	if want == nil {
		require.False(t, stored.Valid, "omitted priority must be SQL NULL, not JSON null")
		return
	}
	require.True(t, stored.Valid)
	var decoded []int64
	require.NoError(t, json.Unmarshal([]byte(stored.String), &decoded))
	require.Equal(t, want, decoded)
}
