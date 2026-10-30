package service

import (
	"context"
	"fmt"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usageadjustment"
	"github.com/stretchr/testify/require"
)

func TestCacheReadAdjustmentRevenueUsesOriginalUsageAndBilledPrice(t *testing.T) {
	for _, percent := range []int{0, 10, 25, 100} {
		t.Run(fmt.Sprint(percent), func(t *testing.T) {
			ctx := usageadjustment.WithRecorder(context.Background())
			rewriter := usageadjustment.Rewriter{Percent: percent, InputIncludesCache: true, Observe: usageadjustment.Observer(ctx, 29, "request")}
			body := rewriter.Rewrite([]byte(`{"type":"response.completed","response":{"id":"resp_29","usage":{"input_tokens":9000,"output_tokens":100,"input_tokens_details":{"cached_tokens":8000}}}}`))
			usage := OpenAIUsage{}
			parseOpenAIWSResponseUsageFromCompletedEvent(body, &usage)
			repo := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newOpenAIRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			err := svc.RecordUsage(ctx, &OpenAIRecordUsageInput{
				Result: &OpenAIForwardResult{RequestID: "request", Model: "gpt-5.1", Usage: usage},
				APIKey: &APIKey{ID: 2}, User: &User{ID: 1}, Account: &Account{ID: 29},
			})
			require.NoError(t, err)
			log := repo.lastLog
			require.Equal(t, 9000, log.InputTokens+log.CacheReadTokens)
			if percent == 0 {
				require.Nil(t, log.CacheReadAdjustment)
				return
			}
			require.NotNil(t, log.CacheReadAdjustment)
			require.Equal(t, int64(8000*percent/100), log.CacheReadAdjustment.ShiftedTokens)
			original := OpenAIUsage{InputTokens: 9000, OutputTokens: 100, CacheReadInputTokens: 8000}
			expected := expectedOpenAICost(t, svc, "gpt-5.1", original, 1.1)
			require.InDelta(t, canonicalUsageActualCost(expected), *log.CacheReadAdjustment.BaselineActualCost, 1e-10)
			require.InDelta(t, log.ActualCost-canonicalUsageActualCost(expected), *log.CacheReadAdjustment.AdditionalActualCost, 1e-10)
			require.Greater(t, *log.CacheReadAdjustment.AdditionalActualCost, 0.0)
		})
	}
}

func TestCacheReadAdjustmentRevenueWithoutRequestIDHeader(t *testing.T) {
	for _, percent := range []int{10, 100} {
		t.Run(fmt.Sprint(percent), func(t *testing.T) {
			ctx := usageadjustment.WithRecorder(context.Background())
			rewriter := usageadjustment.Rewriter{Percent: percent, InputIncludesCache: true, Observe: usageadjustment.Observer(ctx, 31, "")}
			body := rewriter.Rewrite([]byte(`{"type":"response.completed","response":{"id":"resp_no_headers","usage":{"input_tokens":4656,"output_tokens":5,"input_tokens_details":{"cached_tokens":4480}}}}`))
			usage := OpenAIUsage{}
			parseOpenAIWSResponseUsageFromCompletedEvent(body, &usage)
			repo := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newOpenAIRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			err := svc.RecordUsage(ctx, &OpenAIRecordUsageInput{
				Result: &OpenAIForwardResult{ResponseID: "resp_no_headers", Model: "gpt-5.1", Usage: usage},
				APIKey: &APIKey{ID: 2}, User: &User{ID: 1}, Account: &Account{ID: 31},
			})
			require.NoError(t, err)
			log := repo.lastLog
			require.Equal(t, 4656, log.InputTokens+log.CacheReadTokens)
			require.NotNil(t, log.CacheReadAdjustment)
			require.EqualValues(t, 4480, log.CacheReadAdjustment.OriginalCacheTokens)
			require.EqualValues(t, 4480*percent/100, log.CacheReadAdjustment.ShiftedTokens)
			original := OpenAIUsage{InputTokens: 4656, OutputTokens: 5, CacheReadInputTokens: 4480}
			expected := expectedOpenAICost(t, svc, "gpt-5.1", original, 1.1)
			require.InDelta(t, canonicalUsageActualCost(expected), *log.CacheReadAdjustment.BaselineActualCost, 1e-10)
			require.InDelta(t, log.ActualCost-canonicalUsageActualCost(expected), *log.CacheReadAdjustment.AdditionalActualCost, 1e-10)
		})
	}
}
