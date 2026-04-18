// Package mongodb implements the internal store.Store interface over MongoDB
// with change-streams (default) or polling (when a non-zero PollInterval is set).
//
// Change-streams require a replica set; standalone deployments should use
// polling by setting Config.PollInterval to a positive duration.
package mongodb

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/backoff"
	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry"
	libRuntime "github.com/LerianStudio/lib-commons/v5/commons/runtime"
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
}

// entryDoc is the BSON document shape persisted in the collection.
type entryDoc struct {
	Namespace string    `bson:"namespace"`
	Key       string    `bson:"key"`
	Value     string    `bson:"value"`
	UpdatedAt time.Time `bson:"updated_at"`
	UpdatedBy string    `bson:"updated_by"`
}

// toEntry converts a BSON document into the public store.Entry type.
func (d entryDoc) toEntry() store.Entry {
	return store.Entry{
		Namespace: d.Namespace,
		Key:       d.Key,
		Value:     []byte(d.Value),
		UpdatedAt: d.UpdatedAt,
		UpdatedBy: d.UpdatedBy,
	}
}

// changeEvent is the subset of a MongoDB change stream event we decode.
type changeEvent struct {
	OperationType string  `bson:"operationType"`
	FullDocument  *bson.D `bson:"fullDocument,omitempty"`
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
// The compound unique index on (namespace, key) is created idempotently.
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

	// Create compound unique index on (namespace, key) idempotently.
	indexModel := mongo.IndexModel{
		Keys: bson.D{
			{Key: "namespace", Value: 1},
			{Key: "key", Value: 1},
		},
		Options: options.Index().SetUnique(true),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := coll.Indexes().CreateOne(ctx, indexModel); err != nil {
		return nil, fmt.Errorf("mongodb store: create unique index: %w", err)
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

// List returns all entries from the MongoDB collection.
func (s *Store) List(ctx context.Context) ([]store.Entry, error) {
	if s == nil || s.isClosed() {
		return nil, store.ErrClosed
	}

	ctx, span := s.tracer.Start(ctx, "systemplane.mongodb.list")
	defer span.End()

	cursor, err := s.coll.Find(ctx, bson.D{})
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

// Get returns a single entry by namespace and key.
// Returns (_, false, nil) when the entry does not exist.
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

	filter := bson.D{
		{Key: "namespace", Value: namespace},
		{Key: "key", Value: key},
	}

	var doc entryDoc

	err := s.coll.FindOne(ctx, filter).Decode(&doc)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return store.Entry{}, false, nil
		}

		opentelemetry.HandleSpanError(span, "mongodb get: find failed", err)

		return store.Entry{}, false, fmt.Errorf("mongodb store get: %w", err)
	}

	return doc.toEntry(), true, nil
}

// Set persists an entry using an upsert. The change-stream or polling loop
// picks up the modification and notifies subscribers.
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

	filter := bson.D{
		{Key: "namespace", Value: e.Namespace},
		{Key: "key", Value: e.Key},
	}

	now := time.Now().UTC()
	if !e.UpdatedAt.IsZero() {
		now = e.UpdatedAt
	}

	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "namespace", Value: e.Namespace},
			{Key: "key", Value: e.Key},
			{Key: "value", Value: string(e.Value)},
			{Key: "updated_at", Value: now},
			{Key: "updated_by", Value: e.UpdatedBy},
		}},
	}

	opts := options.UpdateOne().SetUpsert(true)

	_, err := s.coll.UpdateOne(ctx, filter, update, opts)
	if err != nil {
		opentelemetry.HandleSpanError(span, "mongodb set: upsert failed", err)
		return fmt.Errorf("mongodb store set: %w", err)
	}

	return nil
}

// Subscribe blocks until ctx is cancelled, watching for changes via
// change-streams or polling depending on cfg.PollInterval.
//
// Change-stream mode (PollInterval == 0) requires a MongoDB replica set.
// Polling mode (PollInterval > 0) works with standalone MongoDB deployments.
//
// Multiple concurrent Subscribe calls are independent — each gets its own
// stream or ticker.
func (s *Store) Subscribe(ctx context.Context, handler func(store.Event)) error {
	if s == nil || s.isClosed() {
		return store.ErrClosed
	}

	if handler == nil {
		return errors.New("mongodb store subscribe: handler must not be nil")
	}

	if s.cfg.PollInterval > 0 {
		return s.subscribePoll(ctx, handler)
	}

	return s.subscribeChangeStream(ctx, handler)
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

// subscribeChangeStream opens a MongoDB change stream on the collection and
// translates insert/update/replace events into store.Event values.
// On stream error, it reconnects with exponential backoff.
func (s *Store) subscribeChangeStream(ctx context.Context, handler func(store.Event)) error {
	attempt := 0

	for {
		if err := s.watchOnce(ctx, handler); err != nil {
			// Context cancelled — clean exit regardless of the watch error.
			if ctx.Err() != nil {
				return ctx.Err() //nolint:wrapcheck // propagate cancellation as-is
			}

			// Log and reconnect with backoff.
			s.logWarn(ctx, "change stream disconnected, reconnecting",
				log.Err(err),
				log.Int("attempt", attempt),
			)

			delay := min(backoff.ExponentialWithJitter(reconnectBaseDelay, attempt), reconnectMaxDelay)
			attempt++

			if waitErr := backoff.WaitContext(ctx, delay); waitErr != nil {
				return waitErr //nolint:wrapcheck // propagate cancellation as-is
			}

			continue
		}

		return nil
	}
}

// watchOnce opens a single change stream session and processes events until
// error or context cancellation.
func (s *Store) watchOnce(ctx context.Context, handler func(store.Event)) error {
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.D{
			{Key: "operationType", Value: bson.D{
				{Key: "$in", Value: bson.A{"insert", "update", "replace"}},
			}},
		}}},
	}

	opts := options.ChangeStream().
		SetFullDocument(options.UpdateLookup)

	stream, err := s.coll.Watch(ctx, pipeline, opts)
	if err != nil {
		return fmt.Errorf("open change stream: %w", err)
	}
	defer stream.Close(ctx)

	for stream.Next(ctx) {
		var event changeEvent
		if err := stream.Decode(&event); err != nil {
			s.logWarn(ctx, "change stream decode error, skipping event", log.Err(err))
			continue
		}

		evt, ok := extractEvent(event)
		if !ok {
			continue
		}

		s.safeInvokeHandler(ctx, handler, evt)
	}

	if ctx.Err() != nil {
		return nil
	}

	if err := stream.Err(); err != nil {
		return fmt.Errorf("change stream error: %w", err)
	}

	return nil
}

