//go:build unit

package circuitbreaker

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"

	"github.com/sony/gobreaker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCircuitBreaker_InitialState(t *testing.T) {
	logger := obs.Nop()
	manager, err := NewManager(logger)
	require.NoError(t, err)

	config := DefaultConfig()
	_, err = manager.GetOrCreate("test-service", config)
	assert.NoError(t, err)

	// Circuit breaker should start in closed state
	assert.Equal(t, StateClosed, manager.GetState("test-service"))
	assert.True(t, manager.IsHealthy("test-service"))
}

func TestCircuitBreaker_OpenState(t *testing.T) {
	logger := obs.Nop()
	manager, err := NewManager(logger)
	require.NoError(t, err)

	config := Config{
		MaxRequests:         1,
		Interval:            100 * time.Millisecond,
		Timeout:             1 * time.Second,
		ConsecutiveFailures: 3,
		FailureRatio:        0.5,
		MinRequests:         2,
	}

	_, err = manager.GetOrCreate("test-service", config)
	assert.NoError(t, err)

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
	_, err = manager.Execute("test-service", func() (any, error) {
		time.Sleep(5 * time.Second) // This should not execute
		return nil, nil
	})
	duration := time.Since(start)

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "circuit breaker open")
	assert.Less(t, duration, 100*time.Millisecond, "Should fast-fail when circuit breaker is open")
}

func TestCircuitBreaker_SuccessfulExecution(t *testing.T) {
	logger := obs.Nop()
	manager, err := NewManager(logger)
	require.NoError(t, err)

	config := DefaultConfig()
	_, err = manager.GetOrCreate("test-service", config)
	assert.NoError(t, err)

	result, err := manager.Execute("test-service", func() (any, error) {
		return "success", nil
	})

	assert.NoError(t, err)
	assert.Equal(t, "success", result)
	assert.Equal(t, StateClosed, manager.GetState("test-service"))
}

func TestCircuitBreaker_GetCounts(t *testing.T) {
	logger := obs.Nop()
	manager, err := NewManager(logger)
	require.NoError(t, err)

	config := DefaultConfig()
	_, err = manager.GetOrCreate("test-service", config)
	assert.NoError(t, err)

	// Execute some requests
	for i := 0; i < 5; i++ {
		_, err = manager.Execute("test-service", func() (any, error) {
			return "success", nil
		})
		require.NoError(t, err)
	}

	// Trigger some failures
	for i := 0; i < 3; i++ {
		_, err = manager.Execute("test-service", func() (any, error) {
			return nil, errors.New("failure")
		})
		require.Error(t, err)
	}

	counts := manager.GetCounts("test-service")
	assert.Equal(t, uint32(8), counts.Requests)
	assert.Equal(t, uint32(5), counts.TotalSuccesses)
	assert.Equal(t, uint32(3), counts.TotalFailures)
}

