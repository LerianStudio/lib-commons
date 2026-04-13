// Package mongodb implements the internal store.Store interface over MongoDB
// with change-streams (default) or polling (when a non-zero PollInterval is set).
// Change-streams require a replica set; standalone deployments should use polling.
package mongodb

import (
	"context"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/LerianStudio/lib-commons/v4/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/internal/store"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

// Config holds the parameters needed to construct a MongoDB-backed Store.
type Config struct {
	// Client is the MongoDB client handle.
	Client *mongo.Client

	// Database is the MongoDB database name.
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

// Store implements [store.Store] over MongoDB.
type Store struct {
	cfg Config
}

// New creates a MongoDB-backed Store. Returns an error if the Config is invalid.
func New(cfg Config) (*Store, error) {
	if cfg.Client == nil {
		return nil, store.ErrNilBackend
	}

	if cfg.Collection == "" {
		cfg.Collection = "systemplane_entries"
	}

	return &Store{cfg: cfg}, nil
}

// List returns all entries from the MongoDB collection.
func (s *Store) List(ctx context.Context) ([]store.Entry, error) {
	// TODO(phase-3): db.collection.find({})
	_ = ctx

	return nil, nil
}

// Get returns a single entry by namespace and key.
func (s *Store) Get(ctx context.Context, namespace, key string) (store.Entry, bool, error) {
	// TODO(phase-3): db.collection.findOne({namespace, key})
	_ = ctx
	_ = namespace
	_ = key

	return store.Entry{}, false, nil
}

// Set persists an entry using an upsert. The change-stream or polling loop
// picks up the modification and notifies subscribers.
func (s *Store) Set(ctx context.Context, e store.Entry) error {
	// TODO(phase-3): db.collection.updateOne with upsert
	_ = ctx
	_ = e

	return nil
}

// Subscribe blocks until ctx is cancelled, watching for changes via
// change-streams or polling depending on cfg.PollInterval.
func (s *Store) Subscribe(ctx context.Context, handler func(store.Event)) error {
	// TODO(phase-3): change-stream watch or poll loop
	_ = ctx
	_ = handler

	return nil
}

// Close releases any resources held by the Store. Idempotent.
func (s *Store) Close() error {
	// TODO(phase-3): close change-stream cursor if open
	return nil
}
