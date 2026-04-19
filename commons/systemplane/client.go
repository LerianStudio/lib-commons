// Client lifecycle: construction, start, and close for systemplane.
package systemplane

import (
	"context"
	"database/sql"
	"encoding/json"
	"sync"
	"sync/atomic"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo"
	"golang.org/x/sync/singleflight"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v5/commons/runtime"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/debounce"
	mongoDB "github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/mongodb"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/postgres"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/store"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// closeWaitTimeout is the maximum time Close waits for the Subscribe goroutine
// to finish before proceeding with teardown.
const closeWaitTimeout = 10 * time.Second

// refreshTimeout is the deadline for individual store.Get calls during
// changefeed-driven refresh.
const refreshTimeout = 5 * time.Second

// tenantStoreTimeout is the deadline for individual store.GetTenantValue /
// SetTenantValue / DeleteTenantValue / ListTenantsForKey calls issued
// internally (e.g. lazy-mode cache miss, ListTenantsForKey without caller
// context). Set-like writes issued via SetForTenant / DeleteForTenant are
// bounded by the caller's ctx; this timeout only applies when the Client
// synthesizes its own background context.
const tenantStoreTimeout = 5 * time.Second

// tracerName is the OpenTelemetry instrumentation scope name for the Client.
const tracerName = "systemplane.client"

// nskey is the composite map key for registry/cache/subscriber lookups.
// Namespace and Key are separated structurally rather than via string
// concatenation, preventing ambiguity when either contains special characters.
type nskey struct {
	Namespace string
	Key       string
}

// evtKey is the composite debouncer key for changefeed event coalescing:
// (namespace, key, tenantID). All fields are comparable strings, so the
// struct itself is a valid Go map key. Using a struct rather than a
// concatenated string eliminates the per-event allocation that the
// previous "ns + \x1f + key + \x1f + tenantID" scheme incurred on every
// NOTIFY / change-stream event — a measurable GC-pressure source at high
// update rates.
type evtKey struct {
	Namespace string
	Key       string
	TenantID  string
}

// subscription holds a single OnChange callback and its monotonic id.
type subscription struct {
	id uint64
	fn func(newValue any)
}

// Client is the runtime-config handle. Read methods are nil-receiver safe,
// returning zero values when the Client is nil or not yet started.
//
// # Mutex hierarchy
//
// Locks are ordered to prevent deadlock. No code path holds two RWMutex
// writes simultaneously:
//
//  1. startMu       (Mutex)    — serializes Start()
//  2. registryMu    (RWMutex)  — protects registry AND tenantScopedRegistry
//  3. cacheMu       (RWMutex)  — protects cache AND tenantCache
//  4. subsMu        (RWMutex)  — protects OnChange subscribers
//  5. tenantSubsMu  (RWMutex)  — protects OnTenantChange subscribers
//
// Dispatch follows the existing "RLock → copy → RUnlock → invoke under panic
// shield" pattern (see fireSubscribers). The convention is: take at most one
// write lock at a time; for chained reads across registry + cache, acquire
// both RLocks in registry→cache order and release in reverse (see List in
// get.go for the reference pattern).
type Client struct {
	store     store.Store
	debouncer *debounce.Debouncer[evtKey]
	logger    log.Logger
	telemetry *opentelemetry.Telemetry

	// registry is populated by Register (before Start) and read-only after Start.
	// registryMu also guards tenantScopedRegistry — both are set together at
	// registration time and never mutated after Start.
	registryMu           sync.RWMutex
	registry             map[nskey]keyDef
	tenantScopedRegistry map[nskey]struct{}

	// cache holds effective values (default or override). Protected by cacheMu.
	// cacheMu also guards tenantCache writes and reads; tenantCache
	// implementations are NOT internally synchronized (see tenant_cache.go).
	cacheMu     sync.RWMutex
	cache       map[nskey]any
	tenantCache tenantCache

	// subscribers holds per-key OnChange callbacks. Protected by subsMu.
	subsMu      sync.RWMutex
	subscribers map[nskey][]subscription
	nextSubID   atomic.Uint64

	// tenantSubscribers holds per-(ns,key) OnTenantChange callbacks. Protected
	// by tenantSubsMu.
	tenantSubsMu      sync.RWMutex
	tenantSubscribers map[nskey][]tenantSubscription
	nextTenantSubID   atomic.Uint64

	// tenantLoadMode drives Start's tenant hydration strategy (eager = load
	// all at Start; lazy = miss-populate under a bounded LRU). Set once at
	// construction via WithLazyTenantLoad; immutable thereafter.
	tenantLoadMode tenantLoadMode

	// sfg coalesces concurrent lazy-mode GetForTenant misses on the same
	// (tenantID, namespace, key) tuple into one backend round-trip. Used
	// only by the lazy path; eager mode never consults the backend on Get
	// and the zero value is safe for unused state.
	sfg singleflight.Group

	// metrics holds OpenTelemetry instruments lazily initialized on first
	// use via metricsOnce. See ensureMetrics / tenant_metrics.go for the
	// current instrument set. nil-safe: when c.telemetry is unset every
	// accessor no-ops.
	metricsOnce sync.Once
	metrics     *clientMetrics

	startMu   sync.Mutex
	started   atomic.Bool
	closeOnce sync.Once
	closed    atomic.Bool
	cancel    context.CancelFunc // cancels the Subscribe goroutine
	wg        sync.WaitGroup     // tracks the Subscribe goroutine
}

// tenantSubscription holds a single OnTenantChange callback and its monotonic
// id. The callback signature carries a ctx pre-scoped to tenantID (via
// core.ContextWithTenantID in fireTenantSubscribers) alongside the tenantID
// itself, so subscribers can invoke tenant-aware lib-commons facilities
// (DLQ, idempotency, webhook) without manually re-propagating the tenant.
type tenantSubscription struct {
	id uint64
	fn func(ctx context.Context, namespace, key, tenantID string, newValue any)
}

// NewPostgres creates a Client backed by a Postgres database with LISTEN/NOTIFY
// change-feed.
//
// The db handle is used for reads and writes. The listenDSN is a separate
// connection string used by pgx.Connect to establish a dedicated LISTEN
// connection; this is typically the same DSN that was used to open db, but
// must be provided explicitly because database/sql does not expose its
// underlying DSN.
func NewPostgres(db *sql.DB, listenDSN string, opts ...Option) (*Client, error) {
	if db == nil {
		return nil, store.ErrNilBackend
	}

	cfg := defaultClientConfig()
	for _, o := range opts {
		o(&cfg)
	}

	pgStore, err := postgres.New(postgres.Config{
		DB:                  db,
		ListenDSN:           listenDSN,
		Channel:             cfg.listenChannel,
		ChannelExplicit:     cfg.listenChannelExplicit,
		Table:               cfg.table,
		Logger:              cfg.logger,
		Telemetry:           cfg.telemetry,
		TenantSchemaEnabled: cfg.tenantSchemaEnabled,
	})
	if err != nil {
		return nil, err
	}

	return newClient(pgStore, cfg), nil
}

// NewMongoDB creates a Client backed by a MongoDB database with change-streams
// (or polling when WithPollInterval is set). Change-streams require a replica
// set; standalone deployments should use WithPollInterval.
func NewMongoDB(client *mongo.Client, database string, opts ...Option) (*Client, error) {
	if client == nil {
		return nil, store.ErrNilBackend
	}

	cfg := defaultClientConfig()
	for _, o := range opts {
		o(&cfg)
	}

	mStore, err := mongoDB.New(mongoDB.Config{
		Client:              client,
		Database:            database,
		Collection:          cfg.collection,
		PollInterval:        cfg.pollInterval,
		Logger:              cfg.logger,
		Telemetry:           cfg.telemetry,
		TenantSchemaEnabled: cfg.tenantSchemaEnabled,
	})
	if err != nil {
		return nil, err
	}

	return newClient(mStore, cfg), nil
}

// newClient builds a Client from an already-constructed Store. Used by the
// public NewPostgres / NewMongoDB constructors and by tests that supply a
// fake store via NewForTesting.
func newClient(s store.Store, cfg clientConfig) *Client {
	logger := cfg.logger
	if logger == nil {
		logger = log.NewNop()
	}

	tc := newTenantCacheForConfig(cfg)

	return &Client{
		store:                s,
		debouncer:            debounce.New[evtKey](cfg.debounce, debounce.WithLogger[evtKey](logger)),
		logger:               logger,
		telemetry:            cfg.telemetry,
		registry:             make(map[nskey]keyDef),
		tenantScopedRegistry: make(map[nskey]struct{}),
		cache:                make(map[nskey]any),
		tenantCache:          tc,
		subscribers:          make(map[nskey][]subscription),
		tenantSubscribers:    make(map[nskey][]tenantSubscription),
		tenantLoadMode:       tc.mode(),
	}
}

// newTenantCacheForConfig picks the tenantCache implementation matching the
// configured load mode. Eager is the default; lazy requires a positive bound
// (WithLazyTenantLoad enforces this, and newTenantCacheLRU falls back to
// eager on non-positive bounds as a defensive guard).
func newTenantCacheForConfig(cfg clientConfig) tenantCache {
	if cfg.tenantLoadMode == tenantLoadLazy && cfg.tenantCacheMax > 0 {
		return newTenantCacheLRU(cfg.tenantCacheMax)
	}

	return newTenantCacheEager()
}

// Start hydrates initial values from the backing store and begins listening
// for changes. It is idempotent; calling Start on an already-started Client
// returns nil silently.
func (c *Client) Start(ctx context.Context) error {
	if c == nil || c.closed.Load() {
		return ErrClosed
	}

	c.startMu.Lock()
	defer c.startMu.Unlock()

	// Already started — idempotent success.
	if c.started.Load() {
		return nil
	}

	ctx, span, finish := c.startSpan(ctx, "systemplane.client.start")
	defer finish()

	// 1. Seed the cache with registered defaults.
	c.registryMu.RLock()

	c.cacheMu.Lock()
	for nk, def := range c.registry {
		c.cache[nk] = def.defaultValue
	}
	c.cacheMu.Unlock()

	c.registryMu.RUnlock()

	// 2. Hydrate from persistent store: overwrite defaults with stored values.
	entries, err := c.store.List(ctx)
	if err != nil {
		opentelemetry.HandleSpanError(span, "hydration failed", err)
		return err
	}

	c.registryMu.RLock()

	for _, entry := range entries {
		nk := nskey{Namespace: entry.Namespace, Key: entry.Key}

		if _, registered := c.registry[nk]; !registered {
			c.logWarn(ctx, "unregistered key in store, skipping",
				log.String("namespace", entry.Namespace),
				log.String("key", entry.Key),
			)

			continue
		}

		var decoded any
		if err := json.Unmarshal(entry.Value, &decoded); err != nil {
			c.logWarn(ctx, "failed to unmarshal stored value, keeping default",
				log.String("namespace", entry.Namespace),
				log.String("key", entry.Key),
				log.Err(err),
			)

			continue
		}

		c.cacheMu.Lock()
		c.cache[nk] = decoded
		c.cacheMu.Unlock()
	}

	c.registryMu.RUnlock()

	// 2b. Eager-hydrate tenant overrides. In lazy mode we skip this step and
	// populate the LRU on miss. Hydration failures here are non-fatal: the
	// lazy fallback semantics already handle miss-populate so a failed
	// eager pass simply degrades to lazy-like behavior without breaking Start.
	if c.tenantLoadMode == tenantLoadEager {
		c.hydrateTenantCache(ctx)
	}

	// 3. Launch the Subscribe goroutine with its own cancellable context.
	subCtx, cancel := context.WithCancel(context.Background()) //nolint:gosec // G118: cancel stored in c.cancel and invoked by Close()
	c.cancel = cancel

	c.wg.Go(func() {
		defer runtime.RecoverAndLog(c.logger, "systemplane.subscribe")

		if err := c.store.Subscribe(subCtx, c.onEvent); err != nil {
			if subCtx.Err() == nil {
				c.logWarn(subCtx, "subscribe returned error",
					log.Err(err),
				)
			}
		}
	})

	c.started.Store(true)

	return nil
}

// Close unsubscribes from the changefeed and releases backend resources.
// Idempotent; safe to call on a nil receiver.
func (c *Client) Close() error {
	if c == nil {
		return nil
	}

	c.closeOnce.Do(func() {
		c.closed.Store(true)

		// Cancel the Subscribe goroutine if it was started.
		if c.cancel != nil {
			c.cancel()
		}

		// Wait for the Subscribe goroutine with a deadline to avoid hanging.
		done := make(chan struct{})

		go func() {
			c.wg.Wait()
			close(done)
		}()

		select {
		case <-done:
			// Clean exit.
		case <-time.After(closeWaitTimeout):
			c.logWarn(context.Background(), "timed out waiting for subscribe goroutine to exit")
		}

		c.debouncer.Close()

		// Close the backend store (does NOT close the externally-passed db/client).
		_ = c.store.Close()
	})

	return nil
}

// onEvent is the raw changefeed handler. It debounces per (namespace, key,
// tenantID) tuple to coalesce rapid updates into a single refresh.
//
// The composite key is a struct (see evtKey) — structs of comparable fields
// are valid Go map keys, so the debouncer's internal map[evtKey]*time.Timer
// works directly. This avoids the per-event string-concat allocation the
// prior "ns + \x1f + key + \x1f + tenantID" scheme required — visible GC
// pressure at high update rates. The tenantID component is always populated
// by the backend (store.SentinelGlobal "_global" for shared rows, the
// actual tenant ID otherwise), so tenant-A and tenant-B events for the
// same (namespace, key) never collide on the same debounce timer slot.
func (c *Client) onEvent(evt store.Event) {
	key := evtKey{
		Namespace: evt.Namespace,
		Key:       evt.Key,
		TenantID:  evt.TenantID,
	}

	c.debouncer.Submit(key, func() {
		c.refreshFromStoreRouted(evt.Namespace, evt.Key, evt.TenantID)
	})
}

// fireSubscribers invokes all OnChange callbacks for a key. Each callback is
// wrapped in panic recovery to prevent one misbehaving subscriber from
// disrupting others.
func (c *Client) fireSubscribers(nk nskey, newValue any) {
	c.subsMu.RLock()
	subs := make([]subscription, len(c.subscribers[nk]))
	copy(subs, c.subscribers[nk])
	c.subsMu.RUnlock()

	for _, sub := range subs {
		fn := sub.fn

		func() {
			defer runtime.RecoverAndLog(c.logger, "systemplane.onchange")

			fn(newValue)
		}()
	}
}

// startSpan creates a child span if telemetry is configured, otherwise returns
// a no-op span. Callers MUST defer finish() to end the span.
func (c *Client) startSpan(ctx context.Context, name string) (context.Context, trace.Span, func()) {
	return c.startSpanWithAttrs(ctx, name)
}

// startSpanWithAttrs creates a child span and (when telemetry is configured)
// sets the provided attributes on it before returning. Callers MUST defer
// finish() to end the span. The attributes argument is variadic so zero
// attributes is a valid — and common — call shape, used by legacy callers
// that do not need span attributes.
func (c *Client) startSpanWithAttrs(ctx context.Context, name string, attrs ...attribute.KeyValue) (context.Context, trace.Span, func()) {
	noop := func() {}

	if c.telemetry == nil {
		return ctx, trace.SpanFromContext(ctx), noop
	}

	tracer, err := c.telemetry.Tracer(tracerName)
	if err != nil || tracer == nil {
		return ctx, trace.SpanFromContext(ctx), noop
	}

	ctx, span := tracer.Start(ctx, name)

	if len(attrs) > 0 {
		span.SetAttributes(attrs...)
	}

	return ctx, span, func() { span.End() }
}

// logWarn emits a warning-level log via the configured logger.
func (c *Client) logWarn(ctx context.Context, msg string, fields ...log.Field) {
	if c.logger != nil {
		c.logger.Log(ctx, log.LevelWarn, msg, fields...)
	}
}

// logDebug emits a debug-level log via the configured logger. Nil-safe on
// the logger field for symmetry with logWarn — newClient always wires a
// non-nil logger today, but the guard future-proofs against construction
// paths that might bypass it.
func (c *Client) logDebug(ctx context.Context, msg string, fields ...log.Field) {
	if c.logger != nil {
		c.logger.Log(ctx, log.LevelDebug, msg, fields...)
	}
}
