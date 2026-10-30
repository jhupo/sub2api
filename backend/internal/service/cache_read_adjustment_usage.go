package service

import (
	"context"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usageadjustment"
)

type CacheReadAdjustmentUsage struct {
	usageadjustment.Snapshot
	BaselineActualCost   *float64 `json:"baseline_actual_cost"`
	AdditionalActualCost *float64 `json:"additional_actual_cost"`
}

var errCacheAdjustmentMediaPricing = fmt.Errorf("original cache modality breakdown unavailable")

func recordCacheReadAdjustment(ctx context.Context, log *UsageLog, requestID string, baseline func(int) (*CostBreakdown, error)) {
	snapshot := usageadjustment.Lookup(ctx, log.AccountID, requestID, log.CacheReadTokens)
	if snapshot == nil || snapshot.ShiftedTokens > int64(log.InputTokens) {
		return
	}
	log.CacheReadAdjustment = &CacheReadAdjustmentUsage{Snapshot: *snapshot}
	cost, err := baseline(int(snapshot.ShiftedTokens))
	if err != nil || cost == nil {
		return
	}
	original := canonicalUsageActualCost(cost)
	delta := QuantizeUsageBillingAmount(log.ActualCost - original)
	log.CacheReadAdjustment.BaselineActualCost = &original
	log.CacheReadAdjustment.AdditionalActualCost = &delta
}

type CacheReadAdjustmentDay struct {
	Date                       string  `json:"date"`
	AccountID                  int64   `json:"account_id"`
	Requests                   int64   `json:"requests"`
	PricedRequests             int64   `json:"priced_requests"`
	ShiftedTokens              int64   `json:"shifted_tokens"`
	AdditionalCost             float64 `json:"additional_cost"`
	BalanceAdditionalCost      float64 `json:"balance_additional_cost"`
	SubscriptionAdditionalCost float64 `json:"subscription_additional_cost"`
}

type CacheReadAdjustmentStatsRepository interface {
	GetCacheReadAdjustmentDaily(context.Context, time.Time, time.Time, string, int64) ([]CacheReadAdjustmentDay, error)
}

func (s *UsageService) GetCacheReadAdjustmentDaily(ctx context.Context, start, end time.Time, zone string, accountID int64) ([]CacheReadAdjustmentDay, error) {
	repo, ok := s.usageRepo.(CacheReadAdjustmentStatsRepository)
	if !ok {
		return nil, fmt.Errorf("cache read adjustment statistics unavailable")
	}
	return repo.GetCacheReadAdjustmentDaily(ctx, start, end, zone, accountID)
}
