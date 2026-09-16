package service

import (
	"context"
	"net/http"
	"sync"
)

const openAIRequestAttemptLimit = 3

type openAIRequestAttemptKey struct{}

// openAI503RetryAttemptKey marks an attempt that is already governed by the
// account-scoped OpenAI 503 retry state. These attempts must not consume the
// legacy generic turn budget: max_same_account_retries=3 means one initial
// request plus three 503 retries.
type openAI503RetryAttemptKey struct{}

type openAI503RetryAttempt struct {
	mu        sync.Mutex
	available bool
}
type openAIRequestAttempts struct {
	mu    sync.Mutex
	count int
	turn  int
	last  *UpstreamFailoverError
}

func withOpenAIRequestAttemptBudget(ctx context.Context) context.Context {
	if _, ok := ctx.Value(openAIRequestAttemptKey{}).(*openAIRequestAttempts); ok {
		return ctx
	}
	return context.WithValue(ctx, openAIRequestAttemptKey{}, &openAIRequestAttempts{})
}

func consumeOpenAIRequestAttempt(ctx context.Context) error {
	if retry, _ := ctx.Value(openAI503RetryAttemptKey{}).(*openAI503RetryAttempt); retry != nil {
		retry.mu.Lock()
		if retry.available {
			retry.available = false
			retry.mu.Unlock()
			return nil
		}
		retry.mu.Unlock()
	}
	budget, _ := ctx.Value(openAIRequestAttemptKey{}).(*openAIRequestAttempts)
	if budget == nil {
		return nil
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if budget.count < openAIRequestAttemptLimit {
		budget.count++
		return nil
	}
	failure := UpstreamFailoverError{
		StatusCode:             http.StatusServiceUnavailable,
		ClientStatusCode:       http.StatusServiceUnavailable,
		ClientMessage:          "Upstream retry budget exhausted; please retry later",
		RequestScopedTransient: true,
	}
	if budget.last != nil {
		failure = *budget.last
	}
	failure.NextAccountAction = NextAccountStop
	failure.RetryableOnSameAccount = false
	failure.RequestScopedTransient = true
	return &failure
}

func withOpenAI503RetryAttempt(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, openAI503RetryAttemptKey{}, &openAI503RetryAttempt{available: true})
}

func recordOpenAIRequestFailure(ctx context.Context, failure *UpstreamFailoverError) {
	budget, _ := ctx.Value(openAIRequestAttemptKey{}).(*openAIRequestAttempts)
	if budget == nil || failure == nil {
		return
	}
	budget.mu.Lock()
	copy := *failure
	budget.last = &copy
	budget.mu.Unlock()
}

// A new client turn has its own budget, even after a delivered response.failed.
// Reconnecting or replaying the same turn does not replenish its attempts.
func BeginOpenAIRequestTurn(ctx context.Context, turn int) {
	budget, _ := ctx.Value(openAIRequestAttemptKey{}).(*openAIRequestAttempts)
	if budget == nil {
		return
	}
	budget.mu.Lock()
	if budget.turn != turn {
		budget.count, budget.last, budget.turn = 0, nil, turn
	}
	budget.mu.Unlock()
}

func annotateOpenAIAttemptUsage(event *OpsUpstreamErrorEvent, payload []byte) {
	event.UpstreamUsageStatus = "unknown"
	if usage, ok := extractOpenAIUsageFromJSONBytes(payload); ok {
		event.UpstreamUsageStatus, event.UpstreamUsage = "reported", &usage
	}
}
