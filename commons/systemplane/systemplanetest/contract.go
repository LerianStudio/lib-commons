// Package systemplanetest provides backend-agnostic contract tests for the
// public systemplane Client API. Service repositories can run this suite
// against their real systemplane backend without importing lib-commons internal
// packages.
package systemplanetest

import (
	"context"
	"errors"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/systemplane"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
)

const (
	contractNamespace  = "systemplanetest"
	defaultEventSettle = 500 * time.Millisecond
	eventDeadline      = 10 * time.Second
	noEventWindow      = 500 * time.Millisecond
	debugValue         = "debug"
	tenantRateKey      = "tenant_rate"
	tenantA            = "tenant-A"
	tenantB            = "tenant-B"
	tenantC            = "tenant-C"
	tenantRateDefault  = 0.10
)

// ClientFactory constructs a fresh, isolated Client for a single contract
// subtest. The client must be backed by the real backend under test. The
// factory should register cleanup for external resources such as database
// handles, containers, or temporary schemas.
type ClientFactory func(t *testing.T) *systemplane.Client

// Factory is kept as a short alias for callers that already use the generic
// "factory" naming convention. It is intentionally a public Client factory,
// not an internal store factory.
type Factory = ClientFactory

// RunOption configures the behavior of [Run].
type RunOption interface {
	applyRunOption(cfg *runConfig)
}

type runOptionFunc func(*runConfig)

func (fn runOptionFunc) applyRunOption(cfg *runConfig) {
	fn(cfg)
}

// runConfig carries the options accumulated via [RunOption] calls.
type runConfig struct {
	skip        map[string]struct{}
	eventSettle time.Duration
}

var legacySkipAliases = map[string][]string{
	"TenantSubscribeReceivesSetEvent":    {"TenantOnChangeReceivesSet"},
	"TenantSubscribeReceivesDeleteEvent": {"TenantOnChangeReceivesDelete"},
	"TenantOnChangeReceivesSetAndDelete": {"TenantOnChangeReceivesSet", "TenantOnChangeReceivesDelete"},
}

// SkipSubtest instructs [Run] to call t.Skip on the named subtest. Useful when
// a specific backend topology cannot satisfy a particular contract. The name
// must match the t.Run subtest name exactly. Legacy Store-level tenant event
// names are mapped to their Client-level equivalents so older polling-mode
// callers keep skipping the same behavioral gap.
func SkipSubtest(name string) RunOption {
	return runOptionFunc(func(cfg *runConfig) {
		if cfg.skip == nil {
			cfg.skip = make(map[string]struct{})
		}

		cfg.skip[name] = struct{}{}
		for _, alias := range legacySkipAliases[name] {
			cfg.skip[alias] = struct{}{}
		}
	})
}

// WithEventSettle configures how long event-driven contract tests wait after
// registering subscribers before issuing writes. Backends with synchronous or
// explicitly-ready changefeeds may pass zero to avoid unnecessary sleep.
func WithEventSettle(d time.Duration) RunOption {
	return runOptionFunc(func(cfg *runConfig) {
		if d >= 0 {
			cfg.eventSettle = d
		}
	})
}

// shouldSkip reports whether the named subtest is in the skip set.
func (c *runConfig) shouldSkip(name string) bool {
	if c == nil || c.skip == nil {
		return false
	}

	_, ok := c.skip[name]

	return ok
}

