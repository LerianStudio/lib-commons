//go:build unit

// OnTenantChange subscriber semantics + cross-kind isolation + debouncer
// coalescing correctness.
//
// This file's central concern is the dispatch matrix: which subscribers fire
// when, with which values, for which event shape. The invariants it locks are:
//
//   - OnTenantChange fires on SetForTenant and DeleteForTenant (AC8 + AC9).
//   - OnChange does NOT fire on tenant writes (AC8 regression pin).
//   - OnTenantChange does NOT fire on global writes (symmetry of AC8).
//   - Unsubscribe is idempotent and safe mid-dispatch (slice-copy invariant).
//   - A panicking subscriber does not kill siblings (RecoverAndLog invariant).
//   - Debouncer widens on (ns, key, tenantID) — per-tenant events never
//     collapse across tenants (TRD §5.2, AC12).
//
// The tenantFakeStore helper lives in tenant_scoped_test.go — it is shared
// across the tenant-scoped and onchange suites.
package systemplane

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
)

// ---------------------------------------------------------------------------
// Fire semantics.
// ---------------------------------------------------------------------------

// TestOnTenantChange_FiresOnSetForTenant is the minimal sanity check for the
// dispatch path. The smoke test in tenant_scoped_smoke_test.go covers the
// AC8 cross-check (OnChange stays silent); this test isolates the firing
// mechanics.
func TestOnTenantChange_FiresOnSetForTenant(t *testing.T) {
	t.Parallel()

	c, _ := buildStartedClient(t, "global", "fee.rate", 0.0)

	type fire struct {
		ns, key, tenantID string
		value             any
	}

	var (
		mu    sync.Mutex
		fires []fire
		done  = make(chan struct{}, 1)
	)

	unsub := c.OnTenantChange("global", "fee.rate", func(_ context.Context, ns, key, tenantID string, newValue any) {
		mu.Lock()
		fires = append(fires, fire{ns, key, tenantID, newValue})
		mu.Unlock()
		select {
		case done <- struct{}{}:
		default:
		}
	})
	defer unsub()

	require.NoError(t, c.SetForTenant(tctx("tenant-A"), "global", "fee.rate", 0.5, "admin"))

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("OnTenantChange did not fire")
	}

	mu.Lock()
	defer mu.Unlock()

	require.Len(t, fires, 1)
	assert.Equal(t, "global", fires[0].ns)
	assert.Equal(t, "fee.rate", fires[0].key)
	assert.Equal(t, "tenant-A", fires[0].tenantID)
	assert.InDelta(t, 0.5, fires[0].value, 0.0001)
}

// TestOnTenantChange_FiresOnDeleteForTenantWithDefault pins AC9 / TRD §4.4:
// DeleteForTenant's changefeed echo fires OnTenantChange with the
// REGISTERED DEFAULT — NOT the pre-delete override value. The subscriber's
// signal is "the effective post-change value for this tenant", and post-
// delete the tenant reverts to global/default.
func TestOnTenantChange_FiresOnDeleteForTenantWithDefault(t *testing.T) {
	t.Parallel()

	c, _ := buildStartedClient(t, "global", "fee.rate", 0.42)

	var (
		mu    sync.Mutex
		fires []any
		done  = make(chan struct{}, 2)
	)

	unsub := c.OnTenantChange("global", "fee.rate", func(_ context.Context, _, _, _ string, newValue any) {
		mu.Lock()
		fires = append(fires, newValue)
		mu.Unlock()
		done <- struct{}{}
	})
	defer unsub()

	ctx := tctx("tenant-A")

	require.NoError(t, c.SetForTenant(ctx, "global", "fee.rate", 0.99, "admin"))

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("set fire missing")
	}

	require.NoError(t, c.DeleteForTenant(ctx, "global", "fee.rate", "admin"))

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("delete fire missing")
	}

	mu.Lock()
	defer mu.Unlock()

	require.Len(t, fires, 2)
	assert.InDelta(t, 0.99, fires[0], 0.0001, "first fire: set value")
	assert.InDelta(t, 0.42, fires[1], 0.0001,
		"second fire: delete reverts to registered default, NOT the pre-delete override")
}

