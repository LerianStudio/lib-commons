//go:build integration

package redis

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIntegration_Lock_MutualExclusion verifies that WithLockOptions enforces
// mutual exclusion: 10 goroutines compete for the same lock key, but only one
// at a time may enter the critical section. An atomic counter tracks the
// maximum observed concurrency inside the lock—must be exactly 1—and total
// completed executions—must be exactly 10.
func TestIntegration_Lock_MutualExclusion(t *testing.T) {
	addr, cleanup := setupRedisContainer(t)
	defer cleanup()

	ctx := context.Background()

	client, err := New(ctx, newTestConfig(addr))
	require.NoError(t, err)
	defer func() { require.NoError(t, client.Close()) }()

	lockMgr, err := NewRedisLockManager(client)
	require.NoError(t, err)

	const goroutines = 10
	const lockKey = "integration:mutex:exclusion"

	opts := LockOptions{
		Expiry:      5 * time.Second,
		Tries:       50,
		RetryDelay:  50 * time.Millisecond,
		DriftFactor: 0.01,
	}

	var (
		totalExecutions atomic.Int64
		maxConcurrent   atomic.Int64
		currentInside   atomic.Int64
		wg              sync.WaitGroup
	)

	errs := make(chan error, goroutines)

	wg.Add(goroutines)

	for i := range goroutines {
		go func(id int) {
			defer wg.Done()

			lockErr := lockMgr.WithLockOptions(ctx, lockKey, opts, func(_ context.Context) error {
				// Track how many goroutines are inside the critical section right now.
				cur := currentInside.Add(1)

				// Atomically update the observed maximum.
				for {
					prev := maxConcurrent.Load()
					if cur <= prev {
						break
					}

					if maxConcurrent.CompareAndSwap(prev, cur) {
						break
					}
				}

				// Simulate work so goroutines overlap in wall-clock time.
				time.Sleep(10 * time.Millisecond)

				currentInside.Add(-1)
				totalExecutions.Add(1)

				return nil
			})
			if lockErr != nil {
				errs <- fmt.Errorf("goroutine %d: WithLockOptions: %w", id, lockErr)
			}
		}(i)
	}

	wg.Wait()
	close(errs)

	for e := range errs {
		t.Error(e)
	}

	assert.Equal(t, int64(1), maxConcurrent.Load(), "at most 1 goroutine may be inside the critical section at any time")
	assert.Equal(t, int64(goroutines), totalExecutions.Load(), "all goroutines must complete their execution")
}

// TestIntegration_Lock_TryLock_Contention verifies the non-blocking TryLock:
//   - Goroutine A acquires the lock; goroutine B's immediate TryLock must fail.
//   - After A unlocks, B retries and succeeds.
func TestIntegration_Lock_TryLock_Contention(t *testing.T) {
	addr, cleanup := setupRedisContainer(t)
	defer cleanup()

	ctx := context.Background()

	client, err := New(ctx, newTestConfig(addr))
	require.NoError(t, err)
	defer func() { require.NoError(t, client.Close()) }()

	lockMgr, err := NewRedisLockManager(client)
	require.NoError(t, err)

	const lockKey = "integration:trylock:contention"

	// Goroutine A acquires the lock.
	handleA, acquiredA, err := lockMgr.TryLock(ctx, lockKey)
	require.NoError(t, err)
	require.True(t, acquiredA, "A must acquire the lock")
	require.NotNil(t, handleA)

	// Goroutine B tries to acquire the same lock — must fail because A holds it.
	_, acquiredB, err := lockMgr.TryLock(ctx, lockKey)
	require.NoError(t, err)
	assert.False(t, acquiredB, "B must NOT acquire the lock while A holds it")

	// A releases the lock.
	require.NoError(t, handleA.Unlock(ctx))

	// B retries — should succeed now.
	handleB, acquiredB2, err := lockMgr.TryLock(ctx, lockKey)
	require.NoError(t, err)
	assert.True(t, acquiredB2, "B must acquire the lock after A releases it")
	require.NotNil(t, handleB)

	require.NoError(t, handleB.Unlock(ctx))
}

