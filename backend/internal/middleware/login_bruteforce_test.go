package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newLoginBruteForceTestGuard(rdb *redis.Client) *LoginBruteForceGuard {
	return &LoginBruteForceGuard{
		redis: rdb,
		settings: func(context.Context) service.PanelRateLimitSettings {
			return service.PanelRateLimitSettings{
				LoginBruteForceEnabled:       true,
				LoginBruteForceThreshold:     2,
				LoginBruteForceWindowSeconds: 60,
				LoginBruteForceBlockSeconds:  30,
			}
		},
	}
}

func TestLoginBruteForceBlocksAfterThreshold(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(newLoginBruteForceTestGuard(rdb).Handler())
	router.POST("/login", func(c *gin.Context) { c.Status(http.StatusUnauthorized) })

	for i := 0; i < 2; i++ {
		request := httptest.NewRequest(http.MethodPost, "/login", nil)
		request.RemoteAddr = "203.0.113.10:1234"
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		require.Equal(t, http.StatusUnauthorized, response.Code)
	}

	request := httptest.NewRequest(http.MethodPost, "/login", nil)
	request.RemoteAddr = "203.0.113.10:1234"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusTooManyRequests, response.Code)
	require.Equal(t, "30", response.Header().Get("Retry-After"))
	require.Contains(t, response.Body.String(), "invalid login attempts")
}

func TestLoginBruteForceSuccessClearsFailureCountAndIPsAreIndependent(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })

	gin.SetMode(gin.TestMode)
	status := http.StatusUnauthorized
	router := gin.New()
	router.Use(newLoginBruteForceTestGuard(rdb).Handler())
	router.POST("/login", func(c *gin.Context) { c.Status(status) })

	request := httptest.NewRequest(http.MethodPost, "/login", nil)
	request.RemoteAddr = "203.0.113.10:1234"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusUnauthorized, response.Code)

	status = http.StatusOK
	request = httptest.NewRequest(http.MethodPost, "/login", nil)
	request.RemoteAddr = "203.0.113.10:1234"
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)

	status = http.StatusUnauthorized
	for _, ip := range []string{"203.0.113.10:1234", "203.0.113.11:1234"} {
		request = httptest.NewRequest(http.MethodPost, "/login", nil)
		request.RemoteAddr = ip
		response = httptest.NewRecorder()
		router.ServeHTTP(response, request)
		require.Equal(t, http.StatusUnauthorized, response.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/login", nil)
	request.RemoteAddr = "203.0.113.11:1234"
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusUnauthorized, response.Code, "different IP has its own failure bucket")
}

func TestLoginBruteForceOnlyCountsUnauthorizedAndFailsClosedOnRedisError(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })

	gin.SetMode(gin.TestMode)
	status := http.StatusInternalServerError
	router := gin.New()
	router.Use(newLoginBruteForceTestGuard(rdb).Handler())
	router.POST("/login", func(c *gin.Context) { c.Status(status) })

	for i := 0; i < 3; i++ {
		request := httptest.NewRequest(http.MethodPost, "/login", nil)
		request.RemoteAddr = "203.0.113.12:1234"
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		require.Equal(t, http.StatusInternalServerError, response.Code)
	}

	status = http.StatusUnauthorized
	request := httptest.NewRequest(http.MethodPost, "/login", nil)
	request.RemoteAddr = "203.0.113.12:1234"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusUnauthorized, response.Code)

	badRedis := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 20 * time.Millisecond})
	t.Cleanup(func() { require.NoError(t, badRedis.Close()) })
	closedRouter := gin.New()
	closedRouter.Use(newLoginBruteForceTestGuard(badRedis).Handler())
	closedRouter.POST("/login", func(c *gin.Context) { c.Status(http.StatusOK) })
	request = httptest.NewRequest(http.MethodPost, "/login", nil)
	request.RemoteAddr = "203.0.113.13:1234"
	response = httptest.NewRecorder()
	closedRouter.ServeHTTP(response, request)
	require.Equal(t, http.StatusTooManyRequests, response.Code)
}
