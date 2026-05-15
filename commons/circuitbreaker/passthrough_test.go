//go:build unit

package circuitbreaker

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// -----------------------------------------------------------------------------
// Interface satisfaction
// -----------------------------------------------------------------------------

func TestPassthroughManager_SatisfiesManager(t *testing.T) {
	t.Parallel()

	var mgr Manager = NewPassthroughManager()
	require.NotNil(t, mgr)
}

func TestPassthroughManager_SatisfiesTenantAwareManager(t *testing.T) {
	t.Parallel()

	mgr := NewPassthroughManager()
	tam, ok := mgr.(TenantAwareManager)
	require.True(t, ok, "passthrough manager must also satisfy TenantAwareManager")
	require.NotNil(t, tam)
}

// -----------------------------------------------------------------------------
// Manager-level Execute semantics
// -----------------------------------------------------------------------------

func TestPassthroughManager_Execute_InvokesFnExactlyOnce(t *testing.T) {
	t.Parallel()

	mgr := NewPassthroughManager()

	calls := 0
	result, err := mgr.Execute("anything", func() (any, error) {
		calls++
		return "passthrough", nil
	})

	require.NoError(t, err)
	assert.Equal(t, "passthrough", result)
	assert.Equal(t, 1, calls)
}

func TestPassthroughManager_Execute_PropagatesError(t *testing.T) {
	t.Parallel()

	mgr := NewPassthroughManager()

	sentinel := errors.New("upstream failure")

	result, err := mgr.Execute("anything", func() (any, error) {
		return nil, sentinel
	})

	assert.Nil(t, result)
	assert.ErrorIs(t, err, sentinel)
}

func TestPassthroughManager_Execute_NilCallback(t *testing.T) {
	t.Parallel()

	mgr := NewPassthroughManager()

	result, err := mgr.Execute("anything", nil)
	assert.Nil(t, result)
	assert.ErrorIs(t, err, ErrNilCallback)
}

// -----------------------------------------------------------------------------
// Manager-level state constants
// -----------------------------------------------------------------------------

func TestPassthroughManager_GetState_ClosedForValidService(t *testing.T) {
	t.Parallel()

	mgr := NewPassthroughManager()

	for _, name := range []string{"jd", "midaz", "anything"} {
		assert.Equal(t, StateClosed, mgr.GetState(name), "service=%q", name)
	}
}

func TestPassthroughManager_InvalidServiceMatchesRealManager(t *testing.T) {
	t.Parallel()

	mgr := NewPassthroughManager()
	called := atomic.Bool{}

	result, err := mgr.Execute("", func() (any, error) {
		called.Store(true)
		return "must-not-run", nil
	})

	assert.Nil(t, result)
	assert.ErrorIs(t, err, ErrInvalidServiceName)
	assert.False(t, called.Load(), "invalid service must not execute passthrough callback")
	assert.Equal(t, StateUnknown, mgr.GetState(""))
	assert.Equal(t, Counts{}, mgr.GetCounts(""))
	assert.False(t, mgr.IsHealthy(""))
}

func TestPassthroughManager_GetCounts_AlwaysZero(t *testing.T) {
	t.Parallel()

	mgr := NewPassthroughManager()

	assert.Equal(t, Counts{}, mgr.GetCounts("anything"))
}

func TestPassthroughManager_IsHealthy_TrueForValidService(t *testing.T) {
	t.Parallel()

	mgr := NewPassthroughManager()

	assert.True(t, mgr.IsHealthy("anything"))
}

func TestPassthroughManager_Reset_Noop(t *testing.T) {
	t.Parallel()

	mgr := NewPassthroughManager()

	assert.NotPanics(t, func() {
		mgr.Reset("anything")
	})

	// State remains Closed after Reset.
	assert.Equal(t, StateClosed, mgr.GetState("anything"))
}

func TestPassthroughManager_RegisterStateChangeListener_Noop(t *testing.T) {
	t.Parallel()

	mgr := NewPassthroughManager()

	// Register a real listener — it should never fire because no state ever changes.
	called := atomic.Bool{}
	mgr.RegisterStateChangeListener(&mockStateChangeListener{
		onStateChangeFn: func(string, State, State) {
			called.Store(true)
		},
	})

	// Drive every passthrough method that *could* conceivably trigger a
	// transition in a real manager.
	_, err := mgr.GetOrCreate("svc", DefaultConfig())
	require.NoError(t, err)
	_, err = mgr.Execute("svc", func() (any, error) { return nil, errors.New("fail") })
	require.Error(t, err)
	mgr.Reset("svc")

	assert.False(t, called.Load(), "passthrough manager must never invoke registered listeners")

	// Nil listener registration also must not panic.
	assert.NotPanics(t, func() {
		mgr.RegisterStateChangeListener(nil)
	})
}

