package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type managedStateRefreshHTTP struct {
	HTTPUpstream
	do func(*http.Request, string) (*http.Response, error)
}

func (u *managedStateRefreshHTTP) Do(req *http.Request, proxy string, _ int64, _ int) (*http.Response, error) {
	return u.do(req, proxy)
}

type managedStateRefreshStore struct {
	*managedStateTestStore
	beforeReplace func(context.Context, UpstreamStateTicket) error
}

func (s *managedStateRefreshStore) Replace(ctx context.Context, record UpstreamStateRecord, ticket UpstreamStateTicket) error {
	if s.beforeReplace != nil {
		if err := s.beforeReplace(ctx, ticket); err != nil {
			return err
		}
	}
	return s.managedStateTestStore.Replace(ctx, record, ticket)
}

func managedStateRefreshService(t *testing.T) (*OpenAIGatewayService, *managedStateRefreshStore, *Account) {
	t.Helper()
	s, cache, account := managedStateService(t)
	account.Credentials["access_token"] = "test-access-token"
	s.accountRepo = &managedStateAccountRepo{accounts: []Account{*account}}
	store := &managedStateRefreshStore{managedStateTestStore: cache}
	s.settingService.upstreamStateStore = store
	return s, store, account
}

// A real local HTTP stream stalls after headers. Refresh must cancel it before
// touching the cache, even though the server never sends any SSE body bytes.
func TestManagedUpstreamStateRefreshCancelsStreamBeforeCacheWrite(t *testing.T) {
	s, store, account := managedStateRefreshService(t)
	state := testUpstreamStateAt(t, time.Now(), 292)
	cancelled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(openAICodexTurnStateHeader, state)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("flush SSE headers: %v", err)
			return
		}
		<-r.Context().Done()
		close(cancelled)
	}))
	defer server.Close()
	var outgoing *http.Request
	calls := 0
	s.httpUpstream = &managedStateRefreshHTTP{do: func(req *http.Request, proxy string) (*http.Response, error) {
		calls++
		require.Empty(t, proxy)
		require.Empty(t, req.Header.Get(openAICodexTurnStateHeader))
		require.Equal(t, "text/event-stream", req.Header.Get("Accept"))
		require.Equal(t, "identity", req.Header.Get("Accept-Encoding"))
		outgoing = req
		local, err := http.NewRequestWithContext(req.Context(), http.MethodPost, server.URL, req.Body)
		require.NoError(t, err)
		return server.Client().Do(local)
	}}
	store.beforeReplace = func(ctx context.Context, _ UpstreamStateTicket) error {
		require.ErrorIs(t, outgoing.Context().Err(), context.Canceled)
		require.NoError(t, ctx.Err(), "network cancellation must not cancel persistence")
		select {
		case <-cancelled:
		case <-time.After(time.Second):
			t.Fatal("SSE request was still running when persistence began")
		}
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := s.RefreshManagedUpstreamState(ctx, account.ID, "model")
	require.NoError(t, err)
	require.Equal(t, 292, result.Length)
	require.Equal(t, 1, calls)
	require.Len(t, store.records, 1)
}

type managedStateUnreadBody struct {
	read, closed bool
}

func (b *managedStateUnreadBody) Read([]byte) (int, error) { b.read = true; return 0, io.EOF }
func (b *managedStateUnreadBody) Close() error             { b.closed = true; return nil }

