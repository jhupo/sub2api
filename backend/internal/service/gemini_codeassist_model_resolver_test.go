package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/Wei-Shaw/sub2api/internal/pkg/geminicli"
)

type cachedGeminiTokenStub struct{ token string }

func (s *cachedGeminiTokenStub) GetAccessToken(context.Context, string) (string, error) {
	return s.token, nil
}
func (s *cachedGeminiTokenStub) SetAccessToken(context.Context, string, string, time.Duration) error {
	return nil
}
func (s *cachedGeminiTokenStub) DeleteAccessToken(context.Context, string) error { return nil }
func (s *cachedGeminiTokenStub) AcquireRefreshLock(context.Context, string, time.Duration) (bool, error) {
	return true, nil
}
func (s *cachedGeminiTokenStub) ReleaseRefreshLock(context.Context, string) error { return nil }

type projectDetectCodeAssistStub struct{ calls int }

func (s *projectDetectCodeAssistStub) LoadCodeAssist(context.Context, string, string, *geminicli.LoadCodeAssistRequest) (*geminicli.LoadCodeAssistResponse, error) {
	s.calls++
	return &geminicli.LoadCodeAssistResponse{
		CloudAICompanionProject: "detected-project",
		CurrentTier:             &geminicli.TierInfo{ID: "standard-tier"},
	}, nil
}
func (s *projectDetectCodeAssistStub) OnboardUser(context.Context, string, string, *geminicli.OnboardUserRequest) (*geminicli.OnboardUserResponse, error) {
	return nil, nil
}

type blockingProjectDetectCodeAssistStub struct {
	calls   atomic.Int32
	entered chan struct{}
	release chan struct{}
	done    chan struct{}
}

func (s *blockingProjectDetectCodeAssistStub) LoadCodeAssist(ctx context.Context, _ string, _ string, _ *geminicli.LoadCodeAssistRequest) (*geminicli.LoadCodeAssistResponse, error) {
	s.calls.Add(1)
	select {
	case <-s.entered:
	default:
		close(s.entered)
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.release:
		close(s.done)
		return &geminicli.LoadCodeAssistResponse{CloudAICompanionProject: "shared-project"}, nil
	}
}

func (s *blockingProjectDetectCodeAssistStub) OnboardUser(context.Context, string, string, *geminicli.OnboardUserRequest) (*geminicli.OnboardUserResponse, error) {
	return nil, nil
}

func TestResolveRuntimeModelGeminiProAliases(t *testing.T) {
	models := map[string]antigravity.ModelInfo{
		"gemini-3.1-pro-low":  {DisplayName: "Gemini 3.1 Pro (Low)"},
		"gemini-3.1-pro-high": {DisplayName: "Gemini 3.1 Pro (High)"},
		"gemini-pro-agent":    {DisplayName: "Gemini 3.1 Pro (High)"},
	}

	tests := []struct {
		name      string
		requested string
		want      string
	}{
		{name: "preview defaults to low", requested: "gemini-3.1-pro-preview", want: "gemini-3.1-pro-low"},
		{name: "explicit high uses agent alias", requested: "gemini-3.1-pro-high", want: "gemini-pro-agent"},
		{name: "models prefix is normalized", requested: "models/gemini-3.1-pro", want: "gemini-3.1-pro-low"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := resolveRuntimeModel(models, tt.requested)
			if !ok {
				t.Fatalf("resolveRuntimeModel(%q) did not find a runtime model", tt.requested)
			}
			if got != tt.want {
				t.Fatalf("resolveRuntimeModel(%q) = %q, want %q", tt.requested, got, tt.want)
			}
		})
	}
}

func TestApplyGeminiRuntimeEffort(t *testing.T) {
	for _, tt := range []struct {
		model  string
		effort string
		want   string
	}{
		{model: "gemini-3.1-pro", effort: "high", want: "gemini-3.1-pro-high"},
		{model: "gemini-3.1-pro", effort: "medium", want: "gemini-3.1-pro"},
		{model: "gemini-3.5-flash", effort: "medium", want: "gemini-3.5-flash-medium"},
		{model: "gemini-3.5-flash", effort: "high", want: "gemini-3.5-flash-high"},
		{model: "gemini-3.8-flash", effort: "medium", want: "gemini-3.8-flash-medium"},
		{model: "gemini-3.8-flash-high", effort: "low", want: "gemini-3.8-flash-high"},
	} {
		effort := tt.effort
		if got := applyGeminiRuntimeEffort(tt.model, &effort); got != tt.want {
			t.Errorf("applyGeminiRuntimeEffort(%q, %q) = %q, want %q", tt.model, tt.effort, got, tt.want)
		}
	}
}