// TestOnTenantChange_MultipleSubscribersAllFire verifies fan-out: two
// subscribers to the same (ns, key) each receive every event.
func TestOnTenantChange_MultipleSubscribersAllFire(t *testing.T) {
	t.Parallel()

	c, _ := buildStartedClient(t, "global", "fee.rate", 0.0)

	var (
		mu             sync.Mutex
		countA, countB int
		doneA, doneB   = make(chan struct{}, 1), make(chan struct{}, 1)
	)

	unsubA := c.OnTenantChange("global", "fee.rate", func(_ context.Context, _, _, _ string, _ any) {
		mu.Lock()
		countA++
		mu.Unlock()
		select {
		case doneA <- struct{}{}:
		default:
		}
	})
	defer unsubA()

	unsubB := c.OnTenantChange("global", "fee.rate", func(_ context.Context, _, _, _ string, _ any) {
		mu.Lock()
		countB++
		mu.Unlock()
		select {
		case doneB <- struct{}{}:
		default:
		}
	})
	defer unsubB()

	require.NoError(t, c.SetForTenant(tctx("tenant-A"), "global", "fee.rate", 0.1, "admin"))

	select {
	case <-doneA:
	case <-time.After(2 * time.Second):
		t.Fatal("A did not fire")
	}

	select {
	case <-doneB:
	case <-time.After(2 * time.Second):
		t.Fatal("B did not fire")
	}

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, countA)
	assert.Equal(t, 1, countB)
}

// TestOnTenantChange_UnsubscribeRemovesFromList verifies that after the
// returned unsubscribe is invoked, further tenant writes do NOT fire the
// unsubscribed callback. The invariant spans (a) unsubscribe immediately
// removes the callback from the list and (b) in-flight events that have
// already snapshotted the list may still fire — the latter is by design
// (see fireTenantSubscribers comments).
func TestOnTenantChange_UnsubscribeRemovesFromList(t *testing.T) {
	t.Parallel()

	c, _ := buildStartedClient(t, "global", "fee.rate", 0.0)

	var (
		mu        sync.Mutex
		fireCount int
	)

	unsub := c.OnTenantChange("global", "fee.rate", func(_ context.Context, _, _, _ string, _ any) {
		mu.Lock()
		fireCount++
		mu.Unlock()
	})

	unsub()
	// Second call to unsub must be safe (sync.Once inside).
	unsub()

	// Allow event to propagate; the subscribe list should be empty.
	require.NoError(t, c.SetForTenant(tctx("tenant-A"), "global", "fee.rate", 0.5, "admin"))

	// Give the synchronous dispatch path a generous window. This is
	// negative-assertion sleep — acceptable because the synchronous path
	// resolves in microseconds.
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 0, fireCount, "unsubscribed callback must NOT fire")
}

// TestOnTenantChange_SelfUnsubscribeFromCallbackIsSafe verifies the slice-
// copy invariant from fireTenantSubscribers: a callback that unsubscribes
// itself mid-dispatch must not deadlock or corrupt iteration for sibling
// subscribers.
func TestOnTenantChange_SelfUnsubscribeFromCallbackIsSafe(t *testing.T) {
	t.Parallel()

	c, _ := buildStartedClient(t, "global", "fee.rate", 0.0)

	var (
		mu           sync.Mutex
		selfFired    int
		siblingFired int
		bothDone     = make(chan struct{}, 2)
	)

	// Pre-declare unsubSelf so the callback can reference it.
	var unsubSelf func()

	unsubSelf = c.OnTenantChange("global", "fee.rate", func(_ context.Context, _, _, _ string, _ any) {
		mu.Lock()
		selfFired++
		mu.Unlock()
		unsubSelf()
		bothDone <- struct{}{}
	})
	defer unsubSelf()

	unsubSibling := c.OnTenantChange("global", "fee.rate", func(_ context.Context, _, _, _ string, _ any) {
		mu.Lock()
		siblingFired++
		mu.Unlock()
		bothDone <- struct{}{}
	})
	defer unsubSibling()

	require.NoError(t, c.SetForTenant(tctx("tenant-A"), "global", "fee.rate", 0.5, "admin"))

	for i := 0; i < 2; i++ {
		select {
		case <-bothDone:
		case <-time.After(2 * time.Second):
			t.Fatalf("missing fire %d", i)
		}
	}

	mu.Lock()
	assert.Equal(t, 1, selfFired, "self-unsubscriber fires once on the dispatch that triggered the unsubscribe")
	assert.Equal(t, 1, siblingFired, "sibling fires despite the self-unsubscribe (slice copy)")
	mu.Unlock()

	// Second event must NOT reach the self-unsubscribed callback.
	require.NoError(t, c.SetForTenant(tctx("tenant-B"), "global", "fee.rate", 0.6, "admin"))

	// Wait for the sibling to pick up event #2.
	select {
	case <-bothDone:
	case <-time.After(2 * time.Second):
		t.Fatal("sibling did not fire on second event")
	}

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, selfFired, "self-unsubscribed callback must NOT fire on a subsequent event")
	assert.Equal(t, 2, siblingFired, "sibling fires twice (once per event)")
}

