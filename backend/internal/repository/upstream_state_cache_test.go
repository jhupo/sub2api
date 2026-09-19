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
	mr.SetTime(time.UnixMilli(r.PurgeAt + 1))
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
	retained, err := c.Get(ctx, "scope")
	require.NoError(t, err)
	purgedAt := retained.PurgeAt + 1
	mr.SetTime(time.UnixMilli(purgedAt))
	r, ticket, err := c.Begin(ctx, "scope")
	require.NoError(t, err)
	require.Nil(t, r)
	rotated := stateCacheRecord("scope", "rotated", ticket.Sequence)
	rotated.AcquiredAt, rotated.UpstreamExpiresAt, rotated.PurgeAt = purgedAt+1, purgedAt+60000, purgedAt+60000
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

func TestUpstreamStateCacheTrafficPreservesRefreshFailure(t *testing.T) {
	for _, initialState := range []string{"valid", ""} {
		t.Run("initial="+initialState, func(t *testing.T) {
			c, _ := newUpstreamStateCacheTest(t)
			ctx := context.Background()
			_, ticket, err := c.Begin(ctx, "scope")
			require.NoError(t, err)
			initial := stateCacheRecord("scope", initialState, ticket.Sequence)
			require.NoError(t, c.Save(ctx, initial, ticket))
			_, refresh, err := c.Begin(ctx, "scope")
			require.NoError(t, err)
			_, traffic, err := c.Begin(ctx, "scope")
			require.NoError(t, err)
			observation := stateCacheRecord("scope", "", traffic.Sequence)
			observation.Validation = "missing"
			require.NoError(t, c.Save(ctx, observation, traffic))
			// Refresh started earlier but fails later. Its result must survive
			// both newer in-flight observations and future ordinary responses.
			failure := stateCacheRecord("scope", "", refresh.Sequence)
			failure.LastError, failure.Validation = "HTTP 200 without state", "refresh_error"
			failure.LastRefreshAt = time.Now().UnixMilli()
			require.NoError(t, c.Save(ctx, failure, refresh))
			for _, observedState := range []string{"", "new-passive-value"} {
				_, next, err := c.Begin(ctx, "scope")
				require.NoError(t, err)
				observation := stateCacheRecord("scope", observedState, next.Sequence)
				observation.CheckedAt = failure.LastRefreshAt + 1000
				require.NoError(t, c.Save(ctx, observation, next))
				record, _, err := c.Begin(ctx, "scope")
				require.NoError(t, err)
				require.Equal(t, failure.LastError, record.LastError)
				require.Equal(t, failure.LastRefreshAt, record.LastRefreshAt)
				if initialState != "" {
					require.Equal(t, initialState, record.State)
					require.Equal(t, initial.UpstreamExpiresAt, record.UpstreamExpiresAt)
				}
			}
			_, replacement, err := c.Begin(ctx, "scope")
			require.NoError(t, err)
			rotated := stateCacheRecord("scope", "new-manual-value", replacement.Sequence)
			rotated.LastRefreshAt = failure.LastRefreshAt + 2000
			require.NoError(t, c.Replace(ctx, rotated, replacement))
			record, _, err := c.Begin(ctx, "scope")
			require.NoError(t, err)
			require.Empty(t, record.LastError)
			require.Equal(t, rotated.LastRefreshAt, record.LastRefreshAt)
		})
	}
}

func TestUpstreamStateCacheReplaceEnforcesCapacity(t *testing.T) {
	c, mr := newUpstreamStateCacheTest(t)
	ctx := context.Background()
	for i := 0; i < 4096; i++ {
		id := fmt.Sprint(i)
		mr.HSet(upstreamStateKeys[0], id, `{"id":"`+id+`"}`)
		_, err := mr.ZAdd(upstreamStateKeys[1], float64(time.Now().Add(time.Minute).UnixMilli()), id)
		require.NoError(t, err)
	}
	_, ticket, err := c.Begin(ctx, "replacement")
	require.NoError(t, err)
	require.NoError(t, c.Replace(ctx, stateCacheRecord("replacement", "value", ticket.Sequence), ticket))
	rows, err := c.List(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 4096)
	record, _, err := c.Begin(ctx, "replacement")
	require.NoError(t, err)
	require.Equal(t, "value", record.State)
}

func TestUpstreamStateCacheGetDoesNotModifyRefreshMetadata(t *testing.T) {
	c, mr := newUpstreamStateCacheTest(t)
	ctx := context.Background()
	record, err := c.Get(ctx, "scope")
	require.NoError(t, err)
	require.Nil(t, record)
	_, ticket, err := c.Begin(ctx, "scope")
	require.NoError(t, err)
	saved := stateCacheRecord("scope", "state", ticket.Sequence)
	saved.LastRefreshAt, saved.Validation = saved.CheckedAt, "normal"
	require.NoError(t, c.Replace(ctx, saved, ticket))
	sequence, err := c.rdb.Get(ctx, upstreamStateKeys[3]).Result()
	require.NoError(t, err)
	for range 3 {
		record, err = c.Get(ctx, "scope")
		require.NoError(t, err)
		require.Equal(t, saved, *record)
	}
	current, err := c.rdb.Get(ctx, upstreamStateKeys[3]).Result()
	require.NoError(t, err)
	require.Equal(t, sequence, current)
	require.Equal(t, 86460*time.Second, mr.TTL(upstreamStateKeys[0]))
}

func TestUpstreamStateCacheFailureSurvivesTokenExpiry(t *testing.T) {
	for _, initialState := range []string{"valid", ""} {
		t.Run("initial="+initialState, func(t *testing.T) {
			c, mr := newUpstreamStateCacheTest(t)
			ctx := context.Background()
			_, ticket, err := c.Begin(ctx, "scope")
			require.NoError(t, err)
			initial := stateCacheRecord("scope", initialState, ticket.Sequence)
			initial.UpstreamExpiresAt = time.Now().Add(time.Minute).UnixMilli()
			initial.PurgeAt = initial.UpstreamExpiresAt
			require.NoError(t, c.Replace(ctx, initial, ticket))
			_, ticket, err = c.Begin(ctx, "scope")
			require.NoError(t, err)
			failure := stateCacheRecord("scope", "", ticket.Sequence)
			failure.LastError, failure.Validation = "proxy timeout", "refresh_error"
			failure.LastRefreshAt = time.Now().UnixMilli()
			failure.PurgeAt = time.Now().Add(24 * time.Hour).UnixMilli()
			require.NoError(t, c.Save(ctx, failure, ticket))
			// More than both token expiry and the maximum configurable retry delay.
			mr.SetTime(time.UnixMilli(failure.LastRefreshAt).Add(61 * time.Minute))
			mr.FastForward(61 * time.Minute)
			rows, err := c.List(ctx)
			require.NoError(t, err)
			require.Len(t, rows, 1)
			require.Equal(t, failure.LastRefreshAt, rows[0].LastRefreshAt)
			require.Equal(t, failure.LastError, rows[0].LastError)
			require.Equal(t, failure.PurgeAt, rows[0].PurgeAt)
			if initialState != "" {
				require.Equal(t, initial.UpstreamExpiresAt, rows[0].UpstreamExpiresAt, "metadata retention must not renew token validity")
			}
		})
	}
}
