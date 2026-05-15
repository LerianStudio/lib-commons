// Package storetest provides internal backend contract tests for systemplane
// store implementations.
package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/store"
)

const (
	fixtureNamespace = "global"
	fixtureKey       = "fee.rate"
	fixtureTenantA   = "tenant-A"
	fixtureTenantB   = "tenant-B"
	fixtureTenantC   = "tenant-C"
	fixtureActor     = "admin"
	defaultSettle    = 500 * time.Millisecond
	eventDeadline    = 10 * time.Second
	noEventWindow    = 500 * time.Millisecond
)

// Factory constructs a fresh, isolated Store for a single contract subtest.
type Factory func(t *testing.T) store.Store

// Option configures RunTenantContracts.
type Option func(*config)

type config struct {
	skip   map[string]struct{}
	settle time.Duration
}

var legacySkipAliases = map[string][]string{
	"TenantOnChangeReceivesDelete": {"TenantSubscribeReceivesDeleteEvent"},
}

// SkipSubtest skips a named internal store contract subtest.
func SkipSubtest(name string) Option {
	return func(cfg *config) {
		if cfg.skip == nil {
			cfg.skip = make(map[string]struct{})
		}

		cfg.skip[name] = struct{}{}
		for _, alias := range legacySkipAliases[name] {
			cfg.skip[alias] = struct{}{}
		}
	}
}

// WithEventSettle configures the subscriber settle window before writes.
func WithEventSettle(d time.Duration) Option {
	return func(cfg *config) {
		if d >= 0 {
			cfg.settle = d
		}
	}
}

func (c *config) shouldSkip(name string) bool {
	if c == nil || c.skip == nil {
		return false
	}

	_, ok := c.skip[name]

	return ok
}

// RunTenantContracts executes Store-level tenant invariants that are not fully
// observable through the public systemplane.Client contract.
func RunTenantContracts(t *testing.T, factory Factory, opts ...Option) {
	t.Helper()

	cfg := &config{settle: defaultSettle}

	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}

	run := func(name string, fn func(*testing.T)) {
		t.Run(name, func(t *testing.T) {
			if cfg.shouldSkip(name) {
				t.Skipf("skipped by SkipSubtest(%q)", name)
			}

			fn(t)
		})
	}

	run("TenantListOnEmpty", func(t *testing.T) { testTenantListOnEmpty(t, factory) })
	run("SetTenantThenGetRoundtrip", func(t *testing.T) { testSetTenantThenGetRoundtrip(t, factory) })
	run("SetTenantTwiceLastWriteWins", func(t *testing.T) { testSetTenantTwiceLastWriteWins(t, factory) })
	run("DeleteTenantValueReturnsMissing", func(t *testing.T) { testDeleteTenantValueReturnsMissing(t, factory) })
	run("DeleteTenantValueIsIdempotent", func(t *testing.T) { testDeleteTenantValueIsIdempotent(t, factory) })
	run("ListTenantsForKeySorted", func(t *testing.T) { testListTenantsForKeySorted(t, factory) })
	run("GlobalAndTenantRowsCoexist", func(t *testing.T) { testGlobalAndTenantRowsCoexist(t, factory) })
	run("ListTenantOverrides_FiltersGlobalsServerSide", func(t *testing.T) { testListTenantOverridesFiltersGlobals(t, factory) })
	run("TenantSubscribeReceivesDeleteEvent", func(t *testing.T) { testTenantSubscribeReceivesDeleteEvent(t, factory, cfg.settle) })
	run("DeleteTenantValueNoOpEmitsNoEvent", func(t *testing.T) { testDeleteTenantValueNoOpEmitsNoEvent(t, factory, cfg.settle) })
}

type tenantValuesLister interface {
	ListTenantValues(ctx context.Context) ([]store.Entry, error)
}

func requireTenantValuesLister(t *testing.T, s store.Store) tenantValuesLister {
	t.Helper()

	lister, ok := s.(tenantValuesLister)
	if !ok {
		t.Skipf("backend %T does not expose ListTenantValues", s)
	}

	return lister
}

