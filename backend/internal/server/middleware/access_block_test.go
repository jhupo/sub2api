package middleware

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	accessmiddleware "github.com/Wei-Shaw/sub2api/internal/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func newAccessBlockPanelLimiter(t *testing.T, rdb *redis.Client, panelJSON, blockJSON string) (*PanelRateLimiter, *AccessBlockGuard) {
	t.Helper()
	repo := &panelRateLimitStubRepo{values: map[string]string{
		service.SettingKeyPanelRateLimitSettings: panelJSON,
		service.SettingKeyAccessBlockSettings:    blockJSON,
	}}
	settingService := service.NewSettingService(repo, &config.Config{})
	return &PanelRateLimiter{
		limiter:        &fakePanelAllower{},
		settingService: settingService,
	}, NewAccessBlockGuard(rdb, settingService)
}

func TestAccessBlockPanelBlacklistObserves429WithoutChangingResponse(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })
	p, accessBlock := newAccessBlockPanelLimiter(t, rdb,
		`{"enabled":true,"user_rpm":0,"heavy_rpm":0,"exempt_admin":true,"public_ip_rpm":1}`,
		`{"enabled":true,"login_protection_enabled":true,"login_failure_threshold":10,"login_failure_window_seconds":600,"login_temporary_block_seconds":3600,"blocked_headers":[],"panel_blacklist_enabled":true,"panel_blacklist_threshold":2,"panel_blacklist_window_seconds":60}`,
	)
	router := newPanelTestRouter(accessBlock.Wrap(p.PublicIP()), nil)

	require.Equal(t, http.StatusOK, performPanelRequest(router, "203.0.113.9:1000").Code)
	require.Equal(t, http.StatusTooManyRequests, performPanelRequest(router, "203.0.113.9:1000").Code)
	require.Equal(t, http.StatusTooManyRequests, performPanelRequest(router, "203.0.113.9:1000").Code)
	require.Equal(t, http.StatusForbidden, performPanelRequest(router, "203.0.113.9:1000").Code)

	blocked, source, err := accessmiddleware.IsPermanentlyAccessBlocked(context.Background(), rdb, "203.0.113.9")
	require.NoError(t, err)
	require.True(t, blocked)
	require.Equal(t, "panel_rate_limit", source)
}

func TestAccessBlockPanelAdminExemptionAndFeatureSwitch(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })
	require.NoError(t, accessmiddleware.AddAccessIPBlock(context.Background(), rdb, "203.0.113.50", true, 0))

	p, accessBlock := newAccessBlockPanelLimiter(t, rdb,
		`{"enabled":true,"user_rpm":10,"heavy_rpm":10,"exempt_admin":true,"public_ip_rpm":10}`,
		`{"enabled":true,"login_protection_enabled":true,"login_failure_threshold":10,"login_failure_window_seconds":600,"login_temporary_block_seconds":3600,"blocked_headers":[],"panel_blacklist_enabled":false,"panel_blacklist_threshold":10,"panel_blacklist_window_seconds":600}`,
	)
	admin := newPanelTestRouter(accessBlock.Wrap(p.Global()), &panelTestIdentity{userID: 1, role: service.RoleAdmin})
	user := newPanelTestRouter(accessBlock.Wrap(p.Global()), &panelTestIdentity{userID: 2, role: service.RoleUser})
	require.Equal(t, http.StatusOK, performPanelRequest(admin, "203.0.113.50:1000").Code)
	require.Equal(t, http.StatusForbidden, performPanelRequest(user, "203.0.113.50:1000").Code)

	disabled := &AccessBlockGuard{redis: rdb, settings: func(context.Context) service.AccessBlockSettings {
		settings := *service.DefaultAccessBlockSettings()
		settings.Enabled = false
		return settings
	}}
	disabledUser := newPanelTestRouter(disabled.Wrap(func(c *gin.Context) { c.Status(http.StatusOK) }), &panelTestIdentity{userID: 3, role: service.RoleUser})
	require.Equal(t, http.StatusOK, performPanelRequest(disabledUser, "203.0.113.50:1000").Code)
}

func TestAccessBlockPanelBlacklistDisabledLeavesRateLimitBehaviorUnchanged(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })
	p, accessBlock := newAccessBlockPanelLimiter(t, rdb,
		`{"enabled":true,"user_rpm":0,"heavy_rpm":0,"exempt_admin":true,"public_ip_rpm":1}`,
		`{"enabled":true,"login_protection_enabled":true,"login_failure_threshold":10,"login_failure_window_seconds":600,"login_temporary_block_seconds":3600,"blocked_headers":[],"panel_blacklist_enabled":false,"panel_blacklist_threshold":1,"panel_blacklist_window_seconds":60}`,
	)
	router := newPanelTestRouter(accessBlock.Wrap(p.PublicIP()), nil)

	require.Equal(t, http.StatusOK, performPanelRequest(router, "203.0.113.60:1000").Code)
	require.Equal(t, http.StatusTooManyRequests, performPanelRequest(router, "203.0.113.60:1000").Code)
	require.Equal(t, http.StatusTooManyRequests, performPanelRequest(router, "203.0.113.60:1000").Code)
	blocked, _, err := accessmiddleware.IsPermanentlyAccessBlocked(context.Background(), rdb, "203.0.113.60")
	require.NoError(t, err)
	require.False(t, blocked)
}

func TestAccessBlockPanelGuardFailsOpenWhenRedisIsUnavailable(t *testing.T) {
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", DialTimeout: 20 * time.Millisecond})
	t.Cleanup(func() { require.NoError(t, rdb.Close()) })
	guard := NewAccessBlockGuard(rdb, nil)
	router := newPanelTestRouter(guard.Wrap(func(c *gin.Context) { c.Status(http.StatusOK) }), nil)

	require.Equal(t, http.StatusOK, performPanelRequest(router, "203.0.113.61:1000").Code)
}
