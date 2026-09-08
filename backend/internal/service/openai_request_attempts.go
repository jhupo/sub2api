package service

import (
	"context"
	"net/http"
	"sync"
)

const openAIRequestAttemptLimit = 3

type openAIRequestAttemptKey struct{}
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
