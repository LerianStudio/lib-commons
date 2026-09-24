package redis

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"
	obsbridge "github.com/LerianStudio/lib-commons/v7/commons/obs/obsbridge"

	"github.com/LerianStudio/lib-observability/v4/assert"
	opentelemetry "github.com/LerianStudio/lib-observability/v4/tracing"
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
	// ErrLockContended reports that the lock was not acquired because another
	// process holds it. It is joined into the error WithLockOptions returns, so
	// a caller can tell a skipped cycle from an infrastructure fault with
	// errors.Is and without importing redsync.
	ErrLockContended = errors.New("lock held by another process")
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
	logger obs.Logger

	// extendMu serialises Extend: a successful redsync ExtendContext writes the
	// mutex's validity deadline unsynchronised.
	extendMu sync.Mutex
}

// Unlock releases the distributed lock.
func (h *lockHandle) Unlock(ctx context.Context) error {
	if h == nil || h.mutex == nil {
		return ErrNilLockHandle
	}

	ok, err := h.mutex.UnlockContext(ctx)
	if err != nil {
		h.logger.Log(ctx, obs.LevelError, "failed to release lock", "error", err)
		return fmt.Errorf("distributed lock: unlock: %w", err)
	}

	if !ok {
		h.logger.Log(ctx, obs.LevelWarn, "lock was not held or already expired")
		return ErrLockNotHeld
	}

	return nil
}

// Extend renews the lease to the expiry the lock was acquired with; see
// LockExtender for the contract.
//
// The redsync mutex carries that expiry from acquisition, so the renewal can
// never silently change the lease length. A lost lease logs at WARN, like an
// Unlock of a lock that was not held, and leaves the span status unset: it is a
// designed outcome the caller branches on, not an infrastructure fault. A
// renewal the quorum accepted but whose round trip outlived the lease is not a
// lost lease — the key may carry the renewed lease — and is reported as an
// error with an error span, like a Redis that did not answer. Concurrent calls
// on one handle are serialised.
func (h *lockHandle) Extend(ctx context.Context) (bool, error) {
	if h == nil || h.mutex == nil {
		return false, ErrNilLockHandle
	}

	logger, tracer, _, _ := obsbridge.TrackingFromContext(ctx)
	safeLockKey := safeLockKeyForLogs(h.mutex.Name())

	ctx, span := tracer.Start(ctx, "redis.lock.extend")
	defer span.End()

	// Read before Redis is asked anything, as acquireLock does: once a command
	// is in flight the error chain cannot tell the caller's deadline apart from
	// a node timeout.
	if err := ctx.Err(); err != nil {
		logger.Log(ctx, obs.LevelDebug, "lock extension abandoned: caller context ended",
			"lock_key", safeLockKey, "error", err)

		return false, fmt.Errorf("distributed lock: extend %s: %w", safeLockKey, err)
	}

	h.extendMu.Lock()
	renewed, err := h.mutex.ExtendContext(ctx)
	h.extendMu.Unlock()

	switch {
	case renewed:
		logger.Log(ctx, obs.LevelDebug, "lock extended", "lock_key", safeLockKey)

		return true, nil

	case isLeaseLost(err):
		logger.Log(ctx, obs.LevelWarn, "lock lease lost: expired or held by another process", "lock_key", safeLockKey)

		return false, nil

	default:
		logger.Log(ctx, obs.LevelError, "failed to extend lock", "lock_key", safeLockKey, "error", err)
		opentelemetry.HandleSpanError(span, "Failed to extend lock", err)

		return false, fmt.Errorf("distributed lock: extend %s: %w", safeLockKey, err)
	}
}

