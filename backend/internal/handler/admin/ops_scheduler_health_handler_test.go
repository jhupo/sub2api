package admin

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGetSchedulerFreshnessHealth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/ops/scheduler-freshness/health", nil)
	handler := NewOpsHandler(service.NewOpsService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil))

	handler.GetSchedulerFreshnessHealth(ctx)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Contains(t, recorder.Body.String(), `"RequestTotal"`)
	require.Contains(t, recorder.Body.String(), `"ProjectionErrorTotal"`)
}

func TestGetSchedulerFreshnessHealthRequiresOpsMonitoring(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/v1/admin/ops/scheduler-freshness/health", nil)
	handler := NewOpsHandler(service.NewOpsService(nil, nil, &config.Config{}, nil, nil, nil, nil, nil, nil, nil, nil))

	handler.GetSchedulerFreshnessHealth(ctx)

	require.Equal(t, http.StatusNotFound, recorder.Code)
}