func TestResolveRuntimeModelUsesDisplayFamilyForPreview(t *testing.T) {
	models := map[string]antigravity.ModelInfo{
		"gemini-3.1-pro-low": {DisplayName: "Gemini 3.1 Pro (Low)"},
	}
	got, ok := resolveRuntimeModel(models, "gemini-3.1-pro-preview-customtools")
	if !ok || got != "gemini-3.1-pro-low" {
		t.Fatalf("preview customtools alias resolved to %q, ok=%v", got, ok)
	}
}

func TestResolveRuntimeModelUsesLabelForPreviewRuntime(t *testing.T) {
	models := map[string]antigravity.ModelInfo{
		"gemini-3.1-pro-preview-customtools": {Label: "Gemini 3.1 Pro (Low)"},
	}
	got, ok := resolveRuntimeModel(models, "gemini-3.1-pro")
	if !ok || got != "gemini-3.1-pro-preview-customtools" {
		t.Fatalf("label-based preview alias resolved to %q, ok=%v", got, ok)
	}
}

func TestResolveRuntimeModelFlashDefaultsAndVariants(t *testing.T) {
	models := map[string]antigravity.ModelInfo{
		"gemini-3.8-flash-low":       {DisplayName: "Gemini 3.8 Flash (Low)"},
		"gemini-3.8-flash-medium":    {DisplayName: "Gemini 3.8 Flash (Medium)"},
		"gemini-3.8-flash-high":      {DisplayName: "Gemini 3.8 Flash (High)"},
		"gemini-3.5-flash-extra-low": {DisplayName: "Gemini 3.5 Flash (Low)"},
		"gemini-3.5-flash-low":       {DisplayName: "Gemini 3.5 Flash (Medium)"},
		"gemini-3-flash-agent":       {DisplayName: "Gemini 3.5 Flash (High)"},
	}
	for _, tt := range []struct {
		requested string
		want      string
	}{
		{requested: "gemini-3.8-flash", want: "gemini-3.8-flash-low"},
		{requested: "gemini-3.8-flash-high", want: "gemini-3.8-flash-high"},
		{requested: "gemini-3.5-flash", want: "gemini-3.5-flash-extra-low"},
		{requested: "gemini-3.5-flash-medium", want: "gemini-3.5-flash-low"},
		{requested: "gemini-3.5-flash-high", want: "gemini-3-flash-agent"},
	} {
		got, ok := resolveRuntimeModel(models, tt.requested)
		if !ok || got != tt.want {
			t.Errorf("resolveRuntimeModel(%q) = %q, %v; want %q", tt.requested, got, ok, tt.want)
		}
	}
}

func TestResolveRuntimeModelSupportsEveryAuthorizedFamily(t *testing.T) {
	models := map[string]antigravity.ModelInfo{
		"claude-opus-4-6-thinking": {DisplayName: "Claude Opus 4.6 (Thinking)"},
		"claude-sonnet-4-6":        {DisplayName: "Claude Sonnet 4.6 (Thinking)"},
		"gpt-oss-120b-medium":      {DisplayName: "GPT-OSS 120B (Medium)"},
		"veo-3.1-generate":         {DisplayName: "Veo 3.1"},
	}
	for requested, want := range map[string]string{
		"claude-opus-4-6":   "claude-opus-4-6-thinking",
		"claude-sonnet-4-6": "claude-sonnet-4-6",
		"gpt-oss-120b":      "gpt-oss-120b-medium",
		"veo-3.1-generate":  "veo-3.1-generate",
	} {
		got, ok := resolveRuntimeModel(models, requested)
		if !ok || got != want {
			t.Errorf("resolveRuntimeModel(%q) = %q, %v; want %q", requested, got, ok, want)
		}
	}
}

