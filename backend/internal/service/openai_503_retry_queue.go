package service

import (
	"context"
	"errors"
	"sync"
	"time"
)

var ErrOpenAI503RetryQueueFull = errors.New("openai 503 retry queue is full")

const openAI503RetryQueueMaxEntries = 256

type openAI503RetryQueue struct {
	mu     sync.Mutex
	queues map[int64][]*openAI503RetryQueueEntry
}

type openAI503RetryQueueEntry struct {
	turn    chan struct{}
	readyAt time.Time
}

func newOpenAI503RetryQueue() *openAI503RetryQueue {
	return &openAI503RetryQueue{queues: make(map[int64][]*openAI503RetryQueueEntry)}
}

// wait orders attempt starts, not response completions. The handler retains its
// account concurrency slot throughout this wait and the subsequent request.
// Only an explicit overload creates a queue; normal traffic joins an existing
// queue but otherwise keeps the account's configured parallelism.
func (q *openAI503RetryQueue) wait(ctx context.Context, accountID int64, delay time.Duration, activate bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if q == nil || accountID <= 0 {
		return nil
	}
	entry, err := q.enqueue(accountID, time.Now().Add(delay), activate)
	if err != nil || entry == nil {
		return err
	}
	defer q.remove(accountID, entry)
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-entry.turn:
	}
	if wait := time.Until(entry.readyAt); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	return ctx.Err()
}

func (q *openAI503RetryQueue) enqueue(accountID int64, readyAt time.Time, activate bool) (*openAI503RetryQueueEntry, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	entries := q.queues[accountID]
	if len(entries) == 0 && !activate {
		return nil, nil
	}
	if len(entries) >= openAI503RetryQueueMaxEntries {
		return nil, ErrOpenAI503RetryQueueFull
	}
	entry := &openAI503RetryQueueEntry{turn: make(chan struct{}), readyAt: readyAt}
	if len(entries) == 0 {
		close(entry.turn)
	}
	q.queues[accountID] = append(entries, entry)
	return entry, nil
}

func (q *openAI503RetryQueue) remove(accountID int64, entry *openAI503RetryQueueEntry) {
	q.mu.Lock()
	defer q.mu.Unlock()
	entries := q.queues[accountID]
	for i, candidate := range entries {
		if candidate != entry {
			continue
		}
		entries = append(entries[:i], entries[i+1:]...)
		if len(entries) == 0 {
			delete(q.queues, accountID)
		} else {
			q.queues[accountID] = entries
			if i == 0 {
				close(entries[0].turn)
			}
		}
		return
	}
}
