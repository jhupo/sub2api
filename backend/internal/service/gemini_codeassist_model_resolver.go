package service

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
	"golang.org/x/sync/singleflight"
)

const (
	geminiCodeAssistCatalogTTL                = 5 * time.Minute
	geminiCodeAssistCatalogStaleTTL           = 30 * time.Minute
	geminiCodeAssistCatalogBodyLimit    int64 = 8 << 20
	geminiCodeAssistCatalogFetchTimeout       = 20 * time.Second
)

// ErrGeminiModelUnavailable is returned only after a successful account-scoped
// catalog lookup proves that the requested model is not available. The name is
// retained at the Gemini gateway boundary; the catalog itself may contain
// Gemini, Claude, GPT-OSS, image, video, or future Code Assist model families.
var ErrGeminiModelUnavailable = errors.New("requested model is not available for this Gemini OAuth account")

type geminiCodeAssistCatalogEntry struct {
	models    map[string]antigravity.ModelInfo
	fetchedAt time.Time
}

// GeminiCodeAssistModelCatalog owns both views of an account's live Code Assist
// catalog. List applies account model_mapping, ListAuthorized exposes the OAuth
// grant for admin synchronization, and Resolve converts a mapped target to the
// actual v1internal runtime ID.
type GeminiCodeAssistModelCatalog interface {
	Resolve(context.Context, *Account, string, string) (string, error)
	List(context.Context, *Account, string) ([]geminicli.Model, error)
	ListAuthorized(context.Context, *Account, string, bool) ([]geminicli.Model, error)
}

// GeminiCodeAssistModelResolver owns the account-scoped runtime model catalog.
// It deliberately keeps this data in memory: model availability is upstream
// state and must not be persisted into account credentials or the database.
type GeminiCodeAssistModelResolver struct {
	mu          sync.Mutex
	entries     map[string]geminiCodeAssistCatalogEntry
	fetchGroup  singleflight.Group
	fetchModels func(context.Context, *Account, string) (map[string]antigravity.ModelInfo, error)
	now         func() time.Time
}

func NewGeminiCodeAssistModelResolver() *GeminiCodeAssistModelResolver {
	r := &GeminiCodeAssistModelResolver{
		entries: make(map[string]geminiCodeAssistCatalogEntry),
		now:     time.Now,
	}
	r.fetchModels = r.fetchAvailableModels
	return r
}

func (r *GeminiCodeAssistModelResolver) cacheKey(account *Account) string {
	if account == nil {
		return ""
	}
	projectID := strings.TrimSpace(account.GetCredential("project_id"))
	return fmt.Sprintf("%d:%s", account.ID, projectID)
}

func (r *GeminiCodeAssistModelResolver) cached(key string, now time.Time) (geminiCodeAssistCatalogEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	entry, ok := r.entries[key]
	if !ok {
		return geminiCodeAssistCatalogEntry{}, false
	}
	if now.Sub(entry.fetchedAt) > geminiCodeAssistCatalogStaleTTL {
		delete(r.entries, key)
		return geminiCodeAssistCatalogEntry{}, false
	}
	return entry, true
}

func (r *GeminiCodeAssistModelResolver) store(key string, models map[string]antigravity.ModelInfo, fetchedAt time.Time) geminiCodeAssistCatalogEntry {
	copyModels := make(map[string]antigravity.ModelInfo, len(models))
	for id, info := range models {
		copyModels[id] = info
	}
	entry := geminiCodeAssistCatalogEntry{models: copyModels, fetchedAt: fetchedAt}
	r.mu.Lock()
	r.entries[key] = entry
	// Bound memory if accounts are deleted/recreated repeatedly.
	if len(r.entries) > 256 {
		cutoff := fetchedAt.Add(-geminiCodeAssistCatalogStaleTTL)
		for cachedKey, cachedEntry := range r.entries {
			if cachedEntry.fetchedAt.Before(cutoff) {
				delete(r.entries, cachedKey)
			}
		}
	}
	r.mu.Unlock()
	return entry
}

