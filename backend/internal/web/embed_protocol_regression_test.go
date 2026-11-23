//go:build embed

package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestEmbeddedFrontendBareAPIAndPageRoutes(t *testing.T) {
	for _, path := range []string{"/chat/completions", "/embeddings", "/messages/count_tokens", "/models/test", "/videos", "/tts", "/stt", "/custom-voices", "/custom-voices/voice/audio", "/realtime", "/web_search", "/x_search"} {
		require.True(t, shouldBypassEmbeddedFrontend(path), path)
	}
	for _, path := range []string{"/model-plaza", "/custom/page", "/monitor", "/setup"} {
		require.False(t, shouldBypassEmbeddedFrontend(path), path)
	}
}

func TestFrontendHTMLUsesCurrentNonceWithMatchingETag(t *testing.T) {
	provider := &mockSettingsProvider{settings: map[string]string{"site_name": "Test"}}
	server, err := NewFrontendServer(provider)
	require.NoError(t, err)
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set(middleware.CSPNonceKey, c.GetHeader("X-Test-Nonce")); c.Next() })
	router.Use(server.Middleware())
	first := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("X-Test-Nonce", "first-nonce")
	router.ServeHTTP(first, request)
	require.Equal(t, http.StatusOK, first.Code)
	require.Contains(t, first.Body.String(), `nonce="first-nonce"`)
	second := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Set("If-None-Match", first.Header().Get("ETag"))
	request.Header.Set("X-Test-Nonce", "second-nonce")
	router.ServeHTTP(second, request)
	require.Equal(t, http.StatusOK, second.Code)
	require.Contains(t, second.Body.String(), `nonce="second-nonce"`)
	require.NotContains(t, second.Body.String(), `nonce="first-nonce"`)
	require.Equal(t, "no-cache, no-store, must-revalidate", second.Header().Get("Cache-Control"))
	require.Equal(t, "no-store", second.Header().Get("CDN-Cache-Control"))
}