func testTenantListOnEmpty(t *testing.T, factory Factory) {
	t.Helper()

	s := newStore(t, factory)
	ctx := context.Background()

	entries, err := requireTenantValuesLister(t, s).ListTenantValues(ctx)
	if err != nil {
		t.Fatalf("ListTenantValues on empty store: %v", err)
	}

	if len(entries) != 0 {
		t.Fatalf("ListTenantValues length = %d, want 0", len(entries))
	}

	tenants, err := s.ListTenantsForKey(ctx, fixtureNamespace, fixtureKey)
	if err != nil {
		t.Fatalf("ListTenantsForKey on empty store: %v", err)
	}

	if tenants == nil || len(tenants) != 0 {
		t.Fatalf("ListTenantsForKey on empty store = %v, want non-nil empty", tenants)
	}
}

func testSetTenantThenGetRoundtrip(t *testing.T, factory Factory) {
	t.Helper()

	s := newStore(t, factory)
	ctx := context.Background()
	entry := store.Entry{Namespace: fixtureNamespace, Key: fixtureKey, Value: []byte(`0.05`), UpdatedBy: "admin-A"}

	if err := s.SetTenantValue(ctx, fixtureTenantA, entry); err != nil {
		t.Fatalf("SetTenantValue tenant-A: %v", err)
	}

	got, found, err := s.GetTenantValue(ctx, fixtureTenantA, fixtureNamespace, fixtureKey)
	if err != nil || !found || got.TenantID != fixtureTenantA || string(got.Value) != `0.05` {
		t.Fatalf("GetTenantValue tenant-A = (%+v, %v, %v), want tenant-A value 0.05", got, found, err)
	}

	_, foundB, err := s.GetTenantValue(ctx, fixtureTenantB, fixtureNamespace, fixtureKey)
	if err != nil || foundB {
		t.Fatalf("GetTenantValue tenant-B = found %v err %v, want missing", foundB, err)
	}
}

func testSetTenantTwiceLastWriteWins(t *testing.T, factory Factory) {
	t.Helper()

	s := newStore(t, factory)
	ctx := context.Background()
	entry := store.Entry{Namespace: fixtureNamespace, Key: fixtureKey, Value: []byte(`0.05`), UpdatedBy: "admin-A"}

	if err := s.SetTenantValue(ctx, fixtureTenantA, entry); err != nil {
		t.Fatalf("SetTenantValue v1: %v", err)
	}

	entry.Value = []byte(`0.10`)

	entry.UpdatedBy = "admin-B"
	if err := s.SetTenantValue(ctx, fixtureTenantA, entry); err != nil {
		t.Fatalf("SetTenantValue v2: %v", err)
	}

	got, found, err := s.GetTenantValue(ctx, fixtureTenantA, fixtureNamespace, fixtureKey)
	if err != nil || !found || string(got.Value) != `0.10` || got.UpdatedBy != "admin-B" {
		t.Fatalf("GetTenantValue after overwrite = (%+v, %v, %v), want value 0.10 by admin-B", got, found, err)
	}

	entries, err := requireTenantValuesLister(t, s).ListTenantValues(ctx)
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
		t.Fatalf("tenant row count = %d, want 1", count)
	}
}

func testDeleteTenantValueReturnsMissing(t *testing.T, factory Factory) {
	t.Helper()

	s := newStore(t, factory)
	ctx := context.Background()
	entry := store.Entry{Namespace: fixtureNamespace, Key: fixtureKey, Value: []byte(`0.05`), UpdatedBy: fixtureActor}

	if err := s.SetTenantValue(ctx, fixtureTenantA, entry); err != nil {
		t.Fatalf("SetTenantValue: %v", err)
	}

	if err := s.DeleteTenantValue(ctx, fixtureTenantA, fixtureNamespace, fixtureKey, fixtureActor); err != nil {
		t.Fatalf("DeleteTenantValue: %v", err)
	}

	_, found, err := s.GetTenantValue(ctx, fixtureTenantA, fixtureNamespace, fixtureKey)
	if err != nil || found {
		t.Fatalf("GetTenantValue after delete = found %v err %v, want missing", found, err)
	}
}

func testDeleteTenantValueIsIdempotent(t *testing.T, factory Factory) {
	t.Helper()

	s := newStore(t, factory)
	ctx := context.Background()
	entry := store.Entry{Namespace: fixtureNamespace, Key: fixtureKey, Value: []byte(`0.05`), UpdatedBy: fixtureActor}

	if err := s.DeleteTenantValue(ctx, "tenant-missing", fixtureNamespace, fixtureKey, fixtureActor); err != nil {
		t.Fatalf("DeleteTenantValue missing row: %v", err)
	}

	if err := s.SetTenantValue(ctx, fixtureTenantA, entry); err != nil {
		t.Fatalf("SetTenantValue: %v", err)
	}

	if err := s.DeleteTenantValue(ctx, fixtureTenantA, fixtureNamespace, fixtureKey, fixtureActor); err != nil {
		t.Fatalf("first DeleteTenantValue: %v", err)
	}

	if err := s.DeleteTenantValue(ctx, fixtureTenantA, fixtureNamespace, fixtureKey, fixtureActor); err != nil {
		t.Fatalf("second DeleteTenantValue: %v", err)
	}
}

