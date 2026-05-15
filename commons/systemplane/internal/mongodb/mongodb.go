// Package mongodb implements the internal store.Store interface over MongoDB
// with change-streams (default) or polling (when a non-zero PollInterval is set).
//
// Change-streams require a replica set; standalone deployments should use
// polling by setting Config.PollInterval to a positive duration.
//
// Tenant scoping:
//   - Every document carries a tenant_id field. The sentinel "_global" marks
//     rows owned by the legacy (non-tenant-scoped) API surface. Any other
//     value is a tenant-specific override.
//   - The document _id is a compound sub-document {namespace, key, tenant_id}.
//     A compound _id (instead of ObjectId) is what makes change-stream delete
//     events self-describing: a delete event has no fullDocument, only
//     documentKey._id, so the tuple must live there to preserve tenant
//     attribution on DeleteForTenant flows (TRD §3.2).
//   - Existing callers of Set / Get / List continue to see only "_global" rows.
//     Tenant-specific reads/writes go through SetTenantValue / GetTenantValue /
//     DeleteTenantValue / ListTenantOverrides / ListTenantsForKey.
package mongodb

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/store"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const (
	defaultCollection = "systemplane_entries"

	bsonFieldID         = "_id"
	bsonFieldNamespace  = "namespace"
	bsonFieldKey        = "key"
	bsonFieldTenantID   = "tenant_id"
	bsonFieldUpdatedAt  = "updated_at"
	bsonFieldOwner      = "owner"
	bsonFieldHeartbeat  = "heartbeat"
	bsonFieldAcquiredAt = "acquired_at"

	bsonOpSet         = "$set"
	bsonOpSetOnInsert = "$setOnInsert"
	bsonOpGT          = "$gt"

	// reconnectBaseDelay is the base duration for exponential backoff on
	// change-stream reconnects.
	reconnectBaseDelay = 500 * time.Millisecond

	// reconnectMaxDelay caps the change-stream reconnect backoff.
	reconnectMaxDelay = 30 * time.Second

	// tracerName is the OpenTelemetry tracer name for this package.
	tracerName = "systemplane.mongodb"

	// defaultSchemaInitTimeout bounds the time spent running migration and
	// index creation at construction time when Config.SchemaInitTimeout is
	// zero. Kept deliberately generous because Phase-2 migration walks every
	// ObjectId-_id row sequentially; operators with large collections can
	// raise it via Config.SchemaInitTimeout.
	defaultSchemaInitTimeout = 30 * time.Second
)

// Compile-time interface check. Any divergence from store.Store fails the
// build loudly rather than at runtime.
var _ store.Store = (*Store)(nil)

// Config holds the parameters needed to construct a MongoDB-backed Store.
type Config struct {
	// Client is the MongoDB client handle. Must not be nil.
	Client *mongo.Client

	// Database is the MongoDB database name. Must not be empty.
	Database string

	// Collection is the MongoDB collection name.
	// Default: "systemplane_entries".
	Collection string

	// PollInterval enables polling mode when set to a positive duration.
	// A zero value (the default) uses change-streams, which require a replica
	// set. Recommended minimum when set: 500ms — anything tighter risks
	// saturating the primary with watermark queries without a proportional
	// delivery-latency benefit.
	PollInterval time.Duration

	// SchemaInitTimeout bounds the time spent running the Phase-2 migration
	// and index creation during New(). A zero value falls back to
	// defaultSchemaInitTimeout (30s). Raise this for large collections where
	// rewriteObjectIDDocuments needs to walk tens of thousands of rows.
	SchemaInitTimeout time.Duration

	// Logger is the structured logger.
	Logger log.Logger

	// Telemetry is the OpenTelemetry provider for spans and metrics.
	Telemetry *opentelemetry.Telemetry

	// TenantSchemaEnabled opts the backend into phase-2 schema. When false
	// (the default), ensureSchema keeps a unique index on (namespace, key)
	// so pre-tenant lib-commons binaries (v5.0.x) can continue to upsert
	// under the legacy ObjectId _id shape. Tenant writes return
	// ErrTenantSchemaNotEnabled. When true, the legacy ObjectId _id is
	// migrated to the compound {namespace, key, tenant_id} shape and
	// tenant writes are permitted.
	TenantSchemaEnabled bool

	// LoadResumeToken, when non-nil, is invoked once per change-stream open
	// (including reconnects) to recover the last persisted resume token. A
	// returned (token, nil) is passed to options.ChangeStream().SetResumeAfter
	// so events that occurred during a disconnect window are replayed from the
	// oplog. A returned (_, err) is logged and fails soft — the change stream
	// opens from the current oplog position without resume, matching the
	// pre-H2 behavior. A (nil, nil) return indicates "no prior token" (fresh
	// subscriber) and is handled the same way.
	LoadResumeToken func(ctx context.Context) (bson.Raw, error)

	// SaveResumeToken, when non-nil, is invoked periodically from the event
	// loop to persist the latest ResumeToken observed. The batching cadence
	// (every N events or T duration) is internal to the subscription loop.
	// SaveResumeToken MUST be idempotent and MUST tolerate being called
	// concurrently with itself only via external synchronization — the loop
	// serializes calls. Failures are logged at WARN and do not interrupt
	// event delivery; the next successful Save replaces the lost one.
	SaveResumeToken func(ctx context.Context, token bson.Raw) error
}