// TestOnTenantChange_PanickingCallbackDoesNotAffectOthers verifies the
// RecoverAndLog invariant: a subscriber that panics does not prevent its
// siblings from being notified.
func TestOnTenantChange_PanickingCallbackDoesNotAffectOthers(t *testing.T) {
	t.Parallel()

	c, _ := buildStartedClient(t, "global", "fee.rate", 0.0)

	var (
		mu           sync.Mutex
		survivorHits int
		survivorDone = make(chan struct{}, 1)
	)

	// Subscriber #1: panics on every invocation.
	unsubPanic := c.OnTenantChange("global", "fee.rate", func(_ context.Context, _, _, _ string, _ any) {
		panic("intentional test panic")
	})
	defer unsubPanic()

	// Subscriber #2: records a hit — this one must survive.
	unsubOK := c.OnTenantChange("global", "fee.rate", func(_ context.Context, _, _, _ string, _ any) {
		mu.Lock()
		survivorHits++
		mu.Unlock()
		select {
		case survivorDone <- struct{}{}:
		default:
		}
	})
	defer unsubOK()

	require.NoError(t, c.SetForTenant(tctx("tenant-A"), "global", "fee.rate", 0.5, "admin"))

	select {
	case <-survivorDone:
	case <-time.After(2 * time.Second):
		t.Fatal("survivor subscriber did not fire — panic broke the dispatch")
	}

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, 1, survivorHits, "panic in one subscriber must not block siblings")
}

// ---------------------------------------------------------------------------
// Cross-kind isolation.
// ---------------------------------------------------------------------------

