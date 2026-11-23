package handler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

func TestOpenAIResponsesWebSocketRejectsRepeatedFieldsBeforeRouting(t *testing.T) {
	for _, body := range []string{
		`{"type":"response.create","model":"one","` + strings.Repeat("\U0001f525", 40) + `":1,"` + strings.Repeat("\U0001f525", 40) + `":2}`,
		`{"type":"response.create","model":"one","model":"two"}`,
		`{"type":"response.create","model":"one","Model":"one"}`,
		`{"type":"response.create","model":"one","reasoning":{"effort":"low","Effort":"high"}}`,
		`{"type":"response.create","model":"one","previous_response_id":"resp_one","previous_response_id":"resp_two"}`,
	} {
		h := newOpenAIHandlerForPreviousResponseIDValidation(t, nil)
		server := newOpenAIWSHandlerTestServer(t, h, middleware.AuthSubject{UserID: 1, Concurrency: 1})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		conn, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/openai/v1/responses", nil)
		require.NoError(t, err)
		require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(body)))
		_, _, err = conn.Read(ctx)
		var closeErr coderws.CloseError
		require.ErrorAs(t, err, &closeErr)
		require.Equal(t, coderws.StatusPolicyViolation, closeErr.Code)
		require.Contains(t, closeErr.Reason, "more than once")
		_ = conn.CloseNow()
		cancel()
		server.Close()
	}
}
