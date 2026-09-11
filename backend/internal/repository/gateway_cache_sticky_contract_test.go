package repository

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func TestGatewayStickyLookupContract(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	cache := NewGatewayCache(client)
	ctx := context.Background()
	const groupID int64 = 5
	const session = "openai:session"

	id, err := cache.GetSessionAccountID(ctx, groupID, session)
	require.ErrorIs(t, err, service.ErrStickySessionNotFound)
	require.Zero(t, id)

	require.NoError(t, cache.SetSessionAccountID(ctx, groupID, session, 25, time.Minute))
	id, err = cache.GetSessionAccountID(ctx, groupID, session)
	require.NoError(t, err)
	require.Equal(t, int64(25), id)

	_, err = cache.GetSessionAccountID(ctx, groupID+1, session)
	require.ErrorIs(t, err, service.ErrStickySessionNotFound)
	server.FastForward(2 * time.Minute)
	id, err = cache.GetSessionAccountID(ctx, groupID, session)
	require.ErrorIs(t, err, service.ErrStickySessionNotFound)
	require.Zero(t, id)

	require.NoError(t, server.Set(buildSessionKey(groupID, session), "invalid-account-id"))
	_, err = cache.GetSessionAccountID(ctx, groupID, session)
	require.Error(t, err)
	require.NotErrorIs(t, err, service.ErrStickySessionNotFound)

	server.SetError("ERR storage unavailable")
	_, err = cache.GetSessionAccountID(ctx, groupID, session)
	require.ErrorContains(t, err, "storage unavailable")
	require.NotErrorIs(t, err, service.ErrStickySessionNotFound)
}
