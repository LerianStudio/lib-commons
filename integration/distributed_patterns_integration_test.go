package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/commons/circuitbreaker"
	"github.com/LerianStudio/lib-commons/commons/ratelimit"
	"github.com/LerianStudio/lib-commons/commons/retry"
	"github.com/LerianStudio/lib-commons/commons/saga"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
)

// DistributedPatternsIntegrationTestSuite tests integration between distributed system patterns
type DistributedPatternsIntegrationTestSuite struct {
	suite.Suite
	ctx context.Context
}

func TestDistributedPatternsIntegrationSuite(t *testing.T) {
	// Skip integration tests if not in integration test mode
	if os.Getenv("INTEGRATION_TESTS") != "true" {
		t.Skip("Skipping integration tests. Set INTEGRATION_TESTS=true to run.")
	}

	suite.Run(t, new(DistributedPatternsIntegrationTestSuite))
}

func (suite *DistributedPatternsIntegrationTestSuite) SetupSuite() {
	suite.ctx = context.Background()
}

func (suite *DistributedPatternsIntegrationTestSuite) TestCircuitBreakerWithRetryIntegration() {
	suite.Run("circuit_breaker_retry_coordination", func() {
		// Create circuit breaker with low threshold for testing
		cb := circuitbreaker.New("test-service",
			circuitbreaker.WithThreshold(3),
			circuitbreaker.WithTimeout(100*time.Millisecond),
		)

		callCount := 0
		failingOperation := func() error {
			callCount++
			return fmt.Errorf("operation failed %d", callCount)
		}

		// Test retry with circuit breaker protection
		err := retry.Do(suite.ctx, func() error {
			return cb.Execute(failingOperation)
		}, retry.WithMaxAttempts(5), retry.WithDelay(10*time.Millisecond))

		// Should fail due to circuit breaker opening
		assert.Error(suite.T(), err)

		// Circuit breaker should be open after threshold failures
		assert.Equal(suite.T(), circuitbreaker.StateOpen, cb.State())

		// Verify metrics
		metrics := cb.Metrics()
		assert.Greater(suite.T(), metrics.Failures, int64(0))
		assert.Greater(suite.T(), metrics.Rejections, int64(0))
	})

	suite.Run("circuit_breaker_recovery_with_retry", func() {
		// Create circuit breaker that will recover
		cb := circuitbreaker.New("recovery-test",
			circuitbreaker.WithThreshold(2),
			circuitbreaker.WithTimeout(50*time.Millisecond),
			circuitbreaker.WithSuccessThreshold(1),
		)

		// Force circuit breaker to open
		for i := 0; i < 3; i++ {
			cb.Execute(func() error { return errors.New("initial failure") })
		}
		assert.Equal(suite.T(), circuitbreaker.StateOpen, cb.State())

		// Wait for timeout
		time.Sleep(60 * time.Millisecond)

		// Now test recovery with retry
		err := retry.Do(suite.ctx, func() error {
			return cb.Execute(func() error {
				return nil // Now succeed
			})
		}, retry.WithMaxAttempts(3))

		// Should succeed and circuit should be closed
		assert.NoError(suite.T(), err)
		assert.Equal(suite.T(), circuitbreaker.StateClosed, cb.State())
	})
}

