package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func queueLength(q *openAI503RetryQueue, accountID int64) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.queues[accountID])
}

func TestOpenAI503RetryQueueDormantUntilActivated(t *testing.T) {
	q := newOpenAI503RetryQueue()
	require.NoError(t, q.wait(context.Background(), 1, 0, false))
	require.Zero(t, queueLength(q, 1))
}

func TestOpenAI503RetryQueueFIFOAndRequeue(t *testing.T) {
	q := newOpenAI503RetryQueue()
	a, err := q.enqueue(1, time.Now(), true)
	require.NoError(t, err)
	b, err := q.enqueue(1, time.Now(), true)
	require.NoError(t, err)
	c, err := q.enqueue(1, time.Now(), false)
	require.NoError(t, err)
	q.remove(1, a)
	select {
	case <-b.turn:
	default:
		t.Fatal("second attempt must be next")
	}
	select {
	case <-c.turn:
		t.Fatal("third attempt cannot overtake second")
	default:
	}
	// A fails again and joins behind C.
	a, err = q.enqueue(1, time.Now(), true)
	require.NoError(t, err)
	q.remove(1, b)
	select {
	case <-c.turn:
	default:
		t.Fatal("new arrival must precede requeued attempt")
	}
	q.remove(1, c)
	q.remove(1, a)
	require.Zero(t, queueLength(q, 1))
}

func TestOpenAI503RetryQueueDispatchesWithoutWaitingForResponse(t *testing.T) {
	q := newOpenAI503RetryQueue()
	gate, err := q.enqueue(1, time.Now(), true)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 2)
	for range 2 {
		go func() { done <- q.wait(ctx, 1, 50*time.Millisecond, true) }()
	}
	require.Eventually(t, func() bool { return queueLength(q, 1) == 3 }, time.Second, time.Millisecond)
	require.NoError(t, q.wait(ctx, 2, 0, false), "another account must stay independent")
	q.remove(1, gate)
	// Both attempts may now run. Neither needs a completion callback to let the
	// other dispatch, and each cooldown started when it entered the queue.
	for range 2 {
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(time.Second):
			t.Fatal("a retry retained its queue position after dispatch")
		}
	}
	require.Zero(t, queueLength(q, 1))
}

func TestOpenAI503RetryQueueCancellationUnblocksNext(t *testing.T) {
	q := newOpenAI503RetryQueue()
	gate, err := q.enqueue(1, time.Now(), true)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- q.wait(ctx, 1, 0, false) }()
	require.Eventually(t, func() bool { return queueLength(q, 1) == 2 }, time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	require.Equal(t, 1, queueLength(q, 1))
	q.remove(1, gate)
	require.NoError(t, q.wait(context.Background(), 1, 0, false))
}

func TestOpenAI503RetryQueueCapacity(t *testing.T) {
	q := newOpenAI503RetryQueue()
	for range openAI503RetryQueueMaxEntries {
		_, err := q.enqueue(1, time.Now(), true)
		require.NoError(t, err)
	}
	require.ErrorIs(t, q.wait(context.Background(), 1, 0, false), ErrOpenAI503RetryQueueFull)
}
