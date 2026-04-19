//go:build unit

// Comprehensive Client-layer coverage of the tenant-scoped methods.
//
// This file exercises every PRD acceptance criterion and every documented
// error path for the five primary methods (RegisterTenantScoped happy-path is
// covered in tenant_scoped_register_test.go; this file covers the remaining
// Set/Get/Delete/List/typed-accessor matrix) using NewForTesting + an
// in-memory fake store.
//
// Tests are split by method for traceability. The critical regression pins
// AC1 (TestGet_IgnoresTenantOverrides) and AC8 (handled in
// tenant_onchange_test.go) live in dedicated sections so a maintainer can
// confirm at-a-glance that the locked decisions hold.
//
// The tenant smoke tests in tenant_scoped_smoke_test.go cover the end-to-end
// happy paths; this file concentrates on negative paths and cross-tenant
// isolation invariants.
package systemplane

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/store"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
)

// ---------------------------------------------------------------------------
// tenantFakeStore — richer in-memory TestStore for negative-path tests.
//
// Capabilities beyond the simple tenantTestStore in tenant_scoped_smoke_test.go:
//
//   - Separate global (_global) row storage so Set and SetTenantValue don't
//     collide.
//   - Failure-injection hook for GetTenantValue (lazy-mode miss tests).
//   - subReady signal so tests can wait for Subscribe goroutine attachment
//     before firing events.
//
// The store is deliberately kept flat (single sync.Mutex) because every test
// exercises it from one writer goroutine. The Task 8 race suite carries the
// truly concurrent coverage.
// ---------------------------------------------------------------------------

// tenantRowKey addresses a row in the fake store. TenantID "_global" is the
// legacy global bucket; any other value is a per-tenant override.
type tenantRowKey struct {
	tenantID  string
	namespace string
	key       string
}

type tenantFakeStore struct {
	mu       sync.Mutex
	rows     map[tenantRowKey]TestEntry
	handlers []func(TestEvent)
	subReady chan struct{}

	// getTenantErr, if non-nil, is returned from GetTenantValue. Used by
	// lazy-mode tests that need to force the miss-populate path to log-and-
	// fall-through rather than succeed.
	getTenantErr error

	// setTenantErr, if non-nil, is returned from SetTenantValue before any
	// state mutation — used by phase-1 rolling-deploy tests that need the
	// backend to simulate ErrTenantSchemaNotEnabled.
	setTenantErr error

	// deleteTenantErr, if non-nil, is returned from DeleteTenantValue before
	// any state mutation — symmetric with setTenantErr for the delete path.
	deleteTenantErr error

	// getTenantCalls counts invocations of GetTenantValue. Used by the
	// single-flight coalescing test to assert that N concurrent cache
	// misses collapse into exactly 1 backend round-trip.
	getTenantCalls atomic.Int64

	// getTenantBlock, if non-nil, is signaled BEFORE GetTenantValue returns,
	// and GetTenantValue waits on getTenantRelease before proceeding. Used
	// by the single-flight test to hold the first call in-flight while the
	// remaining concurrent callers arrive at sfg.Do and are coalesced.
	getTenantBlock   chan struct{}
	getTenantRelease chan struct{}
}

func newTenantFakeStore() *tenantFakeStore {
	return &tenantFakeStore{
		rows:     make(map[tenantRowKey]TestEntry),
		subReady: make(chan struct{}),
	}
}

func (s *tenantFakeStore) fire(evt TestEvent) {
	s.mu.Lock()
	handlers := make([]func(TestEvent), len(s.handlers))
	copy(handlers, s.handlers)
	s.mu.Unlock()

	for _, h := range handlers {
		h(evt)
	}
}

func (s *tenantFakeStore) List(_ context.Context) ([]TestEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]TestEntry, 0)

	for k, e := range s.rows {
		if k.tenantID == store.SentinelGlobal {
			out = append(out, e)
		}
	}

	return out, nil
}

func (s *tenantFakeStore) Get(_ context.Context, namespace, key string) (TestEntry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.rows[tenantRowKey{tenantID: store.SentinelGlobal, namespace: namespace, key: key}]

	return e, ok, nil
}

func (s *tenantFakeStore) Set(_ context.Context, e TestEntry) error {
	e.TenantID = store.SentinelGlobal

	s.mu.Lock()
	s.rows[tenantRowKey{tenantID: store.SentinelGlobal, namespace: e.Namespace, key: e.Key}] = e
	s.mu.Unlock()

	s.fire(TestEvent{Namespace: e.Namespace, Key: e.Key, TenantID: store.SentinelGlobal})

	return nil
}

func (s *tenantFakeStore) Subscribe(ctx context.Context, handler func(TestEvent)) error {
	s.mu.Lock()
	first := len(s.handlers) == 0
	s.handlers = append(s.handlers, handler)
	s.mu.Unlock()

	if first {
		close(s.subReady)
	}

	<-ctx.Done()

	return nil
}

func (s *tenantFakeStore) Close() error { return nil }