func TestResolveRuntimeModelRejectsAmbiguousDisplayNameFragments(t *testing.T) {
	models := map[string]antigravity.ModelInfo{
		"claude-opus-4-6-thinking": {DisplayName: "Claude Opus 4.6 (Thinking)"},
		"gemini-3.1-pro-low":       {DisplayName: "Gemini 3.1 Pro (Low)"},
	}
	for _, requested := range []string{"claude", "pro"} {
		if got, ok := resolveRuntimeModel(models, requested); ok {
			t.Fatalf("resolveRuntimeModel(%q) unexpectedly resolved to %q", requested, got)
		}
	}
}

func TestBuildGeminiCodeAssistRequestBody(t *testing.T) {
	body, err := buildGeminiCodeAssistRequestBody(" project-1 ", "gemini-pro-agent", []byte(`{"contents":[{"role":"user"}]}`))
	if err != nil {
		t.Fatalf("build envelope: %v", err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	for key, want := range map[string]string{
		"project":     "project-1",
		"model":       "gemini-pro-agent",
		"userAgent":   "antigravity",
		"requestType": "agent",
	} {
		if got, _ := envelope[key].(string); got != want {
			t.Errorf("envelope[%q] = %q, want %q", key, got, want)
		}
	}
	requestID, _ := envelope["requestId"].(string)
	if !strings.HasPrefix(requestID, "agent/") || len(strings.Split(requestID, "/")) != 5 {
		t.Fatalf("requestId = %q, want agent/<id>/<timestamp>/<trajectory>/<step>", requestID)
	}
	request, ok := envelope["request"].(map[string]any)
	if !ok {
		t.Fatalf("request envelope missing decoded request object")
	}
	sessionID, ok := request["sessionId"].(string)
	if !ok || strings.TrimSpace(sessionID) == "" {
		t.Fatal("request sessionId is empty")
	}
	labels, ok := request["labels"].(map[string]any)
	if !ok || labels["trajectory_id"] == "" || labels["model_enum"] != "MODEL_PLACEHOLDER_M16" {
		t.Fatalf("request labels = %#v", request["labels"])
	}
}

func TestCollapseGeminiCodeAssistModels(t *testing.T) {
	models := collapseGeminiCodeAssistModels(map[string]antigravity.ModelInfo{
		"gemini-pro-agent":        {DisplayName: "Gemini 3.1 Pro (High)"},
		"gemini-3.1-pro-low":      {DisplayName: "Gemini 3.1 Pro (Low)"},
		"gemini-3.5-flash-medium": {DisplayName: "Gemini 3.5 Flash (Medium)"},
		"gemini-3.5-flash-tiered": {DisplayName: "Gemini 3.5 Flash"},
		"gemini-3.1-flash-image":  {DisplayName: "Gemini 3.1 Flash Image"},
		"claude-sonnet-4-6":       {DisplayName: "Claude Sonnet"},
		"MODEL_PLACEHOLDER_M16":   {DisplayName: "placeholder"},
	})
	if len(models) != 4 {
		t.Fatalf("collapsed model count = %d, want 4", len(models))
	}
	if models[0].ID != "claude-sonnet-4-6" || models[1].ID != "gemini-3.1-flash-image" || models[2].ID != "gemini-3.1-pro" || models[3].ID != "gemini-3.5-flash" {
		t.Fatalf("collapsed models = %#v", models)
	}
}

func TestGeminiCodeAssistCatalogAppliesWhitelistAndAliases(t *testing.T) {
	resolver := NewGeminiCodeAssistModelResolver()
	resolver.fetchModels = func(context.Context, *Account, string) (map[string]antigravity.ModelInfo, error) {
		return map[string]antigravity.ModelInfo{
			"claude-opus-4-6-thinking": {DisplayName: "Claude Opus 4.6 (Thinking)"},
			"gemini-3.1-pro-low":       {DisplayName: "Gemini 3.1 Pro (Low)"},
			"gpt-oss-120b-medium":      {DisplayName: "GPT-OSS 120B (Medium)"},
		}, nil
	}
	account := &Account{
		ID:       95,
		Platform: PlatformGemini,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"oauth_type": "antigravity",
			"project_id": "project-95",
			"model_mapping": map[string]any{
				"gemini-3.1-pro": "gemini-3.1-pro",
				"my-opus":        "claude-opus-4-6",
				"missing":        "not-authorized",
			},
		},
	}

	exposed, err := resolver.List(context.Background(), account, "token")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(exposed) != 2 || exposed[0].ID != "gemini-3.1-pro" || exposed[1].ID != "my-opus" {
		t.Fatalf("List() = %#v", exposed)
	}

	authorized, err := resolver.ListAuthorized(context.Background(), account, "token", false)
	if err != nil {
		t.Fatalf("ListAuthorized() error = %v", err)
	}
	if len(authorized) != 3 || authorized[0].ID != "claude-opus-4-6" || authorized[1].ID != "gemini-3.1-pro" || authorized[2].ID != "gpt-oss-120b" {
		t.Fatalf("ListAuthorized() = %#v", authorized)
	}

	if account.IsModelSupported("gemini-3.8-flash") {
		t.Fatal("explicit Gemini OAuth model_mapping must reject an unlisted Gemini model")
	}
}

func TestGeminiCodeAssistCatalogSupportsAntigravityAccounts(t *testing.T) {
	resolver := NewGeminiCodeAssistModelResolver()
	resolver.fetchModels = func(context.Context, *Account, string) (map[string]antigravity.ModelInfo, error) {
		return map[string]antigravity.ModelInfo{
			"gemini-3.1-flash-image": {DisplayName: "Gemini 3.1 Flash Image"},
		}, nil
	}
	account := &Account{
		ID:       94,
		Platform: PlatformGemini,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"oauth_type": "antigravity",
			"project_id": "project-94",
		},
	}

	models, err := resolver.List(context.Background(), account, "token")
	if err != nil || len(models) != 1 || models[0].ID != "gemini-3.1-flash-image" {
		t.Fatalf("Antigravity List() = %#v, %v", models, err)
	}
}

