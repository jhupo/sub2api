package service

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

func TestOpenAIWSUncorrelatedErrorWaitsOnlyForOwnedFailure(t *testing.T) {
	for _, terminal := range []string{
		`{"type":"response.failed","response":{"id":"resp_current","usage":{"input_tokens":7}}}`,
		`{"type":"response.failed","response":{"id":"resp_old","usage":{"input_tokens":999}}}`,
		`{"type":"response.failed","response":{"usage":{"input_tokens":999}}}`,
		`{"type":"response.output_text.delta","response_id":"resp_current","delta":"late"}`,
	} {
		t.Run(terminal, func(t *testing.T) {
			var boundary openAIWSResponseBoundary
			require.NoError(t, boundary.begin())
			require.NoError(t, boundary.observe([]byte(`{"type":"response.completed","response":{"id":"resp_old"}}`)))
			require.NoError(t, boundary.begin())
			require.NoError(t, boundary.observe([]byte(`{"type":"response.created","response":{"id":"resp_current"}}`)))
			for n := 0; n < 3; n++ {
				deadline := boundary.readDeadline()
				require.ErrorIs(t, boundary.observe([]byte(`{"type":"error","error":{"code":"server_error"}}`)), errOpenAIWSUncorrelatedError)
				if n > 0 {
					require.Equal(t, deadline, boundary.readDeadline())
				}
			}
			err := boundary.observe([]byte(terminal))
			if terminal == `{"type":"response.failed","response":{"id":"resp_current","usage":{"input_tokens":7}}}` {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, errOpenAIWSResponseBoundary)
			}
			require.False(t, boundary.reusable())
		})
	}
}

func TestOpenAIWSPooledUncorrelatedErrorRetainsDeadlineAcrossReads(t *testing.T) {
	inner := &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.completed","response":{"id":"resp_old"}}`),
		[]byte(`{"type":"response.created","response":{"id":"resp_current"}} {"type":"error","error":{"code":"server_error"}}`),
		[]byte(`{"type":"response.failed","response":{"id":"resp_current","usage":{"input_tokens":7}}}`),
	}, readDelays: []time.Duration{0, 0, time.Second}}
	conn := &openAIWSConn{ws: inner, closedCh: make(chan struct{})}
	require.NoError(t, conn.responseBoundary.begin())
	_, err := conn.readMessage(context.Background())
	require.NoError(t, err)
	require.NoError(t, conn.responseBoundary.begin())
	prefix, err := conn.readMessage(context.Background())
	require.NoError(t, err)
	require.Contains(t, string(prefix), "response.created")
	require.NotContains(t, string(prefix), "server_error")
	require.False(t, conn.responseBoundary.readDeadline().IsZero())
	// Shorten the established deadline to avoid a five-second unit test.
	conn.responseBoundary.errorDeadline = time.Now().Add(10 * time.Millisecond)
	payload, err := conn.readMessage(context.Background())
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Nil(t, payload)
	require.False(t, conn.responseBoundary.reusable())
}

func TestOpenAIWSDedicatedUncorrelatedErrorPreservesOnlyCurrentUsage(t *testing.T) {
	conn := &openAIWSResponseFrameConn{inner: &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.completed","response":{"id":"resp_old"}}`),
		[]byte(`{"type":"response.created","response":{"id":"resp_current"}}`),
		[]byte(`{"type":"error","error":{"code":"server_error"}} {"type":"response.failed","response":{"id":"resp_current","usage":{"input_tokens":7}}}`),
	}}}
	require.NoError(t, conn.WriteFrame(context.Background(), coderws.MessageText, []byte(`{"type":"response.create"}`)))
	_, _, err := conn.ReadFrame(context.Background())
	require.NoError(t, err)
	require.NoError(t, conn.WriteFrame(context.Background(), coderws.MessageText, []byte(`{"type":"response.create"}`)))
	_, _, err = conn.ReadFrame(context.Background())
	require.NoError(t, err)
	_, payload, err := conn.ReadFrame(context.Background())
	require.NoError(t, err)
	require.Contains(t, string(payload), `"input_tokens":7`)
	require.NotContains(t, string(payload), "server_error")
	require.False(t, conn.boundary.reusable())
}

