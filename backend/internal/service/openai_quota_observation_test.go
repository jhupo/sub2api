package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type codexPublicationTestRepo struct {
	AccountRepository
	calls     chan map[string]any
	first     chan struct{}
	release   chan struct{}
	count     atomic.Int64
	failFirst bool
}

type quotaJoinedContext struct {
	context.Context
	joined chan struct{}
	once   sync.Once
}

func (c *quotaJoinedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.joined) })
	return c.Context.Done()
}

func TestCodexQuotaQueryCollapsesConcurrentRefreshAndIsolatesCancellation(t *testing.T) {
	account := &Account{ID: 100, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive,
		Credentials: map[string]any{"chatgpt_account_id": "test-account"}}
	repo := &stubQuotaAccountRepo{accounts: map[int64]*Account{100: account}}
	tokens := &stubQuotaTokenCache{tokens: map[string]string{OpenAITokenCacheKey(account): "test-token"}}
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/backend-api/wham/usage" {
			_, _ = w.Write([]byte(`{"credits":[]}`))
			return
		}
		if calls.Add(1) == 1 {
			close(started)
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		_, _ = w.Write([]byte(`{"rate_limit":{"primary_window":{"used_percent":20,"limit_window_seconds":18000}}}`))
	}))
	defer srv.Close()
	defer close(release)
	quota := NewOpenAIQuotaService(repo, nil, NewOpenAITokenProvider(repo, tokens, nil), newQuotaRedirectingFactory(srv))
	firstCtx, cancel := context.WithCancel(context.Background())
	firstResult := make(chan error, 1)
	go func() { _, err := quota.QueryUsage(firstCtx, account.ID); firstResult <- err }()
	<-started
	cancel()
	require.ErrorIs(t, <-firstResult, context.Canceled)
	results := make(chan error, 32)
	for i := 0; i < 32; i++ {
		ctx := &quotaJoinedContext{Context: context.Background(), joined: make(chan struct{})}
		go func() { _, err := quota.QueryUsage(ctx, account.ID); results <- err }()
		<-ctx.joined
	}
	// Allow one response through without closing the channel twice in cleanup.
	release <- struct{}{}
	for i := 0; i < 32; i++ {
		require.NoError(t, <-results)
	}
	require.EqualValues(t, 1, calls.Load())
}

func (r *codexPublicationTestRepo) UpdateExtra(ctx context.Context, _ int64, updates map[string]any) error {
	n := r.count.Add(1)
	if n == 1 {
		close(r.first)
		select {
		case <-r.release:
		case <-ctx.Done():
			return ctx.Err()
		}
		if r.failFirst {
			return errors.New("transient storage failure")
		}
	}
	r.calls <- updates
	return nil
}

func TestCodexQuotaPublicationKeepsTailAndRetries(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "coalesces", true: "retries"}[failure], func(t *testing.T) {
			repo := &codexPublicationTestRepo{calls: make(chan map[string]any, 4), first: make(chan struct{}), release: make(chan struct{}), failFirst: failure}
			svc := &OpenAIGatewayService{accountRepo: repo}
			base := time.Now().UTC()
			publish := func(at time.Time, used float64) {
				window := 300
				svc.updateCodexUsageSnapshot(context.Background(), 1, &OpenAICodexUsageSnapshot{UpdatedAt: at.Format(time.RFC3339Nano), PrimaryUsedPercent: &used, PrimaryWindowMinutes: &window})
			}
			publish(base, 90)
			<-repo.first
			if !failure {
				publish(base.Add(time.Second), 5)
				publish(base.Add(-time.Second), 80)
			}
			close(repo.release)
			if !failure {
				select {
				case <-repo.calls:
				case <-time.After(3 * time.Second):
					t.Fatal("missing initial publication")
				}
			}
			select {
			case tail := <-repo.calls:
				if failure {
					require.Equal(t, float64(90), tail["codex_5h_used_percent"])
				} else {
					require.Equal(t, float64(5), tail["codex_5h_used_percent"])
				}
			case <-time.After(3 * time.Second):
				t.Fatal("missing final quota snapshot")
			}
			require.Eventually(t, func() bool {
				svc.codexUsageMu.Lock()
				defer svc.codexUsageMu.Unlock()
				return len(svc.codexUsagePending) == 0
			}, 3*time.Second, 10*time.Millisecond)
		})
	}
}

