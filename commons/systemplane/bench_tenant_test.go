//go:build unit

// Package systemplane — performance benchmarks for tenant-scoped reads.
//
// PRD AC15 targets:
//
//	BenchmarkGetForTenant_Hit        < 1µs   (eager mode cache hit)
//	BenchmarkGetForTenant_Miss_Eager < 2µs   (cache miss → global/default fallthrough, no DB call)
//
// The lazy-mode miss path is NOT sub-microsecond by design: it triggers a
// bounded-timeout store.GetTenantValue round-trip (TRD §8). We still
// benchmark it against an in-memory TestStore so the code-path overhead is
// visible, but make it clear the number is not comparable to the hit path.
//
// Informational benchmarks (no target, for regression tracking):
//
//	BenchmarkSetForTenant                — write-through cache Set
//	BenchmarkOnTenantChange_FireFanout_10 — dispatch fanout with 10 subscribers
package systemplane

import (
	"context"
	"sort"
	"sync"
	"testing"

	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
)

// benchTenantStore is a minimal, concurrency-safe TestStore used purely for
// benchmarks. It mirrors the tenantTestStore in tenant_scoped_smoke_test.go
// but is kept local so benchmark builds stay decoupled from the smoke test
// file (avoiding any shared mutable state across tests).
type benchTenantStore struct {
	mu   sync.Mutex
	rows map[benchKey]TestEntry
}

type benchKey struct {
	tenantID  string
	namespace string
	key       string
}

func newBenchTenantStore() *benchTenantStore {
	return &benchTenantStore{rows: make(map[benchKey]TestEntry)}
}

func (s *benchTenantStore) List(_ context.Context) ([]TestEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]TestEntry, 0, len(s.rows))

	for _, e := range s.rows {
		if e.TenantID == "_global" {
			out = append(out, e)
		}
	}

	return out, nil
}

func (s *benchTenantStore) Get(_ context.Context, namespace, key string) (TestEntry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.rows[benchKey{tenantID: "_global", namespace: namespace, key: key}]

	return e, ok, nil
}

func (s *benchTenantStore) Set(_ context.Context, e TestEntry) error {
	e.TenantID = "_global"

	s.mu.Lock()
	s.rows[benchKey{tenantID: "_global", namespace: e.Namespace, key: e.Key}] = e
	s.mu.Unlock()

	return nil
}

func (s *benchTenantStore) Subscribe(ctx context.Context, _ func(TestEvent)) error {
	<-ctx.Done()

	return nil
}

func (s *benchTenantStore) Close() error { return nil }

func (s *benchTenantStore) GetTenantValue(_ context.Context, tenantID, namespace, key string) (TestEntry, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	e, ok := s.rows[benchKey{tenantID: tenantID, namespace: namespace, key: key}]

	return e, ok, nil
}

func (s *benchTenantStore) SetTenantValue(_ context.Context, tenantID string, e TestEntry) error {
	e.TenantID = tenantID

	s.mu.Lock()
	s.rows[benchKey{tenantID: tenantID, namespace: e.Namespace, key: e.Key}] = e
	s.mu.Unlock()

	return nil
}

func (s *benchTenantStore) DeleteTenantValue(_ context.Context, tenantID, namespace, key, _ string) error {
	s.mu.Lock()
	delete(s.rows, benchKey{tenantID: tenantID, namespace: namespace, key: key})
	s.mu.Unlock()

	return nil
}

func (s *benchTenantStore) ListTenantValues(_ context.Context) ([]TestEntry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	out := make([]TestEntry, 0, len(s.rows))
	for _, e := range s.rows {
		out = append(out, e)
	}

	return out, nil
}

