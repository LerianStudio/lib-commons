//go:build unit

package circuitbreaker

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
)

type circuitLogEntry struct {
	msg    string
	fields []log.Field
}

type captureCircuitLogger struct {
	mu      sync.Mutex
	entries []circuitLogEntry
}

func (l *captureCircuitLogger) Log(_ context.Context, _ log.Level, msg string, fields ...log.Field) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.entries = append(l.entries, circuitLogEntry{msg: msg, fields: append([]log.Field(nil), fields...)})
}

func (l *captureCircuitLogger) With(...log.Field) log.Logger { return l }
func (l *captureCircuitLogger) WithGroup(string) log.Logger  { return l }
func (l *captureCircuitLogger) Enabled(log.Level) bool       { return true }
func (l *captureCircuitLogger) Sync(context.Context) error   { return nil }

func (l *captureCircuitLogger) snapshot() []circuitLogEntry {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]circuitLogEntry(nil), l.entries...)
}

// -----------------------------------------------------------------------------
// Tenant-aware Manager: interface satisfaction
// -----------------------------------------------------------------------------

func TestNewManager_SatisfiesTenantAwareManager(t *testing.T) {
	t.Parallel()

	mgr, err := NewManager(&log.NopLogger{})
	require.NoError(t, err)

	// The returned Manager must always be assertable to TenantAwareManager;
	// callers rely on this to access the *ForTenant methods.
	tam, ok := mgr.(TenantAwareManager)
	require.True(t, ok, "NewManager must return a value that satisfies TenantAwareManager")
	require.NotNil(t, tam)
}

// -----------------------------------------------------------------------------
// Tenant-aware Manager: storage isolation
// -----------------------------------------------------------------------------

func TestTenantManager_NoTenantAndTenantAreDistinct(t *testing.T) {
	t.Parallel()

	mgr, err := NewManager(&log.NopLogger{})
	require.NoError(t, err)
	tam := mgr.(TenantAwareManager)

	cfg := DefaultConfig()

	// Same service name, process-wide vs tenant-scoped — must be distinct breakers.
	noTenantCB, err := mgr.GetOrCreate("jd", cfg)
	require.NoError(t, err)
	require.NotNil(t, noTenantCB)

	tenantACB, err := tam.GetOrCreateForTenant("tenant-A", "jd", cfg)
	require.NoError(t, err)
	require.NotNil(t, tenantACB)

	// The CircuitBreaker handles must wrap different underlying breakers.
	// We assert via the manager's view: the inventory must list both pairs.
	inv := tam.Inventory()
	require.Len(t, inv, 2)

	keys := map[TenantBreakerKey]struct{}{}
	for _, k := range inv {
		keys[k] = struct{}{}
	}

	_, hasNoTenant := keys[TenantBreakerKey{TenantID: "", ServiceName: "jd"}]
	_, hasTenantA := keys[TenantBreakerKey{TenantID: "tenant-A", ServiceName: "jd"}]
	assert.True(t, hasNoTenant, "inventory should include the no-tenant breaker")
	assert.True(t, hasTenantA, "inventory should include the tenant-A breaker")

	// Single-tenant shim must observe the no-tenant breaker, NOT tenant-A's.
	assert.Equal(t, StateClosed, mgr.GetState("jd"))
	assert.Equal(t, StateUnknown, tam.GetStateForTenant("", "jd"),
		"tenant-aware APIs reject an empty tenant ID; use Manager methods for process-wide breakers")
	assert.Equal(t, StateClosed, tam.GetStateForTenant("tenant-A", "jd"))

	// Querying a non-existent tenant returns Unknown.
	assert.Equal(t, StateUnknown, tam.GetStateForTenant("tenant-Z", "jd"))
}

// -----------------------------------------------------------------------------
// Tenant-aware Manager: tenant-A trip does not affect tenant-B
// -----------------------------------------------------------------------------

