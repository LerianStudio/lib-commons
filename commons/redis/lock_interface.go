package redis

import (
	"context"
)

// LockHandle represents an acquired distributed lock.
// It is obtained from TryLock and must be released via its Unlock method.
//
// Example usage:
//
//	handle, acquired, err := locker.TryLock(ctx, "lock:resource:123")
//	if err != nil {
//	    return err
//	}
//	if !acquired {
//	    return nil // lock busy, skip
//	}
//	defer handle.Unlock(ctx)
//	// ... critical section ...
type LockHandle interface {
	// Unlock releases the distributed lock.
	Unlock(ctx context.Context) error
}

// LockExtender is an optional capability of a LockHandle: renewing the lease
// before it expires, so work that outlives the lock's expiry keeps the lock
// without re-acquiring it. The handles RedisLockManager returns implement it;
// it is deliberately a separate interface, not a method on LockHandle, so that
// adding it cannot break an external LockHandle implementation or mock.
// Callers discover it with a type assertion:
//
//	extender, ok := handle.(redis.LockExtender)
//	if !ok {
//	    return errors.New("lock handle cannot be extended")
//	}
//
//	renewed, err := extender.Extend(ctx)
//	if err != nil {
//	    return err // Redis fault, or ctx ended: the lease may still be ours
//	}
//	if !renewed {
//	    return errLeaseLost // expired or taken by another holder: stop the work
//	}
type LockExtender interface {
	// Extend renews the lease to the full expiry the lock was acquired with.
	// It returns (true, nil) when renewed and (false, nil) when the key is no
	// longer ours — it expired, or another holder took it — in which case the
	// caller no longer has mutual exclusion and must stop. Any error means
	// nothing was learned about the lease: the caller's own context ended
	// (the error unwraps to that context error) or Redis did not answer.
	Extend(ctx context.Context) (bool, error)
}

// LockManager provides an interface for distributed locking operations.
// This interface allows for easy mocking in tests without requiring a real Redis instance.
//
// Example test implementation:
//
//	type MockLockManager struct{}
//
//	func (m *MockLockManager) WithLock(ctx context.Context, lockKey string, fn func(context.Context) error) error {
//	    // In tests, just execute the function without actual locking
//	    return fn(ctx)
//	}
//
//	func (m *MockLockManager) WithLockOptions(ctx context.Context, lockKey string, opts LockOptions, fn func(context.Context) error) error {
//	    return fn(ctx)
//	}
//
//	func (m *MockLockManager) TryLock(ctx context.Context, lockKey string) (LockHandle, bool, error) {
//	    return &mockHandle{}, true, nil
//	}
type LockManager interface {
	// WithLock executes a function while holding a distributed lock with default options.
	// The lock is automatically released when the function returns.
	WithLock(ctx context.Context, lockKey string, fn func(context.Context) error) error

	// WithLockOptions executes a function while holding a distributed lock with custom options.
	// Use this for fine-grained control over lock behavior.
	WithLockOptions(ctx context.Context, lockKey string, opts LockOptions, fn func(context.Context) error) error

	// TryLock attempts to acquire a lock without retrying.
	// Returns the handle and true if lock was acquired, nil and false otherwise.
	// Use LockHandle.Unlock to release the lock when done.
	// It uses a fixed 10-second expiry; a caller needing a different one on a
	// single attempt uses (*RedisLockManager).TryLockWithOptions, which is
	// deliberately absent from this interface so that adding it cannot break
	// an external implementer.
	TryLock(ctx context.Context, lockKey string) (LockHandle, bool, error)
}

// Ensure RedisLockManager implements LockManager interface at compile time.
var _ LockManager = (*RedisLockManager)(nil)

// Ensure the handle RedisLockManager returns can be extended.
var _ LockExtender = (*lockHandle)(nil)
