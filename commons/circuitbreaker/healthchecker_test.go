//go:build unit

package circuitbreaker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewHealthCheckerWithValidation_Success(t *testing.T) {
	logger := &log.NopLogger{}
	manager, err := NewManager(logger)
	require.NoError(t, err)

	hc, err := NewHealthCheckerWithValidation(manager, 1*time.Second, 500*time.Millisecond, logger)

	assert.NoError(t, err)
	assert.NotNil(t, hc)
}

func TestNewHealthCheckerWithValidation_InvalidInterval(t *testing.T) {
	logger := &log.NopLogger{}
	manager, err := NewManager(logger)
	require.NoError(t, err)

	hc, err := NewHealthCheckerWithValidation(manager, 0, 500*time.Millisecond, logger)

	assert.Nil(t, hc)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidHealthCheckInterval))
}

func TestNewHealthCheckerWithValidation_NegativeInterval(t *testing.T) {
	logger := &log.NopLogger{}
	manager, err := NewManager(logger)
	require.NoError(t, err)

	hc, err := NewHealthCheckerWithValidation(manager, -1*time.Second, 500*time.Millisecond, logger)

	assert.Nil(t, hc)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidHealthCheckInterval))
}

func TestNewHealthCheckerWithValidation_InvalidTimeout(t *testing.T) {
	logger := &log.NopLogger{}
	manager, err := NewManager(logger)
	require.NoError(t, err)

	hc, err := NewHealthCheckerWithValidation(manager, 1*time.Second, 0, logger)

	assert.Nil(t, hc)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidHealthCheckTimeout))
}

func TestNewHealthCheckerWithValidation_NegativeTimeout(t *testing.T) {
	logger := &log.NopLogger{}
	manager, err := NewManager(logger)
	require.NoError(t, err)

	hc, err := NewHealthCheckerWithValidation(manager, 1*time.Second, -500*time.Millisecond, logger)

	assert.Nil(t, hc)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrInvalidHealthCheckTimeout))
}

func TestNewHealthCheckerWithValidation_NilManager(t *testing.T) {
	logger := &log.NopLogger{}

	hc, err := NewHealthCheckerWithValidation(nil, 1*time.Second, 500*time.Millisecond, logger)

	assert.Nil(t, hc)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrNilManager))
}

func TestNewHealthCheckerWithValidation_NilLogger(t *testing.T) {
	manager, err := NewManager(&log.NopLogger{})
	require.NoError(t, err)

	hc, err := NewHealthCheckerWithValidation(manager, 1*time.Second, 500*time.Millisecond, nil)

	assert.Nil(t, hc)
	assert.Error(t, err)
	assert.True(t, errors.Is(err, ErrNilLogger))
}

// --- Helper to create a test healthChecker ---

func newTestHealthChecker(t *testing.T) (HealthChecker, Manager) {
	t.Helper()

	logger := &log.NopLogger{}
	mgr, err := NewManager(logger)
	require.NoError(t, err)

	hc, err := NewHealthCheckerWithValidation(mgr, 50*time.Millisecond, 100*time.Millisecond, logger)
	require.NoError(t, err)

	return hc, mgr
}

func TestRegister_NilHealthCheckFunction(t *testing.T) {
	hc, _ := newTestHealthChecker(t)

	// Should not panic, should be a no-op (logs warning)
	assert.NotPanics(t, func() {
		hc.Register("svc", nil)
	})

	// Service should not appear in health status
	status := hc.GetHealthStatus()
	_, exists := status["svc"]
	assert.False(t, exists)
}

func TestRegister_ValidFunction(t *testing.T) {
	hc, mgr := newTestHealthChecker(t)

	cfg := DefaultConfig()
	_, err := mgr.GetOrCreate("my-svc", cfg)
	require.NoError(t, err)

	hc.Register("my-svc", func(ctx context.Context) error {
		return nil
	})

	status := hc.GetHealthStatus()
	_, exists := status["my-svc"]
	assert.True(t, exists)
}

func TestStart_DuplicateIsNoop(t *testing.T) {
	hc, _ := newTestHealthChecker(t)

	// First start
	hc.Start()

	// Second start should be a no-op, not panic
	assert.NotPanics(t, func() {
		hc.Start()
	})

	hc.Stop()
}

func TestStop(t *testing.T) {
	hc, _ := newTestHealthChecker(t)

	hc.Start()

	// Stop should complete without hanging
	done := make(chan struct{})
	go func() {
		hc.Stop()
		close(done)
	}()

	select {
	case <-done:
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not return in time")
	}
}

func TestGetHealthStatus(t *testing.T) {
	hc, mgr := newTestHealthChecker(t)

	cfg := DefaultConfig()

	_, err := mgr.GetOrCreate("svc-a", cfg)
	require.NoError(t, err)

	_, err = mgr.GetOrCreate("svc-b", cfg)
	require.NoError(t, err)

	hc.Register("svc-a", func(ctx context.Context) error { return nil })
	hc.Register("svc-b", func(ctx context.Context) error { return nil })

	status := hc.GetHealthStatus()
	assert.Equal(t, string(StateClosed), status["svc-a"])
	assert.Equal(t, string(StateClosed), status["svc-b"])
}

