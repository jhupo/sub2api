package service

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type managedStateTestStore struct {
	UpstreamStateStore
	records map[string]UpstreamStateRecord
	fail    bool
}

func (s *managedStateTestStore) Get(ctx context.Context, id string) (*UpstreamStateRecord, error) {
	record, _, err := s.Begin(ctx, id)
	return record, err
}

func (s *managedStateTestStore) Begin(_ context.Context, id string) (*UpstreamStateRecord, UpstreamStateTicket, error) {
	if s.fail {
		return nil, UpstreamStateTicket{}, errors.New("unavailable")
	}
	ticket := UpstreamStateTicket{Epoch: "epoch", Sequence: 1}
	r, ok := s.records[id]
	if !ok {
		return nil, ticket, nil
	}
	return &r, ticket, nil
}
func (s *managedStateTestStore) Save(_ context.Context, r UpstreamStateRecord, _ UpstreamStateTicket) error {
	s.records[r.ID] = r
	return nil
}
func (s *managedStateTestStore) Replace(_ context.Context, r UpstreamStateRecord, _ UpstreamStateTicket) error {
	s.records[r.ID] = r
	return nil
}
func (s *managedStateTestStore) List(context.Context) ([]UpstreamStateRecord, error) {
	rows := []UpstreamStateRecord{}
	for _, r := range s.records {
		rows = append(rows, r)
	}
	return rows, nil
}
func (s *managedStateTestStore) Clear(_ context.Context, id string) error {
	if id == "" {
		clear(s.records)
	} else {
		delete(s.records, id)
	}
	return nil
}
func (s *managedStateTestStore) TryLock(context.Context, string, time.Duration) (string, bool, error) {
	return "owner", true, nil
}
func (s *managedStateTestStore) Unlock(context.Context, string, string) error { return nil }

func testUpstreamStateAt(t *testing.T, issued time.Time, encodedLength int) string {
	t.Helper()
	require.Zero(t, encodedLength%4)
	raw := make([]byte, encodedLength/4*3-2)
	raw[0] = 0x80
	binary.BigEndian.PutUint64(raw[1:9], uint64(issued.Unix()))
	for i := 9; i < len(raw); i++ {
		raw[i] = byte(i)
	}
	state := base64.URLEncoding.EncodeToString(raw)
	require.Len(t, state, encodedLength)
	return state
}

func TestUpstreamStateTokenTimestampAndLengthValidation(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	validation, issuedAt, expiresAt := parseUpstreamStateToken(testUpstreamStateAt(t, now, 292), 292, now)
	require.Equal(t, "normal", validation)
	require.Equal(t, now.UnixMilli(), issuedAt)
	require.Equal(t, now.Add(time.Hour).UnixMilli(), expiresAt)

	validation, _, _ = parseUpstreamStateToken(testUpstreamStateAt(t, now, 308), 292, now)
	require.Equal(t, "extended", validation)
	validation, _, _ = parseUpstreamStateToken(testUpstreamStateAt(t, now.Add(-2*time.Hour), 292), 292, now)
	require.Equal(t, "expired", validation)

	validation, issuedAt, expiresAt = parseUpstreamStateToken(strings.Repeat("x", 292), 292, now)
	require.Equal(t, "normal", validation)
	require.Zero(t, issuedAt)
	require.Zero(t, expiresAt)
	for _, invalid := range []string{"\r\n", "\x00", "中"} {
		state := strings.Repeat("x", 100) + invalid + strings.Repeat("x", 192-len(invalid))
		require.Len(t, state, 292)
		validation, _, _ = parseUpstreamStateToken(state, 292, now)
		require.Equal(t, "invalid", validation, "header-unsafe content must never enter the cache")
	}
}

