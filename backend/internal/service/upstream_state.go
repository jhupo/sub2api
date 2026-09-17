package service

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

const upstreamStateSettingKey = "upstream_state_management"

const (
	upstreamStateLifetime         = time.Hour
	upstreamStateRotationLead     = 10 * time.Minute
	upstreamStateObservationTTL   = 5 * time.Minute
	upstreamStateFutureClockSkew  = 5 * time.Minute
	upstreamStateValidationNormal = "normal"
	upstreamStateValidationLong   = "extended"
	webshareCountryModeRandom     = "random"
	webshareCountryModeSpecified  = "specified"
)

var (
	ErrUpstreamStateConflict        = errors.New("upstream state settings changed; reload before saving")
	ErrUpstreamStateReplaceRejected = errors.New("upstream state cache rejected replacement")
)

// Settings are revisioned: a policy change never makes an older cache visible.
type UpstreamStateSettings struct {
	Enabled             bool                `json:"enabled"`
	AutoReplaceEnabled  bool                `json:"auto_replace_enabled"`
	TTLMinutes          int                 `json:"ttl_minutes"`
	ExpectedLength      int                 `json:"expected_length"`
	WebshareEnabled     bool                `json:"webshare_enabled"`
	WebshareAPIKey      string              `json:"webshare_api_key,omitempty"`
	WebshareCountryMode string              `json:"webshare_country_mode"`
	WebshareCountries   []string            `json:"webshare_countries"`
	Revision            string              `json:"revision"`
	StateRevision       string              `json:"state_revision"`
	Pairs               []UpstreamStatePair `json:"pairs"`
	PairRevisions       map[string]string   `json:"pair_revisions,omitempty"`
}

type UpstreamStatePair struct {
	AccountID int64  `json:"account_id"`
	Model     string `json:"model"`
}

func (v UpstreamStateSettings) manages(accountID int64, model string) bool {
	return v.Enabled && v.hasPair(accountID, model)
}

func (v UpstreamStateSettings) hasPair(accountID int64, model string) bool {
	for _, p := range v.Pairs {
		if p.AccountID == accountID && p.Model == model {
			return true
		}
	}
	return false
}

func upstreamStatePairKey(pair UpstreamStatePair) string {
	return upstreamStateDigest(fmt.Sprintf("%d\x00%s", pair.AccountID, pair.Model))
}

func (v UpstreamStateSettings) pairRevision(accountID int64, model string) string {
	return v.PairRevisions[upstreamStatePairKey(UpstreamStatePair{AccountID: accountID, Model: model})]
}

func (v *UpstreamStateSettings) normalizePairRevisions(previous UpstreamStateSettings) {
	revisions := make(map[string]string, len(v.Pairs))
	for _, pair := range v.Pairs {
		key := upstreamStatePairKey(pair)
		if previous.hasPair(pair.AccountID, pair.Model) {
			revisions[key] = previous.PairRevisions[key]
		}
		if revisions[key] == "" {
			revisions[key] = uuid.NewString()
		}
	}
	v.PairRevisions = revisions
}

func defaultUpstreamStateSettings() UpstreamStateSettings {
	return UpstreamStateSettings{
		AutoReplaceEnabled:  true,
		TTLMinutes:          40,
		ExpectedLength:      292,
		WebshareCountryMode: webshareCountryModeRandom,
	}
}