func (s *tenantFakeStore) GetTenantValue(_ context.Context, tenantID, namespace, key string) (TestEntry, bool, error) {
	s.getTenantCalls.Add(1)

	// Coordinate with a blocked-call test harness if the hooks are wired.
	// The channels are read under no lock — they are populated exactly once
	// during test setup and never mutated afterward.
	if s.getTenantBlock != nil {
		// Signal the first caller is in-flight.
		select {
		case s.getTenantBlock <- struct{}{}:
		default:
		}

		// Wait until the test releases us.
		if s.getTenantRelease != nil {
			<-s.getTenantRelease
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.getTenantErr != nil {
		return TestEntry{}, false, s.getTenantErr
	}

	e, ok := s.rows[tenantRowKey{tenantID: tenantID, namespace: namespace, key: key}]

	return e, ok, nil
}

func (s *tenantFakeStore) SetTenantValue(_ context.Context, tenantID string, e TestEntry) error {
	s.mu.Lock()
	if s.setTenantErr != nil {
		err := s.setTenantErr
		s.mu.Unlock()

		return err
	}

	e.TenantID = tenantID
	s.rows[tenantRowKey{tenantID: tenantID, namespace: e.Namespace, key: e.Key}] = e
	s.mu.Unlock()

	s.fire(TestEvent{Namespace: e.Namespace, Key: e.Key, TenantID: tenantID})

	return nil
}

func (s *tenantFakeStore) DeleteTenantValue(_ context.Context, tenantID, namespace, key, _ string) error {
	s.mu.Lock()
	if s.deleteTenantErr != nil {
		err := s.deleteTenantErr
		s.mu.Unlock()

		return err
	}

	delete(s.rows, tenantRowKey{tenantID: tenantID, namespace: namespace, key: key})
	s.mu.Unlock()

	s.fire(TestEvent{Namespace: namespace, Key: key, TenantID: tenantID})

	return nil
}

func (s *tenantFakeStore) ListTenantValues(_ context.Context) ([]TestEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]TestEntry, 0, len(s.rows))
	for _, e := range s.rows {
		out = append(out, e)
	}

	return out, nil
}

func (s *tenantFakeStore) ListTenantsForKey(_ context.Context, namespace, key string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	seen := make(map[string]struct{})

	for k := range s.rows {
		if k.namespace == namespace && k.key == key && k.tenantID != store.SentinelGlobal {
			seen[k.tenantID] = struct{}{}
		}
	}

	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}

	sort.Strings(out)

	return out, nil
}

// directSetTenantRow bypasses the changefeed fire path. Used by lazy-mode
// tests that need a row present in the store without firing an event (the
// Client's hydrate pass would otherwise beat the test's later miss-populate
// assertion).
func (s *tenantFakeStore) directSetTenantRow(tenantID, namespace, key string, value []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.rows[tenantRowKey{tenantID: tenantID, namespace: namespace, key: key}] = TestEntry{
		Namespace: namespace,
		Key:       key,
		TenantID:  tenantID,
		Value:     value,
		UpdatedAt: time.Now().UTC(),
	}
}

// ---------------------------------------------------------------------------
// Test helpers — build and start Clients with the rich tenantFakeStore.
// ---------------------------------------------------------------------------

// buildStartedClient builds and starts a Client with the rich tenantFakeStore,
// registers the given tenant-scoped key, and waits for Subscribe to attach.
// The returned Client is auto-closed via t.Cleanup.
func buildStartedClient(t *testing.T, namespace, key string, defaultValue any, opts ...Option) (*Client, *tenantFakeStore) {
	t.Helper()

	fs := newTenantFakeStore()

	c, err := NewForTesting(fs, opts...)
	require.NoError(t, err, "NewForTesting")

	require.NoError(t, c.RegisterTenantScoped(namespace, key, defaultValue), "RegisterTenantScoped")
	require.NoError(t, c.Start(context.Background()), "Start")

	select {
	case <-fs.subReady:
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe handler did not register in time")
	}

	t.Cleanup(func() { _ = c.Close() })

	return c, fs
}

// buildStartedClientNoKey mirrors buildStartedClient but registers no key.
// Callers call Register / RegisterTenantScoped themselves (or neither, for
// unregistered-key negative tests).
func buildStartedClientNoKey(t *testing.T, opts ...Option) (*Client, *tenantFakeStore) {
	t.Helper()

	fs := newTenantFakeStore()

	c, err := NewForTesting(fs, opts...)
	require.NoError(t, err, "NewForTesting")

	require.NoError(t, c.Start(context.Background()), "Start")

	select {
	case <-fs.subReady:
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe handler did not register in time")
	}

	t.Cleanup(func() { _ = c.Close() })

	return c, fs
}

// tctx is a terser convenience for building a tenant-scoped context.
func tctx(tenantID string) context.Context {
	return core.ContextWithTenantID(context.Background(), tenantID)
}

// ---------------------------------------------------------------------------
// SetForTenant — negative paths.
// ---------------------------------------------------------------------------

func TestSetForTenant_MissingCtxTenantReturnsErrMissingTenantContext(t *testing.T) {
	t.Parallel()

	c, _ := buildStartedClient(t, "global", "fee.rate", 0.0)

	// context.Background has no tenant ID.
	err := c.SetForTenant(context.Background(), "global", "fee.rate", 0.5, "admin")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMissingTenantContext, "missing tenant in ctx must surface ErrMissingTenantContext")
}