func TestOpenAIWSResponseBoundaryRejectsLateAndMismatchedFrames(t *testing.T) {
	for _, late := range []string{
		`{"type":"response.completed","response":{"id":"resp_a","usage":{"input_tokens":999}}}`,
		`{"type":"response.failed","response":{"id":"resp_a"}}`,
		`{"type":"response.output_text.delta","response_id":"resp_a","delta":"private"}`,
		`{"type":"response.output_text.delta","delta":"unowned"}`,
		`{"type":"error","error":{"code":"rate_limit_exceeded"}}`,
	} {
		t.Run(late, func(t *testing.T) {
			var boundary openAIWSResponseBoundary
			require.NoError(t, boundary.begin())
			require.NoError(t, boundary.observe([]byte(`{"type":"response.completed","response":{"id":"resp_a"}}`)))
			require.NoError(t, boundary.begin())
			require.ErrorIs(t, boundary.observe([]byte(late)), errOpenAIWSResponseBoundary)
		})
	}
	var boundary openAIWSResponseBoundary
	require.NoError(t, boundary.begin())
	require.NoError(t, boundary.observe([]byte(`{"type":"response.created","response":{"id":"resp_b"}}`)))
	require.NoError(t, boundary.observe([]byte(`{"type":"response.output_text.delta","id":"evt_1","delta":"text"}`)))
	require.ErrorIs(t, boundary.observe([]byte(`{"type":"response.completed","response":{"id":"resp_a"}}`)), errOpenAIWSResponseBoundary)
}

func TestOpenAIWSResponseBoundaryFailureAndCancellationRetire(t *testing.T) {
	for _, terminal := range []string{"response.failed", "response.incomplete", "response.cancelled", "error"} {
		t.Run(terminal, func(t *testing.T) {
			var boundary openAIWSResponseBoundary
			require.NoError(t, boundary.begin())
			require.NoError(t, boundary.observe([]byte(fmt.Sprintf(`{"type":%q,"response":{"id":"resp_a"}}`, terminal))))
			require.False(t, boundary.reusable())
			require.ErrorIs(t, boundary.begin(), errOpenAIWSResponseBoundary)
		})
	}
	pool, _ := newWSIdentityTestPool(t)
	account := activeCodexFingerprintPoolAccountForTest(701)
	lease, err := pool.Acquire(context.Background(), openAIWSAcquireRequest{Account: account, WSURL: "wss://example.com/responses"})
	require.NoError(t, err)
	require.NoError(t, lease.WriteJSONContext(context.Background(), json.RawMessage(`{"type":"response.create"}`)))
	lease.Release()
	select {
	case <-lease.conn.closedCh:
	default:
		t.Fatal("releasing an unfinished turn must retire the socket")
	}
}