// Run executes the public systemplane Client contract suite. Every subtest gets
// a fresh Client from factory and uses only public v5 API surface from
// github.com/LerianStudio/lib-commons/v5/commons/systemplane.
func Run(t *testing.T, factory ClientFactory, opts ...RunOption) {
	t.Helper()

	cfg := &runConfig{eventSettle: defaultEventSettle}

	for _, opt := range opts {
		if opt != nil {
			opt.applyRunOption(cfg)
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

	run("RegisterBeforeStart", func(t *testing.T) { testRegisterBeforeStart(t, factory) })
	run("StartAndCloseLifecycle", func(t *testing.T) { testStartAndCloseLifecycle(t, factory) })
	run("TypedReads", func(t *testing.T) { testTypedReads(t, factory) })
	run("SetPersistence", func(t *testing.T) { testSetPersistence(t, factory) })
	run("OnChangeReceivesSet", func(t *testing.T) { testOnChangeReceivesSet(t, factory, cfg.eventSettle) })
	run("DebounceDeliversLatestValue", func(t *testing.T) { testDebounceDeliversLatestValue(t, factory, cfg.eventSettle) })
	run("PanicSafeSubscriberCallbacks", func(t *testing.T) { testPanicSafeSubscriberCallbacks(t, factory, cfg.eventSettle) })
	run("MultipleSubscribers", func(t *testing.T) { testMultipleSubscribers(t, factory, cfg.eventSettle) })
	run("TenantScopedOverrideLifecycle", func(t *testing.T) { testTenantScopedOverrideLifecycle(t, factory) })
	run("TenantOnChangeReceivesSet", func(t *testing.T) { testTenantOnChangeReceivesSet(t, factory, cfg.eventSettle) })
	run("TenantOnChangeReceivesDelete", func(t *testing.T) { testTenantOnChangeReceivesDelete(t, factory, cfg.eventSettle) })
	run("TenantDeleteNoOpEmitsNoEvent", func(t *testing.T) { testTenantDeleteNoOpEmitsNoEvent(t, factory, cfg.eventSettle) })
	run("TenantContextValidation", func(t *testing.T) { testTenantContextValidation(t, factory) })
	run("NilReceiverSafety", testNilReceiverSafety)
	run("ValidationFailures", func(t *testing.T) { testValidationFailures(t, factory) })
	run("RedactionMetadata", func(t *testing.T) { testRedactionMetadata(t, factory) })
}

// RunClient is an explicit alias for [Run]. It exists for callers that prefer
// the method name to state that this suite validates public Client behavior.
func RunClient(t *testing.T, factory ClientFactory, opts ...RunOption) {
	t.Helper()

	Run(t, factory, opts...)
}

func testRegisterBeforeStart(t *testing.T, factory ClientFactory) {
	t.Helper()

	c := newClient(t, factory)
	mustRegister(t, c, "level", "info")
	mustStart(t, c)

	if err := c.Register(contractNamespace, "after_start", "value"); !errors.Is(err, systemplane.ErrRegisterAfterStart) {
		t.Fatalf("Register after Start error = %v, want ErrRegisterAfterStart", err)
	}
}

func testStartAndCloseLifecycle(t *testing.T, factory ClientFactory) {
	t.Helper()

	c := newClient(t, factory)
	mustRegister(t, c, "level", "info")
	mustStart(t, c)
	mustStart(t, c)

	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := c.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}

	if err := c.Set(context.Background(), contractNamespace, "level", debugValue, "contract"); !errors.Is(err, systemplane.ErrClosed) {
		t.Fatalf("Set after Close error = %v, want ErrClosed", err)
	}
}

func testTypedReads(t *testing.T, factory ClientFactory) {
	t.Helper()

	c := newClient(t, factory)
	mustRegister(t, c, "string", "info")
	mustRegister(t, c, "int", 10)
	mustRegister(t, c, "bool", false)
	mustRegister(t, c, "float", 1.5)
	mustRegister(t, c, "duration", 2*time.Second)
	mustStart(t, c)

	if got := c.GetString(contractNamespace, "string"); got != "info" {
		t.Fatalf("GetString default = %q, want %q", got, "info")
	}

	if got := c.GetInt(contractNamespace, "int"); got != 10 {
		t.Fatalf("GetInt default = %d, want 10", got)
	}

	if got := c.GetBool(contractNamespace, "bool"); got {
		t.Fatal("GetBool default = true, want false")
	}

	if got := c.GetFloat64(contractNamespace, "float"); !almostEqual(got, 1.5) {
		t.Fatalf("GetFloat64 default = %v, want 1.5", got)
	}

	if got := c.GetDuration(contractNamespace, "duration"); got != 2*time.Second {
		t.Fatalf("GetDuration default = %s, want 2s", got)
	}

	ctx := context.Background()
	mustSet(t, c, ctx, "string", debugValue)
	mustSet(t, c, ctx, "int", 42)
	mustSet(t, c, ctx, "bool", true)
	mustSet(t, c, ctx, "float", 3.25)
	mustSet(t, c, ctx, "duration", "5s")

	if got := c.GetString(contractNamespace, "string"); got != debugValue {
		t.Fatalf("GetString after Set = %q, want %q", got, debugValue)
	}

	if got := c.GetInt(contractNamespace, "int"); got != 42 {
		t.Fatalf("GetInt after Set = %d, want 42", got)
	}

	if got := c.GetBool(contractNamespace, "bool"); !got {
		t.Fatal("GetBool after Set = false, want true")
	}

	if got := c.GetFloat64(contractNamespace, "float"); !almostEqual(got, 3.25) {
		t.Fatalf("GetFloat64 after Set = %v, want 3.25", got)
	}

	if got := c.GetDuration(contractNamespace, "duration"); got != 5*time.Second {
		t.Fatalf("GetDuration after Set = %s, want 5s", got)
	}
}

func testSetPersistence(t *testing.T, factory ClientFactory) {
	t.Helper()

	c := newClient(t, factory)
	mustRegister(t, c, "level", "info", systemplane.WithDescription("log verbosity"))
	mustStart(t, c)
	mustSet(t, c, context.Background(), "level", debugValue)

	if got := c.GetString(contractNamespace, "level"); got != debugValue {
		t.Fatalf("GetString after Set = %q, want %q", got, debugValue)
	}

	entries := c.List(contractNamespace)
	if len(entries) != 1 {
		t.Fatalf("List length = %d, want 1", len(entries))
	}

	if entries[0].Key != "level" || entries[0].Value != debugValue || entries[0].Description != "log verbosity" {
		t.Fatalf("List entry = %#v, want key level/value %s/description log verbosity", entries[0], debugValue)
	}
}

func testOnChangeReceivesSet(t *testing.T, factory ClientFactory, settle time.Duration) {
	t.Helper()

	c := newStartedSingleKeyClient(t, factory, "level", "info")
	changes := make(chan any, 1)

	unsub := c.OnChange(contractNamespace, "level", func(newValue any) {
		changes <- newValue
	})
	defer unsub()

	settleChangefeed(settle)
	mustSet(t, c, context.Background(), "level", debugValue)

	if got := waitForValue(t, changes); got != debugValue {
		t.Fatalf("OnChange value = %#v, want %q", got, debugValue)
	}
}

func testDebounceDeliversLatestValue(t *testing.T, factory ClientFactory, settle time.Duration) {
	t.Helper()

	c := newStartedSingleKeyClient(t, factory, "limit", 0)
	changes := make(chan any, 8)

	unsub := c.OnChange(contractNamespace, "limit", func(newValue any) {
		changes <- newValue
	})
	defer unsub()

	settleChangefeed(settle)
	mustSet(t, c, context.Background(), "limit", 1)
	mustSet(t, c, context.Background(), "limit", 2)
	mustSet(t, c, context.Background(), "limit", 3)

	var got any

	deadline := time.After(eventDeadline)

	for {
		select {
		case got = <-changes:
			if numericEquals(got, 3) {
				return
			}
		case <-deadline:
			t.Fatalf("debounced OnChange never delivered latest value 3; last value: %#v", got)
		}
	}
}

func testPanicSafeSubscriberCallbacks(t *testing.T, factory ClientFactory, settle time.Duration) {
	t.Helper()

	c := newStartedSingleKeyClient(t, factory, "level", "info")
	secondSubscriber := make(chan any, 1)

	unsubPanic := c.OnChange(contractNamespace, "level", func(any) {
		panic("systemplanetest intentional subscriber panic")
	})
	defer unsubPanic()

	unsubSecond := c.OnChange(contractNamespace, "level", func(newValue any) {
		secondSubscriber <- newValue
	})
	defer unsubSecond()

	settleChangefeed(settle)
	mustSet(t, c, context.Background(), "level", debugValue)

	if got := waitForValue(t, secondSubscriber); got != debugValue {
		t.Fatalf("second subscriber value = %#v, want %q", got, debugValue)
	}
}

func testMultipleSubscribers(t *testing.T, factory ClientFactory, settle time.Duration) {
	t.Helper()

	c := newStartedSingleKeyClient(t, factory, "level", "info")

	var seenA, seenB atomic.Int64

	doneA := make(chan struct{}, 1)
	doneB := make(chan struct{}, 1)

	unsubA := c.OnChange(contractNamespace, "level", func(any) {
		seenA.Add(1)
		signal(doneA)
	})
	defer unsubA()

	unsubB := c.OnChange(contractNamespace, "level", func(any) {
		seenB.Add(1)
		signal(doneB)
	})
	defer unsubB()

	settleChangefeed(settle)
	mustSet(t, c, context.Background(), "level", debugValue)
	waitForSignal(t, doneA, "subscriber A")
	waitForSignal(t, doneB, "subscriber B")

	if seenA.Load() == 0 || seenB.Load() == 0 {
		t.Fatalf("subscriber counts = (%d, %d), want both > 0", seenA.Load(), seenB.Load())
	}
}

func testTenantScopedOverrideLifecycle(t *testing.T, factory ClientFactory) {
	t.Helper()

	c := newClient(t, factory)
	mustRegisterTenantRate(t, c)
	mustStart(t, c)

	ctxA := tenantContext(tenantA)
	ctxB := tenantContext(tenantB)
	ctxC := tenantContext(tenantC)
	ctxD := tenantContext("tenant-D")

	if tenants := c.ListTenantsForKey(contractNamespace, tenantRateKey); len(tenants) != 0 {
		t.Fatalf("ListTenantsForKey before overrides = %v, want empty", tenants)
	}

	mustSet(t, c, context.Background(), tenantRateKey, 0.15)
	mustSetTenantRate(t, c, ctxC, 0.30)
	mustSetTenantRate(t, c, ctxA, 0.20)
	mustSetTenantRate(t, c, ctxB, 0.35)
	mustSetTenantRate(t, c, ctxA, 0.25)

	gotA, foundA, err := c.GetForTenant(ctxA, contractNamespace, tenantRateKey)
	if err != nil || !foundA || !numericEquals(gotA, 0.25) {
		t.Fatalf("GetForTenant %s = (%#v, %v, %v), want (0.25, true, nil)", tenantA, gotA, foundA, err)
	}

	gotD, foundD, err := c.GetForTenant(ctxD, contractNamespace, tenantRateKey)
	if err != nil || !foundD || !numericEquals(gotD, 0.15) {
		t.Fatalf("GetForTenant tenant-D = (%#v, %v, %v), want global 0.15", gotD, foundD, err)
	}

	if got := c.GetFloat64(contractNamespace, tenantRateKey); !almostEqual(got, 0.15) {
		t.Fatalf("global GetFloat64 after tenant overrides = %v, want 0.15", got)
	}

	tenants := c.ListTenantsForKey(contractNamespace, tenantRateKey)

	wantTenants := []string{tenantA, tenantB, tenantC}
	if !stringSlicesEqual(tenants, wantTenants) {
		t.Fatalf("ListTenantsForKey = %v, want %v", tenants, wantTenants)
	}

	mustDeleteTenantRate(t, c, ctxA)

	gotA, foundA, err = c.GetForTenant(ctxA, contractNamespace, tenantRateKey)
	if err != nil || !foundA || !numericEquals(gotA, 0.15) {
		t.Fatalf("GetForTenant %s after delete = (%#v, %v, %v), want global 0.15", tenantA, gotA, foundA, err)
	}
}

func testTenantOnChangeReceivesSet(t *testing.T, factory ClientFactory, settle time.Duration) {
	t.Helper()

	c := newClient(t, factory)
	mustRegisterTenantRate(t, c)
	mustStart(t, c)

	type tenantEvent struct {
		ctxTenantID string
		tenantID    string
		value       any
	}

	changes := make(chan tenantEvent, 2)

	unsub := c.OnTenantChange(contractNamespace, tenantRateKey, func(ctx context.Context, _, _ string, tenantID string, newValue any) {
		changes <- tenantEvent{
			ctxTenantID: core.GetTenantIDContext(ctx),
			tenantID:    tenantID,
			value:       newValue,
		}
	})
	defer unsub()

	ctxA := tenantContext(tenantA)

	settleChangefeed(settle)
	mustSetTenantRate(t, c, ctxA, 0.25)

	setEvent := waitForTenantEvent(t, changes)
	if setEvent.tenantID != tenantA || setEvent.ctxTenantID != tenantA || !numericEquals(setEvent.value, 0.25) {
		t.Fatalf("tenant set event = %#v, want %s value 0.25", setEvent, tenantA)
	}
}

func testTenantOnChangeReceivesDelete(t *testing.T, factory ClientFactory, settle time.Duration) {
	t.Helper()

	c := newClient(t, factory)
	mustRegisterTenantRate(t, c)
	mustStart(t, c)

	type tenantEvent struct {
		ctxTenantID string
		tenantID    string
		value       any
	}

	ctxA := tenantContext(tenantA)
	mustSetTenantRate(t, c, ctxA, 0.25)

	changes := make(chan tenantEvent, 2)

	unsub := c.OnTenantChange(contractNamespace, tenantRateKey, func(ctx context.Context, _, _ string, tenantID string, newValue any) {
		changes <- tenantEvent{
			ctxTenantID: core.GetTenantIDContext(ctx),
			tenantID:    tenantID,
			value:       newValue,
		}
	})
	defer unsub()

	settleChangefeed(settle)
	drainTenantEvents(changes)
	mustDeleteTenantRate(t, c, ctxA)

	deleteEvent := waitForTenantEvent(t, changes)
	if deleteEvent.tenantID != tenantA || deleteEvent.ctxTenantID != tenantA || !numericEquals(deleteEvent.value, 0.10) {
		t.Fatalf("tenant delete event = %#v, want %s default value 0.10", deleteEvent, tenantA)
	}
}

func testTenantDeleteNoOpEmitsNoEvent(t *testing.T, factory ClientFactory, settle time.Duration) {
	t.Helper()

	c := newClient(t, factory)
	mustRegisterTenantRate(t, c)
	mustStart(t, c)

	changes := make(chan struct{}, 1)

	unsub := c.OnTenantChange(contractNamespace, tenantRateKey, func(context.Context, string, string, string, any) {
		signal(changes)
	})
	defer unsub()

	settleChangefeed(settle)
	mustDeleteTenantRate(t, c, tenantContext("tenant-missing"))

	select {
	case <-changes:
		t.Fatal("no-op DeleteForTenant emitted unexpected OnTenantChange event")
	case <-time.After(noEventWindow):
	}
}

func testTenantContextValidation(t *testing.T, factory ClientFactory) {
	t.Helper()

	c := newClient(t, factory)
	mustRegisterTenantRate(t, c)
	mustStart(t, c)

	assertTenantError := func(label string, ctx context.Context, want error) {
		t.Helper()

		if _, _, err := c.GetForTenant(ctx, contractNamespace, tenantRateKey); !errors.Is(err, want) {
			t.Fatalf("%s GetForTenant error = %v, want %v", label, err, want)
		}

		if err := c.SetForTenant(ctx, contractNamespace, tenantRateKey, 0.20, "systemplanetest"); !errors.Is(err, want) {
			t.Fatalf("%s SetForTenant error = %v, want %v", label, err, want)
		}

		if err := c.DeleteForTenant(ctx, contractNamespace, tenantRateKey, "systemplanetest"); !errors.Is(err, want) {
			t.Fatalf("%s DeleteForTenant error = %v, want %v", label, err, want)
		}
	}

	assertTenantError("missing tenant", context.Background(), systemplane.ErrMissingTenantContext)
	assertTenantError("invalid tenant", tenantContext("invalid tenant"), systemplane.ErrInvalidTenantID)
	assertTenantError("global sentinel", tenantContext("_global"), systemplane.ErrInvalidTenantID)
}

func testNilReceiverSafety(t *testing.T) {
	t.Helper()

	var c *systemplane.Client
	assertNilReceiverReads(t, c)
	assertNilReceiverMetadata(t, c)
	assertNilReceiverLifecycle(t, c)
}

func assertNilReceiverReads(t *testing.T, c *systemplane.Client) {
	t.Helper()

	if v, ok := c.Get(contractNamespace, "missing"); v != nil || ok {
		t.Fatalf("nil Get = (%#v, %v), want (nil, false)", v, ok)
	}

	if got := c.List(contractNamespace); got != nil {
		t.Fatalf("nil List = %#v, want nil", got)
	}

	if got := c.GetString(contractNamespace, "missing"); got != "" {
		t.Fatalf("nil GetString = %q, want empty", got)
	}

	if got := c.GetInt(contractNamespace, "missing"); got != 0 {
		t.Fatalf("nil GetInt = %d, want 0", got)
	}

	if got := c.GetBool(contractNamespace, "missing"); got {
		t.Fatal("nil GetBool = true, want false")
	}

	if got := c.GetFloat64(contractNamespace, "missing"); got != 0 {
		t.Fatalf("nil GetFloat64 = %v, want 0", got)
	}

	if got := c.GetDuration(contractNamespace, "missing"); got != 0 {
		t.Fatalf("nil GetDuration = %s, want 0", got)
	}
}

func assertNilReceiverMetadata(t *testing.T, c *systemplane.Client) {
	t.Helper()

	if got := c.KeyDescription(contractNamespace, "missing"); got != "" {
		t.Fatalf("nil KeyDescription = %q, want empty", got)
	}

	if got := c.KeyRedaction(contractNamespace, "missing"); got != systemplane.RedactNone {
		t.Fatalf("nil KeyRedaction = %v, want RedactNone", got)
	}

	registered, tenantScoped := c.KeyStatus(contractNamespace, "missing")
	if registered || tenantScoped {
		t.Fatalf("nil KeyStatus = (%v, %v), want (false, false)", registered, tenantScoped)
	}

	if c.Logger() == nil {
		t.Fatal("nil Logger returned nil")
	}

	c.OnChange(contractNamespace, "missing", func(any) {})()
}

func assertNilReceiverLifecycle(t *testing.T, c *systemplane.Client) {
	t.Helper()

	if err := c.Close(); err != nil {
		t.Fatalf("nil Close: %v", err)
	}

	if err := c.Register(contractNamespace, "k", "v"); !errors.Is(err, systemplane.ErrClosed) {
		t.Fatalf("nil Register error = %v, want ErrClosed", err)
	}

	if err := c.Start(context.Background()); !errors.Is(err, systemplane.ErrClosed) {
		t.Fatalf("nil Start error = %v, want ErrClosed", err)
	}

	if err := c.Set(context.Background(), contractNamespace, "k", "v", "contract"); !errors.Is(err, systemplane.ErrClosed) {
		t.Fatalf("nil Set error = %v, want ErrClosed", err)
	}
}

func testValidationFailures(t *testing.T, factory ClientFactory) {
	t.Helper()

	rejectNegative := func(value any) error {
		if asFloat(value) < 0 {
			return errors.New("negative values are rejected")
		}

		return nil
	}

	c := newClient(t, factory)
	if err := c.Register(contractNamespace, "bad_default", -1, systemplane.WithValidator(rejectNegative)); !errors.Is(err, systemplane.ErrValidation) {
		t.Fatalf("invalid default Register error = %v, want ErrValidation", err)
	}

	if err := c.Register(contractNamespace, "limit", 1, systemplane.WithValidator(rejectNegative)); err != nil {
		t.Fatalf("Register valid key: %v", err)
	}

	if err := c.Set(context.Background(), contractNamespace, "limit", 2, "contract"); !errors.Is(err, systemplane.ErrNotStarted) {
		t.Fatalf("Set before Start error = %v, want ErrNotStarted", err)
	}

	mustStart(t, c)

	if err := c.Set(context.Background(), contractNamespace, "limit", -1, "contract"); !errors.Is(err, systemplane.ErrValidation) {
		t.Fatalf("Set invalid value error = %v, want ErrValidation", err)
	}

	if err := c.Set(context.Background(), contractNamespace, "unknown", 1, "contract"); !errors.Is(err, systemplane.ErrUnknownKey) {
		t.Fatalf("Set unknown key error = %v, want ErrUnknownKey", err)
	}

	if err := c.Set(context.Background(), contractNamespace, "limit", math.Inf(1), "contract"); !errors.Is(err, systemplane.ErrValidation) {
		t.Fatalf("Set non-JSON value error = %v, want ErrValidation", err)
	}
}

func testRedactionMetadata(t *testing.T, factory ClientFactory) {
	t.Helper()

	c := newClient(t, factory)
	if err := c.Register(contractNamespace, "secret", "hunter2",
		systemplane.WithDescription("API secret"),
		systemplane.WithRedaction(systemplane.RedactFull),
	); err != nil {
		t.Fatalf("Register secret: %v", err)
	}

	if got := c.KeyDescription(contractNamespace, "secret"); got != "API secret" {
		t.Fatalf("KeyDescription = %q, want %q", got, "API secret")
	}

	if got := c.KeyRedaction(contractNamespace, "secret"); got != systemplane.RedactFull {
		t.Fatalf("KeyRedaction = %v, want RedactFull", got)
	}

	if got := systemplane.ApplyRedaction("hunter2", c.KeyRedaction(contractNamespace, "secret")); got != "[REDACTED]" {
		t.Fatalf("ApplyRedaction = %#v, want [REDACTED]", got)
	}
}

func newClient(t *testing.T, factory ClientFactory) *systemplane.Client {
	t.Helper()

	if factory == nil {
		t.Fatal("nil ClientFactory")
	}

	c := factory(t)
	if c == nil {
		t.Fatal("ClientFactory returned nil")
	}

	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("cleanup Close: %v", err)
		}
	})

	return c
}