// -----------------------------------------------------------------------------
// CircuitBreaker handle returned by GetOrCreate
// -----------------------------------------------------------------------------

func TestPassthroughManager_GetOrCreate_ReturnsPassthroughBreaker(t *testing.T) {
	t.Parallel()

	mgr := NewPassthroughManager()

	cb, err := mgr.GetOrCreate("svc", DefaultConfig())
	require.NoError(t, err)
	require.NotNil(t, cb)

	// Per-breaker Execute mirrors manager-level semantics.
	calls := 0
	result, err := cb.Execute(func() (any, error) {
		calls++
		return 42, nil
	})
	require.NoError(t, err)
	assert.Equal(t, 42, result)
	assert.Equal(t, 1, calls)

	// nil callback returns ErrNilCallback.
	_, err = cb.Execute(nil)
	assert.ErrorIs(t, err, ErrNilCallback)

	assert.Equal(t, StateClosed, cb.State())
	assert.Equal(t, Counts{}, cb.Counts())
}

func TestPassthroughManager_GetOrCreate_ValidatesConfig(t *testing.T) {
	t.Parallel()

	mgr := NewPassthroughManager()

	invalid := Config{
		ConsecutiveFailures: 0,
		MinRequests:         0,
	}

	cb, err := mgr.GetOrCreate("svc", invalid)
	assert.Nil(t, cb)
	assert.ErrorIs(t, err, ErrInvalidConfig)
}

// -----------------------------------------------------------------------------
// TenantAwareManager-level Execute semantics
// -----------------------------------------------------------------------------

func TestPassthroughManager_ExecuteForTenant_InvokesFnExactlyOnce(t *testing.T) {
	t.Parallel()

	mgr := NewPassthroughManager().(TenantAwareManager)

	calls := 0
	result, err := mgr.ExecuteForTenant(context.Background(), "tenant-A", "jd", func() (any, error) {
		calls++
		return "passthrough", nil
	})

	require.NoError(t, err)
	assert.Equal(t, "passthrough", result)
	assert.Equal(t, 1, calls)
}

func TestPassthroughManager_ExecuteForTenant_NilCallback(t *testing.T) {
	t.Parallel()

	mgr := NewPassthroughManager().(TenantAwareManager)

	_, err := mgr.ExecuteForTenant(context.Background(), "tenant-A", "jd", nil)
	assert.ErrorIs(t, err, ErrNilCallback)
}

func TestPassthroughManager_ExecuteForTenant_NilCallbackPrecedesIdentityValidation(t *testing.T) {
	t.Parallel()

	mgr := NewPassthroughManager().(TenantAwareManager)

	_, err := mgr.ExecuteForTenant(context.Background(), "", "", nil)
	assert.ErrorIs(t, err, ErrNilCallback)
}

func TestPassthroughManager_TenantConstantsForValidIdentities(t *testing.T) {
	t.Parallel()

	mgr := NewPassthroughManager().(TenantAwareManager)

	for _, tenant := range []string{"tenant-A", "tenant-B"} {
		for _, svc := range []string{"jd", "midaz", "streaming"} {
			assert.Equal(t, StateClosed, mgr.GetStateForTenant(tenant, svc))
			assert.Equal(t, Counts{}, mgr.GetCountsForTenant(tenant, svc))
			assert.True(t, mgr.IsHealthyForTenant(tenant, svc))
		}
	}
}

func TestPassthroughManager_InvalidTenantIdentityMatchesRealManager(t *testing.T) {
	t.Parallel()

	mgr := NewPassthroughManager().(TenantAwareManager)
	called := atomic.Bool{}

	result, err := mgr.ExecuteForTenant(context.Background(), "", "svc", func() (any, error) {
		called.Store(true)
		return "must-not-run", nil
	})

	assert.Nil(t, result)
	assert.ErrorIs(t, err, ErrInvalidTenantID)
	assert.False(t, called.Load(), "invalid tenant must not execute passthrough callback")
	assert.Equal(t, StateUnknown, mgr.GetStateForTenant("", "svc"))
	assert.Equal(t, Counts{}, mgr.GetCountsForTenant("", "svc"))
	assert.False(t, mgr.IsHealthyForTenant("", "svc"))
}

