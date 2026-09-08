package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestCodexAdaptiveAdmissionRechecksConfiguredLimitAndQuota(t *testing.T) {
	repo := schedulerTestOpenAIAccountRepo{accounts: []Account{{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 20}}}
	svc := &OpenAIGatewayService{accountRepo: repo}
	ctx := codexAdaptivePolicyContext()
	policy, err := svc.resolveCodexAccountSlotAdmission(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 20, policy.MaxConcurrency)
	repo.accounts[0].Concurrency = 2
	policy, err = svc.resolveCodexAccountSlotAdmission(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, 2, policy.MaxConcurrency, "queued requests must not reuse the earlier cap")
	repo.accounts[0].Extra = map[string]any{
		"auto_pause_5h_threshold": 1.0,
		"codex_5h_used_percent":   100.0,
		"codex_5h_reset_at":       time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
	}
	_, err = svc.resolveCodexAccountSlotAdmission(ctx, 1)
	require.ErrorIs(t, err, ErrNoAvailableAccounts, "the adaptive minimum is not permission to bypass an exhausted quota")
	repo.accounts[0].Extra = nil
	repo.accounts[0].Schedulable = false
	_, err = svc.resolveCodexAccountSlotAdmission(ctx, 1)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
}

type adaptiveSchedulerConcurrencyCache struct{ schedulerTestConcurrencyCache }

func (c adaptiveSchedulerConcurrencyCache) AcquireAdaptiveAccountSlot(ctx context.Context, id int64, policy AccountSlotAdmission, requestID string) (bool, error) {
	return c.AcquireAccountSlot(ctx, id, policy.MaxConcurrency, requestID)
}

func TestCodexAdaptiveQueueSpillsToSpareWithoutMigratingAffinity(t *testing.T) {
	for _, advanced := range []string{"false", "true"} {
		for _, spareAvailable := range []bool{false, true} {
			t.Run(advanced+map[bool]string{false: "/full", true: "/spare"}[spareAvailable], func(t *testing.T) {
				group := int64(7)
				accounts := []Account{
					{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, GroupIDs: []int64{group}},
					{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 1, Priority: 1, GroupIDs: []int64{group}},
				}
				cache := newCodexMigrationTestCache("queue-session", 1)
				cfg := &config.Config{}
				cfg.Gateway.Scheduling.StickySessionWaitTimeout = 45 * time.Second
				cfg.Gateway.Scheduling.StickySessionMaxWaiting = 10
				svc := &OpenAIGatewayService{
					accountRepo: schedulerTestOpenAIAccountRepo{accounts: accounts}, cache: cache, cfg: cfg,
					rateLimitService:   newOpenAIAdvancedSchedulerRateLimitService(advanced),
					concurrencyService: NewConcurrencyService(adaptiveSchedulerConcurrencyCache{schedulerTestConcurrencyCache{acquireResults: map[int64]bool{1: false, 2: spareAvailable}}}),
					openaiAccountStats: newOpenAIAccountRuntimeStats(),
				}
				ctx := codexAdaptivePolicyContext()
				codexAdaptiveRequestFromContext(ctx).sessionHash = "queue-session"
				ctx = context.WithValue(ctx, accountSlotAdmissionKey{}, accountSlotAdmissionResolver(svc.resolveCodexAccountSlotAdmission))
				started := time.Now()
				selection, _, err := svc.SelectAccountWithScheduler(ctx, &group, "", "queue-session", "gpt-5.6-sol", nil, OpenAIUpstreamTransportAny, false)
				require.NoError(t, err)
				require.NotNil(t, selection)
				require.Less(t, time.Since(started), 2*time.Second)
				if spareAvailable {
					require.Equal(t, int64(2), selection.Account.ID)
					require.True(t, selection.Acquired)
					selection.ReleaseFunc()
				} else {
					require.NotNil(t, selection.WaitPlan)
					require.LessOrEqual(t, selection.WaitPlan.Timeout, 3*time.Second)
				}
				require.Equal(t, int64(1), cache.sessionBindings["openai:queue-session"])
			})
		}
	}
}