func newStartedSingleKeyClient(t *testing.T, factory ClientFactory, key string, defaultValue any) *systemplane.Client {
	t.Helper()

	c := newClient(t, factory)
	mustRegister(t, c, key, defaultValue)
	mustStart(t, c)

	return c
}

func mustRegister(t *testing.T, c *systemplane.Client, key string, defaultValue any, opts ...systemplane.KeyOption) {
	t.Helper()

	if err := c.Register(contractNamespace, key, defaultValue, opts...); err != nil {
		t.Fatalf("Register(%q): %v", key, err)
	}
}

func mustRegisterTenantRate(t *testing.T, c *systemplane.Client) {
	t.Helper()

	if err := c.RegisterTenantScoped(contractNamespace, tenantRateKey, tenantRateDefault); err != nil {
		t.Fatalf("RegisterTenantScoped(%q): %v", tenantRateKey, err)
	}
}

func mustStart(t *testing.T, c *systemplane.Client) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), eventDeadline)
	defer cancel()

	if err := c.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
}

func mustSet(t *testing.T, c *systemplane.Client, ctx context.Context, key string, value any) {
	t.Helper()

	if err := c.Set(ctx, contractNamespace, key, value, "systemplanetest"); err != nil {
		t.Fatalf("Set(%q, %#v): %v", key, value, err)
	}
}

