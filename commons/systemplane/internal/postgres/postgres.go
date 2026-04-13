// Package postgres implements the internal store.Store interface over Postgres
// with LISTEN/NOTIFY for change-feed delivery. It uses pgx/v5 for the dedicated
// LISTEN connection and database/sql for reads and writes.
package postgres

import (
	"context"
	"database/sql"

	"github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/LerianStudio/lib-commons/v4/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/internal/store"
)

// Config holds the parameters needed to construct a Postgres-backed Store.
type Config struct {
	// DB is the database/sql handle for reads and writes.
	DB *sql.DB

	// ListenDSN is the connection string used by pgx.Connect to establish a
	// dedicated LISTEN connection. Typically the same DSN used to open DB,
	// but must be provided explicitly because database/sql does not expose
	// its underlying DSN.
	ListenDSN string

	// Channel is the Postgres LISTEN/NOTIFY channel name.
	// Default: "systemplane_changes".
	Channel string

	// Table is the Postgres table name.
	// Default: "systemplane_entries".
	Table string

	// Logger is the structured logger.
	Logger log.Logger

	// Telemetry is the OpenTelemetry provider for spans and metrics.
	Telemetry *opentelemetry.Telemetry
}

// Store implements [store.Store] over Postgres with LISTEN/NOTIFY.
type Store struct {
	cfg Config
}

// New creates a Postgres-backed Store. Returns an error if the Config is invalid.
func New(cfg Config) (*Store, error) {
	if cfg.DB == nil {
		return nil, store.ErrNilBackend
	}

	if cfg.Channel == "" {
		cfg.Channel = "systemplane_changes"
	}

	if cfg.Table == "" {
		cfg.Table = "systemplane_entries"
	}

	return &Store{cfg: cfg}, nil
}

// List returns all entries from the Postgres table.
func (s *Store) List(ctx context.Context) ([]store.Entry, error) {
	// TODO(phase-2): SELECT * FROM <table>
	_ = ctx

	return nil, nil
}

// Get returns a single entry by namespace and key.
func (s *Store) Get(ctx context.Context, namespace, key string) (store.Entry, bool, error) {
	// TODO(phase-2): SELECT ... WHERE namespace = $1 AND key = $2
	_ = ctx
	_ = namespace
	_ = key

	return store.Entry{}, false, nil
}

// Set persists an entry using an INSERT ... ON CONFLICT UPDATE (upsert).
// The backing trigger issues NOTIFY on the configured channel.
func (s *Store) Set(ctx context.Context, e store.Entry) error {
	// TODO(phase-2): UPSERT + NOTIFY via trigger
	_ = ctx
	_ = e

	return nil
}

// Subscribe blocks until ctx is cancelled, listening on the Postgres
// LISTEN/NOTIFY channel and invoking handler for each change event.
// Uses pgx.Connect with the ListenDSN for a dedicated connection.
func (s *Store) Subscribe(ctx context.Context, handler func(store.Event)) error {
	// TODO(phase-2): pgx.Connect + LISTEN loop with backoff reconnect
	_ = ctx
	_ = handler

	return nil
}

// Close releases any resources held by the Store. Idempotent.
func (s *Store) Close() error {
	// TODO(phase-2): close LISTEN connection
	return nil
}