func TestAccountSlotAdmissionUnlimitedHasNoTrackedRelease(t *testing.T) {
	service := NewConcurrencyService(nil)
	result, err := service.AcquireAccountSlot(context.Background(), 1, 0)
	require.NoError(t, err)
	require.True(t, result.Acquired)
	result.ReleaseFunc()
	ctx := context.WithValue(context.Background(), accountSlotAdmissionKey{}, accountSlotAdmissionResolver(func(context.Context, int64) (*AccountSlotAdmission, error) {
		return nil, ErrNoAvailableAccounts
	}))
	_, err = service.AcquireAccountSlot(ctx, 1, 0)
	require.ErrorIs(t, err, ErrNoAvailableAccounts, "zero configured concurrency cannot bypass eligibility")
}

func TestCodexAdaptiveQueueTimeoutIsAlwaysBoundedAndPositive(t *testing.T) {
	for _, timeout := range []time.Duration{-time.Second, 0, 45 * time.Second} {
		require.Equal(t, 3*time.Second, boundCodexAdaptiveQueueTimeout(timeout))
	}
	require.Equal(t, time.Second, boundCodexAdaptiveQueueTimeout(time.Second))
}

func TestCodexAdaptiveAdmissionUsesActualCompactMapping(t *testing.T) {
	repo := schedulerTestOpenAIAccountRepo{accounts: []Account{{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true, Concurrency: 20,
		Credentials: map[string]any{"compact_model_mapping": map[string]any{"gpt-5.5": "compact-provider-model"}},
	}}}
	svc := &OpenAIGatewayService{accountRepo: repo}
	ctx := codexAdaptivePolicyContext()
	state := codexAdaptiveRequestFromContext(ctx)
	state.model, state.legacyCompact = "gpt-5.5", true
	policy, err := svc.resolveCodexAccountSlotAdmission(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, "compact-provider-model", policy.PressureModel)
	SetCodexAdaptiveTurnModel(ctx, "gpt-5.5")
	policy, err = svc.resolveCodexAccountSlotAdmission(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, "gpt-5.5", policy.PressureModel, "native compaction uses the normal Responses model")
}

func TestCodexAdaptiveAdmissionRechecksGroupAfterQueueing(t *testing.T) {
	group := int64(7)
	repo := schedulerTestOpenAIAccountRepo{accounts: []Account{{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth,
		Status: StatusActive, Schedulable: true, Concurrency: 20, GroupIDs: []int64{group},
	}}}
	svc := &OpenAIGatewayService{accountRepo: repo}
	ctx := codexAdaptivePolicyContext()
	codexAdaptiveRequestFromContext(ctx).admissionRequest = &OpenAIAccountScheduleRequest{GroupID: &group, RequiredTransport: OpenAIUpstreamTransportAny}
	_, err := svc.resolveCodexAccountSlotAdmission(ctx, 1)
	require.NoError(t, err)
	repo.accounts[0].GroupIDs = []int64{8}
	_, err = svc.resolveCodexAccountSlotAdmission(ctx, 1)
	require.ErrorIs(t, err, ErrNoAvailableAccounts)
}

func TestCodexQuotaProbeRespectsAccountAdmissionBeforeSending(t *testing.T) {
	for _, condition := range []string{"busy", "expired", "auth-cooldown", "disabled"} {
		t.Run(condition, func(t *testing.T) {
			account := Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Concurrency: 2}
			switch condition {
			case "expired":
				past := time.Now().Add(-time.Minute)
				account.AutoPauseOnExpired, account.ExpiresAt = true, &past
			case "auth-cooldown":
				future := time.Now().Add(time.Minute)
				account.TempUnschedulableUntil, account.TempUnschedulableReason = &future, "authentication failed"
			case "disabled":
				account.Schedulable = false
			}
			coordinator := &CodexQuotaOverdraftCoordinator{
				accountRepo:        schedulerTestOpenAIAccountRepo{accounts: []Account{account}},
				concurrencyService: NewConcurrencyService(schedulerTestConcurrencyCache{acquireResults: map[int64]bool{1: false}}),
			}
			result := coordinator.runProbeAttempt(context.Background(), &account, "gpt-5.5")
			require.Equal(t, "inconclusive", result.Status)
			if condition == "busy" {
				require.Equal(t, "account_busy", result.ReasonCode)
			} else {
				require.Equal(t, "account_unavailable", result.ReasonCode)
			}
		})
	}
}