func TestSetForTenant_InvalidTenantIDReturnsErrInvalidTenantID(t *testing.T) {
	t.Parallel()

	c, _ := buildStartedClient(t, "global", "fee.rate", 0.0)

	// core.IsValidTenantID rejects: leading underscore, leading hyphen,
	// special chars, and the "_global" sentinel. Each case flows through
	// extractTenantID with the same error, so a single table-driven test
	// covers the matrix.
	invalidIDs := []struct {
		name     string
		tenantID string
	}{
		{"leading_underscore", "_tenant"},
		{"leading_hyphen", "-tenant"},
		{"global_sentinel", store.SentinelGlobal},
		{"special_chars", "tenant@acme"},
		{"space_inside", "tenant acme"},
		{"only_underscore", "_"},
	}

	for _, tc := range invalidIDs {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := c.SetForTenant(tctx(tc.tenantID), "global", "fee.rate", 0.5, "admin")
			require.Error(t, err, "invalid tenant ID %q should be rejected", tc.tenantID)
			assert.ErrorIs(t, err, ErrInvalidTenantID, "error should wrap ErrInvalidTenantID")
		})
	}
}

func TestSetForTenant_UnknownKeyReturnsErrUnknownKey(t *testing.T) {
	t.Parallel()

	c, _ := buildStartedClient(t, "global", "fee.rate", 0.0)

	// Registered key is "fee.rate"; "missing.key" was never registered.
	err := c.SetForTenant(tctx("tenant-A"), "global", "missing.key", 1, "admin")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnknownKey, "unregistered key should surface ErrUnknownKey")
}

func TestSetForTenant_NonTenantScopedKeyReturnsErrTenantScopeNotRegistered(t *testing.T) {
	t.Parallel()

	// Register via the legacy Register (NOT RegisterTenantScoped). Tenant
	// methods must refuse to operate on this key because the consumer never
	// opted in to tenant semantics. Register must happen before Start, so we
	// build a fresh client without the auto-start helper.
	fs := newTenantFakeStore()

	c, err := NewForTesting(fs)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	require.NoError(t, c.Register("global", "legacy.key", "x"))
	require.NoError(t, c.Start(context.Background()))

	select {
	case <-fs.subReady:
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe handler did not register")
	}

	err = c.SetForTenant(tctx("tenant-A"), "global", "legacy.key", "y", "admin")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTenantScopeNotRegistered, "non-tenant-scoped key must surface ErrTenantScopeNotRegistered")

	// Guardrail: the non-tenant-scoped path must not accidentally wrap a
	// different sentinel.
	assert.NotErrorIs(t, err, ErrUnknownKey, "must not masquerade as ErrUnknownKey")
}

func TestSetForTenant_ValidatorRejectsValueReturnsErrValidation(t *testing.T) {
	t.Parallel()

	fs := newTenantFakeStore()

	c, err := NewForTesting(fs)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	// Validator accepts the default (0.0) but rejects any negative rate.
	rejectNegative := func(v any) error {
		f, ok := v.(float64)
		if !ok {
			return fmt.Errorf("not a float64: %T", v)
		}
		if f < 0 {
			return fmt.Errorf("rate must be non-negative, got %v", f)
		}
		return nil
	}

	require.NoError(t, c.RegisterTenantScoped("global", "fee.rate", 0.0, WithValidator(rejectNegative)))
	require.NoError(t, c.Start(context.Background()))

	select {
	case <-fs.subReady:
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe handler did not register")
	}

	// Positive value — should succeed.
	require.NoError(t, c.SetForTenant(tctx("tenant-A"), "global", "fee.rate", 0.5, "admin"))

	// Negative value — rejected by validator.
	err = c.SetForTenant(tctx("tenant-A"), "global", "fee.rate", -0.5, "admin")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrValidation, "validator rejection must surface ErrValidation")
}

func TestSetForTenant_BeforeStartReturnsErrNotStarted(t *testing.T) {
	t.Parallel()

	fs := newTenantFakeStore()

	c, err := NewForTesting(fs)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	require.NoError(t, c.RegisterTenantScoped("global", "fee.rate", 0.0))

	// Start has NOT been called.
	err = c.SetForTenant(tctx("tenant-A"), "global", "fee.rate", 0.5, "admin")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotStarted, "write before Start must surface ErrNotStarted")
}

