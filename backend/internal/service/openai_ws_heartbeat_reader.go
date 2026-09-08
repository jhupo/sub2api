package service

import (
	"context"
	"time"
)

type openAIWSReadResult struct {
	payload []byte
	err     error
}

// Reads only on demand: a pooled connection must not have an outstanding read
// after its terminal event. The caller remains the sole downstream writer.
type openAIWSHeartbeatReader struct {
	ctx       context.Context
	cancel    context.CancelFunc
	read      func(context.Context) ([]byte, error)
	requests  chan struct{}
	results   chan openAIWSReadResult
	done      chan struct{}
	ticker    *time.Ticker
	heartbeat func()
}

func newOpenAIWSHeartbeatReader(ctx context.Context, read func(context.Context) ([]byte, error), interval time.Duration, heartbeat func()) *openAIWSHeartbeatReader {
	r := &openAIWSHeartbeatReader{ctx: ctx, read: read, heartbeat: heartbeat}
	if interval <= 0 {
		return r
	}
	r.ctx, r.cancel = context.WithCancel(ctx)
	r.requests = make(chan struct{})
	r.results = make(chan openAIWSReadResult, 1)
	r.done = make(chan struct{})
	r.ticker = time.NewTicker(interval)
	go func() {
		defer close(r.done)
		for {
			select {
			case <-r.ctx.Done():
				return
			case <-r.requests:
				payload, err := r.read(r.ctx)
				r.results <- openAIWSReadResult{payload, err}
			}
		}
	}()
	return r
}

func (r *openAIWSHeartbeatReader) Read() ([]byte, error) {
	if r.ticker == nil {
		return r.read(r.ctx)
	}
	select {
	case <-r.ctx.Done():
		return nil, r.ctx.Err()
	case r.requests <- struct{}{}:
	}
	for {
		select {
		case result := <-r.results:
			return result.payload, result.err
		case <-r.ticker.C:
			if r.heartbeat != nil {
				r.heartbeat()
			}
		case <-r.ctx.Done():
			return nil, r.ctx.Err()
		}
	}
}

func (r *openAIWSHeartbeatReader) Close() {
	if r.cancel != nil {
		r.ticker.Stop()
		r.cancel()
		<-r.done
	}
}
