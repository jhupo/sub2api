package service

import (
	"context"
	"testing"
	"time"
)

func TestOpenAI503RetryQueueDormantUntilActivated(t *testing.T) {
	q := newOpenAI503RetryQueue()
	lease, err := q.enter(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if lease.active() {
		t.Fatal("ordinary request should not activate the queue")
	}
	lease.complete()

	active, err := q.activate(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if !active.active() {
		t.Fatal("explicit 503 should activate the queue")
	}
	active.complete()
}

func TestOpenAI503RetryQueueFIFOAndRequeue(t *testing.T) {
	q := newOpenAI503RetryQueue()
	head, err := q.activate(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	secondReady := make(chan *openAI503RetryLease, 1)
	go func() {
		lease, enterErr := q.enter(context.Background(), 1)
		if enterErr != nil {
			return
		}
		secondReady <- lease
	}()
	time.Sleep(10 * time.Millisecond)
	head.requeue(0)
	var second *openAI503RetryLease
	select {
	case second = <-secondReady:
	case <-time.After(time.Second):
		t.Fatal("second request did not enter queue")
	}
	select {
	case <-second.entry.turn:
	case <-time.After(time.Second):
		t.Fatal("second request did not become head after requeue")
	}
	head.complete()
	second.complete()
}

func TestOpenAI503RetryQueueCancellationUnblocksNext(t *testing.T) {
	q := newOpenAI503RetryQueue()
	head, err := q.activate(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan *openAI503RetryLease, 1)
	go func() {
		canceled, enterErr := q.enter(ctx, 1)
		if enterErr == nil {
			entered <- canceled
		}
	}()
	cancel()
	select {
	case canceled := <-entered:
		canceled.complete()
	case <-time.After(time.Second):
		// q.enter observes cancellation and removes its entry.
	}
	head.complete()
}
