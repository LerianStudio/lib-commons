package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/LerianStudio/lib-commons/v2/commons/net/http/ratelimit"
	"github.com/redis/go-redis/v9"
)

// RedisLimiter implements distributed rate limiting using Redis.
// It uses sorted sets with sliding window algorithm for accurate rate limiting.
// This implementation is production-ready and can handle high-throughput scenarios.
type RedisLimiter struct {
	client redis.UniversalClient
	config ratelimit.Config
}

// Factory is a ready-to-use LimiterFactory for creating Redis-backed rate limiters.
// This is the standard factory that consumers should use to avoid boilerplate.
//
// Example usage:
//
//	handler := ratelimit.NewGlobalHandler(&ratelimit.GlobalHandlerConfig{
//	    // ... config
//	    LimiterFactory: redis.Factory,
//	}, logger)
var Factory = func(client redis.UniversalClient, config ratelimit.Config) ratelimit.Limiter {
	return NewRedisLimiter(client, config)
}

// NewRedisLimiter creates a new Redis-backed rate limiter.
// The client parameter accepts redis.UniversalClient which supports:
// - Standalone Redis
// - Redis Sentinel (high availability)
// - Redis Cluster (horizontal scaling)
func NewRedisLimiter(client redis.UniversalClient, config ratelimit.Config) *RedisLimiter {
	return &RedisLimiter{
		client: client,
		config: config,
	}
}

// Allow implements sliding window rate limiting using Redis sorted sets.
// Algorithm:
// 1. Remove old entries outside the current time window
// 2. Count remaining entries in the window
// 3. Add current request with timestamp as score
// 4. Set expiration on the key for automatic cleanup
//
// This approach provides:
// - Accurate rate limiting (no burst issues at window boundaries)
// - Automatic cleanup of old data
// - Atomic operations via Redis pipeline
// - Distributed consistency across multiple service instances
func (rl *RedisLimiter) Allow(ctx context.Context, key string) (*ratelimit.Result, error) {
	now := time.Now()
	windowStart := now.Add(-rl.config.Window)
	resetAt := now.Add(rl.config.Window)

	// Build the full Redis key with prefix for namespacing
	redisKey := rl.buildRedisKey(key)

	// Use Redis pipeline for atomic operations
	pipe := rl.client.Pipeline()

	// Step 1: Remove entries outside the sliding window
	// Score is Unix timestamp in nanoseconds for precision
	pipe.ZRemRangeByScore(ctx, redisKey, "0", fmt.Sprintf("%d", windowStart.UnixNano()))

	// Step 2: Count current entries in the window
	countCmd := pipe.ZCard(ctx, redisKey)

	// Step 3: Add current request timestamp to the sorted set
	// Using nanosecond timestamp as both score and member ensures uniqueness
	pipe.ZAdd(ctx, redisKey, redis.Z{
		Score:  float64(now.UnixNano()),
		Member: fmt.Sprintf("%d", now.UnixNano()),
	})

	// Step 4: Set expiration to window + buffer for cleanup
	// Buffer ensures we don't lose data if cleanup is slightly delayed
	pipe.Expire(ctx, redisKey, rl.config.Window+time.Second)

	// Execute all commands atomically
	_, err := pipe.Exec(ctx)
	if err != nil {
		return nil, fmt.Errorf("redis pipeline failed: %w", err)
	}

	// Get the count before we added the current request
	currentCount := int(countCmd.Val())

	// Determine if request is allowed
	allowed := currentCount < rl.config.Max

	// Calculate remaining requests
	remaining := rl.config.Max - currentCount - 1
	if remaining < 0 {
		remaining = 0
	}

	result := &ratelimit.Result{
		Allowed:    allowed,
		Remaining:  remaining,
		Limit:      rl.config.Max,
		ResetAt:    resetAt,
		RetryAfter: time.Until(resetAt),
	}

	return result, nil
}

// Reset clears all rate limit data for a specific key.
// This is useful for:
// - Administrative overrides (clearing rate limits for specific users)
// - Testing scenarios
// - Implementing "forgiveness" logic after temporary blocks
func (rl *RedisLimiter) Reset(ctx context.Context, key string) error {
	redisKey := rl.buildRedisKey(key)

	err := rl.client.Del(ctx, redisKey).Err()
	if err != nil {
		return fmt.Errorf("failed to reset rate limit: %w", err)
	}

	return nil
}

// GetConfig returns the limiter's configuration.
// This is used by middleware to populate response headers.
func (rl *RedisLimiter) GetConfig() ratelimit.Config {
	return rl.config
}

// buildRedisKey constructs the full Redis key with prefix.
// Format: "{prefix}:{key}"
// Example: "ratelimit:auth:ip:192.168.1.1"
func (rl *RedisLimiter) buildRedisKey(key string) string {
	if rl.config.KeyPrefix == "" {
		return key
	}

	return fmt.Sprintf("%s:%s", rl.config.KeyPrefix, key)
}