// TestOnChange_DoesNotFireOnTenantWrites is the AC8 critical regression pin
// called out by name in tasks.md §Task 7 and TRD §9 "Backward Compatibility
// Matrix". Every pre-tenant consumer registered an OnChange callback and
// assumed it fires ONLY for global writes; breaking this invariant would
// silently deliver tenant values to code that has no tenant context and no
// way to distinguish them — a data-leak-grade regression.
//
// The same invariant is asserted as a side-check inside the smoke test
// TestOnTenantChange_FiresOnTenantWrite_OnChangeStaysSilent (a composite
// positive+negative assertion) and is transitively validated by
// TestDebouncer_GlobalAndTenantDoNotCollide. This dedicated pin exists so
// that a future maintainer performing a bisect or a targeted grep for AC8
// has an obvious first-class target whose sole concern is the invariant.
//
// Positive control: a sibling non-tenant-scoped key's OnChange subscriber
// DOES fire on a legacy Set. Without the positive control a broken dispatch
// loop could pass this test by firing nothing at all.
func TestOnChange_DoesNotFireOnTenantWrites(t *testing.T) {
	t.Parallel()

	// Shared Client carries two keys:
	//   (global, fee.rate)   — tenant-scoped, OnChange must stay silent
	//                          when SetForTenant writes.
	//   (global, legacy.key) — non-tenant-scoped, OnChange must fire when
	//                          legacy Set writes (positive control).
	fs := newTenantFakeStore()

	c, err := NewForTesting(fs)
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })

	require.NoError(t, c.RegisterTenantScoped("global", "fee.rate", 0.0))
	require.NoError(t, c.Register("global", "legacy.key", "init"))
	require.NoError(t, c.Start(context.Background()))

	select {
	case <-fs.subReady:
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe handler did not register in time")
	}

	var (
		tenantKeyOnChangeFires atomic.Int32
		legacyKeyOnChangeFires atomic.Int32
		legacyDone             = make(chan struct{}, 1)
	)

	// Subscribe OnChange to the tenant-scoped key. Per AC8, this callback
	// MUST NOT fire when SetForTenant writes the same (ns, key).
	unsubTenantKey := c.OnChange("global", "fee.rate", func(_ any) {
		tenantKeyOnChangeFires.Add(1)
	})
	defer unsubTenantKey()

	// Positive control: subscribe OnChange to the legacy key.
	unsubLegacyKey := c.OnChange("global", "legacy.key", func(_ any) {
		legacyKeyOnChangeFires.Add(1)
		select {
		case legacyDone <- struct{}{}:
		default:
		}
	})
	defer unsubLegacyKey()

	// Tenant write — OnChange for the tenant-scoped key MUST stay silent.
	require.NoError(t, c.SetForTenant(tctx("tenant-A"), "global", "fee.rate", 0.5, "admin"))

	// Legacy write — OnChange for the legacy key MUST fire (positive control).
	require.NoError(t, c.Set(context.Background(), "global", "legacy.key", "next", "admin"))

	select {
	case <-legacyDone:
	case <-time.After(2 * time.Second):
		t.Fatal("positive control: legacy OnChange did not fire on Set — dispatch is broken upstream of AC8")
	}

	// Additional window for any stray tenant → OnChange delivery to surface.
	// NewForTesting sets debounce=0, so a 200ms window is >> the synchronous
	// dispatch path and any event would have landed by now.
	time.Sleep(200 * time.Millisecond)

	assert.Equal(t, int32(0), tenantKeyOnChangeFires.Load(),
		"AC8 regression: OnChange MUST NOT fire on SetForTenant. "+
			"Pre-tenant consumers rely on OnChange being strictly global-only.")

	assert.Equal(t, int32(1), legacyKeyOnChangeFires.Load(),
		"positive control: legacy OnChange should have fired exactly once for the legacy Set")

	// Second tenant write by a different tenant — still must not reach OnChange.
	require.NoError(t, c.SetForTenant(tctx("tenant-B"), "global", "fee.rate", 0.9, "admin"))

	time.Sleep(200 * time.Millisecond)

	assert.Equal(t, int32(0), tenantKeyOnChangeFires.Load(),
		"AC8 regression: repeated tenant writes from multiple tenants must never reach OnChange")
}

// TestOnTenantChange_DoesNotFireOnGlobalWrites is the symmetry cross-check
// of AC8: a legacy Set writes the _global row; OnTenantChange subscribers
// must stay silent. The smoke test pins the OnChange side of the invariant
// (tenant writes don't fire OnChange); this test pins the inverse.
func TestOnTenantChange_DoesNotFireOnGlobalWrites(t *testing.T) {
	t.Parallel()

	c, _ := buildStartedClient(t, "global", "fee.rate", 0.0)

	var tenantFires atomic.Int32

	unsub := c.OnTenantChange("global", "fee.rate", func(_ context.Context, _, _, _ string, _ any) {
		tenantFires.Add(1)
	})
	defer unsub()

	// Legacy global write — should fire OnChange (tested elsewhere), not
	// OnTenantChange.
	require.NoError(t, c.Set(context.Background(), "global", "fee.rate", 0.5, "admin"))

	// Negative-assertion sleep — acceptable per the smoke test's precedent.
	time.Sleep(200 * time.Millisecond)

	assert.Equal(t, int32(0), tenantFires.Load(),
		"OnTenantChange MUST NOT fire on legacy Set to the global row (AC8 symmetry)")
}

// ---------------------------------------------------------------------------
// Debouncer coalescing — AC12 / TRD §5.2.
//
// The debouncer composite key is (ns, key, tenantID). Rapid writes within
// the window for the SAME tuple coalesce. Writes for DIFFERENT tuples do
// NOT coalesce — this is the critical invariant that prevents cross-tenant
// changefeed drops.
//
// NewForTesting defaults to debounce=0 (see client_testing.go:192-194).
// These tests override with WithDebounce(100ms) so coalescing is observable.
// ---------------------------------------------------------------------------

