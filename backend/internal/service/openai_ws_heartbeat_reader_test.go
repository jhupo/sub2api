package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAIWSHeartbeatReaderDoesNotReadAheadOrLeak(t *testing.T) {
	var calls atomic.Int64
	heartbeats := 0
	ready := make(chan struct{})
	r := newOpenAIWSHeartbeatReader(context.Background(), func(ctx context.Context) ([]byte, error) {
		calls.Add(1)
		select {
		case <-ready:
			return []byte("terminal"), nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}, time.Millisecond, func() {
		heartbeats++
		if heartbeats == 1 {
			close(ready)
		}
	})
	defer r.Close()
	payload, err := r.Read()
	require.NoError(t, err)
	require.Equal(t, "terminal", string(payload))
	require.Positive(t, heartbeats)
	require.EqualValues(t, 1, calls.Load())
}

func TestOpenAIWSHeartbeatReaderCancellationJoinsActiveRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	readDone := make(chan struct{})
	r := newOpenAIWSHeartbeatReader(ctx, func(ctx context.Context) ([]byte, error) {
		defer close(readDone)
		<-ctx.Done()
		return nil, ctx.Err()
	}, time.Millisecond, cancel)
	_, err := r.Read()
	require.ErrorIs(t, err, context.Canceled)
	r.Close()
	select {
	case <-readDone:
	default:
		t.Fatal("reader was not joined before releasing the connection")
	}
}
