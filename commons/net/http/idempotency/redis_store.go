package idempotency

import (
	"context"
	"errors"
	"fmt"
	"time"

	libRedis "github.com/LerianStudio/lib-commons/v7/commons/redis"
	"github.com/redis/go-redis/v9"
)

var errInvalidStoreResult = errors.New("idempotency store returned an invalid result")

var redisAcquireScript = redis.NewScript(`
local current = redis.call("GET", KEYS[1])
if current then
  return {0, current}
end
local created = redis.call("SET", KEYS[1], ARGV[1], "PX", ARGV[2], "NX")
if created then
  return {1, ""}
end
current = redis.call("GET", KEYS[1])
if current then
  return {0, current}
end
return {-1, ""}
`)

var redisCompleteScript = redis.NewScript(`
local current = redis.call("GET", KEYS[1])
if not current or current ~= ARGV[1] then
  return 0
end
redis.call("SET", KEYS[1], ARGV[2], "PX", ARGV[3])
return 1
`)

var redisReleaseScript = redis.NewScript(`
local current = redis.call("GET", KEYS[1])
if not current or current ~= ARGV[1] then
  return 0
end
redis.call("DEL", KEYS[1])
return 1
`)

type redisStore struct {
	conn *libRedis.Client
}

func newRedisStore(conn *libRedis.Client) *redisStore {
	return &redisStore{conn: conn}
}

// NewRedisStore creates the canonical Redis-backed Store adapter. It returns
// nil for a nil connection so callers can pass the result to NewWithStore and
// retain its fail-closed missing-store behavior.
func NewRedisStore(conn *libRedis.Client) Store {
	if conn == nil {
		return nil
	}

	return newRedisStore(conn)
}

func (s *redisStore) Acquire(
	ctx context.Context,
	key string,
	candidate []byte,
	ttl time.Duration,
) ([]byte, bool, error) {
	ttlMillis, err := redisTTLMilliseconds(ttl)
	if err != nil {
		return nil, false, err
	}

	client, err := s.conn.GetClient(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("get redis client: %w", err)
	}

	result, err := redisAcquireScript.Run(ctx, client, []string{key}, candidate, ttlMillis).Result()
	if err != nil {
		return nil, false, fmt.Errorf("acquire idempotency key: %w", err)
	}

	return parseRedisAcquireResult(result)
}

func (s *redisStore) Complete(
	ctx context.Context,
	key string,
	expected, completed []byte,
	ttl time.Duration,
) (bool, error) {
	ttlMillis, err := redisTTLMilliseconds(ttl)
	if err != nil {
		return false, err
	}

	client, err := s.conn.GetClient(ctx)
	if err != nil {
		return false, fmt.Errorf("get redis client: %w", err)
	}

	applied, err := redisCompleteScript.Run(ctx, client, []string{key}, expected, completed, ttlMillis).Int64()
	if err != nil {
		return false, fmt.Errorf("complete idempotency key: %w", err)
	}

	return applied == 1, nil
}

func (s *redisStore) Release(ctx context.Context, key string, expected []byte) (bool, error) {
	client, err := s.conn.GetClient(ctx)
	if err != nil {
		return false, fmt.Errorf("get redis client: %w", err)
	}

	applied, err := redisReleaseScript.Run(ctx, client, []string{key}, expected).Int64()
	if err != nil {
		return false, fmt.Errorf("release idempotency key: %w", err)
	}

	return applied == 1, nil
}

func parseRedisAcquireResult(value any) ([]byte, bool, error) {
	result, ok := value.([]any)
	if !ok || len(result) != 2 {
		return nil, false, errInvalidStoreResult
	}

	code, ok := result[0].(int64)
	if !ok {
		return nil, false, errInvalidStoreResult
	}

	switch code {
	case 1:
		return nil, true, nil
	case 0:
		current, currentOK := result[1].(string)
		if !currentOK {
			return nil, false, errInvalidStoreResult
		}

		return []byte(current), false, nil
	default:
		return nil, false, errInvalidStoreResult
	}
}

func redisTTLMilliseconds(ttl time.Duration) (int64, error) {
	if ttl <= 0 {
		return 0, errInvalidTTL
	}

	milliseconds := ttl.Milliseconds()
	if milliseconds == 0 {
		return 1, nil
	}

	return milliseconds, nil
}
