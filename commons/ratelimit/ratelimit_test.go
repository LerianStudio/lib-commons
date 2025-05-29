package ratelimit

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRateLimiter(t *testing.T) {
	t.Run("token bucket allows burst", func(t *testing.T) {
		limiter := NewTokenBucket(10, 10) // 10 tokens, refill 10/sec

		// Should allow 10 immediate requests
		for i := 0; i < 10; i++ {
			assert.True(t, limiter.Allow())
		}

		// 11th should fail
		assert.False(t, limiter.Allow())
	})

	t.Run("token bucket refills over time", func(t *testing.T) {
		limiter := NewTokenBucket(2, 10) // 2 tokens, refill 10/sec

		// Use all tokens
		assert.True(t, limiter.Allow())
		assert.True(t, limiter.Allow())
		assert.False(t, limiter.Allow())

		// Wait for refill
		time.Sleep(150 * time.Millisecond)

		// Should have ~1 token now
		assert.True(t, limiter.Allow())
	})

	t.Run("sliding window counts requests", func(t *testing.T) {
		limiter := NewSlidingWindow(5, time.Second) // 5 req/sec

		// Should allow 5 requests
		for i := 0; i < 5; i++ {
			assert.True(t, limiter.Allow())
		}

		// 6th should fail
		assert.False(t, limiter.Allow())

		// Wait for window to slide
		time.Sleep(1100 * time.Millisecond)

		// Should allow again
		assert.True(t, limiter.Allow())
	})

	t.Run("fixed window resets after period", func(t *testing.T) {
		limiter := NewFixedWindow(3, 200*time.Millisecond)

		// Use all in first window
		for i := 0; i < 3; i++ {
			assert.True(t, limiter.Allow())
		}
		assert.False(t, limiter.Allow())

		// Wait for new window
		time.Sleep(250 * time.Millisecond)

		// Should reset
		for i := 0; i < 3; i++ {
			assert.True(t, limiter.Allow())
		}
	})

	t.Run("wait blocks until allowed", func(t *testing.T) {
		limiter := NewTokenBucket(1, 5) // 1 token, refill 5/sec

		// Use the token
		assert.True(t, limiter.Allow())

		// Wait should block ~200ms for next token
		start := time.Now()
		ctx := context.Background()
		err := limiter.Wait(ctx)
		duration := time.Since(start)

		assert.NoError(t, err)
		assert.GreaterOrEqual(t, duration, 180*time.Millisecond)
		assert.Less(t, duration, 250*time.Millisecond)
	})

	t.Run("wait respects context cancellation", func(t *testing.T) {
		limiter := NewTokenBucket(1, 1) // Very slow refill

		// Use the token
		assert.True(t, limiter.Allow())

		// Cancel context quickly
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		start := time.Now()
		err := limiter.Wait(ctx)
		duration := time.Since(start)

		assert.Error(t, err)
		assert.Equal(t, context.DeadlineExceeded, err)
		assert.Less(t, duration, 100*time.Millisecond)
	})

	t.Run("reserve tokens for future use", func(t *testing.T) {
		limiter := NewTokenBucket(5, 10)

		// Reserve 3 tokens
		reservation := limiter.Reserve(3)
		assert.NotNil(t, reservation)
		assert.True(t, reservation.OK())

		// Should have 2 left
		assert.True(t, limiter.Allow())
		assert.True(t, limiter.Allow())
		assert.False(t, limiter.Allow())

		// Cancel reservation returns tokens
		reservation.Cancel()

		// Should have 3 tokens back
		for i := 0; i < 3; i++ {
			assert.True(t, limiter.Allow())
		}
	})

	t.Run("reserve respects capacity", func(t *testing.T) {
		limiter := NewTokenBucket(5, 10)

		// Try to reserve more than capacity
		reservation := limiter.Reserve(10)
		assert.NotNil(t, reservation)
		assert.False(t, reservation.OK())
	})

	t.Run("concurrent access", func(t *testing.T) {
		limiter := NewTokenBucket(100, 1000)

		var allowed atomic.Int32
		var denied atomic.Int32
		var wg sync.WaitGroup

		// 200 concurrent requests
		for i := 0; i < 200; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if limiter.Allow() {
					allowed.Add(1)
				} else {
					denied.Add(1)
				}
			}()
		}

		wg.Wait()

		// Should have exactly 100 allowed
		assert.Equal(t, int32(100), allowed.Load())
		assert.Equal(t, int32(100), denied.Load())
	})

	t.Run("per-key rate limiting", func(t *testing.T) {
		limiter := NewKeyedLimiter(func(key string) Limiter {
			return NewTokenBucket(2, 10)
		})

		// Different keys have separate limits
		assert.True(t, limiter.Allow("user1"))
		assert.True(t, limiter.Allow("user1"))
		assert.False(t, limiter.Allow("user1"))

		assert.True(t, limiter.Allow("user2"))
		assert.True(t, limiter.Allow("user2"))
		assert.False(t, limiter.Allow("user2"))
	})

	t.Run("cleanup old keys", func(t *testing.T) {
		limiter := NewKeyedLimiter(
			func(key string) Limiter {
				return NewTokenBucket(1, 1)
			},
			WithCleanupInterval(100*time.Millisecond),
			WithMaxIdleTime(200*time.Millisecond),
		)
		defer limiter.Stop()

		// Use a key
		assert.True(t, limiter.Allow("temp-key"))

		// Wait for cleanup
		time.Sleep(350 * time.Millisecond)

		// Key should be cleaned up, so we get a fresh limiter
		assert.True(t, limiter.Allow("temp-key"))
	})

	t.Run("middleware integration", func(t *testing.T) {
		// This would test HTTP/Fiber middleware
		// Keeping it simple for this example
		limiter := NewTokenBucket(5, 10)

		// Simulate middleware behavior
		for i := 0; i < 5; i++ {
			if !limiter.Allow() {
				t.Fatal("Request should be allowed")
			}
		}

		// 6th request should be rate limited
		if limiter.Allow() {
			t.Fatal("Request should be denied")
		}
	})
}

func TestRateLimiterMetrics(t *testing.T) {
	t.Run("tracks allowed and denied", func(t *testing.T) {
		limiter := NewTokenBucket(3, 10)

		// Make some requests
		for i := 0; i < 5; i++ {
			_ = limiter.Allow()
		}

		metrics := limiter.Metrics()
		assert.Equal(t, int64(3), metrics.Allowed)
		assert.Equal(t, int64(2), metrics.Denied)
		assert.Equal(t, int64(5), metrics.Total)
	})
}

func BenchmarkRateLimiter(b *testing.B) {
	b.Run("TokenBucket", func(b *testing.B) {
		limiter := NewTokenBucket(float64(b.N), float64(b.N))
		b.ResetTimer()

		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_ = limiter.Allow()
			}
		})
	})

	b.Run("SlidingWindow", func(b *testing.B) {
		limiter := NewSlidingWindow(b.N, time.Second)
		b.ResetTimer()

		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_ = limiter.Allow()
			}
		})
	})

	b.Run("KeyedLimiter", func(b *testing.B) {
		limiter := NewKeyedLimiter(func(key string) Limiter {
			return NewTokenBucket(1000, 10000)
		})
		keys := []string{"user1", "user2", "user3", "user4", "user5"}
		b.ResetTimer()

		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				_ = limiter.Allow(keys[i%len(keys)])
				i++
			}
		})
	})
}
