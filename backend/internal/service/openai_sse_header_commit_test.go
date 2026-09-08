package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type codexHeartbeatTestWriter struct {
	gin.ResponseWriter
	once    sync.Once
	flushed chan struct{}
}

func (w *codexHeartbeatTestWriter) Flush() {
	w.ResponseWriter.Flush()
	w.once.Do(func() { close(w.flushed) })
}

func TestOpenAIStreamHeartbeatCommitsStateWithoutSemanticOutput(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "passthrough"}[passthrough], func(t *testing.T) {
			c, recorder := newTurnStateTestContext(t, 11, "heartbeat")
			RequireOpenAIResponseHeaders(c)
			writer := &codexHeartbeatTestWriter{ResponseWriter: c.Writer, flushed: make(chan struct{})}
			c.Writer = writer
			reader, upstream := io.Pipe()
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer upstream.Close()
				_, _ = io.WriteString(upstream, "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-test\"}}\n\n")
				select {
				case <-writer.flushed:
				case <-time.After(4 * time.Second):
					return
				}
				_, _ = io.WriteString(upstream, "event: response.failed\ndata: {\"type\":\"response.failed\",\"response\":{\"id\":\"resp-test\",\"error\":{\"code\":\"server_is_overloaded\",\"message\":\"Our servers are currently overloaded\"}}}\n\n")
			}()
			svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{StreamKeepaliveInterval: 1}}}
			resp := &http.Response{StatusCode: 200, Header: http.Header{"X-Codex-Turn-State": []string{"state-B"}, "X-Request-Id": []string{"req-B"}}, Body: reader}
			account := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
			var err error
			if passthrough {
				result, resultErr := svc.handleStreamingResponsePassthrough(context.Background(), resp, c, account, time.Now(), "gpt-5.6-sol", "gpt-5.6-sol")
				err = resultErr
				require.NotNil(t, result)
				require.Nil(t, result.firstTokenMs)
			} else {
				result, resultErr := svc.handleStreamingResponse(context.Background(), resp, c, account, time.Now(), "gpt-5.6-sol", "gpt-5.6-sol")
				err = resultErr
				require.NotNil(t, result)
				require.Nil(t, result.firstTokenMs)
			}
			_ = reader.Close()
			<-done
			require.Error(t, err)
			var failover *UpstreamFailoverError
			require.False(t, errors.As(err, &failover), "committed protocol state forbids replay")
			require.Equal(t, "state-B", recorder.Result().Header.Get("X-Codex-Turn-State"))
			require.Equal(t, "req-B", recorder.Result().Header.Get("X-Request-Id"))
			require.Contains(t, recorder.Result().Header.Get("Cache-Control"), "no-transform")
			require.Contains(t, recorder.Body.String(), "response.failed")
			require.True(t, OpenAIStreamAttemptCommitted(c))
		})
	}
}

func TestCodexTurnStateProvenanceSurvivesOutOfOrderCompletion(t *testing.T) {
	svc := &OpenAIGatewayService{}
	c, _ := newTurnStateTestContext(t, 11, "same-session")
	svc.noteOpenAICodexTurnStateProvenance(c, &Account{ID: 2}, "new-B")
	svc.noteOpenAICodexTurnStateProvenance(c, &Account{ID: 1}, "late-A")
	for _, test := range []struct {
		state   string
		account int64
		keep    bool
	}{
		{"new-B", 2, true}, {"late-A", 1, true}, {"late-A", 2, false}, {"new-B", 1, false},
	} {
		headers := http.Header{"X-Codex-Turn-State": []string{test.state}}
		svc.guardOpenAICodexTurnStateEcho(c, &Account{ID: test.account}, headers)
		require.Equal(t, test.keep, headers.Get("X-Codex-Turn-State") != "")
	}
}
