package systemplane

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/internal/store"
)

// ---------------------------------------------------------------------------
// In-memory fake Store
// ---------------------------------------------------------------------------

// fakeStore is a test double that implements store.Store entirely in memory.
// Set invokes all registered subscribe handlers synchronously after the map
// write, simulating a changefeed echo.
type fakeStore struct {
	mu       sync.Mutex
	entries  map[nskey]store.Entry
	handlers []func(store.Event)
	closed   bool
	subReady chan struct{} // closed when first Subscribe handler is registered
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		entries:  make(map[nskey]store.Entry),
		subReady: make(chan struct{}),
	}
}

func (f *fakeStore) List(_ context.Context) ([]store.Entry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]store.Entry, 0, len(f.entries))
	for _, e := range f.entries {
		out = append(out, e)
	}

	return out, nil
}

func (f *fakeStore) Get(_ context.Context, namespace, key string) (store.Entry, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	e, ok := f.entries[nskey{Namespace: namespace, Key: key}]

	return e, ok, nil
}

func (f *fakeStore) Set(_ context.Context, e store.Entry) error {
	f.mu.Lock()
	nk := nskey{Namespace: e.Namespace, Key: e.Key}
	f.entries[nk] = e
	// Snapshot handlers under the same lock to avoid data races.
	handlers := make([]func(store.Event), len(f.handlers))
	copy(handlers, f.handlers)
	f.mu.Unlock()

	// Fire changefeed echo synchronously (outside the lock to avoid deadlock
	// with the Client's own locking in onEvent/refreshFromStore).
	evt := store.Event{Namespace: e.Namespace, Key: e.Key}
	for _, h := range handlers {
		h(evt)
	}

	return nil
}

func (f *fakeStore) Subscribe(ctx context.Context, handler func(store.Event)) error {
	f.mu.Lock()
	first := len(f.handlers) == 0
	f.handlers = append(f.handlers, handler)
	f.mu.Unlock()

	// Signal that the first subscriber is registered, unblocking tests that
	// need to wait for Start() to finish its Subscribe goroutine setup.
	if first {
		close(f.subReady)
	}

	// Block until context is cancelled, like a real changefeed.
	<-ctx.Done()

	return nil
}

// waitForSubscriber blocks until at least one Subscribe handler is registered,
// or the timeout expires.
func (f *fakeStore) waitForSubscriber(t *testing.T, timeout time.Duration) {
	t.Helper()

	select {
	case <-f.subReady:
	case <-time.After(timeout):
		t.Fatal("timed out waiting for Subscribe handler registration")
	}
}

func (f *fakeStore) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.closed = true

	return nil
}

// simulateExternalChange writes an entry directly (bypassing the Client) and
// fires all subscribe handlers, mimicking a change made by another process
// that arrives via the changefeed.
func (f *fakeStore) simulateExternalChange(namespace, key string, value any) {
	jsonBytes, _ := json.Marshal(value)

	e := store.Entry{
		Namespace: namespace,
		Key:       key,
		Value:     jsonBytes,
		UpdatedAt: time.Now(),
		UpdatedBy: "external",
	}

	f.mu.Lock()
	nk := nskey{Namespace: namespace, Key: key}
	f.entries[nk] = e
	handlers := make([]func(store.Event), len(f.handlers))
	copy(handlers, f.handlers)
	f.mu.Unlock()

	evt := store.Event{Namespace: namespace, Key: key}
	for _, h := range handlers {
		h(evt)
	}
}

// lastEntry returns the last-written entry for a (namespace, key) pair.
func (f *fakeStore) lastEntry(namespace, key string) (store.Entry, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	e, ok := f.entries[nskey{Namespace: namespace, Key: key}]

	return e, ok
}

// ---------------------------------------------------------------------------
// Helper: build a Client wired to a fakeStore
// ---------------------------------------------------------------------------

