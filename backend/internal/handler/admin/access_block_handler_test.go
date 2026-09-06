package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAccessBlockSettingsPatchPreservesUnspecifiedPolicy(t *testing.T) {
	initial := service.DefaultAccessBlockSettings()
	initial.LoginFailureThreshold = 23
	initial.BlockedHeaders = []service.AccessBlockedHeaderRule{{Name: "User-Agent", Value: "scanner"}}
	encoded, err := json.Marshal(initial)
	require.NoError(t, err)

	repo := newTestSettingRepo()
	repo.values[service.SettingKeyAccessBlockSettings] = string(encoded)
	settingService := service.NewSettingService(repo, &config.Config{})
	handler := NewAccessBlockHandler(settingService, nil)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.PATCH("/settings", handler.UpdateSettings)
	request := httptest.NewRequest(http.MethodPatch, "/settings", bytes.NewBufferString(`{"enabled":false}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	require.Equal(t, http.StatusOK, recorder.Code)

	stored, err := settingService.GetAccessBlockSettings(context.Background())
	require.NoError(t, err)
	require.False(t, stored.Enabled)
	require.Equal(t, 23, stored.LoginFailureThreshold)
	require.Equal(t, initial.BlockedHeaders, stored.BlockedHeaders)
}