func TestTenantManager_TenantTripDoesNotAffectNeighbor(t *testing.T) {
	t.Parallel()

	mgr, err := NewManager(&log.NopLogger{})
	require.NoError(t, err)
	tam := mgr.(TenantAwareManager)

	cfg := Config{
		MaxRequests:         1,
		Interval:            100 * time.Millisecond,
		Timeout:             1 * time.Second,
		ConsecutiveFailures: 2,
		FailureRatio:        0.5,
		MinRequests:         2,
	}

	_, err = tam.GetOrCreateForTenant("tenant-A", "jd", cfg)
	require.NoError(t, err)

	_, err = tam.GetOrCreateForTenant("tenant-B", "jd", cfg)
	require.NoError(t, err)

	// Trip tenant-A's breaker with consecutive failures.
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		_, err = tam.ExecuteForTenant(ctx, "tenant-A", "jd", func() (any, error) {
			return nil, errors.New("tenant-A failure")
		})
		require.Error(t, err)
	}

	require.Equal(t, StateOpen, tam.GetStateForTenant("tenant-A", "jd"),
		"tenant-A's breaker must be open after consecutive failures")

	// Tenant-B's breaker MUST remain closed and healthy.
	assert.Equal(t, StateClosed, tam.GetStateForTenant("tenant-B", "jd"))
	assert.True(t, tam.IsHealthyForTenant("tenant-B", "jd"))

	// Tenant-B's execution path is unaffected.
	result, err := tam.ExecuteForTenant(ctx, "tenant-B", "jd", func() (any, error) {
		return "ok", nil
	})
	require.NoError(t, err)
	assert.Equal(t, "ok", result)

	// Tenant-A's breaker still rejects fast.
	_, err = tam.ExecuteForTenant(ctx, "tenant-A", "jd", func() (any, error) {
		return "should-not-execute", nil
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "currently unavailable")
}

// -----------------------------------------------------------------------------
// Tenant-aware Manager: ResetForTenant only resets the targeted pair
// -----------------------------------------------------------------------------

func TestTenantManager_ResetForTenant_Scoped(t *testing.T) {
	t.Parallel()

	mgr, err := NewManager(&log.NopLogger{})
	require.NoError(t, err)
	tam := mgr.(TenantAwareManager)

	cfg := Config{
		MaxRequests:         1,
		Interval:            100 * time.Millisecond,
		Timeout:             5 * time.Second,
		ConsecutiveFailures: 2,
		FailureRatio:        0.5,
		MinRequests:         2,
	}

	for _, tenant := range []string{"tenant-A", "tenant-B"} {
		_, err := tam.GetOrCreateForTenant(tenant, "jd", cfg)
		require.NoError(t, err)

		// Trip each tenant's breaker.
		for i := 0; i < 3; i++ {
			_, err = tam.ExecuteForTenant(context.Background(), tenant, "jd", func() (any, error) {
				return nil, errors.New("fail")
			})
			require.Error(t, err)
		}

		require.Equal(t, StateOpen, tam.GetStateForTenant(tenant, "jd"))
	}

	// Reset only tenant-A's breaker.
	tam.ResetForTenant("tenant-A", "jd")

	assert.Equal(t, StateClosed, tam.GetStateForTenant("tenant-A", "jd"),
		"ResetForTenant must move the targeted breaker back to Closed")
	assert.Equal(t, StateOpen, tam.GetStateForTenant("tenant-B", "jd"),
		"ResetForTenant must NOT touch other tenants' breakers")
}

// -----------------------------------------------------------------------------
// Tenant-aware Manager: ResetTenant resets every breaker for one tenant
// -----------------------------------------------------------------------------

