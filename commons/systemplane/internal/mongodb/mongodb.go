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
//     DeleteTenantValue / ListTenantValues / ListTenantsForKey.
package mongodb

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/store"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

const (
	defaultCollection = "systemplane_entries"

	// reconnectBaseDelay is the base duration for exponential backoff on
	// change-stream reconnects.
	reconnectBaseDelay = 500 * time.Millisecond

	// reconnectMaxDelay caps the change-stream reconnect backoff.
	reconnectMaxDelay = 30 * time.Second

	// tracerName is the OpenTelemetry tracer name for this package.
	tracerName = "systemplane.mongodb"

	// sentinelGlobal is the tenant_id value used for shared (non-tenant-scoped)
	// rows. The tenant-manager regex ^[a-zA-Z0-9][a-zA-Z0-9_-]*$ forbids a
	// real tenant from starting with "_", so the sentinel cannot collide.
	// See TRD §3.1 and commons/tenant-manager/core/validation.go.
	sentinelGlobal = "_global"

	// schemaInitTimeout bounds the time spent running migration and index
	// creation at construction time.
	schemaInitTimeout = 30 * time.Second
)

// Compile-time interface check.
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
	// A zero value (the default) uses change-streams, which require a
	// replica set.
	PollInterval time.Duration

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

	mu     sync.Mutex
	closed bool
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

	coll := cfg.Client.Database(cfg.Database).Collection(cfg.Collection)

	ctx, cancel := context.WithTimeout(context.Background(), schemaInitTimeout)
	defer cancel()

	schemaFn := ensureLegacySchema
	if cfg.TenantSchemaEnabled {
		schemaFn = ensureSchema
	}

	if err := schemaFn(ctx, coll); err != nil {
		return nil, fmt.Errorf("mongodb store: schema init: %w", err)
	}

	tracer := noop.NewTracerProvider().Tracer(tracerName)
	if cfg.Telemetry != nil {
		if t, err := cfg.Telemetry.Tracer(tracerName); err == nil {
			tracer = t
		}
	}

	return &Store{
		cfg:    cfg,
		coll:   coll,
		tracer: tracer,
	}, nil
}

// List returns all "_global" entries from the MongoDB collection.
//
// This is the legacy (non-tenant-scoped) read path and intentionally excludes
// tenant-specific rows so that existing consumers of List see only the shared
// global configuration. Tenant hydration uses ListTenantValues.
func (s *Store) List(ctx context.Context) ([]store.Entry, error) {
	if s == nil || s.isClosed() {
		return nil, store.ErrClosed
	}

	ctx, span := s.tracer.Start(ctx, "systemplane.mongodb.list")
	defer span.End()

	filter := bson.D{{Key: "tenant_id", Value: sentinelGlobal}}

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

	doc, found, err := s.findOne(ctx, namespace, key, sentinelGlobal)
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

	if err := s.upsert(ctx, e, sentinelGlobal); err != nil {
		opentelemetry.HandleSpanError(span, "mongodb set: upsert failed", err)
		return fmt.Errorf("mongodb store set: %w", err)
	}

	return nil
}

// Close releases any resources held by the Store. Idempotent.
// Does NOT close s.cfg.Client — the caller owns the MongoDB client lifecycle.
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

	return nil
}

// isClosed checks whether the store has been closed.
func (s *Store) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.closed
}

// findOne runs a FindOne keyed on the compound (namespace, key, tenant_id)
// tuple. Returns (entryDoc{}, false, nil) when no document matches.
func (s *Store) findOne(ctx context.Context, namespace, key, tenantID string) (entryDoc, bool, error) {
	filter := bson.D{
		{Key: "namespace", Value: namespace},
		{Key: "key", Value: key},
		{Key: "tenant_id", Value: tenantID},
	}

	var doc entryDoc

	err := s.coll.FindOne(ctx, filter).Decode(&doc)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return entryDoc{}, false, nil
		}

		return entryDoc{}, false, err //nolint:wrapcheck // caller wraps with method context
	}

	return doc, true, nil
}

// upsert writes an entry under the given tenantID. On insert the document
// lands with a compound _id {namespace, key, tenant_id}; on update only the
// mutable fields are set (namespace/key/tenant_id are pinned to _id values).
func (s *Store) upsert(ctx context.Context, e store.Entry, tenantID string) error {
	now := time.Now().UTC()
	if !e.UpdatedAt.IsZero() {
		now = e.UpdatedAt
	}

	id := compoundID{
		Namespace: e.Namespace,
		Key:       e.Key,
		TenantID:  tenantID,
	}

	filter := bson.D{{Key: "_id", Value: id}}

	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "namespace", Value: e.Namespace},
			{Key: "key", Value: e.Key},
			{Key: "tenant_id", Value: tenantID},
			{Key: "value", Value: string(e.Value)},
			{Key: "updated_at", Value: now},
			{Key: "updated_by", Value: e.UpdatedBy},
		}},
	}

	opts := options.UpdateOne().SetUpsert(true)

	if _, err := s.coll.UpdateOne(ctx, filter, update, opts); err != nil {
		return err //nolint:wrapcheck // caller wraps with method context
	}

	return nil
}

// logWarn logs a warning-level message via the configured logger.
func (s *Store) logWarn(ctx context.Context, msg string, fields ...log.Field) {
	if s.cfg.Logger == nil {
		return
	}

	s.cfg.Logger.Log(ctx, log.LevelWarn, msg, fields...)
}