func TestOnStateChange_OpenTriggersImmediateCheck(t *testing.T) {
	hc, _ := newTestHealthChecker(t)

	// Access the internal immediateCheck channel
	hcInternal := hc.(*healthChecker)

	hc.(*healthChecker).OnStateChange(context.Background(), "test-svc", StateClosed, StateOpen)

	// Should have sent a message to immediateCheck channel
	select {
	case svc := <-hcInternal.immediateCheck:
		assert.Equal(t, "test-svc", svc)
	case <-time.After(1 * time.Second):
		t.Fatal("Expected immediate check to be scheduled")
	}
}

func TestOnStateChange_NonOpenDoesNotTrigger(t *testing.T) {
	hc, _ := newTestHealthChecker(t)

	hcInternal := hc.(*healthChecker)

	hc.(*healthChecker).OnStateChange(context.Background(), "test-svc", StateOpen, StateClosed)

	// Should NOT have sent a message
	select {
	case <-hcInternal.immediateCheck:
		t.Fatal("Should not trigger immediate check for non-Open state")
	case <-time.After(50 * time.Millisecond):
		// expected
	}
}

func TestCheckServiceHealth_NonExistentService(t *testing.T) {
	hc, _ := newTestHealthChecker(t)

	hcInternal := hc.(*healthChecker)

	// Should not panic when service is not registered
	assert.NotPanics(t, func() {
		hcInternal.checkServiceHealth("non-existent")
	})
}

func TestCheckServiceHealth_AlreadyHealthy(t *testing.T) {
	hc, mgr := newTestHealthChecker(t)

	cfg := DefaultConfig()
	_, err := mgr.GetOrCreate("healthy-svc", cfg)
	require.NoError(t, err)

	called := false
	hc.Register("healthy-svc", func(ctx context.Context) error {
		called = true
		return nil
	})

	hcInternal := hc.(*healthChecker)
	hcInternal.checkServiceHealth("healthy-svc")

	// Health check function should NOT be called since service is healthy
	assert.False(t, called)
}

func TestCheckServiceHealth_SuccessfulRecovery(t *testing.T) {
	logger := &log.NopLogger{}
	mgr, err := NewManager(logger)
	require.NoError(t, err)

	cfg := Config{
		MaxRequests:         1,
		Interval:            100 * time.Millisecond,
		Timeout:             1 * time.Second,
		ConsecutiveFailures: 2,
		FailureRatio:        0.5,
		MinRequests:         2,
	}

	_, err = mgr.GetOrCreate("recover-svc", cfg)
	require.NoError(t, err)

	// Trip the breaker
	for i := 0; i < 3; i++ {
		_, _ = mgr.Execute("recover-svc", func() (any, error) {
			return nil, errors.New("fail")
		})
	}
	assert.Equal(t, StateOpen, mgr.GetState("recover-svc"))

	hc, err := NewHealthCheckerWithValidation(mgr, 50*time.Millisecond, 100*time.Millisecond, logger)
	require.NoError(t, err)

	hc.Register("recover-svc", func(ctx context.Context) error {
		return nil // healthy
	})

	hcInternal := hc.(*healthChecker)
	hcInternal.checkServiceHealth("recover-svc")

	// Should have reset the breaker
	assert.Equal(t, StateClosed, mgr.GetState("recover-svc"))
}

func TestCheckServiceHealth_FailedRecovery(t *testing.T) {
	logger := &log.NopLogger{}
	mgr, err := NewManager(logger)
	require.NoError(t, err)

	cfg := Config{
		MaxRequests:         1,
		Interval:            100 * time.Millisecond,
		Timeout:             1 * time.Second,
		ConsecutiveFailures: 2,
		FailureRatio:        0.5,
		MinRequests:         2,
	}

	_, err = mgr.GetOrCreate("fail-svc", cfg)
	require.NoError(t, err)

	// Trip the breaker
	for i := 0; i < 3; i++ {
		_, _ = mgr.Execute("fail-svc", func() (any, error) {
			return nil, errors.New("fail")
		})
	}
	assert.Equal(t, StateOpen, mgr.GetState("fail-svc"))

	hc, err := NewHealthCheckerWithValidation(mgr, 50*time.Millisecond, 100*time.Millisecond, logger)
	require.NoError(t, err)

	hc.Register("fail-svc", func(ctx context.Context) error {
		return errors.New("still down")
	})

	hcInternal := hc.(*healthChecker)
	hcInternal.checkServiceHealth("fail-svc")

	// Breaker should remain open
	assert.Equal(t, StateOpen, mgr.GetState("fail-svc"))
}