func (v UpstreamStateSettings) Validate() error {
	seen := map[UpstreamStatePair]bool{}
	if len(v.Pairs) > 10000 {
		return errors.New("too many account/model pairs")
	}
	for _, p := range v.Pairs {
		if p.AccountID <= 0 || strings.TrimSpace(p.Model) != p.Model || p.Model == "" || len(p.Model) > 256 || strings.ContainsAny(p.Model, "*\r\n\t") || seen[p] {
			return errors.New("invalid or duplicate account/model pair")
		}
		seen[p] = true
	}
	if v.TTLMinutes < 1 || v.TTLMinutes > 60 {
		return errors.New("ttl_minutes must be between 1 and 60")
	}
	if v.ExpectedLength < 1 || v.ExpectedLength > 8192 {
		return errors.New("expected_length must be between 1 and 8192")
	}
	if len(v.WebshareAPIKey) > 512 || strings.TrimSpace(v.WebshareAPIKey) != v.WebshareAPIKey || strings.ContainsAny(v.WebshareAPIKey, "\r\n") {
		return errors.New("invalid Webshare API key")
	}
	if v.WebshareEnabled && v.WebshareAPIKey == "" {
		return errors.New("Webshare API key is required when Webshare refresh is enabled")
	}
	switch v.WebshareCountryMode {
	case webshareCountryModeRandom:
		if len(v.WebshareCountries) != 0 {
			return errors.New("webshare_countries must be empty in random mode")
		}
	case webshareCountryModeSpecified:
		if len(v.WebshareCountries) == 0 || len(v.WebshareCountries) > 64 {
			return errors.New("webshare_countries must contain 1 to 64 country codes in specified mode")
		}
		seenCountries := make(map[string]bool, len(v.WebshareCountries))
		for _, country := range v.WebshareCountries {
			if len(country) != 2 || country != strings.ToUpper(country) || country[0] < 'A' || country[0] > 'Z' || country[1] < 'A' || country[1] > 'Z' || seenCountries[country] {
				return errors.New("webshare_countries must contain unique uppercase ISO 3166-1 alpha-2 codes")
			}
			seenCountries[country] = true
		}
	default:
		return errors.New("webshare_country_mode must be random or specified")
	}
	return nil
}

type cachedUpstreamStateSettings struct {
	settings UpstreamStateSettings
	expires  time.Time
}

func (s *SettingService) GetUpstreamStateSettings(ctx context.Context) (UpstreamStateSettings, error) {
	v := defaultUpstreamStateSettings()
	if s == nil || s.settingRepo == nil {
		return v, nil
	}
	raw, err := s.settingRepo.GetValue(ctx, upstreamStateSettingKey)
	if errors.Is(err, ErrSettingNotFound) {
		return v, nil
	}
	if err != nil {
		return v, err
	}
	if raw == "" {
		return v, nil
	}
	if err = json.Unmarshal([]byte(raw), &v); err != nil {
		return defaultUpstreamStateSettings(), err
	}
	if err = v.Validate(); err != nil {
		return defaultUpstreamStateSettings(), err
	}
	// Pair revisions are server-owned. Repair an incomplete value deterministically
	// so every instance derives the same scope before the next settings write.
	for _, pair := range v.Pairs {
		key := upstreamStatePairKey(pair)
		if v.PairRevisions == nil {
			v.PairRevisions = make(map[string]string, len(v.Pairs))
		}
		if v.PairRevisions[key] == "" {
			v.PairRevisions[key] = upstreamStateDigest(v.StateRevision + ":" + key)
		}
	}
	return v, nil
}

func (s *SettingService) upstreamStateSettings(ctx context.Context) UpstreamStateSettings {
	if s == nil {
		return defaultUpstreamStateSettings()
	}
	if raw := s.upstreamStateSettingsCache.Load(); raw != nil {
		if v, ok := raw.(*cachedUpstreamStateSettings); ok && time.Now().Before(v.expires) {
			return v.settings
		}
	}
	v, _, _ := s.upstreamStateSettingsSF.Do("settings", func() (any, error) {
		before := s.upstreamStateSettingsCache.Load()
		if cached, ok := before.(*cachedUpstreamStateSettings); ok && time.Now().Before(cached.expires) {
			return cached.settings, nil
		}
		readCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
		defer cancel()
		settings, err := s.GetUpstreamStateSettings(readCtx)
		if err != nil {
			settings = defaultUpstreamStateSettings()
		}
		// An older refresh must not overwrite a just-saved policy. The request
		// path never waits for the administrator's write transaction.
		if !s.upstreamStateSettingsCache.CompareAndSwap(before, &cachedUpstreamStateSettings{settings, time.Now().Add(3 * time.Second)}) {
			if latest, ok := s.upstreamStateSettingsCache.Load().(*cachedUpstreamStateSettings); ok {
				return latest.settings, nil
			}
		}
		return settings, nil
	})
	if settings, ok := v.(UpstreamStateSettings); ok {
		return settings
	}
	return defaultUpstreamStateSettings()
}