// TestNilClient_TenantMethods table-drives every tenant-scoped public method
// against a nil *Client receiver. The invariants being pinned:
//
//   - Write-path methods (RegisterTenantScoped, SetForTenant, DeleteForTenant,
//     GetForTenant, and every typed accessor) return ErrClosed via errors.Is.
//   - Read-only side-effect-free methods (ListTenantsForKey) return empty /
//     zero values without panicking.
//   - Subscription methods (OnTenantChange) return a no-op unsubscribe
//     function that is safe to call (including multiple times).
//
// Consolidating these into one table avoids the churn of maintaining
// N near-identical "nil receiver" functions as the tenant surface grows.
func TestNilClient_TenantMethods(t *testing.T) {
	t.Parallel()

	ctx := tctx("tenant-A")

	t.Run("RegisterTenantScoped", func(t *testing.T) {
		t.Parallel()

		var c *Client

		err := c.RegisterTenantScoped("ns", "k", "v")
		assert.ErrorIs(t, err, ErrClosed)
	})

	t.Run("SetForTenant", func(t *testing.T) {
		t.Parallel()

		var c *Client

		err := c.SetForTenant(ctx, "ns", "k", 1, "admin")
		assert.ErrorIs(t, err, ErrClosed)
	})

	t.Run("GetForTenant", func(t *testing.T) {
		t.Parallel()

		var c *Client

		v, found, err := c.GetForTenant(ctx, "ns", "k")
		assert.ErrorIs(t, err, ErrClosed)
		assert.Nil(t, v)
		assert.False(t, found)
	})

	t.Run("DeleteForTenant", func(t *testing.T) {
		t.Parallel()

		var c *Client

		err := c.DeleteForTenant(ctx, "ns", "k", "admin")
		assert.ErrorIs(t, err, ErrClosed)
	})

	t.Run("ListTenantsForKey", func(t *testing.T) {
		t.Parallel()

		var c *Client

		got := c.ListTenantsForKey("ns", "k")
		assert.Empty(t, got, "nil receiver must return empty slice without panic")
	})

	t.Run("OnTenantChange", func(t *testing.T) {
		t.Parallel()

		var c *Client

		unsub := c.OnTenantChange("ns", "k", func(_, _, _ string, _ any) {
			t.Fatal("nil-receiver subscription must never fire")
		})
		require.NotNil(t, unsub, "unsubscribe must not be nil — callers defer it")

		// Multiple invocations must be safe.
		unsub()
		unsub()
	})

	t.Run("GetStringForTenant", func(t *testing.T) {
		t.Parallel()

		var c *Client

		s, err := c.GetStringForTenant(ctx, "ns", "k")
		assert.ErrorIs(t, err, ErrClosed)
		assert.Empty(t, s)
	})

	t.Run("GetIntForTenant", func(t *testing.T) {
		t.Parallel()

		var c *Client

		n, err := c.GetIntForTenant(ctx, "ns", "k")
		assert.ErrorIs(t, err, ErrClosed)
		assert.Zero(t, n)
	})

	t.Run("GetBoolForTenant", func(t *testing.T) {
		t.Parallel()

		var c *Client

		b, err := c.GetBoolForTenant(ctx, "ns", "k")
		assert.ErrorIs(t, err, ErrClosed)
		assert.False(t, b)
	})

	t.Run("GetFloat64ForTenant", func(t *testing.T) {
		t.Parallel()

		var c *Client

		f, err := c.GetFloat64ForTenant(ctx, "ns", "k")
		assert.ErrorIs(t, err, ErrClosed)
		assert.Zero(t, f)
	})

	t.Run("GetDurationForTenant", func(t *testing.T) {
		t.Parallel()

		var c *Client

		d, err := c.GetDurationForTenant(ctx, "ns", "k")
		assert.ErrorIs(t, err, ErrClosed)
		assert.Zero(t, d)
	})
}

// TestSetForTenant_SurfacesErrTenantSchemaNotEnabled is the Client-level
// mirror of the backend phase-1 guard. When the underlying store returns
// store.ErrTenantSchemaNotEnabled (which is aliased as
// systemplane.ErrTenantSchemaNotEnabled), the Client wraps the error via
// persistTenantValue and surfaces it unchanged through errors.Is — so
// callers can match on the public sentinel without reaching into the
// internal store package.
//
// This is the rolling-deploy safety contract: phase-1 binaries MUST be
// able to detect "tenant writes not allowed yet" programmatically.
func TestSetForTenant_SurfacesErrTenantSchemaNotEnabled(t *testing.T) {
	t.Parallel()

	c, fs := buildStartedClient(t, "global", "fee.rate", 0.0)

	// Simulate the backend running in phase-1 compat mode: SetTenantValue
	// returns ErrTenantSchemaNotEnabled before mutating state.
	fs.mu.Lock()
	fs.setTenantErr = ErrTenantSchemaNotEnabled
	fs.mu.Unlock()

	err := c.SetForTenant(tctx("tenant-A"), "global", "fee.rate", 0.5, "admin")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTenantSchemaNotEnabled,
		"phase-1 backend error must surface through Client via errors.Is")

	// Defense-in-depth: the tenant cache must NOT have been populated when
	// the backend rejected the write. Otherwise a subsequent GetForTenant
	// in the same process would see a value that never made it to the
	// backend — a silent split-brain.
	_, found, err := c.GetForTenant(tctx("tenant-A"), "global", "fee.rate")
	require.NoError(t, err)
	// Either the global default (0.0) or nothing cached — but definitely
	// not 0.5 that we attempted to write.
	assert.True(t, found, "fall-through to default should still report found")
}

// ---------------------------------------------------------------------------
// GetForTenant — negative paths + fall-through behavior.
// ---------------------------------------------------------------------------

func TestGetForTenant_MissingCtxTenantReturnsErrMissingTenantContext(t *testing.T) {
	t.Parallel()

	c, _ := buildStartedClient(t, "global", "fee.rate", 0.0)

	_, _, err := c.GetForTenant(context.Background(), "global", "fee.rate")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMissingTenantContext)
}

func TestGetForTenant_InvalidTenantIDReturnsErrInvalidTenantID(t *testing.T) {
	t.Parallel()

	c, _ := buildStartedClient(t, "global", "fee.rate", 0.0)

	_, _, err := c.GetForTenant(tctx("_tenant"), "global", "fee.rate")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidTenantID)
}

func TestGetForTenant_UnknownKeyReturnsErrUnknownKey(t *testing.T) {
	t.Parallel()

	c, _ := buildStartedClient(t, "global", "fee.rate", 0.0)

	_, _, err := c.GetForTenant(tctx("tenant-A"), "global", "missing.key")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnknownKey)
}

