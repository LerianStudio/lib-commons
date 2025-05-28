package circuitbreaker

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestCircuitBreaker(t *testing.T) {
	t.Run("allows requests when closed", func(t *testing.T) {
		cb := New("test", WithThreshold(5))

		err := cb.Execute(func() error {
			return nil
		})

		assert.NoError(t, err)
		assert.Equal(t, StateClosed, cb.State())
	})

	t.Run("opens after threshold failures", func(t *testing.T) {
		cb := New("test", WithThreshold(3))

		// Cause 3 failures
		for i := 0; i < 3; i++ {
			_ = cb.Execute(func() error {
				return errors.New("fail")
			})
		}

		assert.Equal(t, StateOpen, cb.State())

		// Next request should fail immediately
		err := cb.Execute(func() error {
			return nil
		})

		assert.Error(t, err)
		assert.Equal(t, ErrCircuitOpen, err)
	})

	t.Run("resets on success", func(t *testing.T) {
		cb := New("test", WithThreshold(3))

		// Two failures
		for i := 0; i < 2; i++ {
			_ = cb.Execute(func() error {
				return errors.New("fail")
			})
		}

		// One success resets the counter
		err := cb.Execute(func() error {
			return nil
		})
		assert.NoError(t, err)

		// Two more failures shouldn't open the circuit
		for i := 0; i < 2; i++ {
			_ = cb.Execute(func() error {
				return errors.New("fail")
			})
		}

		assert.Equal(t, StateClosed, cb.State())
	})

	t.Run("half-open state after timeout", func(t *testing.T) {
		cb := New("test",
			WithThreshold(1),
			WithTimeout(50*time.Millisecond),
		)

		// Open the circuit
		_ = cb.Execute(func() error {
			return errors.New("fail")
		})
		assert.Equal(t, StateOpen, cb.State())

		// Wait for timeout
		time.Sleep(60 * time.Millisecond)

		// Should be half-open now
		assert.Equal(t, StateHalfOpen, cb.State())

		// Success should close it
		err := cb.Execute(func() error {
			return nil
		})
		assert.NoError(t, err)
		assert.Equal(t, StateClosed, cb.State())
	})

	t.Run("half-open returns to open on failure", func(t *testing.T) {
		cb := New("test",
			WithThreshold(1),
			WithTimeout(50*time.Millisecond),
		)

		// Open the circuit
		_ = cb.Execute(func() error {
			return errors.New("fail")
		})

		// Wait for timeout
		time.Sleep(60 * time.Millisecond)
		assert.Equal(t, StateHalfOpen, cb.State())

		// Failure in half-open state
		_ = cb.Execute(func() error {
			return errors.New("fail again")
		})

		assert.Equal(t, StateOpen, cb.State())
	})

	t.Run("success threshold in half-open", func(t *testing.T) {
		cb := New("test",
			WithThreshold(1),
			WithTimeout(50*time.Millisecond),
			WithSuccessThreshold(3),
		)

		// Open the circuit
		_ = cb.Execute(func() error {
			return errors.New("fail")
		})

		// Wait for timeout
		time.Sleep(60 * time.Millisecond)

		// Need 3 successes to close
		for i := 0; i < 2; i++ {
			_ = cb.Execute(func() error {
				return nil
			})
			assert.Equal(t, StateHalfOpen, cb.State())
		}

		// Third success closes it
		_ = cb.Execute(func() error {
			return nil
		})
		assert.Equal(t, StateClosed, cb.State())
	})

	t.Run("custom failure condition", func(t *testing.T) {
		timeoutErr := errors.New("timeout")
		cb := New("test",
			WithThreshold(2),
			WithFailureCondition(func(err error) bool {
				return errors.Is(err, timeoutErr)
			}),
		)

		// Non-timeout errors don't count
		for i := 0; i < 5; i++ {
			_ = cb.Execute(func() error {
				return errors.New("other error")
			})
		}
		assert.Equal(t, StateClosed, cb.State())

		// Timeout errors count
		for i := 0; i < 2; i++ {
			_ = cb.Execute(func() error {
				return timeoutErr
			})
		}
		assert.Equal(t, StateOpen, cb.State())
	})

	t.Run("concurrent requests", func(t *testing.T) {
		cb := New("test", WithThreshold(5))

		var wg sync.WaitGroup
		failureCount := int32(0)

		// First cause enough consecutive failures to open
		for i := 0; i < 5; i++ {
			_ = cb.Execute(func() error {
				return errors.New("fail")
			})
		}

		// Now test concurrent access with open circuit
		for i := 0; i < 20; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()
				err := cb.Execute(func() error {
					return nil
				})
				if err == ErrCircuitOpen {
					atomic.AddInt32(&failureCount, 1)
				}
			}(i)
		}

		wg.Wait()
		assert.Equal(t, StateOpen, cb.State())
		assert.Equal(t, int32(20), atomic.LoadInt32(&failureCount)) // All rejected
	})

	t.Run("on state change callback", func(t *testing.T) {
		var mu sync.Mutex
		var changes []StateChange
		cb := New("test",
			WithThreshold(1),
			WithTimeout(50*time.Millisecond),
			WithOnStateChange(func(change StateChange) {
				mu.Lock()
				changes = append(changes, change)
				mu.Unlock()
			}),
		)

		// Cause state changes
		_ = cb.Execute(func() error {
			return errors.New("fail")
		})

		time.Sleep(60 * time.Millisecond)
		_ = cb.Execute(func() error {
			return nil
		})

		// Wait a bit for async callbacks
		time.Sleep(10 * time.Millisecond)

		mu.Lock()
		defer mu.Unlock()
		assert.Len(t, changes, 3) // closed->open, open->half-open, half-open->closed
		assert.Equal(t, StateClosed, changes[0].From)
		assert.Equal(t, StateOpen, changes[0].To)
	})

	t.Run("context support", func(t *testing.T) {
		cb := New("test")

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		err := cb.ExecuteWithContext(ctx, func() error {
			return nil
		})

		assert.Error(t, err)
		assert.Equal(t, context.Canceled, err)
	})

	t.Run("metrics tracking", func(t *testing.T) {
		cb := New("test", WithThreshold(5))

		// Some successes and failures
		for i := 0; i < 10; i++ {
			_ = cb.Execute(func() error {
				if i < 7 {
					return nil
				}
				return errors.New("fail")
			})
		}

		metrics := cb.Metrics()
		assert.Equal(t, int64(10), metrics.Requests)
		assert.Equal(t, int64(7), metrics.Successes)
		assert.Equal(t, int64(3), metrics.Failures)
		assert.Equal(t, int64(0), metrics.Rejections)
		assert.Equal(t, int64(3), metrics.ConsecutiveFailures)
	})

	t.Run("maximum rejections tracking", func(t *testing.T) {
		cb := New("test", WithThreshold(1))

		// Open the circuit
		_ = cb.Execute(func() error {
			return errors.New("fail")
		})

		// Try 5 more times (all rejected)
		for i := 0; i < 5; i++ {
			_ = cb.Execute(func() error {
				return nil
			})
		}

		metrics := cb.Metrics()
		assert.Equal(t, int64(5), metrics.Rejections)
	})
}

func TestCircuitBreakerReset(t *testing.T) {
	t.Run("manual reset", func(t *testing.T) {
		cb := New("test", WithThreshold(1))

		// Open the circuit
		_ = cb.Execute(func() error {
			return errors.New("fail")
		})
		assert.Equal(t, StateOpen, cb.State())

		// Reset
		cb.Reset()
		assert.Equal(t, StateClosed, cb.State())

		// Can execute again
		err := cb.Execute(func() error {
			return nil
		})
		assert.NoError(t, err)
	})
}

func TestCircuitBreakerName(t *testing.T) {
	cb := New("my-service")
	assert.Equal(t, "my-service", cb.Name())
}

func BenchmarkCircuitBreaker(b *testing.B) {
	b.Run("closed state", func(b *testing.B) {
		cb := New("bench")
		b.ResetTimer()

		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_ = cb.Execute(func() error {
					return nil
				})
			}
		})
	})

	b.Run("open state", func(b *testing.B) {
		cb := New("bench", WithThreshold(1))
		_ = cb.Execute(func() error {
			return errors.New("fail")
		})
		b.ResetTimer()

		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_ = cb.Execute(func() error {
					return nil
				})
			}
		})
	})
}
