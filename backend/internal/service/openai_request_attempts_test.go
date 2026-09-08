package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIRequestAttemptBudgetSharedAcrossTransportsAndAccounts(t *testing.T) {
	ctx := withOpenAIRequestAttemptBudget(context.Background())
	var admitted atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if consumeOpenAIRequestAttempt(context.WithoutCancel(ctx)) == nil {
				admitted.Add(1)
			}
		}()
	}
	wg.Wait()
	require.Equal(t, int64(3), admitted.Load())
	failure := codexAdaptiveCapacityShedError()
	recordOpenAIRequestFailure(ctx, failure)
	err := consumeOpenAIRequestAttempt(ctx)
	var exhausted *UpstreamFailoverError
	require.ErrorAs(t, err, &exhausted)
	require.False(t, exhausted.ShouldRetryNextAccount())
	require.False(t, exhausted.ShouldReportAccountScheduleFailure())
	require.Equal(t, failure.ResponseBody, exhausted.ResponseBody)
	require.True(t, failure.ShouldRetryNextAccount(), "the original failure must not be mutated")
}

func TestOpenAIRequestAttemptBudgetResetsOnlyForNewClientTurn(t *testing.T) {
	ctx := withOpenAIRequestAttemptBudget(context.Background())
	BeginOpenAIRequestTurn(ctx, 1)
	for i := 0; i < 3; i++ {
		require.NoError(t, consumeOpenAIRequestAttempt(ctx))
	}
	BeginOpenAIRequestTurn(ctx, 1)
	require.Error(t, consumeOpenAIRequestAttempt(ctx), "reconnection must retain the same turn budget")
	BeginOpenAIRequestTurn(ctx, 2)
	for i := 0; i < 3; i++ {
		require.NoError(t, consumeOpenAIRequestAttempt(ctx))
	}
	require.Error(t, consumeOpenAIRequestAttempt(ctx))
}

func TestOpenAIAttemptUsageDistinguishesUnknownAndReportedZero(t *testing.T) {
	for _, test := range []struct {
		payload, status string
		input           int
	}{
		{`{"type":"error","error":{"code":"server_is_overloaded"}}`, "unknown", 0},
		{`{"type":"response.failed","response":{"usage":{"input_tokens":0,"output_tokens":0}}}`, "reported", 0},
		{`{"type":"response.failed","response":{"usage":{"input_tokens":800,"output_tokens":20,"input_tokens_details":{"cached_tokens":600}}}}`, "reported", 800},
	} {
		var event OpsUpstreamErrorEvent
		annotateOpenAIAttemptUsage(&event, []byte(test.payload))
		require.Equal(t, test.status, event.UpstreamUsageStatus)
		if test.status == "unknown" {
			require.Nil(t, event.UpstreamUsage)
		} else {
			require.Equal(t, test.input, event.UpstreamUsage.InputTokens)
		}
	}
}