func (r *GeminiCodeAssistModelResolver) fetchAvailableModels(ctx context.Context, account *Account, accessToken string) (map[string]antigravity.ModelInfo, error) {
	if account == nil {
		return nil, errors.New("account is nil")
	}
	projectID := strings.TrimSpace(account.GetCredential("project_id"))
	if projectID == "" {
		return nil, fmt.Errorf("%w: model discovery", ErrGeminiProjectIDRequired)
	}
	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}
	client, err := antigravity.NewClient(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("create Code Assist client: %w", err)
	}
	models, err := client.FetchAvailableModelsCatalog(ctx, accessToken, projectID, geminiCodeAssistCatalogBodyLimit)
	if err != nil {
		return nil, err
	}
	if models == nil || len(models.Models) == 0 {
		return nil, errors.New("code assist returned no models")
	}
	return models.Models, nil
}

func (r *GeminiCodeAssistModelResolver) fetch(ctx context.Context, account *Account, accessToken string) (geminiCodeAssistCatalogEntry, error) {
	if r.fetchModels == nil {
		return geminiCodeAssistCatalogEntry{}, errors.New("code assist model fetcher is not configured")
	}
	models, err := r.fetchModels(ctx, account, accessToken)
	if err != nil {
		return geminiCodeAssistCatalogEntry{}, err
	}
	if len(models) == 0 {
		return geminiCodeAssistCatalogEntry{}, errors.New("code assist returned no models")
	}
	return r.store(r.cacheKey(account), models, r.now()), nil
}

func (r *GeminiCodeAssistModelResolver) catalog(ctx context.Context, account *Account, accessToken string, force bool) (geminiCodeAssistCatalogEntry, error) {
	if r == nil {
		return geminiCodeAssistCatalogEntry{}, errors.New("model resolver is not configured")
	}
	if account == nil {
		return geminiCodeAssistCatalogEntry{}, errors.New("account is nil")
	}
	key := r.cacheKey(account)
	now := r.now()
	if !force {
		if entry, ok := r.cached(key, now); ok && now.Sub(entry.fetchedAt) <= geminiCodeAssistCatalogTTL {
			return entry, nil
		}
	}
	resultCh := r.fetchGroup.DoChan(key, func() (any, error) {
		// A caller can miss the initial cache check while another goroutine is
		// completing the refresh. Re-check inside singleflight so that late
		// arrivals reuse the freshly stored catalog instead of starting a second
		// upstream request immediately after the first one.
		if !force {
			freshNow := r.now()
			if entry, ok := r.cached(key, freshNow); ok && freshNow.Sub(entry.fetchedAt) <= geminiCodeAssistCatalogTTL {
				return entry, nil
			}
		}
		fetchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), geminiCodeAssistCatalogFetchTimeout)
		defer cancel()
		return r.fetch(fetchCtx, account, accessToken)
	})
	var result singleflight.Result
	select {
	case <-ctx.Done():
		return geminiCodeAssistCatalogEntry{}, ctx.Err()
	case result = <-resultCh:
	}
	entry, _ := result.Val.(geminiCodeAssistCatalogEntry)
	err := result.Err
	if err == nil {
		return entry, nil
	}
	// Keep the last known good directory during upstream discovery failures.
	if stale, ok := r.cached(key, now); ok {
		return stale, err
	}
	return geminiCodeAssistCatalogEntry{}, err
}

func normalizeGeminiRuntimeModelID(model string) string {
	return strings.TrimPrefix(strings.TrimSpace(model), "models/")
}

var geminiRuntimeVariantSuffixes = []string{
	"-extra-low",
	"-extra-high",
	"-thinking",
	"-minimal",
	"-medium",
	"-tiered",
	"-high",
	"-low",
}

func trimGeminiRuntimeVariant(model string) (string, bool) {
	for _, suffix := range geminiRuntimeVariantSuffixes {
		if strings.HasSuffix(model, suffix) {
			return strings.TrimSuffix(model, suffix), true
		}
	}
	return model, false
}

func geminiModelDisplayName(info antigravity.ModelInfo, fallback string) string {
	for _, value := range []string{info.DisplayName, info.Label, info.Name, info.ModelName} {
		if display := strings.TrimSpace(value); display != "" {
			return display
		}
	}
	return fallback
}

