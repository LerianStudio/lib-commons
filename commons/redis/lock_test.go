//go:build unit

package redis

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setupTestClient(t *testing.T) *Client {
	t.Helper()

	mr := miniredis.RunT(t)

	client, err := New(context.Background(), Config{
		Topology: Topology{
			Standalone: &StandaloneTopology{Address: mr.Addr()},
		},
		Logger: &log.NopLogger{},
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		require.NoError(t, client.Close())
		mr.Close()
	})

	return client
}

// setupTestLock creates a Redis client and RedisLockManager for testing.
func setupTestLock(t *testing.T) (*Client, *RedisLockManager) {
	t.Helper()

	client := setupTestClient(t)

	lock, err := NewRedisLockManager(client)
	require.NoError(t, err)

	return client, lock
}

func TestRedisLockManager_WithLock(t *testing.T) {
	client := setupTestClient(t)

	lock, err := NewRedisLockManager(client)
	require.NoError(t, err)

	executed := false
	err = lock.WithLock(context.Background(), "test:lock", func(context.Context) error {
		executed = true
		return nil
	})

	require.NoError(t, err)
	assert.True(t, executed)
}

func TestRedisLockManager_WithLock_ErrorPropagation(t *testing.T) {
	client := setupTestClient(t)

	lock, err := NewRedisLockManager(client)
	require.NoError(t, err)

	expectedErr := errors.New("boom")
	err = lock.WithLock(context.Background(), "test:lock", func(context.Context) error {
		return expectedErr
	})

	require.Error(t, err)
	assert.ErrorIs(t, err, expectedErr)
}