// isLeaseLost reports whether a failed ExtendContext means the key is no longer
// ours. MEASURED against redsync v4.17: only when a quorum of nodes answers the
// touch script with 0 — the key is gone or carries another holder's value —
// does redsync return *ErrTaken. ErrExtendFailed is NOT a lost lease: the
// quorum accepted the touch, so the key may carry our renewed lease, but the
// round trip ate the whole validity window. It, and anything else, including a
// sub-quorum mix of taken nodes and nodes that did not answer, is a fault:
// nothing conclusive was learned about the key.
func isLeaseLost(err error) bool {
	var errTaken *redsync.ErrTaken

	return errors.As(err, &errTaken)
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
// A lock held by another process is reported as ErrLockContended and logged at
// debug, not error — see WithLockOptions for the full classification.
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
// A failure to acquire the lock is always returned as an error — that is how
// the caller knows fn did not run — but only an infrastructure fault is
// REPORTED as one. Three outcomes are distinguished:
//
//   - The lock is held by another process — including when the configured
//     Tries were exhausted trying to take it, and when the caller's own
//     deadline expired while it waited. Logged at debug, no error recorded on
//     the span, and the returned error joins ErrLockContended with redsync's
//     own error, so both errors.Is(err, ErrLockContended) and a redsync-typed
//     check hold.
//   - The caller's own context ended before Redis answered. Logged at debug, no
//     error recorded on the span, error returned; it unwraps to the caller's
//     own context error.
//   - Anything else — a Redis that is unreachable or stopped answering, whether
//     or not the caller carried a deadline. Logged at error and recorded on the
//     span, error returned.
//
// This is what lets a periodic sweep run under Tries: 1 on several replicas:
// the replicas that skip a cycle because a sibling holds the key produce debug
// lines and clean spans, and the caller still sees an error it can match
// against ErrLockContended and swallow.
//
//	if err := lock.WithLockOptions(ctx, key, opts, sweep); err != nil {
//	    if errors.Is(err, redis.ErrLockContended) {
//	        return nil // another replica is running this cycle
//	    }
//	    return err
//	}
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

	logger, tracer, _, _ := obsbridge.TrackingFromContext(ctx)
	safeLockKey := safeLockKeyForLogs(lockKey)

	ctx, span := tracer.Start(ctx, "redis.lock.with_lock")
	defer span.End()

	// Tries and RetryDelay are deliberately not handed to redsync: acquireLock
	// owns the retry loop so the reason an acquisition failed survives it.
	mutex := dl.redsync.NewMutex(
		lockKey,
		redsync.WithExpiry(opts.Expiry),
		redsync.WithDriftFactor(opts.DriftFactor),
	)

	logger.Log(ctx, obs.LevelDebug, "attempting to acquire lock", "lock_key", safeLockKey)

	// A failure is classified before it is reported: contention and caller
	// cancellation are designed outcomes and must not masquerade as faults in
	// logs and traces. The error is returned in every case, because it is how
	// the caller knows fn did not run.
	if outcome, err := acquireLock(ctx, mutex, opts); outcome != lockTaken {
		switch outcome {
		case lockContended:
			logger.Log(ctx, obs.LevelDebug, "lock held by another process",
				"lock_key", safeLockKey, "tries", opts.Tries)

			return fmt.Errorf("failed to acquire lock %s: %w: %w", safeLockKey, ErrLockContended, err)

		case lockCallerDone:
			logger.Log(ctx, obs.LevelDebug, "lock acquisition abandoned: caller context ended",
				"lock_key", safeLockKey, "error", err)

		// lockFaulted, and any outcome added later: an unclassified failure is
		// reported as a fault, which is the loud reading rather than the quiet one.
		default:
			logger.Log(ctx, obs.LevelError, "failed to acquire lock", "lock_key", safeLockKey, "error", err)
			opentelemetry.HandleSpanError(span, "Failed to acquire lock", err)
		}

		return fmt.Errorf("failed to acquire lock %s: %w", safeLockKey, err)
	}

	logger.Log(ctx, obs.LevelDebug, "lock acquired", "lock_key", safeLockKey)

	// Ensure lock is released even if function panics.
	// Use a detached context with a timeout so that the unlock is not blocked
	// by a cancelled/expired caller context — a failed unlock leaves a dangling
	// lock until its expiry, which can stall other callers.
	defer func() {
		unlockCtx, unlockCancel := context.WithTimeout(context.Background(), unlockTimeout)
		defer unlockCancel()

		if ok, unlockErr := mutex.UnlockContext(unlockCtx); !ok || unlockErr != nil {
			logger.Log(ctx, obs.LevelError, "failed to release lock", "lock_key", safeLockKey, "unlock_ok", ok, "error", unlockErr)
		} else {
			logger.Log(ctx, obs.LevelDebug, "lock released", "lock_key", safeLockKey)
		}
	}()

	// Execute the function while holding the lock
	logger.Log(ctx, obs.LevelDebug, "executing function under lock", "lock_key", safeLockKey)

	if err := fn(ctx); err != nil {
		logger.Log(ctx, obs.LevelError, "function execution failed under lock", "lock_key", safeLockKey, "error", err)
		opentelemetry.HandleSpanError(span, "Function execution failed", err)

		return fmt.Errorf("distributed lock: function execution: %w", err)
	}

	logger.Log(ctx, obs.LevelDebug, "function completed successfully under lock", "lock_key", safeLockKey)

	return nil
}

