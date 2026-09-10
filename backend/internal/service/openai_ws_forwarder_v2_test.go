package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// HTTP POST /v1/responses -> forwardOpenAIWSV2 keeps the canonical outbound
// tier separate from response.completed.service_tier for usage-time billing.
func TestForwardOpenAIWSV2_KeepsOutboundAndObservedServiceTiersSeparate(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cases := []struct {
		name        string
		requestTier string
		stream      bool
	}{
		{name: "priority_nonstream", requestTier: "priority", stream: false},
		{name: "fast_stream", requestTier: "fast", stream: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			c.Request.Header.Set("User-Agent", "unit-test-agent/1.0")

			cfg := &config.Config{}
			cfg.Security.URLAllowlist.Enabled = false
			cfg.Security.URLAllowlist.AllowInsecureHTTP = true
			cfg.Gateway.OpenAIWS.Enabled = true
			cfg.Gateway.OpenAIWS.APIKeyEnabled = true
			cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
			cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
			cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
			cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
			cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
			cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
			cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 5
			cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

			captureConn := &openAIWSCaptureConn{
				events: [][]byte{
					[]byte(`{"type":"response.completed","response":{"id":"resp_tier_v2","model":"gpt-5.5","status":"completed","service_tier":"default","usage":{"input_tokens":1,"output_tokens":1}}}`),
				},
			}
			captureDialer := &openAIWSCaptureDialer{conn: captureConn}
			pool := newOpenAIWSConnPool(cfg)
			pool.setClientDialerForTest(captureDialer)

			svc := &OpenAIGatewayService{
				cfg:              cfg,
				httpUpstream:     &httpUpstreamRecorder{},
				cache:            &stubGatewayCache{},
				openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
				toolCorrector:    NewCodexToolCorrector(),
				openaiWSPool:     pool,
			}
			account := &Account{
				ID:          5882,
				Name:        "openai-ws-v2-tier",
				Platform:    PlatformOpenAI,
				Type:        AccountTypeAPIKey,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 1,
				Credentials: map[string]any{"api_key": "sk-test"},
				Extra:       map[string]any{"responses_websockets_v2_enabled": true},
			}

			body := []byte(fmt.Sprintf(
				`{"model":"gpt-5.5","stream":%t,"service_tier":%q,"input":[{"type":"input_text","text":"hi"}]}`,
				tc.stream, tc.requestTier,
			))
			result, err := svc.Forward(context.Background(), c, account, body)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.True(t, result.OpenAIWSMode, "must take HTTP POST → forwardOpenAIWSV2, not HTTP fallback")
			require.Equal(t, tc.stream, result.Stream)
			require.Equal(t, "resp_tier_v2", result.RequestID)
			require.NotNil(t, result.ServiceTier)
			require.Equal(t, "priority", *result.ServiceTier)
			require.Equal(t, "default", result.UpstreamResponseServiceTier)
			require.Equal(t, "priority", captureConn.lastWrite["service_tier"],
				"outbound WS payload still carries the requested Fast tier")
		})
	}
}

func TestForwardOpenAIWSV2_HeartbeatCommitsHeadersWithoutTTFTOrReplay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, rec := newTurnStateTestContext(t, 99, "ws-heartbeat")
	RequireOpenAIResponseHeaders(c)
	cfg := &config.Config{}
	cfg.Gateway.StreamKeepaliveInterval = 1
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 5
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
	conn := &openAIWSCaptureConn{
		readDelays: []time.Duration{1200 * time.Millisecond},
		events:     [][]byte{[]byte(`{"type":"error","error":{"code":"server_error","message":"upstream failed"}}`)},
	}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(&openAIWSCaptureDialer{conn: conn, handshake: http.Header{
		"X-Codex-Turn-State": []string{"state-ws"}, "X-Request-Id": []string{"req-ws"},
	}})
	upstream := &httpUpstreamRecorder{}
	svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream, cache: &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(), openaiWSPool: pool}
	account := &Account{ID: 5899, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
		Status: StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"api_key": "test"}, Extra: map[string]any{"responses_websockets_v2_enabled": true}}
	result, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.5","stream":true,"input":"hello"}`))
	require.Error(t, err)
	if result != nil {
		require.Nil(t, result.FirstTokenMs)
	}
	require.Nil(t, upstream.lastReq, "committed WS protocol headers forbid HTTP replay")
	require.True(t, OpenAIStreamAttemptCommitted(c))
	require.Equal(t, "state-ws", rec.Result().Header.Get("X-Codex-Turn-State"))
	require.Equal(t, "req-ws", rec.Result().Header.Get("X-Request-Id"))
	require.Contains(t, rec.Body.String(), ":\n\n")
}

func TestForwardOpenAIWSV2_CancellationClosesConnectionWithoutReplay(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprintf("partial_%t", partial), func(t *testing.T) {
			c, _ := newTurnStateTestContext(t, 99, "ws-cancel")
			ctx, cancel := context.WithCancel(c.Request.Context())
			defer cancel()
			c.Request = c.Request.WithContext(ctx)
			cfg := &config.Config{}
			cfg.Gateway.OpenAIWS.Enabled = true
			cfg.Gateway.OpenAIWS.APIKeyEnabled = true
			cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
			cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
			cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 30
			cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
			conn := &openAIWSCaptureConn{readDelays: []time.Duration{time.Hour},
				events: [][]byte{[]byte(`{"type":"response.completed","response":{"id":"not-completed","usage":{"input_tokens":99,"output_tokens":99}}}`)}}
			if partial {
				conn.readDelays = append([]time.Duration{0}, conn.readDelays...)
				conn.events = append([][]byte{[]byte(`{"type":"response.output_text.done","text":"hello","usage":{"input_tokens":12,"output_tokens":3}}`)}, conn.events...)
			}
			pool := newOpenAIWSConnPool(cfg)
			pool.setClientDialerForTest(&openAIWSCaptureDialer{conn: conn})
			upstream := &httpUpstreamRecorder{}
			svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream, cache: &stubGatewayCache{},
				openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(), openaiWSPool: pool}
			account := &Account{ID: 5999, Platform: PlatformOpenAI, Type: AccountTypeAPIKey,
				Status: StatusActive, Schedulable: true, Concurrency: 1,
				Credentials: map[string]any{"api_key": "test"}, Extra: map[string]any{"responses_websockets_v2_enabled": true}}
			type outcome struct {
				result *OpenAIForwardResult
				err    error
			}
			done := make(chan outcome, 1)
			go func() {
				result, err := svc.Forward(ctx, c, account, []byte(`{"model":"gpt-5.5","stream":true,"input":"hello"}`))
				done <- outcome{result, err}
			}()
			require.Eventually(t, func() bool {
				conn.mu.Lock()
				defer conn.mu.Unlock()
				return len(conn.events) == 0
			}, 3*time.Second, time.Millisecond)
			cancel()
			select {
			case got := <-done:
				require.ErrorIs(t, got.err, context.Canceled)
				require.NotNil(t, got.result)
				require.True(t, got.result.ClientDisconnect)
				require.False(t, got.result.SucceededForScheduling())
				if partial {
					require.Equal(t, 12, got.result.Usage.InputTokens)
				}
				require.Nil(t, upstream.lastReq, "cancellation must not start an HTTP retry")
				conn.mu.Lock()
				closed, writes := conn.closed, len(conn.writes)
				conn.mu.Unlock()
				require.True(t, closed, "a canceled turn cannot return its connection to the pool")
				require.Equal(t, 1, writes)
			case <-time.After(3 * time.Second):
				t.Fatal("canceled upstream did not stop promptly")
			}
		})
	}
}