func TestPerformHealthChecks_MixedServices(t *testing.T) {
	logger := &log.NopLogger{}
	mgr, err := NewManager(logger)
	require.NoError(t, err)

	cfg := Config{
		MaxRequests:         1,
		Interval:            100 * time.Millisecond,
		Timeout:             1 * time.Second,
		ConsecutiveFailures: 2,
		FailureRatio:        0.5,
		MinRequests:         2,
	}

	// Create two services
	_, err = mgr.GetOrCreate("healthy-svc", cfg)
	require.NoError(t, err)

	_, err = mgr.GetOrCreate("unhealthy-svc", cfg)
	require.NoError(t, err)

	// Trip the breaker on unhealthy-svc only
	for i := 0; i < 3; i++ {
		_, _ = mgr.Execute("unhealthy-svc", func() (any, error) {
			return nil, errors.New("fail")
		})
	}
	assert.Equal(t, StateOpen, mgr.GetState("unhealthy-svc"))
	assert.Equal(t, StateClosed, mgr.GetState("healthy-svc"))

	hc, err := NewHealthCheckerWithValidation(mgr, 50*time.Millisecond, 100*time.Millisecond, logger)
	require.NoError(t, err)

	healthyChecked := false
	hc.Register("healthy-svc", func(ctx context.Context) error {
		healthyChecked = true
		return nil
	})

	hc.Register("unhealthy-svc", func(ctx context.Context) error {
		return nil // simulate recovery
	})

	hcInternal := hc.(*healthChecker)
	hcInternal.performHealthChecks()

	// Healthy service should be skipped (its health check func not called)
	assert.False(t, healthyChecked)

	// Unhealthy service should have recovered
	assert.Equal(t, StateClosed, mgr.GetState("unhealthy-svc"))
}

func TestPerformHealthChecks_UnhealthyStaysUnhealthy(t *testing.T) {
	logger := &log.NopLogger{}
	mgr, err := NewManager(logger)
	require.NoError(t, err)

	cfg := Config{
		MaxRequests:         1,
		Interval:            100 * time.Millisecond,
		Timeout:             1 * time.Second,
		ConsecutiveFailures: 2,
		FailureRatio:        0.5,
		MinRequests:         2,
	}

	_, err = mgr.GetOrCreate("still-down", cfg)
	require.NoError(t, err)

	// Trip the breaker
	for i := 0; i < 3; i++ {
		_, _ = mgr.Execute("still-down", func() (any, error) {
			return nil, errors.New("fail")
		})
	}

	hc, err := NewHealthCheckerWithValidation(mgr, 50*time.Millisecond, 100*time.Millisecond, logger)
	require.NoError(t, err)

	hc.Register("still-down", func(ctx context.Context) error {
		return errors.New("nope")
	})

	hcInternal := hc.(*healthChecker)
	hcInternal.performHealthChecks()

	// Should remain open
	assert.Equal(t, StateOpen, mgr.GetState("still-down"))
}

func TestHealthCheckLoop_PeriodicChecks(t *testing.T) {
	logger := &log.NopLogger{}
	mgr, err := NewManager(logger)
	require.NoError(t, err)

	cfg := Config{
		MaxRequests:         1,
		Interval:            100 * time.Millisecond,
		Timeout:             1 * time.Second,
		ConsecutiveFailures: 2,
		FailureRatio:        0.5,
		MinRequests:         2,
	}

	_, err = mgr.GetOrCreate("periodic-svc", cfg)
	require.NoError(t, err)

	// Trip the breaker
	for i := 0; i < 3; i++ {
		_, _ = mgr.Execute("periodic-svc", func() (any, error) {
			return nil, errors.New("fail")
		})
	}
	assert.Equal(t, StateOpen, mgr.GetState("periodic-svc"))

	hc, err := NewHealthCheckerWithValidation(mgr, 50*time.Millisecond, 100*time.Millisecond, logger)
	require.NoError(t, err)

	hc.Register("periodic-svc", func(ctx context.Context) error {
		return nil // recovery succeeds
	})

	hc.Start()
	defer hc.Stop()

	// Poll until the periodic health check fires and recovers the breaker
	require.Eventually(t, func() bool {
		return mgr.GetState("periodic-svc") == StateClosed
	}, 2*time.Second, 50*time.Millisecond, "periodic health check should recover the breaker")
}

func TestOnStateChange_ImmediateCheckChannelFull(t *testing.T) {
	hc, _ := newTestHealthChecker(t)
	hcInternal := hc.(*healthChecker)

	// Fill the immediateCheck channel (capacity 10)
	for i := 0; i < 10; i++ {
		hcInternal.immediateCheck <- "fill"
	}

	// This should not block or panic — it logs a warning instead
	assert.NotPanics(t, func() {
		hcInternal.OnStateChange(context.Background(), "overflow-svc", StateClosed, StateOpen)
	})
}