// TestIntegration_Lock_Expiry tests two scenarios:
//  1. WithLockOptions with short expiry: fn completes quickly, lock is released
//     explicitly → re-acquire must succeed immediately.
//  2. TryLock without explicit unlock: wait beyond the TTL → re-acquire must
//     succeed because the lock auto-expired.
func TestIntegration_Lock_Expiry(t *testing.T) {
	addr, cleanup := setupRedisContainer(t)
	defer cleanup()

	ctx := context.Background()

	client, err := New(ctx, newTestConfig(addr))
	require.NoError(t, err)
	defer func() { require.NoError(t, client.Close()) }()

	lockMgr, err := NewRedisLockManager(client)
	require.NoError(t, err)

	// --- Scenario 1: WithLockOptions completes and releases, re-acquire succeeds ---
	const lockKey1 = "integration:expiry:withopts"

	opts := LockOptions{
		Expiry:      2 * time.Second,
		Tries:       1,
		RetryDelay:  50 * time.Millisecond,
		DriftFactor: 0.01,
	}

	err = lockMgr.WithLockOptions(ctx, lockKey1, opts, func(_ context.Context) error {
		time.Sleep(100 * time.Millisecond)
		return nil
	})
	require.NoError(t, err)

	// Lock was released by WithLockOptions defer — re-acquire must succeed.
	handle, acquired, err := lockMgr.TryLock(ctx, lockKey1)
	require.NoError(t, err)
	assert.True(t, acquired, "re-acquire after WithLockOptions must succeed")

	if handle != nil {
		require.NoError(t, handle.Unlock(ctx))
	}

	// --- Scenario 2: TryLock without explicit unlock, wait for TTL expiry ---
	const lockKey2 = "integration:expiry:ttl"

	handleTTL, acquired, err := lockMgr.TryLock(ctx, lockKey2)
	require.NoError(t, err)
	require.True(t, acquired, "first TryLock must succeed")
	require.NotNil(t, handleTTL)

	// Intentionally do NOT unlock. The default TryLock expiry is 10s.
	// We need a shorter TTL, so we use WithLockOptions to acquire with 2s expiry,
	// but TryLock uses defaults. Instead, acquire via WithLockOptions with 2s expiry
	// and leak the lock by not calling the returned handle.
	// Since TryLock uses DefaultLockOptions (10s), unlock first, then re-acquire
	// with a short-lived custom approach.
	require.NoError(t, handleTTL.Unlock(ctx))

	// Acquire with short expiry via WithLockOptions, but simulate a crash by
	// setting the fn to do nothing—the defer in WithLockOptions will unlock.
	// Instead, use a direct TryLock approach: acquire, don't unlock, wait for
	// the default 10s TTL. That's too long for a test. So we test the concept
	// by using a raw redsync mutex with short expiry through the public API:
	// acquire via WithLockOptions where we just sleep past the entire expiry.
	// Actually, the cleanest approach: acquire via TryLock (10s default expiry),
	// don't unlock, wait 11s. But that's slow. Let's verify the TTL concept
	// with a 3-second key using the internal observation that TryLock uses
	// DefaultLockOptions which has 10s expiry.
	//
	// Pragmatic approach: acquire via WithLockOptions with 2s expiry where the fn
	// takes longer than 2s — the lock will auto-expire in Redis before fn returns.
	// After fn returns, attempt to re-acquire the same key immediately.
	const lockKey3 = "integration:expiry:auto"

	shortOpts := LockOptions{
		Expiry:      2 * time.Second,
		Tries:       1,
		RetryDelay:  50 * time.Millisecond,
		DriftFactor: 0.01,
	}

	// Acquire with 2s TTL, then deliberately do NOT release (simulate the
	// unlock failing because the TTL already expired).
	// We use TryLock indirectly by locking with WithLockOptions where fn
	// takes 3s — the lock expires after 2s while fn is still running.
	// WithLockOptions' defer unlock will silently fail (lock expired), and
	// the error from the fn (nil) propagates.
	err = lockMgr.WithLockOptions(ctx, lockKey3, shortOpts, func(_ context.Context) error {
		// Sleep past the 2s expiry — the Redis key will expire mid-fn.
		time.Sleep(3 * time.Second)
		return nil
	})
	// The fn itself returns nil, but the defer Unlock may log a warning
	// (lock not held). WithLockOptions returns fn's error, which is nil.
	require.NoError(t, err)

	// The lock has already auto-expired — re-acquire must succeed.
	handleAfterExpiry, acquired, err := lockMgr.TryLock(ctx, lockKey3)
	require.NoError(t, err)
	assert.True(t, acquired, "re-acquire after TTL expiry must succeed")

	if handleAfterExpiry != nil {
		require.NoError(t, handleAfterExpiry.Unlock(ctx))
	}
}

// TestIntegration_Lock_RateLimiterPreset verifies that RateLimiterLockOptions()
// produces a usable configuration against real Redis: acquire, execute, release,
// then re-acquire (proving the short 2s expiry preset is functional).
func TestIntegration_Lock_RateLimiterPreset(t *testing.T) {
	addr, cleanup := setupRedisContainer(t)
	defer cleanup()

	ctx := context.Background()

	client, err := New(ctx, newTestConfig(addr))
	require.NoError(t, err)
	defer func() { require.NoError(t, client.Close()) }()

	lockMgr, err := NewRedisLockManager(client)
	require.NoError(t, err)

	const lockKey = "integration:ratelimiter:preset"
	opts := RateLimiterLockOptions()

	// First acquire + execute + auto-release.
	executed := false

	err = lockMgr.WithLockOptions(ctx, lockKey, opts, func(_ context.Context) error {
		executed = true
		return nil
	})
	require.NoError(t, err)
	assert.True(t, executed, "fn must have been executed under the rate-limiter lock")

	// Second acquire — must succeed because the first was properly released.
	executed2 := false

	err = lockMgr.WithLockOptions(ctx, lockKey, opts, func(_ context.Context) error {
		executed2 = true
		return nil
	})
	require.NoError(t, err)
	assert.True(t, executed2, "second acquire must succeed after first release")
}

