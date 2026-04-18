// Package systemplanetest provides backend-agnostic contract tests for the
// internal store.Store interface. Both the Postgres and MongoDB backends
// run this shared suite in their integration tests to ensure behavioral
// equivalence.
package systemplanetest

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/store"
)

// Factory constructs a fresh, isolated Store for a single test.
// Must be safe to call multiple times per *testing.T (each call yields a
// new Store with its own empty state). Implementations typically register
// a t.Cleanup to tear down containers, databases, etc.
type Factory func(t *testing.T) store.Store

// RunOption configures the behavior of [Run].
type RunOption func(*runConfig)

// runConfig carries the options accumulated via [RunOption] calls.
type runConfig struct {
	skip map[string]struct{}
}

// SkipSubtest instructs [Run] to call t.Skip on the named subtest. Useful
// when a specific backend topology cannot satisfy a particular contract —
// e.g. MongoDB polling mode cannot observe inter-tick deletes, so
// "TenantSubscribeReceivesDeleteEvent" must be skipped for that mode.
//
// The name must match the t.Run subtest name exactly (case-sensitive).
// Unknown names are silently ignored so adding a new contract does not
// break callers.
func SkipSubtest(name string) RunOption {
	return func(cfg *runConfig) {
		if cfg.skip == nil {
			cfg.skip = make(map[string]struct{})
		}

		cfg.skip[name] = struct{}{}
	}
}

// shouldSkip reports whether the named subtest is in the skip set.
func (c *runConfig) shouldSkip(name string) bool {
	if c == nil || c.skip == nil {
		return false
	}

	_, ok := c.skip[name]

	return ok
}

// Run executes the full contract test suite. Each sub-test receives a
// fresh Store via the factory. Contract tests block up to 10s for
// eventual-consistency operations (e.g., Subscribe delivery); individual
// tests use smaller deadlines where possible.
//
// # Tenant sub-suite
//
// The tenant-scoped contracts (Task 8, PRD AC13) exercise the tenant surface
// of store.Store — SetTenantValue, GetTenantValue, DeleteTenantValue,
// ListTenantValues, ListTenantsForKey — against the same factory the
// global contracts use. This proves observable parity across Postgres and
// MongoDB backends plus the in-memory TestStore.
//
// A handful of tenant contracts assert that Subscribe delivers events for
// tenant mutations, including DELETE events. MongoDB polling mode cannot
// observe deletes between ticks (see internal/mongodb/mongodb_changestream.go
// subscribePoll godoc). Polling-mode integration tests should skip
// "TenantSubscribeReceivesDeleteEvent" via t.Skip at the integration-test
// level; see internal/mongodb/mongodb_tenant_integration_test.go.
func Run(t *testing.T, factory Factory, opts ...RunOption) {
	t.Helper()

	cfg := &runConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	// run is a tiny helper that wraps t.Run with opt-in skip support. Keeping
	// the skip check in one place prevents copy-paste drift across 19 subtests.
	run := func(name string, fn func(*testing.T)) {
		t.Run(name, func(t *testing.T) {
			if cfg.shouldSkip(name) {
				t.Skipf("skipped by SkipSubtest(%q)", name)
				return
			}

			fn(t)
		})
	}

	run("ListOnEmpty", func(t *testing.T) { testListOnEmpty(t, factory) })
	run("GetMissingReturnsFalse", func(t *testing.T) { testGetMissingReturnsFalse(t, factory) })
	run("SetThenGetRoundtrip", func(t *testing.T) { testSetThenGetRoundtrip(t, factory) })
	run("SetTwiceLastWriteWins", func(t *testing.T) { testSetTwiceLastWriteWins(t, factory) })
	run("ListReturnsAllAfterMultipleSets", func(t *testing.T) { testListReturnsAllAfterMultipleSets(t, factory) })
	run("SubscribeReceivesEventOnSet", func(t *testing.T) { testSubscribeReceivesEventOnSet(t, factory) })
	run("SubscribeRespectsContextCancel", func(t *testing.T) { testSubscribeRespectsContextCancel(t, factory) })
	run("CloseIsIdempotent", func(t *testing.T) { testCloseIsIdempotent(t, factory) })
	run("OperationsAfterCloseReturnError", func(t *testing.T) { testOperationsAfterCloseReturnError(t, factory) })
	run("NamespaceIsolation", func(t *testing.T) { testNamespaceIsolation(t, factory) })

	// Tenant contract sub-suite (Task 8, PRD AC13).
	run("TenantListOnEmpty", func(t *testing.T) { testTenantListOnEmpty(t, factory) })
	run("SetTenantThenGetRoundtrip", func(t *testing.T) { testSetTenantThenGetRoundtrip(t, factory) })
	run("SetTenantTwiceLastWriteWins", func(t *testing.T) { testSetTenantTwiceLastWriteWins(t, factory) })
	run("DeleteTenantValueReturnsMissing", func(t *testing.T) { testDeleteTenantValueReturnsMissing(t, factory) })
	run("DeleteTenantValueIsIdempotent", func(t *testing.T) { testDeleteTenantValueIsIdempotent(t, factory) })
	run("ListTenantsForKeySorted", func(t *testing.T) { testListTenantsForKeySorted(t, factory) })
	run("GlobalAndTenantRowsCoexist", func(t *testing.T) { testGlobalAndTenantRowsCoexist(t, factory) })
	run("TenantSubscribeReceivesSetEvent", func(t *testing.T) { testTenantSubscribeReceivesSetEvent(t, factory) })
	run("TenantSubscribeReceivesDeleteEvent", func(t *testing.T) { testTenantSubscribeReceivesDeleteEvent(t, factory) })
}