func (s *SettingService) SetUpstreamStateSettings(ctx context.Context, v UpstreamStateSettings) (UpstreamStateSettings, error) {
	if err := v.Validate(); err != nil {
		return v, err
	}
	if s.upstreamStateStore == nil {
		return v, errors.New("upstream state cache unavailable")
	}
	s.upstreamStateSettingsMu.Lock()
	defer s.upstreamStateSettingsMu.Unlock()
	current, err := s.GetUpstreamStateSettings(ctx)
	if err != nil {
		return v, err
	}
	if current.Revision != v.Revision {
		return v, ErrUpstreamStateConflict
	}
	v.normalizePairRevisions(current)
	stateBreakingChange := current.Enabled != v.Enabled || current.ExpectedLength != v.ExpectedLength || current.StateRevision == ""
	// Only changes that alter whether a token may be injected invalidate the
	// cache. Rotation timing and Webshare credentials affect future refreshes and
	// do not discard a still-valid state.
	if stateBreakingChange {
		// Fence in-flight captures before publishing the new policy. A failed DB
		// write may clear the optional cache, but cannot enable an unsaved policy.
		if err := s.upstreamStateStore.Clear(ctx, ""); err != nil {
			return v, err
		}
		v.StateRevision = uuid.NewString()
	} else {
		v.StateRevision = current.StateRevision
	}
	v.Revision = uuid.NewString()
	raw, err := json.Marshal(v)
	if err != nil {
		return v, err
	}
	if err = s.settingRepo.Set(ctx, upstreamStateSettingKey, string(raw)); err != nil {
		return v, err
	}
	s.upstreamStateSettingsCache.Store(&cachedUpstreamStateSettings{v, time.Now().Add(3 * time.Second)})
	return v, nil
}

// Raw values only cross this internal repository boundary, never the admin API.
type UpstreamStateRecord struct {
	ID                string `json:"id"`
	Revision          string `json:"revision"`
	PairRevision      string `json:"pair_revision"`
	AccountID         int64  `json:"account_id"`
	AccountName       string `json:"account_name"`
	Model             string `json:"model"`
	ProxyID           int64  `json:"proxy_id"`
	Transport         string `json:"transport"`
	State             string `json:"state"`
	AcquiredAt        int64  `json:"acquired_at"`
	IssuedAt          int64  `json:"issued_at"`
	UpstreamExpiresAt int64  `json:"upstream_expires_at"`
	RotationAt        int64  `json:"rotation_at"`
	PurgeAt           int64  `json:"purge_at"`
	Sequence          int64  `json:"sequence"`
	CheckedAt         int64  `json:"checked_at"`
	ObservedLength    int    `json:"observed_length"`
	Validation        string `json:"validation"`
	ExpirySource      string `json:"expiry_source,omitempty"`
	LastError         string `json:"last_error,omitempty"`
}

type UpstreamStateTicket struct {
	Epoch    string
	Sequence int64
}

type UpstreamStateStore interface {
	Begin(context.Context, string) (*UpstreamStateRecord, UpstreamStateTicket, error)
	Save(context.Context, UpstreamStateRecord, UpstreamStateTicket) error
	Replace(context.Context, UpstreamStateRecord, UpstreamStateTicket) error
	List(context.Context) ([]UpstreamStateRecord, error)
	Clear(context.Context, string) error
	TryLock(context.Context, string, time.Duration) (string, bool, error)
	Unlock(context.Context, string, string) error
}