func TestRedisLockManager_ConcurrentExecutionSingleKey(t *testing.T) {
	client := setupTestClient(t)

	lock, err := NewRedisLockManager(client)
	require.NoError(t, err)

	ctx := context.Background()
	var currentConcurrent int32
	var maxConcurrent int32
	var total int32

	opts := LockOptions{
		Expiry:      5 * time.Second,
		Tries:       50,
		RetryDelay:  20 * time.Millisecond,
		DriftFactor: 0.01,
	}

	const workers = 10
	errCh := make(chan error, workers)
	var wg sync.WaitGroup
	wg.Add(workers)

	for range workers {
		go func() {
			defer wg.Done()

			err := lock.WithLockOptions(ctx, "test:concurrent", opts, func(context.Context) error {
				active := atomic.AddInt32(&currentConcurrent, 1)
				if active > atomic.LoadInt32(&maxConcurrent) {
					atomic.StoreInt32(&maxConcurrent, active)
				}

				atomic.AddInt32(&total, 1)
				time.Sleep(5 * time.Millisecond)
				atomic.AddInt32(&currentConcurrent, -1)

				return nil
			})
			if err != nil {
				errCh <- err
			}
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		require.NoError(t, err)
	}

	assert.Equal(t, int32(workers), total)
	assert.Equal(t, int32(1), maxConcurrent)
}

func TestRedisLockManager_TryLock_Contention(t *testing.T) {
	client := setupTestClient(t)

	lock, err := NewRedisLockManager(client)
	require.NoError(t, err)

	ctx := context.Background()

	handle1, acquired, err := lock.TryLock(ctx, "test:contention")
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotNil(t, handle1)
	defer func() {
		require.NoError(t, handle1.Unlock(ctx))
	}()

	handle2, acquired, err := lock.TryLock(ctx, "test:contention")
	require.NoError(t, err)
	assert.False(t, acquired)
	assert.Nil(t, handle2)
}

func TestRedisLockManager_PanicRecovery(t *testing.T) {
	client := setupTestClient(t)

	lock, err := NewRedisLockManager(client)
	require.NoError(t, err)

	ctx := context.Background()

	require.Panics(t, func() {
		_ = lock.WithLock(ctx, "test:panic", func(context.Context) error {
			panic("panic inside lock")
		})
	})

	executed := false
	err = lock.WithLock(ctx, "test:panic", func(context.Context) error {
		executed = true
		return nil
	})

	require.NoError(t, err)
	assert.True(t, executed)
}

func TestRedisLockManager_NilAndInitGuards(t *testing.T) {
	t.Run("new lock with nil client", func(t *testing.T) {
		lock, err := NewRedisLockManager(nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrNilClient)
		assert.Nil(t, lock)
	})

	t.Run("nil receiver", func(t *testing.T) {
		var dl *RedisLockManager
		ctx := context.Background()

		err := dl.WithLock(ctx, "test:key", func(context.Context) error { return nil })
		assert.ErrorContains(t, err, "lock manager is nil")

		err = dl.WithLockOptions(ctx, "test:key", DefaultLockOptions(), func(context.Context) error { return nil })
		assert.ErrorContains(t, err, "lock manager is nil")

		handle, acquired, err := dl.TryLock(ctx, "test:key")
		assert.ErrorContains(t, err, "lock manager is nil")
		assert.Nil(t, handle)
		assert.False(t, acquired)

		err = dl.Unlock(ctx, nil)
		assert.ErrorContains(t, err, "lock manager is nil")
	})

	t.Run("zero value lock is rejected", func(t *testing.T) {
		dl := &RedisLockManager{}
		ctx := context.Background()

		err := dl.WithLockOptions(ctx, "test:key", DefaultLockOptions(), func(context.Context) error { return nil })
		assert.ErrorContains(t, err, "distributed lock is not initialized")

		handle, acquired, err := dl.TryLock(ctx, "test:key")
		assert.ErrorContains(t, err, "distributed lock is not initialized")
		assert.Nil(t, handle)
		assert.False(t, acquired)
	})
}

func TestRedisLockManager_OptionValidation(t *testing.T) {
	client := setupTestClient(t)

	lock, err := NewRedisLockManager(client)
	require.NoError(t, err)

	err = lock.WithLockOptions(context.Background(), "", DefaultLockOptions(), func(context.Context) error { return nil })
	assert.ErrorContains(t, err, "lock key cannot be empty")

	err = lock.WithLockOptions(context.Background(), "test:key", DefaultLockOptions(), nil)
	assert.ErrorIs(t, err, ErrNilLockFn)

	err = lock.WithLockOptions(context.Background(), "test:key", LockOptions{
		Expiry:      0,
		Tries:       1,
		RetryDelay:  time.Millisecond,
		DriftFactor: 0.01,
	}, func(context.Context) error { return nil })
	assert.ErrorContains(t, err, "lock expiry must be greater than 0")

	err = lock.WithLockOptions(context.Background(), "test:key", LockOptions{
		Expiry:      time.Second,
		Tries:       0,
		RetryDelay:  time.Millisecond,
		DriftFactor: 0.01,
	}, func(context.Context) error { return nil })
	assert.ErrorContains(t, err, "lock tries must be at least 1")

	err = lock.WithLockOptions(context.Background(), "test:key", LockOptions{
		Expiry:      time.Second,
		Tries:       1,
		RetryDelay:  -time.Millisecond,
		DriftFactor: 0.01,
	}, func(context.Context) error { return nil })
	assert.ErrorContains(t, err, "lock retry delay cannot be negative")

	err = lock.WithLockOptions(context.Background(), "test:key", LockOptions{
		Expiry:      time.Second,
		Tries:       1,
		RetryDelay:  time.Millisecond,
		DriftFactor: 1,
	}, func(context.Context) error { return nil })
	assert.ErrorContains(t, err, "lock drift factor")

	// Tries exceeding max cap (1001 > maxLockTries=1000)
	err = lock.WithLockOptions(context.Background(), "test:key", LockOptions{
		Expiry:      time.Second,
		Tries:       1001,
		RetryDelay:  time.Millisecond,
		DriftFactor: 0.01,
	}, func(context.Context) error { return nil })
	assert.ErrorIs(t, err, ErrLockTriesExceeded)
}

func TestLockOptionFactories(t *testing.T) {
	defaultOpts := DefaultLockOptions()
	assert.Equal(t, 10*time.Second, defaultOpts.Expiry)
	assert.Equal(t, 3, defaultOpts.Tries)
	assert.Equal(t, 500*time.Millisecond, defaultOpts.RetryDelay)
	assert.Equal(t, 0.01, defaultOpts.DriftFactor)

	rateLimiterOpts := RateLimiterLockOptions()
	assert.Equal(t, 2*time.Second, rateLimiterOpts.Expiry)
	assert.Equal(t, 2, rateLimiterOpts.Tries)
	assert.Equal(t, 100*time.Millisecond, rateLimiterOpts.RetryDelay)
	assert.Equal(t, 0.01, rateLimiterOpts.DriftFactor)
}

func TestSafeLockKeyForLogs(t *testing.T) {
	safe := safeLockKeyForLogs("lock:tenant\n123")
	assert.NotContains(t, safe, "\n")
	assert.Contains(t, safe, "\\n")

	longKey := strings.Repeat("a", 1024)
	safeLong := safeLockKeyForLogs(longKey)
	assert.Contains(t, safeLong, "...(truncated)")
}

// --- New comprehensive test coverage below ---

func TestRedisLockManager_WithLock_ContextPassedToFn(t *testing.T) {
	_, lock := setupTestLock(t)

	type ctxKey string
	key := ctxKey("trace-id")

	ctx := context.WithValue(context.Background(), key, "abc-123")

	err := lock.WithLock(ctx, "test:ctx-pass", func(ctx context.Context) error {
		val, ok := ctx.Value(key).(string)
		assert.True(t, ok)
		assert.Equal(t, "abc-123", val)

		return nil
	})

	require.NoError(t, err)
}

func TestRedisLockManager_WithLockOptions_CustomExpiry(t *testing.T) {
	_, lock := setupTestLock(t)

	opts := LockOptions{
		Expiry:      30 * time.Second,
		Tries:       1,
		RetryDelay:  time.Millisecond,
		DriftFactor: 0.01,
	}

	executed := false

	err := lock.WithLockOptions(context.Background(), "test:custom-opts", opts, func(context.Context) error {
		executed = true
		return nil
	})

	require.NoError(t, err)
	assert.True(t, executed)
}

func TestRedisLockManager_WithLock_WhitespaceOnlyKey(t *testing.T) {
	_, lock := setupTestLock(t)

	err := lock.WithLock(context.Background(), "   ", func(context.Context) error {
		return nil
	})

	require.Error(t, err)
	assert.ErrorContains(t, err, "lock key cannot be empty")
}

func TestRedisLockManager_WithLock_TabAndNewlineKey(t *testing.T) {
	_, lock := setupTestLock(t)

	err := lock.WithLock(context.Background(), "\t\n", func(context.Context) error {
		return nil
	})

	require.Error(t, err)
	assert.ErrorContains(t, err, "lock key cannot be empty")
}

func TestRedisLockManager_TryLock_EmptyKey(t *testing.T) {
	_, lock := setupTestLock(t)

	handle, acquired, err := lock.TryLock(context.Background(), "")
	require.Error(t, err)
	assert.ErrorContains(t, err, "lock key cannot be empty")
	assert.False(t, acquired)
	assert.Nil(t, handle)
}

func TestRedisLockManager_TryLock_WhitespaceOnlyKey(t *testing.T) {
	_, lock := setupTestLock(t)

	handle, acquired, err := lock.TryLock(context.Background(), "   ")
	require.Error(t, err)
	assert.ErrorContains(t, err, "lock key cannot be empty")
	assert.False(t, acquired)
	assert.Nil(t, handle)
}

func TestRedisLockManager_TryLock_SuccessfulAcquireAndRelease(t *testing.T) {
	_, lock := setupTestLock(t)

	ctx := context.Background()

	handle, acquired, err := lock.TryLock(ctx, "test:try-success")
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotNil(t, handle)

	// Release the lock via LockHandle
	err = handle.Unlock(ctx)
	require.NoError(t, err)

	// Lock should be available again
	handle2, acquired2, err := lock.TryLock(ctx, "test:try-success")
	require.NoError(t, err)
	assert.True(t, acquired2)
	assert.NotNil(t, handle2)

	// Clean up
	require.NoError(t, handle2.Unlock(ctx))
}

func TestRedisLockManager_TryLock_DifferentKeysNoContention(t *testing.T) {
	_, lock := setupTestLock(t)

	ctx := context.Background()

	handle1, acquired1, err := lock.TryLock(ctx, "test:key-a")
	require.NoError(t, err)
	require.True(t, acquired1)
	require.NotNil(t, handle1)
	defer func() { _ = handle1.Unlock(ctx) }()

	// Different key should not contend
	handle2, acquired2, err := lock.TryLock(ctx, "test:key-b")
	require.NoError(t, err)
	assert.True(t, acquired2)
	assert.NotNil(t, handle2)
	defer func() { _ = handle2.Unlock(ctx) }()
}

func TestRedisLockManager_Unlock_NilMutex(t *testing.T) {
	_, lock := setupTestLock(t)

	err := lock.Unlock(context.Background(), nil)
	require.Error(t, err)
	assert.ErrorContains(t, err, "lock handle is nil")
}

func TestRedisLockManager_ConcurrentTryLock(t *testing.T) {
	_, lock := setupTestLock(t)

	ctx := context.Background()

	const workers = 20
	var acquired int32

	var wg sync.WaitGroup

	wg.Add(workers)

	for range workers {
		go func() {
			defer wg.Done()

			handle, ok, err := lock.TryLock(ctx, "test:concurrent-try")
			if err != nil {
				return
			}

			if ok {
				atomic.AddInt32(&acquired, 1)
				// Hold lock briefly
				time.Sleep(10 * time.Millisecond)
				_ = handle.Unlock(ctx)
			}
		}()
	}

	wg.Wait()

	// At least one goroutine must have acquired the lock
	assert.GreaterOrEqual(t, atomic.LoadInt32(&acquired), int32(1))
}

func TestRedisLockManager_ConcurrentDifferentKeys(t *testing.T) {
	_, lock := setupTestLock(t)

	ctx := context.Background()

	const workers = 5
	var wg sync.WaitGroup

	wg.Add(workers)

	errCh := make(chan error, workers)

	for i := range workers {
		go func(idx int) {
			defer wg.Done()

			key := "test:concurrent-diff:" + strings.Repeat("x", idx+1)

			err := lock.WithLock(ctx, key, func(context.Context) error {
				time.Sleep(5 * time.Millisecond)
				return nil
			})
			if err != nil {
				errCh <- err
			}
		}(i)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		require.NoError(t, err)
	}
}

func TestRedisLockManager_WithLockOptions_NegativeDriftFactor(t *testing.T) {
	_, lock := setupTestLock(t)

	err := lock.WithLockOptions(context.Background(), "test:key", LockOptions{
		Expiry:      time.Second,
		Tries:       1,
		RetryDelay:  time.Millisecond,
		DriftFactor: -0.5,
	}, func(context.Context) error { return nil })

	require.Error(t, err)
	assert.ErrorContains(t, err, "lock drift factor")
}

func TestRedisLockManager_WithLockOptions_NegativeExpiry(t *testing.T) {
	_, lock := setupTestLock(t)

	err := lock.WithLockOptions(context.Background(), "test:key", LockOptions{
		Expiry:      -time.Second,
		Tries:       1,
		RetryDelay:  time.Millisecond,
		DriftFactor: 0.01,
	}, func(context.Context) error { return nil })

	require.Error(t, err)
	assert.ErrorContains(t, err, "lock expiry must be greater than 0")
}

func TestRedisLockManager_WithLockOptions_ZeroRetryDelay(t *testing.T) {
	_, lock := setupTestLock(t)

	// Zero retry delay is valid (no delay between retries)
	executed := false

	err := lock.WithLockOptions(context.Background(), "test:zero-delay", LockOptions{
		Expiry:      time.Second,
		Tries:       1,
		RetryDelay:  0,
		DriftFactor: 0.01,
	}, func(context.Context) error {
		executed = true
		return nil
	})

	require.NoError(t, err)
	assert.True(t, executed)
}

func TestRedisLockManager_WithLockOptions_DriftFactorBoundary(t *testing.T) {
	_, lock := setupTestLock(t)

	// DriftFactor = 0 is valid (lower bound inclusive)
	executed := false

	err := lock.WithLockOptions(context.Background(), "test:drift-zero", LockOptions{
		Expiry:      time.Second,
		Tries:       1,
		RetryDelay:  time.Millisecond,
		DriftFactor: 0,
	}, func(context.Context) error {
		executed = true
		return nil
	})

	require.NoError(t, err)
	assert.True(t, executed)

	// DriftFactor = 0.99 is valid (just under 1)
	executed = false

	err = lock.WithLockOptions(context.Background(), "test:drift-high", LockOptions{
		Expiry:      time.Second,
		Tries:       1,
		RetryDelay:  time.Millisecond,
		DriftFactor: 0.99,
	}, func(context.Context) error {
		executed = true
		return nil
	})

	require.NoError(t, err)
	assert.True(t, executed)
}

func TestRedisLockManager_WithLock_ContextionExhaustsRetries(t *testing.T) {
	_, lock := setupTestLock(t)

	ctx := context.Background()

	// Acquire lock and hold it
	handle, acquired, err := lock.TryLock(ctx, "test:exhaust")
	require.NoError(t, err)
	require.True(t, acquired)
	defer func() { _ = handle.Unlock(ctx) }()

	// Try to acquire the same key with limited retries - should fail
	err = lock.WithLockOptions(ctx, "test:exhaust", LockOptions{
		Expiry:      time.Second,
		Tries:       1,
		RetryDelay:  time.Millisecond,
		DriftFactor: 0.01,
	}, func(context.Context) error {
		t.Fatal("function should not be executed when lock cannot be acquired")
		return nil
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to acquire lock")
}

func TestRedisLockManager_ContextCancellation(t *testing.T) {
	_, lock := setupTestLock(t)

	// Acquire lock to create contention
	bgCtx := context.Background()
	handle, acquired, err := lock.TryLock(bgCtx, "test:cancel")
	require.NoError(t, err)
	require.True(t, acquired)
	defer func() { _ = handle.Unlock(bgCtx) }()

	// Create a context that will be cancelled quickly
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// Try to acquire the same lock with the cancellable context
	err = lock.WithLockOptions(ctx, "test:cancel", LockOptions{
		Expiry:      5 * time.Second,
		Tries:       100,
		RetryDelay:  50 * time.Millisecond,
		DriftFactor: 0.01,
	}, func(context.Context) error {
		t.Fatal("function should not execute when context is cancelled")
		return nil
	})

	require.Error(t, err)
}

func TestRedisLockManager_WithLock_FnReceivesSpanContext(t *testing.T) {
	// Verify the function receives a context (potentially enriched with span)
	_, lock := setupTestLock(t)

	err := lock.WithLock(context.Background(), "test:span-ctx", func(ctx context.Context) error {
		// The context should be non-nil and usable
		require.NotNil(t, ctx)

		return nil
	})

	require.NoError(t, err)
}

func TestSafeLockKeyForLogs_ShortKey(t *testing.T) {
	safe := safeLockKeyForLogs("lock:simple")
	// Short keys should be returned as-is (quoted)
	assert.NotContains(t, safe, "...(truncated)")
	assert.Contains(t, safe, "lock:simple")
}

func TestSafeLockKeyForLogs_ExactBoundary(t *testing.T) {
	// Key that produces a quoted string of exactly 128 characters
	// QuoteToASCII adds 2 quote characters, so we need 126 inner chars
	key := strings.Repeat("b", 126)
	safe := safeLockKeyForLogs(key)
	assert.NotContains(t, safe, "...(truncated)")
}

func TestSafeLockKeyForLogs_SpecialCharacters(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		contains string
	}{
		{
			name:     "tab character",
			input:    "lock:key\twith\ttabs",
			contains: "\\t",
		},
		{
			name:     "null byte",
			input:    "lock:key\x00null",
			contains: "\\x00",
		},
		{
			name:     "unicode",
			input:    "lock:key:emoji:😀",
			contains: "lock:key:emoji:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			safe := safeLockKeyForLogs(tt.input)
			assert.Contains(t, safe, tt.contains)
		})
	}
}

func TestSafeLockKeyForLogs_EmptyKey(t *testing.T) {
	safe := safeLockKeyForLogs("")
	// QuoteToASCII on empty string returns `""`
	assert.Equal(t, `""`, safe)
}

func TestValidateLockOptions_AllInvalid(t *testing.T) {
	tests := []struct {
		name    string
		opts    LockOptions
		errText string
	}{
		{
			name: "zero expiry",
			opts: LockOptions{
				Expiry:      0,
				Tries:       1,
				RetryDelay:  time.Millisecond,
				DriftFactor: 0.01,
			},
			errText: "lock expiry must be greater than 0",
		},
		{
			name: "negative expiry",
			opts: LockOptions{
				Expiry:      -5 * time.Second,
				Tries:       1,
				RetryDelay:  time.Millisecond,
				DriftFactor: 0.01,
			},
			errText: "lock expiry must be greater than 0",
		},
		{
			name: "zero tries",
			opts: LockOptions{
				Expiry:      time.Second,
				Tries:       0,
				RetryDelay:  time.Millisecond,
				DriftFactor: 0.01,
			},
			errText: "lock tries must be at least 1",
		},
		{
			name: "negative tries",
			opts: LockOptions{
				Expiry:      time.Second,
				Tries:       -1,
				RetryDelay:  time.Millisecond,
				DriftFactor: 0.01,
			},
			errText: "lock tries must be at least 1",
		},
		{
			name: "negative retry delay",
			opts: LockOptions{
				Expiry:      time.Second,
				Tries:       1,
				RetryDelay:  -time.Millisecond,
				DriftFactor: 0.01,
			},
			errText: "lock retry delay cannot be negative",
		},
		{
			name: "drift factor equals 1",
			opts: LockOptions{
				Expiry:      time.Second,
				Tries:       1,
				RetryDelay:  time.Millisecond,
				DriftFactor: 1.0,
			},
			errText: "lock drift factor must be between 0",
		},
		{
			name: "drift factor greater than 1",
			opts: LockOptions{
				Expiry:      time.Second,
				Tries:       1,
				RetryDelay:  time.Millisecond,
				DriftFactor: 1.5,
			},
			errText: "lock drift factor must be between 0",
		},
		{
			name: "negative drift factor",
			opts: LockOptions{
				Expiry:      time.Second,
				Tries:       1,
				RetryDelay:  time.Millisecond,
				DriftFactor: -0.1,
			},
			errText: "lock drift factor must be between 0",
		},
		{
			name: "tries exceeds max cap",
			opts: LockOptions{
				Expiry:      time.Second,
				Tries:       1001,
				RetryDelay:  time.Millisecond,
				DriftFactor: 0.01,
			},
			errText: "lock tries exceeds maximum",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateLockOptions(tt.opts)
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.errText)
		})
	}
}

