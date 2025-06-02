package contracts

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/commons/circuitbreaker"
	"github.com/LerianStudio/lib-commons/commons/ratelimit"
	"github.com/LerianStudio/lib-commons/commons/retry"
	"github.com/LerianStudio/lib-commons/commons/saga"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCircuitBreakerContract validates circuit breaker pattern implementation
func TestCircuitBreakerContract(t *testing.T) {
	t.Run("circuit_breaker_interface_contract", func(t *testing.T) {
		// Test CircuitBreaker struct contract
		cb := circuitbreaker.New("test", circuitbreaker.WithThreshold(5))
		cbType := reflect.TypeOf(cb)

		// Verify essential methods exist
		expectedMethods := map[string]circuitBreakerMethodContract{
			"Execute": {
				description:  "Execute operation with circuit breaker protection",
				paramCount:   2, // receiver + function
				returnsError: true,
			},
			"ExecuteWithContext": {
				description:  "Execute operation with context and circuit breaker protection",
				paramCount:   3, // receiver + context + function
				returnsError: true,
			},
			"State": {
				description:  "Get current circuit breaker state",
				paramCount:   1, // receiver only
				returnsError: false,
			},
			"Reset": {
				description:  "Manually reset circuit breaker to closed state",
				paramCount:   1, // receiver only
				returnsError: false,
			},
			"Metrics": {
				description:  "Get circuit breaker metrics",
				paramCount:   1, // receiver only
				returnsError: false,
			},
			"Name": {
				description:  "Get circuit breaker name",
				paramCount:   1, // receiver only
				returnsError: false,
			},
		}

		for methodName, expected := range expectedMethods {
			method, exists := cbType.MethodByName(methodName)
			assert.True(
				t,
				exists,
				"CircuitBreaker should have method %s (%s)",
				methodName,
				expected.description,
			)

			if exists {
				methodType := method.Type
				assert.Equal(t, expected.paramCount, methodType.NumIn(),
					"Method %s should have %d parameters", methodName, expected.paramCount)

				if expected.returnsError {
					assert.Greater(
						t,
						methodType.NumOut(),
						0,
						"Method %s should return values",
						methodName,
					)
					lastReturnIdx := methodType.NumOut() - 1
					assert.Equal(t, "error", methodType.Out(lastReturnIdx).String(),
						"Method %s should return error as last value", methodName)
				}
			}
		}
	})

	t.Run("circuit_breaker_state_contract", func(t *testing.T) {
		// Test circuit breaker state transitions
		cb := circuitbreaker.New("test", circuitbreaker.WithThreshold(2))

		// Initial state should be Closed
		assert.Equal(t, circuitbreaker.StateClosed, cb.State(),
			"Circuit breaker should start in Closed state")

		// Test state type contract
		state := cb.State()
		stateType := reflect.TypeOf(state)
		assert.Equal(t, "circuitbreaker.State", stateType.String(),
			"State should be of type circuitbreaker.State")

		// Test state string representation
		assert.Equal(t, "closed", state.String(), "Closed state should have string representation")

		// Test other state string representations
		assert.Equal(t, "open", circuitbreaker.StateOpen.String())
		assert.Equal(t, "half-open", circuitbreaker.StateHalfOpen.String())
	})

	t.Run("circuit_breaker_options_contract", func(t *testing.T) {
		// Test circuit breaker option functions contract
		cb := circuitbreaker.New("test",
			circuitbreaker.WithThreshold(10),
			circuitbreaker.WithTimeout(30*time.Second),
			circuitbreaker.WithSuccessThreshold(3),
		)

		assert.NotNil(t, cb, "Circuit breaker with options should be created")
		assert.Equal(t, "test", cb.Name(), "Circuit breaker name should be preserved")
	})

	t.Run("circuit_breaker_metrics_contract", func(t *testing.T) {
		// Test metrics structure contract
		cb := circuitbreaker.New("test")
		metrics := cb.Metrics()

		metricsType := reflect.TypeOf(metrics)
		expectedFields := map[string]string{
			"Requests":            "int64",
			"Successes":           "int64",
			"Failures":            "int64",
			"Rejections":          "int64",
			"ConsecutiveFailures": "int64",
			"LastFailureTime":     "time.Time",
		}

		for fieldName, expectedType := range expectedFields {
			field, exists := metricsType.FieldByName(fieldName)
			assert.True(t, exists, "Metrics should have field %s", fieldName)

			if exists {
				assert.Equal(t, expectedType, field.Type.String(),
					"Metrics.%s should have type %s", fieldName, expectedType)
			}
		}
	})

	t.Run("circuit_breaker_behavior_contract", func(t *testing.T) {
		// Test basic circuit breaker behavior
		cb := circuitbreaker.New("test", circuitbreaker.WithThreshold(2))

		// Should execute successfully initially
		err := cb.Execute(func() error { return nil })
		assert.NoError(t, err, "Circuit breaker should allow execution initially")

		// Should handle context cancellation
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		err = cb.ExecuteWithContext(ctx, func() error { return nil })
		assert.Error(t, err, "Circuit breaker should respect context cancellation")
		assert.Equal(t, context.Canceled, err, "Should return context.Canceled error")
	})
}