// compoundID is the shape of the document _id.
//
// Storing the full (namespace, key, tenant_id) tuple in _id guarantees that
// change-stream delete events — which surface only documentKey, never
// fullDocument — still carry enough information to route the deletion to the
// correct tenant-aware subscriber. Without this, delete events would require
// change-stream pre/post-images (MongoDB 6.0+ server-side opt-in) or a
// re-read that cannot work because the document is gone. See TRD §3.2.
type compoundID struct {
	Namespace string `bson:"namespace"`
	Key       string `bson:"key"`
	TenantID  string `bson:"tenant_id"`
}

// entryDoc is the BSON document shape persisted in the collection.
//
// The ID field mirrors the compoundID triple redundantly into the top-level
// namespace / key / tenant_id fields. Top-level fields are what
// ListTenantsForKey's Distinct and the polling path's filters read; the _id
// is what the change-stream delete path extracts. Both paths stay cheap.
//
// ID is populated by the BSON decoder (struct-tag driven) and is only read on
// the migration delete path (rewriteObjectIDDocuments), where the raw legacy
// _id value is used to target a DeleteOne against the pre-migration row.
// All other call sites work with the top-level namespace/key/tenant_id trio.
type entryDoc struct {
	ID        compoundID `bson:"_id"`
	Namespace string     `bson:"namespace"`
	Key       string     `bson:"key"`
	TenantID  string     `bson:"tenant_id"`
	Value     string     `bson:"value"`
	UpdatedAt time.Time  `bson:"updated_at"`
	UpdatedBy string     `bson:"updated_by"`
}

// toEntry converts a BSON document into the public store.Entry type.
func (d entryDoc) toEntry() store.Entry {
	return store.Entry{
		Namespace: d.Namespace,
		Key:       d.Key,
		TenantID:  d.TenantID,
		Value:     []byte(d.Value),
		UpdatedAt: d.UpdatedAt,
		UpdatedBy: d.UpdatedBy,
	}
}

// Store implements [store.Store] over MongoDB.
type Store struct {
	cfg    Config
	coll   *mongo.Collection
	tracer trace.Tracer

	// ctx/cancel scope all long-running subscriptions to the Store lifetime.
	// Close cancels ctx so any in-flight Subscribe call unwinds its
	// change-stream or polling loop even when the caller's ctx is still live.
	ctx    context.Context
	cancel context.CancelFunc

	mu     sync.Mutex
	closed bool

	// droppedEvents counts change-stream events that were dropped because
	// their identifying tuple was incomplete (see extractEvent). Surfaced via
	// DroppedEvents() for operator dashboards; the WARN log is still the
	// primary signal.
	droppedEvents atomic.Int64
}