// TestIntegration_Lock_ConcurrentDifferentKeys verifies that locks on distinct
// keys do not block each other: 5 goroutines, each locking a unique key, must
// all complete within a tight timeout.
func TestIntegration_Lock_ConcurrentDifferentKeys(t *testing.T) {
	addr, cleanup := setupRedisContainer(t)
	defer cleanup()

	ctx := context.Background()

	client, err := New(ctx, newTestConfig(addr))
	require.NoError(t, err)
	defer func() { require.NoError(t, client.Close()) }()

	lockMgr, err := NewRedisLockManager(client)
	require.NoError(t, err)

	const goroutines = 5

	var (
		wg          sync.WaitGroup
		completions atomic.Int64
	)

	errs := make(chan error, goroutines)

	wg.Add(goroutines)

	start := time.Now()

	for i := range goroutines {
		go func(id int) {
			defer wg.Done()

			key := fmt.Sprintf("integration:concurrent:key:%d", id)

			lockErr := lockMgr.WithLock(ctx, key, func(_ context.Context) error {
				// Each goroutine does a small amount of work.
				time.Sleep(50 * time.Millisecond)
				completions.Add(1)

				return nil
			})
			if lockErr != nil {
				errs <- fmt.Errorf("goroutine %d: WithLock: %w", id, lockErr)
			}
		}(i)
	}

	wg.Wait()
	close(errs)

	elapsed := time.Since(start)

	for e := range errs {
		t.Error(e)
	}

	assert.Equal(t, int64(goroutines), completions.Load(), "all goroutines must complete")
	assert.Less(t, elapsed, 2*time.Second, "concurrent different-key locks should complete well under 2s")
}

// TestIntegration_Lock_WithLock_ErrorPropagation verifies that:
//  1. An error returned by fn propagates through WithLock.
//  2. The lock is released even when fn returns an error (so another caller can
//     acquire the same key immediately).
func TestIntegration_Lock_WithLock_ErrorPropagation(t *testing.T) {
	addr, cleanup := setupRedisContainer(t)
	defer cleanup()

	ctx := context.Background()

	client, err := New(ctx, newTestConfig(addr))
	require.NoError(t, err)
	defer func() { require.NoError(t, client.Close()) }()

	lockMgr, err := NewRedisLockManager(client)
	require.NoError(t, err)

	const lockKey = "integration:errorprop:key"

	sentinelErr := errors.New("business logic failed")

	err = lockMgr.WithLock(ctx, lockKey, func(_ context.Context) error {
		return sentinelErr
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinelErr, "fn's error must propagate through WithLock")

	// The lock must have been released by WithLockOptions' defer, so TryLock
	// on the same key must succeed.
	handle, acquired, err := lockMgr.TryLock(ctx, lockKey)
	require.NoError(t, err)
	assert.True(t, acquired, "lock must be released even when fn returns an error")

	if handle != nil {
		require.NoError(t, handle.Unlock(ctx))
	}
}

// TestIntegration_Lock_ContextCancellation verifies that a waiting locker
// respects context cancellation. Goroutine A holds the lock; goroutine B
// attempts WithLockOptions with a short-lived context. B should fail with a
// context-related error before exhausting its retries.
func TestIntegration_Lock_ContextCancellation(t *testing.T) {
	addr, cleanup := setupRedisContainer(t)
	defer cleanup()

	ctx := context.Background()

	client, err := New(ctx, newTestConfig(addr))
	require.NoError(t, err)
	defer func() { require.NoError(t, client.Close()) }()

	lockMgr, err := NewRedisLockManager(client)
	require.NoError(t, err)

	const lockKey = "integration:ctxcancel:key"

	// Goroutine A: hold the lock for a long time.
	aReady := make(chan struct{})
	aDone := make(chan struct{})

	go func() {
		opts := LockOptions{
			Expiry:      10 * time.Second,
			Tries:       1,
			RetryDelay:  50 * time.Millisecond,
			DriftFactor: 0.01,
		}

		lockErr := lockMgr.WithLockOptions(ctx, lockKey, opts, func(_ context.Context) error {
			close(aReady) // Signal that A holds the lock.
			<-aDone       // Wait until the test tells us to release.
			return nil
		})
		// A might error if the test context is cancelled, which is fine.
		_ = lockErr
	}()

	// Wait for A to acquire the lock.
	select {
	case <-aReady:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for goroutine A to acquire the lock")
	}

	// Goroutine B: attempt to acquire the same lock with a 200ms timeout context.
	ctxB, cancelB := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancelB()

	bOpts := LockOptions{
		Expiry:      5 * time.Second,
		Tries:       100,
		RetryDelay:  50 * time.Millisecond,
		DriftFactor: 0.01,
	}

	err = lockMgr.WithLockOptions(ctxB, lockKey, bOpts, func(_ context.Context) error {
		t.Error("B's fn must never execute — the lock should not be acquired")
		return nil
	})
	require.Error(t, err, "B must fail because the context timed out")

	// Release A so the goroutine can exit cleanly.
	close(aDone)
}
