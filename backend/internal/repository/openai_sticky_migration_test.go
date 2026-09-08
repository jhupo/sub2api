package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestOpenAIStickyMigrationConcurrentSingleWinner(t *testing.T) {
	client := newOpenAIAdmissionTestRedis(t)
	cache := NewGatewayCache(client).(*gatewayCache)
	ctx := context.Background()
	session := fmt.Sprintf("openai:migration:%d", time.Now().UnixNano())
	require.NoError(t, cache.SetSessionAccountID(ctx, 17, session, 1, time.Minute))
	results := make(chan *service.OpenAIStickyMigration, 64)
	errors := make(chan error, 64)
	var wg sync.WaitGroup
	for n := 0; n < 64; n++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			m, err := cache.ClaimOpenAIStickyMigration(ctx, 17, session, service.OpenAIStickyMigration{SourceID: 1, TargetID: int64(n + 2), Version: fmt.Sprint(n)}, time.Minute)
			results <- m
			errors <- err
		}(n)
	}
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	var winner *service.OpenAIStickyMigration
	for result := range results {
		if winner == nil {
			winner = result
		}
		require.Equal(t, *winner, *result)
	}
	bound, err := cache.GetSessionAccountID(ctx, 17, session)
	require.NoError(t, err)
	require.Equal(t, int64(1), bound)
	committed, err := cache.CommitOpenAIStickyMigration(ctx, 17, session, *winner, time.Minute)
	require.NoError(t, err)
	require.True(t, committed)
	// Late source requests and normal load spillover cannot revert the winner.
	require.NoError(t, cache.SetOpenAIStickySessionIfAbsent(ctx, 17, session, 1, time.Minute))
	late, err := cache.ClaimOpenAIStickyMigration(ctx, 17, session, service.OpenAIStickyMigration{SourceID: 1, TargetID: 99, Version: "late"}, time.Minute)
	require.NoError(t, err)
	require.Equal(t, winner.TargetID, late.TargetID)
	require.Empty(t, late.Version)
	committed, err = cache.CommitOpenAIStickyMigration(ctx, 17, session, *winner, time.Minute)
	require.NoError(t, err)
	require.False(t, committed)
	bound, err = cache.GetSessionAccountID(ctx, 17, session)
	require.NoError(t, err)
	require.Equal(t, winner.TargetID, bound)
	require.NoError(t, cache.DeleteSessionAccountID(ctx, 17, session))
}

func TestOpenAIStickyMigrationLeaseCASAndIsolation(t *testing.T) {
	cache := NewGatewayCache(newOpenAIAdmissionTestRedis(t)).(*gatewayCache)
	ctx := context.Background()
	session := fmt.Sprintf("openai:lease:%d", time.Now().UnixNano())
	a := service.OpenAIStickyMigration{SourceID: 1, TargetID: 2, Version: "a"}
	b := service.OpenAIStickyMigration{SourceID: 1, TargetID: 3, Version: "b"}
	_, err := cache.ClaimOpenAIStickyMigration(ctx, 1, session, a, time.Minute)
	require.NoError(t, err)
	require.NoError(t, cache.AbortOpenAIStickyMigration(ctx, 1, session, "not-owner"))
	owned, err := cache.RefreshOpenAIStickyMigration(ctx, 1, session, a.Version, time.Minute)
	require.NoError(t, err)
	require.True(t, owned)
	require.NoError(t, cache.AbortOpenAIStickyMigration(ctx, 1, session, a.Version))
	_, err = cache.ClaimOpenAIStickyMigration(ctx, 1, session, b, time.Minute)
	require.NoError(t, err)
	committed, err := cache.CommitOpenAIStickyMigration(ctx, 1, session, a, time.Minute)
	require.NoError(t, err)
	require.False(t, committed)
	owned, err = cache.RefreshOpenAIStickyMigration(ctx, 1, session, a.Version, time.Minute)
	require.NoError(t, err)
	require.False(t, owned)
	other, err := cache.ClaimOpenAIStickyMigration(ctx, 2, session, a, time.Minute)
	require.NoError(t, err)
	require.Equal(t, a, *other)
	committed, err = cache.CommitOpenAIStickyMigration(ctx, 1, session, b, time.Minute)
	require.NoError(t, err)
	require.True(t, committed)
	require.NoError(t, cache.AbortOpenAIStickyMigration(ctx, 2, session, a.Version))
	require.NoError(t, cache.DeleteSessionAccountID(ctx, 1, session))
}