func TestPassthroughManager_TenantNoops(t *testing.T) {
	t.Parallel()

	mgr := NewPassthroughManager().(TenantAwareManager)

	assert.NotPanics(t, func() {
		mgr.ResetForTenant("tenant-A", "jd")
		mgr.ResetTenant("tenant-A")
		mgr.RemoveTenant("tenant-A")
	})

	// State is still Closed after all the no-ops.
	assert.Equal(t, StateClosed, mgr.GetStateForTenant("tenant-A", "jd"))
}

func TestPassthroughManager_Inventory_EmptyButNonNil(t *testing.T) {
	t.Parallel()

	mgr := NewPassthroughManager().(TenantAwareManager)

	// Even after "creating" several breakers, inventory remains empty —
	// the passthrough manager carries no state to inventory.
	for _, tenant := range []string{"tenant-A", "tenant-B"} {
		for _, svc := range []string{"jd", "midaz"} {
			_, err := mgr.GetOrCreateForTenant(tenant, svc, DefaultConfig())
			require.NoError(t, err)
		}
	}

	inv := mgr.Inventory()
	assert.NotNil(t, inv, "Inventory must return a non-nil slice")
	assert.Empty(t, inv)
}

func TestPassthroughManager_RegisterTenantStateChangeListener_Noop(t *testing.T) {
	t.Parallel()

	mgr := NewPassthroughManager().(TenantAwareManager)

	listenerDone := make(chan struct{})
	mgr.RegisterTenantStateChangeListener(&captureTenantListener{
		done: listenerDone,
		want: 1, // would close the channel on first fire
	})

	// Drive the methods that fire listeners on a real manager.
	_, err := mgr.GetOrCreateForTenant("tenant-A", "jd", DefaultConfig())
	require.NoError(t, err)
	_, err = mgr.ExecuteForTenant(context.Background(), "tenant-A", "jd", func() (any, error) {
		return nil, errors.New("fail")
	})
	require.Error(t, err)
	mgr.ResetForTenant("tenant-A", "jd")
	mgr.ResetTenant("tenant-A")
	mgr.RemoveTenant("tenant-A")

	select {
	case <-listenerDone:
		t.Fatal("passthrough manager must not invoke tenant state change listeners")
	default:
	}

	// Nil listener registration must not panic.
	assert.NotPanics(t, func() {
		mgr.RegisterTenantStateChangeListener(nil)
	})
}

// -----------------------------------------------------------------------------
// Concurrency: N goroutines calling Execute concurrently — each fn fires once
// -----------------------------------------------------------------------------

func TestPassthroughManager_Concurrent_Execute(t *testing.T) {
	t.Parallel()

	mgr := NewPassthroughManager()

	const goroutines = 64
	const iterations = 100

	// Per-goroutine counters guarantee that each invocation's fn fired
	// exactly once — a shared counter cannot distinguish "all callbacks
	// fired once" from "one callback fired N times" the way per-goroutine
	// counters can.
	counters := make([]atomic.Int64, goroutines)

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		idx := i
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_, err := mgr.Execute("svc", func() (any, error) {
					counters[idx].Add(1)
					return idx, nil
				})
				if err != nil {
					t.Errorf("passthrough Execute returned unexpected error: %v", err)
				}
			}
		}()
	}

	wg.Wait()

	for i := 0; i < goroutines; i++ {
		assert.Equal(t, int64(iterations), counters[i].Load(),
			"goroutine %d's fn must have fired exactly %d times", i, iterations)
	}
}

func TestPassthroughManager_Concurrent_ExecuteForTenant(t *testing.T) {
	t.Parallel()

	mgr := NewPassthroughManager().(TenantAwareManager)

	const goroutines = 64
	const iterations = 100

	counters := make([]atomic.Int64, goroutines)

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		idx := i
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_, err := mgr.ExecuteForTenant(context.Background(), "tenant-A", "svc", func() (any, error) {
					counters[idx].Add(1)
					return idx, nil
				})
				if err != nil {
					t.Errorf("passthrough ExecuteForTenant returned unexpected error: %v", err)
				}
			}
		}()
	}

	wg.Wait()

	for i := 0; i < goroutines; i++ {
		assert.Equal(t, int64(iterations), counters[i].Load())
	}
}
