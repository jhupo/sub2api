package service

import (
	"context"
	"io"
	"testing"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

type cacheAdjustmentFrameConn struct{ frames [][]byte }

func (c *cacheAdjustmentFrameConn) WriteFrame(context.Context, coderws.MessageType, []byte) error {
	return nil
}
func (c *cacheAdjustmentFrameConn) Close() error { return nil }
func (c *cacheAdjustmentFrameConn) ReadFrame(context.Context) (coderws.MessageType, []byte, error) {
	if len(c.frames) == 0 {
		return coderws.MessageText, nil, io.EOF
	}
	payload := c.frames[0]
	c.frames = c.frames[1:]
	return coderws.MessageText, payload, nil
}

func TestOpenAIWSCacheReadAdjustmentSameUsageForClientAndBilling(t *testing.T) {
	ctx := context.Background()
	percent := 10
	inner := &cacheAdjustmentFrameConn{}
	conn := &openAIWSResponseFrameConn{inner: inner, usagePercent: func(context.Context) int { return percent }}
	for _, tt := range []struct {
		id            string
		percent, want int
	}{{"resp_1", 10, 7200}, {"resp_2", 0, 8000}} {
		percent = tt.percent
		inner.frames = [][]byte{
			[]byte(`{"type":"response.created","response":{"id":"` + tt.id + `"}}`),
			[]byte(`{"type":"response.completed","response":{"id":"` + tt.id + `","usage":{"input_tokens":9000,"output_tokens":100,"input_tokens_details":{"cached_tokens":8000}}}}`),
		}
		require.NoError(t, conn.WriteFrame(ctx, coderws.MessageText, []byte(`{"type":"response.create"}`)))
		_, _, err := conn.ReadFrame(ctx)
		require.NoError(t, err)
		_, payload, err := conn.ReadFrame(ctx)
		require.NoError(t, err)
		usage := &OpenAIUsage{}
		parseOpenAIWSResponseUsageFromCompletedEvent(payload, usage)
		require.Equal(t, tt.want, usage.CacheReadInputTokens)
		require.Equal(t, 9000, usage.InputTokens)
	}
}