// TryLock attempts to acquire a lock without retrying.
// Returns the handle and true if lock was acquired, nil and false if lock is busy.
// Returns an error for unexpected failures (network errors, context cancellation, etc.)
//
// Only a Redis that is unreachable or stopped answering logs at ERROR and marks
// the "redis.lock.try_lock" span as an error. A busy lock and a caller whose
// context was already done log at DEBUG and leave the span status unset.
//
// TryLock is TryLockWithOptions with DefaultLockOptions() and Tries forced to 1,
// which means a fixed 10-second expiry. Use TryLockWithOptions when the work
// under the lock needs a different one.
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

	opts := DefaultLockOptions()
	opts.Tries = 1

	return dl.tryLock(ctx, lockKey, opts)
}

// TryLockWithOptions attempts to acquire a lock using caller-supplied options.
// It is TryLock with the expiry, retry and drift settings under the caller's
// control instead of fixed.
//
// The contract is TryLock's: the handle and true when the lock was taken,
// (nil, false, nil) when another process holds it, and an error otherwise.
// Unlike WithLockOptions, contention is not an error here — a caller that wants
// the error shape uses that entry point instead. A Redis that stopped answering
// is always an error, however many Tries were configured: a worker told "busy"
// skips its cycle and reports success, which would hide an outage for as long
// as it lasts. A caller whose own context ended before the first attempt is an
// error too. One case reads as busy: the context ends while waiting out
// RetryDelay after an attempt that found the lock held. The answer is then
// (nil, false, nil), because the lock really was held; WithLockOptions reports
// the same case as ErrLockContended for the same reason.
//
// Tries is honoured as supplied, so Tries: 3 makes three attempts, separated by
// RetryDelay, before reporting the lock busy. Options are validated the same way
// WithLockOptions validates them.
//
// Expiry also bounds how long one attempt waits on a Redis that never answers.
// MEASURED against redsync v4.17 and go-redis: a stalled attempt costs about
// 2 x min(Expiry x 0.05, the client's ReadTimeout, ctx's remaining time), since
// redsync follows each failed attempt with a release under a fresh timeout of the
// same length. At the default 3s ReadTimeout the 30-minute expiry below blocks
// about 6s per attempt, not 3 minutes; Tries N costs N times that plus the
// delays. With ReadTimeout disabled (-1) a 5-minute expiry blocks about 30s per
// attempt. Bound ctx when the caller cannot afford that.
//
// This method is declared on *RedisLockManager and deliberately NOT on the
// LockManager interface: adding a method there would break every external
// implementer. Consumers that need it hold the concrete type, or declare their
// own narrow port containing just the methods they call.
//
// Example — a sweep that may run for half an hour and must not be retried:
//
//	opts := redis.DefaultLockOptions()
//	opts.Expiry = 30 * time.Minute
//	opts.Tries = 1
//
//	handle, acquired, err := lock.TryLockWithOptions(ctx, "lock:archival:sweep", opts)
//	if err != nil {
//	    return fmt.Errorf("failed to attempt lock acquisition: %w", err)
//	}
//	if !acquired {
//	    return nil // another replica is running this sweep
//	}
//	defer handle.Unlock(ctx)
func (dl *RedisLockManager) TryLockWithOptions(ctx context.Context, lockKey string, opts LockOptions) (LockHandle, bool, error) {
	if dl == nil {
		return nil, false, nilLockAssert(ctx, "TryLockWithOptions")
	}

	return dl.tryLock(ctx, lockKey, opts)
}

