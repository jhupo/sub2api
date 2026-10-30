package admin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type cacheAdjustmentStatsRepo struct {
	service.UsageLogRepository
	start, end time.Time
	zone       string
	accountID  int64
}

func (r *cacheAdjustmentStatsRepo) GetCacheReadAdjustmentDaily(_ context.Context, start, end time.Time, zone string, id int64) ([]service.CacheReadAdjustmentDay, error) {
	r.start, r.end, r.zone, r.accountID = start, end, zone, id
	return []service.CacheReadAdjustmentDay{}, nil
}

func TestCacheReadAdjustmentDailyValidatesRangeAndUsesCalendarDays(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &cacheAdjustmentStatsRepo{}
	h := &UsageHandler{usageService: service.NewUsageService(repo, nil, nil, nil)}
	router := gin.New()
	router.GET("/daily", h.CacheReadAdjustmentDaily)
	for _, query := range []string{
		"start_date=bad", "timezone=Invalid/Zone", "account_id=-1", "account_id=bad",
		"start_date=2026-10-30&end_date=2026-10-29",
		"start_date=2026-01-01&end_date=2026-10-30",
	} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/daily?"+query, nil))
		require.Equal(t, http.StatusBadRequest, w.Code, query)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/daily?start_date=2026-03-08&end_date=2026-03-08&timezone=America%2FNew_York&account_id=29", nil))
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 23*time.Hour, repo.end.Sub(repo.start))
	require.Equal(t, int64(29), repo.accountID)
	require.Equal(t, "America/New_York", repo.zone)
}