func TestUpstreamStateRotationUsesFernetExpiryThenConfiguredFallback(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	cfg := UpstreamStateSettings{TTLMinutes: 40, ExpectedLength: 292, WebshareCountryMode: webshareCountryModeRandom}
	fernet := buildUpstreamStateObservation(UpstreamStateRecord{}, testUpstreamStateAt(t, now, 292), cfg, now)
	require.Equal(t, "fernet", fernet.ExpirySource)
	require.Equal(t, now.Add(time.Hour).UnixMilli(), fernet.UpstreamExpiresAt)
	require.Equal(t, now.Add(50*time.Minute).UnixMilli(), fernet.RotationAt)

	fallback := buildUpstreamStateObservation(UpstreamStateRecord{}, strings.Repeat("x", 292), cfg, now)
	require.Equal(t, "fallback", fallback.ExpirySource)
	require.Equal(t, now.Add(40*time.Minute).UnixMilli(), fallback.UpstreamExpiresAt)
	require.Equal(t, now.Add(30*time.Minute).UnixMilli(), fallback.RotationAt)
}

func TestDueManagedUpstreamStatePairsRequiresAutomaticReplacementAndPairOptIn(t *testing.T) {
	now := time.Now()
	enabledPair := UpstreamStatePair{AccountID: 42, Model: "enabled-model"}
	notEnabledPair := UpstreamStatePair{AccountID: 42, Model: "other-model"}
	dueRecord := UpstreamStateRecord{
		AccountID:         enabledPair.AccountID,
		Model:             enabledPair.Model,
		State:             strings.Repeat("s", 292),
		CheckedAt:         now.Add(-time.Hour).UnixMilli(),
		UpstreamExpiresAt: now.Add(5 * time.Minute).UnixMilli(),
		RotationAt:        now.Add(-time.Minute).UnixMilli(),
	}
	otherDueRecord := dueRecord
	otherDueRecord.Model = notEnabledPair.Model
	cfg := UpstreamStateSettings{
		Enabled:            true,
		AutoReplaceEnabled: true,
		Pairs:              []UpstreamStatePair{enabledPair},
	}
	cfg.StateRevision = "state-revision"
	cfg.PairRevisions = map[string]string{upstreamStatePairKey(enabledPair): "pair-revision"}
	dueRecord.Revision, dueRecord.PairRevision = cfg.StateRevision, cfg.pairRevision(enabledPair.AccountID, enabledPair.Model)
	otherDueRecord.Revision, otherDueRecord.PairRevision = cfg.StateRevision, "other-pair-revision"

	require.Equal(t, []UpstreamStatePair{enabledPair}, dueManagedUpstreamStatePairs(cfg, []UpstreamStateRecord{dueRecord, otherDueRecord}, now, 4))

	cfg.AutoReplaceEnabled = false
	require.Empty(t, dueManagedUpstreamStatePairs(cfg, []UpstreamStateRecord{dueRecord}, now, 4))
	cfg.AutoReplaceEnabled = true
	cfg.Enabled = false
	require.Empty(t, dueManagedUpstreamStatePairs(cfg, []UpstreamStateRecord{dueRecord}, now, 4))
}

type managedStateSettingsRepo struct {
	SettingRepository
	raw string
}

func (r *managedStateSettingsRepo) GetValue(_ context.Context, key string) (string, error) {
	if key != upstreamStateSettingKey || r.raw == "" {
		return "", ErrSettingNotFound
	}
	return r.raw, nil
}
func (r *managedStateSettingsRepo) Set(_ context.Context, _ string, value string) error {
	r.raw = value
	return nil
}

func managedStateService(t *testing.T) (*OpenAIGatewayService, *managedStateTestStore, *Account) {
	t.Helper()
	store := &managedStateTestStore{records: map[string]UpstreamStateRecord{}}
	repo := &managedStateSettingsRepo{}
	settings := NewSettingService(repo, &config.Config{})
	settings.upstreamStateStore = store
	_, err := settings.SetUpstreamStateSettings(context.Background(), UpstreamStateSettings{Enabled: true, AutoReplaceEnabled: true, TTLMinutes: 40, ExpectedLength: 292, WebshareCountryMode: webshareCountryModeRandom, Pairs: []UpstreamStatePair{{42, "model"}, {42, "model-a"}, {42, "model-b"}, {42, "final-model"}, {43, "model-a"}}})
	require.NoError(t, err)
	account := &Account{ID: 42, Name: "test-account", Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: map[string]any{"chatgpt_account_id": "credential-a"}}
	return &OpenAIGatewayService{settingService: settings, cfg: &config.Config{}}, store, account
}

