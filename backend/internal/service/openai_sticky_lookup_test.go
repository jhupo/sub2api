package service

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type stickyLookupTestCache struct {
	*schedulerTestGatewayCache
	readErrors map[string]error
	reads      []string
}

func (c *stickyLookupTestCache) GetSessionAccountID(ctx context.Context, groupID int64, key string) (int64, error) {
	c.reads = append(c.reads, key)
	if err := c.readErrors[key]; err != nil {
		return 0, err
	}
	return c.schedulerTestGatewayCache.GetSessionAccountID(ctx, groupID, key)
}

func TestOpenAIStickyLookupDistinguishesAbsenceFromFailure(t *testing.T) {
	storeErr := errors.New("redis unavailable")
	corruptErr := errors.New("invalid account ID")
	untypedMiss := errors.New(ErrStickySessionNotFound.Error())
	for _, tc := range []struct {
		name       string
		fallback   bool
		legacyHash string
		bindings   map[string]int64
		readErrors map[string]error
		wantID     int64
		wantErr    error
		wantReads  []string
	}{
		{name: "new session", wantReads: []string{"openai:session"}},
		{name: "no legacy hash", fallback: true, wantReads: []string{"openai:session"}},
		{name: "both bindings absent", fallback: true, legacyHash: "legacy", wantReads: []string{"openai:session", "openai:legacy"}},
		{name: "wrapped miss", readErrors: map[string]error{"openai:session": fmt.Errorf("lookup: %w", ErrStickySessionNotFound)}, wantReads: []string{"openai:session"}},
		{name: "current binding wins", fallback: true, legacyHash: "legacy", bindings: map[string]int64{"openai:session": 1, "openai:legacy": 2}, wantID: 1, wantReads: []string{"openai:session"}},
		{name: "existing legacy binding", fallback: true, legacyHash: "legacy", bindings: map[string]int64{"openai:legacy": 2}, wantID: 2, wantReads: []string{"openai:session", "openai:legacy"}},
		{name: "primary failure cannot use stale fallback", fallback: true, legacyHash: "legacy", bindings: map[string]int64{"openai:legacy": 2}, readErrors: map[string]error{"openai:session": storeErr}, wantErr: storeErr, wantReads: []string{"openai:session"}},
		{name: "fallback failure is not a miss", fallback: true, legacyHash: "legacy", readErrors: map[string]error{"openai:legacy": storeErr}, wantErr: storeErr, wantReads: []string{"openai:session", "openai:legacy"}},
		{name: "corrupt value", readErrors: map[string]error{"openai:session": corruptErr}, wantErr: corruptErr, wantReads: []string{"openai:session"}},
		{name: "same error text is not the sentinel", readErrors: map[string]error{"openai:session": untypedMiss}, wantErr: untypedMiss, wantReads: []string{"openai:session"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cache := &stickyLookupTestCache{
				schedulerTestGatewayCache: &schedulerTestGatewayCache{sessionBindings: tc.bindings},
				readErrors:                tc.readErrors,
			}
			cfg := &config.Config{}
			cfg.Gateway.OpenAIWS.SessionHashReadOldFallback = tc.fallback
			svc := &OpenAIGatewayService{cache: cache, cfg: cfg}
			ctx := withOpenAILegacySessionHash(context.Background(), tc.legacyHash)
			id, err := svc.getStickySessionAccountID(ctx, nil, "session")
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, tc.wantID, id)
			require.Equal(t, tc.wantReads, cache.reads)
		})
	}
}

func TestCodexStickySelectionAdmitsUnboundSession(t *testing.T) {
	for _, advanced := range []string{"false", "true"} {
		t.Run("advanced="+advanced, func(t *testing.T) {
			cache := newCodexMigrationTestCache("session", 1)
			delete(cache.sessionBindings, "openai:session")
			svc := &OpenAIGatewayService{
				cache: cache, cfg: &config.Config{},
				accountRepo: schedulerTestOpenAIAccountRepo{accounts: []Account{
					{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 10},
				}},
				concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{}),
				rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService(advanced),
			}
			for _, phase := range []string{"new", "existing", "expired"} {
				t.Run(phase, func(t *testing.T) {
					if phase == "expired" {
						delete(cache.sessionBindings, "openai:session")
					}
					ctx := codexAdaptivePolicyContext()
					codexAdaptiveRequestFromContext(ctx).sessionHash = "session"
					defer FinishCodexAdaptiveSchedulingRequest(ctx)
					before := cache.sessionReads
					selection, _, err := svc.SelectAccountWithScheduler(ctx, nil, "", "session", "gpt-5.6-sol", nil, OpenAIUpstreamTransportAny, false)
					require.NoError(t, err)
					require.NotNil(t, selection)
					require.True(t, selection.Acquired)
					t.Cleanup(selection.ReleaseFunc)
					require.Equal(t, int64(1), selection.Account.ID)
					require.Equal(t, int64(1), cache.sessionBindings["openai:session"])
					require.Equal(t, before+1, cache.sessionReads, "all selection branches must share the published lookup snapshot")
					require.Empty(t, cache.migrations)
				})
			}
		})
	}
}

func TestCodexStickySelectionRejectsBindingStoreFailure(t *testing.T) {
	for _, storeErr := range []error{errors.New("redis unavailable"), context.Canceled, context.DeadlineExceeded} {
		t.Run(storeErr.Error(), func(t *testing.T) {
			cache := newCodexMigrationTestCache("session", 1)
			cache.sessionReadErr = storeErr
			svc := &OpenAIGatewayService{cache: cache}
			selection, _, err := svc.SelectAccountWithScheduler(codexAdaptivePolicyContext(), nil, "", "session", "gpt-5.6-sol", nil, OpenAIUpstreamTransportAny, false)
			require.ErrorIs(t, err, storeErr)
			require.Nil(t, selection)
			require.Equal(t, int64(1), cache.sessionBindings["openai:session"])
			require.Empty(t, cache.migrations)
		})
	}
}

func TestCodexStickyLookupKeepsMigrationOwnerWithoutBinding(t *testing.T) {
	cache := newCodexMigrationTestCache("session", 1)
	delete(cache.sessionBindings, "openai:session")
	migration := OpenAIStickyMigration{SourceID: 1, TargetID: 2, Version: "owner", Model: "gpt-5.6-sol"}
	cache.migrations["openai:session"] = migration
	svc := &OpenAIGatewayService{cache: cache}
	ctx := codexAdaptivePolicyContext()
	id, err := svc.getStickySessionAccountID(ctx, nil, "session")
	require.NoError(t, err)
	require.Equal(t, migration.TargetID, id)
	require.True(t, codexAdaptiveStickyMigrationPending(ctx))
	require.Equal(t, migration, codexAdaptiveRequestFromContext(ctx).migration)
	require.Zero(t, cache.sessionReads, "a pending migration owns routing even after the normal binding expires")
}