func TestTenantManager_ResetTenant_AllBreakers(t *testing.T) {
	t.Parallel()

	mgr, err := NewManager(&log.NopLogger{})
	require.NoError(t, err)
	tam := mgr.(TenantAwareManager)

	cfg := Config{
		MaxRequests:         1,
		Interval:            100 * time.Millisecond,
		Timeout:             5 * time.Second,
		ConsecutiveFailures: 2,
		FailureRatio:        0.5,
		MinRequests:         2,
	}

	tripBreaker := func(tenant, service string) {
		_, err := tam.GetOrCreateForTenant(tenant, service, cfg)
		require.NoError(t, err)

		for i := 0; i < 3; i++ {
			_, err = tam.ExecuteForTenant(context.Background(), tenant, service, func() (any, error) {
				return nil, errors.New("fail")
			})
			require.Error(t, err)
		}
	}

	tripBreaker("tenant-A", "jd")
	tripBreaker("tenant-A", "midaz")
	tripBreaker("tenant-B", "jd")

	require.Equal(t, StateOpen, tam.GetStateForTenant("tenant-A", "jd"))
	require.Equal(t, StateOpen, tam.GetStateForTenant("tenant-A", "midaz"))
	require.Equal(t, StateOpen, tam.GetStateForTenant("tenant-B", "jd"))

	tam.ResetTenant("tenant-A")

	assert.Equal(t, StateClosed, tam.GetStateForTenant("tenant-A", "jd"),
		"ResetTenant must reset every breaker for the targeted tenant")
	assert.Equal(t, StateClosed, tam.GetStateForTenant("tenant-A", "midaz"),
		"ResetTenant must reset every breaker for the targeted tenant")
	assert.Equal(t, StateOpen, tam.GetStateForTenant("tenant-B", "jd"),
		"ResetTenant must not affect other tenants")

	// ResetTenant for an unknown tenant is a no-op.
	assert.NotPanics(t, func() {
		tam.ResetTenant("tenant-Z")
	})
}

// -----------------------------------------------------------------------------
// Tenant-aware Manager: RemoveTenant
// -----------------------------------------------------------------------------

func TestTenantManager_RemoveTenant(t *testing.T) {
	t.Parallel()

	mgr, err := NewManager(&log.NopLogger{})
	require.NoError(t, err)
	tam := mgr.(TenantAwareManager)

	cfg := DefaultConfig()

	_, err = tam.GetOrCreateForTenant("tenant-A", "jd", cfg)
	require.NoError(t, err)
	_, err = tam.GetOrCreateForTenant("tenant-A", "midaz", cfg)
	require.NoError(t, err)
	_, err = tam.GetOrCreateForTenant("tenant-B", "jd", cfg)
	require.NoError(t, err)

	require.Len(t, tam.Inventory(), 3)

	tam.RemoveTenant("tenant-A")

	inv := tam.Inventory()
	require.Len(t, inv, 1)
	assert.Equal(t, "tenant-B", inv[0].TenantID)
	assert.Equal(t, "jd", inv[0].ServiceName)

	// After removal, the tenant-A breakers no longer exist.
	assert.Equal(t, StateUnknown, tam.GetStateForTenant("tenant-A", "jd"))
	assert.Equal(t, StateUnknown, tam.GetStateForTenant("tenant-A", "midaz"))

	// No-op for an unknown tenant.
	assert.NotPanics(t, func() {
		tam.RemoveTenant("tenant-Z")
	})

	// No-op for tenant-A again.
	tam.RemoveTenant("tenant-A")
	assert.Len(t, tam.Inventory(), 1)
}

// -----------------------------------------------------------------------------
// Tenant-aware Manager: Inventory snapshot is decoupled from internal state
// -----------------------------------------------------------------------------

func TestTenantManager_Inventory_IsSnapshot(t *testing.T) {
	t.Parallel()

	mgr, err := NewManager(&log.NopLogger{})
	require.NoError(t, err)
	tam := mgr.(TenantAwareManager)

	cfg := DefaultConfig()

	_, err = tam.GetOrCreateForTenant("tenant-A", "jd", cfg)
	require.NoError(t, err)

	snap := tam.Inventory()
	require.Len(t, snap, 1)

	// Mutating the snapshot must not affect the manager's view.
	snap[0] = TenantBreakerKey{TenantID: "rewritten", ServiceName: "evil"}

	fresh := tam.Inventory()
	require.Len(t, fresh, 1)
	assert.Equal(t, "tenant-A", fresh[0].TenantID)
	assert.Equal(t, "jd", fresh[0].ServiceName)
}

// -----------------------------------------------------------------------------
// Tenant-aware Manager: race — RemoveTenant during Execute for a sibling
// -----------------------------------------------------------------------------