func managedStateScope(t *testing.T, s *OpenAIGatewayService, account *Account, keyID int64, session, model string, headers http.Header) *upstreamStateScope {
	t.Helper()
	c, _ := newTurnStateTestContext(t, keyID, session)
	i := &codexAttemptIdentity{accountID: account.ID, clientSession: session, scope: newCodexAccountIdentityScope(account, keyID)}
	stageCodexAttemptIdentity(c, i)
	return s.newUpstreamStateScope(context.Background(), c, account, headers, model, "http")
}

func TestUpstreamStateScopeIsAccountAndModelOnly(t *testing.T) {
	s, _, account := managedStateService(t)
	h := http.Header{"User-Agent": []string{"client/1"}}
	base := managedStateScope(t, s, account, 7, "session-a", "model-a", h)
	require.NotEmpty(t, base.record.ID)
	for _, same := range []*upstreamStateScope{
		managedStateScope(t, s, account, 7, "session-a", "model-a", h),
		managedStateScope(t, s, account, 8, "session-a", "model-a", h),
		managedStateScope(t, s, account, 7, "session-b", "model-a", h),
		managedStateScope(t, s, account, 7, "", "model-a", h),
		managedStateScope(t, s, account, 0, "session-a", "model-a", h),
		managedStateScope(t, s, account, 7, "session-a", "model-a", http.Header{"User-Agent": []string{"client/2"}}),
	} {
		require.Equal(t, base.record.ID, same.record.ID)
	}
	account.Credentials["access_token"] = "refreshed-token"
	require.Equal(t, base.record.ID, managedStateScope(t, s, account, 7, "session-a", "model-a", h).record.ID)
	require.NotEqual(t, base.record.ID, managedStateScope(t, s, account, 7, "session-a", "model-b", h).record.ID)
	other := *account
	other.ID = 43
	require.NotEqual(t, base.record.ID, managedStateScope(t, s, &other, 7, "session-a", "model-a", h).record.ID)
	account.Credentials["chatgpt_account_id"] = "credential-b"
	require.NotEqual(t, base.record.ID, managedStateScope(t, s, account, 7, "session-a", "model-a", h).record.ID)
}

func seedManagedState(t *testing.T, s *OpenAIGatewayService, scope *upstreamStateScope, state string) {
	t.Helper()
	_, ticket, err := s.settingService.upstreamStateStore.Begin(context.Background(), scope.record.ID)
	require.NoError(t, err)
	_, err = s.replaceManagedUpstreamState(context.Background(), scope, state, ticket)
	require.NoError(t, err)
}

func TestUpstreamStateInjectionAndDisabledBehavior(t *testing.T) {
	s, store, account := managedStateService(t)
	ctx := context.Background()
	scope := managedStateScope(t, s, account, 7, "session", "model", http.Header{})
	h := http.Header{}
	h.Set(openAICodexTurnStateHeader, "untrusted-client-echo")
	require.False(t, scope.inject(ctx, h))
	require.Empty(t, h.Get(openAICodexTurnStateHeader))
	state := testUpstreamStateAt(t, time.Now(), 292)
	seedManagedState(t, s, scope, state)
	require.Len(t, store.records, 1)
	require.True(t, scope.inject(ctx, h))
	require.Equal(t, state, h.Get(openAICodexTurnStateHeader))
	store.fail = true
	require.False(t, scope.inject(ctx, h))
	require.Empty(t, h.Get(openAICodexTurnStateHeader))
	_, err := s.settingService.SetUpstreamStateSettings(ctx, UpstreamStateSettings{AutoReplaceEnabled: true, TTLMinutes: 40, ExpectedLength: 292, WebshareCountryMode: webshareCountryModeRandom, Revision: scope.config.Revision})
	require.NoError(t, err)
	// A retained scope in the WS prewarm pool cannot act after disabling.
	require.False(t, scope.inject(ctx, h))
	disabled := managedStateScope(t, s, account, 7, "session", "model", h)
	require.Nil(t, disabled)
	h.Set(openAICodexTurnStateHeader, "normal-client-echo")
	disabled.inject(ctx, h)
	require.Equal(t, "normal-client-echo", h.Get(openAICodexTurnStateHeader))
}

type managedStateHTTP struct {
	HTTPUpstream
	header   http.Header
	response *http.Response
}