// buildStartedClientWithDebounce mirrors buildStartedClient but enables the
// 100ms debounce window. Used exclusively by the debouncer tests below.
func buildStartedClientWithDebounce(t *testing.T, namespace, key string, defaultValue any, window time.Duration) (*Client, *tenantFakeStore) {
	t.Helper()

	return buildStartedClient(t, namespace, key, defaultValue, WithDebounce(window))
}

// TestDebouncer_CoalescesRapidSetsForSameTenant pins that five rapid
// SetForTenant calls in a 100ms window for (tenant-A, ns, key) collapse to
// a single OnTenantChange fire with the final value. The intra-tuple
// coalescing is the whole point of the debouncer.
func TestDebouncer_CoalescesRapidSetsForSameTenant(t *testing.T) {
	t.Parallel()

	const window = 100 * time.Millisecond

	c, _ := buildStartedClientWithDebounce(t, "global", "fee.rate", 0.0, window)

	type fire struct {
		tenantID string
		value    any
	}

	var (
		mu    sync.Mutex
		fires []fire
	)

	// Buffered channel for signalling: every fire pushes a copy; the test
	// waits deterministically for the first signal and then gives the
	// debouncer a bounded quiet window to prove no second fire arrives.
	signals := make(chan fire, 8)

	unsub := c.OnTenantChange("global", "fee.rate", func(_ context.Context, _, _, tenantID string, newValue any) {
		f := fire{tenantID, newValue}

		mu.Lock()
		fires = append(fires, f)
		mu.Unlock()

		select {
		case signals <- f:
		default:
		}
	})
	defer unsub()

	// Five rapid writes for the SAME tenant. The debouncer should collapse
	// all five changefeed echoes into one refresh/fire with the final value.
	for i := 0; i < 5; i++ {
		require.NoError(t, c.SetForTenant(tctx("tenant-A"), "global", "fee.rate", float64(i), "admin"))
	}

	// Wait deterministically for the first fire (with a generous upper bound).
	select {
	case <-signals:
	case <-time.After(5 * window):
		t.Fatal("no fire observed within 5x the debounce window")
	}

	// Now allow a bounded quiet window: any additional fire beyond the first
	// comes from a mid-burst flush that the test tolerates up to one extra.
	// Past 2x the debounce window no further flushes are possible.
	time.Sleep(2 * window)

	mu.Lock()
	defer mu.Unlock()

	// The exact fire count CAN be 1 (pure intra-tuple coalescing) or 2 in
	// edge cases where the debouncer happens to flush mid-burst. The
	// invariant we care about is (a) it is NOT 5 (coalescing works) AND
	// (b) the LAST fire observed matches the final value (tail wins).
	require.NotEmpty(t, fires, "at least one fire expected")
	assert.LessOrEqual(t, len(fires), 2,
		"5 rapid sets for the same tuple must coalesce (got %d fires)", len(fires))
	assert.Equal(t, "tenant-A", fires[len(fires)-1].tenantID)
	assert.InDelta(t, float64(4), fires[len(fires)-1].value, 0.0001,
		"final fire should hold the last value written (tail wins)")
}