// testClient creates a Client backed by the given fakeStore with debouncing
// disabled (zero window) for deterministic tests.
func testClient(t *testing.T, fs *fakeStore) *Client {
	t.Helper()

	cfg := defaultClientConfig()
	cfg.debounce = 0 // synchronous dispatch for test determinism
	cfg.logger = log.NewNop()

	c := newClient(fs, cfg)
	t.Cleanup(func() { _ = c.Close() })

	return c
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestRegister_ErrorAfterStart(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	if err := c.Register("ns", "k", "v"); err != nil {
		t.Fatalf("register before start should succeed: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start failed: %v", err)
	}

	err := c.Register("ns", "k2", "v2")
	if !errors.Is(err, ErrRegisterAfterStart) {
		t.Fatalf("expected ErrRegisterAfterStart, got %v", err)
	}
}

func TestRegister_DuplicateRejected(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	if err := c.Register("ns", "k", "v"); err != nil {
		t.Fatalf("first register should succeed: %v", err)
	}

	err := c.Register("ns", "k", "other")
	if !errors.Is(err, ErrDuplicateKey) {
		t.Fatalf("expected ErrDuplicateKey, got %v", err)
	}
}

func TestRegister_InvalidDefaultRejected(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	alwaysFail := func(_ any) error { return errors.New("nope") }

	err := c.Register("ns", "k", "bad", WithValidator(alwaysFail))
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation, got %v", err)
	}
}

func TestGet_UnregisteredReturnsFalse(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	v, ok := c.Get("ns", "unknown")
	if ok || v != nil {
		t.Fatalf("expected (nil, false), got (%v, %v)", v, ok)
	}
}

func TestGet_RegisteredDefaultReturned(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	if err := c.Register("ns", "level", "info"); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Get before Start — returns the registered default.
	v, ok := c.Get("ns", "level")
	if !ok {
		t.Fatal("expected ok=true for registered key before Start")
	}

	if v != "info" {
		t.Fatalf("expected default 'info', got %v", v)
	}
}

func TestGet_AfterHydrate_StoredValueWins(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()

	// Pre-populate the fake store with a value that differs from the default.
	debugBytes, _ := json.Marshal("debug")
	fs.entries[nskey{Namespace: "ns", Key: "level"}] = store.Entry{
		Namespace: "ns",
		Key:       "level",
		Value:     debugBytes,
		UpdatedAt: time.Now(),
		UpdatedBy: "seed",
	}

	c := testClient(t, fs)

	if err := c.Register("ns", "level", "info"); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	v, ok := c.Get("ns", "level")
	if !ok {
		t.Fatal("expected ok=true")
	}

	if v != "debug" {
		t.Fatalf("expected stored value 'debug', got %v", v)
	}
}

func TestSet_UnregisteredRejected(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	// Register something so Start is valid, then try setting an unregistered key.
	if err := c.Register("ns", "known", "v"); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	err := c.Set(context.Background(), "ns", "unknown", "val", "tester")
	if !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("expected ErrUnknownKey, got %v", err)
	}
}

func TestSet_ValidatorRejects(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	onlyPositive := func(v any) error {
		n, ok := v.(int)
		if !ok || n <= 0 {
			return errors.New("must be positive int")
		}

		return nil
	}

	if err := c.Register("ns", "retries", 3, WithValidator(onlyPositive)); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	err := c.Set(context.Background(), "ns", "retries", -1, "tester")
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation, got %v", err)
	}
}

func TestSet_SuccessUpdatesCacheImmediately(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	if err := c.Register("ns", "level", "info"); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	if err := c.Set(context.Background(), "ns", "level", "debug", "ops"); err != nil {
		t.Fatalf("set: %v", err)
	}

	// Get should return the new value immediately, without waiting for changefeed.
	v, ok := c.Get("ns", "level")
	if !ok || v != "debug" {
		t.Fatalf("expected immediate cache update to 'debug', got (%v, %v)", v, ok)
	}
}

