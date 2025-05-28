package retry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRetry(t *testing.T) {
	t.Run("successful operation on first try", func(t *testing.T) {
		attempts := 0
		err := Do(context.Background(), func() error {
			attempts++
			return nil
		})
		
		assert.NoError(t, err)
		assert.Equal(t, 1, attempts)
	})

	t.Run("successful operation after retries", func(t *testing.T) {
		attempts := 0
		err := Do(context.Background(), func() error {
			attempts++
			if attempts < 3 {
				return errors.New("temporary error")
			}
			return nil
		}, WithMaxRetries(5))
		
		assert.NoError(t, err)
		assert.Equal(t, 3, attempts)
	})

	t.Run("fails after max retries", func(t *testing.T) {
		attempts := 0
		expectedErr := errors.New("persistent error")
		
		err := Do(context.Background(), func() error {
			attempts++
			return expectedErr
		}, WithMaxRetries(3))
		
		assert.Error(t, err)
		assert.Equal(t, 4, attempts) // initial + 3 retries
		assert.Contains(t, err.Error(), "persistent error")
	})

	t.Run("respects context cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		attempts := 0
		
		go func() {
			time.Sleep(50 * time.Millisecond)
			cancel()
		}()
		
		err := Do(ctx, func() error {
			attempts++
			return errors.New("error")
		}, WithDelay(100*time.Millisecond))
		
		assert.Error(t, err)
		assert.True(t, errors.Is(err, context.Canceled))
		assert.LessOrEqual(t, attempts, 2)
	})

	t.Run("exponential backoff", func(t *testing.T) {
		start := time.Now()
		attempts := 0
		
		err := Do(context.Background(), func() error {
			attempts++
			if attempts < 3 {
				return errors.New("retry")
			}
			return nil
		}, WithExponentialBackoff(10*time.Millisecond, 2))
		
		duration := time.Since(start)
		assert.NoError(t, err)
		assert.Equal(t, 3, attempts)
		// First retry after 10ms, second after 20ms = 30ms total minimum
		assert.GreaterOrEqual(t, duration, 30*time.Millisecond)
	})

	t.Run("max delay cap", func(t *testing.T) {
		start := time.Now()
		attempts := 0
		
		err := Do(context.Background(), func() error {
			attempts++
			if attempts < 3 {
				return errors.New("retry")
			}
			return nil
		}, 
			WithExponentialBackoff(100*time.Millisecond, 10),
			WithMaxDelay(150*time.Millisecond),
		)
		
		duration := time.Since(start)
		assert.NoError(t, err)
		// First retry after 100ms, second capped at 150ms = 250ms total
		assert.Less(t, duration, 300*time.Millisecond)
	})

	t.Run("retry only on retryable errors", func(t *testing.T) {
		attempts := 0
		permanentErr := errors.New("permanent error")
		
		err := Do(context.Background(), func() error {
			attempts++
			return permanentErr
		}, WithRetryIf(func(err error) bool {
			return err.Error() == "temporary error"
		}))
		
		assert.Error(t, err)
		assert.Equal(t, 1, attempts) // No retries for permanent error
	})

	t.Run("custom retry condition", func(t *testing.T) {
		attempts := 0
		tempErr := &tempError{msg: "temporary"}
		
		err := Do(context.Background(), func() error {
			attempts++
			if attempts < 3 {
				return tempErr
			}
			return nil
		}, WithRetryIf(func(err error) bool {
			var te *tempError
			return errors.As(err, &te) && te.Temporary()
		}))
		
		assert.NoError(t, err)
		assert.Equal(t, 3, attempts)
	})

	t.Run("on retry callback", func(t *testing.T) {
		var retriedErrors []error
		attempts := 0
		
		err := Do(context.Background(), func() error {
			attempts++
			if attempts < 3 {
				return errors.New("retry me")
			}
			return nil
		}, WithOnRetry(func(n int, err error) {
			retriedErrors = append(retriedErrors, err)
		}))
		
		assert.NoError(t, err)
		assert.Len(t, retriedErrors, 2)
		assert.Equal(t, "retry me", retriedErrors[0].Error())
	})

	t.Run("jitter in delays", func(t *testing.T) {
		delays := make([]time.Duration, 5)
		
		for i := 0; i < 5; i++ {
			start := time.Now()
			attempts := 0
			
			_ = Do(context.Background(), func() error {
				attempts++
				if attempts < 2 {
					return errors.New("retry")
				}
				return nil
			}, WithDelay(100*time.Millisecond), WithJitter(0.1))
			
			delays[i] = time.Since(start)
		}
		
		// Check that not all delays are exactly the same (jitter working)
		allSame := true
		for i := 1; i < len(delays); i++ {
			if delays[i] != delays[0] {
				allSame = false
				break
			}
		}
		assert.False(t, allSame, "Jitter should cause delays to vary")
	})
}

type tempError struct {
	msg string
}

func (e *tempError) Error() string {
	return e.msg
}

func (e *tempError) Temporary() bool {
	return true
}

func TestRetryWithResult(t *testing.T) {
	t.Run("returns result on success", func(t *testing.T) {
		attempts := 0
		result, err := DoWithResult(context.Background(), func() (string, error) {
			attempts++
			if attempts < 2 {
				return "", errors.New("retry")
			}
			return "success", nil
		})
		
		assert.NoError(t, err)
		assert.Equal(t, "success", result)
		assert.Equal(t, 2, attempts)
	})

	t.Run("returns zero value on failure", func(t *testing.T) {
		result, err := DoWithResult(context.Background(), func() (int, error) {
			return 0, errors.New("fail")
		}, WithMaxRetries(1))
		
		assert.Error(t, err)
		assert.Equal(t, 0, result)
	})
}

func TestRetryOptions(t *testing.T) {
	t.Run("default options", func(t *testing.T) {
		config := newDefaultConfig()
		assert.Equal(t, 3, config.maxRetries)
		assert.Equal(t, 1*time.Second, config.delay)
		assert.Equal(t, 0.0, config.jitter)
		assert.NotNil(t, config.retryIf)
	})

	t.Run("apply multiple options", func(t *testing.T) {
		config := newDefaultConfig()
		opts := []Option{
			WithMaxRetries(5),
			WithDelay(100 * time.Millisecond),
			WithJitter(0.2),
		}
		
		for _, opt := range opts {
			opt(config)
		}
		
		assert.Equal(t, 5, config.maxRetries)
		assert.Equal(t, 100*time.Millisecond, config.delay)
		assert.Equal(t, 0.2, config.jitter)
	})
}

func TestConcurrentRetries(t *testing.T) {
	t.Run("handles concurrent retries", func(t *testing.T) {
		const goroutines = 10
		results := make(chan error, goroutines)
		
		for i := 0; i < goroutines; i++ {
			go func(id int) {
				attempts := 0
				err := Do(context.Background(), func() error {
					attempts++
					if attempts < 2 {
						return errors.New("retry")
					}
					return nil
				}, WithMaxRetries(3))
				results <- err
			}(i)
		}
		
		for i := 0; i < goroutines; i++ {
			err := <-results
			assert.NoError(t, err)
		}
	})
}

func BenchmarkRetry(b *testing.B) {
	b.Run("successful operation", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_ = Do(context.Background(), func() error {
				return nil
			})
		}
	})
	
	b.Run("with retries", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			attempts := 0
			_ = Do(context.Background(), func() error {
				attempts++
				if attempts < 3 {
					return errors.New("retry")
				}
				return nil
			}, WithDelay(1*time.Microsecond))
		}
	})
}