// TestTenantManager_RemoveTenant_RaceWithExecute is gated under `go test -race`
// in CI. It races RemoveTenant("tenant-A") against ExecuteForTenant on
// tenant-B's breaker; neither must panic, and tenant-B's execution must
// still complete because its breaker is untouched.
func TestTenantManager_RemoveTenant_RaceWithExecute(t *testing.T) {
	t.Parallel()

	mgr, err := NewManager(&log.NopLogger{})
	require.NoError(t, err)
	tam := mgr.(TenantAwareManager)

	cfg := DefaultConfig()

	_, err = tam.GetOrCreateForTenant("tenant-A", "jd", cfg)
	require.NoError(t, err)
	_, err = tam.GetOrCreateForTenant("tenant-B", "jd", cfg)
	require.NoError(t, err)

	const iterations = 200

	var wg sync.WaitGroup
	wg.Add(2)

	executed := atomic.Int64{}
	recreateErrs := make(chan error, iterations)

	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_, err := tam.ExecuteForTenant(context.Background(), "tenant-B", "jd", func() (any, error) {
				return "ok", nil
			})
			if err == nil {
				executed.Add(1)
			}
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			tam.RemoveTenant("tenant-A")
			// Re-create so the removal loop has something to remove each
			// iteration; mirrors a tenant flap.
			_, err := tam.GetOrCreateForTenant("tenant-A", "jd", cfg)
			if err != nil {
				recreateErrs <- err
			}
		}
	}()

	wg.Wait()
	close(recreateErrs)
	for err := range recreateErrs {
		assert.NoError(t, err)
	}

	// Every tenant-B execution should have succeeded; the breaker is
	// closed and isolated from tenant-A removals.
	assert.Equal(t, int64(iterations), executed.Load(),
		"tenant-B execution must not be affected by tenant-A removals")
}

// -----------------------------------------------------------------------------
// Tenant-aware Manager: TenantStateChangeListener fires for every transition
// -----------------------------------------------------------------------------

type captureTenantListener struct {
	mu     sync.Mutex
	events []capturedTenantEvent
	done   chan struct{}
	want   int
}

type capturedTenantEvent struct {
	tenantID    string
	serviceName string
	from        State
	to          State
}

func newCaptureTenantListener(want int) *captureTenantListener {
	return &captureTenantListener{
		done: make(chan struct{}),
		want: want,
	}
}

func (l *captureTenantListener) OnTenantStateChange(_ context.Context, tenantID, serviceName string, from State, to State) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.events = append(l.events, capturedTenantEvent{
		tenantID:    tenantID,
		serviceName: serviceName,
		from:        from,
		to:          to,
	})

	if len(l.events) >= l.want {
		select {
		case <-l.done:
		default:
			close(l.done)
		}
	}
}

func (l *captureTenantListener) snapshot() []capturedTenantEvent {
	l.mu.Lock()
	defer l.mu.Unlock()

	out := make([]capturedTenantEvent, len(l.events))
	copy(out, l.events)

	return out
}

func TestTenantManager_TenantListener_FiresForEveryTransition(t *testing.T) {
	t.Parallel()

	mgr, err := NewManager(&log.NopLogger{})
	require.NoError(t, err)
	tam := mgr.(TenantAwareManager)

	listener := newCaptureTenantListener(2) // expect tenant-A trip + tenant-B trip
	tam.RegisterTenantStateChangeListener(listener)

	cfg := Config{
		MaxRequests:         1,
		Interval:            100 * time.Millisecond,
		Timeout:             5 * time.Second,
		ConsecutiveFailures: 2,
		FailureRatio:        0.5,
		MinRequests:         2,
	}

	for _, tenant := range []string{"tenant-A", "tenant-B"} {
		_, err := tam.GetOrCreateForTenant(tenant, "jd", cfg)
		require.NoError(t, err)

		for i := 0; i < 3; i++ {
			_, err = tam.ExecuteForTenant(context.Background(), tenant, "jd", func() (any, error) {
				return nil, errors.New("fail")
			})
			require.Error(t, err)
		}
	}

	select {
	case <-listener.done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for tenant state-change events")
	}

	events := listener.snapshot()
	require.GreaterOrEqual(t, len(events), 2)

	tenants := map[string]struct{}{}
	for _, e := range events {
		tenants[e.tenantID] = struct{}{}
		assert.Equal(t, "jd", e.serviceName)
		assert.Equal(t, StateClosed, e.from)
		assert.Equal(t, StateOpen, e.to)
	}

	_, hasA := tenants["tenant-A"]
	_, hasB := tenants["tenant-B"]
	assert.True(t, hasA, "tenant-A transition must have fired")
	assert.True(t, hasB, "tenant-B transition must have fired")
}

