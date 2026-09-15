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

func TestOpenAI503StreamNeverReplaysCommittedOutput(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	_, err := c.Writer.Write([]byte("data: partial\n\n"))
	require.NoError(t, err)
	svc := &OpenAIGatewayService{}
	state, err := svc.newOpenAI503RetryState(context.Background(), &Account{ID: 1, Platform: PlatformOpenAI})
	require.NoError(t, err)
	failure := &UpstreamFailoverError{StatusCode: 503, ResponseBody: []byte(`{"error":{"code":"server_is_overloaded"}}`)}
	retry, got := state.handleStreamError(c, nil, failure)
	require.False(t, retry)
	require.Same(t, failure, got)
	require.Zero(t, state.retries)
}

func TestOpenAI503DisabledAndZeroRetryDoNotFallBackToPoolRetries(t *testing.T) {
	for _, value := range []string{
		`{"enabled":false,"max_same_account_retries":3}`,
		`{"enabled":true,"max_same_account_retries":0}`,
	} {
		svc := &OpenAIGatewayService{settingService: &SettingService{settingRepo: &openAIAdvancedSchedulerSettingRepoStub{
			values: map[string]string{SettingKeyOpenAI503RetrySettings: value},
		}}}
		state, err := svc.newOpenAI503RetryState(context.Background(), &Account{ID: 1, Platform: PlatformOpenAI})
		require.NoError(t, err)
		retry, err := state.retry(nil, 503, nil, []byte(`{"error":{"code":"server_is_overloaded"}}`))
		require.False(t, retry)
		var failure *UpstreamFailoverError
		require.ErrorAs(t, err, &failure)
		require.True(t, failure.OpenAI503QueueHandled)
		require.False(t, failure.RetryableOnSameAccount)
	}
}
