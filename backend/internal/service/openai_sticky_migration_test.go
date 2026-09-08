package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type codexMigrationTestCache struct {
	*stubGatewayCache
	mu         sync.Mutex
	migrations map[string]OpenAIStickyMigration
	commitErr  error
	readErr    error
}

func newCodexMigrationTestCache(session string, accountID int64) *codexMigrationTestCache {
	return &codexMigrationTestCache{
		stubGatewayCache: &stubGatewayCache{sessionBindings: map[string]int64{"openai:" + session: accountID}},
		migrations:       make(map[string]OpenAIStickyMigration),
	}
}

func (c *codexMigrationTestCache) SetOpenAIStickySessionIfAbsent(_ context.Context, _ int64, key string, id int64, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.sessionBindings[key] == 0 {
		c.sessionBindings[key] = id
	}
	return nil
}
func (c *codexMigrationTestCache) GetOpenAIStickyMigration(_ context.Context, _ int64, key string) (*OpenAIStickyMigration, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.readErr != nil {
		return nil, c.readErr
	}
	if m, ok := c.migrations[key]; ok {
		return &m, nil
	}
	return nil, nil
}
func (c *codexMigrationTestCache) ClaimOpenAIStickyMigration(_ context.Context, _ int64, key string, proposal OpenAIStickyMigration, _ time.Duration) (*OpenAIStickyMigration, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if bound := c.sessionBindings[key]; bound > 0 && bound != proposal.SourceID {
		return &OpenAIStickyMigration{SourceID: proposal.SourceID, TargetID: bound, Model: proposal.Model}, nil
	}
	if existing, ok := c.migrations[key]; ok {
		return &existing, nil
	}
	if proposal.SourceID == proposal.TargetID {
		proposal.Version = ""
		return &proposal, nil
	}
	c.migrations[key] = proposal
	return &proposal, nil
}

func TestCodexStickySelectionFailsClosedOnStoreFailure(t *testing.T) {
	cache := newCodexMigrationTestCache("session", 1)
	cache.readErr = errors.New("redis unavailable")
	svc := &OpenAIGatewayService{cache: cache}
	ctx := codexAdaptivePolicyContext()
	selection, _, err := svc.SelectAccountWithScheduler(ctx, nil, "", "session", "gpt-5.6-sol", nil, OpenAIUpstreamTransportAny, false)
	require.ErrorIs(t, err, cache.readErr)
	require.Nil(t, selection)
}
func (c *codexMigrationTestCache) RefreshOpenAIStickyMigration(_ context.Context, _ int64, key, version string, _ time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.migrations[key].Version == version, nil
}
func (c *codexMigrationTestCache) AbortOpenAIStickyMigration(_ context.Context, _ int64, key, version string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.migrations[key].Version == version {
		delete(c.migrations, key)
	}
	return nil
}
func (c *codexMigrationTestCache) CommitOpenAIStickyMigration(_ context.Context, _ int64, key string, m OpenAIStickyMigration, _ time.Duration) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.commitErr != nil {
		return false, c.commitErr
	}
	if c.migrations[key] != m || c.sessionBindings[key] != m.SourceID {
		return false, nil
	}
	c.sessionBindings[key] = m.TargetID
	delete(c.migrations, key)
	return true, nil
}

func TestCodexStickyMigrationSuccessAndPublicationFailure(t *testing.T) {
	for _, sourceSuccess := range []bool{false, true} {
		t.Run(map[bool]string{false: "target", true: "source"}[sourceSuccess], func(t *testing.T) {
			cache := newCodexMigrationTestCache("session", 1)
			svc := &OpenAIGatewayService{cache: cache}
			ctx := codexAdaptivePolicyContext()
			state := codexAdaptiveRequestFromContext(ctx)
			state.sessionHash = "session"
			svc.ApplyCodexAdaptiveFailoverPolicy(ctx, openAIFirstOutputTestAccount(1), "gpt-5.6-sol", codexAdaptiveCapacityShedError())
			_, err := svc.coordinateCodexStickySelection(ctx, OpenAIAccountScheduleRequest{SessionHash: "session"}, &AccountSelectionResult{Account: openAIFirstOutputTestAccount(2)})
			require.NoError(t, err)
			defer FinishCodexAdaptiveSchedulingRequest(ctx)
			if sourceSuccess {
				require.NoError(t, svc.CommitCodexAdaptiveStickyOnSuccess(ctx, nil, openAIFirstOutputTestAccount(1), false))
				require.Equal(t, int64(1), cache.sessionBindings["openai:session"])
				require.NotEmpty(t, cache.migrations, "a source completion must not abort another request's migration")
				return
			}
			cache.commitErr = errors.New("redis unavailable")
			require.Error(t, svc.CommitCodexAdaptiveStickyOnSuccess(ctx, nil, openAIFirstOutputTestAccount(2), false))
			require.True(t, codexAdaptiveStickyMigrationPending(ctx))
			require.Equal(t, int64(1), cache.sessionBindings["openai:session"])
			cache.commitErr = nil
			require.NoError(t, svc.CommitCodexAdaptiveStickyOnSuccess(ctx, nil, openAIFirstOutputTestAccount(2), false))
			require.Equal(t, int64(2), cache.sessionBindings["openai:session"])
		})
	}
}

func TestCodexStickyMigrationOtherModelDoesNotInvalidateLease(t *testing.T) {
	cache := newCodexMigrationTestCache("session", 1)
	other := OpenAIStickyMigration{SourceID: 1, TargetID: 2, Version: "other-model", Model: "gpt-5.6-terra"}
	cache.migrations["openai:session"] = other
	svc := &OpenAIGatewayService{cache: cache}
	ctx := codexAdaptivePolicyContext()
	codexAdaptiveRequestFromContext(ctx).sessionHash = "session"
	svc.ApplyCodexAdaptiveFailoverPolicy(ctx, openAIFirstOutputTestAccount(1), "gpt-5.6-sol", codexAdaptiveCapacityShedError())
	selection, err := svc.coordinateCodexStickySelection(ctx, OpenAIAccountScheduleRequest{SessionHash: "session", RequestedModel: "gpt-5.6-sol"}, &AccountSelectionResult{Account: openAIFirstOutputTestAccount(3)})
	require.NoError(t, err)
	require.Equal(t, int64(3), selection.Account.ID)
	require.NoError(t, svc.CommitCodexAdaptiveStickyOnSuccess(ctx, nil, selection.Account, false))
	require.Equal(t, other, cache.migrations["openai:session"])
	require.Equal(t, int64(1), cache.sessionBindings["openai:session"])
}