func TestValidateLockOptions_Valid(t *testing.T) {
	tests := []struct {
		name string
		opts LockOptions
	}{
		{
			name: "default options",
			opts: DefaultLockOptions(),
		},
		{
			name: "rate limiter options",
			opts: RateLimiterLockOptions(),
		},
		{
			name: "minimal valid",
			opts: LockOptions{
				Expiry:      time.Millisecond,
				Tries:       1,
				RetryDelay:  0,
				DriftFactor: 0,
			},
		},
		{
			name: "large values",
			opts: LockOptions{
				Expiry:      time.Hour,
				Tries:       1000,
				RetryDelay:  time.Minute,
				DriftFactor: 0.99,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateLockOptions(tt.opts)
			require.NoError(t, err)
		})
	}
}

func TestRedisLockManager_WithLock_MultipleSequentialLocks(t *testing.T) {
	_, lock := setupTestLock(t)

	ctx := context.Background()
	var order []int

	for i := range 5 {
		idx := i

		err := lock.WithLock(ctx, "test:sequential", func(context.Context) error {
			order = append(order, idx)
			return nil
		})
		require.NoError(t, err)
	}

	assert.Len(t, order, 5)
	assert.Equal(t, []int{0, 1, 2, 3, 4}, order)
}

func TestRedisLockManager_WithLock_LongLockKey(t *testing.T) {
	_, lock := setupTestLock(t)

	// Redis supports keys up to 512MB; test with a reasonably long key
	longKey := "test:" + strings.Repeat("x", 500)
	executed := false

	err := lock.WithLock(context.Background(), longKey, func(context.Context) error {
		executed = true
		return nil
	})

	require.NoError(t, err)
	assert.True(t, executed)
}

