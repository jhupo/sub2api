package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAccountHandlerTestRejectsInvalidPrompt(t *testing.T) {
	gin.SetMode(gin.TestMode)
	longPrompt, err := json.Marshal(map[string]string{"prompt": strings.Repeat("x", 4001)})
	require.NoError(t, err)
	for _, body := range []string{`{"prompt":42}`, `{"prompt":"broken`, string(longPrompt)} {
		router := gin.New()
		handler := &AccountHandler{}
		router.POST("/accounts/:id/test", handler.Test)
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/accounts/42/test", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(recorder, request)
		require.Equal(t, http.StatusBadRequest, recorder.Code)
	}
}
