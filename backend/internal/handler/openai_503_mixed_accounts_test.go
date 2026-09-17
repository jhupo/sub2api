package handler

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type openAI503MixedAccountUpstream struct {
	service.HTTPUpstream

	mu           sync.Mutex
	failingID    int64
	accountTypes map[int64]string
	accountIDs   []int64
	bodies       map[int64][][]byte
}

func (u *openAI503MixedAccountUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	var body []byte
	if req != nil && req.Body != nil {
		body, _ = io.ReadAll(req.Body)
		_ = req.Body.Close()
		req.Body = io.NopCloser(bytes.NewReader(body))
	}
	u.mu.Lock()
	u.accountIDs = append(u.accountIDs, accountID)
	if u.bodies == nil {
		u.bodies = make(map[int64][][]byte)
	}
	u.bodies[accountID] = append(u.bodies[accountID], append([]byte(nil), body...))
	accountType := u.accountTypes[accountID]
	u.mu.Unlock()

	if accountID == u.failingID {
		return &http.Response{
			StatusCode: http.StatusServiceUnavailable,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(
				`{"error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}`,
			)),
		}, nil
	}

	completed := `{"id":"resp_mixed_503_ok","object":"response","model":"gpt-5.2","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
	if accountType == service.AccountTypeOAuth {
		completed = "data: {\"type\":\"response.completed\",\"response\":" + completed + "}\n\n"
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:       io.NopCloser(strings.NewReader(completed)),
		}, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(completed)),
	}, nil
}

func (u *openAI503MixedAccountUpstream) snapshot(accountID int64) ([]int64, [][]byte) {
	u.mu.Lock()
	defer u.mu.Unlock()
	ids := append([]int64(nil), u.accountIDs...)
	bodies := make([][]byte, len(u.bodies[accountID]))
	for i := range u.bodies[accountID] {
		bodies[i] = append([]byte(nil), u.bodies[accountID][i]...)
	}
	return ids, bodies
}

func TestOpenAI503MixedOAuthAndPoolAccountsRetryThenFailover(t *testing.T) {
	tests := []struct {
		name       string
		firstType  string
		secondType string
	}{
		{name: "oauth_to_api_pool", firstType: service.AccountTypeOAuth, secondType: service.AccountTypeAPIKey},
		{name: "api_pool_to_oauth", firstType: service.AccountTypeAPIKey, secondType: service.AccountTypeOAuth},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			const (
				firstID  int64 = 9930
				secondID int64 = 9931
			)
			newAccount := func(id int64, accountType string, priority int) service.Account {
				account := service.Account{
					ID: id, Name: accountType, Platform: service.PlatformOpenAI, Type: accountType,
					Status: service.StatusActive, Schedulable: true, Priority: priority, Concurrency: 1,
				}
				if accountType == service.AccountTypeOAuth {
					account.Credentials = map[string]any{
						"access_token":       "oauth-token",
						"chatgpt_account_id": "chatgpt-account",
					}
					return account
				}
				account.Credentials = map[string]any{
					"api_key":                      "sk-pool",
					"base_url":                     "https://api.example.test",
					"pool_mode":                    true,
					"pool_mode_retry_count":        float64(1),
					"pool_mode_retry_status_codes": []any{float64(http.StatusServiceUnavailable)},
				}
				account.Extra = map[string]any{"openai_passthrough": true}
				return account
			}

			accounts := []service.Account{
				newAccount(firstID, tt.firstType, 1),
				newAccount(secondID, tt.secondType, 2),
			}
			groupID := int64(4205)
			cfg := &config.Config{RunMode: config.RunModeSimple}
			cfg.Default.RateMultiplier = 1
			cfg.Security.URLAllowlist.Enabled = false
			cfg.Gateway.MaxAccountSwitches = 2

			accountRepo := &openAIWSFailoverHandlerAccountRepoStub{accounts: accounts}
			upstream := &openAI503MixedAccountUpstream{
				failingID: firstID,
				accountTypes: map[int64]string{
					firstID:  tt.firstType,
					secondID: tt.secondType,
				},
			}
			rateLimitSvc := service.NewRateLimitService(accountRepo, nil, cfg, nil, nil)
			billingCacheSvc := service.NewBillingCacheService(nil, nil, nil, nil, nil, cfg, nil)
			t.Cleanup(billingCacheSvc.Stop)
			settingSvc := service.NewSettingService(&contentModerationHandlerSettingRepo{values: map[string]string{
				service.SettingKeyOpenAI503RetrySettings: `{"enabled":true,"retry_delay_seconds":1,"max_same_account_retries":3}`,
			}}, cfg)
			gatewaySvc := service.NewOpenAIGatewayService(
				accountRepo,
				nil,
				nil,
				nil,
				nil,
				nil,
				nil,
				cfg,
				nil,
				nil,
				service.NewBillingService(cfg, nil),
				rateLimitSvc,
				billingCacheSvc,
				upstream,
				&service.DeferredService{},
				nil,
				nil,
				nil,
				nil,
				nil,
				settingSvc,
				nil,
			)
			h := NewOpenAIGatewayHandler(
				gatewaySvc,
				service.NewConcurrencyService(nil),
				billingCacheSvc,
				service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg),
				nil,
				nil,
				nil,
				nil,
				cfg,
			)

			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(`{"model":"gpt-5.2","input":"hello","stream":false}`))
			c.Request.Header.Set("Content-Type", "application/json")
			c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
				ID: 1805, GroupID: &groupID,
				User:  &service.User{ID: 1705, Status: service.StatusActive},
				Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive},
			})
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1705, Concurrency: 0})

			h.Responses(c)

			calls, retryBodies := upstream.snapshot(firstID)
			expectedFirstAttempts := 4
			if tt.firstType == service.AccountTypeAPIKey {
				expectedFirstAttempts = 2
			}
			expectedCalls := make([]int64, expectedFirstAttempts+1)
			for i := range expectedFirstAttempts {
				expectedCalls[i] = firstID
			}
			expectedCalls[expectedFirstAttempts] = secondID
			require.Equal(t, expectedCalls, calls)
			require.Len(t, retryBodies, expectedFirstAttempts)
			for i := 1; i < len(retryBodies); i++ {
				require.Equal(t, retryBodies[0], retryBodies[i], "each same-account retry must replay the identical request")
			}
			require.Equal(t, http.StatusOK, rec.Code)
			require.Equal(t, "resp_mixed_503_ok", gjson.GetBytes(rec.Body.Bytes(), "id").String())
		})
	}
}