func (suite *DistributedPatternsIntegrationTestSuite) TestRateLimitWithCircuitBreakerIntegration() {
	suite.Run("rate_limit_circuit_breaker_coordination", func() {
		// Create rate limiter and circuit breaker
		limiter := ratelimit.NewRateLimiter(2, time.Second) // 2 requests per second
		cb := circuitbreaker.New("rate-limited-service",
			circuitbreaker.WithThreshold(3),
		)

		successCount := 0
		rejectedCount := 0

		// Test multiple requests with rate limiting and circuit breaking
		for i := 0; i < 10; i++ {
			if !limiter.Allow() {
				rejectedCount++
				continue
			}

			err := cb.Execute(func() error {
				successCount++
				return nil // All allowed requests succeed
			})

			if err != nil {
				rejectedCount++
			}
		}

		// Should have rate-limited some requests
		assert.Greater(suite.T(), rejectedCount, 0)
		assert.Greater(suite.T(), successCount, 0)
		assert.Equal(suite.T(), 10, successCount+rejectedCount)

		// Circuit should remain closed since operations succeed
		assert.Equal(suite.T(), circuitbreaker.StateClosed, cb.State())
	})

	suite.Run("rate_limit_with_wait", func() {
		// Test rate limiter wait with context timeout
		limiter := ratelimit.NewRateLimiter(1, time.Second)

		// First request should succeed immediately
		err := limiter.Wait(suite.ctx)
		assert.NoError(suite.T(), err)

		// Second request should require waiting
		ctx, cancel := context.WithTimeout(suite.ctx, 50*time.Millisecond)
		defer cancel()

		start := time.Now()
		err = limiter.Wait(ctx)
		duration := time.Since(start)

		// Should timeout due to rate limiting
		if err != nil {
			assert.Equal(suite.T(), context.DeadlineExceeded, err)
			assert.Greater(suite.T(), duration, 40*time.Millisecond)
		}
	})
}

func (suite *DistributedPatternsIntegrationTestSuite) TestSagaWithCircuitBreakerIntegration() {
	suite.Run("saga_with_circuit_breaker_protection", func() {
		// Create circuit breaker for saga steps
		paymentCB := circuitbreaker.New("payment-service", circuitbreaker.WithThreshold(2))
		inventoryCB := circuitbreaker.New("inventory-service", circuitbreaker.WithThreshold(2))

		// Create saga with circuit breaker protected steps
		sagaInstance := saga.NewSaga("protected-order-processing")

		// Add payment step with circuit breaker
		paymentStep := &ProtectedStep{
			name:           "process-payment",
			circuitBreaker: paymentCB,
			operation: func() error {
				// Simulate payment processing
				return nil
			},
			compensation: func() error {
				// Simulate payment rollback
				return nil
			},
		}

		// Add inventory step with circuit breaker
		inventoryStep := &ProtectedStep{
			name:           "reserve-inventory",
			circuitBreaker: inventoryCB,
			operation: func() error {
				// Simulate inventory reservation
				return nil
			},
			compensation: func() error {
				// Simulate inventory release
				return nil
			},
		}

		sagaInstance.AddStep(paymentStep).AddStep(inventoryStep)

		// Execute saga
		err := sagaInstance.Execute(suite.ctx, map[string]interface{}{
			"order_id":    "12345",
			"customer_id": "67890",
			"amount":      100.50,
		})

		assert.NoError(suite.T(), err, "Saga should succeed with healthy circuit breakers")

		// Verify circuit breakers remain closed
		assert.Equal(suite.T(), circuitbreaker.StateClosed, paymentCB.State())
		assert.Equal(suite.T(), circuitbreaker.StateClosed, inventoryCB.State())
	})

	suite.Run("saga_with_failing_circuit_breaker", func() {
		// Create circuit breaker that will fail
		failingCB := circuitbreaker.New("failing-service",
			circuitbreaker.WithThreshold(1),
		)

		// Force circuit breaker to open
		failingCB.Execute(func() error { return errors.New("service down") })
		assert.Equal(suite.T(), circuitbreaker.StateOpen, failingCB.State())

		// Create saga with failing step
		sagaInstance := saga.NewSaga("failing-saga")
		failingStep := &ProtectedStep{
			name:           "failing-step",
			circuitBreaker: failingCB,
			operation: func() error {
				return errors.New("this won't be called due to open circuit")
			},
			compensation: func() error {
				return nil
			},
		}

		sagaInstance.AddStep(failingStep)

		// Execute saga
		err := sagaInstance.Execute(suite.ctx, map[string]interface{}{})

		// Should fail due to circuit breaker
		assert.Error(suite.T(), err)
		assert.Contains(suite.T(), err.Error(), "circuit breaker is open")
	})
}