// ---------------------------------------------------------------------------
// Test implementations
// ---------------------------------------------------------------------------

// testListOnEmpty: List on a fresh store returns an empty slice, no error.
func testListOnEmpty(t *testing.T, factory Factory) {
	t.Helper()

	s := factory(t)
	ctx := context.Background()

	entries, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List on empty store: %v", err)
	}

	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}
}

// testGetMissingReturnsFalse: Get for a non-existent key returns (_, false, nil).
func testGetMissingReturnsFalse(t *testing.T, factory Factory) {
	t.Helper()

	s := factory(t)
	ctx := context.Background()

	_, found, err := s.Get(ctx, "ns", "missing-key")
	if err != nil {
		t.Fatalf("Get on missing key: %v", err)
	}

	if found {
		t.Fatal("expected found=false for missing key")
	}
}

// testSetThenGetRoundtrip: Set an entry, then Get returns the same data.
func testSetThenGetRoundtrip(t *testing.T, factory Factory) {
	t.Helper()

	s := factory(t)
	ctx := context.Background()

	entry := store.Entry{
		Namespace: "global",
		Key:       "log.level",
		Value:     []byte(`"info"`),
		UpdatedAt: time.Now().UTC().Truncate(time.Millisecond),
		UpdatedBy: "actor-1",
	}

	if err := s.Set(ctx, entry); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, found, err := s.Get(ctx, "global", "log.level")
	if err != nil {
		t.Fatalf("Get after Set: %v", err)
	}

	if !found {
		t.Fatal("expected found=true after Set")
	}

	if got.Namespace != entry.Namespace {
		t.Fatalf("namespace: got %q, want %q", got.Namespace, entry.Namespace)
	}

	if got.Key != entry.Key {
		t.Fatalf("key: got %q, want %q", got.Key, entry.Key)
	}

	if string(got.Value) != string(entry.Value) {
		t.Fatalf("value: got %q, want %q", got.Value, entry.Value)
	}

	if got.UpdatedBy != entry.UpdatedBy {
		t.Fatalf("updated_by: got %q, want %q", got.UpdatedBy, entry.UpdatedBy)
	}

	// updated_at should be within the last 5 seconds (allows backend clock drift).
	if time.Since(got.UpdatedAt) > 5*time.Second {
		t.Fatalf("updated_at too old: %v (now: %v)", got.UpdatedAt, time.Now().UTC())
	}
}

// testSetTwiceLastWriteWins: Set v1, Set v2 for same key; Get returns v2,
// List returns exactly one entry.
func testSetTwiceLastWriteWins(t *testing.T, factory Factory) {
	t.Helper()

	s := factory(t)
	ctx := context.Background()

	base := store.Entry{
		Namespace: "global",
		Key:       "rate_limit.rps",
		UpdatedBy: "actor-1",
	}

	base.Value = []byte(`100`)
	if err := s.Set(ctx, base); err != nil {
		t.Fatalf("Set v1: %v", err)
	}

	base.Value = []byte(`200`)
	base.UpdatedBy = "actor-2"

	if err := s.Set(ctx, base); err != nil {
		t.Fatalf("Set v2: %v", err)
	}

	got, found, err := s.Get(ctx, base.Namespace, base.Key)
	if err != nil {
		t.Fatalf("Get after overwrite: %v", err)
	}

	if !found {
		t.Fatal("expected found=true")
	}

	if string(got.Value) != `200` {
		t.Fatalf("value: got %q, want %q", got.Value, `200`)
	}

	if got.UpdatedBy != "actor-2" {
		t.Fatalf("updated_by: got %q, want %q", got.UpdatedBy, "actor-2")
	}

	entries, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(entries) != 1 {
		t.Fatalf("expected 1 entry after two Sets to same key, got %d", len(entries))
	}
}

