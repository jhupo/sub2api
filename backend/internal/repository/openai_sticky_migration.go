package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

var claimStickyMigration = redis.NewScript(`
local bound = redis.call('GET', KEYS[1])
if bound and bound ~= ARGV[1] then return {bound, '', ARGV[1], ARGV[5]} end
local target = redis.call('HGET', KEYS[2], 'target')
if target then return {target, redis.call('HGET', KEYS[2], 'version'), redis.call('HGET', KEYS[2], 'source'), redis.call('HGET', KEYS[2], 'model')} end
if ARGV[1] == ARGV[2] then return {ARGV[2], '', ARGV[1], ARGV[5]} end
redis.call('HSET', KEYS[2], 'source', ARGV[1], 'target', ARGV[2], 'version', ARGV[3], 'model', ARGV[5])
redis.call('PEXPIRE', KEYS[2], ARGV[4])
return {ARGV[2], ARGV[3], ARGV[1], ARGV[5]}
`)

var bindStickyIfAbsent = redis.NewScript(`
local bound = redis.call('GET', KEYS[1])
if not bound or bound == ARGV[1] then
  redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
end
return 1
`)

func (c *gatewayCache) SetOpenAIStickySessionIfAbsent(ctx context.Context, group int64, session string, accountID int64, ttl time.Duration) error {
	return bindStickyIfAbsent.Run(ctx, c.rdb, []string{buildSessionKey(group, session)}, accountID, ttl.Milliseconds()).Err()
}

var commitStickyMigration = redis.NewScript(`
if redis.call('HGET', KEYS[2], 'version') ~= ARGV[3] then return 0 end
if redis.call('HGET', KEYS[2], 'source') ~= ARGV[1] or redis.call('HGET', KEYS[2], 'target') ~= ARGV[2] then return 0 end
local bound = redis.call('GET', KEYS[1])
if bound and bound ~= ARGV[1] then return 0 end
redis.call('SET', KEYS[1], ARGV[2], 'PX', ARGV[4])
redis.call('DEL', KEYS[2])
return 1
`)

var refreshStickyMigration = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'version') ~= ARGV[1] then return 0 end
redis.call('PEXPIRE', KEYS[1], ARGV[2])
return 1
`)

var abortStickyMigration = redis.NewScript(`
if redis.call('HGET', KEYS[1], 'version') ~= ARGV[1] then return 0 end
return redis.call('DEL', KEYS[1])
`)

func (c *gatewayCache) GetOpenAIStickyMigration(ctx context.Context, group int64, session string) (*service.OpenAIStickyMigration, error) {
	values, err := c.rdb.HMGet(ctx, buildSessionKey(group, session)+":migration", "target", "version", "source", "model").Result()
	if err != nil {
		return nil, err
	}
	if values[0] == nil {
		return nil, nil
	}
	return decodeStickyMigration(values)
}

func decodeStickyMigration(values []any) (*service.OpenAIStickyMigration, error) {
	if len(values) != 4 {
		return nil, fmt.Errorf("invalid sticky migration result")
	}
	var source, target int64
	if _, err := fmt.Sscan(fmt.Sprint(values[0]), &target); err != nil {
		return nil, err
	}
	if _, err := fmt.Sscan(fmt.Sprint(values[2]), &source); err != nil {
		return nil, err
	}
	return &service.OpenAIStickyMigration{SourceID: source, TargetID: target, Version: fmt.Sprint(values[1]), Model: fmt.Sprint(values[3])}, nil
}

func (c *gatewayCache) ClaimOpenAIStickyMigration(ctx context.Context, group int64, session string, proposal service.OpenAIStickyMigration, ttl time.Duration) (*service.OpenAIStickyMigration, error) {
	key := buildSessionKey(group, session)
	values, err := claimStickyMigration.Run(ctx, c.rdb, []string{key, key + ":migration"}, proposal.SourceID, proposal.TargetID, proposal.Version, ttl.Milliseconds(), proposal.Model).Slice()
	if err != nil {
		return nil, err
	}
	return decodeStickyMigration(values)
}

func (c *gatewayCache) RefreshOpenAIStickyMigration(ctx context.Context, group int64, session, version string, ttl time.Duration) (bool, error) {
	result, err := refreshStickyMigration.Run(ctx, c.rdb, []string{buildSessionKey(group, session) + ":migration"}, version, ttl.Milliseconds()).Int()
	return result == 1, err
}

func (c *gatewayCache) AbortOpenAIStickyMigration(ctx context.Context, group int64, session, version string) error {
	return abortStickyMigration.Run(ctx, c.rdb, []string{buildSessionKey(group, session) + ":migration"}, version).Err()
}

func (c *gatewayCache) CommitOpenAIStickyMigration(ctx context.Context, group int64, session string, migration service.OpenAIStickyMigration, ttl time.Duration) (bool, error) {
	key := buildSessionKey(group, session)
	result, err := commitStickyMigration.Run(ctx, c.rdb, []string{key, key + ":migration"}, migration.SourceID, migration.TargetID, migration.Version, ttl.Milliseconds()).Int()
	return result == 1, err
}