func TestTenantManager_TenantListener_FiresForNoTenantTransition(t *testing.T) {
	t.Parallel()

	mgr, err := NewManager(&log.NopLogger{})
	require.NoError(t, err)
	tam := mgr.(TenantAwareManager)

	listener := newCaptureTenantListener(1)
	tam.RegisterTenantStateChangeListener(listener)

	cfg := Config{
		MaxRequests:         1,
		Interval:            100 * time.Millisecond,
		Timeout:             5 * time.Second,
		ConsecutiveFailures: 2,
		FailureRatio:        0.5,
		MinRequests:         2,
	}

	_, err = mgr.GetOrCreate("jd", cfg)
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		_, err = mgr.Execute("jd", func() (any, error) {
			return nil, errors.New("fail")
		})
		require.Error(t, err)
	}

	select {
	case <-listener.done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for no-tenant state-change event")
	}

	events := listener.snapshot()
	require.NotEmpty(t, events)
	assert.Equal(t, "", events[0].tenantID)
	assert.Equal(t, "jd", events[0].serviceName)
	assert.Equal(t, StateClosed, events[0].from)
	assert.Equal(t, StateOpen, events[0].to)
}

// -----------------------------------------------------------------------------
// Tenant-aware Manager: nil-tenant-listener registration is rejected silently
// -----------------------------------------------------------------------------

func TestTenantManager_NilTenantListenerRegistration(t *testing.T) {
	t.Parallel()

	mgr, err := NewManager(&log.NopLogger{})
	require.NoError(t, err)
	tam := mgr.(TenantAwareManager)

	assert.NotPanics(t, func() {
		tam.RegisterTenantStateChangeListener(nil)
	})

	// Typed nil should also be rejected without panic.
	var typedNil TenantStateChangeListener
	assert.NotPanics(t, func() {
		tam.RegisterTenantStateChangeListener(typedNil)
	})
}

// -----------------------------------------------------------------------------
// Tenant-aware Manager: legacy listener only fires for no-tenant transitions
// -----------------------------------------------------------------------------

type captureLegacyListener struct {
	mu     sync.Mutex
	events []capturedTenantEvent // re-uses the tenant event shape; tenantID will be ""
}

func (l *captureLegacyListener) OnStateChange(_ context.Context, serviceName string, from State, to State) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.events = append(l.events, capturedTenantEvent{
		serviceName: serviceName,
		from:        from,
		to:          to,
	})
}

func (l *captureLegacyListener) snapshot() []capturedTenantEvent {
	l.mu.Lock()
	defer l.mu.Unlock()

	out := make([]capturedTenantEvent, len(l.events))
	copy(out, l.events)

	return out
}

