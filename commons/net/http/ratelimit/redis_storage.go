package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	constant "github.com/LerianStudio/lib-commons/v5/commons/constants"
	"github.com/LerianStudio/lib-commons/v5/commons/internal/nilcheck"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v5/commons/opentelemetry"
	tmcore "github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	tmvalkey "github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/valkey"
	"github.com/LerianStudio/lib-observability/assert"
	"github.com/LerianStudio/lib-observability/log"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	libRedis "github.com/LerianStudio/lib-commons/v5/commons/redis"
)

const (
	keyPrefix     = "ratelimit:"
	scanBatchSize = 100

	// luaIncrExpire is an atomic Lua script that increments the counter, sets expiry on the
	// first request in a window, and returns both the current count and the remaining TTL in
	// milliseconds. Executed atomically by Redis — no other command can interleave, eliminating
	// the race condition present in sequential INCR + EXPIRE calls. Returning the TTL from the
	// same script avoids an extra PTTL roundtrip and ensures the value is consistent with the
	// counter read above.
	//
	// PTTL sentinels:
	//   -1: key exists but has no TTL (e.g., TTL inadvertently cleared by an external SET).
	//   -2: key no longer exists (e.g., deleted between INCR and PTTL).
	// When either sentinel is observed, the script repairs the expiry before returning so
	// callers never report a finite Retry-After for an immortal counter.
	luaIncrExpire = `
local count = redis.call('INCR', KEYS[1])
if count == 1 then
    redis.call('PEXPIRE', KEYS[1], tonumber(ARGV[1]))
    return {count, tonumber(ARGV[1])}
end
local pttl = redis.call('PTTL', KEYS[1])
if pttl < 0 then
    redis.call('PEXPIRE', KEYS[1], tonumber(ARGV[1]))
    return {count, tonumber(ARGV[1])}
end
return {count, pttl}
`
)

var luaIncrExpireScript = redis.NewScript(luaIncrExpire)

// ErrInvalidWindow is returned by Increment when the supplied window is non-positive.
// PEXPIRE rejects non-positive values, so the storage layer enforces this contract
// up-front rather than surfacing an opaque Redis error to the caller.
var ErrInvalidWindow = errors.New("ratelimit increment window must be > 0")

// ErrStorageUnavailable is returned when Redis storage is nil or not initialized.
var ErrStorageUnavailable = errors.New("ratelimit redis storage is unavailable")

// ErrInvalidTenantContext is returned when a tenant ID in context is invalid.
// The raw tenant ID is intentionally omitted to prevent unsafe identifiers from
// leaking into logs, spans, or returned error strings.
var ErrInvalidTenantContext = errors.New("ratelimit tenant context is invalid")

// RedisStorageOption is a functional option for configuring RedisStorage.
type RedisStorageOption func(*RedisStorage)

// WithRedisStorageLogger provides a structured logger for assertion and error logging.
func WithRedisStorageLogger(l log.Logger) RedisStorageOption {
	return func(s *RedisStorage) {
		if !nilcheck.Interface(l) {
			s.logger = l
		}
	}
}

func (storage *RedisStorage) unavailableStorageError(operation string) error {
	var logger log.Logger
	if storage != nil {
		logger = storage.logger
	}

	asserter := assert.New(context.Background(), logger, "http.ratelimit", operation)
	if err := asserter.Never(context.Background(), "ratelimit redis storage is unavailable"); err != nil {
		return ErrStorageUnavailable
	}

	return ErrStorageUnavailable
}

// RedisStorage implements fiber.Storage interface using lib-commons Redis connection.
// This enables distributed rate limiting across multiple application instances.
type RedisStorage struct {
	conn   *libRedis.Client
	logger log.Logger
}

// NewRedisStorage creates a new Redis-backed storage for Fiber rate limiting.
// Returns nil if the Redis connection is nil. Options can configure a logger.
func NewRedisStorage(conn *libRedis.Client, opts ...RedisStorageOption) *RedisStorage {
	storage := &RedisStorage{}

	for _, opt := range opts {
		if opt == nil {
			continue
		}

		opt(storage)
	}

	if conn == nil {
		asserter := assert.New(context.Background(), storage.logger, "http.ratelimit", "NewRedisStorage")
		if err := asserter.Never(context.Background(), "redis connection is nil; ratelimit storage disabled"); err != nil {
			return nil
		}

		return nil
	}

	storage.conn = conn

	return storage
}