// The matrix contains configured OAuth accounts, even before they receive traffic.
// Models are final upstream IDs, not inbound aliases. No discovery requests are sent.
type UpstreamStateMatrixRow struct {
	ID                string `json:"id,omitempty"`
	AccountID         int64  `json:"account_id"`
	AccountName       string `json:"account_name"`
	Model             string `json:"model"`
	Enabled           bool   `json:"enabled"`
	Cached            int    `json:"cached"`
	Digest            string `json:"digest,omitempty"`
	CheckedAt         int64  `json:"checked_at"`
	AcquiredAt        int64  `json:"acquired_at"`
	IssuedAt          int64  `json:"issued_at"`
	UpstreamExpiresAt int64  `json:"upstream_expires_at"`
	RotationAt        int64  `json:"rotation_at"`
	ObservedLength    int    `json:"observed_length"`
	Validation        string `json:"validation"`
	ExpirySource      string `json:"expiry_source,omitempty"`
	LastError         string `json:"last_error,omitempty"`
}

func upstreamStateAccountModels(a *Account) map[string]bool {
	models := map[string]bool{}
	mapping := a.GetModelMapping()
	if len(mapping) == 0 || a.IsOpenAIPassthroughEnabled() {
		for _, m := range openai.DefaultModels {
			if !isCodexDedicatedMediaModel(m.ID) {
				models[normalizeOpenAIModelForUpstream(a, m.ID)] = true
			}
		}
	} else {
		for _, target := range mapping {
			if target != "" && !strings.Contains(target, "*") {
				models[normalizeOpenAIModelForUpstream(a, target)] = true
			}
		}
	}
	for _, target := range a.GetCompactModelMapping() {
		if target != "" && !strings.Contains(target, "*") {
			models[normalizeOpenAIModelForUpstream(a, target)] = true
		}
	}
	return models
}

func (s *SettingService) UpstreamStateMatrix(ctx context.Context) ([]UpstreamStateMatrixRow, error) {
	if s.upstreamStateAccounts == nil || s.upstreamStateStore == nil {
		return nil, errors.New("upstream state management unavailable")
	}
	cfg, err := s.GetUpstreamStateSettings(ctx)
	if err != nil {
		return nil, err
	}
	accounts, err := s.upstreamStateAccounts.ListByPlatform(ctx, PlatformOpenAI)
	if err != nil {
		return nil, err
	}
	records, err := s.upstreamStateStore.List(ctx)
	if err != nil {
		return nil, err
	}
	rows := []UpstreamStateMatrixRow{}
	now := time.Now().UnixMilli()
	enabled := map[UpstreamStatePair]bool{}
	configured := map[int64][]string{}
	for _, p := range cfg.Pairs {
		enabled[p] = true
		configured[p.AccountID] = append(configured[p.AccountID], p.Model)
	}
	observations := map[UpstreamStatePair][]UpstreamStateRecord{}
	for _, r := range records {
		pair := UpstreamStatePair{r.AccountID, r.Model}
		if cfg.Enabled && enabled[pair] && r.Revision == cfg.StateRevision && r.PairRevision == cfg.pairRevision(r.AccountID, r.Model) && r.PurgeAt > now {
			observations[pair] = append(observations[pair], r)
		}
	}
	for _, a := range accounts {
		if a.Type != AccountTypeOAuth {
			continue
		}
		models := upstreamStateAccountModels(&a)
		for _, model := range configured[a.ID] {
			models[model] = true
		}
		for model := range models {
			pair := UpstreamStatePair{a.ID, model}
			row := UpstreamStateMatrixRow{AccountID: a.ID, AccountName: a.Name, Model: model, Enabled: enabled[pair], Validation: "waiting"}
			for _, r := range observations[pair] {
				if r.CheckedAt > row.CheckedAt {
					row.ID, row.CheckedAt, row.ObservedLength, row.Validation, row.ExpirySource, row.LastError = r.ID, r.CheckedAt, r.ObservedLength, r.Validation, r.ExpirySource, r.LastError
				}
				if r.State != "" && r.UpstreamExpiresAt > now {
					row.Cached = 1
					row.ID, row.Digest = r.ID, upstreamStateDigest(r.State)[:12]
					row.AcquiredAt, row.IssuedAt = r.AcquiredAt, r.IssuedAt
					row.UpstreamExpiresAt, row.RotationAt = r.UpstreamExpiresAt, r.RotationAt
				}
			}
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].AccountID != rows[j].AccountID {
			return rows[i].AccountID < rows[j].AccountID
		}
		return rows[i].Model < rows[j].Model
	})
	return rows, nil
}

