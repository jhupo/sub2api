package usageadjustment

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRecorderIsolatesAccountsTurnsAndDetachedBilling(t *testing.T) {
	ctx := WithRecorder(context.Background())
	observe := Observer(ctx, 29, "http-request")
	for _, id := range []string{"resp_1", "resp_2"} {
		rewriter := Rewriter{Percent: 10, InputIncludesCache: true, Observe: observe}
		rewriter.Rewrite([]byte(`{"response":{"id":"` + id + `","usage":{"input_tokens":9000,"input_tokens_details":{"cached_tokens":8000}}}}`))
	}
	detached := CopyRecorder(ctx, context.Background())
	for _, id := range []string{"resp_1", "resp_2", "http-request"} {
		snapshot := Lookup(detached, 29, id, 7200)
		require.Equal(t, &Snapshot{Percent: 10, OriginalCacheTokens: 8000, ShiftedTokens: 800}, snapshot)
	}
	require.Nil(t, Lookup(ctx, 30, "resp_1", 7200))
	require.Nil(t, Lookup(ctx, 29, "other-turn", 7200))
	require.Nil(t, Lookup(ctx, 29, "resp_1", 8000))
}

func TestRecorderDoesNotEstimateFromZeroOrIncompleteUsage(t *testing.T) {
	for _, body := range []string{
		`{"usage":{"input_tokens":100,"cache_read_input_tokens":0}}`,
		`{"usage":{"cache_read_input_tokens":8000}}`,
	} {
		ctx := WithRecorder(context.Background())
		rewriter := Rewriter{Percent: 10, Observe: Observer(ctx, 29, "request")}
		rewriter.Rewrite([]byte(body))
		require.Nil(t, Lookup(ctx, 29, "request", 7200))
	}
}