// Get retrieves the value for the given key.
// Returns nil, nil when the key does not exist.
func (storage *RedisStorage) Get(key string) ([]byte, error) {
	if storage == nil || storage.conn == nil {
		return nil, storage.unavailableStorageError("Get")
	}

	ctx := context.Background()
	tracer := otel.Tracer("ratelimit")

	ctx, span := tracer.Start(ctx, "ratelimit.get")
	defer span.End()

	span.SetAttributes(attribute.String(constant.AttrDBSystem, constant.DBSystemRedis))

	client, err := storage.conn.GetClient(ctx)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "Failed to get redis client for ratelimit", err)
		return nil, fmt.Errorf("get redis client: %w", err)
	}

	redisKey, err := storage.redisKey(ctx, key)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "Ratelimit redis key construction failed", err)
		return nil, err
	}

	val, err := client.Get(ctx, redisKey).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}

	if err != nil {
		storage.logError(ctx, "redis get failed", err, "key", key)
		libOpentelemetry.HandleSpanError(span, "Ratelimit redis get failed", err)

		return nil, fmt.Errorf("redis get: %w", err)
	}

	return val, nil
}

// Set stores the given value for the given key with an expiration.
// 0 expiration means no expiration. Empty key or value will be ignored.
func (storage *RedisStorage) Set(key string, val []byte, exp time.Duration) error {
	if storage == nil || storage.conn == nil {
		return storage.unavailableStorageError("Set")
	}

	if key == "" || len(val) == 0 {
		return nil
	}

	ctx := context.Background()
	tracer := otel.Tracer("ratelimit")

	ctx, span := tracer.Start(ctx, "ratelimit.set")
	defer span.End()

	span.SetAttributes(attribute.String(constant.AttrDBSystem, constant.DBSystemRedis))

	client, err := storage.conn.GetClient(ctx)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "Failed to get redis client for ratelimit", err)
		return fmt.Errorf("get redis client: %w", err)
	}

	redisKey, err := storage.redisKey(ctx, key)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "Ratelimit redis key construction failed", err)
		return err
	}

	if err := client.Set(ctx, redisKey, val, exp).Err(); err != nil {
		storage.logError(ctx, "redis set failed", err, "key", key)
		libOpentelemetry.HandleSpanError(span, "Ratelimit redis set failed", err)

		return fmt.Errorf("redis set: %w", err)
	}

	return nil
}

// Delete removes the value for the given key.
// Returns no error if the key does not exist.
func (storage *RedisStorage) Delete(key string) error {
	if storage == nil || storage.conn == nil {
		return storage.unavailableStorageError("Delete")
	}

	ctx := context.Background()
	tracer := otel.Tracer("ratelimit")

	ctx, span := tracer.Start(ctx, "ratelimit.delete")
	defer span.End()

	span.SetAttributes(attribute.String(constant.AttrDBSystem, constant.DBSystemRedis))

	client, err := storage.conn.GetClient(ctx)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "Failed to get redis client for ratelimit", err)
		return fmt.Errorf("get redis client: %w", err)
	}

	redisKey, err := storage.redisKey(ctx, key)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "Ratelimit redis key construction failed", err)
		return err
	}

	if err := client.Del(ctx, redisKey).Err(); err != nil {
		storage.logError(ctx, "redis delete failed", err, "key", key)
		libOpentelemetry.HandleSpanError(span, "Ratelimit redis delete failed", err)

		return fmt.Errorf("redis delete: %w", err)
	}

	return nil
}

// Reset clears only keys in the storage-owned rate-limit namespace.
// It deletes "ratelimit:*" and tenant-scoped "tenant:*:ratelimit:*" keys.
// Historical custom-prefix keys such as "<prefix>:ratelimit:*" are not reset
// here because this storage type does not know which prefixes are owned by the
// current service, and destructive leading-wildcard scans are unsafe in shared
// Redis deployments.
func (storage *RedisStorage) Reset() error {
	if storage == nil || storage.conn == nil {
		return storage.unavailableStorageError("Reset")
	}

	ctx := context.Background()
	tracer := otel.Tracer("ratelimit")

	ctx, span := tracer.Start(ctx, "ratelimit.reset")
	defer span.End()

	span.SetAttributes(attribute.String(constant.AttrDBSystem, constant.DBSystemRedis))

	client, err := storage.conn.GetClient(ctx)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "Failed to get redis client for ratelimit", err)
		return fmt.Errorf("get redis client: %w", err)
	}

	for _, pattern := range []string{keyPrefix + "*", "tenant:*:" + keyPrefix + "*"} {
		if err := storage.resetPattern(ctx, client, span, pattern, isStorageOwnedRateLimitKey); err != nil {
			return err
		}
	}

	return nil
}

