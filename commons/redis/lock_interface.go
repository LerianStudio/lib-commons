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
	TryLock(ctx context.Context, lockKey string) (LockHandle, bool, error)
}

// Ensure RedisLockManager implements LockManager interface at compile time.
var _ LockManager = (*RedisLockManager)(nil)