func TestTenantManager_LegacyListener_OnlyFiresForNoTenant(t *testing.T) {
	t.Parallel()

	mgr, err := NewManager(&log.NopLogger{})
	require.NoError(t, err)
	tam := mgr.(TenantAwareManager)

	legacy := &captureLegacyListener{}
	mgr.RegisterStateChangeListener(legacy)

	cfg := Config{
		MaxRequests:         1,
		Interval:            100 * time.Millisecond,
		Timeout:             5 * time.Second,
		ConsecutiveFailures: 2,
		FailureRatio:        0.5,
		MinRequests:         2,
	}

	// Tenant-A trip — legacy listener MUST NOT see this.
	_, err = tam.GetOrCreateForTenant("tenant-A", "jd", cfg)
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		_, err = tam.ExecuteForTenant(context.Background(), "tenant-A", "jd", func() (any, error) {
			return nil, errors.New("fail")
		})
		require.Error(t, err)
	}

	// No-tenant trip — legacy listener MUST see this.
	_, err = mgr.GetOrCreate("midaz", cfg)
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		_, err = mgr.Execute("midaz", func() (any, error) {
			return nil, errors.New("fail")
		})
		require.Error(t, err)
	}

	// Wait for async listener delivery.
	require.Eventually(t, func() bool {
		return len(legacy.snapshot()) >= 1
	}, 2*time.Second, 25*time.Millisecond)

	events := legacy.snapshot()
	for _, e := range events {
		assert.Equal(t, "midaz", e.serviceName,
			"legacy listener must NOT see tenant-A transitions; saw service=%s", e.serviceName)
	}
}

// -----------------------------------------------------------------------------
// Tenant-aware Manager: ExecuteForTenant nil callback
// -----------------------------------------------------------------------------

func TestTenantManager_ExecuteForTenant_NilCallback(t *testing.T) {
	t.Parallel()

	mgr, err := NewManager(&log.NopLogger{})
	require.NoError(t, err)
	tam := mgr.(TenantAwareManager)

	result, err := tam.ExecuteForTenant(context.Background(), "tenant-A", "jd", nil)
	assert.Nil(t, result)
	assert.ErrorIs(t, err, ErrNilCallback)
}

// -----------------------------------------------------------------------------
// Tenant-aware Manager: GetOrCreateForTenant config mismatch
// -----------------------------------------------------------------------------

func TestTenantManager_GetOrCreateForTenant_ConfigMismatch(t *testing.T) {
	t.Parallel()

	mgr, err := NewManager(&log.NopLogger{})
	require.NoError(t, err)
	tam := mgr.(TenantAwareManager)

	cfgA := DefaultConfig()
	cfgB := AggressiveConfig()

	_, err = tam.GetOrCreateForTenant("tenant-A", "jd", cfgA)
	require.NoError(t, err)

	// Same (tenant, service) with a different Config — must error.
	cb, err := tam.GetOrCreateForTenant("tenant-A", "jd", cfgB)
	assert.Nil(t, cb)
	assert.ErrorIs(t, err, ErrConfigMismatch)

	// A different tenant with the divergent Config must succeed — the
	// uniqueness boundary is (tenantID, serviceName).
	cb, err = tam.GetOrCreateForTenant("tenant-B", "jd", cfgB)
	require.NoError(t, err)
	require.NotNil(t, cb)
}