func mustSetTenantRate(t *testing.T, c *systemplane.Client, ctx context.Context, value any) {
	t.Helper()

	if err := c.SetForTenant(ctx, contractNamespace, tenantRateKey, value, "systemplanetest"); err != nil {
		t.Fatalf("SetForTenant(%q, %#v): %v", tenantRateKey, value, err)
	}
}

func mustDeleteTenantRate(t *testing.T, c *systemplane.Client, ctx context.Context) {
	t.Helper()

	if err := c.DeleteForTenant(ctx, contractNamespace, tenantRateKey, "systemplanetest"); err != nil {
		t.Fatalf("DeleteForTenant(%q): %v", tenantRateKey, err)
	}
}

func tenantContext(tenantID string) context.Context {
	return core.ContextWithTenantID(context.Background(), tenantID)
}

func settleChangefeed(d time.Duration) {
	if d > 0 {
		time.Sleep(d)
	}
}

func waitForValue(t *testing.T, ch <-chan any) any {
	t.Helper()

	select {
	case got := <-ch:
		return got
	case <-time.After(eventDeadline):
		t.Fatal("timed out waiting for OnChange callback")

		return nil
	}
}

func waitForSignal(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()

	select {
	case <-ch:
	case <-time.After(eventDeadline):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func waitForTenantEvent[T any](t *testing.T, ch <-chan T) T {
	t.Helper()

	select {
	case got := <-ch:
		return got
	case <-time.After(eventDeadline):
		t.Fatal("timed out waiting for OnTenantChange callback")

		var zero T

		return zero
	}
}

func drainTenantEvents[T any](ch <-chan T) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

func signal(ch chan<- struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func almostEqual(got, want float64) bool {
	const tolerance = 0.000001

	return math.Abs(got-want) <= tolerance
}

func numericEquals(got any, want float64) bool {
	return almostEqual(asFloat(got), want)
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

func asFloat(value any) float64 {
	switch v := value.(type) {
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case float64:
		return v
	case float32:
		return float64(v)
	default:
		return math.NaN()
	}
}