func (s *SettingService) SetUpstreamStatePair(ctx context.Context, pair UpstreamStatePair, enabled bool, revision string) (UpstreamStateSettings, error) {
	s.upstreamStateSettingsMu.Lock()
	defer s.upstreamStateSettingsMu.Unlock()
	cfg, err := s.GetUpstreamStateSettings(ctx)
	if err != nil {
		return cfg, err
	}
	if cfg.Revision != revision {
		return cfg, ErrUpstreamStateConflict
	}
	if s.upstreamStateAccounts == nil {
		return cfg, errors.New("account repository unavailable")
	}
	account, err := s.upstreamStateAccounts.GetByID(ctx, pair.AccountID)
	if err != nil {
		return cfg, err
	}
	if account.Platform != PlatformOpenAI || account.Type != AccountTypeOAuth {
		return cfg, errors.New("only OpenAI OAuth accounts are supported")
	}
	wasEnabled := cfg.hasPair(pair.AccountID, pair.Model)
	if wasEnabled == enabled {
		return cfg, nil
	}
	if s.upstreamStateStore == nil {
		return cfg, errors.New("upstream state cache unavailable")
	}
	// Fence the previous generation before changing policy. Re-enabling always
	// receives a new pair revision, so late writes and pooled WS connections from
	// an earlier enabled period can never become eligible again.
	if err = s.clearUpstreamStatePair(ctx, pair); err != nil {
		return cfg, err
	}
	pairs := make([]UpstreamStatePair, 0, len(cfg.Pairs)+1)
	for _, p := range cfg.Pairs {
		if p != pair {
			pairs = append(pairs, p)
		}
	}
	if enabled {
		pairs = append(pairs, pair)
	}
	cfg.Pairs = pairs
	cfg.normalizePairRevisions(cfg)
	if enabled {
		cfg.PairRevisions[upstreamStatePairKey(pair)] = uuid.NewString()
	}
	cfg.Revision = uuid.NewString()
	if cfg.StateRevision == "" {
		return cfg, errors.New("state cache revision is missing")
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		return cfg, err
	}
	if err = s.settingRepo.Set(ctx, upstreamStateSettingKey, string(raw)); err != nil {
		return cfg, err
	}
	s.upstreamStateSettingsCache.Store(&cachedUpstreamStateSettings{cfg, time.Now().Add(3 * time.Second)})
	if !enabled {
		// Catch writes that began after the first fence while another instance
		// still had the old policy cached. Their old pair revision stays unusable.
		_ = s.clearUpstreamStatePair(ctx, pair)
	}
	return cfg, nil
}

func (s *SettingService) clearUpstreamStatePair(ctx context.Context, pair UpstreamStatePair) error {
	records, err := s.upstreamStateStore.List(ctx)
	if err != nil {
		return err
	}
	cleared := false
	for _, record := range records {
		if record.AccountID != pair.AccountID || record.Model != pair.Model {
			continue
		}
		if err = s.upstreamStateStore.Clear(ctx, record.ID); err != nil {
			return err
		}
		cleared = true
	}
	if cleared {
		return nil
	}
	// Clear rotates the cache epoch even if the synthetic ID is absent.
	return s.upstreamStateStore.Clear(ctx, "pair-fence:"+upstreamStatePairKey(pair))
}

