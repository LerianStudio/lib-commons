package redis

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTestRedis creates a miniredis server for testing
func setupTestRedis(t *testing.T) (*RedisConnection, func()) {
	mr := miniredis.RunT(t)

	conn := &RedisConnection{
		Address: []string{mr.Addr()},
		DB:      0,
	}

	client := redis.NewClient(&redis.Options{
		Addr: mr.Addr(),
	})

	conn.Client = client
	conn.Connected = true

	cleanup := func() {
		client.Close()
		mr.Close()
	}

	return conn, cleanup
}

// TestDistributedLock_WithLock tests basic locking functionality
func TestDistributedLock_WithLock(t *testing.T) {
	conn, cleanup := setupTestRedis(t)
	defer cleanup()

	lock, err := NewDistributedLock(conn)
	require.NoError(t, err)

	ctx := context.Background()
	executed := false

	err = lock.WithLock(ctx, "test:lock", func() error {
		executed = true
		return nil
	})

	assert.NoError(t, err)
	assert.True(t, executed, "function should have been executed")
}

// TestDistributedLock_WithLock_Error tests error propagation
func TestDistributedLock_WithLock_Error(t *testing.T) {
	conn, cleanup := setupTestRedis(t)
	defer cleanup()

	lock, err := NewDistributedLock(conn)
	require.NoError(t, err)

	ctx := context.Background()
	expectedErr := assert.AnError

	err = lock.WithLock(ctx, "test:lock", func() error {
		return expectedErr
	})

	assert.Error(t, err)
	assert.Equal(t, expectedErr, err)
}

// TestDistributedLock_ConcurrentExecution tests that locks prevent concurrent execution
func TestDistributedLock_ConcurrentExecution(t *testing.T) {
	conn, cleanup := setupTestRedis(t)
	defer cleanup()

	lock, err := NewDistributedLock(conn)
	require.NoError(t, err)

	ctx := context.Background()
	var counter int32
	var maxConcurrent int32
	var currentConcurrent int32

	const numGoroutines = 10

	// Use more patient lock options for testing
	opts := LockOptions{
		Expiry:      5 * time.Second,
		Tries:       50, // Many retries to ensure all goroutines get a chance
		RetryDelay:  50 * time.Millisecond,
		DriftFactor: 0.01,
	}

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for range numGoroutines {
		go func() {
			defer wg.Done()

			err := lock.WithLockOptions(ctx, "test:concurrent:lock", opts, func() error {
				// Track concurrent executions
				concurrent := atomic.AddInt32(&currentConcurrent, 1)
				if concurrent > atomic.LoadInt32(&maxConcurrent) {
					atomic.StoreInt32(&maxConcurrent, concurrent)
				}

				// Increment counter
				atomic.AddInt32(&counter, 1)

				// Simulate work
				time.Sleep(10 * time.Millisecond)

				// Decrement concurrent counter
				atomic.AddInt32(&currentConcurrent, -1)

				return nil
			})

			assert.NoError(t, err)
		}()
	}

	wg.Wait()

	assert.Equal(t, int32(numGoroutines), counter, "all goroutines should have executed")
	assert.Equal(t, int32(1), maxConcurrent, "at most 1 goroutine should execute concurrently")
}

// TestDistributedLock_TryLock tests non-blocking lock acquisition
func TestDistributedLock_TryLock(t *testing.T) {
	conn, cleanup := setupTestRedis(t)
	defer cleanup()

	lock, err := NewDistributedLock(conn)
	require.NoError(t, err)

	ctx := context.Background()

	// First lock should succeed
	mutex1, acquired1, err1 := lock.TryLock(ctx, "test:trylock")
	assert.NoError(t, err1)
	assert.True(t, acquired1, "first lock should be acquired")
	assert.NotNil(t, mutex1)

	if acquired1 {
		defer lock.Unlock(ctx, mutex1)
	}

	// Second lock should fail (already held)
	mutex2, acquired2, err2 := lock.TryLock(ctx, "test:trylock")
	assert.NoError(t, err2)
	assert.False(t, acquired2, "second lock should not be acquired")
	assert.Nil(t, mutex2)
}

