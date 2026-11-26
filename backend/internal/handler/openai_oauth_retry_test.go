package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIResponsesOAuthRetryThenSwitch(t *testing.T) {
	for _, tc := range []struct {
		name        string
		retries     int
		enabled     bool
		passthrough bool
		wantCalls   []int64
		wantStatus  int
	}{
		{"retry_twice_then_switch", 2, true, true, []int64{9910, 9910, 9910, 9911}, http.StatusOK},
		{"zero_switches_immediately", 0, true, true, []int64{9910, 9911}, http.StatusOK},
		{"disabled_preserves_passthrough", 2, false, true, []int64{9910}, http.StatusServiceUnavailable},
		{"standard_forward_retries", 1, true, false, []int64{9910, 9910, 9911}, http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			groupID := int64(4203)
			accounts := []service.Account{
				{ID: 9910, Name: "oauth-retry", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
					Status: service.StatusActive, Schedulable: true, Priority: 1,
					Credentials: map[string]any{
						"access_token": "oauth-token", "chatgpt_account_id": "test-account",
						"openai_oauth_retry_enabled": tc.enabled, "openai_oauth_retry_count": float64(tc.retries),
						"openai_oauth_retry_status_codes": []any{float64(http.StatusServiceUnavailable)},
					}, Extra: map[string]any{"openai_passthrough": tc.passthrough}},
				{ID: 9911, Name: "fallback", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
					Status: service.StatusActive, Schedulable: true, Priority: 2,
					Credentials: map[string]any{"api_key": "sk-fallback", "base_url": "https://api.example.test"},
					Extra:       map[string]any{"openai_passthrough": true}},
			}
			cfg := &config.Config{RunMode: config.RunModeSimple}
			cfg.Default.RateMultiplier = 1
			cfg.Security.URLAllowlist.Enabled = false
			repo := &openAIWSFailoverHandlerAccountRepoStub{accounts: accounts}
			upstream := &openAIHTTPPassthroughAuthFailoverUpstream{statusCode: http.StatusServiceUnavailable}
			billingCache := service.NewBillingCacheService(nil, nil, nil, nil, nil, cfg, nil)
			t.Cleanup(billingCache.Stop)
			gateway := service.NewOpenAIGatewayService(
				repo, nil, nil, nil, nil, nil, nil, cfg, nil, nil, service.NewBillingService(cfg, nil),
				nil, billingCache, upstream, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil,
			)
			h := NewOpenAIGatewayHandler(gateway, service.NewConcurrencyService(nil), billingCache,
				service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg), nil, nil, nil, nil, cfg)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(`{"model":"gpt-5.2","input":"hello","stream":false}`))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
				ID: 1803, GroupID: &groupID, User: &service.User{ID: 1703, Status: service.StatusActive},
				Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive},
			})
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1703})

			h.Responses(c)

			require.Equal(t, tc.wantCalls, upstream.calls())
			require.Equal(t, tc.wantStatus, recorder.Code, recorder.Body.String())
		})
	}
}