func TestRedisLockManager_InterfaceCompliance(t *testing.T) {
	// Verify compile-time interface compliance
	var _ LockManager = (*RedisLockManager)(nil)
}

// --- New tests for LockHandle API ---

func TestRedisLockManager_Unlock_ExpiredMutex(t *testing.T) {
	// Create miniredis directly so we can control time via FastForward.
	mr := miniredis.RunT(t)

	client, err := New(context.Background(), Config{
		Topology: Topology{
			Standalone: &StandaloneTopology{Address: mr.Addr()},
		},
		Logger: &log.NopLogger{},
	})
	require.NoError(t, err)

	t.Cleanup(func() {
		require.NoError(t, client.Close())
		mr.Close()
	})

	lock, err := NewRedisLockManager(client)
	require.NoError(t, err)

	ctx := context.Background()

	// Acquire a lock with a very short expiry via WithLockOptions + TryLock pattern.
	// We use the low-level redsync through TryLock (which uses DefaultLockOptions expiry = 10s).
	// Instead, acquire lock with WithLockOptions so we control expiry:
	// Actually, TryLock uses DefaultLockOptions internally. We need the handle.
	// We'll acquire and then fast-forward miniredis to expire the key.
	handle, acquired, err := lock.TryLock(ctx, "test:expire")
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotNil(t, handle)

	// Fast-forward time in miniredis to expire the lock key.
	// DefaultLockOptions has 10s expiry; fast-forward past it.
	mr.FastForward(15 * time.Second)

	// Attempting to unlock an expired lock should return an error.
	// redsync returns "failed to unlock, lock was already expired" when the key has expired.
	err = handle.Unlock(ctx)
	require.Error(t, err)
	assert.ErrorContains(t, err, "already expired")
}