func TestTenantManager_GetOrCreateForTenant_InvalidIdentity(t *testing.T) {
	t.Parallel()

	mgr, err := NewManager(&log.NopLogger{})
	require.NoError(t, err)
	tam := mgr.(TenantAwareManager)

	tests := []struct {
		name        string
		tenantID    string
		serviceName string
		wantErr     error
	}{
		{name: "empty tenant", tenantID: "", serviceName: "svc", wantErr: ErrInvalidTenantID},
		{name: "tenant contains colon", tenantID: "tenant:svc", serviceName: "name", wantErr: ErrInvalidTenantID},
		{name: "empty service", tenantID: "tenant-A", serviceName: "", wantErr: ErrInvalidServiceName},
		{name: "service contains nul", tenantID: "tenant-A", serviceName: "svc\x00tail", wantErr: ErrInvalidServiceName},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cb, err := tam.GetOrCreateForTenant(tt.tenantID, tt.serviceName, DefaultConfig())
			assert.Nil(t, cb)
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestManager_GetOrCreate_LegacyAllowsEmptyTenantButRejectsControlServiceName(t *testing.T) {
	t.Parallel()

	mgr, err := NewManager(&log.NopLogger{})
	require.NoError(t, err)

	cb, err := mgr.GetOrCreate("svc", DefaultConfig())
	require.NoError(t, err)
	require.NotNil(t, cb)

	cb, err = mgr.GetOrCreate("svc\x00tail", DefaultConfig())
	assert.Nil(t, cb)
	assert.ErrorIs(t, err, ErrInvalidServiceName)
}

func TestTenantManager_ObservabilityUsesTenantHash(t *testing.T) {
	t.Parallel()

	const rawTenantID = "tenant-sensitive-123"
	logger := &captureCircuitLogger{}
	mgr, err := NewManager(logger)
	require.NoError(t, err)
	tam := mgr.(TenantAwareManager)

	_, err = tam.GetOrCreateForTenant(rawTenantID, "svc", DefaultConfig())
	require.NoError(t, err)

	_, err = tam.GetOrCreateForTenant(rawTenantID, "svc", AggressiveConfig())
	require.ErrorIs(t, err, ErrConfigMismatch)
	assert.NotContains(t, err.Error(), rawTenantID, "returned errors must not expose raw tenant IDs")
	assert.Contains(t, err.Error(), tenantHashLabel(rawTenantID))

	foundHash := false
	for _, entry := range logger.snapshot() {
		assert.NotContains(t, entry.msg, rawTenantID)
		for _, field := range entry.fields {
			assert.NotEqual(t, "tenant_id", field.Key)
			assert.NotEqual(t, rawTenantID, field.Value)
			assert.False(t, strings.Contains(field.Key, rawTenantID))
			if field.Key == "tenant_hash" {
				foundHash = true
				assert.Equal(t, tenantHashLabel(rawTenantID), field.Value)
			}
		}
	}

	assert.True(t, foundHash, "manager logs must include tenant_hash")
}

// -----------------------------------------------------------------------------
// Metrics: tenant_hash label appears only for tenant-aware counters
// -----------------------------------------------------------------------------

func TestTenantManager_Metrics_TenantHashLabel(t *testing.T) {
	t.Parallel()

	factory, reader := newTestMetricsFactory(t)

	mgr, err := NewManager(&log.NopLogger{}, WithMetricsFactory(factory))
	require.NoError(t, err)
	tam := mgr.(TenantAwareManager)

	cfg := Config{
		MaxRequests:         1,
		Interval:            100 * time.Millisecond,
		Timeout:             5 * time.Second,
		ConsecutiveFailures: 2,
		FailureRatio:        0.5,
		MinRequests:         2,
	}

	// Tenant-A — successful execution + trip.
	_, err = tam.GetOrCreateForTenant("tenant-A", "jd", cfg)
	require.NoError(t, err)

	_, err = tam.ExecuteForTenant(context.Background(), "tenant-A", "jd", func() (any, error) {
		return "ok", nil
	})
	require.NoError(t, err)

	for i := 0; i < 3; i++ {
		_, err = tam.ExecuteForTenant(context.Background(), "tenant-A", "jd", func() (any, error) {
			return nil, errors.New("fail")
		})
		require.Error(t, err)
	}

	// No-tenant successful execution to confirm the legacy label set is preserved.
	_, err = mgr.GetOrCreate("midaz", cfg)
	require.NoError(t, err)
	_, err = mgr.Execute("midaz", func() (any, error) {
		return "ok", nil
	})
	require.NoError(t, err)

	rm := collectMetrics(t, reader)

	execMetric := findMetricByName(rm, "circuit_breaker_executions_total")
	require.NotNil(t, execMetric)

	dps := sumDataPoints(t, execMetric)

	foundTenantA := false
	foundNoTenant := false
	tenantAHash := tenantHashLabel("tenant-A")
	for _, dp := range dps {
		if hasAttributeValue(dp, "service", "jd") &&
			hasAttributeValue(dp, "tenant_hash", tenantAHash) &&
			hasAttributeValue(dp, "result", "success") {
			foundTenantA = true
		}

		if hasAttributeValue(dp, "service", "midaz") &&
			hasAttributeValue(dp, "result", "success") &&
			!hasAttributeKey(dp, "tenant_hash") {
			foundNoTenant = true
		}
	}

	assert.True(t, foundTenantA, "execution metric must include tenant_hash label")
	assert.True(t, foundNoTenant, "legacy no-tenant execution metric must not gain tenant_hash label")

	stateMetric := findMetricByName(rm, "circuit_breaker_state_transitions_total")
	require.NotNil(t, stateMetric)

	stateDPs := sumDataPoints(t, stateMetric)
	foundTenantATrip := false
	for _, dp := range stateDPs {
		if hasAttributeValue(dp, "service", "jd") &&
			hasAttributeValue(dp, "tenant_hash", tenantAHash) &&
			hasAttributeValue(dp, "to_state", string(StateOpen)) {
			foundTenantATrip = true
		}
	}
	assert.True(t, foundTenantATrip, "state-transition metric must carry tenant_hash for tenant-A's trip")
}

// -----------------------------------------------------------------------------
// Concurrency: simultaneous ("", "jd") and ("tenant-A", "jd") executions
// -----------------------------------------------------------------------------

func TestTenantManager_ConcurrentNoTenantAndTenant(t *testing.T) {
	t.Parallel()

	mgr, err := NewManager(&log.NopLogger{})
	require.NoError(t, err)
	tam := mgr.(TenantAwareManager)

	cfg := DefaultConfig()

	_, err = mgr.GetOrCreate("jd", cfg)
	require.NoError(t, err)
	_, err = tam.GetOrCreateForTenant("tenant-A", "jd", cfg)
	require.NoError(t, err)

	const goroutines = 32
	const iterations = 100

	var wg sync.WaitGroup
	wg.Add(2 * goroutines)

	noTenantCount := atomic.Int64{}
	tenantACount := atomic.Int64{}

	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_, err := mgr.Execute("jd", func() (any, error) {
					return "ok", nil
				})
				if err == nil {
					noTenantCount.Add(1)
				}
			}
		}()

		go func() {
			defer wg.Done()
			for j := 0; j < iterations; j++ {
				_, err := tam.ExecuteForTenant(context.Background(), "tenant-A", "jd", func() (any, error) {
					return "ok", nil
				})
				if err == nil {
					tenantACount.Add(1)
				}
			}
		}()
	}

	wg.Wait()

	assert.Equal(t, int64(goroutines*iterations), noTenantCount.Load())
	assert.Equal(t, int64(goroutines*iterations), tenantACount.Load())

	// Both breakers should remain closed since every call succeeded.
	assert.Equal(t, StateClosed, mgr.GetState("jd"))
	assert.Equal(t, StateClosed, tam.GetStateForTenant("tenant-A", "jd"))
}