func (u *managedStateHTTP) Do(r *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	u.header = r.Header.Clone()
	return u.response, nil
}

func TestUpstreamStateHTTPTransportUsesFinalScopeAndKeepsStream(t *testing.T) {
	s, store, account := managedStateService(t)
	c, _ := newTurnStateTestContext(t, 7, "client-session")
	body := []byte(`{"model":"final-model","stream":true}`)
	req, err := s.buildUpstreamRequest(context.Background(), c, account, body, "token", true, "", false)
	require.NoError(t, err)
	bodyStream := io.NopCloser(strings.NewReader("data: first-event\n\n"))
	h := http.Header{}
	state := testUpstreamStateAt(t, time.Now(), 292)
	h.Set(openAICodexTurnStateHeader, state)
	u := &managedStateHTTP{response: &http.Response{StatusCode: 200, Header: h, Body: bodyStream}}
	s.httpUpstream = u
	resp, err := s.doOpenAIUpstream(req, "", account)
	require.NoError(t, err)
	received, readErr := io.ReadAll(resp.Body)
	require.NoError(t, readErr)
	require.Equal(t, "data: first-event\n\n", string(received), "collection must not consume SSE")
	require.Empty(t, store.records, "ordinary responses must not populate managed state")
	scope, ok := req.Context().Value(upstreamStateContextKey{}).(*upstreamStateScope)
	require.True(t, ok)
	seedManagedState(t, s, scope, state)
	saved := store.records[scope.record.ID]
	// Simulate a legacy bridge writing a state after building the request.
	req.Header.Set(openAICodexTurnStateHeader, "wrong-bridge-state")
	for _, responseState := range []string{"", "short", testUpstreamStateAt(t, time.Now().Add(time.Second), 292)} {
		u.response.Header.Set(openAICodexTurnStateHeader, responseState)
		_, err = s.doOpenAIUpstream(req, "", account)
		require.NoError(t, err)
		require.Equal(t, state, u.header.Get(openAICodexTurnStateHeader))
		require.Equal(t, saved, store.records[scope.record.ID], "ordinary response must not alter state, expiry or status")
	}
	c.Request.URL.Path = "/v1/alpha/search"
	other, _ := http.NewRequest(http.MethodPost, "https://example.test/v1/alpha/search", nil)
	other = s.attachUpstreamStateScope(other, c, account, body)
	require.Nil(t, other.Context().Value(upstreamStateContextKey{}))
}

