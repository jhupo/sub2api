package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestGeminiPricingErrorAfterKeepaliveIsSSE(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	c.Writer.WriteHeaderNow()
	googleError(c, http.StatusServiceUnavailable, "Model pricing is not configured")
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), "data:")
	require.Contains(t, w.Body.String(), `"code":503`)
	require.Contains(t, w.Body.String(), `"status":"UNAVAILABLE"`)
	require.True(t, w.Flushed)
}

func TestGeminiPricingErrorBeforeStreamIsJSON(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	googleError(c, http.StatusServiceUnavailable, "Model pricing is not configured")
	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	require.Contains(t, w.Header().Get("Content-Type"), "application/json")
}
