//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usageadjustment"
	"github.com/stretchr/testify/require"
)

func TestCacheReadAdjustmentAnthropicRevenue(t *testing.T) {
	ctx := usageadjustment.WithRecorder(context.Background())
	rewriter := usageadjustment.Rewriter{Percent: 10, Observe: usageadjustment.Observer(ctx, 29, "req")}
	rewriter.Rewrite([]byte(`{"id":"msg29","usage":{"input_tokens":1000,"cache_read_input_tokens":8000}}`))
	repo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newGatewayRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
	err := svc.RecordUsage(ctx, &RecordUsageInput{Result: &ForwardResult{RequestID: "req", Model: "claude-sonnet-4-5",
		Usage: ClaudeUsage{InputTokens: 1800, CacheReadInputTokens: 7200, OutputTokens: 100}},
		APIKey: &APIKey{ID: 2}, User: &User{ID: 1}, Account: &Account{ID: 29}})
	require.NoError(t, err)
	log := repo.lastLog
	require.NotNil(t, log.CacheReadAdjustment)
	require.Equal(t, int64(800), log.CacheReadAdjustment.ShiftedTokens)
	require.InDelta(t, log.ActualCost-*log.CacheReadAdjustment.BaselineActualCost, *log.CacheReadAdjustment.AdditionalActualCost, 1e-10)
	require.Greater(t, *log.CacheReadAdjustment.AdditionalActualCost, 0.0)
}
