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
		settings: func(context.Context) service.AccessBlockSettings {
			return service.AccessBlockSettings{
				Enabled:                    true,
				LoginProtectionEnabled:     true,
				LoginFailureThreshold:      2,
				LoginFailureWindowSeconds:  60,
				LoginTemporaryBlockSeconds: 30,
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

func TestLoginBruteForceBlockManagement(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })

	ctx := context.Background()
	key := "access_block:temporary:203.0.113.20"
	require.NoError(t, rdb.Set(ctx, key, blockSourceLogin, 45*time.Second).Err())
	require.NoError(t, rdb.Set(ctx, "access_block:login_failure:203.0.113.20", "1", time.Minute).Err())

	blocks, err := ListAccessBlocks(ctx, rdb)
	require.NoError(t, err)
	require.Len(t, blocks, 1)
	require.Equal(t, "203.0.113.20", blocks[0].IP)
	require.GreaterOrEqual(t, blocks[0].RemainingSeconds, int64(44))

	require.NoError(t, RemoveAccessBlock(ctx, rdb, "203.0.113.20"))
	require.Zero(t, rdb.Exists(ctx, key).Val())
	require.Zero(t, rdb.Exists(ctx, "access_block:login_failure:203.0.113.20").Val())
	require.Error(t, RemoveAccessBlock(ctx, rdb, "not-an-ip"))
}

func TestLoginBruteForceHeaderAndPermanentBlocks(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })

	guard := newLoginBruteForceTestGuard(rdb)
	guard.settings = func(context.Context) service.AccessBlockSettings {
		return service.AccessBlockSettings{
			Enabled:                    true,
			LoginProtectionEnabled:     false,
			LoginFailureThreshold:      2,
			LoginFailureWindowSeconds:  60,
			LoginTemporaryBlockSeconds: 30,
			BlockedHeaders:             []service.AccessBlockedHeaderRule{{Name: "User-Agent", Value: "scanner"}},
		}
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(guard.Handler())
	router.POST("/login", func(c *gin.Context) { c.Status(http.StatusOK) })

	request := httptest.NewRequest(http.MethodPost, "/login", nil)
	request.RemoteAddr = "203.0.113.30:1234"
	request.Header.Set("User-Agent", "scanner")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusForbidden, response.Code)

	require.NoError(t, AddAccessIPBlock(context.Background(), rdb, "203.0.113.31", true, 0))
	request = httptest.NewRequest(http.MethodPost, "/login", nil)
	request.RemoteAddr = "203.0.113.31:1234"
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusForbidden, response.Code)

	blocks, err := ListAccessBlocks(context.Background(), rdb)
	require.NoError(t, err)
	require.Len(t, blocks, 1)
	require.True(t, blocks[0].Permanent)
	require.Equal(t, "manual", blocks[0].Source)
}

func TestLoginAccessBlockFeatureSwitchDisablesEnforcement(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })
	require.NoError(t, AddAccessIPBlock(context.Background(), rdb, "203.0.113.32", true, 0))

	guard := newLoginBruteForceTestGuard(rdb)
	guard.settings = func(context.Context) service.AccessBlockSettings {
		settings := *service.DefaultAccessBlockSettings()
		settings.Enabled = false
		return settings
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(guard.Handler())
	router.POST("/login", func(c *gin.Context) { c.Status(http.StatusOK) })

	request := httptest.NewRequest(http.MethodPost, "/login", nil)
	request.RemoteAddr = "203.0.113.32:1234"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusOK, response.Code)
}

func TestManualTemporaryBlockWorksWhenAutomaticLoginProtectionIsDisabled(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })
	require.NoError(t, AddAccessIPBlock(context.Background(), rdb, "203.0.113.33", false, 30*time.Second))

	guard := newLoginBruteForceTestGuard(rdb)
	guard.settings = func(context.Context) service.AccessBlockSettings {
		settings := *service.DefaultAccessBlockSettings()
		settings.LoginProtectionEnabled = false
		return settings
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(guard.Handler())
	router.POST("/login", func(c *gin.Context) { c.Status(http.StatusOK) })

	request := httptest.NewRequest(http.MethodPost, "/login", nil)
	request.RemoteAddr = "203.0.113.33:1234"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	require.Equal(t, http.StatusTooManyRequests, response.Code)
}

func TestPanelBlacklistViolationCreatesPermanentBlock(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })

	blocked, err := RecordPanelBlacklistViolation(context.Background(), rdb, "203.0.113.40", time.Minute, 2)
	require.NoError(t, err)
	require.False(t, blocked)
	blocked, err = RecordPanelBlacklistViolation(context.Background(), rdb, "203.0.113.40", time.Minute, 2)
	require.NoError(t, err)
	require.True(t, blocked)
	permanent, source, err := IsPermanentlyAccessBlocked(context.Background(), rdb, "203.0.113.40")
	require.NoError(t, err)
	require.True(t, permanent)
	require.Equal(t, "panel_rate_limit", source)
}