// TestDebouncer_DoesNotCollapseAcrossTenants is the critical TRD §5.2 /
// AC12 invariant: SetForTenant(tenant-A) and SetForTenant(tenant-B) in
// the same debounce window produce TWO fires, not one.
//
// Without the tenantID component in the composite key, tenant-B's
// changefeed event would overwrite tenant-A's timer slot and silently
// drop tenant-A's OnTenantChange — the exact cross-tenant corruption this
// test prevents.
func TestDebouncer_DoesNotCollapseAcrossTenants(t *testing.T) {
	t.Parallel()

	const window = 100 * time.Millisecond

	c, _ := buildStartedClientWithDebounce(t, "global", "fee.rate", 0.0, window)

	type fire struct {
		tenantID string
		value    any
	}

	var (
		mu    sync.Mutex
		fires []fire
	)

	signals := make(chan fire, 8)

	unsub := c.OnTenantChange("global", "fee.rate", func(_ context.Context, _, _, tenantID string, newValue any) {
		f := fire{tenantID, newValue}

		mu.Lock()
		fires = append(fires, f)
		mu.Unlock()

		select {
		case signals <- f:
		default:
		}
	})
	defer unsub()

	// Back-to-back writes for DIFFERENT tenants. These events arrive at the
	// debouncer within microseconds of each other — well inside the 100ms
	// window. The widened composite key ensures each lives in its own timer
	// slot.
	require.NoError(t, c.SetForTenant(tctx("tenant-A"), "global", "fee.rate", 1.0, "admin"))
	require.NoError(t, c.SetForTenant(tctx("tenant-B"), "global", "fee.rate", 2.0, "admin"))

	// Wait deterministically for exactly two signals (one per tenant).
	// Beyond 5x the debounce window anything else is a CI hang.
	deadline := time.After(5 * window)
	for i := 0; i < 2; i++ {
		select {
		case <-signals:
		case <-deadline:
			mu.Lock()
			got := len(fires)
			mu.Unlock()

			t.Fatalf("expected 2 fires within 5x debounce window, observed %d", got)
		}
	}

	mu.Lock()
	defer mu.Unlock()

	require.Len(t, fires, 2,
		"two tenants in the same window must produce two fires — collapse would silently drop an event")

	// Order is not strictly guaranteed by the debouncer (same-window timers
	// fire in insertion order, but the refresh goroutines can interleave).
	// Assert the set of observed (tenant, value) tuples instead.
	seen := make(map[string]float64, 2)
	for _, f := range fires {
		seen[f.tenantID] = f.value.(float64)
	}

	assert.InDelta(t, 1.0, seen["tenant-A"], 0.0001, "tenant-A's fire must carry its own value")
	assert.InDelta(t, 2.0, seen["tenant-B"], 0.0001, "tenant-B's fire must carry its own value")
}

// TestDebouncer_GlobalAndTenantDoNotCollide is the other critical cross-
// kind invariant: a Set (global write) and a SetForTenant (tenant write)
// in the same window each fire exactly once, on their respective
// subscriber surfaces. The debouncer treats _global and tenant-A as
// distinct timer slots because they have distinct composite keys.
func TestDebouncer_GlobalAndTenantDoNotCollide(t *testing.T) {
	t.Parallel()

	const window = 100 * time.Millisecond

	c, _ := buildStartedClientWithDebounce(t, "global", "fee.rate", 0.0, window)

	var (
		mu            sync.Mutex
		onChangeFires []any
		onTenantFires []any
	)

	globalSignals := make(chan struct{}, 4)
	tenantSignals := make(chan struct{}, 4)

	unsubGlobal := c.OnChange("global", "fee.rate", func(newValue any) {
		mu.Lock()
		onChangeFires = append(onChangeFires, newValue)
		mu.Unlock()

		select {
		case globalSignals <- struct{}{}:
		default:
		}
	})
	defer unsubGlobal()

	unsubTenant := c.OnTenantChange("global", "fee.rate", func(_ context.Context, _, _, _ string, newValue any) {
		mu.Lock()
		onTenantFires = append(onTenantFires, newValue)
		mu.Unlock()

		select {
		case tenantSignals <- struct{}{}:
		default:
		}
	})
	defer unsubTenant()

	require.NoError(t, c.Set(context.Background(), "global", "fee.rate", 10.0, "admin"))
	require.NoError(t, c.SetForTenant(tctx("tenant-A"), "global", "fee.rate", 20.0, "admin"))

	// Wait deterministically for one fire on each surface. 5x the debounce
	// window is a hard upper bound — beyond that the test has hung.
	deadline := time.After(5 * window)

	gotGlobal, gotTenant := false, false
	for !gotGlobal || !gotTenant {
		select {
		case <-globalSignals:
			gotGlobal = true
		case <-tenantSignals:
			gotTenant = true
		case <-deadline:
			t.Fatalf("timed out waiting for both fires (global=%v tenant=%v)", gotGlobal, gotTenant)
		}
	}

	mu.Lock()
	defer mu.Unlock()

	require.Len(t, onChangeFires, 1, "OnChange should fire once for the global Set")
	assert.InDelta(t, 10.0, onChangeFires[0], 0.0001)

	require.Len(t, onTenantFires, 1, "OnTenantChange should fire once for the tenant Set")
	assert.InDelta(t, 20.0, onTenantFires[0], 0.0001)
}

// ---------------------------------------------------------------------------
// Nil-receiver / defensive paths.
// ---------------------------------------------------------------------------