// -----------------------------------------------------------------------------
// Metric attribute helper — ensures legacy no-tenant metrics keep their old label set
// -----------------------------------------------------------------------------

func TestTenantManager_Metrics_NoTenantOmitsTenantHashLabel(t *testing.T) {
	t.Parallel()
	// Legacy Manager users had metrics keyed only by service/result. Preserve
	// that label shape so existing dashboards and recording rules keep working.
	factory, reader := newTestMetricsFactory(t)

	mgr, err := NewManager(&log.NopLogger{}, WithMetricsFactory(factory))
	require.NoError(t, err)

	_, err = mgr.GetOrCreate("svc", DefaultConfig())
	require.NoError(t, err)
	_, err = mgr.Execute("svc", func() (any, error) { return "ok", nil })
	require.NoError(t, err)

	rm := collectMetrics(t, reader)
	em := findMetricByName(rm, "circuit_breaker_executions_total")
	require.NotNil(t, em)

	dps := sumDataPoints(t, em)
	foundLegacyShape := false
	for _, dp := range dps {
		if hasAttributeValue(dp, "service", "svc") &&
			hasAttributeValue(dp, "result", "success") &&
			!hasAttributeKey(dp, "tenant_hash") {
			foundLegacyShape = true
		}
	}
	assert.True(t, foundLegacyShape, "expected no tenant_hash attribute on no-tenant emission")
}

// Helper to silence unused-import linters in case the metrics test file
// hasn't been touched. sdkmetric is imported via the metrics_test helpers.
var _ = sdkmetric.NewManualReader