// TestDistributedLock_WithLockOptions tests custom lock options
func TestDistributedLock_WithLockOptions(t *testing.T) {
	conn, cleanup := setupTestRedis(t)
	defer cleanup()

	lock, err := NewDistributedLock(conn)
	require.NoError(t, err)

	ctx := context.Background()
	executed := false

	opts := LockOptions{
		Expiry:      5 * time.Second,
		Tries:       5,
		RetryDelay:  100 * time.Millisecond,
		DriftFactor: 0.01,
	}

	err = lock.WithLockOptions(ctx, "test:lock:options", opts, func() error {
		executed = true
		return nil
	})

	assert.NoError(t, err)
	assert.True(t, executed, "function should have been executed")
}

// TestDistributedLock_DefaultLockOptions tests default options
func TestDistributedLock_DefaultLockOptions(t *testing.T) {
	opts := DefaultLockOptions()

	assert.Equal(t, 10*time.Second, opts.Expiry)
	assert.Equal(t, 3, opts.Tries)
	assert.Equal(t, 500*time.Millisecond, opts.RetryDelay)
	assert.Equal(t, 0.01, opts.DriftFactor)
}

// TestDistributedLock_Unlock tests explicit unlocking
func TestDistributedLock_Unlock(t *testing.T) {
	conn, cleanup := setupTestRedis(t)
	defer cleanup()

	lock, err := NewDistributedLock(conn)
	require.NoError(t, err)

	ctx := context.Background()

	mutex, acquired, err := lock.TryLock(ctx, "test:unlock")
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotNil(t, mutex)

	// Unlock should succeed
	err = lock.Unlock(ctx, mutex)
	assert.NoError(t, err)

	// After unlock, another lock should be acquirable
	mutex2, acquired2, err2 := lock.TryLock(ctx, "test:unlock")
	assert.NoError(t, err2)
	assert.True(t, acquired2)
	assert.NotNil(t, mutex2)

	if acquired2 {
		lock.Unlock(ctx, mutex2)
	}
}