func TestOpenAIWSResponseBoundaryHistoryBound(t *testing.T) {
	var boundary openAIWSResponseBoundary
	for n := 0; n < openAIWSResponseHistoryLimit; n++ {
		require.NoError(t, boundary.begin())
		require.NoError(t, boundary.observe([]byte(fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_%d"}}`, n))))
	}
	require.False(t, boundary.reusable())
	require.ErrorIs(t, boundary.begin(), errOpenAIWSResponseBoundary)
}

func TestOpenAIWSDedicatedResponseBoundaryRejectsLateUsage(t *testing.T) {
	conn := &openAIWSResponseFrameConn{inner: &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.completed","response":{"id":"resp_a","usage":{"input_tokens":1}}}`),
		[]byte(`{"type":"response.completed","response":{"id":"resp_a","usage":{"input_tokens":999}}}`),
	}}}
	require.NoError(t, conn.WriteFrame(context.Background(), coderws.MessageText, []byte(`{"type":"response.create"}`)))
	_, first, err := conn.ReadFrame(context.Background())
	require.NoError(t, err)
	require.Contains(t, string(first), "resp_a")
	require.NoError(t, conn.WriteFrame(context.Background(), coderws.MessageText, []byte(`{"type":"response.create"}`)))
	_, stale, err := conn.ReadFrame(context.Background())
	require.ErrorIs(t, err, errOpenAIWSResponseBoundary)
	require.Nil(t, stale, "late usage must never reach the relay's accounting hooks")
}

func TestOpenAIWSDedicatedResponseBoundaryValidatesEveryDocument(t *testing.T) {
	for _, payload := range []string{
		`{"type":"response.created","response":{"id":"resp_current"}} {"type":"response.completed","response":{"id":"resp_old","usage":{"input_tokens":999}}}`,
		`{"type":"session.updated"} {"type":"response.completed","response":{"id":"resp_old","usage":{"input_tokens":999}}}`,
	} {
		t.Run(payload, func(t *testing.T) {
			conn := &openAIWSResponseFrameConn{inner: &openAIWSCaptureConn{events: [][]byte{
				[]byte(`{"type":"response.completed","response":{"id":"resp_old"}}`), []byte(payload),
			}}}
			require.NoError(t, conn.WriteFrame(context.Background(), coderws.MessageText, []byte(`{"type":"response.create"}`)))
			_, _, err := conn.ReadFrame(context.Background())
			require.NoError(t, err)
			require.NoError(t, conn.WriteFrame(context.Background(), coderws.MessageText, []byte(`{"type":"response.create"}`)))
			_, first, err := conn.ReadFrame(context.Background())
			require.NoError(t, err)
			require.NotContains(t, string(first), "999")
			_, late, err := conn.ReadFrame(context.Background())
			require.ErrorIs(t, err, errOpenAIWSResponseBoundary)
			require.Nil(t, late)
		})
	}
}

func TestOpenAIWSDedicatedResponseBoundaryPreservesFailedUsageAfterBareError(t *testing.T) {
	conn := &openAIWSResponseFrameConn{inner: &openAIWSCaptureConn{events: [][]byte{
		[]byte(`{"type":"response.created","response":{"id":"resp_a"}}`),
		[]byte(`{"type":"error","error":{"code":"server_error"}} {"type":"response.failed","response":{"id":"resp_a","usage":{"input_tokens":7,"output_tokens":2}}}`),
	}}}
	require.NoError(t, conn.WriteFrame(context.Background(), coderws.MessageText, []byte(`{"type":"response.create"}`)))
	for _, event := range []string{"response.created", `"type":"error"`, "response.failed"} {
		_, payload, err := conn.ReadFrame(context.Background())
		require.NoError(t, err)
		require.Contains(t, string(payload), event)
		if event == "response.failed" {
			require.Contains(t, string(payload), `"input_tokens":7`)
		}
	}
	require.ErrorIs(t, conn.WriteFrame(context.Background(), coderws.MessageText, []byte(`{"type":"response.create"}`)), errOpenAIWSResponseBoundary)
	_, payload, err := conn.ReadFrame(context.Background())
	require.ErrorIs(t, err, errOpenAIWSConnClosed)
	require.Nil(t, payload)
}

func TestOpenAIWSResponseBoundaryMalformedMessageCannotCompleteTurn(t *testing.T) {
	for _, payload := range []string{
		`{"type":"response.completed","response":{"id":"resp_a"}} garbage`,
		`{"type":"response.completed","response":{"id":"resp_a"}} {broken}`,
		`{}`, `null`,
	} {
		t.Run(payload, func(t *testing.T) {
			var boundary openAIWSResponseBoundary
			require.NoError(t, boundary.begin())
			require.Error(t, boundary.observe([]byte(payload)))
			require.False(t, boundary.reusable())
			require.ErrorIs(t, boundary.begin(), errOpenAIWSResponseBoundary)
		})
	}
}

func BenchmarkOpenAIWSResponseBoundaryDelta(b *testing.B) {
	var boundary openAIWSResponseBoundary
	if err := boundary.begin(); err != nil {
		b.Fatal(err)
	}
	if err := boundary.observe([]byte(`{"type":"response.created","response":{"id":"resp_current"}}`)); err != nil {
		b.Fatal(err)
	}
	frame := []byte(`{"type":"response.output_text.delta","response_id":"resp_current","delta":"hello"}`)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if err := boundary.observe(frame); err != nil {
			b.Fatal(err)
		}
	}
}