func testListTenantsForKeySorted(t *testing.T, factory Factory) {
	t.Helper()

	s := newStore(t, factory)
	ctx := context.Background()
	entry := store.Entry{Namespace: fixtureNamespace, Key: fixtureKey, Value: []byte(`0.05`), UpdatedBy: fixtureActor}

	for _, tenantID := range []string{fixtureTenantC, fixtureTenantA, fixtureTenantB, fixtureTenantA} {
		if err := s.SetTenantValue(ctx, tenantID, entry); err != nil {
			t.Fatalf("SetTenantValue %s: %v", tenantID, err)
		}
	}

	if err := s.Set(ctx, store.Entry{Namespace: fixtureNamespace, Key: fixtureKey, Value: []byte(`0.01`), UpdatedBy: fixtureActor}); err != nil {
		t.Fatalf("Set global: %v", err)
	}

	tenants, err := s.ListTenantsForKey(ctx, fixtureNamespace, fixtureKey)
	if err != nil {
		t.Fatalf("ListTenantsForKey: %v", err)
	}

	want := []string{fixtureTenantA, fixtureTenantB, fixtureTenantC}
	if !stringSlicesEqual(tenants, want) {
		t.Fatalf("ListTenantsForKey = %v, want %v", tenants, want)
	}
}

func testGlobalAndTenantRowsCoexist(t *testing.T, factory Factory) {
	t.Helper()

	s := newStore(t, factory)
	ctx := context.Background()

	if err := s.Set(ctx, store.Entry{Namespace: fixtureNamespace, Key: fixtureKey, Value: []byte(`0.01`), UpdatedBy: fixtureActor}); err != nil {
		t.Fatalf("Set global: %v", err)
	}

	if err := s.SetTenantValue(ctx, fixtureTenantA, store.Entry{Namespace: fixtureNamespace, Key: fixtureKey, Value: []byte(`0.05`), UpdatedBy: "tenant-admin"}); err != nil {
		t.Fatalf("SetTenantValue tenant-A: %v", err)
	}

	got, found, err := s.Get(ctx, fixtureNamespace, fixtureKey)
	if err != nil || !found || string(got.Value) != `0.01` {
		t.Fatalf("Get global = (%+v, %v, %v), want value 0.01", got, found, err)
	}

	gotTenant, found, err := s.GetTenantValue(ctx, fixtureTenantA, fixtureNamespace, fixtureKey)
	if err != nil || !found || string(gotTenant.Value) != `0.05` {
		t.Fatalf("GetTenantValue = (%+v, %v, %v), want value 0.05", gotTenant, found, err)
	}

	assertBothRowsVisible(t, s)
}

func assertBothRowsVisible(t *testing.T, s store.Store) {
	t.Helper()

	entries, err := requireTenantValuesLister(t, s).ListTenantValues(context.Background())
	if err != nil {
		t.Fatalf("ListTenantValues: %v", err)
	}

	sawGlobal, sawTenant := rowsContainGlobalAndTenant(entries)
	if !sawGlobal || !sawTenant {
		t.Fatalf("ListTenantValues saw global=%v tenant=%v, want both", sawGlobal, sawTenant)
	}
}

func rowsContainGlobalAndTenant(entries []store.Entry) (bool, bool) {
	var sawGlobal, sawTenant bool

	for _, e := range entries {
		if e.Namespace != fixtureNamespace || e.Key != fixtureKey {
			continue
		}

		sawGlobal = sawGlobal || e.TenantID == store.SentinelGlobal
		sawTenant = sawTenant || e.TenantID == fixtureTenantA
	}

	return sawGlobal, sawTenant
}