// TestDistributedLock_NilMutexUnlock tests error handling for nil mutex
func TestDistributedLock_NilMutexUnlock(t *testing.T) {
	conn, cleanup := setupTestRedis(t)
	defer cleanup()

	lock, err := NewDistributedLock(conn)
	require.NoError(t, err)

	ctx := context.Background()

	err = lock.Unlock(ctx, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mutex is nil")
}

// TestDistributedLock_ContextCancellation tests lock behavior with context cancellation
func TestDistributedLock_ContextCancellation(t *testing.T) {
	conn, cleanup := setupTestRedis(t)
	defer cleanup()

	lock, err := NewDistributedLock(conn)
	require.NoError(t, err)

	// Create a context that's already cancelled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	executed := false
	err = lock.WithLock(ctx, "test:cancelled", func() error {
		executed = true
		return nil
	})

	assert.Error(t, err)
	assert.False(t, executed, "function should not execute with cancelled context")
}

// TestDistributedLock_MultipleLocksDifferentKeys tests multiple locks on different keys
func TestDistributedLock_MultipleLocksDifferentKeys(t *testing.T) {
	conn, cleanup := setupTestRedis(t)
	defer cleanup()

	lock, err := NewDistributedLock(conn)
	require.NoError(t, err)

	ctx := context.Background()

	var wg sync.WaitGroup
	var counter1, counter2 int32

	// Two different locks should not interfere with each other
	wg.Add(2)

	go func() {
		defer wg.Done()
		err := lock.WithLock(ctx, "test:lock:1", func() error {
			atomic.AddInt32(&counter1, 1)
			time.Sleep(50 * time.Millisecond)
			return nil
		})
		assert.NoError(t, err)
	}()

	go func() {
		defer wg.Done()
		err := lock.WithLock(ctx, "test:lock:2", func() error {
			atomic.AddInt32(&counter2, 1)
			time.Sleep(50 * time.Millisecond)
			return nil
		})
		assert.NoError(t, err)
	}()

	wg.Wait()

	assert.Equal(t, int32(1), counter1)
	assert.Equal(t, int32(1), counter2)
}

// TestDistributedLock_PanicRecovery tests that locks are released even on panic
func TestDistributedLock_PanicRecovery(t *testing.T) {
	conn, cleanup := setupTestRedis(t)
	defer cleanup()

	lock, err := NewDistributedLock(conn)
	require.NoError(t, err)

	ctx := context.Background()

	// First call panics
	func() {
		defer func() {
			if r := recover(); r != nil {
				// Panic recovered as expected
			}
		}()

		lock.WithLock(ctx, "test:panic", func() error {
			panic("test panic")
		})
	}()

	// Second call should succeed (lock was released despite panic)
	executed := false
	err = lock.WithLock(ctx, "test:panic", func() error {
		executed = true
		return nil
	})

	assert.NoError(t, err)
	assert.True(t, executed, "lock should be available after panic")
}

// TestDistributedLock_ConcurrentDifferentKeys tests high concurrency on different keys
func TestDistributedLock_ConcurrentDifferentKeys(t *testing.T) {
	conn, cleanup := setupTestRedis(t)
	defer cleanup()

	lock, err := NewDistributedLock(conn)
	require.NoError(t, err)

	ctx := context.Background()
	const numKeys = 5
	const numGoroutinesPerKey = 4

	counters := make([]int32, numKeys)
	var wg sync.WaitGroup

	// Use patient lock options for concurrent scenario
	opts := LockOptions{
		Expiry:      5 * time.Second,
		Tries:       50,
		RetryDelay:  50 * time.Millisecond,
		DriftFactor: 0.01,
	}

	// Channel to collect errors from goroutines
	errCh := make(chan error, numKeys*numGoroutinesPerKey)

	for keyIdx := range numKeys {
		for range numGoroutinesPerKey {
			wg.Add(1)
			go func(k int) {
				defer wg.Done()

				lockKey := fmt.Sprintf("test:concurrent:key:%d", k)
				err := lock.WithLockOptions(ctx, lockKey, opts, func() error {
					atomic.AddInt32(&counters[k], 1)
					time.Sleep(5 * time.Millisecond)
					return nil
				})
				if err != nil {
					errCh <- err
				}
			}(keyIdx)
		}
	}

	wg.Wait()
	close(errCh)

	// Assert errors in main goroutine
	for err := range errCh {
		assert.NoError(t, err)
	}

	// Each counter should have been incremented by numGoroutinesPerKey
	for i, count := range counters {
		assert.Equal(t, int32(numGoroutinesPerKey), count, "counter %d should be %d", i, numGoroutinesPerKey)
	}
}

// TestDistributedLock_ReentrantNotSupported tests that re-entrant locking is not supported
func TestDistributedLock_ReentrantNotSupported(t *testing.T) {
	conn, cleanup := setupTestRedis(t)
	defer cleanup()

	lock, err := NewDistributedLock(conn)
	require.NoError(t, err)

	ctx := context.Background()

	err = lock.WithLock(ctx, "test:reentrant", func() error {
		// Try to acquire the same lock again (this should fail/timeout)
		opts := LockOptions{
			Expiry:     1 * time.Second,
			Tries:      1, // Only try once
			RetryDelay: 100 * time.Millisecond,
		}

		err := lock.WithLockOptions(ctx, "test:reentrant", opts, func() error {
			return nil
		})

		// This should fail because the lock is already held
		assert.Error(t, err)
		return nil
	})

	assert.NoError(t, err)
}

// TestDistributedLock_ShortTimeout tests behavior with very short timeout
func TestDistributedLock_ShortTimeout(t *testing.T) {
	conn, cleanup := setupTestRedis(t)
	defer cleanup()

	lock, err := NewDistributedLock(conn)
	require.NoError(t, err)

	ctx := context.Background()

	// First goroutine holds the lock
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		lock.WithLock(ctx, "test:timeout", func() error {
			time.Sleep(200 * time.Millisecond) // Hold for 200ms
			return nil
		})
	}()

	time.Sleep(50 * time.Millisecond) // Ensure first goroutine has the lock

	// Second goroutine tries with short timeout
	go func() {
		defer wg.Done()

		opts := LockOptions{
			Expiry:     1 * time.Second,
			Tries:      1, // Give up quickly
			RetryDelay: 50 * time.Millisecond,
		}

		err := lock.WithLockOptions(ctx, "test:timeout", opts, func() error {
			return nil
		})

		// Should fail to acquire
		assert.Error(t, err)
	}()

	wg.Wait()
}