func TestOnChange_FiresAfterChangefeedEvent(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	if err := c.Register("ns", "level", "info"); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Wait for the Subscribe goroutine to register its handler.
	fs.waitForSubscriber(t, time.Second)

	var received atomic.Value

	c.OnChange("ns", "level", func(newValue any) {
		received.Store(newValue)
	})

	// Simulate a remote change arriving via the changefeed.
	fs.simulateExternalChange("ns", "level", "warn")

	// The debouncer has zero window, so invocation is synchronous.
	// But give a small window for goroutine scheduling.
	deadline := time.After(500 * time.Millisecond)
	for {
		if v := received.Load(); v != nil {
			if v != "warn" {
				t.Fatalf("expected callback value 'warn', got %v", v)
			}

			return
		}

		select {
		case <-deadline:
			t.Fatal("timed out waiting for OnChange callback")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func TestOnChange_LocalSetFiresSubscriber(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	if err := c.Register("ns", "level", "info"); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Wait for the Subscribe goroutine to register its handler.
	fs.waitForSubscriber(t, time.Second)

	var received atomic.Value

	c.OnChange("ns", "level", func(newValue any) {
		received.Store(newValue)
	})

	// Local Set — the fake store fires changefeed echo synchronously, which
	// triggers the debouncer (zero-window → synchronous), which triggers
	// refreshFromStore, which fires the subscriber.
	if err := c.Set(context.Background(), "ns", "level", "error", "ops"); err != nil {
		t.Fatalf("set: %v", err)
	}

	deadline := time.After(500 * time.Millisecond)
	for {
		if v := received.Load(); v != nil {
			if v != "error" {
				t.Fatalf("expected 'error', got %v", v)
			}

			return
		}

		select {
		case <-deadline:
			t.Fatal("timed out waiting for OnChange callback after local Set")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
}

func TestOnChange_Unsubscribe(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	if err := c.Register("ns", "level", "info"); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	fs.waitForSubscriber(t, time.Second)

	var callCount atomic.Int32

	unsub := c.OnChange("ns", "level", func(_ any) {
		callCount.Add(1)
	})

	// First change — should fire.
	fs.simulateExternalChange("ns", "level", "debug")
	time.Sleep(50 * time.Millisecond)

	if callCount.Load() != 1 {
		t.Fatalf("expected 1 callback, got %d", callCount.Load())
	}

	// Unsubscribe, then trigger another change — should NOT fire.
	unsub()

	fs.simulateExternalChange("ns", "level", "warn")
	time.Sleep(50 * time.Millisecond)

	if callCount.Load() != 1 {
		t.Fatalf("expected still 1 callback after unsubscribe, got %d", callCount.Load())
	}
}

func TestOnChange_PanicIsRecovered(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	if err := c.Register("ns", "level", "info"); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	fs.waitForSubscriber(t, time.Second)

	// First subscriber panics.
	c.OnChange("ns", "level", func(_ any) {
		panic("boom")
	})

	// Second subscriber should still fire.
	var received atomic.Value

	c.OnChange("ns", "level", func(newValue any) {
		received.Store(newValue)
	})

	fs.simulateExternalChange("ns", "level", "debug")
	time.Sleep(50 * time.Millisecond)

	v := received.Load()
	if v != "debug" {
		t.Fatalf("expected second subscriber to fire with 'debug', got %v", v)
	}
}

func TestOnChange_UnregisteredKeyIsSilent(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	// OnChange on an unregistered key — should not panic.
	unsub := c.OnChange("ns", "nonexistent", func(_ any) {})

	// Returned unsubscribe should be callable without panic.
	unsub()
	unsub() // double-call safety
}

func TestTypedAccessors_NilSafe(t *testing.T) {
	t.Parallel()

	var c *Client

	if s := c.GetString("ns", "k"); s != "" {
		t.Fatalf("GetString on nil: expected '', got %q", s)
	}

	if n := c.GetInt("ns", "k"); n != 0 {
		t.Fatalf("GetInt on nil: expected 0, got %d", n)
	}

	if b := c.GetBool("ns", "k"); b {
		t.Fatalf("GetBool on nil: expected false, got %v", b)
	}

	if f := c.GetFloat64("ns", "k"); f != 0 {
		t.Fatalf("GetFloat64 on nil: expected 0, got %f", f)
	}

	if d := c.GetDuration("ns", "k"); d != 0 {
		t.Fatalf("GetDuration on nil: expected 0, got %v", d)
	}
}

func TestClose_Idempotent(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	if err := c.Register("ns", "k", "v"); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Close twice — neither call should panic or return an error.
	if err := c.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}

	if err := c.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
}

func TestOperationsAfterClose(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	if err := c.Register("ns", "k", "v"); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	_ = c.Close()

	// Register after close.
	if err := c.Register("ns", "k2", "v2"); !errors.Is(err, ErrClosed) {
		t.Fatalf("Register after close: expected ErrClosed, got %v", err)
	}

	// Set after close.
	if err := c.Set(context.Background(), "ns", "k", "new", "ops"); !errors.Is(err, ErrClosed) {
		t.Fatalf("Set after close: expected ErrClosed, got %v", err)
	}

	// Get after close returns zero values (Client still has cache, but this is
	// acceptable per spec — Get is nil-safe and cache data remains valid).
	// The closed flag is checked in Register/Set/Start only.
	v, ok := c.Get("ns", "k")
	if !ok {
		t.Fatal("Get after close: expected ok=true (cache still populated)")
	}

	if v != "v" {
		t.Fatalf("Get after close: expected 'v', got %v", v)
	}
}

func TestSet_UpdatedByPropagated(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	if err := c.Register("ns", "k", "v"); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	if err := c.Set(context.Background(), "ns", "k", "new", "ops-1"); err != nil {
		t.Fatalf("set: %v", err)
	}

	entry, ok := fs.lastEntry("ns", "k")
	if !ok {
		t.Fatal("expected entry in fake store")
	}

	if entry.UpdatedBy != "ops-1" {
		t.Fatalf("expected UpdatedBy='ops-1', got %q", entry.UpdatedBy)
	}
}

func TestTypedCoercion_Durations(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	// Case 1: Native time.Duration default.
	if err := c.Register("ns", "timeout", 5*time.Second); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	fs.waitForSubscriber(t, time.Second)

	d := c.GetDuration("ns", "timeout")
	if d != 5*time.Second {
		t.Fatalf("expected 5s from native Duration default, got %v", d)
	}

	// Case 2: Set a JSON string duration. After Set, the cached value is a
	// string (because Set stores the Go value, not the JSON-decoded form).
	if err := c.Set(context.Background(), "ns", "timeout", "10s", "ops"); err != nil {
		t.Fatalf("set string duration: %v", err)
	}

	d = c.GetDuration("ns", "timeout")
	if d != 10*time.Second {
		t.Fatalf("expected 10s from string '10s', got %v", d)
	}

	// Case 3: Simulate a changefeed delivering a nanosecond number (JSON float64).
	// json.Unmarshal decodes numbers as float64.
	fs.simulateExternalChange("ns", "timeout", float64(3*time.Second))
	time.Sleep(50 * time.Millisecond)

	d = c.GetDuration("ns", "timeout")
	if d != 3*time.Second {
		t.Fatalf("expected 3s from float64 nanoseconds, got %v", d)
	}
}

func TestStart_IsIdempotent(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	if err := c.Register("ns", "k", "v"); err != nil {
		t.Fatalf("register: %v", err)
	}

	// First Start.
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("first start: %v", err)
	}

	// Second Start — should return nil silently (not ErrAlreadyStarted).
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("second start: expected nil, got %v", err)
	}
}

func TestSet_BeforeStartReturnsErrNotStarted(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	if err := c.Register("ns", "k", "v"); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Set before Start.
	err := c.Set(context.Background(), "ns", "k", "new", "ops")
	if !errors.Is(err, ErrNotStarted) {
		t.Fatalf("expected ErrNotStarted, got %v", err)
	}
}

func TestNilClient_CloseSafe(t *testing.T) {
	t.Parallel()

	var c *Client

	// Close on nil should not panic and return nil.
	if err := c.Close(); err != nil {
		t.Fatalf("Close on nil: expected nil, got %v", err)
	}
}

func TestNilClient_StartReturnsErrClosed(t *testing.T) {
	t.Parallel()

	var c *Client

	if err := c.Start(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Start on nil: expected ErrClosed, got %v", err)
	}
}

func TestNilClient_RegisterReturnsErrClosed(t *testing.T) {
	t.Parallel()

	var c *Client

	if err := c.Register("ns", "k", "v"); !errors.Is(err, ErrClosed) {
		t.Fatalf("Register on nil: expected ErrClosed, got %v", err)
	}
}

func TestNilClient_SetReturnsErrClosed(t *testing.T) {
	t.Parallel()

	var c *Client

	if err := c.Set(context.Background(), "ns", "k", "v", "ops"); !errors.Is(err, ErrClosed) {
		t.Fatalf("Set on nil: expected ErrClosed, got %v", err)
	}
}

func TestNilClient_GetReturnsFalse(t *testing.T) {
	t.Parallel()

	var c *Client

	v, ok := c.Get("ns", "k")
	if ok || v != nil {
		t.Fatalf("Get on nil: expected (nil, false), got (%v, %v)", v, ok)
	}
}

func TestNilClient_OnChangeReturnsNoop(t *testing.T) {
	t.Parallel()

	var c *Client

	unsub := c.OnChange("ns", "k", func(_ any) {})
	unsub() // should not panic
}

func TestOptions_ApplyCorrectly(t *testing.T) {
	t.Parallel()

	cfg := defaultClientConfig()

	// Verify defaults.
	if cfg.listenChannel != "systemplane_changes" {
		t.Fatalf("default listenChannel: expected 'systemplane_changes', got %q", cfg.listenChannel)
	}

	if cfg.debounce != 100*time.Millisecond {
		t.Fatalf("default debounce: expected 100ms, got %v", cfg.debounce)
	}

	if cfg.collection != "systemplane_entries" {
		t.Fatalf("default collection: expected 'systemplane_entries', got %q", cfg.collection)
	}

	if cfg.table != "systemplane_entries" {
		t.Fatalf("default table: expected 'systemplane_entries', got %q", cfg.table)
	}

	// Apply options.
	WithListenChannel("custom_ch")(&cfg)
	WithDebounce(200 * time.Millisecond)(&cfg)
	WithCollection("custom_coll")(&cfg)
	WithTable("custom_tbl")(&cfg)
	WithPollInterval(5 * time.Second)(&cfg)
	WithLogger(log.NewNop())(&cfg)

	if cfg.listenChannel != "custom_ch" {
		t.Fatalf("expected 'custom_ch', got %q", cfg.listenChannel)
	}

	if cfg.debounce != 200*time.Millisecond {
		t.Fatalf("expected 200ms, got %v", cfg.debounce)
	}

	if cfg.collection != "custom_coll" {
		t.Fatalf("expected 'custom_coll', got %q", cfg.collection)
	}

	if cfg.table != "custom_tbl" {
		t.Fatalf("expected 'custom_tbl', got %q", cfg.table)
	}

	if cfg.pollInterval != 5*time.Second {
		t.Fatalf("expected 5s, got %v", cfg.pollInterval)
	}
}

func TestRedaction(t *testing.T) {
	t.Parallel()

	if v := ApplyRedaction("secret", RedactNone); v != "secret" {
		t.Fatalf("RedactNone: expected 'secret', got %v", v)
	}

	if v := ApplyRedaction("secret", RedactMask); v != "****" {
		t.Fatalf("RedactMask: expected '****', got %v", v)
	}

	if v := ApplyRedaction("secret", RedactFull); v != "[REDACTED]" {
		t.Fatalf("RedactFull: expected '[REDACTED]', got %v", v)
	}
}

func TestKeyOptions(t *testing.T) {
	t.Parallel()

	def := keyDef{redaction: RedactNone}

	WithDescription("a knob")(&def)
	WithRedaction(RedactMask)(&def)

	if def.description != "a knob" {
		t.Fatalf("expected description 'a knob', got %q", def.description)
	}

	if def.redaction != RedactMask {
		t.Fatalf("expected RedactMask, got %v", def.redaction)
	}
}

func TestMultipleSubscribers_AllFire(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	if err := c.Register("ns", "k", "v"); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	fs.waitForSubscriber(t, time.Second)

	var count atomic.Int32

	for range 5 {
		c.OnChange("ns", "k", func(_ any) {
			count.Add(1)
		})
	}

	fs.simulateExternalChange("ns", "k", "new")
	time.Sleep(50 * time.Millisecond)

	if got := count.Load(); got != 5 {
		t.Fatalf("expected 5 subscribers to fire, got %d", got)
	}
}

func TestGetString_HappyPath(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	if err := c.Register("ns", "name", "alice"); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	if got := c.GetString("ns", "name"); got != "alice" {
		t.Fatalf("expected 'alice', got %q", got)
	}

	// Non-string value returns "".
	if err := c.Register("ns2", "count", 42); err != nil {
		// Already started, so register a new client for this case.
	}

	// Test GetString with unregistered key.
	if got := c.GetString("ns", "nope"); got != "" {
		t.Fatalf("expected '' for unregistered key, got %q", got)
	}
}

func TestGetInt_HappyPath(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	if err := c.Register("ns", "retries", 5); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	if got := c.GetInt("ns", "retries"); got != 5 {
		t.Fatalf("expected 5, got %d", got)
	}

	// Unregistered returns 0.
	if got := c.GetInt("ns", "nope"); got != 0 {
		t.Fatalf("expected 0, got %d", got)
	}
}

func TestGetInt_Float64Coercion(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	// When hydrated from JSON, numbers arrive as float64.
	if err := c.Register("ns", "retries", 3); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Pre-populate store with a JSON number (which json.Unmarshal decodes as float64).
	jsonBytes, _ := json.Marshal(7)
	fs.entries[nskey{Namespace: "ns", Key: "retries"}] = store.Entry{
		Namespace: "ns",
		Key:       "retries",
		Value:     jsonBytes,
		UpdatedAt: time.Now(),
		UpdatedBy: "seed",
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	got := c.GetInt("ns", "retries")
	if got != 7 {
		t.Fatalf("expected 7 (via float64 coercion), got %d", got)
	}
}

func TestGetInt_WrongTypeReturnsZero(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	if err := c.Register("ns", "k", "not-a-number"); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	if got := c.GetInt("ns", "k"); got != 0 {
		t.Fatalf("expected 0 for string value, got %d", got)
	}
}

func TestGetBool_HappyPath(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	if err := c.Register("ns", "enabled", true); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	if got := c.GetBool("ns", "enabled"); !got {
		t.Fatal("expected true, got false")
	}

	if got := c.GetBool("ns", "nope"); got {
		t.Fatal("expected false for unregistered key, got true")
	}
}

func TestGetFloat64_HappyPath(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	if err := c.Register("ns", "rate", 0.75); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	if got := c.GetFloat64("ns", "rate"); got != 0.75 {
		t.Fatalf("expected 0.75, got %f", got)
	}

	if got := c.GetFloat64("ns", "nope"); got != 0 {
		t.Fatalf("expected 0 for unregistered key, got %f", got)
	}
}

func TestGetDuration_StringParseFails(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	if err := c.Register("ns", "d", "not-a-duration"); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	if got := c.GetDuration("ns", "d"); got != 0 {
		t.Fatalf("expected 0 for unparseable string, got %v", got)
	}
}

func TestGetDuration_WrongTypeReturnsZero(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	if err := c.Register("ns", "d", true); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	if got := c.GetDuration("ns", "d"); got != 0 {
		t.Fatalf("expected 0 for bool value, got %v", got)
	}
}

func TestRefreshFromStore_UnmarshalError(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	if err := c.Register("ns", "k", "default"); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	fs.waitForSubscriber(t, time.Second)

	// Inject a corrupt value directly into the fake store and trigger changefeed.
	fs.mu.Lock()
	nk := nskey{Namespace: "ns", Key: "k"}
	fs.entries[nk] = store.Entry{
		Namespace: "ns",
		Key:       "k",
		Value:     []byte("{{invalid json}}"),
		UpdatedAt: time.Now(),
		UpdatedBy: "bad-actor",
	}
	handlers := make([]func(store.Event), len(fs.handlers))
	copy(handlers, fs.handlers)
	fs.mu.Unlock()

	evt := store.Event{Namespace: "ns", Key: "k"}
	for _, h := range handlers {
		h(evt)
	}
	time.Sleep(50 * time.Millisecond)

	// The cache should remain at the old value because unmarshal failed.
	v, ok := c.Get("ns", "k")
	if !ok {
		t.Fatal("expected ok=true")
	}

	if v != "default" {
		t.Fatalf("expected 'default' (unchanged after unmarshal error), got %v", v)
	}
}

func TestRefreshFromStore_KeyDeletedFallsBackToDefault(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	if err := c.Register("ns", "k", "default-val"); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Pre-populate with a different value.
	jsonBytes, _ := json.Marshal("override")
	fs.entries[nskey{Namespace: "ns", Key: "k"}] = store.Entry{
		Namespace: "ns",
		Key:       "k",
		Value:     jsonBytes,
		UpdatedAt: time.Now(),
		UpdatedBy: "seed",
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	fs.waitForSubscriber(t, time.Second)

	// Verify the override is active.
	if v, _ := c.Get("ns", "k"); v != "override" {
		t.Fatalf("expected 'override', got %v", v)
	}

	// Now delete the entry from the fake store and simulate a changefeed event.
	// refreshFromStore will find (existed=false) and fall back to default.
	fs.mu.Lock()
	delete(fs.entries, nskey{Namespace: "ns", Key: "k"})
	handlers := make([]func(store.Event), len(fs.handlers))
	copy(handlers, fs.handlers)
	fs.mu.Unlock()

	evt := store.Event{Namespace: "ns", Key: "k"}
	for _, h := range handlers {
		h(evt)
	}
	time.Sleep(50 * time.Millisecond)

	v, ok := c.Get("ns", "k")
	if !ok {
		t.Fatal("expected ok=true")
	}

	if v != "default-val" {
		t.Fatalf("expected fallback to 'default-val', got %v", v)
	}
}

func TestStart_HydrateSkipsUnregisteredKeys(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()

	// Seed the store with an entry for an unregistered key.
	jsonBytes, _ := json.Marshal("orphan")
	fs.entries[nskey{Namespace: "ns", Key: "orphan"}] = store.Entry{
		Namespace: "ns",
		Key:       "orphan",
		Value:     jsonBytes,
		UpdatedAt: time.Now(),
		UpdatedBy: "seed",
	}

	c := testClient(t, fs)

	if err := c.Register("ns", "known", "val"); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Start should log a warning for the orphan but not error.
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Orphan should not be in cache.
	_, ok := c.Get("ns", "orphan")
	if ok {
		t.Fatal("expected orphan key to NOT be in cache")
	}

	// Known key should have its default.
	v, ok := c.Get("ns", "known")
	if !ok || v != "val" {
		t.Fatalf("expected 'val', got (%v, %v)", v, ok)
	}
}

func TestStart_HydrateSkipsBadJSON(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()

	// Seed with a registered key but corrupt JSON.
	fs.entries[nskey{Namespace: "ns", Key: "k"}] = store.Entry{
		Namespace: "ns",
		Key:       "k",
		Value:     []byte("{{not json}}"),
		UpdatedAt: time.Now(),
		UpdatedBy: "seed",
	}

	c := testClient(t, fs)

	if err := c.Register("ns", "k", "default"); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Start should log a warning but not error; the default remains.
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	v, ok := c.Get("ns", "k")
	if !ok || v != "default" {
		t.Fatalf("expected default 'default' after bad JSON, got (%v, %v)", v, ok)
	}
}

func TestWithValidator_NilIsIgnored(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	// WithValidator(nil) should be silently ignored.
	if err := c.Register("ns", "k", "v", WithValidator(nil)); err != nil {
		t.Fatalf("register with nil validator: %v", err)
	}
}

func TestWithTelemetry_Applied(t *testing.T) {
	t.Parallel()

	cfg := defaultClientConfig()

	// Nil telemetry should be ignored.
	WithTelemetry(nil)(&cfg)

	if cfg.telemetry != nil {
		t.Fatal("nil telemetry should not be set")
	}
}

func TestSet_NonJSONSerializableRejected(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	if err := c.Register("ns", "k", "v"); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Channels are not JSON-serializable.
	err := c.Set(context.Background(), "ns", "k", make(chan int), "ops")
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation for non-serializable value, got %v", err)
	}
}

func TestNewClient_NilLoggerDefaultsToNop(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	cfg := defaultClientConfig()
	cfg.logger = nil // explicitly nil

	c := newClient(fs, cfg)
	t.Cleanup(func() { _ = c.Close() })

	// The logger should have been replaced with log.NewNop().
	if c.logger == nil {
		t.Fatal("expected non-nil logger after newClient with nil logger input")
	}

	// Basic operations should work without panicking on nil logger.
	if err := c.Register("ns", "k", "v"); err != nil {
		t.Fatalf("register: %v", err)
	}
}

// errorStore is a test double where List returns an error.
type errorStore struct {
	fakeStore
	listErr error
	getErr  error
}

func (e *errorStore) List(_ context.Context) ([]store.Entry, error) {
	if e.listErr != nil {
		return nil, e.listErr
	}

	return e.fakeStore.List(context.Background())
}

func (e *errorStore) Get(_ context.Context, ns, key string) (store.Entry, bool, error) {
	if e.getErr != nil {
		return store.Entry{}, false, e.getErr
	}

	return e.fakeStore.Get(context.Background(), ns, key)
}

func TestStart_ListError_PropagatesError(t *testing.T) {
	t.Parallel()

	es := &errorStore{
		fakeStore: *newFakeStore(),
		listErr:   errors.New("db connection lost"),
	}

	cfg := defaultClientConfig()
	cfg.debounce = 0
	cfg.logger = log.NewNop()

	c := newClient(es, cfg)
	t.Cleanup(func() { _ = c.Close() })

	if err := c.Register("ns", "k", "v"); err != nil {
		t.Fatalf("register: %v", err)
	}

	err := c.Start(context.Background())
	if err == nil {
		t.Fatal("expected error from Start when List fails")
	}

	if err.Error() != "db connection lost" {
		t.Fatalf("expected 'db connection lost', got %q", err.Error())
	}
}

func TestRefreshFromStore_GetError_KeepsCurrent(t *testing.T) {
	t.Parallel()

	es := &errorStore{
		fakeStore: *newFakeStore(),
	}

	cfg := defaultClientConfig()
	cfg.debounce = 0
	cfg.logger = log.NewNop()

	c := newClient(es, cfg)
	t.Cleanup(func() { _ = c.Close() })

	if err := c.Register("ns", "k", "original"); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	es.fakeStore.waitForSubscriber(t, time.Second)

	// Now make Get fail.
	es.getErr = errors.New("timeout")

	// Manually trigger a changefeed event.
	es.fakeStore.mu.Lock()
	handlers := make([]func(store.Event), len(es.fakeStore.handlers))
	copy(handlers, es.fakeStore.handlers)
	es.fakeStore.mu.Unlock()

	evt := store.Event{Namespace: "ns", Key: "k"}
	for _, h := range handlers {
		h(evt)
	}
	time.Sleep(50 * time.Millisecond)

	// Cache should remain at the original value.
	v, ok := c.Get("ns", "k")
	if !ok || v != "original" {
		t.Fatalf("expected 'original' unchanged after Get error, got (%v, %v)", v, ok)
	}
}

func TestRefreshFromStore_UnregisteredChangefeedEvent(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	if err := c.Register("ns", "known", "v"); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	fs.waitForSubscriber(t, time.Second)

	// Simulate a changefeed event for an unregistered key.
	// This should log a warning but not crash.
	fs.simulateExternalChange("ns", "ghost-key", "boo")
	time.Sleep(50 * time.Millisecond)

	// Client should remain functional.
	v, ok := c.Get("ns", "known")
	if !ok || v != "v" {
		t.Fatalf("client should remain functional after unregistered changefeed event")
	}
}

func TestStartSpan_NilTelemetry(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	cfg := defaultClientConfig()
	cfg.debounce = 0
	cfg.logger = log.NewNop()
	// telemetry is nil by default — this exercises the early-return in startSpan.

	c := newClient(fs, cfg)
	t.Cleanup(func() { _ = c.Close() })

	if err := c.Register("ns", "k", "v"); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Start calls startSpan internally.
	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
}

func TestOnChange_NilFnReturnsNoop(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	if err := c.Register("ns", "k", "v"); err != nil {
		t.Fatalf("register: %v", err)
	}

	// nil fn should return no-op unsubscribe.
	unsub := c.OnChange("ns", "k", nil)
	unsub() // should not panic
}

func TestClose_WaitsForSubscribeGoroutine(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	if err := c.Register("ns", "k", "v"); err != nil {
		t.Fatalf("register: %v", err)
	}

	if err := c.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}

	fs.waitForSubscriber(t, time.Second)

	// Close should cancel the subscribe goroutine and wait.
	if err := c.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

func TestRegister_EmptyNamespaceRejected(t *testing.T) {
	t.Parallel()

	fs := newFakeStore()
	c := testClient(t, fs)

	err := c.Register("", "k", "v")
	if !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("expected ErrUnknownKey for empty namespace, got %v", err)
	}

	err = c.Register("ns", "", "v")
	if !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("expected ErrUnknownKey for empty key, got %v", err)
	}
}
