package idempotency

import (
	"context"
	"errors"
	"fmt"
	"time"

	libRedis "github.com/LerianStudio/lib-commons/v6/commons/redis"
	"github.com/redis/go-redis/v9"
)

var (
	errInvalidStoreResult             = errors.New("idempotency store returned an invalid result")
	errLegacyResponseNotFound         = errors.New("legacy idempotency replay response not found")
	errLegacyBridgeClusterUnsupported = errors.New("legacy idempotency bridge does not support Redis Cluster")
)

type legacyResponseReader interface {
	ReadLegacyResponse(ctx context.Context, key string) ([]byte, error)
}

type legacyBridgeStore interface {
	legacyResponseReader
	AcquireBridge(
		ctx context.Context,
		keys bridgeKeyPair,
		candidate bridgeRecordPair,
		owner string,
		ttl time.Duration,
	) (bridgeRecordPair, bool, error)
	CompleteBridge(
		ctx context.Context,
		keys bridgeKeyPair,
		expected, completed bridgeRecordPair,
		response []byte,
		owner string,
		ttl time.Duration,
	) (bool, error)
	ReleaseBridge(ctx context.Context, keys bridgeKeyPair, expected bridgeRecordPair, owner string) (bool, error)
}

type bridgeKeyPair struct {
	legacy    string
	canonical string
}

func (p bridgeKeyPair) dualNamespace() bool {
	return p.legacy != p.canonical
}

type bridgeRecordPair struct {
	legacy    []byte
	canonical []byte
}

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

var redisBridgeAcquireSingleScript = redis.NewScript(`
local current = redis.call("GET", KEYS[1])
if current then
  return {0, current, ""}
end
local created = redis.call("MSETNX", KEYS[1], ARGV[1], KEYS[2], ARGV[2])
if created == 1 then
  redis.call("PEXPIRE", KEYS[1], ARGV[3])
  redis.call("PEXPIRE", KEYS[2], ARGV[3])
  return {1, "", ""}
end
current = redis.call("GET", KEYS[1])
if current then
  return {0, current, ""}
end
return {-1, "", ""}
`)

var redisBridgeAcquireDualScript = redis.NewScript(`
local legacy = redis.call("GET", KEYS[1])
local current = redis.call("GET", KEYS[2])
if legacy or current then
  return {0, legacy or "", current or ""}
end
local created = redis.call("MSETNX", KEYS[1], ARGV[1], KEYS[2], ARGV[2], KEYS[3], ARGV[3])
if created == 1 then
  redis.call("PEXPIRE", KEYS[1], ARGV[4])
  redis.call("PEXPIRE", KEYS[2], ARGV[4])
  redis.call("PEXPIRE", KEYS[3], ARGV[4])
  return {1, "", ""}
end
legacy = redis.call("GET", KEYS[1])
current = redis.call("GET", KEYS[2])
if legacy or current then
  return {0, legacy or "", current or ""}
end
return {-1, "", ""}
`)

var redisBridgeCompleteSingleScript = redis.NewScript(`
local legacy = redis.call("GET", KEYS[1])
local owner = redis.call("GET", KEYS[3])
if not legacy or legacy ~= ARGV[1] or not owner or owner ~= ARGV[4] then
  return 0
end
redis.call("MSET", KEYS[1], ARGV[2], KEYS[2], ARGV[3])
redis.call("PEXPIRE", KEYS[1], ARGV[5])
redis.call("PEXPIRE", KEYS[2], ARGV[5])
redis.call("DEL", KEYS[3])
return 1
`)

var redisBridgeCompleteDualScript = redis.NewScript(`
local legacy = redis.call("GET", KEYS[1])
local current = redis.call("GET", KEYS[2])
local owner = redis.call("GET", KEYS[4])
if not legacy or legacy ~= ARGV[1] or not current or current ~= ARGV[2] or
   not owner or owner ~= ARGV[6] then
  return 0
end
redis.call("MSET", KEYS[1], ARGV[3], KEYS[2], ARGV[4], KEYS[3], ARGV[5])
redis.call("PEXPIRE", KEYS[1], ARGV[7])
redis.call("PEXPIRE", KEYS[2], ARGV[7])
redis.call("PEXPIRE", KEYS[3], ARGV[7])
redis.call("DEL", KEYS[4])
return 1
`)

var redisBridgeReleaseSingleScript = redis.NewScript(`
local legacy = redis.call("GET", KEYS[1])
local owner = redis.call("GET", KEYS[3])
if not legacy or legacy ~= ARGV[1] or not owner or owner ~= ARGV[2] then
  return 0
end
redis.call("DEL", KEYS[1], KEYS[2], KEYS[3])
return 1
`)