func TestCircuitBreaker_Reset(t *testing.T) {
	logger := obs.Nop()
	manager, err := NewManager(logger)
	require.NoError(t, err)

	config := Config{
		MaxRequests:         1,
		Interval:            100 * time.Millisecond,
		Timeout:             1 * time.Second,
		ConsecutiveFailures: 3,
		FailureRatio:        0.5,
		MinRequests:         2,
	}

	_, err = manager.GetOrCreate("test-service", config)
	assert.NoError(t, err)

	// Trigger failures to open circuit breaker
	for i := 0; i < 5; i++ {
		_, err = manager.Execute("test-service", func() (any, error) {
			return nil, errors.New("service error")
		})
		require.Error(t, err)
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
	logger := obs.Nop()
	manager, err := NewManager(logger)
	require.NoError(t, err)

	// Query non-existent service
	assert.Equal(t, StateUnknown, manager.GetState("non-existent"))

	// Execute on non-existent service should fail
	_, err = manager.Execute("non-existent", func() (any, error) {
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
	logger := obs.Nop()
	manager, err := NewManager(logger)
	require.NoError(t, err)

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
	_, err = manager.GetOrCreate("test-service", config)
	assert.NoError(t, err)

	// Trigger failures to open circuit breaker and trigger state change
	for i := 0; i < 3; i++ {
		_, err = manager.Execute("test-service", func() (any, error) {
			return nil, errors.New("service error")
		})
		require.Error(t, err)
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
	logger := obs.Nop()
	manager, err := NewManager(logger)
	require.NoError(t, err)

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
	_, err = manager.GetOrCreate("test-service", config)
	assert.NoError(t, err)

	// Trigger a state change to ensure system still works
	for i := 0; i < 3; i++ {
		_, err = manager.Execute("test-service", func() (any, error) {
			return nil, errors.New("service error")
		})
		require.Error(t, err)
	}

	// Should successfully transition to open state
	assert.Equal(t, StateOpen, manager.GetState("test-service"))
}

// mockStateChangeListener is a test helper for mocking state change listeners
type mockStateChangeListener struct {
	onStateChangeFn func(serviceName string, from State, to State)
}

func (m *mockStateChangeListener) OnStateChange(_ context.Context, serviceName string, from State, to State) {
	if m.onStateChangeFn != nil {
		m.onStateChangeFn(serviceName, from, to)
	}
}

func TestNewManager_NilLogger(t *testing.T) {
	m, err := NewManager(nil)
	assert.Nil(t, m)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrNilLogger)
}

func TestGetOrCreate_InvalidConfig(t *testing.T) {
	logger := obs.Nop()
	m, err := NewManager(logger)
	require.NoError(t, err)

	// Both trip conditions zero → invalid
	invalidCfg := Config{
		ConsecutiveFailures: 0,
		MinRequests:         0,
	}

	cb, err := m.GetOrCreate("bad-config-service", invalidCfg)
	assert.Nil(t, cb)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidConfig)
}

func TestGetOrCreate_ReturnExistingBreaker(t *testing.T) {
	logger := obs.Nop()
	m, err := NewManager(logger)
	require.NoError(t, err)

	cfg := DefaultConfig()

	cb1, err := m.GetOrCreate("my-service", cfg)
	require.NoError(t, err)

	cb2, err := m.GetOrCreate("my-service", cfg)
	require.NoError(t, err)

	// Both should return a valid breaker in the same state
	assert.Equal(t, cb1.State(), cb2.State())
}

func TestExecute_OpenStateRejection(t *testing.T) {
	logger := obs.Nop()
	m, err := NewManager(logger)
	require.NoError(t, err)

	cfg := Config{
		MaxRequests:         1,
		Interval:            100 * time.Millisecond,
		Timeout:             200 * time.Millisecond,
		ConsecutiveFailures: 2,
		FailureRatio:        0.5,
		MinRequests:         2,
	}

	_, err = m.GetOrCreate("svc", cfg)
	require.NoError(t, err)

	// Trip the breaker open by sending consecutive failures
	for i := 0; i < 3; i++ {
		_, _ = m.Execute("svc", func() (any, error) {
			return nil, errors.New("fail")
		})
	}
	assert.Equal(t, StateOpen, m.GetState("svc"))

	// Poll until the breaker transitions to half-open (timeout is 200ms)
	require.Eventually(t, func() bool {
		return m.GetState("svc") == StateHalfOpen
	}, 2*time.Second, 10*time.Millisecond, "breaker should transition to half-open after timeout")

	// MaxRequests=1, so the first call in half-open is the probe.
	// Make it fail so the breaker re-opens.
	_, _ = m.Execute("svc", func() (any, error) {
		return nil, errors.New("still failing")
	})

	// Poll again until the breaker transitions back to half-open
	require.Eventually(t, func() bool {
		return m.GetState("svc") == StateHalfOpen
	}, 2*time.Second, 10*time.Millisecond, "breaker should transition to half-open after second timeout")

	// In half-open the probe call (first) is allowed; make it fail to re-open
	_, err = m.Execute("svc", func() (any, error) {
		return nil, errors.New("probe fail")
	})
	// After the probe fails in half-open, breaker re-opens.
	// The next call should be rejected with an open-state error.
	_, err = m.Execute("svc", func() (any, error) {
		return nil, nil
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "currently unavailable")
}

func TestGetCounts_NonExistentService(t *testing.T) {
	logger := obs.Nop()
	m, err := NewManager(logger)
	require.NoError(t, err)

	counts := m.GetCounts("does-not-exist")
	assert.Equal(t, Counts{}, counts)
}

func TestIsHealthy_NonExistentService(t *testing.T) {
	logger := obs.Nop()
	m, err := NewManager(logger)
	require.NoError(t, err)

	// StateUnknown != StateClosed → not healthy
	assert.False(t, m.IsHealthy("no-such-service"))
}

func TestReset_NonExistentService(t *testing.T) {
	logger := obs.Nop()
	m, err := NewManager(logger)
	require.NoError(t, err)

	// Should be a no-op, not panic
	assert.NotPanics(t, func() {
		m.Reset("non-existent-service")
	})
}

func TestCircuitBreaker_Wrapper_Execute(t *testing.T) {
	logger := obs.Nop()
	m, err := NewManager(logger)
	require.NoError(t, err)

	cfg := DefaultConfig()
	cb, err := m.GetOrCreate("wrapper-test", cfg)
	require.NoError(t, err)

	// Test Execute through the CircuitBreaker interface
	result, err := cb.Execute(func() (any, error) {
		return 42, nil
	})
	assert.NoError(t, err)
	assert.Equal(t, 42, result)

	// Test State
	assert.Equal(t, StateClosed, cb.State())

	// Test Counts
	counts := cb.Counts()
	assert.Equal(t, uint32(1), counts.Requests)
	assert.Equal(t, uint32(1), counts.TotalSuccesses)
}

func TestReadyToTrip_ConsecutiveFailures(t *testing.T) {
	cfg := Config{
		ConsecutiveFailures: 3,
		MinRequests:         0,
	}

	tripFn := readyToTrip(cfg)

	// Below threshold
	assert.False(t, tripFn(gobreaker.Counts{ConsecutiveFailures: 2}))

	// At threshold
	assert.True(t, tripFn(gobreaker.Counts{ConsecutiveFailures: 3}))

	// Above threshold
	assert.True(t, tripFn(gobreaker.Counts{ConsecutiveFailures: 5}))
}

func TestReadyToTrip_FailureRatio(t *testing.T) {
	cfg := Config{
		ConsecutiveFailures: 0,
		MinRequests:         4,
		FailureRatio:        0.5,
	}

	tripFn := readyToTrip(cfg)

	// Not enough requests
	assert.False(t, tripFn(gobreaker.Counts{Requests: 3, TotalFailures: 3}))

	// Enough requests, below ratio
	assert.False(t, tripFn(gobreaker.Counts{Requests: 4, TotalFailures: 1}))

	// Enough requests, at ratio
	assert.True(t, tripFn(gobreaker.Counts{Requests: 4, TotalFailures: 2}))

	// Enough requests, above ratio
	assert.True(t, tripFn(gobreaker.Counts{Requests: 4, TotalFailures: 3}))
}

func TestReadyToTrip_NeitherConditionMet(t *testing.T) {
	cfg := Config{
		ConsecutiveFailures: 0,
		MinRequests:         0,
	}

	tripFn := readyToTrip(cfg)

	// Both conditions disabled → never trips
	assert.False(t, tripFn(gobreaker.Counts{Requests: 100, TotalFailures: 100, ConsecutiveFailures: 100}))
}

// admissionConfig trips on the first failure so tests reach OPEN in one call.
func admissionConfig(maxRequests uint32, timeout time.Duration) Config {
	return Config{MaxRequests: maxRequests, Timeout: timeout, ConsecutiveFailures: 1}
}

func newAdmissionManager(t *testing.T) (TenantAwareManager, AdmissionReader) {
	t.Helper()

	mgr, err := NewManager(obs.Nop())
	require.NoError(t, err)

	reader, ok := mgr.(AdmissionReader)
	require.True(t, ok, "NewManager must satisfy AdmissionReader")

	return mgr.(TenantAwareManager), reader
}

// tripBreaker fails one call and returns the instants that bracket the
// transition to OPEN.
func tripBreaker(t *testing.T, mgr TenantAwareManager, tenantID, serviceName string) (time.Time, time.Time) {
	t.Helper()

	before := time.Now()

	var err error
	if tenantID == "" {
		_, err = mgr.Execute(serviceName, func() (any, error) { return nil, errors.New("down") })
	} else {
		_, err = mgr.ExecuteForTenant(context.Background(), tenantID, serviceName, func() (any, error) { return nil, errors.New("down") })
	}

	require.Error(t, err)

	return before, time.Now()
}

// holdProbe starts one call that stays in flight until release is closed.
func holdProbe(t *testing.T, mgr Manager, serviceName string, release <-chan struct{}, result chan error) {
	t.Helper()

	started := make(chan struct{})

	go func() {
		_, err := mgr.Execute(serviceName, func() (any, error) {
			close(started)
			<-release

			return nil, nil
		})
		result <- err
	}()

	select {
	case <-started:
	case err := <-result:
		t.Fatalf("probe was not admitted: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("probe did not start")
	}
}

func TestAdmission_UnknownClosedAndInvalidAreNotBlocked(t *testing.T) {
	mgr, reader := newAdmissionManager(t)

	retryAt, blocked := reader.Admission("missing")
	assert.False(t, blocked)
	assert.True(t, retryAt.IsZero())

	_, err := mgr.GetOrCreate("svc", admissionConfig(1, time.Minute))
	require.NoError(t, err)

	retryAt, blocked = reader.Admission("svc")
	assert.False(t, blocked)
	assert.True(t, retryAt.IsZero())

	retryAt, blocked = reader.Admission("")
	assert.False(t, blocked)
	assert.True(t, retryAt.IsZero())
}

func TestAdmission_DefaultTimeoutIsSixtySeconds(t *testing.T) {
	mgr, reader := newAdmissionManager(t)

	_, err := mgr.GetOrCreate("svc", admissionConfig(0, 0))
	require.NoError(t, err)

	before, after := tripBreaker(t, mgr, "", "svc")

	retryAt, blocked := reader.Admission("svc")
	require.True(t, blocked)
	assert.False(t, retryAt.Before(before.Add(60*time.Second)))
	assert.False(t, retryAt.After(after.Add(60*time.Second)))
}

func TestAdmission_HalfOpenQuotaWithoutConsumingProbes(t *testing.T) {
	mgr, reader := newAdmissionManager(t)

	_, err := mgr.GetOrCreate("svc", admissionConfig(2, 50*time.Millisecond))
	require.NoError(t, err)

	tripBreaker(t, mgr, "", "svc")

	// Lazy expiry: only the admission read moves the breaker to HALF-OPEN.
	require.Eventually(t, func() bool {
		_, blocked := reader.Admission("svc")
		return !blocked
	}, 5*time.Second, 10*time.Millisecond)
	require.Equal(t, StateHalfOpen, mgr.GetState("svc"))

	for range 10 {
		retryAt, blocked := reader.Admission("svc")
		require.False(t, blocked, "repeated reads must not reserve probes")
		require.True(t, retryAt.IsZero())
	}

	release := make(chan struct{})
	results := make(chan error, 2)

	holdProbe(t, mgr, "svc", release, results)

	retryAt, blocked := reader.Admission("svc")
	assert.False(t, blocked, "one of two probes in flight leaves capacity")
	assert.True(t, retryAt.IsZero())

	holdProbe(t, mgr, "svc", release, results)

	retryAt, blocked = reader.Admission("svc")
	assert.True(t, blocked, "exhausted HALF-OPEN is blocked")
	assert.True(t, retryAt.IsZero(), "exhausted HALF-OPEN has no deadline")

	close(release)
	require.NoError(t, <-results)
	require.NoError(t, <-results)

	require.Equal(t, StateClosed, mgr.GetState("svc"))

	retryAt, blocked = reader.Admission("svc")
	assert.False(t, blocked)
	assert.True(t, retryAt.IsZero())
}

func TestAdmission_DefaultMaxRequestsIsOne(t *testing.T) {
	mgr, reader := newAdmissionManager(t)

	_, err := mgr.GetOrCreate("svc", admissionConfig(0, 20*time.Millisecond))
	require.NoError(t, err)

	tripBreaker(t, mgr, "", "svc")

	require.Eventually(t, func() bool {
		_, blocked := reader.Admission("svc")
		return !blocked
	}, 5*time.Second, 5*time.Millisecond)

	release := make(chan struct{})
	results := make(chan error, 1)

	holdProbe(t, mgr, "svc", release, results)

	retryAt, blocked := reader.Admission("svc")
	assert.True(t, blocked)
	assert.True(t, retryAt.IsZero())

	_, err = mgr.Execute("svc", func() (any, error) { return nil, nil })
	require.ErrorIs(t, err, ErrBreakerHalfOpenFull, "gobreaker must agree the quota is one probe")

	close(release)
	require.NoError(t, <-results)
}

func TestAdmission_HalfOpenProbeFailureReopensWithNewDeadline(t *testing.T) {
	mgr, reader := newAdmissionManager(t)

	const timeout = 250 * time.Millisecond

	_, err := mgr.GetOrCreate("svc", admissionConfig(1, timeout))
	require.NoError(t, err)

	tripBreaker(t, mgr, "", "svc")
	_, blocked := reader.Admission("svc")
	require.True(t, blocked)

	require.Eventually(t, func() bool {
		_, blocked := reader.Admission("svc")
		return !blocked
	}, 5*time.Second, 5*time.Millisecond)

	before, after := tripBreaker(t, mgr, "", "svc")

	retryAt, blocked := reader.Admission("svc")
	require.True(t, blocked)
	assert.False(t, retryAt.Before(before.Add(timeout)))
	assert.False(t, retryAt.After(after.Add(timeout)))
}

func TestAdmission_CorrectWhenListenerNotificationsAreDropped(t *testing.T) {
	mgr, reader := newAdmissionManager(t)

	listener := newCaptureTenantListener(1)
	mgr.RegisterTenantStateChangeListener(listener)

	// Saturate the listener queue so every notification is dropped.
	impl := mgr.(*manager)
	for range cap(impl.listenerSem) {
		impl.listenerSem <- struct{}{}
	}

	_, err := mgr.GetOrCreate("svc", admissionConfig(1, time.Minute))
	require.NoError(t, err)

	before, after := tripBreaker(t, mgr, "", "svc")

	retryAt, blocked := reader.Admission("svc")
	require.True(t, blocked)
	assert.False(t, retryAt.Before(before.Add(time.Minute)))
	assert.False(t, retryAt.After(after.Add(time.Minute)))
	assert.Empty(t, listener.snapshot(), "the notification must have been dropped")
}
