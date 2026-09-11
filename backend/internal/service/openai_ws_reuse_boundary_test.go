package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func newWSReuseBoundaryFixture(t *testing.T, events ...[]byte) (*OpenAIGatewayService, *Account, *openAIWSCaptureDialer) {
	t.Helper()
	cfg := newOpenAIWSV2TestConfig()
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.StoreDisabledConnMode = "off"
	pool := newOpenAIWSConnPool(cfg)
	t.Cleanup(pool.Close)
	dialer := &openAIWSCaptureDialer{conn: &openAIWSCaptureConn{events: events}}
	pool.setClientDialerForTest(dialer)
	svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: &httpUpstreamRecorder{}, cache: &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(), openaiWSPool: pool}
	account := activeCodexFingerprintPoolAccountForTest(405)
	account.Status, account.Schedulable, account.Concurrency = StatusActive, true, 1
	account.Credentials = map[string]any{"access_token": "test-token"}
	account.Extra[codexFingerprintModeExtraKey] = "device"
	account.Extra["responses_websockets_v2_enabled"] = true
	return svc, account, dialer
}

func forwardWSBoundaryRequest(t *testing.T, svc *OpenAIGatewayService, account *Account, keyID int64, session string) *OpenAIForwardResult {
	t.Helper()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("session_id", session)
	c.Set("api_key", &APIKey{ID: keyID})
	result, err := svc.Forward(context.Background(), c, account, []byte(`{"model":"gpt-5.1","stream":false,"input":"hello"}`))
	require.NoError(t, err)
	require.NotNil(t, result)
	return result
}

func TestOpenAIWSReuseAcrossSessionsNormalTerminalBoundary(t *testing.T) {
	svc, account, dialer := newWSReuseBoundaryFixture(t,
		[]byte(`{"type":"response.created","response":{"id":"resp_a","model":"gpt-5.1"}}`),
		[]byte(`{"type":"response.completed","response":{"id":"resp_a","model":"gpt-5.1","usage":{"input_tokens":3,"output_tokens":2}}}`),
		[]byte(`{"type":"response.created","response":{"id":"resp_b","model":"gpt-5.1"}}`),
		[]byte(`{"type":"response.completed","response":{"id":"resp_b","model":"gpt-5.1","usage":{"input_tokens":7,"output_tokens":4}}}`),
	)
	first := forwardWSBoundaryRequest(t, svc, account, 11, "session-a")
	second := forwardWSBoundaryRequest(t, svc, account, 12, "session-b")
	require.Equal(t, "resp_a", first.RequestID)
	require.Equal(t, "resp_b", second.RequestID)
	require.Equal(t, 1, dialer.DialCount())
	require.Equal(t, 3, first.Usage.InputTokens)
	require.Equal(t, 7, second.Usage.InputTokens)
}

func TestOpenAIWSReuseFailedTerminalRetiresLateFrames(t *testing.T) {
	svc, account, dialer := newWSReuseBoundaryFixture(t,
		[]byte(`{"type":"response.failed","response":{"id":"resp_a","error":{"code":"server_error","message":"failed"}}}`),
		[]byte(`{"type":"response.failed","response":{"id":"resp_a","usage":{"input_tokens":999}}}`),
	)
	first := forwardWSBoundaryRequest(t, svc, account, 11, "session-a")
	require.Equal(t, "resp_a", first.RequestID)
	dialer.conn.mu.Lock()
	closed := dialer.conn.closed
	dialer.conn.mu.Unlock()
	require.True(t, closed, "a failed response must retire its socket before another user can acquire it")
	dialer.mu.Lock()
	dialer.conn = &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.created","response":{"id":"resp_b"}}`),
		[]byte(`{"type":"response.completed","response":{"id":"resp_b","usage":{"input_tokens":7,"output_tokens":4}}}`),
	}}
	dialer.mu.Unlock()
	second := forwardWSBoundaryRequest(t, svc, account, 12, "session-b")
	require.Equal(t, "resp_b", second.RequestID)
	require.Equal(t, 7, second.Usage.InputTokens)
	require.Equal(t, 2, dialer.DialCount())
}
