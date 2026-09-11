package service

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestCodexBridgeFinalOutboundIdentity(t *testing.T) {
	for _, protocol := range []string{"chat", "messages"} {
		for _, mode := range []string{"off", "device", "session", "full"} {
			t.Run(protocol+"/"+mode, func(t *testing.T) {
				body := []byte(`{"model":"gpt-5.4","max_tokens":16,"messages":[{"role":"user","content":"hello"}],"stream":false}`)
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+protocol, bytes.NewReader(body))
				c.Request.Header.Set("session-id", "client-session")
				upstream := &httpUpstreamRecorder{resp: openAICompatSSECompletedResponse("resp_identity", "gpt-5.4")}
				svc := &OpenAIGatewayService{cfg: &config.Config{}, httpUpstream: upstream}
				account := newTestOAuthAccount(711, map[string]any{codexFingerprintModeExtraKey: mode, codexFingerprintSeedExtraKey: testCodexFingerprintSeed})
				account.Credentials = map[string]any{"access_token": "test-token", "chatgpt_account_id": "test-account"}
				var err error
				if protocol == "chat" {
					_, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, "cache-key", "gpt-5.4")
				} else {
					_, err = svc.ForwardAsAnthropic(context.Background(), c, account, body, "cache-key", "gpt-5.4")
				}
				require.NoError(t, err)
				require.NotNil(t, upstream.lastReq)
				identity := stagedCodexAttemptIdentity(c, account)
				headers := upstream.lastReq.Header
				if mode == "session" || mode == "full" {
					require.Equal(t, identity.fingerprint.sessionID, headers.Get("session_id"))
					require.Equal(t, headers.Get("session_id"), headers.Get("session-id"))
					require.Equal(t, headers.Get("session_id"), gjson.GetBytes(upstream.lastBody, "client_metadata.session_id").String())
				} else {
					require.Equal(t, identity.bridgeSessionID, headers.Get("session_id"))
				}
			})
		}
	}
}