func TestUpstreamStateAdminRedactsAndRevisions(t *testing.T) {
	s, store, account := managedStateService(t)
	ctx := context.Background()
	account.Credentials["model_mapping"] = map[string]any{"alias": "model"}
	s.settingService.upstreamStateAccounts = &managedStateAccountRepo{accounts: []Account{*account}}
	scope := managedStateScope(t, s, account, 7, "secret-session", "model", http.Header{})
	h := http.Header{}
	h.Set(openAICodexTurnStateHeader, testUpstreamStateAt(t, time.Now(), 292))
	seedManagedState(t, s, scope, h.Get(openAICodexTurnStateHeader))
	rows, err := s.settingService.UpstreamStateMatrix(ctx)
	require.NoError(t, err)
	var modelRow *UpstreamStateMatrixRow
	for i := range rows {
		if rows[i].Model == "model" {
			modelRow = &rows[i]
			break
		}
	}
	require.NotNil(t, modelRow)
	require.Equal(t, 1, modelRow.Cached)
	require.Equal(t, 292, modelRow.StateLength)
	raw, err := json.Marshal(rows)
	require.NoError(t, err)
	require.NotContains(t, string(raw), h.Get(openAICodexTurnStateHeader))
	require.NotContains(t, string(raw), "secret-session")
	require.NotContains(t, string(raw), "credential-a")
	// A failed refresh has its own error. It cannot turn a reusable cached
	// state's main status or displayed length into missing/zero.
	record := store.records[scope.record.ID]
	record.Validation, record.ObservedLength, record.LastError = "refresh_error", 0, "HTTP 200 without state"
	store.records[record.ID] = record
	rows, err = s.settingService.UpstreamStateMatrix(ctx)
	require.NoError(t, err)
	modelRow = modelRowForTest(t, rows, "model")
	require.Equal(t, "normal", modelRow.Validation)
	require.Equal(t, 292, modelRow.StateLength)
	require.Equal(t, record.LastError, modelRow.LastError)
	old := store.records[scope.record.ID]
	cfg, err := s.settingService.GetUpstreamStateSettings(ctx)
	require.NoError(t, err)
	previousStateRevision := cfg.StateRevision
	cfg.TTLMinutes = 20
	cfg, err = s.settingService.SetUpstreamStateSettings(ctx, cfg)
	require.NoError(t, err)
	require.Equal(t, previousStateRevision, cfg.StateRevision, "fallback timing does not invalidate Fernet-backed state")
	rows, err = s.settingService.UpstreamStateMatrix(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, modelRowForTest(t, rows, "model").Cached)

	cfg.ExpectedLength = 300
	cfg, err = s.settingService.SetUpstreamStateSettings(ctx, cfg)
	require.NoError(t, err)
	require.NotEqual(t, previousStateRevision, cfg.StateRevision)
	store.records[old.ID] = old // An old instance cannot make an old revision visible.
	rows, err = s.settingService.UpstreamStateMatrix(ctx)
	require.NoError(t, err)
	require.Zero(t, modelRowForTest(t, rows, "model").Cached)
	for _, cfg := range []UpstreamStateSettings{
		{TTLMinutes: 0, ExpectedLength: 292, WebshareCountryMode: webshareCountryModeRandom},
		{TTLMinutes: 61, ExpectedLength: 292, WebshareCountryMode: webshareCountryModeRandom},
		{TTLMinutes: 40, ExpectedLength: 0, WebshareCountryMode: webshareCountryModeRandom},
		{TTLMinutes: 40, ExpectedLength: 8193, WebshareCountryMode: webshareCountryModeRandom},
		{TTLMinutes: 40, ExpectedLength: 292, WebshareCountryMode: webshareCountryModeSpecified},
		{TTLMinutes: 40, ExpectedLength: 292, WebshareCountryMode: webshareCountryModeSpecified, WebshareCountries: []string{"us"}},
	} {
		require.Error(t, cfg.Validate())
	}
	require.NoError(t, (UpstreamStateSettings{TTLMinutes: 40, ExpectedLength: 292, WebshareCountryMode: webshareCountryModeSpecified, WebshareCountries: []string{"US", "JP"}}).Validate())
}

func modelRowForTest(t *testing.T, rows []UpstreamStateMatrixRow, model string) *UpstreamStateMatrixRow {
	t.Helper()
	for i := range rows {
		if rows[i].Model == model {
			return &rows[i]
		}
	}
	t.Fatalf("model row %q not found", model)
	return nil
}

func TestUpstreamStateWSHandshakeIsolation(t *testing.T) {
	s, _, account := managedStateService(t)
	c, _ := newTurnStateTestContext(t, 7, "session")
	headers, resolution, err := s.buildOpenAIWSHeaders(context.Background(), c, account, "token", OpenAIWSProtocolDecision{}, false, "untrusted", "", "", "model-a", "")
	require.NoError(t, err)
	require.NotNil(t, resolution.upstreamState)
	first := openAIWSAcquireRequest{Account: account, Headers: headers, upstreamState: resolution.upstreamState}
	second := first
	second.upstreamState = managedStateScope(t, s, account, 7, "session", "model-b", headers)
	require.NotEqual(t, first.handshakeCompatibility(headers), second.handshakeCompatibility(headers))
	resolution.upstreamState.inject(context.Background(), headers)
	require.Empty(t, headers.Get(openAICodexTurnStateHeader))
	// A completed handshake is captured once by dialConn, not when acquiring a
	// lease of the same connection. TTL comes from that first observation.
}

type managedStateDialer struct {
	calls         int
	header        http.Header
	responseState string
}