func testListTenantOverridesFiltersGlobals(t *testing.T, factory Factory) {
	t.Helper()

	s := newStore(t, factory)
	ctx := context.Background()

	for _, e := range []store.Entry{
		{Namespace: fixtureNamespace, Key: "rate_limit", Value: []byte(`100`), UpdatedBy: fixtureActor},
		{Namespace: fixtureNamespace, Key: "log.level", Value: []byte(`"info"`), UpdatedBy: fixtureActor},
	} {
		if err := s.Set(ctx, e); err != nil {
			t.Fatalf("Set global %s/%s: %v", e.Namespace, e.Key, err)
		}
	}

	entry := store.Entry{Namespace: fixtureNamespace, Key: fixtureKey, Value: []byte(`0.05`), UpdatedBy: fixtureActor}
	for _, tenantID := range []string{fixtureTenantA, fixtureTenantB, fixtureTenantC} {
		if err := s.SetTenantValue(ctx, tenantID, entry); err != nil {
			t.Fatalf("SetTenantValue %s: %v", tenantID, err)
		}
	}

	got, err := s.ListTenantOverrides(ctx, "", "", "", 0)
	if err != nil {
		t.Fatalf("ListTenantOverrides: %v", err)
	}

	if len(got) != 3 {
		t.Fatalf("ListTenantOverrides length = %d, want 3: %+v", len(got), got)
	}

	for _, e := range got {
		if e.TenantID == store.SentinelGlobal {
			t.Fatalf("ListTenantOverrides returned sentinel row: %+v", e)
		}
	}
}

func testTenantSubscribeReceivesDeleteEvent(t *testing.T, factory Factory, settle time.Duration) {
	t.Helper()

	s := newStore(t, factory)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	seed := store.Entry{Namespace: fixtureNamespace, Key: fixtureKey, Value: []byte(`0.05`), UpdatedBy: fixtureActor}
	if err := s.SetTenantValue(ctx, fixtureTenantA, seed); err != nil {
		t.Fatalf("SetTenantValue seed: %v", err)
	}

	events := make(chan store.Event, 8)
	subErr := make(chan error, 1)

	go func() {
		subErr <- s.Subscribe(ctx, func(e store.Event) { events <- e })
	}()

	settleChangefeed(settle)
	drainEvents(events)

	if err := s.DeleteTenantValue(ctx, fixtureTenantA, fixtureNamespace, fixtureKey, fixtureActor); err != nil {
		t.Fatalf("DeleteTenantValue: %v", err)
	}

	if !eventually(eventDeadline, func() bool {
		for {
			select {
			case e := <-events:
				if e.Namespace == fixtureNamespace && e.Key == fixtureKey && e.TenantID == fixtureTenantA {
					return true
				}
			default:
				return false
			}
		}
	}) {
		t.Fatal("timed out waiting for tenant delete event")
	}

	cancel()
	assertSubscribeTerminated(t, subErr)
}

func testDeleteTenantValueNoOpEmitsNoEvent(t *testing.T, factory Factory, settle time.Duration) {
	t.Helper()

	s := newStore(t, factory)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	events := make(chan store.Event, 8)
	subErr := make(chan error, 1)

	go func() {
		subErr <- s.Subscribe(ctx, func(e store.Event) { events <- e })
	}()

	settleChangefeed(settle)

	if err := s.DeleteTenantValue(ctx, "tenant-never", fixtureNamespace, fixtureKey, fixtureActor); err != nil {
		t.Fatalf("DeleteTenantValue no-op: %v", err)
	}

	timer := time.NewTimer(noEventWindow)
	defer timer.Stop()

	select {
	case e := <-events:
		t.Fatalf("no-op DeleteTenantValue emitted unexpected event: %+v", e)
	case <-timer.C:
	}

	cancel()
	assertSubscribeTerminated(t, subErr)
}

func newStore(t *testing.T, factory Factory) store.Store {
	t.Helper()

	if factory == nil {
		t.Fatal("nil store Factory")
	}

	s := factory(t)
	if s == nil {
		t.Fatal("store Factory returned nil")
	}

	return s
}

func settleChangefeed(d time.Duration) {
	if d > 0 {
		time.Sleep(d)
	}
}

func drainEvents(events <-chan store.Event) {
	for {
		select {
		case <-events:
		default:
			return
		}
	}
}

func assertSubscribeTerminated(t *testing.T, errCh <-chan error) {
	t.Helper()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Subscribe returned unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Subscribe did not return after context cancel")
	}
}

func eventually(timeout time.Duration, check func() bool) bool {
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
		}
	}
}

func stringSlicesEqual(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}

	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}

	return true
}
