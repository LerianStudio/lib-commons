package redis

import (
	"context"

	"github.com/go-redsync/redsync/v4"
)

// DistributedLocker provides an interface for distributed locking operations.
// This interface allows for easy mocking in tests without requiring a real Redis instance.
//
// Example test implementation:
//
//	type MockDistributedLock struct{}
//
//	func (m *MockDistributedLock) WithLock(ctx context.Context, lockKey string, fn func() error) error {
//	    // In tests, just execute the function without actual locking
//	    return fn()
//	}
//
//	func (m *MockDistributedLock) WithLockOptions(ctx context.Context, lockKey string, opts LockOptions, fn func() error) error {
//	    return fn()
//	}
type DistributedLocker interface {
	// WithLock executes a function while holding a distributed lock with default options.
	// The lock is automatically released when the function returns.
	WithLock(ctx context.Context, lockKey string, fn func() error) error

	// WithLockOptions executes a function while holding a distributed lock with custom options.
	// Use this for fine-grained control over lock behavior.
	WithLockOptions(ctx context.Context, lockKey string, opts LockOptions, fn func() error) error

	// TryLock attempts to acquire a lock without retrying.
	// Returns the mutex and true if lock was acquired, nil and false otherwise.
	TryLock(ctx context.Context, lockKey string) (*redsync.Mutex, bool, error)

	// Unlock releases a previously acquired lock (used with TryLock).
	Unlock(ctx context.Context, mutex *redsync.Mutex) error
}

// Ensure DistributedLock implements DistributedLocker interface at compile time
var _ DistributedLocker = (*DistributedLock)(nil)
