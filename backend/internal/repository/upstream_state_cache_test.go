package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newUpstreamStateCacheTest(t *testing.T) (*upstreamStateCache, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	mr.SetTime(time.Now())
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return &upstreamStateCache{rdb: client}, mr
}

func stateCacheRecord(id, state string, sequence int64) service.UpstreamStateRecord {
	now := time.Now().UnixMilli()
	expiresAt := now + int64(40*time.Minute/time.Millisecond)
	return service.UpstreamStateRecord{ID: id, State: state, AcquiredAt: now, CheckedAt: now, UpstreamExpiresAt: expiresAt, RotationAt: now + int64(20*time.Minute/time.Millisecond), PurgeAt: expiresAt, Sequence: sequence}
}

func TestUpstreamStateCacheFixedExpiryAndOrdering(t *testing.T) {
	c, mr := newUpstreamStateCacheTest(t)
	ctx := context.Background()
	_, older, err := c.Begin(ctx, "scope")
	require.NoError(t, err)
	_, newer, err := c.Begin(ctx, "scope")
	require.NoError(t, err)
	first := stateCacheRecord("scope", "fresh", newer.Sequence)
	require.NoError(t, c.Save(ctx, first, newer))
	require.NoError(t, c.Save(ctx, stateCacheRecord("scope", "stale", older.Sequence), older))
	r, repeat, err := c.Begin(ctx, "scope")
	require.NoError(t, err)
	require.Equal(t, "fresh", r.State)
	repeated := first
	repeated.Sequence = repeat.Sequence
	repeated.UpstreamExpiresAt += 600000
	require.NoError(t, c.Save(ctx, repeated, repeat))
	r, _, err = c.Begin(ctx, "scope")
	require.NoError(t, err)
	require.Equal(t, first.UpstreamExpiresAt, r.UpstreamExpiresAt, "repeated observations must not renew expiry")
	mr.SetTime(time.UnixMilli(first.PurgeAt + 1))
	r, _, err = c.Begin(ctx, "scope")
	require.NoError(t, err)
	require.Nil(t, r)
	rows, err := c.List(ctx)
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestUpstreamStateCacheClearFencesInflightWrites(t *testing.T) {
	for _, all := range []bool{false, true} {
		t.Run(fmt.Sprint(all), func(t *testing.T) {
			c, _ := newUpstreamStateCacheTest(t)
			ctx := context.Background()
			_, ticket, err := c.Begin(ctx, "a")
			require.NoError(t, err)
			require.NoError(t, c.Save(ctx, stateCacheRecord("a", "old", ticket.Sequence), ticket))
			_, other, err := c.Begin(ctx, "b")
			require.NoError(t, err)
			require.NoError(t, c.Save(ctx, stateCacheRecord("b", "kept", other.Sequence), other))
			id := "a"
			if all {
				id = ""
			}
			require.NoError(t, c.Clear(ctx, id))
			require.NoError(t, c.Save(ctx, stateCacheRecord("a", "late", ticket.Sequence), ticket))
			require.ErrorIs(t, c.Replace(ctx, stateCacheRecord("a", "late-replacement", ticket.Sequence), ticket), service.ErrUpstreamStateReplaceRejected)
			r, next, err := c.Begin(ctx, "a")
			require.NoError(t, err)
			require.Nil(t, r, "cleared state must not be resurrected by an in-flight request")
			r, _, err = c.Begin(ctx, "b")
			require.NoError(t, err)
			if all {
				require.Nil(t, r)
			} else {
				require.Equal(t, "kept", r.State)
			}
			require.NoError(t, c.Save(ctx, stateCacheRecord("a", "new-request", next.Sequence), next))
			r, _, err = c.Begin(ctx, "a")
			require.NoError(t, err)
			require.Equal(t, "new-request", r.State)
		})
	}
}

func TestUpstreamStateCacheSharedInstancesAndBound(t *testing.T) {
	c, mr := newUpstreamStateCacheTest(t)
	ctx := context.Background()
	// Seed at the documented capacity; the production Lua must evict atomically.
	for i := 0; i < 4096; i++ {
		id := fmt.Sprint(i)
		mr.HSet(upstreamStateKeys[0], id, `{"id":"`+id+`"}`)
		_, err := mr.ZAdd(upstreamStateKeys[1], float64(time.Now().Add(time.Minute).UnixMilli()), id)
		require.NoError(t, err)
	}
	_, ticket, err := c.Begin(ctx, "new")
	require.NoError(t, err)
	require.NoError(t, c.Save(ctx, stateCacheRecord("new", "state", ticket.Sequence), ticket))
	other := NewUpstreamStateCache(c.rdb)
	rows, err := other.List(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 4096)
	r, _, err := other.Begin(ctx, "new")
	require.NoError(t, err)
	require.Equal(t, "state", r.State)
	// Redis loss must create a new epoch, not accept an old in-flight ticket.
	mr.FlushAll()
	_, _, err = c.Begin(ctx, "new")
	require.NoError(t, err)
	require.NoError(t, c.Save(ctx, stateCacheRecord("new", "late", ticket.Sequence), ticket))
	r, _, err = c.Begin(ctx, "new")
	require.NoError(t, err)
	require.Nil(t, r)
}

func TestUpstreamStateCacheObservationKeepsStateUntilRotation(t *testing.T) {
	c, mr := newUpstreamStateCacheTest(t)
	ctx := context.Background()
	_, ticket, err := c.Begin(ctx, "scope")
	require.NoError(t, err)
	first := stateCacheRecord("scope", "first", ticket.Sequence)
	require.NoError(t, c.Save(ctx, first, ticket))
	for _, state := range []string{"different", ""} {
		_, ticket, err = c.Begin(ctx, "scope")
		require.NoError(t, err)
		observation := stateCacheRecord("scope", state, ticket.Sequence)
		observation.Validation, observation.ObservedLength, observation.CheckedAt = "mismatch", 5, time.Now().UnixMilli()
		observation.UpstreamExpiresAt += 60000
		require.NoError(t, c.Save(ctx, observation, ticket))
		r, _, err := c.Begin(ctx, "scope")
		require.NoError(t, err)
		require.Equal(t, "first", r.State)
		require.Equal(t, first.UpstreamExpiresAt, r.UpstreamExpiresAt)
		require.Equal(t, "mismatch", r.Validation)
		require.Equal(t, 5, r.ObservedLength)
	}
	mr.SetTime(time.UnixMilli(first.PurgeAt + 1))
	r, ticket, err := c.Begin(ctx, "scope")
	require.NoError(t, err)
	require.Nil(t, r)
	rotated := stateCacheRecord("scope", "rotated", ticket.Sequence)
	rotated.AcquiredAt, rotated.UpstreamExpiresAt, rotated.PurgeAt = first.PurgeAt+2, first.PurgeAt+60000, first.PurgeAt+60000
	require.NoError(t, c.Save(ctx, rotated, ticket))
	r, _, err = c.Begin(ctx, "scope")
	require.NoError(t, err)
	require.Equal(t, "rotated", r.State)
}

func TestUpstreamStateCacheAcceptsFirstValidValueAfterNewerInvalidObservation(t *testing.T) {
	c, _ := newUpstreamStateCacheTest(t)
	ctx := context.Background()
	_, older, err := c.Begin(ctx, "scope")
	require.NoError(t, err)
	_, newer, err := c.Begin(ctx, "scope")
	require.NoError(t, err)
	invalid := stateCacheRecord("scope", "", newer.Sequence)
	invalid.Validation, invalid.ObservedLength, invalid.CheckedAt = "mismatch", 5, time.Now().UnixMilli()
	require.NoError(t, c.Save(ctx, invalid, newer))
	valid := stateCacheRecord("scope", "valid", older.Sequence)
	valid.Validation, valid.ObservedLength, valid.CheckedAt = "normal", 5, time.Now().UnixMilli()
	require.NoError(t, c.Save(ctx, valid, older))
	r, _, err := c.Begin(ctx, "scope")
	require.NoError(t, err)
	require.Equal(t, "valid", r.State)
}

func TestUpstreamStateCacheReplaceRotatesValidStateAtomically(t *testing.T) {
	c, _ := newUpstreamStateCacheTest(t)
	ctx := context.Background()
	_, firstTicket, err := c.Begin(ctx, "scope")
	require.NoError(t, err)
	require.NoError(t, c.Save(ctx, stateCacheRecord("scope", "first", firstTicket.Sequence), firstTicket))

	_, rotateTicket, err := c.Begin(ctx, "scope")
	require.NoError(t, err)
	rotated := stateCacheRecord("scope", "rotated", rotateTicket.Sequence)
	require.NoError(t, c.Replace(ctx, rotated, rotateTicket))

	record, _, err := c.Begin(ctx, "scope")
	require.NoError(t, err)
	require.Equal(t, "rotated", record.State)
	require.Equal(t, rotated.AcquiredAt, record.AcquiredAt)
}