func TestCodexQuotaUsageObservationTimeSurvivesDelayedPublication(t *testing.T) {
	observed := time.Date(2026, 9, 8, 1, 0, 0, 0, time.UTC)
	usage := &OpenAIQuotaUsage{observedAt: observed, RateLimit: &OpenAIRateLimit{PrimaryWindow: &OpenAIRateLimitWindow{UsedPercent: 5, LimitWindowSeconds: 18000, ResetAt: observed.Add(2 * time.Hour).Unix()}}}
	updates := buildCodexQuotaUsageUpdates(usage, observed.Add(time.Hour))
	require.Equal(t, observed.UnixMicro(), updates["codex_usage_observed_at_us"])
	require.Equal(t, observed.Add(2*time.Hour).Format(time.RFC3339), updates["codex_5h_reset_at"])
	var display UsageInfo
	applyExtraToUsage(&display, updates, observed.Add(time.Hour))
	require.Equal(t, observed, *display.UpdatedAt)
}

func TestCodexQuotaPublicationMergesPartialWindowsInObservationOrder(t *testing.T) {
	old := map[string]any{"codex_usage_observed_at_us": int64(1), "codex_5h_used_percent": 10.0, "codex_7d_used_percent": 20.0}
	latest := map[string]any{"codex_usage_observed_at_us": int64(2), "codex_5h_used_percent": 5.0}
	for _, updates := range []map[string]any{mergeCodexQuotaObservations(old, latest), mergeCodexQuotaObservations(latest, old)} {
		require.Equal(t, int64(2), updates["codex_usage_observed_at_us"])
		require.Equal(t, 5.0, updates["codex_5h_used_percent"])
		require.Equal(t, 20.0, updates["codex_7d_used_percent"])
	}
	require.Equal(t, 10.0, old["codex_5h_used_percent"], "an in-flight publication must remain immutable")
}

func TestCodexQuotaRefreshUsesGETAndPreservesStaleTimestampOnFailure(t *testing.T) {
	account := &Account{ID: 100, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Credentials: map[string]any{"chatgpt_account_id": "test-account"}}
	repo := &stubQuotaAccountRepo{accounts: map[int64]*Account{100: account}}
	tokenCache := &stubQuotaTokenCache{tokens: map[string]string{OpenAITokenCacheKey(account): "test-token"}}
	var calls, posts atomic.Int64
	var fail atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet {
			posts.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		if fail.Load() {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		if r.URL.Path == "/backend-api/wham/usage" {
			_, _ = w.Write([]byte(`{"rate_limit":{"primary_window":{"used_percent":20,"limit_window_seconds":18000,"reset_after_seconds":3600}}}`))
		} else {
			_, _ = w.Write([]byte(`{"credits":[]}`))
		}
	}))
	defer srv.Close()
	quota := NewOpenAIQuotaService(repo, nil, NewOpenAITokenProvider(repo, tokenCache, nil), newQuotaRedirectingFactory(srv))
	svc := &AccountUsageService{openAIQuotaService: quota}
	usage, err := svc.getOpenAIUsage(context.Background(), account, true)
	require.NoError(t, err)
	require.Empty(t, usage.ErrorCode)
	require.Equal(t, 20.0, usage.FiveHour.Utilization)
	require.NotNil(t, usage.UpdatedAt)
	observed := *usage.UpdatedAt
	require.Empty(t, account.Extra, "dashboard refresh must not mutate a shared account snapshot")
	account.Extra = map[string]any{"codex_usage_updated_at": observed.Format(time.RFC3339Nano), "codex_5h_used_percent": 20.0}
	fail.Store(true)
	stale, err := svc.getOpenAIUsage(context.Background(), account, true)
	require.NoError(t, err)
	require.Equal(t, "quota_refresh_failed", stale.ErrorCode)
	require.Equal(t, observed, *stale.UpdatedAt)
	require.Equal(t, 20.0, stale.FiveHour.Utilization)
	require.Zero(t, posts.Load())
	require.GreaterOrEqual(t, calls.Load(), int64(3))
}
