package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAI503HTTPAndSSEShareRetryBudget(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(map[bool]string{false: "native", true: "passthrough"}[passthrough], func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			body := []byte(`{"model":"gpt-5.5","stream":true,"instructions":"test instructions","input":"hello"}`)
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
			overload := `{"type":"error","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}`
			upstream := &httpUpstreamRecorder{responses: []*http.Response{
				{StatusCode: 503, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(overload))},
				{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + overload + "\n\n"))},
			}}
			svc := &OpenAIGatewayService{
				cfg:          &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}},
				httpUpstream: upstream,
				settingService: &SettingService{settingRepo: &openAIAdvancedSchedulerSettingRepoStub{values: map[string]string{
					SettingKeyOpenAI503RetrySettings: `{"enabled":true,"retry_delay_seconds":1,"max_same_account_retries":1}`,
				}}},
			}
			account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 5,
				Credentials: map[string]any{"access_token": "test-token", "chatgpt_account_id": "test-account"}}
			var err error
			if passthrough {
				_, err = svc.forwardOpenAIPassthrough(context.Background(), c, account, body, body, "gpt-5.5", false, nil, true, time.Now())
			} else {
				_, err = svc.Forward(context.Background(), c, account, body)
			}
			var failure *UpstreamFailoverError
			require.ErrorAs(t, err, &failure)
			require.Equal(t, 503, failure.StatusCode)
			require.True(t, failure.OpenAI503QueueHandled)
			require.False(t, failure.RetryableOnSameAccount)
			require.Len(t, upstream.requests, 2, "SSE must not start another independent retry budget")
			require.Equal(t, upstream.bodies[0], upstream.bodies[1])
			require.Equal(t, upstream.requests[0].Header.Get("User-Agent"), upstream.requests[1].Header.Get("User-Agent"))
			require.False(t, c.Writer.Written())
			require.Zero(t, queueLength(svc.getOpenAI503RetryQueue(), account.ID))
		})
	}
}

func TestOpenAI503RetriesDoNotConsumeGenericTurnBudget(t *testing.T) {
	gin.SetMode(gin.TestMode)
	retryBody := `{"type":"error","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded"}}`
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader(retryBody))},
		{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader(retryBody))},
		{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader(retryBody))},
		{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"ok":true}`))},
	}}
	ctx := withOpenAIRequestAttemptBudget(context.Background())
	svc := &OpenAIGatewayService{
		httpUpstream: upstream,
		settingService: &SettingService{settingRepo: &openAIAdvancedSchedulerSettingRepoStub{values: map[string]string{
			SettingKeyOpenAI503RetrySettings: `{"enabled":true,"retry_delay_seconds":0,"max_same_account_retries":3}`,
		}}},
	}
	account := &Account{ID: 901, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
		Credentials: map[string]any{"access_token": "test-token", "chatgpt_account_id": "test-account"}}
	state, err := svc.newOpenAI503RetryState(ctx, account)
	require.NoError(t, err)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())

	resp, err := state.do(c, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/responses", strings.NewReader(`{"model":"gpt-5.5"}`))
	}, "")
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, upstream.requests, 4, "three configured 503 retries must allow four total upstream attempts")
	budget, _ := ctx.Value(openAIRequestAttemptKey{}).(*openAIRequestAttempts)
	require.NotNil(t, budget)
	budget.mu.Lock()
	require.Equal(t, 1, budget.count, "503 retries must not consume the generic request budget")
	budget.mu.Unlock()
}

func TestOpenAI503PoolAccountBypassesDedicatedRetry(t *testing.T) {
	retryBody := `{"error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded"}}`
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader(retryBody))},
	}}
	svc := &OpenAIGatewayService{
		httpUpstream: upstream,
		settingService: &SettingService{settingRepo: &openAIAdvancedSchedulerSettingRepoStub{values: map[string]string{
			SettingKeyOpenAI503RetrySettings: `{"enabled":true,"retry_delay_seconds":0,"max_same_account_retries":3}`,
		}}},
	}
	account := &Account{ID: 906, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{
			"api_key":                      "sk-pool",
			"pool_mode":                    true,
			"pool_mode_retry_count":        float64(7),
			"pool_mode_retry_status_codes": []any{float64(http.StatusServiceUnavailable)},
		}}
	state, err := svc.newOpenAI503RetryState(context.Background(), account)
	require.NoError(t, err)

	resp, err := state.do(nil, func() (*http.Request, error) {
		return http.NewRequest(http.MethodPost, "https://api.openai.com/v1/responses", strings.NewReader(`{"model":"gpt-5.5"}`))
	}, "")

	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)
	require.Len(t, upstream.requests, 1, "API pool retries belong to the handler's original pool-mode loop")
	require.Zero(t, state.retries)
}

func TestOpenAI503MixedHTTPAndSSEFailuresUseOneConfiguredBudget(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"gpt-5.5","stream":true,"input":"hello"}`)
	overload := `{"type":"error","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded"}}`
	completed := `data: {"type":"response.completed","response":{"id":"resp_mixed_retry","object":"response","model":"gpt-5.5","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}` + "\n\n"
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader(overload))},
		{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + overload + "\n\n"))},
		{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader("data: " + overload + "\n\n"))},
		{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(completed))},
	}}
	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}},
		httpUpstream: upstream,
		settingService: &SettingService{settingRepo: &openAIAdvancedSchedulerSettingRepoStub{values: map[string]string{
			SettingKeyOpenAI503RetrySettings: `{"enabled":true,"retry_delay_seconds":0,"max_same_account_retries":3}`,
		}}},
	}
	account := &Account{ID: 905, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
		Credentials: map[string]any{"access_token": "test-token", "chatgpt_account_id": "test-account"}}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(string(body)))
	ctx := withOpenAIRequestAttemptBudget(context.Background())

	result, err := svc.Forward(ctx, c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.requests, 4)
	budget, _ := ctx.Value(openAIRequestAttemptKey{}).(*openAIRequestAttempts)
	budget.mu.Lock()
	require.Equal(t, 1, budget.count)
	budget.mu.Unlock()
}