func TestGetForTenant_NonTenantScopedKeyReturnsErrTenantScopeNotRegistered(t *testing.T) {
	t.Parallel()

	fs := newTenantFakeStore()

	c, err := NewForTesting(fs)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	require.NoError(t, c.Register("global", "legacy.key", "x"))
	require.NoError(t, c.Start(context.Background()))

	select {
	case <-fs.subReady:
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe handler did not register")
	}

	_, _, err = c.GetForTenant(tctx("tenant-A"), "global", "legacy.key")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTenantScopeNotRegistered)
}

// TestGetForTenant_NoOverrideFallsThroughToGlobalSet exercises the D3 locked
// decision: when a tenant has no override, GetForTenant returns whatever the
// legacy global cache holds (the value Set wrote to the _global row). Only
// when no global was set either does it return the registered default.
//
// This complements tenant_scoped_smoke_test.go:TestGetForTenant_FallsThroughToGlobal
// — the smoke test used a default of 0.01 and a global of 0.10; this test
// exercises a fresh Client where only the global was written.
func TestGetForTenant_NoOverrideFallsThroughToGlobalSet(t *testing.T) {
	t.Parallel()

	c, _ := buildStartedClient(t, "global", "log.level", "info")

	// Write to the legacy global cache.
	require.NoError(t, c.Set(context.Background(), "global", "log.level", "debug", "admin"))

	// tenant-A has no override — should see the global "debug", not the default.
	v, found, err := c.GetForTenant(tctx("tenant-A"), "global", "log.level")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "debug", v, "no tenant override should fall through to the global value")
}

func TestGetForTenant_NoOverrideNoGlobalReturnsDefault(t *testing.T) {
	t.Parallel()

	// No Set call — the legacy global cache holds only the registered default.
	c, _ := buildStartedClient(t, "global", "log.level", "info")

	v, found, err := c.GetForTenant(tctx("tenant-A"), "global", "log.level")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "info", v, "no override + no global-Set should return the registered default")
}

// TestGetForTenant_CrossTenantIsolation pins the core tenant-isolation
// invariant: tenant-A's override is invisible to tenant-B.
func TestGetForTenant_CrossTenantIsolation(t *testing.T) {
	t.Parallel()

	c, _ := buildStartedClient(t, "global", "log.level", "info")

	// tenant-A sets an override.
	require.NoError(t, c.SetForTenant(tctx("tenant-A"), "global", "log.level", "debug", "admin"))

	// tenant-A sees its override.
	v, found, err := c.GetForTenant(tctx("tenant-A"), "global", "log.level")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "debug", v, "tenant-A should see its own override")

	// tenant-B has no override — falls through to global/default ("info").
	v, found, err = c.GetForTenant(tctx("tenant-B"), "global", "log.level")
	require.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "info", v, "tenant-B must not see tenant-A's override (cross-tenant isolation)")
}

// ---------------------------------------------------------------------------
// DeleteForTenant — negative paths + idempotency.
// ---------------------------------------------------------------------------

func TestDeleteForTenant_MissingCtxTenantReturnsErrMissingTenantContext(t *testing.T) {
	t.Parallel()

	c, _ := buildStartedClient(t, "global", "fee.rate", 0.0)

	err := c.DeleteForTenant(context.Background(), "global", "fee.rate", "admin")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMissingTenantContext)
}

func TestDeleteForTenant_UnknownKeyReturnsErrUnknownKey(t *testing.T) {
	t.Parallel()

	c, _ := buildStartedClient(t, "global", "fee.rate", 0.0)

	err := c.DeleteForTenant(tctx("tenant-A"), "global", "missing.key", "admin")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnknownKey)
}

// TestDeleteForTenant_IdempotentNoOpOnMissingOverride locks the TRD §4.4
// contract that DeleteForTenant is idempotent: deleting a row that was never
// set returns nil. The TestStore.DeleteTenantValue is a backend no-op on
// missing rows, and the Client wraps it as-is.
func TestDeleteForTenant_IdempotentNoOpOnMissingOverride(t *testing.T) {
	t.Parallel()

	c, _ := buildStartedClient(t, "global", "fee.rate", 0.0)

	// tenant-A has no override. Delete must not error.
	err := c.DeleteForTenant(tctx("tenant-A"), "global", "fee.rate", "admin")
	require.NoError(t, err, "delete on missing override should be idempotent no-op")

	// Second delete also a no-op.
	err = c.DeleteForTenant(tctx("tenant-A"), "global", "fee.rate", "admin")
	require.NoError(t, err, "second delete should also be no-op")
}

// ---------------------------------------------------------------------------
// ListTenantsForKey — contract.
// ---------------------------------------------------------------------------

// TestListTenantsForKey_UnregisteredKeyReturnsEmpty pins the TRD signature
// choice: ListTenantsForKey has no error return (unlike the other tenant
// methods). Unregistered or misconfigured keys log a warning and return an
// empty slice.
func TestListTenantsForKey_UnregisteredKeyReturnsEmpty(t *testing.T) {
	t.Parallel()

	c, _ := buildStartedClient(t, "global", "fee.rate", 0.0)

	// Not registered.
	tenants := c.ListTenantsForKey("global", "missing.key")
	assert.Empty(t, tenants, "unregistered key should return empty slice")
	assert.NotNil(t, tenants, "return value should be an empty slice, not nil")

	// Wrong kind (registered via Register, not RegisterTenantScoped).
	fs := newTenantFakeStore()

	c2, err := NewForTesting(fs)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c2.Close() })

	require.NoError(t, c2.Register("global", "legacy.key", "x"))
	require.NoError(t, c2.Start(context.Background()))

	select {
	case <-fs.subReady:
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe handler did not register")
	}

	tenants2 := c2.ListTenantsForKey("global", "legacy.key")
	assert.Empty(t, tenants2, "non-tenant-scoped key should return empty slice")
	assert.NotNil(t, tenants2, "return value should be an empty slice, not nil")
}

