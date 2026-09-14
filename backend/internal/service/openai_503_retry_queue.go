package service

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrOpenAI503RetryQueueFull indicates that an account-local overload queue
// has reached its bounded in-memory capacity. The normal scheduler remains
// responsible for choosing another account when the caller allows failover.
var ErrOpenAI503RetryQueueFull = errors.New("openai 503 retry queue is full")

const openAI503RetryQueueMaxEntries = 256

type openAI503RetryQueue struct {
	mu     sync.Mutex
	queues map[int64][]*openAI503RetryQueueEntry
}

type openAI503RetryQueueEntry struct {
	turn    chan struct{}
	readyAt time.Time
	closed  bool
}

// openAI503RetryLease is held for the lifetime of one account attempt. The
// caller keeps its ordinary account slot while this lease waits or retries.
// That makes the queue account-local without changing the upper scheduler.
type openAI503RetryLease struct {
	queue     *openAI503RetryQueue
	accountID int64
	entry     *openAI503RetryQueueEntry
	once      sync.Once
}

func newOpenAI503RetryQueue() *openAI503RetryQueue {
	return &openAI503RetryQueue{queues: make(map[int64][]*openAI503RetryQueueEntry)}
}

func (q *openAI503RetryQueue) enter(ctx context.Context, accountID int64) (*openAI503RetryLease, error) {
	if q == nil || accountID <= 0 {
		return &openAI503RetryLease{}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	q.mu.Lock()
	entries := q.queues[accountID]
	// The queue is dormant until an explicit 503 activates it. Normal requests
	// must retain the account's configured concurrency and therefore do not
	// create a FIFO entry by themselves.
	if len(entries) == 0 {
		q.mu.Unlock()
		return &openAI503RetryLease{queue: q, accountID: accountID}, nil
	}
	if len(entries) >= openAI503RetryQueueMaxEntries {
		q.mu.Unlock()
		return nil, ErrOpenAI503RetryQueueFull
	}
	entry := &openAI503RetryQueueEntry{turn: make(chan struct{})}
	if len(entries) == 0 {
		close(entry.turn)
	}
	q.queues[accountID] = append(entries, entry)
	q.mu.Unlock()

	lease := &openAI503RetryLease{queue: q, accountID: accountID, entry: entry}
	if err := lease.waitTurn(ctx); err != nil {
		lease.complete()
		return nil, err
	}
	return lease, nil
}

// activate creates the first queue entry for an account after that account
// has received an explicit capacity 503. The caller owns the returned lease
// and keeps its ordinary account slot while it waits and retries.
func (q *openAI503RetryQueue) activate(ctx context.Context, accountID int64) (*openAI503RetryLease, error) {
	if q == nil || accountID <= 0 {
		return &openAI503RetryLease{}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	q.mu.Lock()
	entries := q.queues[accountID]
	if len(entries) >= openAI503RetryQueueMaxEntries {
		q.mu.Unlock()
		return nil, ErrOpenAI503RetryQueueFull
	}
	entry := &openAI503RetryQueueEntry{turn: make(chan struct{})}
	if len(entries) == 0 {
		close(entry.turn)
	}
	q.queues[accountID] = append(entries, entry)
	q.mu.Unlock()
	lease := &openAI503RetryLease{queue: q, accountID: accountID, entry: entry}
	if err := lease.waitTurn(ctx); err != nil {
		lease.complete()
		return nil, err
	}
	return lease, nil
}

func (l *openAI503RetryLease) waitTurn(ctx context.Context) error {
	if l == nil || l.queue == nil || l.entry == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-l.entry.turn:
	}
	if wait := time.Until(l.entry.readyAt); wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}

func (l *openAI503RetryLease) active() bool {
	return l != nil && l.queue != nil && l.entry != nil
}

// requeue moves a failed request to the tail when another request is waiting.
// With no follower, it keeps the current lease at the head and only delays its
// next attempt. This preserves FIFO ordering without creating a self-deadlock.
func (l *openAI503RetryLease) requeue(delay time.Duration) {
	if l == nil || l.queue == nil || l.entry == nil {
		return
	}
	if delay < 0 {
		delay = 0
	}
	q := l.queue
	q.mu.Lock()
	defer q.mu.Unlock()
	entries := q.queues[l.accountID]
	index := -1
	for i, entry := range entries {
		if entry == l.entry {
			index = i
			break
		}
	}
	if index < 0 || len(entries) == 0 || entries[0] != l.entry {
		return
	}
	l.entry.readyAt = time.Now().Add(delay)
	if len(entries) == 1 {
		return
	}

	entries = append(entries[:index], entries[index+1:]...)
	l.entry.turn = make(chan struct{})
	entries = append(entries, l.entry)
	q.queues[l.accountID] = entries
	close(entries[0].turn)
}

func (l *openAI503RetryLease) complete() {
	if l == nil || l.queue == nil || l.entry == nil {
		return
	}
	l.once.Do(func() {
		q := l.queue
		q.mu.Lock()
		defer q.mu.Unlock()
		entries := q.queues[l.accountID]
		index := -1
		for i, entry := range entries {
			if entry == l.entry {
				index = i
				break
			}
		}
		if index < 0 {
			return
		}
		l.entry.closed = true
		wasHead := index == 0
		entries = append(entries[:index], entries[index+1:]...)
		if len(entries) == 0 {
			delete(q.queues, l.accountID)
			return
		}
		q.queues[l.accountID] = entries
		if wasHead {
			close(entries[0].turn)
		}
	})
}
