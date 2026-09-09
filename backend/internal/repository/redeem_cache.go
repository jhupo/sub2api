package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

const (
	redeemRateLimitKeyPrefix = "redeem:ratelimit:v2:"
	redeemLockKeyPrefix      = "redeem:lock:"
)

const redeemAttemptWindowScript = `
local count = redis.call('INCR', KEYS[1])
if count == 1 or redis.call('TTL', KEYS[1]) < 0 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return count
`

// redeemRateLimitKey generates the Redis key for redeem attempt rate limiting.
func redeemRateLimitKey(userID int64) string {
	return fmt.Sprintf("%s%d", redeemRateLimitKeyPrefix, userID)
}

// redeemLockKey generates the Redis key for redeem code locking.
func redeemLockKey(code string) string {
	return redeemLockKeyPrefix + code
}

type redeemCache struct {
	rdb *redis.Client
}

func NewRedeemCache(rdb *redis.Client) service.RedeemCache {
	return &redeemCache{rdb: rdb}
}

func (c *redeemCache) GetRedeemAttemptCount(ctx context.Context, userID int64) (int, error) {
	key := redeemRateLimitKey(userID)
	count, err := c.rdb.Get(ctx, key).Int()
	if err == redis.Nil {
		return 0, nil
	}
	return count, err
}

func (c *redeemCache) IncrementRedeemAttemptCount(ctx context.Context, userID int64, window time.Duration) error {
	key := redeemRateLimitKey(userID)
	_, err := c.rdb.Eval(ctx, redeemAttemptWindowScript, []string{key}, window.Milliseconds()).Result()
	return err
}

func (c *redeemCache) AcquireRedeemLock(ctx context.Context, code string, ttl time.Duration) (bool, error) {
	key := redeemLockKey(code)
	return c.rdb.SetNX(ctx, key, 1, ttl).Result()
}

func (c *redeemCache) ReleaseRedeemLock(ctx context.Context, code string) error {
	key := redeemLockKey(code)
	return c.rdb.Del(ctx, key).Err()
}