func TestManagedUpstreamStateRefreshReportsDistinctFailures(t *testing.T) {
	for _, tc := range []struct {
		name, state, message string
		status               int
		requestError         error
	}{
		{name: "missing", status: 200, message: "HTTP 200 without X-Codex-Turn-State"},
		{name: "invalid length", status: 200, state: "short", message: "5 bytes, expected 292"},
		{name: "upstream", status: 503, message: "HTTP 503: overloaded"},
		{name: "transport", requestError: errors.New("proxyconnect: Bad Request"), message: "(request): proxyconnect: Bad Request"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, store, account := managedStateRefreshService(t)
			body := &managedStateUnreadBody{}
			s.httpUpstream = &managedStateRefreshHTTP{do: func(req *http.Request, _ string) (*http.Response, error) {
				if tc.requestError != nil {
					return nil, tc.requestError
				}
				header := http.Header{}
				header.Set(openAICodexTurnStateHeader, tc.state)
				var stream io.ReadCloser = body
				if tc.status == 503 {
					stream = io.NopCloser(strings.NewReader(`{"error":{"message":"overloaded"}}`))
				}
				return &http.Response{StatusCode: tc.status, Header: header, Body: stream}, nil
			}}
			result, err := s.RefreshManagedUpstreamState(context.Background(), account.ID, "model")
			require.Nil(t, result)
			require.ErrorIs(t, err, ErrUpstreamStateRefreshFailed)
			require.Contains(t, err.Error(), tc.message)
			if tc.status == 200 {
				require.True(t, body.closed)
				require.False(t, body.read, "refresh must not consume a successful response body")
			}
			require.Len(t, store.records, 1)
			for _, record := range store.records {
				require.Empty(t, record.State)
				require.Contains(t, record.LastError, tc.message)
				require.Positive(t, record.LastRefreshAt)
			}
		})
	}
}

func TestManagedUpstreamStateRefreshUsesTicketFromBeforeRequest(t *testing.T) {
	s, store, account := managedStateRefreshService(t)
	s.httpUpstream = &managedStateRefreshHTTP{do: func(_ *http.Request, _ string) (*http.Response, error) {
		// A concurrent clear invalidates the original epoch. Begin must not be
		// called again after the network response to obtain a fresh ticket.
		store.fail = true
		header := http.Header{}
		header.Set(openAICodexTurnStateHeader, testUpstreamStateAt(t, time.Now(), 292))
		return &http.Response{StatusCode: 200, Header: header, Body: &managedStateUnreadBody{}}, nil
	}}
	store.beforeReplace = func(_ context.Context, ticket UpstreamStateTicket) error {
		require.Equal(t, "epoch", ticket.Epoch)
		return ErrUpstreamStateReplaceRejected
	}
	_, err := s.RefreshManagedUpstreamState(context.Background(), account.ID, "model")
	require.ErrorIs(t, err, ErrUpstreamStateReplaceRejected)
	require.Empty(t, store.records, "rejected refresh must not recreate an error record")
}

func TestManagedUpstreamStateRunnerStopsAfterAutoReplaceDisabled(t *testing.T) {
	s, _, account := managedStateRefreshService(t)
	calls := 0
	s.httpUpstream = &managedStateRefreshHTTP{do: func(_ *http.Request, _ string) (*http.Response, error) {
		calls++
		cfg, err := s.settingService.GetUpstreamStateSettings(context.Background())
		require.NoError(t, err)
		cfg.AutoReplaceEnabled = false
		_, err = s.settingService.SetUpstreamStateSettings(context.Background(), cfg)
		require.NoError(t, err)
		return nil, errors.New("request failed")
	}}
	s.runDueManagedUpstreamStates(context.Background())
	require.Equal(t, 1, calls, "remaining queued pairs must not refresh after disabling automation")
	// Disabling automation alone still allows an explicit manual refresh.
	_, err := s.RefreshManagedUpstreamState(context.Background(), account.ID, "model")
	require.ErrorIs(t, err, ErrUpstreamStateRefreshFailed)
	require.Equal(t, 2, calls)
}

