package middleware

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

const accessBlockPrefix = "access_block:"

const accessBlockedContextKey = "access_blocked"

const (
	loginBlockTempPrefix = accessBlockPrefix + "temporary:"
	loginBlockDenyPrefix = accessBlockPrefix + "deny:"
	panelViolationPrefix = accessBlockPrefix + "panel_violation:"
	blockSourceLogin     = "login_failures"
	blockSourceManual    = "manual"
	blockSourcePanel     = "panel_rate_limit"
	maxTemporaryBlock    = 7 * 24 * time.Hour
)

// AccessBlock describes an active IP block stored in Redis.
type AccessBlock struct {
	IP               string
	RemainingSeconds int64
	Permanent        bool
	Source           string
}

var loginBruteForceRecordScript = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
local ttl = redis.call('PTTL', KEYS[1])
if count == 1 or ttl == -1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
if count >= tonumber(ARGV[3]) then
  redis.call('SET', KEYS[2], ARGV[4], 'PX', ARGV[2])
  redis.call('DEL', KEYS[1])
  return {count, 1}
end
return {count, 0}
`)

var loginBruteForceRecord = func(ctx context.Context, client *redis.Client, failureKey, blockKey string, window, block time.Duration, threshold int) (bool, error) {
	values, err := loginBruteForceRecordScript.Run(ctx, client, []string{failureKey, blockKey},
		window.Milliseconds(), block.Milliseconds(), threshold, blockSourceLogin).Slice()
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

var panelBlacklistRecordScript = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
local ttl = redis.call('PTTL', KEYS[1])
if count == 1 or ttl == -1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
if count >= tonumber(ARGV[2]) then
  redis.call('SET', KEYS[2], ARGV[3])
  redis.call('DEL', KEYS[3])
  redis.call('DEL', KEYS[1])
  return 1
end
return 0
`)

func normalizeBlockIP(ip string) (string, error) {
	parsed := net.ParseIP(strings.TrimSpace(ip))
	if parsed == nil {
		return "", fmt.Errorf("invalid IP address")
	}
	return parsed.String(), nil
}

// CheckLoginIPBlock checks permanent and login-only temporary blocks.
func CheckLoginIPBlock(ctx context.Context, client *redis.Client, ip string) (blocked bool, retryAfter time.Duration, permanent bool, source string, err error) {
	if client == nil {
		return false, 0, false, "", fmt.Errorf("redis client is unavailable")
	}
	ip, err = normalizeBlockIP(ip)
	if err != nil {
		return false, 0, false, "", err
	}
	denyTTL, err := client.PTTL(ctx, loginBlockDenyPrefix+ip).Result()
	if err != nil {
		return false, 0, false, "", err
	}
	if denyTTL == -1 || denyTTL > 0 {
		source = accessBlockSource(ctx, client, loginBlockDenyPrefix+ip, blockSourceManual)
		return true, max(denyTTL, 0), true, source, nil
	}
	tempTTL, err := client.PTTL(ctx, loginBlockTempPrefix+ip).Result()
	if err != nil {
		return false, 0, false, "", err
	}
	if tempTTL > 0 {
		source = accessBlockSource(ctx, client, loginBlockTempPrefix+ip, blockSourceLogin)
		return true, tempTTL, false, source, nil
	}
	if tempTTL == -1 {
		source = accessBlockSource(ctx, client, loginBlockTempPrefix+ip, blockSourceLogin)
		return true, 0, false, source, nil
	}
	return false, 0, false, "", nil
}

// IsPermanentlyAccessBlocked checks the deny list used by login and panel APIs.
func IsPermanentlyAccessBlocked(ctx context.Context, client *redis.Client, ip string) (bool, string, error) {
	if client == nil {
		return false, "", fmt.Errorf("redis client is unavailable")
	}
	ip, err := normalizeBlockIP(ip)
	if err != nil {
		return false, "", err
	}
	ttl, err := client.PTTL(ctx, loginBlockDenyPrefix+ip).Result()
	if err != nil {
		return false, "", err
	}
	if ttl == -1 || ttl > 0 {
		return true, accessBlockSource(ctx, client, loginBlockDenyPrefix+ip, blockSourceManual), nil
	}
	return false, "", nil
}

