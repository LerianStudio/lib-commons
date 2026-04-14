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

	"github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/LerianStudio/lib-commons/v4/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v4/commons/runtime"
	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/internal/debounce"
	mongoDB "github.com/LerianStudio/lib-commons/v4/commons/systemplane/internal/mongodb"
	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/internal/postgres"
	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/internal/store"
	"go.opentelemetry.io/otel/trace"
)

// closeWaitTimeout is the maximum time Close waits for the Subscribe goroutine
// to finish before proceeding with teardown.
const closeWaitTimeout = 10 * time.Second

// refreshTimeout is the deadline for individual store.Get calls during
// changefeed-driven refresh.
const refreshTimeout = 5 * time.Second

// tracerName is the OpenTelemetry instrumentation scope name for the Client.
const tracerName = "systemplane.client"

// nskey is the composite map key for registry/cache/subscriber lookups.
// Namespace and Key are separated structurally rather than via string
// concatenation, preventing ambiguity when either contains special characters.
type nskey struct {
	Namespace string
	Key       string
}

// subscription holds a single OnChange callback and its monotonic id.
type subscription struct {
	id uint64
	fn func(newValue any)
}

// Client is the runtime-config handle. Read methods are nil-receiver safe,
// returning zero values when the Client is nil or not yet started.
type Client struct {
	store     store.Store
	debouncer *debounce.Debouncer
	logger    log.Logger
	telemetry *opentelemetry.Telemetry

	// registry is populated by Register (before Start) and read-only after Start.
	registryMu sync.RWMutex
	registry   map[nskey]keyDef

	// cache holds effective values (default or override). Protected by cacheMu.
	cacheMu sync.RWMutex
	cache   map[nskey]any

	// subscribers holds per-key OnChange callbacks. Protected by subsMu.
	subsMu      sync.RWMutex
	subscribers map[nskey][]subscription
	nextSubID   atomic.Uint64

	startOnce sync.Once
	started   atomic.Bool
	closeOnce sync.Once
	closed    atomic.Bool
	cancel    context.CancelFunc // cancels the Subscribe goroutine
	wg        sync.WaitGroup     // tracks the Subscribe goroutine
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
		DB:        db,
		ListenDSN: listenDSN,
		Channel:   cfg.listenChannel,
		Table:     cfg.table,
		Logger:    cfg.logger,
		Telemetry: cfg.telemetry,
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
		Client:       client,
		Database:     database,
		Collection:   cfg.collection,
		PollInterval: cfg.pollInterval,
		Logger:       cfg.logger,
		Telemetry:    cfg.telemetry,
	})
	if err != nil {
		return nil, err
	}

	return newClient(mStore, cfg), nil
}

// newClientFromStore builds a Client from an already-constructed Store.
// Exported only within the package (lowercase) — used by both constructors
// and by tests that supply a fake store.
func newClient(s store.Store, cfg clientConfig) *Client {
	logger := cfg.logger
	if logger == nil {
		logger = log.NewNop()
	}

	return &Client{
		store:       s,
		debouncer:   debounce.New(cfg.debounce, debounce.WithLogger(logger)),
		logger:      logger,
		telemetry:   cfg.telemetry,
		registry:    make(map[nskey]keyDef),
		cache:       make(map[nskey]any),
		subscribers: make(map[nskey][]subscription),
	}
}

// Start hydrates initial values from the backing store and begins listening
// for changes. It is idempotent; calling Start on an already-started Client
// returns nil silently.
func (c *Client) Start(ctx context.Context) error {
	if c == nil || c.closed.Load() {
		return ErrClosed
	}

	var startErr error

	c.startOnce.Do(func() {
		ctx, span, finish := c.startSpan(ctx, "systemplane.client.start") //nolint:govet // ctx shadow is intentional — span context must propagate
		defer finish()

		// 1. Seed the cache with registered defaults.
		c.registryMu.RLock()

		for nk, def := range c.registry {
			c.cache[nk] = def.defaultValue
		}

		c.registryMu.RUnlock()

		// 2. Hydrate from persistent store: overwrite defaults with stored values.
		entries, err := c.store.List(ctx)
		if err != nil {
			opentelemetry.HandleSpanError(span, "hydration failed", err)
			startErr = err

			return
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

		// 3. Launch the Subscribe goroutine with its own cancellable context.
		subCtx, cancel := context.WithCancel(context.Background()) //nolint:gosec // G118: cancel stored in c.cancel and invoked by Close()
		c.cancel = cancel

		c.wg.Go(func() {
			if err := c.store.Subscribe(subCtx, c.onEvent); err != nil {
				if subCtx.Err() == nil {
					c.logWarn(subCtx, "subscribe returned error",
						log.Err(err),
					)
				}
			}
		})

		c.started.Store(true)
	})

	return startErr
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

// onEvent is the raw changefeed handler. It debounces per (namespace, key) to
// coalesce rapid updates into a single refresh.
//
// The composite key uses U+001F (Unit Separator) as a delimiter, which is a
// control character guaranteed not to appear in reasonable namespace/key strings.
func (c *Client) onEvent(evt store.Event) {
	compositeKey := evt.Namespace + "\x1f" + evt.Key

	c.debouncer.Submit(compositeKey, func() {
		c.refreshFromStore(evt.Namespace, evt.Key)
	})
}

// refreshFromStore re-reads a single key from the backend, updates the cache,
// and fires OnChange subscribers.
func (c *Client) refreshFromStore(ns, key string) {
	nk := nskey{Namespace: ns, Key: key}

	// 1. Look up registration.
	c.registryMu.RLock()
	def, registered := c.registry[nk]
	c.registryMu.RUnlock()

	if !registered {
		c.logWarn(context.Background(), "changefeed event for unregistered key, skipping",
			log.String("namespace", ns),
			log.String("key", key),
		)

		return
	}

	// 2. Fetch from store with a bounded timeout.
	ctx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
	defer cancel()

	entry, found, err := c.store.Get(ctx, ns, key)
	if err != nil {
		c.logWarn(ctx, "refresh from store failed",
			log.String("namespace", ns),
			log.String("key", key),
			log.Err(err),
		)

		return
	}

	// 3. Resolve the new value: persisted or default.
	newValue := def.defaultValue

	if found {
		var decoded any
		if err := json.Unmarshal(entry.Value, &decoded); err != nil {
			c.logWarn(ctx, "failed to unmarshal refreshed value, keeping current",
				log.String("namespace", ns),
				log.String("key", key),
				log.Err(err),
			)

			return
		}

		newValue = decoded
	}

	// 4. Update cache.
	c.cacheMu.Lock()
	c.cache[nk] = newValue
	c.cacheMu.Unlock()

	// 5. Fire subscribers.
	c.fireSubscribers(nk, newValue)
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
	noop := func() {}

	if c.telemetry == nil {
		return ctx, trace.SpanFromContext(ctx), noop
	}

	tracer, err := c.telemetry.Tracer(tracerName)
	if err != nil || tracer == nil {
		return ctx, trace.SpanFromContext(ctx), noop
	}

	ctx, span := tracer.Start(ctx, name)

	return ctx, span, func() { span.End() }
}

// logWarn emits a warning-level log via the configured logger.
func (c *Client) logWarn(ctx context.Context, msg string, fields ...log.Field) {
	if c.logger != nil {
		c.logger.Log(ctx, log.LevelWarn, msg, fields...)
	}
}
