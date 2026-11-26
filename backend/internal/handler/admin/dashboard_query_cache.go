package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
)

var (
	dashboardTrendCache        = newNamedSnapshotCache("dashboard-trend", reportCacheTTL)
	dashboardLiveTrendCache    = newNamedSnapshotCache("dashboard-live-trend", time.Minute)
	dashboardModelStatsCache   = newNamedSnapshotCache("dashboard-models", reportCacheTTL)
	dashboardGroupStatsCache   = newNamedSnapshotCache("dashboard-groups", reportCacheTTL)
	dashboardUsersTrendCache   = newNamedSnapshotCache("dashboard-users-trend", reportCacheTTL)
	dashboardAPIKeysTrendCache = newNamedSnapshotCache("dashboard-api-keys-trend", reportCacheTTL)
)

func useLiveDashboardTrendCache(aggregationEnabled bool, filters dashboardTrendCacheKey, startTime, endTime, now time.Time) bool {
	return aggregationEnabled &&
		!now.Before(startTime) && now.Before(endTime) &&
		(filters.Granularity == "hour" || filters.Granularity == "day") &&
		filters.UserID == 0 && filters.APIKeyID == 0 && filters.AccountID == 0 && filters.GroupID == 0 &&
		filters.Model == "" && filters.RequestType == nil && filters.Stream == nil &&
		filters.NativeCompactionV2 == nil && filters.BillingType == nil && filters.UpstreamModelMismatch == nil
}

type dashboardTrendCacheKey struct {
	StartTime             string `json:"start_time"`
	EndTime               string `json:"end_time"`
	Granularity           string `json:"granularity"`
	UserID                int64  `json:"user_id"`
	APIKeyID              int64  `json:"api_key_id"`
	AccountID             int64  `json:"account_id"`
	GroupID               int64  `json:"group_id"`
	Model                 string `json:"model"`
	RequestType           *int16 `json:"request_type"`
	Stream                *bool  `json:"stream"`
	NativeCompactionV2    *bool  `json:"native_compaction_v2"`
	BillingType           *int8  `json:"billing_type"`
	UpstreamModelMismatch *bool  `json:"upstream_model_mismatch"`
}

type dashboardModelGroupCacheKey struct {
	StartTime             string `json:"start_time"`
	EndTime               string `json:"end_time"`
	UserID                int64  `json:"user_id"`
	APIKeyID              int64  `json:"api_key_id"`
	AccountID             int64  `json:"account_id"`
	GroupID               int64  `json:"group_id"`
	ModelSource           string `json:"model_source,omitempty"`
	RequestType           *int16 `json:"request_type"`
	Stream                *bool  `json:"stream"`
	NativeCompactionV2    *bool  `json:"native_compaction_v2"`
	BillingType           *int8  `json:"billing_type"`
	UpstreamModelMismatch *bool  `json:"upstream_model_mismatch"`
}

type dashboardEntityTrendCacheKey struct {
	StartTime   string `json:"start_time"`
	EndTime     string `json:"end_time"`
	Granularity string `json:"granularity"`
	Limit       int    `json:"limit"`
}

func cacheStatusValue(hit bool) string {
	if hit {
		return "hit"
	}
	return "miss"
}

func mustMarshalDashboardCacheKey(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(raw)
}

func snapshotPayloadAs[T any](payload any) (T, error) {
	typed, ok := payload.(T)
	if !ok {
		if raw, rawOK := payload.(json.RawMessage); rawOK {
			var decoded T
			if err := json.Unmarshal(raw, &decoded); err != nil {
				var zero T
				return zero, err
			}
			return decoded, nil
		}
		var zero T
		return zero, fmt.Errorf("unexpected cache payload type %T", payload)
	}
	return typed, nil
}

func (h *DashboardHandler) getUsageTrendCached(
	ctx context.Context,
	startTime, endTime time.Time,
	granularity string,
	userID, apiKeyID, accountID, groupID int64,
	model string,
	requestType *int16,
	stream *bool,
	nativeCompactionV2 *bool,
	billingType *int8,
	upstreamModelMismatch *bool,
) ([]usagestats.TrendDataPoint, bool, error) {
	filters := dashboardTrendCacheKey{
		StartTime:             startTime.UTC().Format(time.RFC3339),
		EndTime:               endTime.UTC().Format(time.RFC3339),
		Granularity:           granularity,
		UserID:                userID,
		APIKeyID:              apiKeyID,
		AccountID:             accountID,
		GroupID:               groupID,
		Model:                 model,
		RequestType:           requestType,
		Stream:                stream,
		NativeCompactionV2:    nativeCompactionV2,
		BillingType:           billingType,
		UpstreamModelMismatch: upstreamModelMismatch,
	}
	key := mustMarshalDashboardCacheKey(filters)
	cache := dashboardTrendCache
	if useLiveDashboardTrendCache(h.dashboardService.AggregationEnabled(), filters, startTime, endTime, time.Now()) {
		cache = dashboardLiveTrendCache
	}
	entry, hit, err := cache.GetOrLoadContext(ctx, key, func() (any, error) {
		loadCtx, cancel := reportCacheLoadContext(ctx)
		defer cancel()
		return h.dashboardService.GetUsageTrendWithUsageFilters(loadCtx, startTime, endTime, granularity, usagestats.UsageLogFilters{
			UserID: userID, APIKeyID: apiKeyID, AccountID: accountID, GroupID: groupID,
			Model: model, RequestType: requestType, Stream: stream, NativeCompactionV2: nativeCompactionV2, BillingType: billingType,
			UpstreamModelMismatch: upstreamModelMismatch,
		})
	})
	if err != nil {
		return nil, hit, err
	}
	trend, err := snapshotPayloadAs[[]usagestats.TrendDataPoint](entry.Payload)
	return trend, hit, err
}

