package circuitbreaker

import (
	"errors"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v2/commons/log"
	"github.com/stretchr/testify/assert"
)

func TestCircuitBreaker_InitialState(t *testing.T) {
	logger := &log.NoneLogger{}
	manager := NewManager(logger)

	config := DefaultConfig()
	manager.GetOrCreate("test-service", config)

	// Circuit breaker should start in closed state
	assert.Equal(t, StateClosed, manager.GetState("test-service"))
	assert.True(t, manager.IsHealthy("test-service"))
}

func TestCircuitBreaker_OpenState(t *testing.T) {
	logger := &log.NoneLogger{}
	manager := NewManager(logger)

	config := Config{
		MaxRequests:         1,
		Interval:            100 * time.Millisecond,
		Timeout:             1 * time.Second,
		ConsecutiveFailures: 3,
		FailureRatio:        0.5,
		MinRequests:         2,
	}

	manager.GetOrCreate("test-service", config)

	// Trigger failures to open circuit breaker
	for i := 0; i < 5; i++ {
		_, err := manager.Execute("test-service", func() (any, error) {
			return nil, errors.New("service error")
		})
		assert.Error(t, err)
	}

	// Circuit breaker should be open
	assert.Equal(t, StateOpen, manager.GetState("test-service"))
	assert.False(t, manager.IsHealthy("test-service"))

	// Requests should fast-fail
	start := time.Now()
	_, err := manager.Execute("test-service", func() (any, error) {
		time.Sleep(5 * time.Second) // This should not execute
		return nil, nil
	})
	duration := time.Since(start)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "circuit breaker open")
	assert.Less(t, duration, 100*time.Millisecond, "Should fast-fail when circuit breaker is open")
}

func TestCircuitBreaker_SuccessfulExecution(t *testing.T) {
	logger := &log.NoneLogger{}
	manager := NewManager(logger)

	config := DefaultConfig()
	manager.GetOrCreate("test-service", config)

	result, err := manager.Execute("test-service", func() (any, error) {
		return "success", nil
	})

	assert.NoError(t, err)
	assert.Equal(t, "success", result)
	assert.Equal(t, StateClosed, manager.GetState("test-service"))
}

func TestCircuitBreaker_GetCounts(t *testing.T) {
	logger := &log.NoneLogger{}
	manager := NewManager(logger)

	config := DefaultConfig()
	manager.GetOrCreate("test-service", config)

	// Execute some requests
	for i := 0; i < 5; i++ {
		_, _ = manager.Execute("test-service", func() (any, error) {
			return "success", nil
		})
	}

	// Trigger some failures
	for i := 0; i < 3; i++ {
		_, _ = manager.Execute("test-service", func() (any, error) {
			return nil, errors.New("failure")
		})
	}

	counts := manager.GetCounts("test-service")
	assert.Equal(t, uint32(8), counts.Requests)
	assert.Equal(t, uint32(5), counts.TotalSuccesses)
	assert.Equal(t, uint32(3), counts.TotalFailures)
}

func TestCircuitBreaker_Reset(t *testing.T) {
	logger := &log.NoneLogger{}
	manager := NewManager(logger)

	config := Config{
		MaxRequests:         1,
		Interval:            100 * time.Millisecond,
		Timeout:             1 * time.Second,
		ConsecutiveFailures: 3,
		FailureRatio:        0.5,
		MinRequests:         2,
	}

	manager.GetOrCreate("test-service", config)

	// Trigger failures to open circuit breaker
	for i := 0; i < 5; i++ {
		_, _ = manager.Execute("test-service", func() (any, error) {
			return nil, errors.New("service error")
		})
	}

	// Circuit breaker should be open
	assert.Equal(t, StateOpen, manager.GetState("test-service"))

	// Reset the circuit breaker - it should automatically recreate with same config
	manager.Reset("test-service")

	// Circuit breaker should be closed and healthy after reset
	assert.Equal(t, StateClosed, manager.GetState("test-service"))
	assert.True(t, manager.IsHealthy("test-service"))

	// Verify it still works after reset
	result, err := manager.Execute("test-service", func() (any, error) {
		return "success", nil
	})
	assert.NoError(t, err)
	assert.Equal(t, "success", result)
}

