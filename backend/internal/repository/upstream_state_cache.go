package repository

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Bounded Redis cache, shared by all gateway instances. Each operation uses
// Redis time and a single Lua transaction. No opaque state enters SQL or logs.
type upstreamStateCache struct{ rdb *redis.Client }

func NewUpstreamStateCache(rdb *redis.Client) service.UpstreamStateStore {
	return &upstreamStateCache{rdb: rdb}
}

var upstreamStateKeys = []string{"upstream_state:{cache}:values", "upstream_state:{cache}:expiry", "upstream_state:{cache}:epoch", "upstream_state:{cache}:sequence"}

// Business requests only read the managed value. They never advance refresh
// metadata or create state from their response headers.
func (c *upstreamStateCache) Get(ctx context.Context, id string) (*service.UpstreamStateRecord, error) {
	raw, err := c.rdb.HGet(ctx, upstreamStateKeys[0], id).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var record service.UpstreamStateRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, err
	}
	return &record, nil
}

const upstreamStatePrune = `
local clock = redis.call('TIME')
local now = tonumber(clock[1])*1000 + math.floor(tonumber(clock[2])/1000)
local expired = redis.call('ZRANGEBYSCORE', KEYS[2], '-inf', now)
for _, id in ipairs(expired) do redis.call('HDEL', KEYS[1], id) end
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', now)
`

var upstreamStateBegin = redis.NewScript(upstreamStatePrune + `
redis.call('SET', KEYS[3], ARGV[2], 'NX')
local sequence = redis.call('INCR', KEYS[4])
return {redis.call('HGET', KEYS[1], ARGV[1]) or '', redis.call('GET', KEYS[3]) or '0', tostring(sequence)}
`)

func (c *upstreamStateCache) Begin(ctx context.Context, id string) (*service.UpstreamStateRecord, service.UpstreamStateTicket, error) {
	var ticket service.UpstreamStateTicket
	values, err := upstreamStateBegin.Run(ctx, c.rdb, upstreamStateKeys, id, uuid.NewString()).StringSlice()
	if err != nil {
		return nil, ticket, err
	}
	ticket.Epoch = values[1]
	ticket.Sequence, err = strconv.ParseInt(values[2], 10, 64)
	if err != nil {
		return nil, ticket, err
	}
	if values[0] == "" {
		return nil, ticket, nil
	}
	var record service.UpstreamStateRecord
	if err = json.Unmarshal([]byte(values[0]), &record); err != nil {
		return nil, ticket, err
	}
	return &record, ticket, nil
}

const upstreamStateWrite = `
redis.call('HSET', KEYS[1], incoming.id, cjson.encode(incoming))
redis.call('ZADD', KEYS[2], incoming.purge_at, incoming.id)
local overflow = redis.call('ZCARD', KEYS[2]) - 4096
if overflow > 0 then
  local victims = redis.call('ZRANGE', KEYS[2], 0, overflow-1)
  for _, id in ipairs(victims) do redis.call('HDEL', KEYS[1], id); redis.call('ZREM', KEYS[2], id) end
end
-- Physical expiry also cleans idle installations without future traffic.
redis.call('EXPIRE', KEYS[1], 3660)
redis.call('EXPIRE', KEYS[2], 3660)
return redis.call('HEXISTS', KEYS[1], incoming.id)
`

var upstreamStateSave = redis.NewScript(upstreamStatePrune + `
if (redis.call('GET', KEYS[3]) or '0') ~= ARGV[1] then return 0 end
local incoming = cjson.decode(ARGV[2])
if incoming.purge_at <= now then return 0 end
local old = redis.call('HGET', KEYS[1], incoming.id)
if old then
  local previous = cjson.decode(old)
  -- Refresh outcome is independent of ordinary HTTP/WS observations. Traffic
  -- must neither erase a refresh error nor move the refresh retry deadline.
  if (previous.last_refresh_at or 0) > (incoming.last_refresh_at or 0) then
    incoming.last_refresh_at = previous.last_refresh_at
    incoming.last_error = previous.last_error
  end
  if previous.sequence >= incoming.sequence then
    incoming.sequence = previous.sequence
    incoming.checked_at = previous.checked_at
    incoming.observed_length = previous.observed_length
    incoming.validation = previous.validation
  end
  if previous.state ~= '' then
    previous.sequence = incoming.sequence
    previous.checked_at = incoming.checked_at
    previous.observed_length = incoming.observed_length
    previous.validation = incoming.validation
    previous.last_refresh_at = incoming.last_refresh_at
    previous.last_error = incoming.last_error
    incoming = previous
  end
  -- Without a cached state, accept the first usable value even if a newer
  -- invalid observation has arrived. Keep the newest observation metadata.
end
` + upstreamStateWrite)

func (c *upstreamStateCache) Save(ctx context.Context, r service.UpstreamStateRecord, ticket service.UpstreamStateTicket) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return upstreamStateSave.Run(ctx, c.rdb, upstreamStateKeys, ticket.Epoch, string(raw)).Err()
}

var upstreamStateReplace = redis.NewScript(upstreamStatePrune + `
if (redis.call('GET', KEYS[3]) or '0') ~= ARGV[1] then return 0 end
local incoming = cjson.decode(ARGV[2])
if incoming.purge_at <= now then return 0 end
` + upstreamStateWrite)

func (c *upstreamStateCache) Replace(ctx context.Context, r service.UpstreamStateRecord, ticket service.UpstreamStateTicket) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	replaced, err := upstreamStateReplace.Run(ctx, c.rdb, upstreamStateKeys, ticket.Epoch, string(raw)).Int64()
	if err != nil {
		return err
	}
	if replaced != 1 {
		return service.ErrUpstreamStateReplaceRejected
	}
	return nil
}

var upstreamStateList = redis.NewScript(upstreamStatePrune + `return redis.call('HVALS', KEYS[1])`)

func (c *upstreamStateCache) List(ctx context.Context) ([]service.UpstreamStateRecord, error) {
	values, err := upstreamStateList.Run(ctx, c.rdb, upstreamStateKeys).StringSlice()
	if err != nil {
		return nil, err
	}
	result := make([]service.UpstreamStateRecord, 0, len(values))
	for _, raw := range values {
		var r service.UpstreamStateRecord
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			return nil, err
		}
		result = append(result, r)
	}
	return result, nil
}

var upstreamStateClear = redis.NewScript(`
redis.call('SET', KEYS[3], ARGV[2])
if ARGV[1] == '' then
  redis.call('DEL', KEYS[1], KEYS[2])
else
  redis.call('HDEL', KEYS[1], ARGV[1])
  redis.call('ZREM', KEYS[2], ARGV[1])
end
return 1
`)

func (c *upstreamStateCache) Clear(ctx context.Context, id string) error {
	return upstreamStateClear.Run(ctx, c.rdb, upstreamStateKeys, id, uuid.NewString()).Err()
}

const upstreamStateLockPrefix = "upstream_state:{cache}:lock:"

func (c *upstreamStateCache) TryLock(ctx context.Context, key string, ttl time.Duration) (string, bool, error) {
	owner := uuid.NewString()
	ok, err := c.rdb.SetNX(ctx, upstreamStateLockPrefix+key, owner, ttl).Result()
	return owner, ok, err
}

var upstreamStateUnlock = redis.NewScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  return redis.call('DEL', KEYS[1])
end
return 0
`)

func (c *upstreamStateCache) Unlock(ctx context.Context, key, owner string) error {
	return upstreamStateUnlock.Run(ctx, c.rdb, []string{upstreamStateLockPrefix + key}, owner).Err()
}