func TestOpenAI503CompactUsesConfiguredRetries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	retryBody := `{"error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded"}}`
	completed := `{"id":"resp_compact_retry","object":"response","model":"gpt-5.5","status":"completed","output":[{"type":"compaction","encrypted_content":"compact-ok"}],"usage":{"input_tokens":4,"output_tokens":1,"total_tokens":5}}`
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader(retryBody))},
		{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader(retryBody))},
		{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader(retryBody))},
		{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(completed))},
	}}
	svc := &OpenAIGatewayService{
		cfg:          &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}},
		httpUpstream: upstream,
		settingService: &SettingService{settingRepo: &openAIAdvancedSchedulerSettingRepoStub{values: map[string]string{
			SettingKeyOpenAI503RetrySettings: `{"enabled":true,"retry_delay_seconds":0,"max_same_account_retries":3}`,
		}}},
	}
	body := []byte(`{"model":"gpt-5.5","stream":false,"input":[{"type":"message","role":"user","content":"compact this"}]}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(string(body)))
	account := &Account{ID: 902, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
		Credentials: map[string]any{"access_token": "test-token", "chatgpt_account_id": "test-account"}}

	result, err := svc.Forward(withOpenAIRequestAttemptBudget(context.Background()), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.requests, 4)
	require.Equal(t, chatgptCodexURL+"/compact", upstream.lastReq.URL.String())
	for i := 1; i < len(upstream.bodies); i++ {
		require.Equal(t, upstream.bodies[0], upstream.bodies[i], "503 retries must rebuild the same compact request")
	}
}

func TestOpenAI503AlphaSearchUsesConfiguredRetries(t *testing.T) {
	gin.SetMode(gin.TestMode)
	retryBody := `{"error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded"}}`
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader(retryBody))},
		{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader(retryBody))},
		{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader(retryBody))},
		{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"output":"search-ok"}`))},
	}}
	svc := &OpenAIGatewayService{
		cfg:          &config.Config{},
		httpUpstream: upstream,
		settingService: &SettingService{settingRepo: &openAIAdvancedSchedulerSettingRepoStub{values: map[string]string{
			SettingKeyOpenAI503RetrySettings: `{"enabled":true,"retry_delay_seconds":0,"max_same_account_retries":3}`,
		}}},
	}
	body := []byte(`{"id":"search-retry","model":"gpt-5.5","commands":{"search_query":[{"q":"news"}]}}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/alpha/search", strings.NewReader(string(body)))
	account := &Account{ID: 903, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 1,
		Credentials: map[string]any{"access_token": "test-token", "chatgpt_account_id": "test-account"}}

	result, err := svc.ForwardAlphaSearch(withOpenAIRequestAttemptBudget(context.Background()), c, account, body)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 1, result.WebSearchCalls)
	require.Len(t, upstream.requests, 4)
	for i := 1; i < len(upstream.bodies); i++ {
		require.Equal(t, upstream.bodies[0], upstream.bodies[i], "503 retries must rebuild the same search request")
	}
}

func TestOpenAI503RetrySessionExemptsOneWSAttempt(t *testing.T) {
	ctx := withOpenAIRequestAttemptBudget(context.Background())
	require.NoError(t, consumeOpenAIRequestAttempt(ctx))
	svc := &OpenAIGatewayService{settingService: &SettingService{settingRepo: &openAIAdvancedSchedulerSettingRepoStub{values: map[string]string{
		SettingKeyOpenAI503RetrySettings: `{"enabled":true,"retry_delay_seconds":0,"max_same_account_retries":1}`,
	}}}}
	session, err := svc.NewOpenAI503RetrySession(ctx, &Account{ID: 904, Platform: PlatformOpenAI, Type: AccountTypeOAuth})
	require.NoError(t, err)
	failure := newOpenAIUpstreamFailoverError(
		http.StatusServiceUnavailable,
		nil,
		[]byte(`{"type":"error","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded"}}`),
		"Our servers are currently overloaded",
		true,
	)

	retry, err := session.HandleStreamError(nil, failure)
	require.True(t, retry)
	require.NoError(t, err)
	retryCtx := session.RetryContext(ctx)
	require.NoError(t, consumeOpenAIRequestAttempt(retryCtx), "the configured 503 retry owns its first WS send")
	require.NoError(t, consumeOpenAIRequestAttempt(retryCtx), "the exemption is one-shot; the next send uses the generic budget")
	budget, _ := ctx.Value(openAIRequestAttemptKey{}).(*openAIRequestAttempts)
	budget.mu.Lock()
	require.Equal(t, 2, budget.count)
	budget.mu.Unlock()
}

func TestOpenAI503StreamNeverReplaysCommittedOutput(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	_, err := c.Writer.Write([]byte("data: partial\n\n"))
	require.NoError(t, err)
	svc := &OpenAIGatewayService{}
	state, err := svc.newOpenAI503RetryState(context.Background(), &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth})
	require.NoError(t, err)
	failure := &UpstreamFailoverError{StatusCode: 503, ResponseBody: []byte(`{"error":{"code":"server_is_overloaded"}}`)}
	retry, got := state.handleStreamError(c, nil, failure)
	require.False(t, retry)
	require.Same(t, failure, got)
	require.Zero(t, state.retries)
}

func TestOpenAI503WSCapacityUsesConfiguredRetryBudget(t *testing.T) {
	svc := &OpenAIGatewayService{settingService: &SettingService{settingRepo: &openAIAdvancedSchedulerSettingRepoStub{
		values: map[string]string{
			SettingKeyOpenAI503RetrySettings: `{"enabled":true,"retry_delay_seconds":0,"max_same_account_retries":1}`,
		},
	}}}
	state, err := svc.newOpenAI503RetryState(context.Background(), &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth})
	require.NoError(t, err)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	failure := newOpenAIUpstreamFailoverError(
		http.StatusServiceUnavailable,
		http.Header{},
		[]byte(`{"type":"error","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded"}}`),
		"Our servers are currently overloaded",
		true,
	)

	retry, retryErr := state.handleStreamError(c, nil, failure)
	require.True(t, retry)
	require.NoError(t, retryErr)
	require.Equal(t, 1, state.retries)

	retry, retryErr = state.handleStreamError(c, nil, failure)
	require.False(t, retry)
	var exhausted *UpstreamFailoverError
	require.ErrorAs(t, retryErr, &exhausted)
	require.True(t, exhausted.OpenAI503QueueHandled)
}

func TestOpenAI503DisabledAndZeroRetryDoNotFallBackToPoolRetries(t *testing.T) {
	for _, value := range []string{
		`{"enabled":false,"max_same_account_retries":3}`,
		`{"enabled":true,"max_same_account_retries":0}`,
	} {
		svc := &OpenAIGatewayService{settingService: &SettingService{settingRepo: &openAIAdvancedSchedulerSettingRepoStub{
			values: map[string]string{SettingKeyOpenAI503RetrySettings: value},
		}}}
		state, err := svc.newOpenAI503RetryState(context.Background(), &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth})
		require.NoError(t, err)
		retry, err := state.retry(nil, 503, nil, []byte(`{"error":{"code":"server_is_overloaded"}}`))
		require.False(t, retry)
		var failure *UpstreamFailoverError
		require.ErrorAs(t, err, &failure)
		require.True(t, failure.OpenAI503QueueHandled)
		require.False(t, failure.RetryableOnSameAccount)
	}
}