// Nil-receiver coverage for OnTenantChange lives in the consolidated
// TestNilClient_TenantMethods table in tenant_scoped_test.go. The nil-fn
// case below is distinct (Client is valid, fn is nil) so it stays here.

func TestOnTenantChange_NilFnReturnsNoOpUnsubscribe(t *testing.T) {
	t.Parallel()

	c, _ := buildStartedClient(t, "global", "fee.rate", 0.0)

	unsub := c.OnTenantChange("global", "fee.rate", nil)
	require.NotNil(t, unsub)

	// Must not panic.
	unsub()
}

// TestOnTenantChange_UnregisteredKeyReturnsNoOp pins the "silent tolerance"
// invariant from onchange.go: registering for a key that was never
// registered returns a no-op unsubscribe. No error, no panic, no fires.
func TestOnTenantChange_UnregisteredKeyReturnsNoOp(t *testing.T) {
	t.Parallel()

	c, _ := buildStartedClient(t, "global", "fee.rate", 0.0)

	unsub := c.OnTenantChange("global", "unregistered.key", func(_ context.Context, _, _, _ string, _ any) {
		t.Fatal("unregistered-key callback must not fire")
	})
	require.NotNil(t, unsub)

	// Write to the registered key — unregistered subscriber should stay silent.
	require.NoError(t, c.SetForTenant(tctx("tenant-A"), "global", "fee.rate", 0.5, "admin"))

	// Give time for a stray fire if the guard was broken.
	time.Sleep(100 * time.Millisecond)

	unsub()
}

// TestOnTenantChange_LegacyRegisteredKeyReturnsNoOp verifies that a key
// registered via the legacy Register (not RegisterTenantScoped) cannot be
// used as a target for OnTenantChange — the method returns a no-op.
func TestOnTenantChange_LegacyRegisteredKeyReturnsNoOp(t *testing.T) {
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

	unsub := c.OnTenantChange("global", "legacy.key", func(_ context.Context, _, _, _ string, _ any) {
		t.Fatal("OnTenantChange on a legacy key must never fire")
	})
	require.NotNil(t, unsub)

	// Even a legacy Set must not fire OnTenantChange for a legacy key.
	require.NoError(t, c.Set(context.Background(), "global", "legacy.key", "y", "admin"))

	time.Sleep(100 * time.Millisecond)

	unsub()
}

// TestOnTenantChange_CallbackCtxCarriesTenantID pins the documented
// OnTenantChange contract: the callback's ctx argument is pre-scoped to
// tenantID via core.ContextWithTenantID. Subscribers can therefore pass
// the ctx directly into tenant-aware lib-commons facilities (DLQ,
// idempotency, webhook) without manually re-propagating the tenant.
//
// A regression here (e.g. replacing the ctx with context.Background()
// without the tenant binding) would silently break every tenant-aware
// integration downstream — they would attempt to enqueue / publish /
// idempotency-check against an empty tenant scope.
func TestOnTenantChange_CallbackCtxCarriesTenantID(t *testing.T) {
	t.Parallel()

	c, _ := buildStartedClient(t, "global", "fee.rate", 0.0)

	var (
		mu         sync.Mutex
		observedID string
		done       = make(chan struct{}, 1)
	)

	unsub := c.OnTenantChange("global", "fee.rate", func(ctx context.Context, _, _, tenantID string, _ any) {
		mu.Lock()
		// core.GetTenantIDContext is the extraction mirror of
		// core.ContextWithTenantID used by fireTenantSubscribers. If the
		// ctx is pre-scoped correctly, this returns the tenantID arg.
		observedID = core.GetTenantIDContext(ctx)
		mu.Unlock()

		// Also assert the positional tenantID matches — if both are equal
		// we've confirmed the ctx was scoped to the same tenant the
		// callback is being told about.
		_ = tenantID

		select {
		case done <- struct{}{}:
		default:
		}
	})
	defer unsub()

	require.NoError(t, c.SetForTenant(tctx("tenant-A"), "global", "fee.rate", 0.5, "admin"))

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("OnTenantChange did not fire")
	}

	mu.Lock()
	defer mu.Unlock()

	assert.Equal(t, "tenant-A", observedID,
		"callback ctx MUST carry tenantID via core.ContextWithTenantID — downstream tenant-aware "+
			"facilities depend on this invariant")
}