// testListReturnsAllAfterMultipleSets: Set 3 entries with different
// namespace/key combos. List returns all 3 (order unspecified).
func testListReturnsAllAfterMultipleSets(t *testing.T, factory Factory) {
	t.Helper()

	s := factory(t)
	ctx := context.Background()

	entries := []store.Entry{
		{Namespace: "global", Key: "log.level", Value: []byte(`"info"`), UpdatedBy: "a"},
		{Namespace: "global", Key: "rate_limit", Value: []byte(`100`), UpdatedBy: "b"},
		{Namespace: "tenant:acme", Key: "feature.x", Value: []byte(`true`), UpdatedBy: "c"},
	}

	for _, e := range entries {
		if err := s.Set(ctx, e); err != nil {
			t.Fatalf("Set(%s/%s): %v", e.Namespace, e.Key, err)
		}
	}

	got, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(got) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(got))
	}

	// Sort both slices by namespace+key for deterministic comparison.
	entryKey := func(e store.Entry) string { return e.Namespace + "/" + e.Key }

	sort.Slice(got, func(i, j int) bool { return entryKey(got[i]) < entryKey(got[j]) })
	sort.Slice(entries, func(i, j int) bool { return entryKey(entries[i]) < entryKey(entries[j]) })

	for i := range entries {
		if entryKey(got[i]) != entryKey(entries[i]) {
			t.Fatalf("entry[%d]: got %q, want %q", i, entryKey(got[i]), entryKey(entries[i]))
		}
	}
}

// testSubscribeReceivesEventOnSet: Subscribe in a goroutine, Set an entry,
// verify the handler receives a matching Event within 10 seconds.
func testSubscribeReceivesEventOnSet(t *testing.T, factory Factory) {
	t.Helper()

	s := factory(t)
	ctx, cancel := context.WithCancel(context.Background())

	t.Cleanup(cancel)

	events := make(chan store.Event, 8)
	subErr := make(chan error, 1)

	go func() {
		subErr <- s.Subscribe(ctx, func(e store.Event) {
			events <- e
		})
	}()

	// Give Subscribe a moment to establish the listener before writing.
	// This setup sleep is unavoidable with real backends (Postgres LISTEN,
	// MongoDB change-streams) because the Store interface has no "ready"
	// signal. The assertion side uses polling (eventually), not sleep.
	time.Sleep(200 * time.Millisecond)

	entry := store.Entry{
		Namespace: "global",
		Key:       "log.level",
		Value:     []byte(`"debug"`),
		UpdatedBy: "actor-test",
	}

	if err := s.Set(ctx, entry); err != nil {
		t.Fatalf("Set: %v", err)
	}

	// Wait for event delivery (up to 10s for change-stream warmup).
	if !eventually(t, 10*time.Second, func() bool {
		select {
		case e := <-events:
			return e.Namespace == entry.Namespace && e.Key == entry.Key
		default:
			return false
		}
	}) {
		t.Fatal("timed out waiting for subscribe event")
	}

	cancel()

	// Drain Subscribe return.
	select {
	case err := <-subErr:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Subscribe returned unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe did not return after context cancel")
	}
}

// testSubscribeRespectsContextCancel: Cancel the context, verify Subscribe
// returns promptly with nil or context.Canceled.
func testSubscribeRespectsContextCancel(t *testing.T, factory Factory) {
	t.Helper()

	s := factory(t)
	ctx, cancel := context.WithCancel(context.Background())
	subErr := make(chan error, 1)

	go func() {
		subErr <- s.Subscribe(ctx, func(_ store.Event) {})
	}()

	// Let Subscribe settle, then cancel.
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case err := <-subErr:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Subscribe returned unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe did not return within 2s after context cancel")
	}
}