func TestCircuitBreaker_UnknownService(t *testing.T) {
	logger := &log.NoneLogger{}
	manager := NewManager(logger)

	// Query non-existent service
	assert.Equal(t, StateUnknown, manager.GetState("non-existent"))

	// Execute on non-existent service should fail
	_, err := manager.Execute("non-existent", func() (any, error) {
		return "success", nil
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "circuit breaker not found")
}

func TestCircuitBreaker_ConfigPresets(t *testing.T) {
	// Test that all preset configs are valid
	configs := []Config{
		DefaultConfig(),
		AggressiveConfig(),
		ConservativeConfig(),
		HTTPServiceConfig(),
		DatabaseConfig(),
	}

	for _, config := range configs {
		assert.Greater(t, config.MaxRequests, uint32(0))
		assert.Greater(t, config.Interval, time.Duration(0))
		assert.Greater(t, config.Timeout, time.Duration(0))
		assert.Greater(t, config.ConsecutiveFailures, uint32(0))
		assert.Greater(t, config.FailureRatio, 0.0)
		assert.Greater(t, config.MinRequests, uint32(0))
	}
}

func TestCircuitBreaker_StateChangeListenerPanicRecovery(t *testing.T) {
	logger := &log.NoneLogger{}
	manager := NewManager(logger)

	config := Config{
		MaxRequests:         1,
		Interval:            100 * time.Millisecond,
		Timeout:             1 * time.Second,
		ConsecutiveFailures: 2,
		FailureRatio:        0.5,
		MinRequests:         2,
	}

	// Channels to track listener execution
	panicListenerCalled := make(chan bool, 1)
	normalListenerCalled := make(chan bool, 1)
	secondNormalListenerCalled := make(chan bool, 1)

	// Create a listener that panics
	panicListener := &mockStateChangeListener{
		onStateChangeFn: func(serviceName string, from State, to State) {
			panicListenerCalled <- true
			panic("intentional panic in listener")
		},
	}

	// Create normal listeners that should still be called despite the panic
	normalListener := &mockStateChangeListener{
		onStateChangeFn: func(serviceName string, from State, to State) {
			normalListenerCalled <- true
		},
	}

	secondNormalListener := &mockStateChangeListener{
		onStateChangeFn: func(serviceName string, from State, to State) {
			secondNormalListenerCalled <- true
		},
	}

	// Register all listeners
	manager.RegisterStateChangeListener(panicListener)
	manager.RegisterStateChangeListener(normalListener)
	manager.RegisterStateChangeListener(secondNormalListener)

	// Create circuit breaker
	manager.GetOrCreate("test-service", config)

	// Trigger failures to open circuit breaker and trigger state change
	for i := 0; i < 3; i++ {
		_, _ = manager.Execute("test-service", func() (any, error) {
			return nil, errors.New("service error")
		})
	}

	// Wait for all listeners to be called (with timeout)
	timeout := time.After(2 * time.Second)

	panicCalled := false
	normalCalled := false
	secondNormalCalled := false

	for i := 0; i < 3; i++ {
		select {
		case <-panicListenerCalled:
			panicCalled = true
		case <-normalListenerCalled:
			normalCalled = true
		case <-secondNormalListenerCalled:
			secondNormalCalled = true
		case <-timeout:
			t.Fatal("Timeout waiting for listeners to be called")
		}
	}

	// Verify all listeners were called despite the panic
	assert.True(t, panicCalled, "Panic listener should have been called")
	assert.True(t, normalCalled, "Normal listener should have been called despite panic")
	assert.True(t, secondNormalCalled, "Second normal listener should have been called despite panic")

	// Verify circuit breaker is still open
	assert.Equal(t, StateOpen, manager.GetState("test-service"))
}

func TestCircuitBreaker_NilListenerRegistration(t *testing.T) {
	logger := &log.NoneLogger{}
	manager := NewManager(logger)

	// Attempt to register nil listener
	manager.RegisterStateChangeListener(nil)

	// Should not panic and should handle gracefully
	config := Config{
		MaxRequests:         1,
		Interval:            100 * time.Millisecond,
		Timeout:             1 * time.Second,
		ConsecutiveFailures: 2,
		FailureRatio:        0.5,
		MinRequests:         2,
	}
	manager.GetOrCreate("test-service", config)

	// Trigger a state change to ensure system still works
	for i := 0; i < 3; i++ {
		_, _ = manager.Execute("test-service", func() (any, error) {
			return nil, errors.New("service error")
		})
	}

	// Should successfully transition to open state
	assert.Equal(t, StateOpen, manager.GetState("test-service"))
}

// mockStateChangeListener is a test helper for mocking state change listeners
type mockStateChangeListener struct {
	onStateChangeFn func(serviceName string, from State, to State)
}

func (m *mockStateChangeListener) OnStateChange(serviceName string, from State, to State) {
	if m.onStateChangeFn != nil {
		m.onStateChangeFn(serviceName, from, to)
	}
}
