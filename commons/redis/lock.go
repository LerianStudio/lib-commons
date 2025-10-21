package redis

import (
	"context"
	"fmt"
	"time"

	libCommons "github.com/LerianStudio/lib-commons/v2/commons"
	"github.com/LerianStudio/lib-commons/v2/commons/opentelemetry"
	"github.com/go-redsync/redsync/v4"
	"github.com/go-redsync/redsync/v4/redis/goredis/v9"
)

// DistributedLock provides distributed locking capabilities using Redis and the RedLock algorithm.
// This implementation ensures mutual exclusion across multiple service instances, preventing race
// conditions in critical sections such as:
// - Password update operations
// - Cache invalidation
// - Rate limiting checks
// - Any other operation requiring distributed coordination
//
// The RedLock algorithm provides strong guarantees even in the presence of:
// - Network partitions
// - Process crashes
// - Clock drift
//
// Example usage:
//
//	lock, err := redis.NewDistributedLock(redisConnection)
//	if err != nil {
//	    return err
//	}
//
//	err = lock.WithLock(ctx, "lock:user:123", func() error {
//	    // Critical section - only one instance will execute this at a time
//	    return updateUser(123)
//	})
type DistributedLock struct {
	redsync *redsync.Redsync
}

// LockOptions configures lock behavior for advanced use cases.
// Use DefaultLockOptions() for sensible defaults.
type LockOptions struct {
	// Expiry is how long the lock is held before auto-expiring (prevents deadlocks)
	// Default: 10 seconds
	Expiry time.Duration

	// Tries is the number of attempts to acquire the lock before giving up
	// Default: 3
	Tries int

	// RetryDelay is the delay between retry attempts
	// Default: 500ms
	RetryDelay time.Duration

	// DriftFactor accounts for clock drift in distributed systems (RedLock algorithm)
	// Default: 0.01 (1%)
	DriftFactor float64
}

// DefaultLockOptions returns production-ready defaults for distributed locking.
// These values are tuned for typical microservice scenarios with:
// - Operations completing within seconds
// - Network latency < 100ms
// - Acceptable retry overhead
func DefaultLockOptions() LockOptions {
	return LockOptions{
		Expiry:      10 * time.Second,
		Tries:       3,
		RetryDelay:  500 * time.Millisecond,
		DriftFactor: 0.01,
	}
}

// RateLimiterLockOptions returns optimized defaults for rate limiter locking.
// These values are tuned for short, fast operations like rate limiting:
// - Quick operations (< 100ms)
// - Fast retry for better throughput
// - Lower expiry to reduce contention
func RateLimiterLockOptions() LockOptions {
	return LockOptions{
		Expiry:      2 * time.Second,
		Tries:       2,
		RetryDelay:  100 * time.Millisecond,
		DriftFactor: 0.01,
	}
}

// NewDistributedLock creates a new distributed lock manager.
// The lock manager uses the RedLock algorithm for distributed consensus.
//
// Thread-safe: Yes - multiple goroutines can use the same DistributedLock instance.
//
// Example:
//
//	lock, err := redis.NewDistributedLock(redisConnection)
//	if err != nil {
//	    return fmt.Errorf("failed to initialize lock: %w", err)
//	}
func NewDistributedLock(conn *RedisConnection) (*DistributedLock, error) {
	ctx := context.Background()

	client, err := conn.GetClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get redis client: %w", err)
	}

	pool := goredis.NewPool(client)
	rs := redsync.New(pool)

	return &DistributedLock{
		redsync: rs,
	}, nil
}

// WithLock executes a function while holding a distributed lock.
// The lock is automatically released when the function returns, even on panic.
//
// Parameters:
//   - ctx: context for cancellation and tracing
//   - lockKey: unique identifier for the lock (e.g., "lock:user:123")
//   - fn: function to execute under lock
//
// Returns:
//   - error: from fn() or lock acquisition failure
//
// Example:
//
//	err := lock.WithLock(ctx, "lock:user:password:123", func() error {
//	    return updatePassword(123, newPassword)
//	})
func (dl *DistributedLock) WithLock(ctx context.Context, lockKey string, fn func() error) error {
	return dl.WithLockOptions(ctx, lockKey, DefaultLockOptions(), fn)
}

