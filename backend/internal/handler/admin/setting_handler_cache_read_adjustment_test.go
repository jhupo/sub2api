package admin

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
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

type cacheAdjustmentAccountStub struct{}

func (cacheAdjustmentAccountStub) GetByIDs(_ context.Context, ids []int64) ([]*service.Account, error) {
	accounts := make([]*service.Account, 0, len(ids))
	for _, id := range ids {
		if id < 100 {
			accounts = append(accounts, &service.Account{ID: id, Name: "Account " + strconv.FormatInt(id, 10), Platform: "openai"})
		}
	}
	return accounts, nil
}

func TestCacheReadAdjustmentIncrementalAccounts(t *testing.T) {
	repo := &cacheAdjustmentHandlerRepo{raw: `{"enabled":true,"percent":25,"account_ids":[7,999]}`}
	svc := service.NewSettingService(repo, nil)
	h := &SettingHandler{settingService: svc, cacheAdjustmentAccounts: cacheAdjustmentAccountStub{}}
	router := gin.New()
	router.PATCH("/settings", h.PatchCacheReadAdjustmentSettings)
	router.GET("/settings/accounts", h.ListCacheReadAdjustmentAccounts)
	router.POST("/settings/accounts", h.AddCacheReadAdjustmentAccounts)
	router.DELETE("/settings/accounts", h.RemoveCacheReadAdjustmentAccounts)
	request := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		return w
	}
	w := request(http.MethodPatch, "/settings", `{"enabled":false}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.False(t, gjson.Get(w.Body.String(), "data.enabled").Bool())
	require.Equal(t, int64(25), gjson.Get(w.Body.String(), "data.percent").Int())
	require.Equal(t, `[7,999]`, gjson.Get(w.Body.String(), "data.account_ids").Raw)
	w = request(http.MethodPost, "/settings/accounts", `{"account_ids":[8,8,7]}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, `[7,8,999]`, gjson.Get(w.Body.String(), "data.account_ids").Raw)
	w = request(http.MethodGet, "/settings/accounts?page=2&page_size=2", "")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, int64(3), gjson.Get(w.Body.String(), "data.total").Int())
	require.Equal(t, int64(999), gjson.Get(w.Body.String(), "data.items.0.id").Int())
	require.True(t, gjson.Get(w.Body.String(), "data.items.0.missing").Bool())
	require.False(t, gjson.Get(w.Body.String(), "data.items.0.credentials").Exists())
	before := repo.raw
	for _, body := range []string{`{}`, `{"account_ids":[]}`, `{"account_ids":[-1]}`, `{"account_ids":[9,999]}`} {
		require.Equal(t, http.StatusBadRequest, request(http.MethodPost, "/settings/accounts", body).Code, body)
		require.Equal(t, before, repo.raw)
	}
	for _, body := range []string{`{}`, `{"percent":101}`, `{"percent":1.5}`, `{"add_account_ids":[7],"remove_account_ids":[7]}`} {
		require.Equal(t, http.StatusBadRequest, request(http.MethodPatch, "/settings", body).Code, body)
		require.Equal(t, before, repo.raw)
	}
	w = request(http.MethodDelete, "/settings/accounts", `{"account_ids":[7,999,42]}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, `[8]`, gjson.Get(w.Body.String(), "data.account_ids").Raw)
	require.False(t, gjson.Get(w.Body.String(), "data.enabled").Bool())
	require.Equal(t, int64(25), gjson.Get(w.Body.String(), "data.percent").Int())
	w = request(http.MethodDelete, "/settings/accounts", `{"account_ids":[7,999,42]}`)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.Equal(t, `[8]`, gjson.Get(w.Body.String(), "data.account_ids").Raw)
	repo.setErr = errors.New("storage unavailable")
	require.Equal(t, http.StatusInternalServerError, request(http.MethodPatch, "/settings", `{"enabled":true}`).Code)
	require.Zero(t, svc.CacheReadAdjustmentPercent(context.Background(), 8))
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