// New creates a MongoDB-backed Store. Returns an error if the Config is invalid.
//
// Schema bootstrap is two-phased to support rolling deploys:
//
//   - Phase 1 (default, TenantSchemaEnabled=false): ensureLegacySchema creates
//     a unique index on (namespace, key). Documents retain their legacy
//     ObjectId _id shape. Tenant writes return ErrTenantSchemaNotEnabled.
//     This is the rolling-deploy safe configuration: pre-tenant binaries
//     (v5.0.x) can continue to upsert against the same collection.
//   - Phase 2 (TenantSchemaEnabled=true): ensureSchema drops the legacy
//     unique index and runs the compound _id migration. After this phase
//     the backend accepts tenant writes.
//
// When PollInterval is zero, Subscribe uses change-streams (requires a replica
// set). When PollInterval is positive, Subscribe uses a polling loop that works
// with standalone MongoDB deployments.
func New(cfg Config) (*Store, error) {
	if cfg.Client == nil {
		return nil, store.ErrNilBackend
	}

	if cfg.Database == "" {
		return nil, fmt.Errorf("mongodb store: %w: database name is required", store.ErrNilBackend)
	}

	if cfg.Collection == "" {
		cfg.Collection = defaultCollection
	}

	initTimeout := cfg.SchemaInitTimeout
	if initTimeout <= 0 {
		initTimeout = defaultSchemaInitTimeout
	}

	coll := cfg.Client.Database(cfg.Database).Collection(cfg.Collection)

	initCtx, cancelInit := context.WithTimeout(context.Background(), initTimeout)
	defer cancelInit()

	logger := cfg.Logger

	if cfg.TenantSchemaEnabled {
		if err := ensureSchema(initCtx, coll, logger); err != nil {
			return nil, fmt.Errorf("mongodb store: schema init: %w", err)
		}
	} else {
		if err := ensureLegacySchema(initCtx, coll); err != nil {
			return nil, fmt.Errorf("mongodb store: schema init: %w", err)
		}
	}

	tracer := noop.NewTracerProvider().Tracer(tracerName)
	if cfg.Telemetry != nil {
		if t, err := cfg.Telemetry.Tracer(tracerName); err == nil {
			tracer = t
		}
	}

	ctx, cancel := context.WithCancel(context.Background())

	return &Store{
		cfg:    cfg,
		coll:   coll,
		tracer: tracer,
		ctx:    ctx,
		cancel: cancel,
	}, nil
}

// List returns all "_global" entries from the MongoDB collection.
//
// This is the legacy (non-tenant-scoped) read path and intentionally excludes
// tenant-specific rows so that existing consumers of List see only the shared
// global configuration. Tenant hydration uses ListTenantOverrides (the hot
// path; see tenant_hydration.go) or ListTenantValues (full listing; used by
// the contract test suite).
func (s *Store) List(ctx context.Context) ([]store.Entry, error) {
	if s == nil || s.isClosed() {
		return nil, store.ErrClosed
	}

	ctx, span := s.tracer.Start(ctx, "systemplane.mongodb.list")
	defer span.End()

	filter := bson.D{{Key: bsonFieldTenantID, Value: store.SentinelGlobal}}

	cursor, err := s.coll.Find(ctx, filter)
	if err != nil {
		opentelemetry.HandleSpanError(span, "mongodb list: find failed", err)
		return nil, fmt.Errorf("mongodb store list: %w", err)
	}
	defer cursor.Close(ctx)

	var docs []entryDoc
	if err := cursor.All(ctx, &docs); err != nil {
		opentelemetry.HandleSpanError(span, "mongodb list: decode failed", err)
		return nil, fmt.Errorf("mongodb store list: decode: %w", err)
	}

	entries := make([]store.Entry, len(docs))
	for i := range docs {
		entries[i] = docs[i].toEntry()
	}

	span.SetAttributes(attribute.Int("entries.count", len(entries)))

	return entries, nil
}

