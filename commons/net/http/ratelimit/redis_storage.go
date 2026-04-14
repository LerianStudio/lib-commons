package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/assert"
	constant "github.com/LerianStudio/lib-commons/v5/commons/constants"
	"github.com/LerianStudio/lib-commons/v5/commons/log"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v5/commons/opentelemetry"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"

	libRedis "github.com/LerianStudio/lib-commons/v5/commons/redis"
)

const (
	keyPrefix     = "ratelimit:"
	scanBatchSize = 100
)

// ErrStorageUnavailable is returned when Redis storage is nil or not initialized.
var ErrStorageUnavailable = errors.New("ratelimit redis storage is unavailable")

// RedisStorageOption is a functional option for configuring RedisStorage.
type RedisStorageOption func(*RedisStorage)

// WithRedisStorageLogger provides a structured logger for assertion and error logging.
func WithRedisStorageLogger(l log.Logger) RedisStorageOption {
	return func(s *RedisStorage) {
		if l != nil {
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
	_ = asserter.Never(context.Background(), "ratelimit redis storage is unavailable")

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
		opt(storage)
	}

	if conn == nil {
		asserter := assert.New(context.Background(), storage.logger, "http.ratelimit", "NewRedisStorage")
		_ = asserter.Never(context.Background(), "redis connection is nil; ratelimit storage disabled")

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

	val, err := client.Get(ctx, keyPrefix+key).Bytes()
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

	if err := client.Set(ctx, keyPrefix+key, val, exp).Err(); err != nil {
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

	if err := client.Del(ctx, keyPrefix+key).Err(); err != nil {
		storage.logError(ctx, "redis delete failed", err, "key", key)
		libOpentelemetry.HandleSpanError(span, "Ratelimit redis delete failed", err)

		return fmt.Errorf("redis delete: %w", err)
	}

	return nil
}

// Reset clears all rate limit keys from the storage.
// This uses SCAN to find and delete keys with the rate limit prefix.
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

	var cursor uint64

	for {
		keys, nextCursor, err := client.Scan(ctx, cursor, keyPrefix+"*", scanBatchSize).Result()
		if err != nil {
			storage.logError(ctx, "redis scan failed during reset", err)
			libOpentelemetry.HandleSpanError(span, "Ratelimit redis scan failed", err)

			return fmt.Errorf("redis scan: %w", err)
		}

		if len(keys) > 0 {
			if err := client.Del(ctx, keys...).Err(); err != nil {
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
	if storage == nil || storage.logger == nil {
		return
	}

	fields := make([]log.Field, 0, 1+(len(kv)+1)/2)
	fields = append(fields, log.Err(err))

	for i := 0; i+1 < len(kv); i += 2 {
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