func (h *DashboardHandler) getModelStatsCached(
	ctx context.Context,
	startTime, endTime time.Time,
	userID, apiKeyID, accountID, groupID int64,
	modelSource string,
	requestType *int16,
	stream *bool,
	nativeCompactionV2 *bool,
	billingType *int8,
	upstreamModelMismatch *bool,
) ([]usagestats.ModelStat, bool, error) {
	key := mustMarshalDashboardCacheKey(dashboardModelGroupCacheKey{
		StartTime:             startTime.UTC().Format(time.RFC3339),
		EndTime:               endTime.UTC().Format(time.RFC3339),
		UserID:                userID,
		APIKeyID:              apiKeyID,
		AccountID:             accountID,
		GroupID:               groupID,
		ModelSource:           usagestats.NormalizeModelSource(modelSource),
		RequestType:           requestType,
		Stream:                stream,
		NativeCompactionV2:    nativeCompactionV2,
		BillingType:           billingType,
		UpstreamModelMismatch: upstreamModelMismatch,
	})
	entry, hit, err := dashboardModelStatsCache.GetOrLoadContext(ctx, key, func() (any, error) {
		loadCtx, cancel := reportCacheLoadContext(ctx)
		defer cancel()
		return h.dashboardService.GetModelStatsWithUsageFiltersBySource(loadCtx, startTime, endTime, usagestats.UsageLogFilters{
			UserID: userID, APIKeyID: apiKeyID, AccountID: accountID, GroupID: groupID,
			RequestType: requestType, Stream: stream, NativeCompactionV2: nativeCompactionV2, BillingType: billingType,
			UpstreamModelMismatch: upstreamModelMismatch,
		}, modelSource)
	})
	if err != nil {
		return nil, hit, err
	}
	stats, err := snapshotPayloadAs[[]usagestats.ModelStat](entry.Payload)
	return stats, hit, err
}

func (h *DashboardHandler) getGroupStatsCached(
	ctx context.Context,
	startTime, endTime time.Time,
	userID, apiKeyID, accountID, groupID int64,
	requestType *int16,
	stream *bool,
	nativeCompactionV2 *bool,
	billingType *int8,
	upstreamModelMismatch *bool,
) ([]usagestats.GroupStat, bool, error) {
	key := mustMarshalDashboardCacheKey(dashboardModelGroupCacheKey{
		StartTime:             startTime.UTC().Format(time.RFC3339),
		EndTime:               endTime.UTC().Format(time.RFC3339),
		UserID:                userID,
		APIKeyID:              apiKeyID,
		AccountID:             accountID,
		GroupID:               groupID,
		RequestType:           requestType,
		Stream:                stream,
		NativeCompactionV2:    nativeCompactionV2,
		BillingType:           billingType,
		UpstreamModelMismatch: upstreamModelMismatch,
	})
	entry, hit, err := dashboardGroupStatsCache.GetOrLoadContext(ctx, key, func() (any, error) {
		loadCtx, cancel := reportCacheLoadContext(ctx)
		defer cancel()
		return h.dashboardService.GetGroupStatsWithUsageFilters(loadCtx, startTime, endTime, usagestats.UsageLogFilters{
			UserID: userID, APIKeyID: apiKeyID, AccountID: accountID, GroupID: groupID,
			RequestType: requestType, Stream: stream, NativeCompactionV2: nativeCompactionV2, BillingType: billingType,
			UpstreamModelMismatch: upstreamModelMismatch,
		})
	})
	if err != nil {
		return nil, hit, err
	}
	stats, err := snapshotPayloadAs[[]usagestats.GroupStat](entry.Payload)
	return stats, hit, err
}

func (h *DashboardHandler) getAPIKeyUsageTrendCached(ctx context.Context, startTime, endTime time.Time, granularity string, limit int) ([]usagestats.APIKeyUsageTrendPoint, bool, error) {
	key := mustMarshalDashboardCacheKey(dashboardEntityTrendCacheKey{
		StartTime:   startTime.UTC().Format(time.RFC3339),
		EndTime:     endTime.UTC().Format(time.RFC3339),
		Granularity: granularity,
		Limit:       limit,
	})
	entry, hit, err := dashboardAPIKeysTrendCache.GetOrLoadContext(ctx, key, func() (any, error) {
		return h.dashboardService.GetAPIKeyUsageTrend(ctx, startTime, endTime, granularity, limit)
	})
	if err != nil {
		return nil, hit, err
	}
	trend, err := snapshotPayloadAs[[]usagestats.APIKeyUsageTrendPoint](entry.Payload)
	return trend, hit, err
}

func (h *DashboardHandler) getUserUsageTrendCached(ctx context.Context, startTime, endTime time.Time, granularity string, limit int) ([]usagestats.UserUsageTrendPoint, bool, error) {
	key := mustMarshalDashboardCacheKey(dashboardEntityTrendCacheKey{
		StartTime:   startTime.UTC().Format(time.RFC3339),
		EndTime:     endTime.UTC().Format(time.RFC3339),
		Granularity: granularity,
		Limit:       limit,
	})
	entry, hit, err := dashboardUsersTrendCache.GetOrLoadContext(ctx, key, func() (any, error) {
		return h.dashboardService.GetUserUsageTrend(ctx, startTime, endTime, granularity, limit)
	})
	if err != nil {
		return nil, hit, err
	}
	trend, err := snapshotPayloadAs[[]usagestats.UserUsageTrendPoint](entry.Payload)
	return trend, hit, err
}