func TestRedisLockManager_Unlock_CancelledContext(t *testing.T) {
	_, lock := setupTestLock(t)

	ctx := context.Background()

	handle, acquired, err := lock.TryLock(ctx, "test:cancel-unlock")
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotNil(t, handle)

	// Cancel the context before unlocking.
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()

	// Unlock with cancelled context should fail with a context-related error.
	err = handle.Unlock(cancelCtx)
	require.Error(t, err)
	assert.ErrorContains(t, err, context.Canceled.Error())

	// The lock should still be releasable with a valid context.
	require.NoError(t, handle.Unlock(context.Background()))
}

func TestRedisLockManager_LockHandle_NilHandle(t *testing.T) {
	_, lock := setupTestLock(t)

	ctx := context.Background()

	// Test that calling Unlock with a nil LockHandle returns an error.
	err := lock.Unlock(ctx, nil)
	require.Error(t, err)
	assert.ErrorContains(t, err, "lock handle is nil")
}

func TestRedisLockManager_ValidateLockOptions_TriesCap(t *testing.T) {
	tests := []struct {
		name    string
		tries   int
		wantErr bool
		errText string
	}{
		{
			name:    "at max cap (1000) is valid",
			tries:   1000,
			wantErr: false,
		},
		{
			name:    "exceeds max cap (1001) is rejected",
			tries:   1001,
			wantErr: true,
			errText: "lock tries exceeds maximum",
		},
		{
			name:    "way above max cap",
			tries:   10000,
			wantErr: true,
			errText: "lock tries exceeds maximum",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateLockOptions(LockOptions{
				Expiry:      time.Second,
				Tries:       tt.tries,
				RetryDelay:  time.Millisecond,
				DriftFactor: 0.01,
			})

			if tt.wantErr {
				require.Error(t, err)
				assert.ErrorContains(t, err, tt.errText)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestRedisLockManager_NewRedisLockManager_ErrNilClient(t *testing.T) {
	lock, err := NewRedisLockManager(nil)
	require.Error(t, err)
	assert.Nil(t, lock)

	// Verify the sentinel error supports errors.Is.
	assert.ErrorIs(t, err, ErrNilClient)
	assert.Equal(t, "redis client is nil", err.Error())
}

func TestRedisLockManager_LockHandle_Interface(t *testing.T) {
	_, lock := setupTestLock(t)

	ctx := context.Background()

	handle, acquired, err := lock.TryLock(ctx, "test:interface-check")
	require.NoError(t, err)
	require.True(t, acquired)
	require.NotNil(t, handle)

	// Verify that the returned handle satisfies the LockHandle interface.
	var _ LockHandle = handle

	// Clean up.
	require.NoError(t, handle.Unlock(ctx))
}