func TestManagedUpstreamStateRefreshBackoffIsIndependentOfTraffic(t *testing.T) {
	now := time.Now()
	r := UpstreamStateRecord{State: "valid", UpstreamExpiresAt: now.Add(time.Minute).UnixMilli(), RotationAt: now.Add(-time.Minute).UnixMilli(), CheckedAt: now.UnixMilli()}
	require.True(t, upstreamStateRecordDue(r, true, defaultUpstreamStateSettings(), now), "ordinary traffic must not defer rotation")
	r.LastError = "refresh failed"
	r.LastRefreshAt = now.Add(-4 * time.Minute).UnixMilli()
	require.False(t, upstreamStateRecordDue(r, true, defaultUpstreamStateSettings(), now))
	r.LastRefreshAt = now.Add(-5 * time.Minute).UnixMilli()
	require.True(t, upstreamStateRecordDue(r, true, defaultUpstreamStateSettings(), now))
	r.State = ""
	require.True(t, upstreamStateRecordDue(r, true, defaultUpstreamStateSettings(), now), "missing observations must not starve refresh")
}

func TestManagedUpstreamStateErrorMessageRedactsCredentials(t *testing.T) {
	message := managedUpstreamStateErrorMessage(errors.New("proxy http://user:secret@proxy.test failed; access_token=token-value; state=opaque-value"), "user:secret", "token-value")
	require.NotContains(t, message, "secret")
	require.NotContains(t, message, "token-value")
	require.NotContains(t, message, "opaque-value")
}

func TestManagedUpstreamStateQueueDoesNotStarveUnattemptedPairs(t *testing.T) {
	now := time.Now()
	a := UpstreamStatePair{42, "model-a"}
	b := UpstreamStatePair{42, "model-b"}
	cfg := UpstreamStateSettings{Enabled: true, AutoReplaceEnabled: true, Pairs: []UpstreamStatePair{a, b}}
	failed := UpstreamStateRecord{AccountID: a.AccountID, Model: a.Model, LastRefreshAt: now.Add(-6 * time.Minute).UnixMilli()}
	require.Equal(t, []UpstreamStatePair{b}, dueManagedUpstreamStatePairs(cfg, []UpstreamStateRecord{failed}, now, 1))
}

func TestManagedUpstreamStateRunnerRechecksPairAfterManualSet(t *testing.T) {
	s, _, account := managedStateRefreshService(t)
	ctx := context.Background()
	cfg, err := s.settingService.GetUpstreamStateSettings(ctx)
	require.NoError(t, err)
	cfg.Pairs = []UpstreamStatePair{{account.ID, "model-a"}, {account.ID, "model-b"}}
	_, err = s.settingService.SetUpstreamStateSettings(ctx, cfg)
	require.NoError(t, err)
	calls := 0
	s.httpUpstream = &managedStateRefreshHTTP{do: func(_ *http.Request, _ string) (*http.Response, error) {
		calls++
		_, err := s.SetManagedUpstreamState(ctx, account.ID, "model-b", testUpstreamStateAt(t, time.Now(), 292))
		require.NoError(t, err)
		return nil, errors.New("request failed")
	}}
	s.runDueManagedUpstreamStates(ctx)
	require.Equal(t, 1, calls, "manual replacement while waiting must suppress the queued refresh")
}

func TestManagedUpstreamStateConfiguredScheduling(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	cfg := defaultUpstreamStateSettings()
	cfg.TTLMinutes = 60
	record := buildUpstreamStateObservation(UpstreamStateRecord{}, testUpstreamStateAt(t, now.Add(-45*time.Minute), 292), cfg, now.Add(-45*time.Minute))
	record.LastRefreshAt = record.AcquiredAt
	require.False(t, upstreamStateRecordDue(record, true, cfg, now))
	cfg.RotationLeadMinutes = 20
	require.True(t, upstreamStateRecordDue(record, true, cfg, now), "a timing change must affect saved states")
	cfg.RotationLeadMinutes = 0
	require.False(t, upstreamStateRecordDue(record, true, cfg, now))
	require.True(t, upstreamStateRecordDue(record, true, cfg, time.UnixMilli(record.UpstreamExpiresAt)))

	cfg.RetryIntervalMinutes = 60
	record.LastError = "proxy timeout"
	record.LastRefreshAt = now.UnixMilli()
	require.False(t, upstreamStateRecordDue(record, true, cfg, now.Add(59*time.Minute)), "expiry must not bypass failed retry delay")
	require.True(t, upstreamStateRecordDue(record, true, cfg, now.Add(time.Hour)))
	cfg.RetryIntervalMinutes = 1
	require.True(t, upstreamStateRecordDue(record, true, cfg, now.Add(16*time.Minute)), "a saved interval change applies without reacquiring state")

	cfg.RetryIntervalMinutes = 60
	record.LastError = ""
	record.LastRefreshAt = record.AcquiredAt
	require.True(t, upstreamStateRecordDue(record, true, cfg, time.UnixMilli(record.UpstreamExpiresAt)), "retry delay must not defer healthy expiry")
	cfg.RotationLeadMinutes = 20
	record.AcquiredAt = now.UnixMilli()
	record.LastRefreshAt = now.UnixMilli()
	require.False(t, upstreamStateRecordDue(record, true, cfg, now), "an already-due successful acquisition must not loop")
}