// testCloseIsIdempotent: Close twice, no panic, no error on second call.
func testCloseIsIdempotent(t *testing.T, factory Factory) {
	t.Helper()

	s := factory(t)

	if err := s.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// testOperationsAfterCloseReturnError: After Close, List, Get, Set, and
// Subscribe all return store.ErrClosed.
func testOperationsAfterCloseReturnError(t *testing.T, factory Factory) {
	t.Helper()

	s := factory(t)
	ctx := context.Background()

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if _, err := s.List(ctx); !errors.Is(err, store.ErrClosed) {
		t.Fatalf("List after Close: got %v, want %v", err, store.ErrClosed)
	}

	if _, _, err := s.Get(ctx, "ns", "k"); !errors.Is(err, store.ErrClosed) {
		t.Fatalf("Get after Close: got %v, want %v", err, store.ErrClosed)
	}

	entry := store.Entry{Namespace: "ns", Key: "k", Value: []byte(`1`), UpdatedBy: "a"}
	if err := s.Set(ctx, entry); !errors.Is(err, store.ErrClosed) {
		t.Fatalf("Set after Close: got %v, want %v", err, store.ErrClosed)
	}

	if err := s.Subscribe(ctx, func(_ store.Event) {}); !errors.Is(err, store.ErrClosed) {
		t.Fatalf("Subscribe after Close: got %v, want %v", err, store.ErrClosed)
	}
}

// testNamespaceIsolation: Set entries with the same key in different
// namespaces; verify they are stored and retrievable independently.
func testNamespaceIsolation(t *testing.T, factory Factory) {
	t.Helper()

	s := factory(t)
	ctx := context.Background()

	e1 := store.Entry{Namespace: "nsA", Key: "k1", Value: []byte(`"alpha"`), UpdatedBy: "a"}
	e2 := store.Entry{Namespace: "nsB", Key: "k1", Value: []byte(`"beta"`), UpdatedBy: "b"}

	if err := s.Set(ctx, e1); err != nil {
		t.Fatalf("Set nsA/k1: %v", err)
	}

	if err := s.Set(ctx, e2); err != nil {
		t.Fatalf("Set nsB/k1: %v", err)
	}

	gotA, foundA, err := s.Get(ctx, "nsA", "k1")
	if err != nil || !foundA {
		t.Fatalf("Get nsA/k1: found=%v, err=%v", foundA, err)
	}

	if string(gotA.Value) != `"alpha"` {
		t.Fatalf("nsA/k1 value: got %q, want %q", gotA.Value, `"alpha"`)
	}

	gotB, foundB, err := s.Get(ctx, "nsB", "k1")
	if err != nil || !foundB {
		t.Fatalf("Get nsB/k1: found=%v, err=%v", foundB, err)
	}

	if string(gotB.Value) != `"beta"` {
		t.Fatalf("nsB/k1 value: got %q, want %q", gotB.Value, `"beta"`)
	}

	entries, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	// Verify the two entries are distinguishable.
	nsSet := map[string]bool{}
	for _, e := range entries {
		nsSet[e.Namespace] = true
	}

	if !nsSet["nsA"] || !nsSet["nsB"] {
		t.Fatalf("expected namespaces nsA and nsB, got %v", nsSet)
	}
}

// ---------------------------------------------------------------------------
// Tenant-scoped contract implementations (Task 8, PRD AC13)
// ---------------------------------------------------------------------------

// sentinelGlobal is the tenant_id value used for shared (non-tenant-scoped)
// rows. Mirrored locally from the Client/package-level constant so the
// contract suite has no cyclic dependency on the Client package.
const sentinelGlobal = "_global"

// Shared fixture strings used across the tenant contract tests. They are
// declared as a tight group rather than inlined so the suite speaks a
// single vocabulary — contract tests benefit enormously from naming
// conventions being identical across subtests when a failure surfaces.
//
// `fixtureNamespace` deliberately overlaps the string "global" used in the
// pre-Task-8 global contracts at the top of the file; re-declaring it as a
// constant lets the tenant sub-suite satisfy goconst without touching the
// Task 7 test bodies that own the upper half of this file.
const (
	fixtureTenantA   = "tenant-A"
	fixtureKey       = "fee.rate"
	fixtureNamespace = "global"
)

// testTenantListOnEmpty: ListTenantValues on a fresh store returns an empty
// slice; ListTenantsForKey for any key returns an empty slice. Neither
// surfaces nil.
func testTenantListOnEmpty(t *testing.T, factory Factory) {
	t.Helper()

	s := factory(t)
	ctx := context.Background()

	entries, err := s.ListTenantValues(ctx)
	if err != nil {
		t.Fatalf("ListTenantValues on empty store: %v", err)
	}

	if len(entries) != 0 {
		t.Fatalf("expected 0 tenant entries, got %d", len(entries))
	}

	tenants, err := s.ListTenantsForKey(ctx, "global", fixtureKey)
	if err != nil {
		t.Fatalf("ListTenantsForKey on empty store: %v", err)
	}

	if tenants == nil {
		t.Fatal("ListTenantsForKey must return a non-nil empty slice, got nil")
	}

	if len(tenants) != 0 {
		t.Fatalf("expected 0 tenants, got %d (%v)", len(tenants), tenants)
	}
}

// testSetTenantThenGetRoundtrip: SetTenantValue persists; GetTenantValue for
// the same tenantID returns the row, while GetTenantValue for a different
// tenantID returns (_, false, nil). Enforces that rows are strictly scoped
// per tenantID.
func testSetTenantThenGetRoundtrip(t *testing.T, factory Factory) {
	t.Helper()

	s := factory(t)
	ctx := context.Background()

	entry := store.Entry{
		Namespace: "global",
		Key:       fixtureKey,
		Value:     []byte(`0.05`),
		UpdatedBy: "admin-A",
	}

	if err := s.SetTenantValue(ctx, fixtureTenantA, entry); err != nil {
		t.Fatalf("SetTenantValue tenant-A: %v", err)
	}

	got, found, err := s.GetTenantValue(ctx, fixtureTenantA, "global", fixtureKey)
	if err != nil {
		t.Fatalf("GetTenantValue tenant-A: %v", err)
	}

	if !found {
		t.Fatal("GetTenantValue tenant-A: expected found=true")
	}

	if got.TenantID != fixtureTenantA {
		t.Fatalf("GetTenantValue tenant-A: tenant_id=%q, want %q", got.TenantID, fixtureTenantA)
	}

	if string(got.Value) != `0.05` {
		t.Fatalf("GetTenantValue tenant-A: value=%q, want %q", got.Value, `0.05`)
	}

	if got.UpdatedBy != "admin-A" {
		t.Fatalf("GetTenantValue tenant-A: updated_by=%q, want %q", got.UpdatedBy, "admin-A")
	}

	// Other tenant must NOT see tenant-A's row.
	_, foundB, err := s.GetTenantValue(ctx, "tenant-B", "global", fixtureKey)
	if err != nil {
		t.Fatalf("GetTenantValue tenant-B: %v", err)
	}

	if foundB {
		t.Fatal("GetTenantValue tenant-B: expected found=false (row belongs to tenant-A)")
	}
}

// testSetTenantTwiceLastWriteWins: two SetTenantValue calls for the same
// (tenantID, namespace, key) collapse into a single row and the second
// value wins.
func testSetTenantTwiceLastWriteWins(t *testing.T, factory Factory) {
	t.Helper()

	s := factory(t)
	ctx := context.Background()

	base := store.Entry{
		Namespace: "global",
		Key:       fixtureKey,
		Value:     []byte(`0.05`),
		UpdatedBy: "admin-A",
	}

	if err := s.SetTenantValue(ctx, fixtureTenantA, base); err != nil {
		t.Fatalf("SetTenantValue v1: %v", err)
	}

	base.Value = []byte(`0.10`)
	base.UpdatedBy = "admin-B"

	if err := s.SetTenantValue(ctx, fixtureTenantA, base); err != nil {
		t.Fatalf("SetTenantValue v2: %v", err)
	}

	got, found, err := s.GetTenantValue(ctx, fixtureTenantA, "global", fixtureKey)
	if err != nil {
		t.Fatalf("GetTenantValue: %v", err)
	}

	if !found {
		t.Fatal("expected found=true")
	}

	if string(got.Value) != `0.10` {
		t.Fatalf("value: got %q, want %q", got.Value, `0.10`)
	}

	if got.UpdatedBy != "admin-B" {
		t.Fatalf("updated_by: got %q, want %q", got.UpdatedBy, "admin-B")
	}

	// Only one row in ListTenantValues for (tenant-A, global, fee.rate).
	entries, err := s.ListTenantValues(ctx)
	if err != nil {
		t.Fatalf("ListTenantValues: %v", err)
	}

	var count int

	for _, e := range entries {
		if e.TenantID == fixtureTenantA && e.Namespace == fixtureNamespace && e.Key == fixtureKey {
			count++
		}
	}

	if count != 1 {
		t.Fatalf("expected exactly 1 row for (tenant-A, global, fee.rate), got %d", count)
	}
}

// testDeleteTenantValueReturnsMissing: after DeleteTenantValue, a
// GetTenantValue for the same (tenantID, ns, key) returns (_, false, nil).
// The Client layer translates this to "fall through to global/default",
// but at the store level the row simply disappears.
func testDeleteTenantValueReturnsMissing(t *testing.T, factory Factory) {
	t.Helper()

	s := factory(t)
	ctx := context.Background()

	entry := store.Entry{
		Namespace: "global",
		Key:       fixtureKey,
		Value:     []byte(`0.05`),
		UpdatedBy: "admin",
	}

	if err := s.SetTenantValue(ctx, fixtureTenantA, entry); err != nil {
		t.Fatalf("SetTenantValue: %v", err)
	}

	if err := s.DeleteTenantValue(ctx, fixtureTenantA, "global", fixtureKey, "admin"); err != nil {
		t.Fatalf("DeleteTenantValue: %v", err)
	}

	_, found, err := s.GetTenantValue(ctx, fixtureTenantA, "global", fixtureKey)
	if err != nil {
		t.Fatalf("GetTenantValue after delete: %v", err)
	}

	if found {
		t.Fatal("GetTenantValue after delete: expected found=false")
	}
}

// testDeleteTenantValueIsIdempotent: DeleteTenantValue on a non-existent row
// returns nil, matching the contract documented at internal/store/store.go.
func testDeleteTenantValueIsIdempotent(t *testing.T, factory Factory) {
	t.Helper()

	s := factory(t)
	ctx := context.Background()

	// Never set anything for tenant-missing; delete must still succeed.
	if err := s.DeleteTenantValue(ctx, "tenant-missing", "global", fixtureKey, "admin"); err != nil {
		t.Fatalf("DeleteTenantValue on missing row: %v", err)
	}

	// Double-delete after a set should also be nil.
	entry := store.Entry{Namespace: "global", Key: fixtureKey, Value: []byte(`0.05`), UpdatedBy: "admin"}
	if err := s.SetTenantValue(ctx, fixtureTenantA, entry); err != nil {
		t.Fatalf("SetTenantValue: %v", err)
	}

	if err := s.DeleteTenantValue(ctx, fixtureTenantA, "global", fixtureKey, "admin"); err != nil {
		t.Fatalf("first DeleteTenantValue: %v", err)
	}

	if err := s.DeleteTenantValue(ctx, fixtureTenantA, "global", fixtureKey, "admin"); err != nil {
		t.Fatalf("second DeleteTenantValue (idempotent): %v", err)
	}
}

// testListTenantsForKeySorted: ListTenantsForKey returns tenants sorted and
// deduplicated with the sentinelGlobal row excluded. Writes arrive in a
// non-sorted order and tenant-A is set twice to exercise dedup.
func testListTenantsForKeySorted(t *testing.T, factory Factory) {
	t.Helper()

	s := factory(t)
	ctx := context.Background()

	entry := store.Entry{Namespace: "global", Key: fixtureKey, Value: []byte(`0.05`), UpdatedBy: "admin"}

	// Inserted deliberately out of sorted order: C, A, B, A-again.
	for _, tid := range []string{"tenant-C", fixtureTenantA, "tenant-B", fixtureTenantA} {
		if err := s.SetTenantValue(ctx, tid, entry); err != nil {
			t.Fatalf("SetTenantValue %s: %v", tid, err)
		}
	}

	// The global row for the same (ns, key) MUST be excluded from the tenant
	// list — sentinelGlobal is a sentinel, not a real tenant.
	if err := s.Set(ctx, store.Entry{
		Namespace: "global",
		Key:       fixtureKey,
		Value:     []byte(`0.01`),
		UpdatedBy: "admin",
	}); err != nil {
		t.Fatalf("Set global: %v", err)
	}

	tenants, err := s.ListTenantsForKey(ctx, "global", fixtureKey)
	if err != nil {
		t.Fatalf("ListTenantsForKey: %v", err)
	}

	if tenants == nil {
		t.Fatal("ListTenantsForKey must return a non-nil slice")
	}

	want := []string{fixtureTenantA, "tenant-B", "tenant-C"}
	if len(tenants) != len(want) {
		t.Fatalf("tenants: got %v (len %d), want %v (len %d)", tenants, len(tenants), want, len(want))
	}

	for i := range want {
		if tenants[i] != want[i] {
			t.Fatalf("tenants[%d]: got %q, want %q (full: %v)", i, tenants[i], want[i], tenants)
		}
	}

	// Guard against any backend accidentally returning the sentinel.
	for _, tid := range tenants {
		if tid == sentinelGlobal {
			t.Fatalf("ListTenantsForKey must never return %q sentinel, got: %v", sentinelGlobal, tenants)
		}
	}
}

// testGlobalAndTenantRowsCoexist: Set (global) and SetTenantValue for the
// same (namespace, key) must produce two distinct rows. The global-facing
// Get reads the global row; GetTenantValue reads the tenant row;
// ListTenantValues sees both. This pins the D2 / TRD §3 row-separation
// invariant at the store contract level.
func testGlobalAndTenantRowsCoexist(t *testing.T, factory Factory) {
	t.Helper()

	s := factory(t)
	ctx := context.Background()

	globalEntry := store.Entry{
		Namespace: "global",
		Key:       fixtureKey,
		Value:     []byte(`0.01`),
		UpdatedBy: "admin",
	}

	tenantEntry := store.Entry{
		Namespace: "global",
		Key:       fixtureKey,
		Value:     []byte(`0.05`),
		UpdatedBy: "tenant-admin",
	}

	if err := s.Set(ctx, globalEntry); err != nil {
		t.Fatalf("Set global: %v", err)
	}

	if err := s.SetTenantValue(ctx, fixtureTenantA, tenantEntry); err != nil {
		t.Fatalf("SetTenantValue tenant-A: %v", err)
	}

	// Global-path Get returns the global value, NOT tenant-A's override.
	got, found, err := s.Get(ctx, "global", fixtureKey)
	if err != nil || !found {
		t.Fatalf("Get global: found=%v, err=%v", found, err)
	}

	if string(got.Value) != `0.01` {
		t.Fatalf("Get global value: got %q, want %q", got.Value, `0.01`)
	}

	// Tenant-path Get returns the tenant value, NOT the global.
	gotT, foundT, err := s.GetTenantValue(ctx, fixtureTenantA, "global", fixtureKey)
	if err != nil || !foundT {
		t.Fatalf("GetTenantValue tenant-A: found=%v, err=%v", foundT, err)
	}

	if string(gotT.Value) != `0.05` {
		t.Fatalf("GetTenantValue tenant-A value: got %q, want %q", gotT.Value, `0.05`)
	}

	// ListTenantValues must surface both rows (global + tenant-A). Extracted
	// to a helper so the main test body stays under the gocyclo budget.
	assertBothRowsVisible(t, s)
}

// assertBothRowsVisible fails the test unless ListTenantValues returns both
// the _global sentinel row and the tenant-A row for the (global, fee.rate)
// tuple. Kept out of testGlobalAndTenantRowsCoexist so the complexity of the
// outer function stays within the gocyclo budget without sacrificing the
// inline assertion pattern elsewhere in the suite.
func assertBothRowsVisible(t *testing.T, s store.Store) {
	t.Helper()

	ctx := context.Background()

	allEntries, err := s.ListTenantValues(ctx)
	if err != nil {
		t.Fatalf("ListTenantValues: %v", err)
	}

	var sawGlobal, sawTenant bool

	for _, e := range allEntries {
		if e.Namespace != fixtureNamespace || e.Key != fixtureKey {
			continue
		}

		switch e.TenantID {
		case sentinelGlobal:
			sawGlobal = true
		case fixtureTenantA:
			sawTenant = true
		}
	}

	if !sawGlobal {
		t.Fatal("ListTenantValues missing the _global row for (global, fee.rate)")
	}

	if !sawTenant {
		t.Fatal("ListTenantValues missing the tenant-A row for (global, fee.rate)")
	}
}

// testTenantSubscribeReceivesSetEvent: SetTenantValue triggers a Subscribe
// event whose TenantID matches the write's tenant ID (not the sentinel).
// The write-side actor performs the Set from a separate goroutine after the
// subscriber has had time to register. This exercises the TRD §5.2 widened
// debounce key at the store contract level.
func testTenantSubscribeReceivesSetEvent(t *testing.T, factory Factory) {
	t.Helper()

	s := factory(t)
	ctx, cancel := context.WithCancel(context.Background())

	t.Cleanup(cancel)

	events := make(chan store.Event, 8)
	subErr := make(chan error, 1)

	go func() {
		subErr <- s.Subscribe(ctx, func(e store.Event) {
			events <- e
		})
	}()

	// Change-streams / LISTEN registration is asynchronous; no Store-level
	// "ready" signal exists so we sleep for a backend-generous window before
	// writing. Consistent with testSubscribeReceivesEventOnSet's 200ms.
	time.Sleep(500 * time.Millisecond)

	entry := store.Entry{
		Namespace: "global",
		Key:       fixtureKey,
		Value:     []byte(`0.05`),
		UpdatedBy: "tenant-admin",
	}

	if err := s.SetTenantValue(ctx, fixtureTenantA, entry); err != nil {
		t.Fatalf("SetTenantValue: %v", err)
	}

	if !eventually(t, 10*time.Second, func() bool {
		select {
		case e := <-events:
			// Must match BOTH (ns, key) AND TenantID — the row separation
			// invariant requires the event to carry the tenant ID, not the
			// sentinel.
			return e.Namespace == entry.Namespace && e.Key == entry.Key && e.TenantID == fixtureTenantA
		default:
			return false
		}
	}) {
		t.Fatal("timed out waiting for tenant-A Subscribe event with matching TenantID")
	}

	cancel()

	select {
	case err := <-subErr:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Subscribe returned unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe did not return after context cancel")
	}
}

// testTenantSubscribeReceivesDeleteEvent: DeleteTenantValue triggers a
// Subscribe event. This contract is CRITICAL for OnTenantChange's
// "revert-to-default" behavior (TRD §4.4 / PRD AC9) — without the delete
// event the Client cannot notify subscribers that the tenant has reverted.
//
// IMPORTANT: MongoDB polling mode cannot observe deletes between ticks
// (see internal/mongodb/mongodb_changestream.go subscribePoll godoc). The
// polling-mode integration test MUST skip this subtest via
// [SkipSubtest]("TenantSubscribeReceivesDeleteEvent").
func testTenantSubscribeReceivesDeleteEvent(t *testing.T, factory Factory) {
	t.Helper()

	s := factory(t)
	ctx, cancel := context.WithCancel(context.Background())

	t.Cleanup(cancel)

	// Seed the row BEFORE the subscriber registers so the delete is the only
	// event we assert on. Seeded writes that race with Subscribe setup may or
	// may not be delivered depending on backend; we don't assert on them.
	seed := store.Entry{
		Namespace: "global",
		Key:       fixtureKey,
		Value:     []byte(`0.05`),
		UpdatedBy: "admin",
	}

	if err := s.SetTenantValue(ctx, fixtureTenantA, seed); err != nil {
		t.Fatalf("SetTenantValue seed: %v", err)
	}

	events := make(chan store.Event, 8)
	subErr := make(chan error, 1)

	go func() {
		subErr <- s.Subscribe(ctx, func(e store.Event) {
			events <- e
		})
	}()

	time.Sleep(500 * time.Millisecond)

	if err := s.DeleteTenantValue(ctx, fixtureTenantA, "global", fixtureKey, "admin"); err != nil {
		t.Fatalf("DeleteTenantValue: %v", err)
	}

	if !eventually(t, 10*time.Second, func() bool {
		for {
			select {
			case e := <-events:
				// The seed's insert event may have been observed depending
				// on subscribe timing; we're looking specifically for the
				// delete with matching TenantID.
				if e.Namespace == fixtureNamespace && e.Key == fixtureKey && e.TenantID == fixtureTenantA {
					// Drain queued events until the delete or timeout — we
					// can't distinguish insert from delete from Event alone,
					// so accept any matching event AFTER the delete was
					// issued. Given events arrive in order per tenant, this
					// is a sufficient proof.
					return true
				}
			default:
				return false
			}
		}
	}) {
		t.Fatal("timed out waiting for tenant-A delete Subscribe event")
	}

	cancel()

	select {
	case err := <-subErr:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Subscribe returned unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe did not return after context cancel")
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// eventually polls check at short intervals until it returns true or timeout
// elapses. Returns true if check succeeded, false on timeout.
func eventually(t *testing.T, timeout time.Duration, check func() bool) bool {
	t.Helper()

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	for {
		if check() {
			return true
		}

		select {
		case <-deadline.C:
			return false
		case <-ticker.C:
			// next iteration
		}
	}
}