func upstreamStateDigest(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }

func parseUpstreamStateToken(state string, expectedLength int, now time.Time) (validation string, issuedAt, expiresAt int64) {
	state = strings.TrimSpace(state)
	if state == "" {
		return "missing", 0, 0
	}
	if len(state) == expectedLength+16 {
		validation = upstreamStateValidationLong
	} else if len(state) != expectedLength {
		validation = "mismatch"
	} else {
		validation = upstreamStateValidationNormal
	}
	raw, err := base64.URLEncoding.DecodeString(state)
	if err != nil || len(raw) < 9 || raw[0] != 0x80 {
		// Length is the acceptance boundary. Some upstream variants do not
		// expose a parseable Fernet timestamp; callers use the configured
		// fallback lifetime for those otherwise normal-sized values.
		if validation == upstreamStateValidationNormal {
			return validation, 0, 0
		}
		return "invalid", 0, 0
	}
	issued := time.Unix(int64(binary.BigEndian.Uint64(raw[1:9])), 0)
	if issued.After(now.Add(upstreamStateFutureClockSkew)) {
		if validation == upstreamStateValidationNormal {
			return validation, 0, 0
		}
		return "invalid", 0, 0
	}
	expires := issued.Add(upstreamStateLifetime)
	if !expires.After(now) {
		return "expired", issued.UnixMilli(), expires.UnixMilli()
	}
	return validation, issued.UnixMilli(), expires.UnixMilli()
}

func buildUpstreamStateObservation(base UpstreamStateRecord, state string, cfg UpstreamStateSettings, now time.Time) UpstreamStateRecord {
	validation, issuedAt, expiresAt := parseUpstreamStateToken(state, cfg.ExpectedLength, now)
	base.CheckedAt = now.UnixMilli()
	base.ObservedLength = len(strings.TrimSpace(state))
	base.Validation = validation
	base.IssuedAt = issuedAt
	base.UpstreamExpiresAt = expiresAt
	base.PurgeAt = now.Add(upstreamStateObservationTTL).UnixMilli()
	if validation == upstreamStateValidationNormal {
		base.State = strings.TrimSpace(state)
		base.AcquiredAt = base.CheckedAt
		if expiresAt > 0 {
			base.ExpirySource = "fernet"
			base.RotationAt = expiresAt - int64(upstreamStateRotationLead/time.Millisecond)
			base.PurgeAt = expiresAt
		} else {
			base.ExpirySource = "fallback"
			base.UpstreamExpiresAt = now.Add(time.Duration(cfg.TTLMinutes) * time.Minute).UnixMilli()
			lead := upstreamStateRotationLead
			fallbackLifetime := time.Duration(cfg.TTLMinutes) * time.Minute
			if fallbackLifetime <= lead {
				lead = fallbackLifetime / 2
			}
			base.RotationAt = base.UpstreamExpiresAt - int64(lead/time.Millisecond)
			base.PurgeAt = base.UpstreamExpiresAt
		}
	}
	return base
}

type upstreamStateScope struct {
	settings *SettingService
	config   UpstreamStateSettings
	record   UpstreamStateRecord
}

type upstreamStateAttempt struct {
	scope  *upstreamStateScope
	ticket UpstreamStateTicket
}

func (scope *upstreamStateScope) poolKey() string {
	return scope.config.StateRevision + ":" + scope.config.pairRevision(scope.record.AccountID, scope.record.Model) + ":" + scope.record.ID
}

// One managed state is shared by every request using the same selected OAuth
// account and final upstream model. Inbound API keys, sessions, client identity,
// transport and proxy egress deliberately do not split this cache.
func (s *OpenAIGatewayService) newUpstreamStateScope(ctx context.Context, c *gin.Context, account *Account, headers http.Header, model, transport string) *upstreamStateScope {
	if s == nil || s.settingService == nil || account == nil || account.Type != AccountTypeOAuth || account.Platform != PlatformOpenAI {
		return nil
	}
	cfg := s.settingService.upstreamStateSettings(ctx)
	return s.newUpstreamStateScopeWithConfig(c, account, model, transport, cfg)
}