var redisBridgeReleaseDualScript = redis.NewScript(`
local legacy = redis.call("GET", KEYS[1])
local current = redis.call("GET", KEYS[2])
local owner = redis.call("GET", KEYS[4])
if not legacy or legacy ~= ARGV[1] or not current or current ~= ARGV[2] or
   not owner or owner ~= ARGV[3] then
  return 0
end
redis.call("DEL", KEYS[1], KEYS[2], KEYS[3], KEYS[4])
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

func (s *redisStore) ReadLegacyResponse(ctx context.Context, key string) ([]byte, error) {
	client, err := s.conn.GetClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("get redis client: %w", err)
	}

	response, err := client.Get(ctx, legacyResponseKey(key)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, errLegacyResponseNotFound
	}

	if err != nil {
		return nil, fmt.Errorf("get legacy idempotency response: %w", err)
	}

	return response, nil
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

func (s *redisStore) AcquireBridge(
	ctx context.Context,
	keys bridgeKeyPair,
	candidate bridgeRecordPair,
	owner string,
	ttl time.Duration,
) (bridgeRecordPair, bool, error) {
	ttlMillis, err := redisTTLMilliseconds(ttl)
	if err != nil {
		return bridgeRecordPair{}, false, err
	}

	client, err := s.conn.GetClient(ctx)
	if err != nil {
		return bridgeRecordPair{}, false, fmt.Errorf("get redis client: %w", err)
	}

	if err = validateLegacyBridgeClient(client); err != nil {
		return bridgeRecordPair{}, false, err
	}

	var result any
	if keys.dualNamespace() {
		result, err = redisBridgeAcquireDualScript.Run(ctx, client,
			[]string{keys.legacy, keys.canonical, bridgeOwnerKey(keys.legacy)},
			candidate.legacy, candidate.canonical, owner, ttlMillis).Result()
	} else {
		result, err = redisBridgeAcquireSingleScript.Run(ctx, client,
			[]string{keys.legacy, bridgeOwnerKey(keys.legacy)},
			candidate.legacy, owner, ttlMillis).Result()
	}

	if err != nil {
		return bridgeRecordPair{}, false, fmt.Errorf("acquire legacy bridge idempotency key: %w", err)
	}

	return parseRedisBridgeAcquireResult(result)
}

func (s *redisStore) CompleteBridge(
	ctx context.Context,
	keys bridgeKeyPair,
	expected, completed bridgeRecordPair,
	response []byte,
	owner string,
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

	if err = validateLegacyBridgeClient(client); err != nil {
		return false, err
	}

	var applied int64
	if keys.dualNamespace() {
		applied, err = redisBridgeCompleteDualScript.Run(ctx, client,
			[]string{keys.legacy, keys.canonical, legacyResponseKey(keys.legacy), bridgeOwnerKey(keys.legacy)},
			expected.legacy, expected.canonical, completed.legacy, completed.canonical,
			response, owner, ttlMillis).Int64()
	} else {
		applied, err = redisBridgeCompleteSingleScript.Run(ctx, client,
			[]string{keys.legacy, legacyResponseKey(keys.legacy), bridgeOwnerKey(keys.legacy)},
			expected.legacy, completed.legacy, response, owner, ttlMillis).Int64()
	}

	if err != nil {
		return false, fmt.Errorf("complete legacy bridge idempotency key: %w", err)
	}

	return applied == 1, nil
}

func (s *redisStore) ReleaseBridge(
	ctx context.Context,
	keys bridgeKeyPair,
	expected bridgeRecordPair,
	owner string,
) (bool, error) {
	client, err := s.conn.GetClient(ctx)
	if err != nil {
		return false, fmt.Errorf("get redis client: %w", err)
	}

	if err = validateLegacyBridgeClient(client); err != nil {
		return false, err
	}

	var applied int64
	if keys.dualNamespace() {
		applied, err = redisBridgeReleaseDualScript.Run(ctx, client,
			[]string{keys.legacy, keys.canonical, legacyResponseKey(keys.legacy), bridgeOwnerKey(keys.legacy)},
			expected.legacy, expected.canonical, owner).Int64()
	} else {
		applied, err = redisBridgeReleaseSingleScript.Run(ctx, client,
			[]string{keys.legacy, legacyResponseKey(keys.legacy), bridgeOwnerKey(keys.legacy)},
			expected.legacy, owner).Int64()
	}

	if err != nil {
		return false, fmt.Errorf("release legacy bridge idempotency key: %w", err)
	}

	return applied == 1, nil
}

// Reserved suffixes derive companion Redis keys from a primary idempotency
// key. Client-supplied keys ending in one of these would collide with the
// derived keys of another idempotency key, so the middleware rejects them
// before any Redis access.
const (
	legacyResponseKeySuffix = ":response"
	bridgeOwnerKeySuffix    = ":bridge-owner"
)

func legacyResponseKey(key string) string {
	return key + legacyResponseKeySuffix
}

func bridgeOwnerKey(key string) string {
	return key + bridgeOwnerKeySuffix
}

func parseRedisBridgeAcquireResult(value any) (bridgeRecordPair, bool, error) {
	result, ok := value.([]any)
	if !ok || len(result) != 3 {
		return bridgeRecordPair{}, false, errInvalidStoreResult
	}

	code, ok := result[0].(int64)
	if !ok {
		return bridgeRecordPair{}, false, errInvalidStoreResult
	}

	switch code {
	case 1:
		return bridgeRecordPair{}, true, nil
	case 0:
		legacy, legacyOK := result[1].(string)

		canonical, canonicalOK := result[2].(string)
		if !legacyOK || !canonicalOK || (legacy == "" && canonical == "") {
			return bridgeRecordPair{}, false, errInvalidStoreResult
		}

		return bridgeRecordPair{legacy: []byte(legacy), canonical: []byte(canonical)}, false, nil
	default:
		return bridgeRecordPair{}, false, errInvalidStoreResult
	}
}

func validateLegacyBridgeClient(client redis.UniversalClient) error {
	if _, clustered := client.(*redis.ClusterClient); clustered {
		return errLegacyBridgeClusterUnsupported
	}

	return nil
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