// extractEvent converts a decoded changeEvent into a store.Event.
// Returns false if the event cannot be mapped (missing fields, etc.).
func extractEvent(ce changeEvent) (store.Event, bool) {
	if ce.FullDocument == nil {
		return store.Event{}, false
	}

	ns := bsonLookupString(ce.FullDocument, "namespace")
	key := bsonLookupString(ce.FullDocument, "key")

	if ns == "" || key == "" {
		return store.Event{}, false
	}

	return store.Event{Namespace: ns, Key: key}, true
}

// subscribePoll uses a ticker to periodically query for entries updated since
// the last watermark and emits events for each changed entry.
func (s *Store) subscribePoll(ctx context.Context, handler func(store.Event)) error {
	watermark := time.Now().UTC()
	ticker := time.NewTicker(s.cfg.PollInterval)

	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			newWatermark, err := s.pollChanges(ctx, watermark, handler)
			if err != nil {
				// Transient error — log and retry on next tick.
				s.logWarn(ctx, "poll query failed", log.Err(err))
				continue
			}

			watermark = newWatermark
		}
	}
}

// pollChanges queries for documents updated after the watermark, invokes the
// handler for each, and returns the new watermark.
func (s *Store) pollChanges(
	ctx context.Context,
	watermark time.Time,
	handler func(store.Event),
) (time.Time, error) {
	filter := bson.D{
		{Key: "updated_at", Value: bson.D{{Key: "$gt", Value: watermark}}},
	}

	findOpts := options.Find().SetSort(bson.D{{Key: "updated_at", Value: 1}})

	cursor, err := s.coll.Find(ctx, filter, findOpts)
	if err != nil {
		return watermark, fmt.Errorf("poll find: %w", err)
	}
	defer cursor.Close(ctx)

	newWatermark := watermark

	for cursor.Next(ctx) {
		var doc entryDoc
		if err := cursor.Decode(&doc); err != nil {
			s.logWarn(ctx, "poll decode error, skipping document", log.Err(err))
			continue
		}

		evt := store.Event{Namespace: doc.Namespace, Key: doc.Key}
		s.safeInvokeHandler(ctx, handler, evt)

		if doc.UpdatedAt.After(newWatermark) {
			newWatermark = doc.UpdatedAt
		}
	}

	if err := cursor.Err(); err != nil {
		return watermark, fmt.Errorf("poll cursor error: %w", err)
	}

	return newWatermark, nil
}

// safeInvokeHandler calls handler inside a deferred panic recovery.
func (s *Store) safeInvokeHandler(ctx context.Context, handler func(store.Event), evt store.Event) {
	defer libRuntime.RecoverAndLogWithContext(ctx, s.cfg.Logger, "systemplane", "mongodb.handler")

	handler(evt)
}

// logWarn logs a warning-level message via the configured logger.
func (s *Store) logWarn(ctx context.Context, msg string, fields ...log.Field) {
	if s.cfg.Logger == nil {
		return
	}

	s.cfg.Logger.Log(ctx, log.LevelWarn, msg, fields...)
}

// GetTenantValue is a Task 1 stub. Real implementation lands in Task 4.
func (s *Store) GetTenantValue(_ context.Context, _, _, _ string) (store.Entry, bool, error) {
	return store.Entry{}, false, errors.New("mongodb: GetTenantValue not implemented — task 4")
}

// SetTenantValue is a Task 1 stub. Real implementation lands in Task 4.
func (s *Store) SetTenantValue(_ context.Context, _ string, _ store.Entry) error {
	return errors.New("mongodb: SetTenantValue not implemented — task 4")
}

// DeleteTenantValue is a Task 1 stub. Real implementation lands in Task 4.
func (s *Store) DeleteTenantValue(_ context.Context, _, _, _, _ string) error {
	return errors.New("mongodb: DeleteTenantValue not implemented — task 4")
}

// ListTenantValues is a Task 1 stub. Real implementation lands in Task 4.
func (s *Store) ListTenantValues(_ context.Context) ([]store.Entry, error) {
	return nil, errors.New("mongodb: ListTenantValues not implemented — task 4")
}

// ListTenantsForKey is a Task 1 stub. Real implementation lands in Task 4.
func (s *Store) ListTenantsForKey(_ context.Context, _, _ string) ([]string, error) {
	return nil, errors.New("mongodb: ListTenantsForKey not implemented — task 4")
}

// bsonLookupString extracts a string value from a bson.D by key.
// Returns "" when the key is absent or the value is not a string.
func bsonLookupString(doc *bson.D, key string) string {
	if doc == nil {
		return ""
	}

	for _, elem := range *doc {
		if elem.Key == key {
			s, ok := elem.Value.(string)
			if ok {
				return s
			}

			return ""
		}
	}

	return ""
}