func (d *managedStateDialer) Dial(_ context.Context, _ string, h http.Header, _ string) (openAIWSClientConn, int, http.Header, error) {
	d.calls++
	d.header = h.Clone()
	response := http.Header{}
	response.Set(openAICodexTurnStateHeader, d.responseState)
	return &openAIWSFakeConn{}, http.StatusSwitchingProtocols, response, nil
}
func TestUpstreamStateWSHandshakeDoesNotCapture(t *testing.T) {
	s, store, account := managedStateService(t)
	scope := managedStateScope(t, s, account, 7, "session", "model", http.Header{})
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 1
	pool := newOpenAIWSConnPool(cfg)
	defer pool.Close()
	dialer := &managedStateDialer{responseState: testUpstreamStateAt(t, time.Now(), 292)}
	pool.setClientDialerForTest(dialer)
	req := openAIWSAcquireRequest{Account: account, WSURL: "wss://example.test/responses", Headers: http.Header{}, upstreamState: scope}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	first, err := pool.Acquire(ctx, req)
	require.NoError(t, err)
	require.False(t, first.Reused())
	first.Release()
	require.Empty(t, store.records, "new WS handshake must not populate managed state")
	second, err := pool.Acquire(ctx, req)
	require.NoError(t, err)
	require.True(t, second.Reused())
	second.Release()
	require.Empty(t, store.records)
	require.Equal(t, 1, dialer.calls)
}

type managedStateAccountRepo struct {
	AccountRepository
	accounts []Account
}

func (r *managedStateAccountRepo) ListByPlatform(context.Context, string) ([]Account, error) {
	return r.accounts, nil
}
func (r *managedStateAccountRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	for _, a := range r.accounts {
		if a.ID == id {
			return &a, nil
		}
	}
	return nil, errors.New("account not found")
}

func TestUpstreamStatePairsDefaultOffAndMatrixWithoutTraffic(t *testing.T) {
	s, store, account := managedStateService(t)
	account.Credentials["model_mapping"] = map[string]any{"alias": "model"}
	s.settingService.upstreamStateAccounts = &managedStateAccountRepo{accounts: []Account{*account, {ID: 43, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}}}
	ctx := context.Background()
	rows, err := s.settingService.UpstreamStateMatrix(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, rows)
	for _, row := range rows {
		require.Equal(t, int64(42), row.AccountID)
		require.Equal(t, "waiting", row.Validation)
		require.Zero(t, row.Cached)
		require.NotEqual(t, "alias", row.Model)
	}
	require.Nil(t, managedStateScope(t, s, account, 7, "session", "not-enabled", http.Header{}))
	cfg, err := s.settingService.GetUpstreamStateSettings(ctx)
	require.NoError(t, err)
	cfg.Pairs = nil
	cfg, err = s.settingService.SetUpstreamStateSettings(ctx, cfg)
	require.NoError(t, err)
	require.Nil(t, managedStateScope(t, s, account, 7, "session", "model", http.Header{}))
	cfg, err = s.settingService.SetUpstreamStatePair(ctx, UpstreamStatePair{42, "model"}, true, cfg.Revision)
	require.NoError(t, err)
	scope := managedStateScope(t, s, account, 7, "session", "model", http.Header{})
	require.NotNil(t, scope)
	_, ticket, err := store.Begin(ctx, scope.record.ID)
	require.NoError(t, err)
	_, err = s.replaceManagedUpstreamState(ctx, scope, "short", ticket)
	require.Error(t, err)
	rows, err = s.settingService.UpstreamStateMatrix(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "waiting", rows[0].Validation)
	require.Zero(t, rows[0].ObservedLength)
	require.Zero(t, rows[0].Cached)
	seedManagedState(t, s, scope, testUpstreamStateAt(t, time.Now(), 292))
	rows, err = s.settingService.UpstreamStateMatrix(ctx)
	require.NoError(t, err)
	require.Equal(t, "normal", rows[0].Validation)
	require.Equal(t, 1, rows[0].Cached)
	require.Greater(t, rows[0].RotationAt, time.Now().UnixMilli())
	_, err = s.settingService.SetUpstreamStatePair(ctx, UpstreamStatePair{42, "model"}, false, cfg.Revision)
	require.NoError(t, err)
	require.False(t, scope.inject(ctx, http.Header{}))
	_, err = s.settingService.SetUpstreamStateSettings(ctx, cfg)
	require.ErrorIs(t, err, ErrUpstreamStateConflict, "a stale settings page must not re-enable a disabled pair")
}

