package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAPIKeyRepositoryGetByIDLoadsFreshGroupPermissions(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	ctx := context.Background()
	user := mustCreateAPIKeyRepoUser(t, ctx, client, "ws-turn-authorization@test.com")
	group, err := client.Group.Create().SetName("exclusive-ws-group").SetPlatform(service.PlatformOpenAI).
		SetStatus(service.StatusActive).SetRateMultiplier(1).SetIsExclusive(true).Save(ctx)
	require.NoError(t, err)
	_, err = client.User.UpdateOneID(user.ID).AddAllowedGroupIDs(group.ID).Save(ctx)
	require.NoError(t, err)
	key := &service.APIKey{UserID: user.ID, Key: "sk-ws-turn-test", Name: "WS", GroupID: &group.ID, Status: service.StatusActive}
	require.NoError(t, repo.Create(ctx, key))
	got, err := repo.GetByID(ctx, key.ID)
	require.NoError(t, err)
	require.NotNil(t, got.User)
	require.Equal(t, []int64{group.ID}, got.User.AllowedGroups)
	require.True(t, got.User.CanBindGroup(group.ID, got.Group.IsExclusive))
	_, err = client.User.UpdateOneID(user.ID).RemoveAllowedGroupIDs(group.ID).Save(ctx)
	require.NoError(t, err)
	got, err = repo.GetByID(ctx, key.ID)
	require.NoError(t, err)
	require.False(t, got.User.CanBindGroup(group.ID, got.Group.IsExclusive))
}
