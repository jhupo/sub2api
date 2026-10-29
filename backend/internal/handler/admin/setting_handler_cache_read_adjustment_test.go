package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type cacheAdjustmentHandlerRepo struct {
	service.SettingRepository
	raw    string
	setErr error
}

func (r *cacheAdjustmentHandlerRepo) GetValue(context.Context, string) (string, error) {
	return r.raw, nil
}
func (r *cacheAdjustmentHandlerRepo) Set(_ context.Context, _ string, value string) error {
	if r.setErr != nil {
		return r.setErr
	}
	r.raw = value
	return nil
}

func TestCacheReadAdjustmentAdminSettings(t *testing.T) {
	repo := &cacheAdjustmentHandlerRepo{}
	svc := service.NewSettingService(repo, nil)
	h := &SettingHandler{settingService: svc}
	router := gin.New()
	router.PUT("/settings", h.UpdateCacheReadAdjustmentSettings)
	router.GET("/settings", h.GetCacheReadAdjustmentSettings)
	request := func(method, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/settings", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		return w
	}
	for _, body := range []string{`{"percent":101}`, `{"percent":1.5}`, `{"percent":10,"account_ids":[-1]}`} {
		require.Equal(t, http.StatusBadRequest, request(http.MethodPut, body).Code)
	}
	require.Empty(t, repo.raw)
	w := request(http.MethodGet, "")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, int64(10), gjson.Get(w.Body.String(), "data.percent").Int())
	w = request(http.MethodPut, `{"enabled":true,"percent":10,"account_ids":[8,7,7]}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, 10, svc.CacheReadAdjustmentPercent(context.Background(), 7))
	w = request(http.MethodGet, "")
	require.Equal(t, `[7,8]`, gjson.Get(w.Body.String(), "data.account_ids").Raw)
	repo.setErr = errors.New("storage unavailable")
	require.Equal(t, http.StatusInternalServerError, request(http.MethodPut, `{"enabled":false,"percent":10,"account_ids":[7]}`).Code)
	require.Equal(t, 10, svc.CacheReadAdjustmentPercent(context.Background(), 7))
}
