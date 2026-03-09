package redis

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	libCommons "github.com/LerianStudio/lib-commons/v4/commons"
	"github.com/LerianStudio/lib-commons/v4/commons/assert"
	"github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/LerianStudio/lib-commons/v4/commons/opentelemetry"
	"github.com/go-redsync/redsync/v4"
	redsyncredis "github.com/go-redsync/redsync/v4/redis"
	"github.com/go-redsync/redsync/v4/redis/goredis/v9"
)

const (
	maxLockTries = 1000
	// unlockTimeout is the maximum duration for an unlock operation using a
	// detached context. This prevents unlock from failing silently when the
	// caller's context has been cancelled.
	unlockTimeout = 5 * time.Second
)

var (
	// ErrNilLockHandle is returned when a nil or uninitialized lock handle is used.
	ErrNilLockHandle = errors.New("lock handle is nil or not initialized")
	// ErrLockNotHeld is returned when unlock is called on a lock that was not held or already expired.
	ErrLockNotHeld = errors.New("lock was not held or already expired")
	// ErrNilLockManager is returned when a method is called on a nil RedisLockManager.
	ErrNilLockManager = errors.New("lock manager is nil")
	// ErrLockNotInitialized is returned when the distributed lock's redsync is not initialized.
	ErrLockNotInitialized = errors.New("distributed lock is not initialized")
	// ErrNilLockFn is returned when a nil function is passed to WithLock.
	ErrNilLockFn = errors.New("lock function is nil")
	// ErrEmptyLockKey is returned when an empty lock key is provided.
	ErrEmptyLockKey = errors.New("lock key cannot be empty")
	// ErrLockExpiryInvalid is returned when lock expiry is not positive.
	ErrLockExpiryInvalid = errors.New("lock expiry must be greater than 0")
	// ErrLockTriesInvalid is returned when lock tries is less than 1.
	ErrLockTriesInvalid = errors.New("lock tries must be at least 1")
	// ErrLockTriesExceeded is returned when lock tries exceeds the maximum.
	ErrLockTriesExceeded = errors.New("lock tries exceeds maximum")
	// ErrLockRetryDelayNegative is returned when retry delay is negative.
	ErrLockRetryDelayNegative = errors.New("lock retry delay cannot be negative")
	// ErrLockDriftFactorInvalid is returned when drift factor is outside [0, 1).
	ErrLockDriftFactorInvalid = errors.New("lock drift factor must be between 0 (inclusive) and 1 (exclusive)")
	// ErrNilLockHandleOnUnlock is returned when Unlock is called with a nil handle.
	ErrNilLockHandleOnUnlock = errors.New("lock handle is nil")
)

// RedisLockManager provides distributed locking capabilities using Redis and the RedLock algorithm.
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
//	lock, err := redis.NewRedisLockManager(redisClient)
//	if err != nil {
//	    return err
//	}
//
//	err = lock.WithLock(ctx, "lock:user:123", func(ctx context.Context) error {
//	    // Critical section - only one instance will execute this at a time
//	    return updateUser(123)
//	})
type RedisLockManager struct {
	redsync *redsync.Redsync
}