// WithLockOptions executes a function while holding a distributed lock with custom options.
// Use this when you need fine-grained control over lock behavior.
//
// Example with custom timeout:
//
//	opts := redis.LockOptions{
//	    Expiry:     30 * time.Second, // Long-running operation
//	    Tries:      5,                 // More aggressive retries
//	    RetryDelay: 1 * time.Second,
//	}
//	err := lock.WithLockOptions(ctx, "lock:report:generation", opts, func() error {
//	    return generateReport()
//	})
func (dl *DistributedLock) WithLockOptions(ctx context.Context, lockKey string, opts LockOptions, fn func() error) error {
	logger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)

	ctx, span := tracer.Start(ctx, "distributed_lock.with_lock")
	defer span.End()

	// Create mutex with configured options
	mutex := dl.redsync.NewMutex(
		lockKey,
		redsync.WithExpiry(opts.Expiry),
		redsync.WithTries(opts.Tries),
		redsync.WithRetryDelay(opts.RetryDelay),
		redsync.WithDriftFactor(opts.DriftFactor),
	)

	logger.Debugf("Attempting to acquire lock: %s", lockKey)

	// Try to acquire the lock
	if err := mutex.LockContext(ctx); err != nil {
		logger.Errorf("Failed to acquire lock %s: %v", lockKey, err)
		opentelemetry.HandleSpanError(&span, "Failed to acquire lock", err)

		return fmt.Errorf("failed to acquire lock %s: %w", lockKey, err)
	}

	logger.Debugf("Lock acquired: %s", lockKey)

	// Ensure lock is released even if function panics
	defer func() {
		if ok, err := mutex.UnlockContext(ctx); !ok || err != nil {
			logger.Errorf("Failed to release lock %s: ok=%v err=%v", lockKey, ok, err)
		} else {
			logger.Debugf("Lock released: %s", lockKey)
		}
	}()

	// Execute the function while holding the lock
	logger.Debugf("Executing function under lock: %s", lockKey)

	if err := fn(); err != nil {
		logger.Errorf("Function execution failed under lock %s: %v", lockKey, err)
		opentelemetry.HandleSpanError(&span, "Function execution failed", err)

		return err
	}

	logger.Debugf("Function completed successfully under lock: %s", lockKey)

	return nil
}

// TryLock attempts to acquire a lock without retrying.
// Returns true if lock was acquired, false otherwise.
//
// Use this when you want to skip the operation if the lock is busy:
//
//	mutex, acquired, err := lock.TryLock(ctx, "lock:cache:refresh")
//	if err != nil {
//	    return err
//	}
//	if !acquired {
//	    logger.Info("Lock busy, skipping cache refresh")
//	    return nil
//	}
//	defer lock.Unlock(ctx, mutex)
//	// Perform cache refresh...
func (dl *DistributedLock) TryLock(ctx context.Context, lockKey string) (*redsync.Mutex, bool, error) {
	logger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)

	ctx, span := tracer.Start(ctx, "distributed_lock.try_lock")
	defer span.End()

	mutex := dl.redsync.NewMutex(
		lockKey,
		redsync.WithExpiry(10*time.Second),
		redsync.WithTries(1), // Only try once
	)

	if err := mutex.LockContext(ctx); err != nil {
		logger.Debugf("Could not acquire lock %s: %v", lockKey, err)
		return nil, false, nil
	}

	logger.Debugf("Lock acquired: %s", lockKey)

	return mutex, true, nil
}

// Unlock releases a previously acquired lock.
// This is only needed if you use TryLock(). WithLock() handles unlocking automatically.
func (dl *DistributedLock) Unlock(ctx context.Context, mutex *redsync.Mutex) error {
	logger := libCommons.NewLoggerFromContext(ctx)

	if mutex == nil {
		return fmt.Errorf("mutex is nil")
	}

	ok, err := mutex.UnlockContext(ctx)
	if err != nil {
		logger.Errorf("Failed to unlock mutex: %v", err)
		return err
	}

	if !ok {
		logger.Warnf("Mutex was not locked or already expired")
		return fmt.Errorf("mutex was not locked")
	}

	return nil
}
