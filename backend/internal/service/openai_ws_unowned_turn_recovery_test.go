package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIWSUnownedTurnRecoveryPreservesRequestAndSafetyGates(t *testing.T) {
	for _, tc := range []struct {
		name             string
		recoveryDisabled bool
		toolOutput       bool
		noReplay         bool
	}{
		{name: "fresh_success_keeps_anchor_and_cache"},
		{name: "tool_output_does_not_replay", toolOutput: true},
		{name: "disabled_recovery_fails_closed", recoveryDisabled: true},
		{name: "missing_replay_context_fails_closed", noReplay: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			svc, account, _ := newWSReuseBoundaryFixture(t)
			// Use the pooled direct-WS path with strict store=false continuation.
			account.Type = AccountTypeAPIKey
			account.Credentials = map[string]any{"api_key": "test-key"}
			svc.cfg.Gateway.OpenAIWS.IngressPreviousResponseRecoveryEnabled = !tc.recoveryDisabled
			firstConn := &openAIWSCaptureConn{events: [][]byte{
				[]byte(`{"type":"response.completed","response":{"id":"resp_first","usage":{"input_tokens":1}}}`),
				[]byte(`{"type":"error","error":{"code":"previous_response_not_found","message":"unowned"}}`),
			}}
			freshConn := &openAIWSCaptureConn{events: [][]byte{
				[]byte(`{"type":"response.completed","response":{"id":"resp_second","usage":{"input_tokens":7}}}`),
			}}
			dialer := &openAIWSQueueDialer{conns: []openAIWSClientConn{firstConn, freshConn}}
			svc.openaiWSPool.setClientDialerForTest(dialer)
			var results []*OpenAIForwardResult
			hooks := &OpenAIWSIngressHooks{AfterTurn: func(_ int, result *OpenAIForwardResult, _ error) {
				if result != nil {
					results = append(results, result)
				}
			}}
			serverErrs := make(chan error, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := coderws.Accept(w, r, nil)
				if err != nil {
					serverErrs <- err
					return
				}
				defer conn.CloseNow()
				ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
				defer cancel()
				_, first, err := conn.Read(ctx)
				if err != nil {
					serverErrs <- err
					return
				}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = r
				serverErrs <- svc.ProxyResponsesWebSocketFromClient(ctx, c, conn, account, "test-key", first, hooks)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
			require.NoError(t, err)
			defer client.CloseNow()
			firstInput := `,"input":[{"type":"input_text","text":"hello"}]`
			secondInput := `,"input":[{"type":"input_text","text":"world"}]`
			if tc.noReplay {
				firstInput, secondInput = "", ""
			}
			if tc.toolOutput {
				secondInput = `,"input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]`
			}
			first := `{"type":"response.create","model":"gpt-5.1","store":false,"prompt_cache_key":"stable-cache"` + firstInput + `}`
			second := `{"type":"response.create","model":"gpt-5.1","store":false,"prompt_cache_key":"stable-cache","previous_response_id":"resp_first"` + secondInput + `}`
			require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(first)))
			_, payload, err := client.Read(ctx)
			require.NoError(t, err)
			require.Equal(t, "resp_first", gjson.GetBytes(payload, "response.id").String())
			require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(second)))
			_, payload, err = client.Read(ctx)
			denied := tc.recoveryDisabled || tc.toolOutput || tc.noReplay
			if denied {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, "resp_second", gjson.GetBytes(payload, "response.id").String())
				require.NoError(t, client.Close(coderws.StatusNormalClosure, "done"))
			}
			select {
			case serverErr := <-serverErrs:
				if denied {
					require.ErrorIs(t, serverErr, errOpenAIWSUnownedTurnStart)
				} else {
					require.NoError(t, serverErr)
				}
			case <-ctx.Done():
				t.Fatal("server did not finish")
			}
			if denied {
				require.Equal(t, 1, dialer.DialCount())
				require.Len(t, results, 1, "unowned errors must not generate a usage result")
			} else {
				require.Equal(t, 2, dialer.DialCount())
				require.Len(t, results, 2)
				require.Equal(t, 7, results[1].Usage.InputTokens)
				require.Len(t, firstConn.writes, 2)
				require.Len(t, freshConn.writes, 1)
				require.Equal(t, firstConn.writes[1], freshConn.writes[0], "fresh confirmation must not strip the anchor or remap the cache key")
			}
		})
	}
}
