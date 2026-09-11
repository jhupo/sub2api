package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type wsFundingTransportConn struct{ *stagedPassthroughConn }

func (c *wsFundingTransportConn) WriteJSON(ctx context.Context, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return c.WriteFrame(ctx, coderws.MessageText, payload)
}

func TestOpenAIWSFundingTransportAuthorizesEveryTurnBeforeSending(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, mode := range []string{OpenAIWSIngressModePassthrough, OpenAIWSIngressModeCtxPool} {
		t.Run(mode, func(t *testing.T) {
			cfg := passthroughLifecycleConfig()
			upstream := newStagedPassthroughConn()
			svc := newPassthroughLifecycleService(cfg, upstream)
			dialer := &stagedPassthroughDialer{conn: &wsFundingTransportConn{upstream}}
			svc.openaiWSPassthroughDialer = dialer
			pool := svc.getOpenAIWSConnPool()
			pool.setClientDialerForTest(dialer)
			defer pool.Close()
			account := passthroughLifecycleAccount()
			account.Extra["openai_apikey_responses_websockets_v2_mode"] = mode
			var authorizations atomic.Int32
			denied := errors.New("subscription daily allowance exhausted")
			hooks := &OpenAIWSIngressHooks{AuthorizeTurn: func(turn int, payload []byte) error {
				authorizations.Add(1)
				if turn == 2 {
					return NewOpenAIWSRequestScopedClientCloseError(coderws.StatusPolicyViolation, "insufficient allowance", denied)
				}
				if gjson.GetBytes(payload, "model").String() == "" {
					return errors.New("model missing at authorization")
				}
				return nil
			}}
			serverErr := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					serverErr <- err
					return
				}
				defer conn.CloseNow()
				_, first, err := conn.Read(r.Context())
				if err != nil {
					serverErr <- err
					return
				}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = r
				serverErr <- svc.ProxyResponsesWebSocketFromClient(r.Context(), c, conn, account, "sk-test", first, hooks)
			}))
			defer server.Close()
			client := dialPassthroughLifecycleClient(t, server)
			defer client.CloseNow()
			requirePassthroughUpstreamWrite(t, upstream, 3*time.Second)
			require.EqualValues(t, 1, authorizations.Load())
			upstream.Send(`{"type":"response.created","response":{"id":"resp_funding_1"}}`)
			upstream.Send(`{"type":"response.completed","response":{"id":"resp_funding_1","usage":{"input_tokens":5,"output_tokens":2}}}`)
			for i := 0; i < 2; i++ {
				_, err := readPassthroughLifecycleFrame(t, client, 3*time.Second)
				require.NoError(t, err)
			}
			writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			err := client.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","input":"second"}`))
			cancel()
			require.NoError(t, err)
			_, closeErr := readPassthroughLifecycleFrame(t, client, 3*time.Second)
			require.Error(t, closeErr)
			select {
			case err := <-serverErr:
				require.ErrorIs(t, err, denied)
			case <-time.After(5 * time.Second):
				t.Fatal("denied turn did not close")
			}
			require.EqualValues(t, 2, authorizations.Load())
			require.Empty(t, upstream.writes, "denied turn must not reach upstream")
		})
	}
}

func TestOpenAIWSHTTPBridgeFundingSeesFinalBodyAndBlocksSend(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(""))}}
	svc := &OpenAIGatewayService{cfg: passthroughLifecycleConfig(), httpUpstream: upstream}
	account := passthroughLifecycleAccount()
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/v1/responses", nil)
	denied := errors.New("wallet unavailable")
	authorize := func(body []byte) error {
		require.False(t, gjson.GetBytes(body, "type").Exists())
		require.False(t, gjson.GetBytes(body, "previous_response_id").Exists())
		require.Equal(t, "hi", gjson.GetBytes(body, "input").String())
		return denied
	}
	payload := []byte(`{"type":"response.create","model":"gpt-5.1","input":"hi"}`)
	result, err := svc.proxyOpenAIWSHTTPBridgeTurn(context.Background(), authorize, c, account, "sk-test", payload, len(payload), "gpt-5.1", "", "", "", "", 1, func([]byte) error { return nil })
	require.ErrorIs(t, err, denied)
	require.Nil(t, result)
	require.Empty(t, upstream.lastBody)
}