func runtimeModelCandidates(requested string) []string {
	requested = normalizeGeminiRuntimeModelID(requested)
	if requested == "" {
		return nil
	}
	seen := map[string]struct{}{}
	result := make([]string, 0, 8)
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		if _, exists := seen[id]; exists {
			return
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	isGemini31Pro := requested == "gemini-3.1-pro" || strings.HasPrefix(requested, "gemini-3.1-pro-") || requested == "gemini-pro-agent"
	isGemini35Flash := requested == "gemini-3-flash" || requested == "gemini-3-flash-preview" || requested == "gemini-3-flash-agent" || strings.HasPrefix(requested, "gemini-3.5-flash")
	// Some advertised runtime ids are not callable. Put the verified agent ids
	// ahead of those aliases before adding the exact request as a fallback.
	if isGemini31Pro && (strings.HasSuffix(requested, "-high") || strings.HasSuffix(requested, "-extra-high") || requested == "gemini-pro-agent") {
		add("gemini-pro-agent")
	}
	if isGemini35Flash {
		switch {
		case strings.HasSuffix(requested, "-high"), strings.HasSuffix(requested, "-extra-high"), requested == "gemini-3-flash-agent":
			add("gemini-3-flash-agent")
		case strings.HasSuffix(requested, "-medium"):
			add("gemini-3.5-flash-low")
		case requested == "gemini-3.5-flash", requested == "gemini-3-flash", requested == "gemini-3-flash-preview":
			add("gemini-3.5-flash-extra-low")
		}
	}
	add(requested)
	// Public Gemini API names commonly carry a preview/customtools suffix,
	// while Code Assist exposes the same family under a runtime id without it.
	for _, suffix := range []string{"-preview-customtools", "-preview"} {
		if strings.HasSuffix(requested, suffix) {
			add(strings.TrimSuffix(requested, suffix))
			break
		}
	}
	requestedFamily, explicitVariant := trimGeminiRuntimeVariant(requested)
	if explicitVariant {
		add(requestedFamily)
	}
	if isGemini31Pro {
		// Low is the stable default; gemini-3.1-pro-high currently returns 400
		// for some Code Assist stream requests, while gemini-pro-agent is the
		// verified high-thinking runtime alias.
		if strings.HasSuffix(requested, "-high") || requested == "gemini-pro-agent" {
			add("gemini-pro-agent")
			add("gemini-3.1-pro-high")
			add("gemini-3.1-pro-low")
		} else if strings.HasSuffix(requested, "-low") {
			add("gemini-3.1-pro-low")
			add("gemini-pro-agent")
			add("gemini-3.1-pro-high")
		} else {
			add("gemini-3.1-pro-low")
			add("gemini-pro-agent")
			add("gemini-3.1-pro-high")
		}
	}
	if isGemini35Flash {
		switch {
		case strings.HasSuffix(requested, "-high"), strings.HasSuffix(requested, "-extra-high"), requested == "gemini-3-flash-agent":
			add("gemini-3-flash-agent")
			add("gemini-3.5-flash-low")
			add("gemini-3.5-flash-extra-low")
		case strings.HasSuffix(requested, "-medium"):
			add("gemini-3.5-flash-low")
			add("gemini-3.5-flash-extra-low")
			add("gemini-3-flash-agent")
		default:
			add("gemini-3.5-flash-extra-low")
			add("gemini-3.5-flash-low")
			add("gemini-3-flash-agent")
		}
	}
	// Public family ids should use a conservative runtime by default. Explicit
	// variant ids already appear first and therefore keep their requested level.
	if !explicitVariant {
		add(requested + "-low")
		add(requested + "-extra-low")
		add(requested + "-minimal")
		add(requested + "-medium")
		add(requested + "-tiered")
		add(requested + "-high")
		add(requested + "-extra-high")
	}
	return result
}

func applyGeminiRuntimeEffort(requested string, effort *string) string {
	requested = normalizeGeminiRuntimeModelID(requested)
	if requested == "" || effort == nil {
		return requested
	}
	if _, explicitVariant := trimGeminiRuntimeVariant(requested); explicitVariant || strings.HasSuffix(requested, "-agent") {
		return requested
	}
	level := NormalizeMaxReasoningEffort(*effort)
	switch {
	case requested == "gemini-3.1-pro" || strings.HasPrefix(requested, "gemini-3.1-pro-preview"):
		if level == "high" || level == "xhigh" || level == "max" {
			return "gemini-3.1-pro-high"
		}
	case requested == "gemini-3.5-flash" || requested == "gemini-3-flash" || requested == "gemini-3-flash-preview":
		switch level {
		case "medium":
			return "gemini-3.5-flash-medium"
		case "high", "xhigh", "max":
			return "gemini-3.5-flash-high"
		}
	case strings.HasPrefix(requested, "gemini-3.6-flash"), strings.HasPrefix(requested, "gemini-3.7-flash"), strings.HasPrefix(requested, "gemini-3.8-flash"):
		switch level {
		case "medium":
			return requested + "-medium"
		case "high", "xhigh", "max":
			return requested + "-high"
		default:
			return requested + "-low"
		}
	}
	return requested
}

func resolveRuntimeModel(models map[string]antigravity.ModelInfo, requested string) (string, bool) {
	for _, candidate := range runtimeModelCandidates(requested) {
		if _, ok := models[candidate]; ok {
			return candidate, true
		}
	}
	requested = normalizeGeminiRuntimeModelID(requested)
	requestedFamily := requested
	for _, suffix := range []string{"-preview-customtools", "-preview"} {
		requestedFamily = strings.TrimSuffix(requestedFamily, suffix)
	}
	// A catalog can expose an equivalent runtime ID under a thinking variant or
	// a provider alias. Only accept IDs whose normalized public family matches;
	// display names are descriptive metadata and are not a routing contract.
	runtimeIDs := make([]string, 0, len(models))
	for id := range models {
		runtimeIDs = append(runtimeIDs, id)
	}
	sort.Strings(runtimeIDs)
	for _, rawID := range runtimeIDs {
		id := normalizeGeminiRuntimeModelID(rawID)
		if !isUsableCodeAssistRuntimeModelID(id) {
			continue
		}
		base := codeAssistPublicModelID(id)
		if base == requested || base == requestedFamily {
			return id, true
		}
	}
	return "", false
}

// Resolve returns the real runtime model id used by v1internal. Discovery is
// only used for Code Assist OAuth accounts; AI Studio/API-key accounts keep
// their existing mapping behavior.
func (r *GeminiCodeAssistModelResolver) Resolve(ctx context.Context, account *Account, accessToken, requested string) (string, error) {
	if account == nil || (!account.IsGeminiCodeAssist() && !account.IsGeminiAntigravity()) {
		return normalizeGeminiRuntimeModelID(requested), nil
	}
	requested = normalizeGeminiRuntimeModelID(requested)
	if requested == "" {
		return "", errors.New("model is required")
	}
	requested = applyGeminiRuntimeEffort(requested, RequestedReasoningEffortFromContext(ctx))
	entry, fetchErr := r.catalog(ctx, account, accessToken, false)
	if fetchErr != nil && len(entry.models) == 0 {
		return "", fmt.Errorf("load Gemini OAuth model catalog: %w", fetchErr)
	}
	if runtime, ok := resolveRuntimeModel(entry.models, requested); ok {
		return runtime, nil
	}
	return "", fmt.Errorf("%w: %s", ErrGeminiModelUnavailable, requested)
}

// List returns the client-visible catalog after applying account model_mapping.
// An empty mapping exposes every model granted by OAuth. A non-empty mapping is
// both a whitelist and an alias table: only client-side keys whose targets can
// be resolved in the live authorization catalog are returned.
func (r *GeminiCodeAssistModelResolver) List(ctx context.Context, account *Account, accessToken string) ([]geminicli.Model, error) {
	entry, err := r.catalog(ctx, account, accessToken, false)
	if err != nil && len(entry.models) == 0 {
		return nil, err
	}
	authorized := collapseGeminiCodeAssistModels(entry.models)
	if account == nil || len(account.GetModelMapping()) == 0 {
		if len(authorized) == 0 {
			return nil, errors.New("code assist returned no usable models")
		}
		return authorized, err
	}

	byID := make(map[string]geminicli.Model, len(authorized))
	for _, model := range authorized {
		byID[model.ID] = model
	}
	mapping := account.GetModelMapping()
	clientIDs := make([]string, 0, len(mapping))
	for clientID := range mapping {
		clientIDs = append(clientIDs, clientID)
	}
	sort.Strings(clientIDs)

	models := make([]geminicli.Model, 0, len(clientIDs))
	for _, rawClientID := range clientIDs {
		clientID := normalizeGeminiRuntimeModelID(rawClientID)
		target := normalizeGeminiRuntimeModelID(mapping[rawClientID])
		if clientID == "" || target == "" {
			continue
		}
		runtimeID, ok := resolveRuntimeModel(entry.models, target)
		if !ok {
			continue
		}
		publicTarget := codeAssistPublicModelID(runtimeID)
		displayName := clientID
		if model, exists := byID[publicTarget]; exists && clientID == publicTarget {
			displayName = model.DisplayName
		}
		models = append(models, geminicli.Model{ID: clientID, Type: "model", DisplayName: displayName})
	}
	return models, err
}

// ListAuthorized returns every usable public model derived from the account's
// OAuth catalog, without applying model_mapping. Explicit administrator syncs
// pass force=true; normal reads keep using the short-lived runtime cache.
func (r *GeminiCodeAssistModelResolver) ListAuthorized(ctx context.Context, account *Account, accessToken string, force bool) ([]geminicli.Model, error) {
	entry, err := r.catalog(ctx, account, accessToken, force)
	if err != nil && len(entry.models) == 0 {
		return nil, err
	}
	models := collapseGeminiCodeAssistModels(entry.models)
	if len(models) == 0 {
		return nil, errors.New("code assist returned no usable models")
	}
	return models, err
}

func isUsableCodeAssistRuntimeModelID(model string) bool {
	model = normalizeGeminiRuntimeModelID(model)
	if model == "" || strings.ContainsAny(model, " \t\r\n") {
		return false
	}
	lower := strings.ToLower(model)
	return !strings.HasPrefix(lower, "model_") &&
		!strings.HasPrefix(lower, "chat_") &&
		!strings.HasPrefix(lower, "tab_")
}

func codeAssistPublicModelID(runtimeID string) string {
	runtimeID = normalizeGeminiRuntimeModelID(runtimeID)
	switch runtimeID {
	case "gemini-pro-agent":
		return "gemini-3.1-pro"
	case "gemini-3-flash-agent":
		return "gemini-3.5-flash"
	default:
		for _, suffix := range []string{"-preview-customtools", "-preview"} {
			if strings.HasSuffix(runtimeID, suffix) {
				return strings.TrimSuffix(runtimeID, suffix)
			}
		}
		publicID, _ := trimGeminiRuntimeVariant(runtimeID)
		return publicID
	}
}

func collapseGeminiCodeAssistModels(raw map[string]antigravity.ModelInfo) []geminicli.Model {
	public := make(map[string]geminicli.Model)
	runtimeIDs := make([]string, 0, len(raw))
	for runtimeID := range raw {
		runtimeIDs = append(runtimeIDs, runtimeID)
	}
	sort.Strings(runtimeIDs)
	for _, rawRuntimeID := range runtimeIDs {
		runtimeID := rawRuntimeID
		info := raw[rawRuntimeID]
		runtimeID = normalizeGeminiRuntimeModelID(runtimeID)
		if !isUsableCodeAssistRuntimeModelID(runtimeID) {
			continue
		}
		publicID := codeAssistPublicModelID(runtimeID)
		if publicID == "" {
			continue
		}
		if _, exists := public[publicID]; exists {
			continue
		}
		display := geminiModelDisplayName(info, publicID)
		public[publicID] = geminicli.Model{
			ID:          publicID,
			Type:        "model",
			DisplayName: display,
		}
	}
	models := make([]geminicli.Model, 0, len(public))
	for _, model := range public {
		models = append(models, model)
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	return models
}