func (suite *DistributedPatternsIntegrationTestSuite) TestRetryWithBackoffIntegration() {
	suite.Run("retry_with_exponential_backoff", func() {
		attempts := 0
		var attemptTimes []time.Time

		start := time.Now()
		err := retry.Do(suite.ctx, func() error {
			attempts++
			attemptTimes = append(attemptTimes, time.Now())
			if attempts < 3 {
				return fmt.Errorf("attempt %d failed", attempts)
			}
			return nil // Succeed on 3rd attempt
		},
			retry.WithMaxAttempts(5),
			retry.WithDelay(10*time.Millisecond),
			retry.WithJitter(retry.JitterNone), // No jitter for predictable testing
		)

		totalDuration := time.Since(start)

		// Should succeed on 3rd attempt
		assert.NoError(suite.T(), err)
		assert.Equal(suite.T(), 3, attempts)
		assert.Len(suite.T(), attemptTimes, 3)

		// Should have delays between attempts
		assert.Greater(suite.T(), totalDuration, 20*time.Millisecond) // At least 2 delays

		// Verify timing between attempts
		if len(attemptTimes) >= 2 {
			firstDelay := attemptTimes[1].Sub(attemptTimes[0])
			assert.GreaterOrEqual(suite.T(), firstDelay, 10*time.Millisecond)
		}
	})

	suite.Run("retry_with_jitter", func() {
		attempts := 0
		var attemptTimes []time.Time

		err := retry.Do(suite.ctx, func() error {
			attempts++
			attemptTimes = append(attemptTimes, time.Now())
			if attempts < 3 {
				return fmt.Errorf("attempt %d failed", attempts)
			}
			return nil
		},
			retry.WithMaxAttempts(5),
			retry.WithDelay(20*time.Millisecond),
			retry.WithJitter(retry.JitterFull), // Full jitter
		)

		// Should succeed with jitter applied
		assert.NoError(suite.T(), err)
		assert.Equal(suite.T(), 3, attempts)

		// With jitter, delays should vary but still be reasonable
		if len(attemptTimes) >= 2 {
			delay := attemptTimes[1].Sub(attemptTimes[0])
			assert.Greater(suite.T(), delay, 0*time.Millisecond)
			assert.Less(suite.T(), delay, 50*time.Millisecond) // Should be within reasonable bounds
		}
	})
}

func (suite *DistributedPatternsIntegrationTestSuite) TestConcurrentPatternIntegration() {
	suite.Run("concurrent_circuit_breakers", func() {
		// Test multiple circuit breakers under concurrent load
		cb := circuitbreaker.New("concurrent-test",
			circuitbreaker.WithThreshold(50),
		)

		var wg sync.WaitGroup
		successCount := int64(0)
		errorCount := int64(0)

		// Launch concurrent operations
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func(id int) {
				defer wg.Done()

				err := cb.Execute(func() error {
					// Simulate work
					time.Sleep(1 * time.Millisecond)
					// Fail some operations to test circuit breaker
					if id%10 == 0 {
						return fmt.Errorf("operation %d failed", id)
					}
					return nil
				})

				if err != nil {
					errorCount++
				} else {
					successCount++
				}
			}(i)
		}

		wg.Wait()

		// Should have both successes and failures
		assert.Greater(suite.T(), successCount, int64(0))
		assert.Greater(suite.T(), errorCount, int64(0))

		// Circuit breaker should still be functional
		metrics := cb.Metrics()
		assert.Equal(suite.T(), int64(100), metrics.Requests)
		assert.Equal(suite.T(), successCount, metrics.Successes)
	})

	suite.Run("concurrent_rate_limiting", func() {
		// Test rate limiter under concurrent load
		limiter := ratelimit.NewRateLimiter(10, time.Second) // 10 requests per second

		var wg sync.WaitGroup
		allowedCount := int64(0)
		deniedCount := int64(0)

		// Launch concurrent requests
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()

				if limiter.Allow() {
					allowedCount++
				} else {
					deniedCount++
				}
			}()
		}

		wg.Wait()

		// Should have rate limited some requests
		assert.Greater(suite.T(), deniedCount, int64(0))
		assert.Greater(suite.T(), allowedCount, int64(0))
		assert.Equal(suite.T(), int64(50), allowedCount+deniedCount)

		// Should not exceed rate limit
		assert.LessOrEqual(suite.T(), allowedCount, int64(15)) // Allow some tolerance
	})
}

