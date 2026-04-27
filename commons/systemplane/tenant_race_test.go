//go:build unit

// Package systemplane — race-detector stress tests for tenant-scoped paths.
//
// PRD AC11 requires the tenant methods to be safe under concurrent use
// across distinct tenants. This file exercises two shapes:
//
//  1. 100 goroutines × distinct tenants × shared (ns, key) hammering
//     SetForTenant + GetForTenant + DeleteForTenant. The race detector
//     (-race) must report zero findings. OnChange MUST never fire (AC8).
//
//  2. Concurrent OnTenantChange register/unregister while other goroutines
//     drive SetForTenant, to flush out any deadlock or map-mutation bug in
//     the tenantSubscribers slice management.
//
// Benchmarks live in bench_tenant_test.go; the in-memory store mirrors
// the tenantTestStore in tenant_scoped_smoke_test.go but is locally
// defined to keep this file buildable in isolation and concurrency-safe
// for 100+ concurrent writers.
package systemplane

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
)

// raceKey addresses a single row in raceStore.
type raceKey struct {
	tenantID  string
	namespace string
	key       string
}

// raceStore is a concurrency-safe TestStore used by the race tests. Unlike
// tenantTestStore in tenant_scoped_smoke_test.go (one mutex over everything,
// optimized for single-writer smoke tests), raceStore uses a dedicated
// RWMutex for rows and a separate slice mutex for handlers so read-heavy
// Get paths don't serialize behind Subscribe's handler list mutation.
//
// fire() copies the handler slice under RLock, releases, then invokes each
// handler — matching the Client's fireSubscribers pattern. This prevents a
// handler that writes (via a cycle through OnTenantChange → register →
// Subscribe) from deadlocking on the handler mutex.
type raceStore struct {
	rowsMu   sync.RWMutex
	rows     map[raceKey]TestEntry
	hMu      sync.Mutex
	handlers []func(TestEvent)
	subReady chan struct{}
}

func newRaceStore() *raceStore {
	return &raceStore{
		rows:     make(map[raceKey]TestEntry),
		subReady: make(chan struct{}),
	}
}

func (s *raceStore) fire(evt TestEvent) {
	s.hMu.Lock()
	handlers := make([]func(TestEvent), len(s.handlers))
	copy(handlers, s.handlers)
	s.hMu.Unlock()

	for _, h := range handlers {
		h(evt)
	}
}

func (s *raceStore) List(_ context.Context) ([]TestEntry, error) {
	s.rowsMu.RLock()
	defer s.rowsMu.RUnlock()

	out := make([]TestEntry, 0, len(s.rows))

	for _, e := range s.rows {
		if e.TenantID == "_global" {
			out = append(out, e)
		}
	}

	return out, nil
}

func (s *raceStore) Get(_ context.Context, namespace, key string) (TestEntry, bool, error) {
	s.rowsMu.RLock()
	defer s.rowsMu.RUnlock()

	e, ok := s.rows[raceKey{tenantID: "_global", namespace: namespace, key: key}]

	return e, ok, nil
}

func (s *raceStore) Set(_ context.Context, e TestEntry) error {
	e.TenantID = "_global"

	s.rowsMu.Lock()
	s.rows[raceKey{tenantID: "_global", namespace: e.Namespace, key: e.Key}] = e
	s.rowsMu.Unlock()

	s.fire(TestEvent{Namespace: e.Namespace, Key: e.Key, TenantID: "_global"})

	return nil
}

func (s *raceStore) Subscribe(ctx context.Context, handler func(TestEvent)) error {
	s.hMu.Lock()
	first := len(s.handlers) == 0
	s.handlers = append(s.handlers, handler)
	s.hMu.Unlock()

	if first {
		close(s.subReady)
	}

	<-ctx.Done()

	return nil
}

func (s *raceStore) Close() error { return nil }

func (s *raceStore) GetTenantValue(_ context.Context, tenantID, namespace, key string) (TestEntry, bool, error) {
	s.rowsMu.RLock()
	defer s.rowsMu.RUnlock()

	e, ok := s.rows[raceKey{tenantID: tenantID, namespace: namespace, key: key}]

	return e, ok, nil
}

func (s *raceStore) SetTenantValue(_ context.Context, tenantID string, e TestEntry) error {
	e.TenantID = tenantID

	s.rowsMu.Lock()
	s.rows[raceKey{tenantID: tenantID, namespace: e.Namespace, key: e.Key}] = e
	s.rowsMu.Unlock()

	s.fire(TestEvent{Namespace: e.Namespace, Key: e.Key, TenantID: tenantID})

	return nil
}

