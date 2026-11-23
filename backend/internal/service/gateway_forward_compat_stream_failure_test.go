//go:build unit

package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAnthropicCompatStreamExplicitErrorOverridesTerminalMarker(t *testing.T) {
	for _, terminal := range []string{"stop_reason", "message_stop", "done"} {
		t.Run(terminal, func(t *testing.T) {
			stream := &anthropicCompatStream{body: io.NopCloser(strings.NewReader(""))}
			stream.observe(&apicompat.AnthropicStreamEvent{Type: "message_start"}, "")
			switch terminal {
			case "stop_reason":
				stream.observe(&apicompat.AnthropicStreamEvent{
					Type: "message_delta", Delta: &apicompat.AnthropicDelta{StopReason: "end_turn"},
				}, "")
			case "message_stop":
				stream.observe(&apicompat.AnthropicStreamEvent{Type: "message_stop"}, "")
			case "done":
				stream.sawTerminal = true
			}
			require.True(t, stream.sawTerminal)
			require.Nil(t, stream.failure(), "terminal without an error still succeeds")
			const payload = `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`
			stream.observe(&apicompat.AnthropicStreamEvent{Type: "error"}, payload)
			failure := stream.failure()
			require.NotNil(t, failure)
			require.Equal(t, "overloaded_error", failure.errType)
			var upstreamErr *sseStreamErrorEventError
			require.ErrorAs(t, failure.err, &upstreamErr)
			require.Equal(t, payload, upstreamErr.RawData)

			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			before := c.Writer.Size()
			_, err := io.WriteString(c.Writer, "partial")
			require.NoError(t, err)
			failuresWritten := 0
			result, err := finishAnthropicCompatStreamFailure(c, before, failure,
				&ForwardResult{Usage: ClaudeUsage{InputTokens: 10, OutputTokens: 2}}, func() { failuresWritten++ })
			require.Nil(t, result, "an explicit upstream error must not settle a successful result")
			require.ErrorAs(t, err, &upstreamErr)
			require.Equal(t, 1, failuresWritten)

			stream.clientDisconnected = true
			require.Nil(t, stream.failure(), "disconnected clients keep the existing drain billing contract")
		})
	}
}

func TestAnthropicCompatStreamingErrorAfterStopReasonFails(t *testing.T) {
	const errorPayload = `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`
	payload := anthropicStreamWithoutMessageStop() + "\nevent: error\ndata: " + errorPayload + "\n\n"
	for name, handle := range compatStreamHandlers() {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/"+name, nil)
			response := &http.Response{Header: http.Header{}, Body: io.NopCloser(strings.NewReader(payload))}
			result, err := handle(newCompatStreamIdleTestService(0), response, c)
			var upstreamErr *sseStreamErrorEventError
			require.ErrorAs(t, err, &upstreamErr)
			require.Equal(t, errorPayload, upstreamErr.RawData)
			require.Nil(t, result)
			require.Equal(t, 1, strings.Count(recorder.Body.String(), compatStreamFailureMarkers[name]))
			require.NotContains(t, recorder.Body.String(), compatStreamTerminalMarkers[name])
		})
	}
}