func TestGeminiCodeAssistAuthorizedSyncCanForceRefresh(t *testing.T) {
	resolver := NewGeminiCodeAssistModelResolver()
	calls := 0
	resolver.fetchModels = func(context.Context, *Account, string) (map[string]antigravity.ModelInfo, error) {
		calls++
		return map[string]antigravity.ModelInfo{
			"gemini-3.1-pro-low": {DisplayName: "Gemini 3.1 Pro (Low)"},
		}, nil
	}
	account := &Account{
		ID: 96, Platform: PlatformGemini, Type: AccountTypeOAuth,
		Credentials: map[string]any{"oauth_type": GeminiOAuthTypeCodeAssist, "project_id": "project-96"},
	}

	_, err := resolver.ListAuthorized(context.Background(), account, "token", false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolver.ListAuthorized(context.Background(), account, "token", false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolver.ListAuthorized(context.Background(), account, "token", true)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("fetch calls = %d, want 2", calls)
	}
}

func TestGeminiTokenProviderDetectsProjectOnTokenCacheHit(t *testing.T) {
	codeAssist := &projectDetectCodeAssistStub{}
	provider := NewGeminiTokenProvider(nil, &cachedGeminiTokenStub{token: "cached-token"}, &GeminiOAuthService{codeAssist: codeAssist})
	account := &Account{
		ID:       42,
		Platform: PlatformGemini,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"oauth_type": "code_assist",
		},
	}
	token, err := provider.GetAccessToken(context.Background(), account)
	if err != nil || token != "cached-token" {
		t.Fatalf("GetAccessToken() = %q, %v", token, err)
	}
	if got := account.GetCredential("project_id"); got != "detected-project" {
		t.Fatalf("project_id = %q, want detected-project", got)
	}
	if codeAssist.calls != 1 {
		t.Fatalf("LoadCodeAssist calls = %d, want 1", codeAssist.calls)
	}
}