func TestManagedUpstreamStateRefreshUsesBoundedSSEBudget(t *testing.T) {
	s, _, account := managedStateRefreshService(t)
	s.httpUpstream = &managedStateRefreshHTTP{do: func(req *http.Request, _ string) (*http.Response, error) {
		require.Equal(t, HTTPUpstreamProfileOpenAI, HTTPUpstreamProfileFromContext(req.Context()))
		deadline, ok := req.Context().Deadline()
		require.True(t, ok)
		remaining := time.Until(deadline)
		require.Greater(t, remaining, 19*time.Second)
		require.LessOrEqual(t, remaining, upstreamStateRequestTimeout)
		return nil, errors.New("test request failed")
	}}
	_, err := s.RefreshManagedUpstreamState(context.Background(), account.ID, "model")
	require.ErrorIs(t, err, ErrUpstreamStateRefreshFailed)
}

func TestManagedUpstreamStateRotationCycleAppliesToAllTokens(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	cfg := defaultUpstreamStateSettings()
	for _, token := range []string{testUpstreamStateAt(t, now, 292), strings.Repeat("x", 292)} {
		record := buildUpstreamStateObservation(UpstreamStateRecord{}, token, cfg, now)
		record.LastRefreshAt = now.UnixMilli()
		require.Equal(t, now.Add(40*time.Minute).UnixMilli(), upstreamStateRotationAt(record, cfg))
		require.False(t, upstreamStateRecordDue(record, true, cfg, now.Add(39*time.Minute)))
		require.True(t, upstreamStateRecordDue(record, true, cfg, now.Add(40*time.Minute)))
		changed := cfg
		changed.TTLMinutes = 20
		require.Equal(t, now.Add(20*time.Minute).UnixMilli(), upstreamStateRotationAt(record, changed))
		require.True(t, upstreamStateRecordDue(record, true, changed, now.Add(20*time.Minute)))
		record.LastError = "proxy failed"
		record.LastRefreshAt = now.Add(40 * time.Minute).UnixMilli()
		require.False(t, upstreamStateRecordDue(record, true, cfg, now.Add(44*time.Minute)))
		require.True(t, upstreamStateRecordDue(record, true, cfg, now.Add(45*time.Minute)))
	}
	// A token acquired late must still rotate before its estimated upstream expiry.
	old := buildUpstreamStateObservation(UpstreamStateRecord{}, testUpstreamStateAt(t, now.Add(-30*time.Minute), 292), cfg, now)
	require.Equal(t, now.Add(20*time.Minute).UnixMilli(), upstreamStateRotationAt(old, cfg))
	// A successful replacement starts a new cycle from acquisition.
	next := buildUpstreamStateObservation(UpstreamStateRecord{}, testUpstreamStateAt(t, now.Add(40*time.Minute), 292), cfg, now.Add(40*time.Minute))
	require.Equal(t, now.Add(80*time.Minute).UnixMilli(), next.RotationAt)
}