// Increment atomically increments the counter for key by 1 and, on the first hit of a
// window (count == 1), sets a millisecond-precision PEXPIRE matching the supplied window.
// Returns the post-increment count and the actual remaining TTL of the key — the latter is
// consistent with the increment because both are returned from a single Lua EVAL.
//
// The remaining TTL is useful for accurate Retry-After / X-RateLimit-Reset headers without
// a second PTTL round-trip. On the first hit the returned TTL equals window; on subsequent
// hits it reflects time already elapsed.
//
// If the underlying PTTL returns the sentinel -1 (no expiry — e.g. a TTL was inadvertently
// cleared by an external SET) or -2 (key not found — e.g. the key was deleted between INCR
// and PTTL), the Lua script restores the window expiry before returning so consumers observe
// a finite Retry-After value backed by actual Redis state.
//
// The key is automatically prefixed with the package's "ratelimit:" prefix unless it
// already starts with that exact namespace. Arbitrary keys that merely contain
// ":ratelimit:" are treated as caller-supplied identities and remain inside the
// storage-owned namespace. When ctx contains a tenant ID, the final Redis key is
// additionally scoped through tenant-manager/valkey.
//
// Errors:
//   - ErrStorageUnavailable: storage or its Redis client is nil.
//   - ErrInvalidWindow: window is <= 0 or truncates to 0 milliseconds.
//   - Wrapped redis errors: the network call failed or the cluster rejected the EVAL.
//     Consumers SHOULD treat these errors as transient and fail-open according to their
//     availability policy — this method does NOT impose a fail-open / fail-closed decision.
func (storage *RedisStorage) Increment(ctx context.Context, key string, window time.Duration) (count int64, ttl time.Duration, err error) {
	return storage.increment(ctx, key, window, storage.redisKey)
}

// incrementFullKey increments a key that has already been constructed by the
// package-owned RateLimiter middleware. This preserves the historical
// WithKeyPrefix("svc") wire shape ("svc:ratelimit:<tier>:<identity>") without
// teaching the public Increment API to guess full keys by substring.
func (storage *RedisStorage) incrementFullKey(ctx context.Context, key string, window time.Duration) (count int64, ttl time.Duration, err error) {
	return storage.increment(ctx, key, window, storage.redisFullKey)
}

func (storage *RedisStorage) increment(
	ctx context.Context,
	key string,
	window time.Duration,
	keyFunc func(context.Context, string) (string, error),
) (count int64, ttl time.Duration, err error) {
	if storage == nil || storage.conn == nil {
		return 0, 0, storage.unavailableStorageError("Increment")
	}

	if window <= 0 || window.Milliseconds() <= 0 {
		return 0, 0, ErrInvalidWindow
	}

	tracer := otel.Tracer("ratelimit")

	ctx, span := tracer.Start(ctx, "ratelimit.increment")
	defer span.End()

	windowMs := window.Milliseconds()

	span.SetAttributes(
		attribute.String(constant.AttrDBSystem, constant.DBSystemRedis),
		attribute.Int64("ratelimit.window_ms", windowMs),
	)

	client, err := storage.conn.GetClient(ctx)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "Failed to get redis client for ratelimit", err)
		return 0, 0, fmt.Errorf("get redis client: %w", err)
	}

	redisKey, err := keyFunc(ctx, key)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "Ratelimit redis key construction failed", err)
		return 0, 0, err
	}

	vals, err := luaIncrExpireScript.Run(ctx, client, []string{redisKey}, windowMs).Slice()
	if err != nil {
		storage.logError(ctx, "redis eval failed", err, "key", key)
		libOpentelemetry.HandleSpanError(span, "Ratelimit redis eval failed", err)

		return 0, 0, fmt.Errorf("redis eval: %w", err)
	}

	if len(vals) < 2 {
		err := fmt.Errorf("unexpected lua result length %d", len(vals))
		libOpentelemetry.HandleSpanError(span, "Ratelimit redis eval unexpected result", err)

		return 0, 0, err
	}

	count, ok := vals[0].(int64)
	if !ok {
		err := fmt.Errorf("unexpected lua result type %T for count", vals[0])
		libOpentelemetry.HandleSpanError(span, "Ratelimit redis eval unexpected count type", err)

		return 0, 0, err
	}

	ttlMs, ok := vals[1].(int64)
	if !ok {
		err := fmt.Errorf("unexpected lua result type %T for ttl", vals[1])
		libOpentelemetry.HandleSpanError(span, "Ratelimit redis eval unexpected ttl type", err)

		return 0, 0, err
	}

	// The Lua script repairs PTTL sentinel values before returning. This branch is a final
	// guard for Redis-compatible backends that may still return a negative TTL shape.
	if ttlMs < 0 {
		ttlMs = windowMs
	}

	span.SetAttributes(attribute.Int64("ratelimit.count_post_increment", count))

	return count, time.Duration(ttlMs) * time.Millisecond, nil
}