func (s *OpenAIGatewayService) newUpstreamStateScopeWithConfig(c *gin.Context, account *Account, model, transport string, cfg UpstreamStateSettings) *upstreamStateScope {
	if !cfg.manages(account.ID, model) {
		return nil
	}
	pairRevision := cfg.pairRevision(account.ID, model)
	if pairRevision == "" {
		return nil
	}
	scope := &upstreamStateScope{settings: s.settingService, config: cfg}
	model = strings.TrimSpace(model)
	source := codexAccountIdentitySource(c, account)
	credentialNamespace := codexAccountIdentityNamespace(source)
	if credentialNamespace == "" || model == "" {
		return scope
	}
	parts := []any{cfg.StateRevision, pairRevision, account.ID, credentialNamespace, model}
	proxyID := int64(0)
	if account.ProxyID != nil {
		proxyID = *account.ProxyID
	}
	raw, _ := json.Marshal(parts)
	id := upstreamStateDigest(string(raw))
	scope.record = UpstreamStateRecord{ID: id, Revision: cfg.StateRevision, PairRevision: pairRevision, AccountID: account.ID, AccountName: account.Name, Model: model, ProxyID: proxyID, Transport: transport}
	return scope
}

// This is the final send/dial boundary, after old bridge caches and header
// overrides. Enabled management never trusts an unscoped client echo.
func (scope *upstreamStateScope) begin(ctx context.Context, headers http.Header) *upstreamStateAttempt {
	if scope == nil {
		return nil
	}
	headers.Del(openAICodexTurnStateHeader)
	current := scope.settings.upstreamStateSettings(ctx)
	if current.StateRevision != scope.config.StateRevision || !current.manages(scope.record.AccountID, scope.record.Model) {
		return nil
	}
	if scope.record.ID == "" || scope.settings.upstreamStateStore == nil {
		return nil
	}
	cacheCtx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	record, ticket, err := scope.settings.upstreamStateStore.Begin(cacheCtx, scope.record.ID)
	if err != nil {
		return nil
	} // Optional cache never fails the business request.
	if record != nil && record.UpstreamExpiresAt > time.Now().UnixMilli() && len(record.State) == scope.config.ExpectedLength {
		headers.Set(openAICodexTurnStateHeader, record.State)
	}
	return &upstreamStateAttempt{scope: scope, ticket: ticket}
}

func (a *upstreamStateAttempt) capture(ctx context.Context, status int, headers http.Header) {
	if a == nil || !((status >= 200 && status < 300) || status == http.StatusSwitchingProtocols) {
		return
	}
	current := a.scope.settings.upstreamStateSettings(ctx)
	if current.StateRevision != a.scope.config.StateRevision || !current.manages(a.scope.record.AccountID, a.scope.record.Model) {
		return
	}
	r := buildUpstreamStateObservation(a.scope.record, headers.Get(openAICodexTurnStateHeader), a.scope.config, time.Now())
	r.Sequence = a.ticket.Sequence
	cacheCtx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	_ = a.scope.settings.upstreamStateStore.Save(cacheCtx, r, a.ticket)
}

type upstreamStateContextKey struct{}

func (s *OpenAIGatewayService) attachUpstreamStateScope(req *http.Request, c *gin.Context, account *Account, body []byte) *http.Request {
	if !strings.HasSuffix(req.URL.Path, "/responses") && !strings.HasSuffix(req.URL.Path, "/responses/compact") {
		return req
	}
	scope := s.newUpstreamStateScope(req.Context(), c, account, req.Header, gjson.GetBytes(body, "model").String(), "http")
	if scope == nil {
		return req
	}
	return req.WithContext(context.WithValue(req.Context(), upstreamStateContextKey{}, scope))
}