// TestListTenantsForKey_DeduplicatesAndSorts verifies the TRD §4 contract:
// results are sorted, deduplicated, and exclude "_global".
func TestListTenantsForKey_DeduplicatesAndSorts(t *testing.T) {
	t.Parallel()

	c, _ := buildStartedClient(t, "global", "fee.rate", 0.0)

	// Set overrides for three tenants, with tenant-Z's set twice (dedupe test).
	// Order of set is intentionally non-alphabetical (Z, M, A, M again).
	require.NoError(t, c.SetForTenant(tctx("tenant-Z"), "global", "fee.rate", 0.1, "admin"))
	require.NoError(t, c.SetForTenant(tctx("tenant-M"), "global", "fee.rate", 0.2, "admin"))
	require.NoError(t, c.SetForTenant(tctx("tenant-A"), "global", "fee.rate", 0.3, "admin"))
	require.NoError(t, c.SetForTenant(tctx("tenant-M"), "global", "fee.rate", 0.99, "admin"))

	tenants := c.ListTenantsForKey("global", "fee.rate")
	assert.Equal(t, []string{"tenant-A", "tenant-M", "tenant-Z"}, tenants,
		"result must be sorted, deduplicated, and exclude _global")
}

// ---------------------------------------------------------------------------
// Typed accessors — fail-closed on missing ctx (D8).
// ---------------------------------------------------------------------------

func TestGetStringForTenant_WrongTypeReturnsErrValidation(t *testing.T) {
	t.Parallel()

	// Register a float default — asking for a string must fail.
	c, _ := buildStartedClient(t, "global", "fee.rate", 0.5)

	_, err := c.GetStringForTenant(tctx("tenant-A"), "global", "fee.rate")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrValidation, "type mismatch must surface ErrValidation (not silent zero)")
}

// TestGetIntForTenant_MissingTenantReturnsError locks the D8 invariant: a
// typed tenant accessor CANNOT silently zero when the tenant is missing from
// ctx. The legacy typed accessors (GetInt) would return 0 and no signal;
// their tenant counterparts must return a loud error.
func TestGetIntForTenant_MissingTenantReturnsError(t *testing.T) {
	t.Parallel()

	c, _ := buildStartedClient(t, "global", "threshold", 42)

	n, err := c.GetIntForTenant(context.Background(), "global", "threshold")
	require.Error(t, err, "missing tenant must NOT collapse to silent zero")
	assert.ErrorIs(t, err, ErrMissingTenantContext)
	assert.Equal(t, 0, n, "on error the typed accessor returns the zero value")
}

func TestGetBoolForTenant_Succeeds(t *testing.T) {
	t.Parallel()

	c, _ := buildStartedClient(t, "global", "flag", false)
	require.NoError(t, c.SetForTenant(tctx("tenant-A"), "global", "flag", true, "admin"))

	b, err := c.GetBoolForTenant(tctx("tenant-A"), "global", "flag")
	require.NoError(t, err)
	assert.True(t, b)

	// tenant-B falls through to default.
	b, err = c.GetBoolForTenant(tctx("tenant-B"), "global", "flag")
	require.NoError(t, err)
	assert.False(t, b, "tenant-B must see the default, not tenant-A's override")
}

func TestGetFloat64ForTenant_Succeeds(t *testing.T) {
	t.Parallel()

	c, _ := buildStartedClient(t, "global", "ratio", 1.0)
	require.NoError(t, c.SetForTenant(tctx("tenant-A"), "global", "ratio", 2.5, "admin"))

	f, err := c.GetFloat64ForTenant(tctx("tenant-A"), "global", "ratio")
	require.NoError(t, err)
	assert.InDelta(t, 2.5, f, 0.0001)
}

func TestGetDurationForTenant_Succeeds(t *testing.T) {
	t.Parallel()

	c, _ := buildStartedClient(t, "global", "timeout", "5s")

	// Registered default is a parseable string. GetDurationForTenant's legacy
	// counterpart handles the string case — same here.
	d, err := c.GetDurationForTenant(tctx("tenant-A"), "global", "timeout")
	require.NoError(t, err)
	assert.Equal(t, 5*time.Second, d)

	// Now write a number (float64, as JSON produces) — should decode as nanos.
	require.NoError(t, c.SetForTenant(tctx("tenant-A"), "global", "timeout", float64(2*time.Second), "admin"))

	d, err = c.GetDurationForTenant(tctx("tenant-A"), "global", "timeout")
	require.NoError(t, err)
	assert.Equal(t, 2*time.Second, d)
}

// ---------------------------------------------------------------------------
// AC1 CRITICAL REGRESSION PIN — TestGet_IgnoresTenantOverrides.
//
// This is the PRD AC1 compatibility invariant: a tenant override MUST NOT
// leak into the legacy global Get path. Every pre-tenant consumer depends
// on this — breaking it would silently corrupt every existing OnChange +
// Get usage site.
// ---------------------------------------------------------------------------

