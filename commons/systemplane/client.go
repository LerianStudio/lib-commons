// Client lifecycle: construction, start, and close for systemplane.
package systemplane

import (
	"context"
	"database/sql"

	"github.com/jackc/pgx/v5"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/internal/store"
)

// ensure pgx is used (the stub does not call pgx yet, but the Postgres
// constructor accepts a listenDSN for pgx.Connect-based LISTEN/NOTIFY).
var _ = (*pgx.Conn)(nil)

// Client is the runtime-config handle. Read methods are nil-receiver safe,
// returning zero values when the Client is nil or not yet started.
type Client struct {
	cfg     clientConfig
	store   store.Store
	started bool
	closed  bool
}

// NewPostgres creates a Client backed by a Postgres database with LISTEN/NOTIFY
// change-feed.
//
// The db handle is used for reads and writes. The listenDSN is a separate
// connection string used by pgx.Connect to establish a dedicated LISTEN
// connection; this is typically the same DSN that was used to open db, but
// must be provided explicitly because database/sql does not expose its
// underlying DSN. See the existing changefeed adapter in
// commons/systemplane/adapters/changefeed/postgres for precedent.
func NewPostgres(db *sql.DB, listenDSN string, opts ...Option) (*Client, error) {
	if db == nil {
		return nil, ErrClosed
	}

	cfg := defaultClientConfig()
	for _, o := range opts {
		o(&cfg)
	}

	// TODO(phase-2): construct internal/postgres.Store
	_ = listenDSN

	return &Client{cfg: cfg}, nil
}

// NewMongoDB creates a Client backed by a MongoDB database with change-streams
// (or polling when WithPollInterval is set). Change-streams require a replica
// set; standalone deployments should use WithPollInterval.
func NewMongoDB(client *mongo.Client, database string, opts ...Option) (*Client, error) {
	if client == nil {
		return nil, ErrClosed
	}

	cfg := defaultClientConfig()
	for _, o := range opts {
		o(&cfg)
	}

	// TODO(phase-3): construct internal/mongodb.Store
	_ = database

	return &Client{cfg: cfg}, nil
}

// Start hydrates initial values from the backing store and begins listening
// for changes. It is idempotent; calling Start on an already-started Client
// returns [ErrAlreadyStarted].
func (c *Client) Start(ctx context.Context) error {
	if c == nil {
		return ErrClosed
	}

	// TODO(phase-5): implement hydration + subscribe loop
	_ = ctx

	return nil
}

// Close unsubscribes from the changefeed and releases backend resources.
// Idempotent; safe to call on a nil receiver.
func (c *Client) Close() error {
	if c == nil {
		return ErrClosed
	}

	// TODO(phase-5): implement teardown
	return nil
}