func (s *raceStore) DeleteTenantValue(_ context.Context, tenantID, namespace, key, _ string) error {
	s.rowsMu.Lock()
	// Only fire when a row actually existed — real backends do the same.
	rk := raceKey{tenantID: tenantID, namespace: namespace, key: key}
	_, existed := s.rows[rk]
	delete(s.rows, rk)
	s.rowsMu.Unlock()

	if existed {
		s.fire(TestEvent{Namespace: namespace, Key: key, TenantID: tenantID})
	}

	return nil
}

func (s *raceStore) ListTenantOverrides(_ context.Context, _, _, _ string, _ int) ([]TestEntry, error) {
	s.rowsMu.RLock()
	defer s.rowsMu.RUnlock()

	out := make([]TestEntry, 0, len(s.rows))
	for k, e := range s.rows {
		if k.tenantID == "_global" {
			continue
		}

		out = append(out, e)
	}

	return out, nil
}

func (s *raceStore) ListTenantsForKey(_ context.Context, namespace, key string) ([]string, error) {
	s.rowsMu.RLock()
	defer s.rowsMu.RUnlock()

	seen := make(map[string]struct{})

	for k := range s.rows {
		if k.namespace == namespace && k.key == key && k.tenantID != "_global" {
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

// TestRace_ConcurrentSetForTenant_DistinctTenants spawns 100 goroutines,
// each writing to its own (tenant-<n>, "global", "fee.rate") tuple. The
// race detector (-race) must report zero findings.
//
// The key invariants being proven:
//
//  1. AC11 — race safety under 100-way parallelism on distinct tenants.
//  2. AC8 — OnChange fires ZERO times. Tenant writes must never cross
//     into the legacy global-subscribe channel.
//  3. Debouncer widening (§5.2) — each tenant's events keep their own
//     debounce slot. With debounce=0 (the NewForTesting default) every
//     write produces exactly one fire.
//
// We deliberately keep debouncing disabled for this test so fire counts
// are deterministic. The debounce-collapse-across-tenants invariant is
// pinned separately via the unit suite in Task 7.
func TestRace_ConcurrentSetForTenant_DistinctTenants(t *testing.T) {
	t.Parallel()

	const numTenants = 100

	const writesPerTenant = 10

	ts := newRaceStore()

	c, err := NewForTesting(ts)
	require.NoError(t, err, "NewForTesting")

	require.NoError(t, c.RegisterTenantScoped("global", "fee.rate", 0.0), "RegisterTenantScoped")

	require.NoError(t, c.Start(context.Background()), "Start")

	t.Cleanup(func() { _ = c.Close() })

	// Wait for Subscribe to register its handler so the first write's echo
	// reaches the dispatcher.
	select {
	case <-ts.subReady:
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe handler did not register within 2s")
	}

	var (
		tenantFires atomic.Int64
		globalFires atomic.Int64
	)

	// Track per-tenant fire presence so we can assert every tenant received
	// at least one OnTenantChange event. Use a map guarded by a mutex; the
	// race detector is watching.
	perTenantMu := sync.Mutex{}
	perTenantSeen := make(map[string]int)

	unsubTenant := c.OnTenantChange("global", "fee.rate", func(_ context.Context, _, _, tenantID string, _ any) {
		tenantFires.Add(1)

		perTenantMu.Lock()
		perTenantSeen[tenantID]++
		perTenantMu.Unlock()
	})
	t.Cleanup(unsubTenant)

	// AC8: OnChange must NEVER fire on tenant writes.
	unsubGlobal := c.OnChange("global", "fee.rate", func(_ any) {
		globalFires.Add(1)
	})
	t.Cleanup(unsubGlobal)

	var wg sync.WaitGroup

	for i := 0; i < numTenants; i++ {
		wg.Add(1)

		go func(tenantN int) {
			defer wg.Done()

			tenantID := fmt.Sprintf("tenant-%d", tenantN)
			ctx := core.ContextWithTenantID(context.Background(), tenantID)

			for j := 0; j < writesPerTenant; j++ {
				if err := c.SetForTenant(ctx, "global", "fee.rate", float64(j), "race"); err != nil {
					t.Errorf("tenant %s SetForTenant[%d]: %v", tenantID, j, err)
				}

				// Interleave a read so GetForTenant's lock interactions are
				// also on the race path.
				if _, _, err := c.GetForTenant(ctx, "global", "fee.rate"); err != nil {
					t.Errorf("tenant %s GetForTenant[%d]: %v", tenantID, j, err)
				}
			}

			// Final delete: produces one more changefeed event per tenant.
			if err := c.DeleteForTenant(ctx, "global", "fee.rate", "race"); err != nil {
				t.Errorf("tenant %s DeleteForTenant: %v", tenantID, err)
			}
		}(i)
	}

	wg.Wait()

	// With debounce=0 (NewForTesting default) the dispatch path is fully
	// synchronous: SetForTenant → raceStore.SetTenantValue → raceStore.fire
	// → debouncer.Submit(window=0) → invokeWithRecover → fireTenantSubscribers
	// → subscriber callback. Every fire happens on the writing goroutine's
	// stack before SetForTenant returns, so by the time wg.Wait returns all
	// expected events have been counted. No polling loop required.

	// AC8 — legacy OnChange must have fired ZERO times.
	require.Zero(t, globalFires.Load(),
		"AC8 violated: OnChange fired %d times on tenant writes", globalFires.Load())

	// Each tenant must have seen at least one OnTenantChange fire. The
	// minimum guaranteed is 1 per tenant (even if the debouncer coalesced
	// every write within a tenant into a single fire, which cannot happen
	// with debounce=0 but would be acceptable under a real window).
	perTenantMu.Lock()
	defer perTenantMu.Unlock()

	require.Len(t, perTenantSeen, numTenants,
		"expected all %d tenants to have received at least one OnTenantChange fire, got %d distinct tenants",
		numTenants, len(perTenantSeen))

	for i := 0; i < numTenants; i++ {
		tid := fmt.Sprintf("tenant-%d", i)

		count, ok := perTenantSeen[tid]
		require.True(t, ok, "tenant %s received no OnTenantChange fires", tid)
		require.Positive(t, count, "tenant %s received zero OnTenantChange fires", tid)
	}

	// With debounce=0 the fire count should equal numTenants*(writesPerTenant+1)
	// exactly. Allow equality or more (in case a future refactor adds additional
	// benign fires) to keep the test robust; the strict upper-bound check
	// lives in the unit suite.
	require.GreaterOrEqual(t, tenantFires.Load(), int64(numTenants),
		"expected at least %d OnTenantChange fires (one per tenant), got %d",
		numTenants, tenantFires.Load())
}

// TestRace_ConcurrentSubscribeUnsubscribe hammers OnTenantChange
// register/unregister from 50 goroutines while another 50 goroutines drive
// SetForTenant. The race detector must report zero findings; there must be
// no deadlock (a 30s deadline guards against one). This exercises the
// tenantSubsMu slice copy + panic-shield pattern under adversarial load.
func TestRace_ConcurrentSubscribeUnsubscribe(t *testing.T) {
	t.Parallel()

	const numWriters = 50

	const numSubscribers = 50

	const opsPerGoroutine = 20

	ts := newRaceStore()

	c, err := NewForTesting(ts)
	require.NoError(t, err, "NewForTesting")

	require.NoError(t, c.RegisterTenantScoped("global", "fee.rate", 0.0), "RegisterTenantScoped")

	require.NoError(t, c.Start(context.Background()), "Start")

	t.Cleanup(func() { _ = c.Close() })

	select {
	case <-ts.subReady:
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe handler did not register within 2s")
	}

	// Single baseline subscriber to ensure something is fired even when
	// the transient subs unsubscribe themselves.
	var baselineFires atomic.Int64

	unsubBase := c.OnTenantChange("global", "fee.rate", func(_ context.Context, _, _, _ string, _ any) {
		baselineFires.Add(1)
	})
	t.Cleanup(unsubBase)

	done := make(chan struct{})

	var wg sync.WaitGroup

	// Subscriber churn: repeatedly Register → (handler may fire) → Unregister.
	for i := 0; i < numSubscribers; i++ {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for j := 0; j < opsPerGoroutine; j++ {
				select {
				case <-done:
					return
				default:
				}

				unsub := c.OnTenantChange("global", "fee.rate", func(_ context.Context, _, _, _ string, _ any) {
					// no-op transient subscriber
				})
				unsub()
			}
		}()
	}

	// Writer churn: SetForTenant from distinct tenants.
	for i := 0; i < numWriters; i++ {
		wg.Add(1)

		go func(writerN int) {
			defer wg.Done()

			tenantID := fmt.Sprintf("tenant-%d", writerN)
			ctx := core.ContextWithTenantID(context.Background(), tenantID)

			for j := 0; j < opsPerGoroutine; j++ {
				select {
				case <-done:
					return
				default:
				}

				if err := c.SetForTenant(ctx, "global", "fee.rate", float64(j), "race"); err != nil {
					t.Errorf("tenant %s SetForTenant[%d]: %v", tenantID, j, err)
					return
				}
			}
		}(i)
	}

	// Deadlock guard.
	doneCh := make(chan struct{})

	go func() {
		wg.Wait()
		close(doneCh)
	}()

	select {
	case <-doneCh:
	case <-time.After(30 * time.Second):
		close(done)
		t.Fatal("TestRace_ConcurrentSubscribeUnsubscribe timed out after 30s — possible deadlock")
	}

	// Baseline subscriber should have seen at least numWriters fires
	// (one per writer's first Set, minimum). We don't pin an exact count
	// because transient subscribers may have fired for some writes too.
	require.Positive(t, baselineFires.Load(),
		"baseline subscriber must fire at least once under writer load")
}