// tryLock is the single implementation behind TryLock and TryLockWithOptions.
// The nil-receiver check is left to the callers so each names itself in the
// assertion it fires.
func (dl *RedisLockManager) tryLock(ctx context.Context, lockKey string, opts LockOptions) (LockHandle, bool, error) {
	if dl.redsync == nil {
		return nil, false, ErrLockNotInitialized
	}

	if strings.TrimSpace(lockKey) == "" {
		return nil, false, ErrEmptyLockKey
	}

	if err := validateLockOptions(opts); err != nil {
		return nil, false, err
	}

	logger, tracer, _, _ := obsbridge.TrackingFromContext(ctx)
	safeLockKey := safeLockKeyForLogs(lockKey)

	ctx, span := tracer.Start(ctx, "redis.lock.try_lock")
	defer span.End()

	// Tries and RetryDelay are deliberately not handed to redsync: acquireLock
	// owns the retry loop so the reason an acquisition failed survives it.
	mutex := dl.redsync.NewMutex(
		lockKey,
		redsync.WithExpiry(opts.Expiry),
		redsync.WithDriftFactor(opts.DriftFactor),
	)

	outcome, err := acquireLock(ctx, mutex, opts)

	switch outcome {
	case lockTaken:
		logger.Log(ctx, obs.LevelDebug, "lock acquired", "lock_key", safeLockKey)

		return &lockHandle{mutex: mutex, logger: logger}, true, nil

	case lockContended:
		logger.Log(ctx, obs.LevelDebug, "lock already held by another process", "lock_key", safeLockKey)

		return nil, false, nil

	case lockCallerDone:
		logger.Log(ctx, obs.LevelDebug, "lock acquisition abandoned: caller context ended",
			"lock_key", safeLockKey, "error", err)

		return nil, false, fmt.Errorf("failed to attempt lock acquisition for %s: %w", safeLockKey, err)

	// lockFaulted, and any outcome added later: Redis is unreachable or stopped
	// answering, the one outcome here that is a genuine fault.
	default:
		logger.Log(ctx, obs.LevelError, "failed to attempt lock acquisition", "lock_key", safeLockKey, "error", err)
		opentelemetry.HandleSpanError(span, "Failed to attempt lock acquisition", err)

		return nil, false, fmt.Errorf("failed to attempt lock acquisition for %s: %w", safeLockKey, err)
	}
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

// lockOutcome says how an acquisition ended. It exists so WithLockOptions and
// TryLock read one classification instead of each inventing its own.
type lockOutcome int

const (
	// lockTaken means the lock was acquired.
	lockTaken lockOutcome = iota
	// lockContended means another process holds the key.
	lockContended
	// lockCallerDone means the caller's own context ended before Redis answered.
	lockCallerDone
	// lockFaulted means Redis is unreachable or stopped answering.
	lockFaulted
)

// acquireLock runs the retry loop itself, one single-attempt redsync call per
// try, rather than handing Tries and RetryDelay to redsync.
//
// redsync has a retry loop of its own, but it discards the reason: when the
// caller's context ends while it waits out a retry delay it returns a bare
// ErrFailed (mutex.go lockContext), throwing away the last attempt's real
// error. MEASURED against redsync v4.17 with a server that completes the
// handshake and then stops answering: with Tries above one and any caller
// deadline shorter than Tries x RetryDelay, a total outage arrives as that bare
// ErrFailed, indistinguishable from a contended lock — so the outage reads as a
// skipped cycle. Owning the loop keeps every attempt's error, so the outcome is
// decided on what Redis actually did.
//
// Retries continue across a fault, as redsync's do: one failed round trip may be
// a blip, and the next attempt is the cheapest way to find out.
//
// At least one attempt always runs, whatever Tries says, and the returned error
// is the last attempt's — nil only for lockTaken.
func acquireLock(ctx context.Context, mutex *redsync.Mutex, opts LockOptions) (lockOutcome, error) {
	// Read once, before anything is asked of Redis. After the first attempt the
	// failure itself is the better evidence; see classifyLockFailure for what a
	// caller deadline expiring mid-flight turns into.
	callerDoneAtStart := ctx.Err() != nil

	var lastErr error

	for try := 0; ; try++ {
		if try > 0 {
			timer := time.NewTimer(opts.RetryDelay)

			select {
			case <-ctx.Done():
				timer.Stop()

				return classifyLockFailure(lastErr, callerDoneAtStart), lastErr
			case <-timer.C:
			}
		}

		lastErr = mutex.TryLockContext(ctx)
		if lastErr == nil {
			return lockTaken, nil
		}

		if try+1 >= opts.Tries {
			return classifyLockFailure(lastErr, callerDoneAtStart), lastErr
		}
	}
}

// classifyLockFailure decides what a failed acquisition means, in the order the
// evidence is trustworthy:
//
//   - Redis answered that the key is taken: contention, whatever the caller's
//     context is doing. This is what keeps ErrLockContended on the error when a
//     caller's deadline expires while it waits out a lock someone else holds.
//   - Otherwise, the caller's context was already done before Redis was asked
//     anything: the caller gave up, and nothing was ever learned about Redis.
//   - Anything else: Redis is unreachable or stopped answering.
//
// A caller deadline that expires while an attempt is in flight surfaces as a
// go-redis "i/o timeout" whose chain does not unwrap to context.DeadlineExceeded,
// so it is classified as a fault: ERROR log and span error. The window is one
// Redis round trip, so a caller that reaches the lock with less than one RTT of
// budget left reads as an outage. Bound the budget before calling rather than
// expecting this function to tell the two apart.
//
// The error chain cannot answer "was it the caller's deadline?" either: an
// unreachable node's error carries context.DeadlineExceeded from redsync's own
// per-attempt timeout, so errors.Is(err, context.DeadlineExceeded) never means
// the caller's deadline. That is why the caller's ctx.Err() is read, not the
// chain.
//
// A caller that cancels itself mid-request needs no arm of its own: the loop
// only ever reaches this with a contended attempt behind it or with Redis
// silent, and MEASURED against go-redis, a cancellation that lands after the
// command is on the wire is not observed until the read returns on its own
// timeout — which is a Redis that stopped answering, reported as one.
func classifyLockFailure(err error, callerDoneAtStart bool) lockOutcome {
	switch {
	case isLockContention(err):
		return lockContended
	case callerDoneAtStart:
		return lockCallerDone
	default:
		return lockFaulted
	}
}

// isLockContention reports whether err means the lock is simply held elsewhere,
// as opposed to an infrastructure fault. It is the single classifier both
// WithLockOptions and TryLock read, so the two entry points cannot disagree
// again about what a contended lock is.
//
// The classification uses redsync's typed sentinels rather than string
// matching. MEASURED against redsync v4.17: a single node that already holds
// the key yields *ErrTaken, and an attempt that took the key but spent its whole
// validity window doing so yields ErrFailed. redsync returns a bare ErrFailed
// from its OWN retry loop too, when a context cuts that loop short — which is
// why acquireLock runs the loop here instead, one single attempt at a time, and
// never feeds this function an error from a loop it did not control.
//
// A nil error is not contention.
func isLockContention(err error) bool {
	var errTaken *redsync.ErrTaken

	return errors.Is(err, redsync.ErrFailed) || errors.As(err, &errTaken)
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