func TestGeminiTokenProviderProjectDetectionSurvivesCallerCancellation(t *testing.T) {
	codeAssist := &blockingProjectDetectCodeAssistStub{
		entered: make(chan struct{}),
		release: make(chan struct{}),
		done:    make(chan struct{}),
	}
	provider := NewGeminiTokenProvider(nil, &cachedGeminiTokenStub{token: "cached-token"}, &GeminiOAuthService{codeAssist: codeAssist})
	account := &Account{
		ID:       43,
		Platform: PlatformGemini,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"oauth_type": "code_assist",
		},
	}

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		_, _ = provider.GetAccessToken(requestCtx, account)
	}()
	<-codeAssist.entered
	cancelRequest()
	<-requestDone
	close(codeAssist.release)

	select {
	case <-codeAssist.done:
	case <-time.After(time.Second):
		t.Fatal("project detection was canceled with the HTTP request")
	}
	if got := codeAssist.calls.Load(); got != 1 {
		t.Fatalf("LoadCodeAssist calls = %d, want 1", got)
	}
}

func TestGeminiCodeAssistCatalogUsesLastKnownGoodOnRefreshFailure(t *testing.T) {
	resolver := NewGeminiCodeAssistModelResolver()
	now := time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)
	resolver.now = func() time.Time { return now }
	fetchErr := false
	calls := 0
	resolver.fetchModels = func(context.Context, *Account, string) (map[string]antigravity.ModelInfo, error) {
		calls++
		if fetchErr {
			return nil, errors.New("temporary discovery failure")
		}
		return map[string]antigravity.ModelInfo{
			"gemini-pro-agent": {DisplayName: "Gemini 3.1 Pro (High)"},
		}, nil
	}
	account := &Account{
		ID:       91,
		Platform: PlatformGemini,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"oauth_type": "code_assist",
			"project_id": "project-91",
		},
	}

	models, err := resolver.List(context.Background(), account, "token")
	if err != nil || len(models) != 1 || models[0].ID != "gemini-3.1-pro" {
		t.Fatalf("initial List() = %#v, %v", models, err)
	}

	now = now.Add(geminiCodeAssistCatalogTTL + time.Second)
	fetchErr = true
	models, err = resolver.List(context.Background(), account, "token")
	if err == nil || len(models) != 1 || models[0].ID != "gemini-3.1-pro" {
		t.Fatalf("stale List() = %#v, %v", models, err)
	}

	now = now.Add(geminiCodeAssistCatalogStaleTTL)
	models, err = resolver.List(context.Background(), account, "token")
	if err == nil || len(models) != 0 {
		t.Fatalf("expired List() = %#v, %v", models, err)
	}
	if calls != 3 {
		t.Fatalf("fetch calls = %d, want 3", calls)
	}
}

func TestGeminiCodeAssistCatalogCoalescesConcurrentFetches(t *testing.T) {
	resolver := NewGeminiCodeAssistModelResolver()
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	resolver.fetchModels = func(context.Context, *Account, string) (map[string]antigravity.ModelInfo, error) {
		calls.Add(1)
		once.Do(func() { close(entered) })
		<-release
		return map[string]antigravity.ModelInfo{
			"gemini-3.1-pro-low": {DisplayName: "Gemini 3.1 Pro (Low)"},
		}, nil
	}
	account := &Account{
		ID:       92,
		Platform: PlatformGemini,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"oauth_type": "code_assist",
			"project_id": "project-92",
		},
	}

	const workers = 8
	errCh := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			model, err := resolver.Resolve(context.Background(), account, "token", "gemini-3.1-pro")
			if err == nil && model != "gemini-3.1-pro-low" {
				err = errors.New("unexpected resolved model: " + model)
			}
			errCh <- err
		}()
	}
	<-entered
	close(release)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("fetch calls = %d, want 1", got)
	}
}

func TestGeminiCodeAssistResolverRejectsColdDiscoveryFailure(t *testing.T) {
	resolver := NewGeminiCodeAssistModelResolver()
	resolver.fetchModels = func(context.Context, *Account, string) (map[string]antigravity.ModelInfo, error) {
		return nil, errors.New("catalog unavailable")
	}
	account := &Account{
		ID:          93,
		Platform:    PlatformGemini,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"oauth_type": "code_assist", "project_id": "project-93"},
	}
	ctx := WithRequestedReasoningEffort(context.Background(), "high")
	runtimeModel, err := resolver.Resolve(ctx, account, "token", "gemini-3.1-pro")
	if err == nil || runtimeModel != "" || !strings.Contains(err.Error(), "catalog unavailable") {
		t.Fatalf("Resolve() = %q, %v; want catalog error", runtimeModel, err)
	}
}