func (s *benchTenantStore) ListTenantsForKey(_ context.Context, namespace, key string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

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

// newBenchClient returns a started Client wired to a fresh benchTenantStore,
// with (namespace, key) registered as tenant-scoped using defaultValue.
// Debouncing is disabled (NewForTesting default) so no background timers
// interfere with the b.N loop.
func newBenchClient(b *testing.B, namespace, key string, defaultValue any, opts ...Option) (*Client, *benchTenantStore) {
	b.Helper()

	ts := newBenchTenantStore()

	c, err := NewForTesting(ts, opts...)
	if err != nil {
		b.Fatalf("NewForTesting: %v", err)
	}

	if err := c.RegisterTenantScoped(namespace, key, defaultValue); err != nil {
		b.Fatalf("RegisterTenantScoped: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		b.Fatalf("Start: %v", err)
	}

	b.Cleanup(func() { _ = c.Close() })

	return c, ts
}

// BenchmarkGetForTenant_Hit measures the eager-mode hot path:
// tenantCache hit, no backend touch.
//
// Target (PRD AC15): < 1µs per op.
func BenchmarkGetForTenant_Hit(b *testing.B) {
	c, _ := newBenchClient(b, "global", "fee.rate", 0.05)

	ctx := core.ContextWithTenantID(context.Background(), "tenant-A")

	// Prime the tenantCache via the write-through path. After SetForTenant
	// returns, tenantCache[tenant-A][global/fee.rate] is populated with the
	// canonical (JSON-round-tripped) value.
	if err := c.SetForTenant(ctx, "global", "fee.rate", 0.10, "bench"); err != nil {
		b.Fatalf("SetForTenant prime: %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _, _ = c.GetForTenant(ctx, "global", "fee.rate")
	}
}

// BenchmarkGetForTenant_Miss_Eager measures the eager-mode fallthrough:
// tenantCache miss → legacy global cache hit (or registered default).
// No backend call; this exercises the full registry + cache lookup without
// any override ever having been written for the tenant.
//
// Target (PRD AC15): < 2µs per op.
func BenchmarkGetForTenant_Miss_Eager(b *testing.B) {
	c, _ := newBenchClient(b, "global", "fee.rate", 0.05)

	ctx := core.ContextWithTenantID(context.Background(), "tenant-B")

	// Do NOT prime any tenant override; reads for tenant-B must fall
	// through to the legacy global cache (which holds the default after
	// RegisterTenantScoped seeds it at tenant_scoped.go:119-121).

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _, _ = c.GetForTenant(ctx, "global", "fee.rate")
	}
}

// BenchmarkGetForTenant_Miss_Lazy measures the lazy-mode miss path against
// an in-memory TestStore. This is NOT the AC15 sub-microsecond target
// path — the backend is consulted on every miss. The benchmark is
// informational: it tracks the code-path overhead (ctx timeout, JSON
// unmarshal, LRU populate) so regressions are visible.
//
// Expected magnitude: tens of microseconds with an in-memory store (mostly
// context.WithTimeout + goroutine-safe lock cost). With a real database
// this inflates to 5-10ms (see TRD §8).
func BenchmarkGetForTenant_Miss_Lazy(b *testing.B) {
	c, _ := newBenchClient(b, "global", "fee.rate", 0.05, WithLazyTenantLoad(100))

	ctx := core.ContextWithTenantID(context.Background(), "tenant-C")
	// No override set — every iteration misses the LRU and falls through
	// to the backend (which returns not-found), then to the legacy global.

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _, _ = c.GetForTenant(ctx, "global", "fee.rate")
	}
}

// BenchmarkSetForTenant measures the write-through path: validator run,
// JSON marshal, store.SetTenantValue, canonical round-trip, cacheMu.Lock.
// Against the in-memory TestStore the bottleneck is json.Marshal +
// json.Unmarshal; against a real backend this is dominated by network.
func BenchmarkSetForTenant(b *testing.B) {
	c, _ := newBenchClient(b, "global", "fee.rate", 0.0)

	ctx := core.ContextWithTenantID(context.Background(), "tenant-A")

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = c.SetForTenant(ctx, "global", "fee.rate", float64(i), "bench")
	}
}

// BenchmarkOnTenantChange_FireFanout_10 measures fireTenantSubscribers with
// 10 registered no-op subscribers. This isolates the dispatch cost (slice
// copy + per-subscriber panic shield + fn invocation) from the end-to-end
// Set → changefeed → refresh → fire path, which is dominated by backend
// latency in production.
//
// Informational: no hard target. Should scale linearly in subscriber count.
func BenchmarkOnTenantChange_FireFanout_10(b *testing.B) {
	c, _ := newBenchClient(b, "global", "fee.rate", 0.0)

	const numSubs = 10

	unsubs := make([]func(), numSubs)

	for i := 0; i < numSubs; i++ {
		unsubs[i] = c.OnTenantChange("global", "fee.rate", func(_, _, _ string, _ any) {
			// no-op
		})
	}

	b.Cleanup(func() {
		for _, u := range unsubs {
			u()
		}
	})

	nk := nskey{Namespace: "global", Key: "fee.rate"}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		c.fireTenantSubscribers(nk, "tenant-A", 0.05)
	}
}

// BenchmarkListTenantsForKey measures the ListTenantsForKey read path:
// requireTenantScoped registry check + c.store.ListTenantsForKey.
// Informational — scales with the count of distinct tenants holding an
// override for the key.
func BenchmarkListTenantsForKey(b *testing.B) {
	c, _ := newBenchClient(b, "global", "fee.rate", 0.0)

	// Seed 50 tenants so the call returns a meaningfully sized slice.
	for i := 0; i < 50; i++ {
		ctx := core.ContextWithTenantID(context.Background(), tenantName(i))
		if err := c.SetForTenant(ctx, "global", "fee.rate", float64(i), "bench"); err != nil {
			b.Fatalf("SetForTenant prime[%d]: %v", i, err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_ = c.ListTenantsForKey("global", "fee.rate")
	}
}

// tenantName returns a stable tenant ID for benchmark priming. Declared as
// a helper so future benchmarks can reuse the same naming scheme.
func tenantName(n int) string {
	// Simple alphanumeric so core.IsValidTenantID passes.
	return "tenant-" + itoa(n)
}

// itoa avoids importing strconv solely for a 5-digit number formatter.
// Benchmarks are hot paths and we keep allocations explicit.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}

	var buf [20]byte

	i := len(buf)

	neg := n < 0
	if neg {
		n = -n
	}

	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}

	if neg {
		i--
		buf[i] = '-'
	}

	return string(buf[i:])
}