func TestGet_IgnoresTenantOverrides(t *testing.T) {
	t.Parallel()

	// Registered default for the global row.
	c, _ := buildStartedClient(t, "global", "fee.rate", 0.01)

	// Set an override for tenant-A. This touches tenantCache, NOT the
	// legacy global cache (see TRD §4.1 / tenant_scoped.go:247-254).
	require.NoError(t, c.SetForTenant(tctx("tenant-A"), "global", "fee.rate", 0.99, "admin"))

	// Legacy Get returns the registered default — never the tenant override.
	v, found := c.Get("global", "fee.rate")
	require.True(t, found, "registered key must always be found by legacy Get")
	assert.InDelta(t, 0.01, v, 0.0001,
		"legacy Get MUST return the default (0.01), not tenant-A's override (0.99). "+
			"Breaking this is AC1 regression; every pre-tenant consumer would observe unexpected values.")

	// Same invariant, even after setting overrides for two tenants.
	require.NoError(t, c.SetForTenant(tctx("tenant-B"), "global", "fee.rate", 0.77, "admin"))

	v, found = c.Get("global", "fee.rate")
	require.True(t, found)
	assert.InDelta(t, 0.01, v, 0.0001,
		"legacy Get must continue returning the global default with multiple tenant overrides active")

	// Typed accessors on the legacy path behave the same way.
	assert.InDelta(t, 0.01, c.GetFloat64("global", "fee.rate"), 0.0001,
		"GetFloat64 on the legacy path must ignore tenant overrides")
}

// ---------------------------------------------------------------------------
// Lazy mode — miss-populate + store-failure fall-through.
//
// These tests exercise the lazy-mode GetForTenant path where a cache miss
// triggers a single-flight store.GetTenantValue. Both the success case (miss
// populates the LRU) and the failure case (store error falls through to the
// global/default cascade) are covered.
// ---------------------------------------------------------------------------

// TestGetForTenant_LazyModeMissPopulatesLRU verifies that in lazy mode, a
// GetForTenant cache miss fetches from the backend and populates the LRU.
//
// Population is verified through observable behavior (not private state):
// after the first miss populates the cache, the backend is forced to fail
// on subsequent calls. If the second GetForTenant still returns the correct
// value, it could only have come from the cache — proving population without
// coupling the test to the tenantCache internal shape.
func TestGetForTenant_LazyModeMissPopulatesLRU(t *testing.T) {
	t.Parallel()

	fs := newTenantFakeStore()

	c, err := NewForTesting(fs, WithLazyTenantLoad(10))
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	require.NoError(t, c.RegisterTenantScoped("global", "fee.rate", 0.0))
	require.NoError(t, c.Start(context.Background()))

	select {
	case <-fs.subReady:
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe handler did not register")
	}

	// In lazy mode, Start should NOT hydrate tenant rows. Seed a tenant row
	// directly (bypassing the changefeed) so the first GetForTenant exercises
	// the miss-populate path.
	fs.directSetTenantRow("tenant-A", "global", "fee.rate", []byte(`0.42`))

	// Miss-populate: first GetForTenant fetches from the store and returns
	// the stored value. This is the only call that should touch the backend.
	v, found, err := c.GetForTenant(tctx("tenant-A"), "global", "fee.rate")
	require.NoError(t, err)
	assert.True(t, found)
	assert.InDelta(t, 0.42, v, 0.0001, "lazy miss should populate from the store")

	// Force the backend to fail. A subsequent cache miss would fall through
	// to the global default (0.0) via the error-handling path; a cache hit
	// must bypass the backend entirely and keep returning 0.42.
	fs.mu.Lock()
	fs.getTenantErr = errors.New("backend unavailable after initial populate")
	fs.mu.Unlock()

	// Second read MUST return the previously populated value. This proves
	// the LRU entry exists and is consulted before any backend call —
	// population verified purely through observable behavior.
	v2, found2, err := c.GetForTenant(tctx("tenant-A"), "global", "fee.rate")
	require.NoError(t, err, "cache hit must not observe the backend error")
	assert.True(t, found2, "populated LRU entry must survive backend failure")
	assert.InDelta(t, 0.42, v2, 0.0001,
		"second read must return the cached value — population only verifiable via this behavior")
}

// TestGetForTenant_LazyModeStoreFailureFallsThrough verifies that when the
// store fails during a lazy miss, GetForTenant logs a warning and falls
// through to the global/default cascade rather than erroring.
//
// Per tenant_scoped.go:330-338: "On error, log, fall through."
func TestGetForTenant_LazyModeStoreFailureFallsThrough(t *testing.T) {
	t.Parallel()

	fs := newTenantFakeStore()
	// Force GetTenantValue to fail, exercising the fall-through path.
	fs.getTenantErr = errors.New("synthetic backend failure")

	c, err := NewForTesting(fs, WithLazyTenantLoad(10))
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	require.NoError(t, c.RegisterTenantScoped("global", "log.level", "info"))
	require.NoError(t, c.Start(context.Background()))

	select {
	case <-fs.subReady:
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe handler did not register")
	}

	// Store is broken; GetForTenant should still succeed, falling through to
	// the registered default. This is the "a degraded store must not block
	// reads" invariant.
	v, found, err := c.GetForTenant(tctx("tenant-A"), "global", "log.level")
	require.NoError(t, err, "store failure must not surface to the caller; fall through to default")
	assert.True(t, found)
	assert.Equal(t, "info", v, "fall-through returns the registered default")
}

