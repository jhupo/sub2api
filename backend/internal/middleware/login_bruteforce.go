package middleware

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

const loginBruteForcePrefix = "login_bruteforce:"

var loginBruteForceRecordScript = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
local ttl = redis.call('PTTL', KEYS[1])
if count == 1 or ttl == -1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
if count >= tonumber(ARGV[3]) then
  redis.call('SET', KEYS[2], '1', 'PX', ARGV[2])
  redis.call('DEL', KEYS[1])
  return {count, 1}
end
return {count, 0}
`)

var loginBruteForceRecord = func(ctx context.Context, client *redis.Client, failureKey, blockKey string, window, block time.Duration, threshold int) (bool, error) {
	values, err := loginBruteForceRecordScript.Run(ctx, client, []string{failureKey, blockKey},
		window.Milliseconds(), block.Milliseconds(), threshold).Slice()
	if err != nil {
		return false, err
	}
	if len(values) != 2 {
		return false, fmt.Errorf("login brute-force script returned %d values", len(values))
	}
	blocked, err := parseInt64(values[1])
	if err != nil {
		return false, err
	}
	return blocked == 1, nil
}

// LoginBruteForceGuard blocks an IP after repeated invalid-password responses.
// It shares the trusted client-IP resolver with the regular auth limiter.
type LoginBruteForceGuard struct {
	redis          *redis.Client
	settingService *service.SettingService
	settings       func(context.Context) service.PanelRateLimitSettings
}

func NewLoginBruteForceGuard(redisClient *redis.Client, settingService *service.SettingService) *LoginBruteForceGuard {
	return &LoginBruteForceGuard{redis: redisClient, settingService: settingService}
}

func (g *LoginBruteForceGuard) Handler() gin.HandlerFunc {
	return func(c *gin.Context) {
		if g == nil || g.redis == nil {
			c.Next()
			return
		}

		settings := service.DefaultPanelRateLimitSettings()
		if g.settings != nil {
			configured := g.settings(c.Request.Context())
			settings = &configured
		} else if g.settingService != nil {
			cached := g.settingService.GetPanelRateLimitSettingsCached(c.Request.Context())
			settings = &cached
		}
		if !settings.LoginBruteForceEnabled || settings.LoginBruteForceThreshold < 1 ||
			settings.LoginBruteForceWindowSeconds < 1 || settings.LoginBruteForceBlockSeconds < 1 {
			c.Next()
			return
		}

		ip := clientIPForRateLimit(c)
		blockKey := loginBruteForcePrefix + "block:" + ip
		ttl, err := g.redis.PTTL(c.Request.Context(), blockKey).Result()
		if err != nil {
			slog.Warn("login brute-force check failed, blocking request", "error", err)
			abortLoginBruteForce(c, time.Duration(settings.LoginBruteForceBlockSeconds)*time.Second)
			return
		}
		if ttl > 0 {
			abortLoginBruteForce(c, ttl)
			return
		}
		if ttl == -1 {
			// A block key without TTL is a corrupted state; fail closed and
			// give operators the configured duration to repair Redis state.
			abortLoginBruteForce(c, time.Duration(settings.LoginBruteForceBlockSeconds)*time.Second)
			return
		}

		c.Next()
		status := c.Writer.Status()
		ctx := context.WithoutCancel(c.Request.Context())
		failureKey := loginBruteForcePrefix + "fail:" + ip
		if status == http.StatusUnauthorized {
			blocked, recordErr := loginBruteForceRecord(
				ctx,
				g.redis,
				failureKey,
				blockKey,
				time.Duration(settings.LoginBruteForceWindowSeconds)*time.Second,
				time.Duration(settings.LoginBruteForceBlockSeconds)*time.Second,
				settings.LoginBruteForceThreshold,
			)
			if recordErr != nil {
				slog.Warn("login brute-force record failed", "error", recordErr)
			} else if blocked {
				slog.Info("login IP temporarily blocked after repeated invalid credentials", "ip", ip)
			}
		} else if status >= http.StatusOK && status < http.StatusMultipleChoices {
			if err := g.redis.Del(ctx, failureKey).Err(); err != nil {
				slog.Warn("login brute-force reset failed", "error", err)
			}
		}
	}
}

func abortLoginBruteForce(c *gin.Context, retryAfter time.Duration) {
	if retryAfter <= 0 {
		retryAfter = time.Second
	}
	seconds := int64(retryAfter / time.Second)
	if retryAfter%time.Second != 0 {
		seconds++
	}
	c.Header("Retry-After", strconv.FormatInt(seconds, 10))
	c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
		"error":   "rate limit exceeded",
		"message": "Too many invalid login attempts, please try again later",
	})
}