func (storage *RedisStorage) redisKey(ctx context.Context, key string) (string, error) {
	baseKey := normalizeRateLimitKey(key)

	if tenantID := tmcore.GetTenantIDContext(ctx); tenantID != "" && !tmcore.IsValidTenantID(tenantID) {
		return "", ErrInvalidTenantContext
	}

	prefixedKey, err := tmvalkey.GetKeyContext(ctx, baseKey)
	if err != nil {
		return "", ErrInvalidTenantContext
	}

	return prefixedKey, nil
}

func normalizeRateLimitKey(key string) string {
	if strings.HasPrefix(key, keyPrefix) {
		return key
	}

	return keyPrefix + key
}

func (storage *RedisStorage) redisFullKey(ctx context.Context, key string) (string, error) {
	if tenantID := tmcore.GetTenantIDContext(ctx); tenantID != "" && !tmcore.IsValidTenantID(tenantID) {
		return "", ErrInvalidTenantContext
	}

	prefixedKey, err := tmvalkey.GetKeyContext(ctx, key)
	if err != nil {
		return "", ErrInvalidTenantContext
	}

	return prefixedKey, nil
}

func isStorageOwnedRateLimitKey(key string) bool {
	if strings.HasPrefix(key, keyPrefix) {
		return true
	}

	segments := strings.SplitN(key, ":", 4)

	return len(segments) == 4 && segments[0] == "tenant" && segments[1] != "" && segments[2] == strings.TrimSuffix(keyPrefix, ":") && segments[3] != ""
}

func (storage *RedisStorage) resetPattern(ctx context.Context, client redis.UniversalClient, span trace.Span, pattern string, shouldDelete func(string) bool) error {
	var cursor uint64

	for {
		keys, nextCursor, err := client.Scan(ctx, cursor, pattern, scanBatchSize).Result()
		if err != nil {
			storage.logError(ctx, "redis scan failed during reset", err)
			libOpentelemetry.HandleSpanError(span, "Ratelimit redis scan failed", err)

			return fmt.Errorf("redis scan: %w", err)
		}

		if len(keys) > 0 {
			ownedKeys := keys[:0]
			for _, key := range keys {
				if shouldDelete == nil || shouldDelete(key) {
					ownedKeys = append(ownedKeys, key)
				}
			}

			if len(ownedKeys) == 0 {
				cursor = nextCursor
				if cursor == 0 {
					break
				}

				continue
			}

			if err := client.Del(ctx, ownedKeys...).Err(); err != nil {
				storage.logError(ctx, "redis batch delete failed during reset", err)
				libOpentelemetry.HandleSpanError(span, "Ratelimit redis batch delete failed", err)

				return fmt.Errorf("redis batch delete: %w", err)
			}
		}

		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}

	return nil
}

// logError logs a Redis operation error if a logger is configured.
func (storage *RedisStorage) logError(_ context.Context, msg string, err error, kv ...string) {
	if storage == nil || nilcheck.Interface(storage.logger) {
		return
	}

	fields := make([]log.Field, 0, 1+(len(kv)+1)/2)
	fields = append(fields, log.Err(err))

	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i] == "key" {
			fields = append(fields, log.String("key_hash", hashKey(kv[i+1])))

			continue
		}

		fields = append(fields, log.String(kv[i], kv[i+1]))
	}

	// Defensively handle odd-length kv: use a sentinel so missing values are obvious in logs.
	if len(kv)%2 != 0 {
		const missingValue = "<missing>"

		fields = append(fields, log.String(kv[len(kv)-1], missingValue))
	}

	storage.logger.Log(context.Background(), log.LevelWarn, msg, fields...)
}

// Close is a no-op as the Redis connection is managed by the application lifecycle.
func (*RedisStorage) Close() error {
	return nil
}