// LockOptions configures lock behavior for advanced use cases.
// Use DefaultLockOptions() for sensible defaults.
type LockOptions struct {
	// Expiry is how long the lock is held before auto-expiring (prevents deadlocks)
	// Default: 10 seconds
	Expiry time.Duration

	// Tries is the number of attempts to acquire the lock before giving up
	// Default: 3, Maximum: 1000
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

// clientPool implements the redsync redis.Pool interface with lazy client resolution.
// On each Get call it resolves the latest redis.UniversalClient from the Client wrapper,
// ensuring the pool survives IAM token refresh reconnections.
type clientPool struct {
	conn *Client
}

func (p *clientPool) Get(ctx context.Context) (redsyncredis.Conn, error) {
	rdb, err := p.conn.GetClient(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get redis client for lock pool: %w", err)
	}

	return goredis.NewPool(rdb).Get(ctx)
}

// lockHandle wraps a redsync.Mutex to implement LockHandle.
// It is returned by TryLock and provides a self-contained Unlock method.
type lockHandle struct {
	mutex  *redsync.Mutex
	logger log.Logger
}

// Unlock releases the distributed lock.
func (h *lockHandle) Unlock(ctx context.Context) error {
	if h == nil || h.mutex == nil {
		return ErrNilLockHandle
	}

	ok, err := h.mutex.UnlockContext(ctx)
	if err != nil {
		h.logger.Log(ctx, log.LevelError, "failed to release lock", log.Err(err))
		return fmt.Errorf("distributed lock: unlock: %w", err)
	}

	if !ok {
		h.logger.Log(ctx, log.LevelWarn, "lock was not held or already expired")
		return ErrLockNotHeld
	}

	return nil
}

// nilLockAssert fires a nil-receiver assertion and returns an error.
func nilLockAssert(ctx context.Context, operation string) error {
	a := assert.New(ctx, resolvePackageLogger(), "redis.RedisLockManager", operation)
	_ = a.Never(ctx, "nil receiver on *redis.RedisLockManager")

	return ErrNilLockManager
}

// NewRedisLockManager creates a new distributed lock manager.
// The lock manager uses the RedLock algorithm for distributed consensus.
// It uses a lazy pool that resolves the latest Redis client per operation,
// surviving IAM token refresh reconnections.
//
// Thread-safe: Yes - multiple goroutines can use the same RedisLockManager instance.
//
// Example:
//
//	lock, err := redis.NewRedisLockManager(redisClient)
//	if err != nil {
//	    return fmt.Errorf("failed to initialize lock: %w", err)
//	}
func NewRedisLockManager(conn *Client) (*RedisLockManager, error) {
	if conn == nil {
		return nil, ErrNilClient
	}

	// Verify connectivity at construction time.
	ctx := context.Background()

	if _, err := conn.GetClient(ctx); err != nil {
		return nil, fmt.Errorf("failed to get redis client: %w", err)
	}

	// Use a lazy pool that resolves the client per operation,
	// surviving IAM token refresh reconnections.
	pool := &clientPool{conn: conn}
	rs := redsync.New(pool)

	return &RedisLockManager{
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
//	err := lock.WithLock(ctx, "lock:user:password:123", func(ctx context.Context) error {
//	    return updatePassword(123, newPassword)
//	})
func (dl *RedisLockManager) WithLock(ctx context.Context, lockKey string, fn func(context.Context) error) error {
	if dl == nil {
		return nilLockAssert(ctx, "WithLock")
	}

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
//	err := lock.WithLockOptions(ctx, "lock:report:generation", opts, func(ctx context.Context) error {
//	    return generateReport()
//	})
func (dl *RedisLockManager) WithLockOptions(ctx context.Context, lockKey string, opts LockOptions, fn func(context.Context) error) error {
	if dl == nil {
		return nilLockAssert(ctx, "WithLockOptions")
	}

	if dl.redsync == nil {
		return ErrLockNotInitialized
	}

	if fn == nil {
		return ErrNilLockFn
	}

	if strings.TrimSpace(lockKey) == "" {
		return ErrEmptyLockKey
	}

	if err := validateLockOptions(opts); err != nil {
		return err
	}

	logger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)
	safeLockKey := safeLockKeyForLogs(lockKey)

	ctx, span := tracer.Start(ctx, "redis.lock.with_lock")
	defer span.End()

	// Create mutex with configured options
	mutex := dl.redsync.NewMutex(
		lockKey,
		redsync.WithExpiry(opts.Expiry),
		redsync.WithTries(opts.Tries),
		redsync.WithRetryDelay(opts.RetryDelay),
		redsync.WithDriftFactor(opts.DriftFactor),
	)

	logger.Log(ctx, log.LevelDebug, "attempting to acquire lock", log.String("lock_key", safeLockKey))

	// Try to acquire the lock
	if err := mutex.LockContext(ctx); err != nil {
		logger.Log(ctx, log.LevelError, "failed to acquire lock", log.String("lock_key", safeLockKey), log.Err(err))
		opentelemetry.HandleSpanError(span, "Failed to acquire lock", err)

		return fmt.Errorf("failed to acquire lock %s: %w", safeLockKey, err)
	}

	logger.Log(ctx, log.LevelDebug, "lock acquired", log.String("lock_key", safeLockKey))

	// Ensure lock is released even if function panics.
	// Use a detached context with a timeout so that the unlock is not blocked
	// by a cancelled/expired caller context — a failed unlock leaves a dangling
	// lock until its expiry, which can stall other callers.
	defer func() {
		unlockCtx, unlockCancel := context.WithTimeout(context.Background(), unlockTimeout)
		defer unlockCancel()

		if ok, unlockErr := mutex.UnlockContext(unlockCtx); !ok || unlockErr != nil {
			logger.Log(ctx, log.LevelError, "failed to release lock", log.String("lock_key", safeLockKey), log.Bool("unlock_ok", ok), log.Err(unlockErr))
		} else {
			logger.Log(ctx, log.LevelDebug, "lock released", log.String("lock_key", safeLockKey))
		}
	}()

	// Execute the function while holding the lock
	logger.Log(ctx, log.LevelDebug, "executing function under lock", log.String("lock_key", safeLockKey))

	if err := fn(ctx); err != nil {
		logger.Log(ctx, log.LevelError, "function execution failed under lock", log.String("lock_key", safeLockKey), log.Err(err))
		opentelemetry.HandleSpanError(span, "Function execution failed", err)

		return fmt.Errorf("distributed lock: function execution: %w", err)
	}

	logger.Log(ctx, log.LevelDebug, "function completed successfully under lock", log.String("lock_key", safeLockKey))

	return nil
}

// TryLock attempts to acquire a lock without retrying.
// Returns the handle and true if lock was acquired, nil and false if lock is busy.
// Returns an error for unexpected failures (network errors, context cancellation, etc.)
//
// Use LockHandle.Unlock to release the lock when done:
//
//	handle, acquired, err := lock.TryLock(ctx, "lock:cache:refresh")
//	if err != nil {
//	    // Unexpected error (network, context cancellation, etc.) - should be propagated
//	    return fmt.Errorf("failed to attempt lock acquisition: %w", err)
//	}
//	if !acquired {
//	    logger.Info("Lock busy, skipping cache refresh")
//	    return nil
//	}
//	defer handle.Unlock(ctx)
//	// Perform cache refresh...
func (dl *RedisLockManager) TryLock(ctx context.Context, lockKey string) (LockHandle, bool, error) {
	if dl == nil {
		return nil, false, nilLockAssert(ctx, "TryLock")
	}

	if dl.redsync == nil {
		return nil, false, ErrLockNotInitialized
	}

	if strings.TrimSpace(lockKey) == "" {
		return nil, false, ErrEmptyLockKey
	}

	logger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)
	safeLockKey := safeLockKeyForLogs(lockKey)

	ctx, span := tracer.Start(ctx, "redis.lock.try_lock")
	defer span.End()

	defaultOpts := DefaultLockOptions()

	mutex := dl.redsync.NewMutex(
		lockKey,
		redsync.WithExpiry(defaultOpts.Expiry),
		redsync.WithTries(1), // Only try once
	)

	if err := mutex.LockContext(ctx); err != nil {
		// Classify lock contention vs infrastructure faults using redsync's
		// typed sentinels rather than string matching. ErrFailed is returned
		// when all retries are exhausted and ErrTaken when the lock is held
		// on a quorum of nodes — both indicate normal contention.
		var errTaken *redsync.ErrTaken

		isLockContention := errors.Is(err, redsync.ErrFailed) || errors.As(err, &errTaken)

		if isLockContention {
			logger.Log(ctx, log.LevelDebug, "lock already held by another process", log.String("lock_key", safeLockKey))
			return nil, false, nil
		}

		// Any other error (e.g., network, context cancellation, RedisError)
		// is an actual infrastructure fault and must be propagated.
		logger.Log(ctx, log.LevelDebug, "could not acquire lock", log.String("lock_key", safeLockKey), log.Err(err))
		opentelemetry.HandleSpanError(span, "Failed to attempt lock acquisition", err)

		return nil, false, fmt.Errorf("failed to attempt lock acquisition for %s: %w", safeLockKey, err)
	}

	logger.Log(ctx, log.LevelDebug, "lock acquired", log.String("lock_key", safeLockKey))

	return &lockHandle{mutex: mutex, logger: logger}, true, nil
}

// Unlock releases a previously acquired lock.
//
// Deprecated: Use LockHandle.Unlock() directly instead. This method is provided
// for backward compatibility during migration from the old *redsync.Mutex-based API.
func (dl *RedisLockManager) Unlock(ctx context.Context, handle LockHandle) error {
	if dl == nil {
		return nilLockAssert(ctx, "Unlock")
	}

	if handle == nil {
		return ErrNilLockHandleOnUnlock
	}

	return handle.Unlock(ctx)
}

func validateLockOptions(opts LockOptions) error {
	if opts.Expiry <= 0 {
		return ErrLockExpiryInvalid
	}

	if opts.Tries < 1 {
		return ErrLockTriesInvalid
	}

	if opts.Tries > maxLockTries {
		return ErrLockTriesExceeded
	}

	if opts.RetryDelay < 0 {
		return ErrLockRetryDelayNegative
	}

	if opts.DriftFactor < 0 || opts.DriftFactor >= 1 {
		return ErrLockDriftFactorInvalid
	}

	return nil
}

func safeLockKeyForLogs(lockKey string) string {
	const maxLockKeyLogLength = 128

	safeLockKey := strconv.QuoteToASCII(lockKey)
	if len(safeLockKey) <= maxLockKeyLogLength {
		return safeLockKey
	}

	return safeLockKey[:maxLockKeyLogLength] + "...(truncated)"
}