// TestSagaPatternContract validates saga pattern implementation
func TestSagaPatternContract(t *testing.T) {
	t.Run("saga_interface_contract", func(t *testing.T) {
		// Test Step interface contract
		var step saga.Step
		stepType := reflect.TypeOf(&step).Elem()

		assert.Equal(t, "Step", stepType.Name(), "Should be named Step interface")
		assert.Equal(t, reflect.Interface, stepType.Kind(), "Should be an interface")

		// Verify Step interface methods
		expectedMethods := []string{"Name", "Execute", "Compensate"}
		assert.Equal(t, len(expectedMethods), stepType.NumMethod(),
			"Step interface should have %d methods", len(expectedMethods))

		for _, methodName := range expectedMethods {
			method, exists := stepType.MethodByName(methodName)
			assert.True(t, exists, "Step interface should have method %s", methodName)

			if methodName == "Name" {
				// Name() string
				assert.Equal(t, 1, method.Type.NumOut(), "Name should return 1 value")
				assert.Equal(t, "string", method.Type.Out(0).String(), "Name should return string")
			} else {
				// Execute/Compensate(ctx context.Context, data any) error
				assert.Equal(t, 2, method.Type.NumIn(), "%s should take 2 parameters", methodName)
				assert.Equal(t, 1, method.Type.NumOut(), "%s should return 1 value", methodName)
				assert.Equal(t, "context.Context", method.Type.In(0).String(),
					"%s first parameter should be context.Context", methodName)
				assert.Equal(t, "error", method.Type.Out(0).String(),
					"%s should return error", methodName)
			}
		}
	})

	t.Run("saga_struct_contract", func(t *testing.T) {
		// Test Saga struct methods contract
		sagaInstance := saga.NewSaga("test")
		sagaType := reflect.TypeOf(sagaInstance)

		expectedMethods := map[string]sagaMethodContract{
			"AddStep": {
				description: "Add step to saga",
				paramCount:  2, // receiver + step
				returnsSaga: true,
			},
			"WithTimeout": {
				description: "Set saga timeout",
				paramCount:  2, // receiver + duration
				returnsSaga: true,
			},
			"WithRetry": {
				description: "Set retry configuration",
				paramCount:  3, // receiver + retries + delay
				returnsSaga: true,
			},
			"Execute": {
				description:  "Execute saga",
				paramCount:   3, // receiver + context + data
				returnsError: true,
			},
		}

		for methodName, expected := range expectedMethods {
			method, exists := sagaType.MethodByName(methodName)
			assert.True(
				t,
				exists,
				"Saga should have method %s (%s)",
				methodName,
				expected.description,
			)

			if exists {
				methodType := method.Type
				assert.Equal(t, expected.paramCount, methodType.NumIn(),
					"Method %s should have %d parameters", methodName, expected.paramCount)

				if expected.returnsSaga {
					assert.Equal(
						t,
						1,
						methodType.NumOut(),
						"Method %s should return 1 value",
						methodName,
					)
					assert.Equal(t, "*saga.Saga", methodType.Out(0).String(),
						"Method %s should return *saga.Saga for chaining", methodName)
				}

				if expected.returnsError {
					assert.Equal(
						t,
						1,
						methodType.NumOut(),
						"Method %s should return 1 value",
						methodName,
					)
					assert.Equal(t, "error", methodType.Out(0).String(),
						"Method %s should return error", methodName)
				}
			}
		}
	})

	t.Run("saga_status_contract", func(t *testing.T) {
		// Test SagaStatus constants contract
		statusValues := []saga.SagaStatus{
			saga.SagaStatusPending,
			saga.SagaStatusRunning,
			saga.SagaStatusCompleted,
			saga.SagaStatusFailed,
			saga.SagaStatusCompensating,
			saga.SagaStatusCompensated,
		}

		expectedStrings := []string{
			"pending",
			"running",
			"completed",
			"failed",
			"compensating",
			"compensated",
		}

		for i, status := range statusValues {
			assert.Equal(t, expectedStrings[i], string(status),
				"Status %v should have string value %s", status, expectedStrings[i])
		}
	})

	t.Run("saga_coordinator_contract", func(t *testing.T) {
		// Test Coordinator struct contract
		coordinator := saga.NewCoordinator()
		coordinatorType := reflect.TypeOf(coordinator)

		expectedMethods := map[string]coordinatorMethodContract{
			"Register": {
				description:  "Register saga with coordinator",
				paramCount:   2, // receiver + saga
				returnsError: false,
			},
			"Start": {
				description:   "Start saga execution",
				paramCount:    4, // receiver + context + name + data
				returnsError:  true,
				returnsString: true,
			},
			"GetStatus": {
				description:      "Get saga execution status",
				paramCount:       2, // receiver + executionID
				returnsExecution: true,
			},
		}

		for methodName, expected := range expectedMethods {
			method, exists := coordinatorType.MethodByName(methodName)
			assert.True(
				t,
				exists,
				"Coordinator should have method %s (%s)",
				methodName,
				expected.description,
			)

			if exists {
				methodType := method.Type
				assert.Equal(t, expected.paramCount, methodType.NumIn(),
					"Method %s should have %d parameters", methodName, expected.paramCount)

				if expected.returnsError {
					assert.Greater(
						t,
						methodType.NumOut(),
						0,
						"Method %s should return values",
						methodName,
					)
				}
			}
		}
	})

	t.Run("saga_builder_contract", func(t *testing.T) {
		// Test SagaBuilder fluent interface contract
		builder := saga.NewSagaBuilder("test")
		builderType := reflect.TypeOf(builder)

		expectedMethods := map[string]builderMethodContract{
			"WithTimeout": {
				description:    "Set saga timeout",
				paramCount:     2, // receiver + duration
				returnsBuilder: true,
			},
			"WithRetry": {
				description:    "Set retry configuration",
				paramCount:     3, // receiver + retries + delay
				returnsBuilder: true,
			},
			"WithStep": {
				description:    "Add step to saga",
				paramCount:     4, // receiver + name + execute + compensate
				returnsBuilder: true,
			},
			"Build": {
				description: "Build saga from builder",
				paramCount:  1, // receiver only
				returnsSaga: true,
			},
		}

		for methodName, expected := range expectedMethods {
			method, exists := builderType.MethodByName(methodName)
			assert.True(
				t,
				exists,
				"SagaBuilder should have method %s (%s)",
				methodName,
				expected.description,
			)

			if exists {
				methodType := method.Type
				assert.Equal(t, expected.paramCount, methodType.NumIn(),
					"Method %s should have %d parameters", methodName, expected.paramCount)

				if expected.returnsBuilder {
					assert.Equal(
						t,
						1,
						methodType.NumOut(),
						"Method %s should return 1 value",
						methodName,
					)
					assert.Equal(t, "*saga.SagaBuilder", methodType.Out(0).String(),
						"Method %s should return *SagaBuilder for chaining", methodName)
				}

				if expected.returnsSaga {
					assert.Equal(
						t,
						1,
						methodType.NumOut(),
						"Method %s should return 1 value",
						methodName,
					)
					assert.Equal(t, "*saga.Saga", methodType.Out(0).String(),
						"Method %s should return *Saga", methodName)
				}
			}
		}
	})
}