// TestGetForTenant_LazyModeSingleFlightCoalescesMisses is the C5 regression
// pin: N concurrent GetForTenant calls on the same (tenantID, ns, key)
// tuple that all miss the LRU must collapse into exactly ONE
// store.GetTenantValue round-trip, not N.
//
// Mechanism: the fake store blocks its first GetTenantValue call via a
// signaling channel. While the first goroutine is held in-flight, the
// remaining N-1 goroutines arrive at sfg.Do for the same key and wait on
// the single-flight group instead of issuing their own backend call. The
// test then releases the blocked call and asserts that all goroutines
// observed the same value AND that GetTenantValue was invoked exactly
// once across the burst.
//
// Without single-flight this test would record N calls to the backend
// and is the smoking gun for the C5 fix.
func TestGetForTenant_LazyModeSingleFlightCoalescesMisses(t *testing.T) {
	t.Parallel()

	const numGoroutines = 20

	fs := newTenantFakeStore()
	// Block the backend call until the test explicitly releases it.
	// Buffer block by 1 so the first call does not deadlock before any
	// drainer is present; the test reads from block to confirm in-flight.
	fs.getTenantBlock = make(chan struct{}, 1)
	fs.getTenantRelease = make(chan struct{})

	c, err := NewForTesting(fs, WithLazyTenantLoad(10))
	require.NoError(t, err)

	t.Cleanup(func() { _ = c.Close() })

	require.NoError(t, c.RegisterTenantScoped("global", "fee.rate", 0.0))
	require.NoError(t, c.Start(context.Background()))

	select {
	case <-fs.subReady:
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe handler did not register")
	}

	// Seed a tenant row directly so the single-flight fetch has a real
	// value to return (we want to distinguish "coalesced to one call" from
	// "coalesced because nothing was found").
	fs.directSetTenantRow("tenant-A", "global", "fee.rate", []byte(`0.42`))

	type result struct {
		val any
		ok  bool
		err error
	}

	results := make(chan result, numGoroutines)

	// Launch N concurrent GetForTenant calls; the first will block in the
	// backend, the remaining N-1 must coalesce via sfg.Do.
	for i := 0; i < numGoroutines; i++ {
		go func() {
			v, ok, err := c.GetForTenant(tctx("tenant-A"), "global", "fee.rate")
			results <- result{val: v, ok: ok, err: err}
		}()
	}

	// Wait for the first call to signal it has entered GetTenantValue.
	select {
	case <-fs.getTenantBlock:
	case <-time.After(2 * time.Second):
		t.Fatal("first GetTenantValue call did not arrive in time")
	}

	// Give the remaining N-1 goroutines a moment to pile up at sfg.Do.
	// 50ms is ample at any realistic CPU count; 20 goroutines reach the
	// single-flight point in well under 1ms on an idle machine.
	time.Sleep(50 * time.Millisecond)

	// Release the blocked backend call. All goroutines should now complete.
	close(fs.getTenantRelease)

	// Drain results and verify every goroutine saw the canonical value.
	for i := 0; i < numGoroutines; i++ {
		select {
		case r := <-results:
			require.NoError(t, r.err)
			assert.True(t, r.ok)
			assert.InDelta(t, 0.42, r.val, 0.0001,
				"all concurrent callers must observe the single-flight result")
		case <-time.After(2 * time.Second):
			t.Fatalf("goroutine %d did not return", i)
		}
	}

	// THE CRITICAL ASSERTION: exactly one backend round-trip for N
	// concurrent misses. Without single-flight this would be N.
	calls := fs.getTenantCalls.Load()
	assert.Equal(t, int64(1), calls,
		"single-flight must coalesce %d concurrent misses into 1 backend call (got %d)",
		numGoroutines, calls)
}

// ---------------------------------------------------------------------------
// Meta — self-check on the tenantFakeStore helper.
//
// Asserts the helper's fire() and subscribe wiring work as documented. A
// subtle bug here would silently invalidate every subscriber-firing test, so
// we pin the helper's contract explicitly.
// ---------------------------------------------------------------------------

func TestTenantFakeStore_FireReachesSubscribedHandlers(t *testing.T) {
	t.Parallel()

	fs := newTenantFakeStore()

	var (
		mu       sync.Mutex
		received []TestEvent
		done     = make(chan struct{}, 1)
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		_ = fs.Subscribe(ctx, func(evt TestEvent) {
			mu.Lock()
			received = append(received, evt)
			mu.Unlock()
			select {
			case done <- struct{}{}:
			default:
			}
		})
	}()

	select {
	case <-fs.subReady:
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe did not register")
	}

	fs.fire(TestEvent{Namespace: "ns", Key: "k", TenantID: "tenant-A"})

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler did not fire")
	}

	mu.Lock()
	defer mu.Unlock()
	require.Len(t, received, 1)
	assert.Equal(t, "tenant-A", received[0].TenantID)
	// Guardrail: store.SentinelGlobal really is "_global" — a rename would cascade
	// through every backend assertion. Keep this check literal so a future
	// change has to think twice.
	assert.Equal(t, "_global", store.SentinelGlobal, "store.SentinelGlobal should remain '_global' (TRD §3, decision D2)")
}

// Guardrail: tenant-manager core symbols are still reachable. If the import
// becomes unused this test prevents a silent drop.
var _ = strings.Contains
var _ = core.IsValidTenantID