// Get returns a single "_global" entry by namespace and key.
// Returns (_, false, nil) when the entry does not exist.
//
// Get is strictly global-scoped: tenant-specific rows are invisible to this
// method. See GetTenantValue for tenant-aware reads.
func (s *Store) Get(ctx context.Context, namespace, key string) (store.Entry, bool, error) {
	if s == nil || s.isClosed() {
		return store.Entry{}, false, store.ErrClosed
	}

	ctx, span := s.tracer.Start(ctx, "systemplane.mongodb.get")
	defer span.End()

	span.SetAttributes(
		attribute.String("entry.namespace", namespace),
		attribute.String("entry.key", key),
	)

	doc, found, err := s.findOne(ctx, namespace, key, store.SentinelGlobal)
	if err != nil {
		opentelemetry.HandleSpanError(span, "mongodb get: find failed", err)
		return store.Entry{}, false, fmt.Errorf("mongodb store get: %w", err)
	}

	if !found {
		return store.Entry{}, false, nil
	}

	return doc.toEntry(), true, nil
}

// Set persists a "_global" entry using an upsert. The change-stream or
// polling loop picks up the modification and notifies subscribers.
//
// Set is strictly global-scoped: it unconditionally writes tenant_id="_global"
// regardless of the Entry.TenantID field on input. Tenant writes must go
// through SetTenantValue. This matches TRD §9 (Backward Compatibility Matrix).
func (s *Store) Set(ctx context.Context, e store.Entry) error {
	if s == nil || s.isClosed() {
		return store.ErrClosed
	}

	ctx, span := s.tracer.Start(ctx, "systemplane.mongodb.set")
	defer span.End()

	span.SetAttributes(
		attribute.String("entry.namespace", e.Namespace),
		attribute.String("entry.key", e.Key),
	)

	if e.Namespace == "" || e.Key == "" {
		err := errors.New("mongodb store set: namespace and key must be non-empty")
		opentelemetry.HandleSpanBusinessErrorEvent(span, "validation failed", err)

		return err
	}

	if err := s.upsert(ctx, e, store.SentinelGlobal); err != nil {
		opentelemetry.HandleSpanError(span, "mongodb set: upsert failed", err)
		return fmt.Errorf("mongodb store set: %w", err)
	}

	return nil
}

// Close releases any resources held by the Store. Idempotent.
// Does NOT close s.cfg.Client — the caller owns the MongoDB client lifecycle.
//
// Close cancels s.ctx, which terminates any in-flight Subscribe call even
// when the caller's Subscribe ctx is still live. Without this, a goroutine
// that did `store.Subscribe(context.Background(), ...)` would leak past
// Close until its process exit.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.closed {
		return nil
	}

	s.closed = true

	if s.cancel != nil {
		s.cancel()
	}

	return nil
}

// DroppedEvents returns the total number of change-stream events dropped due
// to incomplete identifiers since Store construction. Exposed for operator
// dashboards and canary checks. Concurrency-safe (atomic load).
func (s *Store) DroppedEvents() int64 {
	if s == nil {
		return 0
	}

	return s.droppedEvents.Load()
}

// isClosed checks whether the store has been closed.
func (s *Store) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.closed
}

// logWarn logs a warning-level message via the configured logger. Centralized
// so every emitter in the package goes through the same nil-guard — without
// it, call sites scatter `if s.cfg.Logger != nil` checks that drift over
// time.
func (s *Store) logWarn(ctx context.Context, msg string, fields ...log.Field) {
	if s == nil || s.cfg.Logger == nil {
		return
	}

	s.cfg.Logger.Log(ctx, log.LevelWarn, msg, fields...)
}

// logInfo logs an info-level message via the configured logger. Symmetric
// with logWarn; used by audit-style emissions (tenant delete, migration
// decisions) where WARN would overstate the severity.
func (s *Store) logInfo(ctx context.Context, msg string, fields ...log.Field) {
	if s == nil || s.cfg.Logger == nil {
		return
	}

	s.cfg.Logger.Log(ctx, log.LevelInfo, msg, fields...)
}