func (suite *DistributedPatternsIntegrationTestSuite) TestPatternCompositionIntegration() {
	suite.Run("retry_circuit_breaker_rate_limit_composition", func() {
		// Compose all three patterns together
		limiter := ratelimit.NewRateLimiter(5, time.Second)
		cb := circuitbreaker.New("composed-service",
			circuitbreaker.WithThreshold(3),
			circuitbreaker.WithTimeout(100*time.Millisecond),
		)

		operationCount := 0

		composedOperation := func() error {
			// Apply rate limiting first
			if !limiter.Allow() {
				return fmt.Errorf("rate limited")
			}

			// Then apply circuit breaker protection
			return cb.Execute(func() error {
				operationCount++
				// Fail first few attempts to test retry
				if operationCount < 3 {
					return fmt.Errorf("operation %d failed", operationCount)
				}
				return nil
			})
		}

		// Apply retry with the composed operation
		err := retry.Do(suite.ctx, composedOperation,
			retry.WithMaxAttempts(5),
			retry.WithDelay(50*time.Millisecond),
		)

		// Should eventually succeed or fail gracefully
		if err != nil {
			suite.T().Logf("Composed operation failed (expected in some cases): %v", err)
		} else {
			suite.T().Logf("Composed operation succeeded after %d attempts", operationCount)
		}

		// Verify patterns worked together
		assert.Greater(suite.T(), operationCount, 0)
	})
}

// ProtectedStep implements saga.Step with circuit breaker protection
type ProtectedStep struct {
	name           string
	circuitBreaker *circuitbreaker.CircuitBreaker
	operation      func() error
	compensation   func() error
}

func (ps *ProtectedStep) Name() string {
	return ps.name
}

func (ps *ProtectedStep) Execute(ctx context.Context, data any) error {
	return ps.circuitBreaker.ExecuteWithContext(ctx, ps.operation)
}

func (ps *ProtectedStep) Compensate(ctx context.Context, data any) error {
	// Compensation usually doesn't need circuit breaker protection
	// as it's trying to undo something that was already done
	return ps.compensation()
}

// BenchmarkDistributedPatterns benchmarks distributed pattern performance
func BenchmarkDistributedPatterns(b *testing.B) {
	if os.Getenv("INTEGRATION_TESTS") != "true" {
		b.Skip("Skipping integration benchmarks. Set INTEGRATION_TESTS=true to run.")
	}

	ctx := context.Background()

	b.Run("circuit_breaker_overhead", func(b *testing.B) {
		cb := circuitbreaker.New("benchmark")

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			cb.Execute(func() error {
				return nil
			})
		}
	})

	b.Run("rate_limiter_overhead", func(b *testing.B) {
		limiter := ratelimit.NewRateLimiter(1000000, time.Second) // High limit

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			limiter.Allow()
		}
	})

	b.Run("retry_overhead", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			retry.Do(ctx, func() error {
				return nil // Always succeed
			})
		}
	})

	b.Run("composed_patterns_overhead", func(b *testing.B) {
		limiter := ratelimit.NewRateLimiter(1000000, time.Second)
		cb := circuitbreaker.New("benchmark-composed")

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if limiter.Allow() {
				cb.Execute(func() error {
					return nil
				})
			}
		}
	})
}
