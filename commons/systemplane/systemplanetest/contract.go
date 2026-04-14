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

	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/internal/store"
)

// Factory constructs a fresh, isolated Store for a single test.
// Must be safe to call multiple times per *testing.T (each call yields a
// new Store with its own empty state). Implementations typically register
// a t.Cleanup to tear down containers, databases, etc.
type Factory func(t *testing.T) store.Store

// Run executes the full contract test suite. Each sub-test receives a
// fresh Store via the factory. Contract tests block up to 10s for
// eventual-consistency operations (e.g., Subscribe delivery); individual
// tests use smaller deadlines where possible.
func Run(t *testing.T, factory Factory) {
	t.Helper()

	t.Run("ListOnEmpty", func(t *testing.T) {
		testListOnEmpty(t, factory)
	})
	t.Run("GetMissingReturnsFalse", func(t *testing.T) {
		testGetMissingReturnsFalse(t, factory)
	})
	t.Run("SetThenGetRoundtrip", func(t *testing.T) {
		testSetThenGetRoundtrip(t, factory)
	})
	t.Run("SetTwiceLastWriteWins", func(t *testing.T) {
		testSetTwiceLastWriteWins(t, factory)
	})
	t.Run("ListReturnsAllAfterMultipleSets", func(t *testing.T) {
		testListReturnsAllAfterMultipleSets(t, factory)
	})
	t.Run("SubscribeReceivesEventOnSet", func(t *testing.T) {
		testSubscribeReceivesEventOnSet(t, factory)
	})
	t.Run("SubscribeRespectsContextCancel", func(t *testing.T) {
		testSubscribeRespectsContextCancel(t, factory)
	})
	t.Run("CloseIsIdempotent", func(t *testing.T) {
		testCloseIsIdempotent(t, factory)
	})
	t.Run("OperationsAfterCloseReturnError", func(t *testing.T) {
		testOperationsAfterCloseReturnError(t, factory)
	})
	t.Run("NamespaceIsolation", func(t *testing.T) {
		testNamespaceIsolation(t, factory)
	})
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