func TestUpstreamStateExpiredCacheNotInjected(t *testing.T) {
	s, store, account := managedStateService(t)
	account.Credentials["model_mapping"] = map[string]any{"alias": "model"}
	s.settingService.upstreamStateAccounts = &managedStateAccountRepo{accounts: []Account{*account}}
	scope := managedStateScope(t, s, account, 7, "session", "model", http.Header{})
	r := scope.record
	r.State, r.UpstreamExpiresAt, r.PurgeAt = testUpstreamStateAt(t, time.Now().Add(-2*time.Hour), 292), time.Now().Add(-time.Second).UnixMilli(), time.Now().Add(time.Minute).UnixMilli()
	store.records[r.ID] = r
	h := http.Header{}
	scope.inject(context.Background(), h)
	require.Empty(t, h.Get(openAICodexTurnStateHeader))
	rows, err := s.settingService.UpstreamStateMatrix(context.Background())
	require.NoError(t, err)
	for _, row := range rows {
		if row.Model == "model" {
			require.Zero(t, row.Cached)
			return
		}
	}
	t.Fatal("model row not found")
}

func TestUpstreamStatePairTogglePreservesOtherPairCache(t *testing.T) {
	s, store, account := managedStateService(t)
	s.settingService.upstreamStateAccounts = &managedStateAccountRepo{accounts: []Account{*account}}
	ctx := context.Background()
	scopeA := managedStateScope(t, s, account, 7, "session", "model-a", http.Header{})
	scopeB := managedStateScope(t, s, account, 7, "session", "model-b", http.Header{})
	stateA := testUpstreamStateAt(t, time.Now(), 292)
	stateB := testUpstreamStateAt(t, time.Now().Add(time.Second), 292)
	for scope, state := range map[*upstreamStateScope]string{scopeA: stateA, scopeB: stateB} {
		seedManagedState(t, s, scope, state)
	}
	require.Len(t, store.records, 2)
	cfg, err := s.settingService.GetUpstreamStateSettings(ctx)
	require.NoError(t, err)
	previousStateRevision := cfg.StateRevision
	cfg, err = s.settingService.SetUpstreamStatePair(ctx, UpstreamStatePair{AccountID: account.ID, Model: "model-a"}, false, cfg.Revision)
	require.NoError(t, err)
	require.Equal(t, previousStateRevision, cfg.StateRevision)
	require.NotEqual(t, scopeA.config.Revision, cfg.Revision)
	require.Len(t, store.records, 1)

	headers := http.Header{}
	require.True(t, scopeB.inject(ctx, headers), "an existing scope for another pair remains valid")
	require.Equal(t, stateB, headers.Get(openAICodexTurnStateHeader))
	require.False(t, scopeA.inject(ctx, http.Header{}))

	cfg, err = s.settingService.SetUpstreamStatePair(ctx, UpstreamStatePair{AccountID: account.ID, Model: "model-a"}, true, cfg.Revision)
	require.NoError(t, err)
	// A late response from another instance may still write the old generation
	// after re-enable. It must not be injected, displayed, scheduled or used to
	// select an old WS pool.
	store.records[scopeA.record.ID] = buildUpstreamStateObservation(scopeA.record, stateA, scopeA.config, time.Now())
	reenabledScope := managedStateScope(t, s, account, 7, "session", "model-a", http.Header{})
	require.NotEqual(t, scopeA.record.ID, reenabledScope.record.ID)
	require.NotEqual(t, scopeA.poolKey(), reenabledScope.poolKey())
	_, err = s.replaceManagedUpstreamState(ctx, scopeA, stateA, UpstreamStateTicket{})
	require.ErrorIs(t, err, ErrUpstreamStateReplaceRejected)
	headers = http.Header{}
	require.False(t, reenabledScope.inject(ctx, headers))
	require.Empty(t, headers.Get(openAICodexTurnStateHeader))
	require.Len(t, store.records, 2, "the unrelated pair remains cached alongside an inaccessible late write")
	rows, err := s.settingService.UpstreamStateMatrix(ctx)
	require.NoError(t, err)
	require.Zero(t, modelRowForTest(t, rows, "model-a").Cached)
	records, err := store.List(ctx)
	require.NoError(t, err)
	require.Contains(t, dueManagedUpstreamStatePairs(cfg, records, time.Now(), 4), UpstreamStatePair{AccountID: account.ID, Model: "model-a"})
}