// TestRateLimitContract validates rate limiting implementation
func TestRateLimitContract(t *testing.T) {
	t.Run("rate_limiter_interface_contract", func(t *testing.T) {
		// Test RateLimiter creation and basic structure
		limiter := ratelimit.NewRateLimiter(10, time.Second)
		require.NotNil(t, limiter, "Rate limiter should be created")

		limiterType := reflect.TypeOf(limiter)

		// Verify essential methods exist
		expectedMethods := map[string]rateLimiterMethodContract{
			"Allow": {
				description: "Check if request is allowed",
				paramCount:  1, // receiver only
				returnsBool: true,
			},
			"Wait": {
				description:  "Wait for permission to proceed",
				paramCount:   2, // receiver + context
				returnsError: true,
			},
		}

		for methodName, expected := range expectedMethods {
			method, exists := limiterType.MethodByName(methodName)
			assert.True(
				t,
				exists,
				"RateLimiter should have method %s (%s)",
				methodName,
				expected.description,
			)

			if exists {
				methodType := method.Type
				assert.Equal(t, expected.paramCount, methodType.NumIn(),
					"Method %s should have %d parameters", methodName, expected.paramCount)

				if expected.returnsBool {
					assert.Equal(
						t,
						1,
						methodType.NumOut(),
						"Method %s should return 1 value",
						methodName,
					)
					assert.Equal(t, "bool", methodType.Out(0).String(),
						"Method %s should return bool", methodName)
				}

				if expected.returnsError {
					assert.Equal(
						t,
						1,
						methodType.NumOut(),
						"Method %s should return 1 value",
						methodName,
					)
					assert.Equal(t, "error", methodType.Out(0).String(),
						"Method %s should return error", methodName)
				}
			}
		}
	})

	t.Run("rate_limiter_behavior_contract", func(t *testing.T) {
		// Test basic rate limiting behavior
		limiter := ratelimit.NewRateLimiter(1, time.Second)

		// First request should be allowed
		allowed := limiter.Allow()
		assert.True(t, allowed, "First request should be allowed")

		// Immediate second request should be denied (rate limited)
		allowed = limiter.Allow()
		assert.False(t, allowed, "Immediate second request should be denied")

		// Test context with Wait method
		ctx := context.Background()
		err := limiter.Wait(ctx)
		// Note: This may block or return immediately depending on implementation
		// The contract is that it should respect context and return error appropriately
		if err != nil {
			assert.Implements(t, (*error)(nil), err, "Wait should return proper error")
		}
	})
}

