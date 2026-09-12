package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestOpenAIWSFundingFailureEventPreservesSubscriptionLimitCode(t *testing.T) {
	payload := buildOpenAIWSFailureEvent("resp_partial", "gpt-test", "DAILY_LIMIT_EXCEEDED", "Daily subscription usage limit exceeded")
	require.True(t, json.Valid(payload))
	require.Equal(t, "response.failed", gjson.GetBytes(payload, "type").String())
	require.Equal(t, "DAILY_LIMIT_EXCEEDED", gjson.GetBytes(payload, "response.error.code").String())
	require.Equal(t, "Daily subscription usage limit exceeded", gjson.GetBytes(payload, "response.error.message").String())
}

func TestOpenAIWSTurnBillingIDOverridesConnectionAndUpstream(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkey.ClientRequestID, "same-client-id")
	ctx = context.WithValue(ctx, ctxkey.RequestID, "connection-id")
	for _, id := range []string{"ws-turn:one", "ws-turn:two"} {
		turn := ContextWithOpenAIWSTurnBillingID(ctx, id)
		require.Equal(t, id, ResolveBalancePreauthorizationRequestID(turn))
		require.Equal(t, id, resolveUsageBillingRequestID(turn, "resp_upstream"))
		require.Equal(t, "web_search:separate-event", resolveUsageBillingRequestID(turn, "web_search:separate-event"))
	}
}

func TestOpenAIWSUsesStreamingReservationWithoutHTTPFlag(t *testing.T) {
	body := []byte(`{"type":"response.create","model":"gpt-test","input":"hi"}`)
	require.Equal(t, DefaultBalancePreauthorizationOutputWindow, EstimateStreamingPreauthorizationTokens(body).OutputTokens)
	require.Equal(t, DefaultBalancePreauthorizationNonStreamingOutputWindow, EstimateBalancePreauthorizationTokens(body).OutputTokens)
	body = []byte(`{"model":"gpt-test","max_output_tokens":700}`)
	require.Equal(t, 700, EstimateStreamingPreauthorizationTokens(body).OutputTokens)
}

func TestOpenAIWSOutputReservationIncludesReasoningButNotImageBudget(t *testing.T) {
	var budget OpenAIWSOutputReservation
	require.Equal(t, 100, budget.UpperBound([]byte(`{"type":"response.completed","response":{"usage":{"input_tokens":1,"output_tokens":1000,"output_tokens_details":{"image_tokens":900,"reasoning_tokens":50}}}}`)))
}

func TestOpenAIWSOutputReservationIgnoresRepeatedAndControlFrames(t *testing.T) {
	var budget OpenAIWSOutputReservation
	require.Equal(t, 5, budget.UpperBound([]byte(`{"type":"response.output_text.delta","delta":"hello"}`)))
	for _, payload := range []string{
		`{"type":"response.created","response":{"id":"resp_1"}}`,
		`{"type":"response.output_text.done","text":"hello"}`,
		`{"type":"response.completed","response":{"output":[{"text":"hello"}]}}`,
		`{"type":"response.image_generation_call.partial_image","partial_image_b64":"abc"}`,
	} {
		require.Equal(t, 5, budget.UpperBound([]byte(payload)))
	}
	require.Equal(t, 7, budget.UpperBound([]byte(`{"type":"response.function_call_arguments.delta","delta":"{}"}`)))
}

func TestOpenAIWSOutputReservationDoneOnlyAndMultipleItems(t *testing.T) {
	var budget OpenAIWSOutputReservation
	require.Equal(t, 5, budget.UpperBound([]byte(`{"type":"response.output_text.done","output_index":0,"text":"hello"}`)))
	require.Equal(t, 5, budget.UpperBound([]byte(`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","content":[{"text":"hello"}]}}`)))
	require.Equal(t, 7, budget.UpperBound([]byte(`{"type":"response.function_call_arguments.done","output_index":1,"arguments":"{}"}`)))
	require.Equal(t, 7, budget.UpperBound([]byte(`{"type":"response.completed","response":{"output":[{"type":"message","content":[{"text":"hello"}]},{"type":"function_call","arguments":"{}"}]}}`)))
	var terminalOnly OpenAIWSOutputReservation
	require.Equal(t, 5, terminalOnly.UpperBound([]byte(`{"type":"response.completed","response":{"output":[{"type":"message","content":[{"text":"hello"}]}]}}`)))
}

func TestOpenAIWSRepriceRetainsHoldOwnershipAndSettlementID(t *testing.T) {
	f := newPreauthorizationFixture()
	f.calculator.outputUnitPrice = 0.0001
	request := balancePreauthorizationTestRequest()
	request.RequestID = "ws-turn:retry"
	guard, err := f.service.Preauthorize(context.Background(), request)
	require.NoError(t, err)
	initial := guard.HoldAmount()
	for i := 0; i < 3; i++ {
		require.NoError(t, f.service.RepriceBeforeSend(context.Background(), guard, request))
		require.Equal(t, initial, guard.HoldAmount())
	}
	require.Zero(t, f.wallet.topUpCalls, "retrying an unchanged turn must reuse its hold")
	request.InitialOutputWindowTokens = 1024
	require.NoError(t, f.service.RepriceBeforeSend(context.Background(), guard, request))
	require.Greater(t, guard.HoldAmount(), initial)
	repriced := guard.HoldAmount()
	require.NoError(t, f.service.RepriceBeforeSend(context.Background(), guard, request))
	require.Equal(t, repriced, guard.HoldAmount())
	require.Equal(t, 1, f.wallet.topUpCalls, "a larger retry reserves its difference only once")
	require.NoError(t, guard.ObserveStreamingOutput(context.Background(), 100))
	require.Equal(t, 2, f.wallet.topUpCalls)
	require.InDelta(t, repriced+1024*0.0001, guard.HoldAmount(), 2e-8)
	require.Equal(t, request.RequestID, guard.RequestID())
	require.True(t, guard.IsCurrentOwner())
	worker, ok := guard.TransferToWorker()
	require.True(t, ok)
	require.ErrorIs(t, f.service.RepriceBeforeSend(context.Background(), guard, request), ErrBalancePreauthorizationOwnershipTransferred)
	require.NoError(t, guard.Refund(context.Background()))
	require.True(t, worker.IsCurrentOwner())
	require.NoError(t, worker.Refund(context.Background()))
}