func accessBlockSource(ctx context.Context, client *redis.Client, key, fallback string) string {
	value, err := client.Get(ctx, key).Result()
	if err != nil || strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// AddAccessIPBlock creates a manual temporary login block or a permanent
// login-and-panel deny entry.
func AddAccessIPBlock(ctx context.Context, client *redis.Client, ip string, permanent bool, duration time.Duration) error {
	if client == nil {
		return fmt.Errorf("redis client is unavailable")
	}
	ip, err := normalizeBlockIP(ip)
	if err != nil {
		return err
	}
	if permanent {
		pipe := client.TxPipeline()
		pipe.Del(ctx, loginBlockTempPrefix+ip)
		pipe.Set(ctx, loginBlockDenyPrefix+ip, blockSourceManual, 0)
		_, err := pipe.Exec(ctx)
		return err
	}
	if duration < 10*time.Second || duration > maxTemporaryBlock {
		return fmt.Errorf("temporary block duration must be between 10 seconds and 7 days")
	}
	pipe := client.TxPipeline()
	pipe.Del(ctx, loginBlockDenyPrefix+ip)
	pipe.Set(ctx, loginBlockTempPrefix+ip, blockSourceManual, duration)
	_, err = pipe.Exec(ctx)
	return err
}

// RecordPanelBlacklistViolation atomically upgrades repeated panel 429s to a
// permanent IP block. It is intentionally opt-in and only called for public IPs.
func RecordPanelBlacklistViolation(ctx context.Context, client *redis.Client, ip string, window time.Duration, threshold int) (bool, error) {
	if client == nil {
		return false, fmt.Errorf("redis client is unavailable")
	}
	ip, err := normalizeBlockIP(ip)
	if err != nil {
		return false, err
	}
	if window < time.Second || threshold < 1 {
		return false, fmt.Errorf("invalid panel blacklist policy")
	}
	value, err := panelBlacklistRecordScript.Run(ctx, client,
		[]string{panelViolationPrefix + ip, loginBlockDenyPrefix + ip, loginBlockTempPrefix + ip},
		window.Milliseconds(), threshold, blockSourcePanel).Int()
	return value == 1, err
}

// LoginBruteForceGuard blocks an IP after repeated invalid-password responses.
// It shares the trusted client-IP resolver with the regular auth limiter.
type LoginBruteForceGuard struct {
	redis          *redis.Client
	settingService *service.SettingService
	settings       func(context.Context) service.AccessBlockSettings
}

func NewLoginBruteForceGuard(redisClient *redis.Client, settingService *service.SettingService) *LoginBruteForceGuard {
	return &LoginBruteForceGuard{redis: redisClient, settingService: settingService}
}

// ListAccessBlocks returns active blocks. Redis is the source of truth;
// expired keys are skipped so the admin page never shows stale entries.
func ListAccessBlocks(ctx context.Context, client *redis.Client) ([]AccessBlock, error) {
	if client == nil {
		return nil, fmt.Errorf("redis client is unavailable")
	}
	blocks := make([]AccessBlock, 0)
	for _, pattern := range []string{loginBlockTempPrefix + "*", loginBlockDenyPrefix + "*"} {
		var cursor uint64
		for {
			keys, next, err := client.Scan(ctx, cursor, pattern, 200).Result()
			if err != nil {
				return nil, err
			}
			for _, key := range keys {
				prefix, fallbackSource := loginBlockTempPrefix, blockSourceLogin
				if strings.HasPrefix(key, loginBlockDenyPrefix) {
					prefix, fallbackSource = loginBlockDenyPrefix, blockSourceManual
				}
				ip := strings.TrimPrefix(key, prefix)
				if net.ParseIP(ip) == nil {
					continue
				}
				ttl, err := client.PTTL(ctx, key).Result()
				if err != nil {
					return nil, err
				}
				if ttl == -2 {
					continue
				}
				blocks = append(blocks, AccessBlock{
					IP: ip,
					RemainingSeconds: func() int64 {
						if ttl < 0 {
							return 0
						}
						return int64((ttl + time.Second - 1) / time.Second)
					}(),
					Permanent: prefix == loginBlockDenyPrefix,
					Source:    accessBlockSource(ctx, client, key, fallbackSource),
				})
			}
			cursor = next
			if cursor == 0 {
				break
			}
		}
	}
	sort.Slice(blocks, func(i, j int) bool {
		if blocks[i].Permanent != blocks[j].Permanent {
			return blocks[i].Permanent
		}
		return blocks[i].IP < blocks[j].IP
	})
	return blocks, nil
}

// RemoveAccessBlock removes active blocks and their related counters.
func RemoveAccessBlock(ctx context.Context, client *redis.Client, ip string) error {
	if client == nil {
		return fmt.Errorf("redis client is unavailable")
	}
	ip, err := normalizeBlockIP(ip)
	if err != nil {
		return err
	}
	return client.Del(ctx,
		loginBlockTempPrefix+ip,
		loginBlockDenyPrefix+ip,
		accessBlockPrefix+"login_failure:"+ip,
		panelViolationPrefix+ip,
	).Err()
}

func (g *LoginBruteForceGuard) Handler() gin.HandlerFunc {
	return func(c *gin.Context) {
		if g == nil || g.redis == nil {
			c.Next()
			return
		}

		settings := service.DefaultAccessBlockSettings()
		if g.settings != nil {
			configured := g.settings(c.Request.Context())
			settings = &configured
		} else if g.settingService != nil {
			cached := g.settingService.GetAccessBlockSettingsCached(c.Request.Context())
			settings = &cached
		}
		if !settings.Enabled {
			c.Next()
			return
		}
		for _, rule := range settings.BlockedHeaders {
			for _, value := range c.Request.Header.Values(rule.Name) {
				if value == rule.Value {
					SetAccessBlocked(c)
					abortPermanentlyBlocked(c)
					return
				}
			}
		}
		ip, ipErr := normalizeBlockIP(clientIPForRateLimit(c))
		if ipErr != nil {
			if settings.LoginProtectionEnabled {
				slog.Warn("login client IP resolution failed, blocking request", "error", ipErr)
				abortLoginBruteForce(c, time.Duration(settings.LoginTemporaryBlockSeconds)*time.Second)
				return
			}
			c.Next()
			return
		}

		blocked, retryAfter, permanent, source, err := CheckLoginIPBlock(c.Request.Context(), g.redis, ip)
		if err != nil {
			if settings.LoginProtectionEnabled {
				slog.Warn("login access block check failed, blocking request", "error", err)
				abortLoginBruteForce(c, time.Duration(settings.LoginTemporaryBlockSeconds)*time.Second)
				return
			}
			c.Next()
			return
		}
		if blocked && (permanent || settings.LoginProtectionEnabled || source == blockSourceManual) {
			SetAccessBlocked(c)
			if permanent {
				abortPermanentlyBlocked(c)
			} else {
				if retryAfter <= 0 {
					retryAfter = time.Duration(settings.LoginTemporaryBlockSeconds) * time.Second
				}
				abortLoginBruteForce(c, retryAfter)
			}
			return
		}
		if !settings.LoginProtectionEnabled || settings.LoginFailureThreshold < 1 ||
			settings.LoginFailureWindowSeconds < 1 || settings.LoginTemporaryBlockSeconds < 1 {
			c.Next()
			return
		}
		blockKey := loginBlockTempPrefix + ip
		c.Next()
		status := c.Writer.Status()
		ctx := context.WithoutCancel(c.Request.Context())
		failureKey := accessBlockPrefix + "login_failure:" + ip
		if status == http.StatusUnauthorized {
			blocked, recordErr := loginBruteForceRecord(
				ctx,
				g.redis,
				failureKey,
				blockKey,
				time.Duration(settings.LoginFailureWindowSeconds)*time.Second,
				time.Duration(settings.LoginTemporaryBlockSeconds)*time.Second,
				settings.LoginFailureThreshold,
			)
			if recordErr != nil {
				slog.Warn("login brute-force record failed", "error", recordErr)
			} else if blocked {
				SetAccessBlocked(c)
				slog.Info("login IP temporarily blocked after repeated invalid credentials", "ip", ip)
			}
		} else if status >= http.StatusOK && status < http.StatusMultipleChoices {
			if err := g.redis.Del(ctx, failureKey).Err(); err != nil {
				slog.Warn("login brute-force reset failed", "error", err)
			}
		}
	}
}

// SetAccessBlocked marks the current request for the audit middleware.
func SetAccessBlocked(c *gin.Context) {
	if c != nil {
		c.Set(accessBlockedContextKey, true)
	}
}

// IsAccessBlocked reports whether the current request was rejected by, or
// triggered, an access-block rule.
func IsAccessBlocked(c *gin.Context) bool {
	return c != nil && c.GetBool(accessBlockedContextKey)
}

func abortPermanentlyBlocked(c *gin.Context) {
	c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
		"error":   "access blocked",
		"message": "Access from this client has been blocked",
	})
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