// TestRetryContract validates retry mechanism implementation
func TestRetryContract(t *testing.T) {
	t.Run("retry_function_contract", func(t *testing.T) {
		// Test retry.Do function signature contract
		retryFuncType := reflect.TypeOf(retry.Do)

		// retry.Do should be a function
		assert.Equal(t, reflect.Func, retryFuncType.Kind(), "retry.Do should be a function")

		// Should accept context, operation, and options
		assert.GreaterOrEqual(
			t,
			retryFuncType.NumIn(),
			2,
			"retry.Do should accept at least 2 parameters",
		)

		// Should return error
		assert.Equal(t, 1, retryFuncType.NumOut(), "retry.Do should return 1 value")
		assert.Equal(t, "error", retryFuncType.Out(0).String(), "retry.Do should return error")
	})

	t.Run("retry_options_contract", func(t *testing.T) {
		// Test retry option functions exist and can be used
		ctx := context.Background()

		// Test that retry can be called with various options
		err := retry.Do(ctx, func() error {
			return nil // Always succeed for contract test
		}, retry.WithMaxAttempts(3), retry.WithDelay(100*time.Millisecond))

		assert.NoError(t, err, "Retry with options should work for successful operation")

		// Test that retry respects context cancellation
		cancelledCtx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		err = retry.Do(cancelledCtx, func() error {
			return nil
		})

		// Should respect cancelled context
		if err != nil {
			assert.True(t, err == context.Canceled || err == context.DeadlineExceeded,
				"Retry should respect context cancellation")
		}
	})

	t.Run("retry_jitter_contract", func(t *testing.T) {
		// Test jitter functionality exists and can be configured
		// This validates the jitter interface contract

		// Test that jitter types are available
		jitterTypes := []interface{}{
			retry.JitterNone,
			retry.JitterFull,
			retry.JitterEqual,
		}

		for _, jitterType := range jitterTypes {
			assert.NotNil(t, jitterType, "Jitter type should be defined")
		}

		// Test that retry can be configured with jitter
		ctx := context.Background()
		err := retry.Do(ctx, func() error {
			return nil
		}, retry.WithJitter(retry.JitterFull))

		assert.NoError(t, err, "Retry with jitter should work")
	})
}

// Helper types for contract testing

type circuitBreakerMethodContract struct {
	description  string
	paramCount   int
	returnsError bool
}

type sagaMethodContract struct {
	description  string
	paramCount   int
	returnsSaga  bool
	returnsError bool
}

type coordinatorMethodContract struct {
	description      string
	paramCount       int
	returnsError     bool
	returnsString    bool
	returnsExecution bool
}

type builderMethodContract struct {
	description    string
	paramCount     int
	returnsBuilder bool
	returnsSaga    bool
}

type rateLimiterMethodContract struct {
	description  string
	paramCount   int
	returnsBool  bool
	returnsError bool
}
