package middleware

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"time"

	accessmiddleware "github.com/Wei-Shaw/sub2api/internal/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

const panelRateLimitExceededContextKey = "panel_rate_limit_exceeded"

// AccessBlockGuard enforces permanent panel blocks and observes panel 429s.
// The panel limiter owns only quota decisions; this middleware owns every
// block-list read and write.
type AccessBlockGuard struct {
	redis          *redis.Client
	settingService *service.SettingService
	settings       func(context.Context) service.AccessBlockSettings
}

func NewAccessBlockGuard(redisClient *redis.Client, settingService *service.SettingService) *AccessBlockGuard {
	return &AccessBlockGuard{redis: redisClient, settingService: settingService}
}

func (g *AccessBlockGuard) Handler() gin.HandlerFunc {
	return g.Wrap(func(c *gin.Context) { c.Next() })
}

func (g *AccessBlockGuard) Wrap(next gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		if next == nil {
			c.Next()
			return
		}
		if g == nil || g.redis == nil {
			next(c)
			return
		}
		settings := service.DefaultAccessBlockSettings()
		if g.settings != nil {
			configured := g.settings(c.Request.Context())
			settings = &configured
		} else if g.settingService != nil {
			configured := g.settingService.GetAccessBlockSettingsCached(c.Request.Context())
			settings = &configured
		}
		if !settings.Enabled || isAccessBlockExemptAdmin(c) {
			next(c)
			return
		}

		ip := SecurityClientIP(c)
		if net.ParseIP(ip) != nil {
			blocked, _, err := accessmiddleware.IsPermanentlyAccessBlocked(c.Request.Context(), g.redis, ip)
			if err != nil {
				slog.Warn("permanent access block check failed, allowing request", "error", err)
			} else if blocked {
				accessmiddleware.SetAccessBlocked(c)
				AbortWithError(c, http.StatusForbidden, "ACCESS_BLOCKED", "Access from this client has been blocked")
				return
			}
		}

		next(c)
		if !consumePanelRateLimitExceeded(c) || !settings.PanelBlacklistEnabled || !isPubliclyRoutableClientIP(ip) {
			return
		}
		blocked, err := accessmiddleware.RecordPanelBlacklistViolation(
			context.WithoutCancel(c.Request.Context()),
			g.redis,
			ip,
			time.Duration(settings.PanelBlacklistWindowSeconds)*time.Second,
			settings.PanelBlacklistThreshold,
		)
		if err != nil {
			slog.Warn("panel blacklist violation record failed", "error", err)
			return
		}
		if blocked {
			slog.Info("client permanently blocked after repeated panel rate-limit violations", "ip", ip)
		}
	}
}

func isAccessBlockExemptAdmin(c *gin.Context) bool {
	role, ok := GetUserRoleFromContext(c)
	return ok && role == service.RoleAdmin
}

func markPanelRateLimitExceeded(c *gin.Context) {
	if c != nil {
		c.Set(panelRateLimitExceededContextKey, true)
	}
}

func consumePanelRateLimitExceeded(c *gin.Context) bool {
	if c == nil || !c.GetBool(panelRateLimitExceededContextKey) {
		return false
	}
	c.Set(panelRateLimitExceededContextKey, false)
	return true
}
