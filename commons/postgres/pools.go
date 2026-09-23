package postgres

import (
	"database/sql"
	"errors"
	"fmt"
)

// ErrNilPool is returned when a constructor is handed a nil *sql.DB where an
// already-open pool is required.
var ErrNilPool = errors.New("postgres pool is nil")

// NewFromPools builds a Client that is ALREADY connected, over pools the caller
// opened itself. It exists for tests and for adapters that own their pools —
// a repository unit test driving a sqlmock handle, or a component that received
// a *sql.DB from elsewhere. Production code keeps New(cfg), which owns its DSNs
// and dials lazily.
//
// It replaces the reflect+unsafe harnesses services grew to write the client's
// unexported resolver/primary/replica fields, which corrupt memory silently the
// day one of those fields is retyped.
//
// POOLS — primary is required; a nil primary returns ErrNilPool. A nil replica
// means "no replica": the resolver is built with an empty replica set, so reads
// fall through to the primary, exactly as the dialing path does for a config
// with no ReplicaDSN. The resolver is built by the same createResolverFn the
// dialing path uses, so load balancing and nil-replica handling cannot drift.
//
// NO DIAL — Resolver(ctx), Primary() and IsConnected() answer from the injected
// pools immediately. cfg.PrimaryDSN is never read and may be empty: cfg is
// neither validated nor TLS-checked here, because there is no DSN to check —
// the caller already made those decisions when it opened the pools. Config
// defaults (logger, pool sizing) are applied so the client behaves like one
// built by New, but the sizing is NOT pushed onto the injected pools; their
// tuning belongs to whoever opened them.
//
// NO POOL TELEMETRY — the db.sql.connection.* gauges are deliberately not
// registered. Registering them goes through sqlobs.Setup, which CLOSES the
// handle it is given and hands back a replacement backed by a fresh pool it
// dials from the DSN. With an injected pool there is no DSN, and closing the
// caller's handle is precisely what must not happen. Query-level telemetry the
// caller already applied to its own handle is untouched.
//
// OWNERSHIP — Close() closes the injected pools, like it closes dialed ones.
// Handing the pools over means handing over the responsibility for closing
// them; after Close the client reports IsConnected false and Primary returns
// ErrNotConnected.
//
// DO NOT CALL Connect(ctx) on the returned client. Connect dials cfg and swaps
// the result in, closing the injected pools — the normal reconnect contract,
// which here throws away exactly what the caller injected.
func NewFromPools(primary, replica *sql.DB, cfg Config) (*Client, error) {
	if primary == nil {
		return nil, fmt.Errorf("postgres new from pools: %w", ErrNilPool)
	}

	cfg = cfg.withDefaults()

	resolver, err := createResolverFn(primary, replica, cfg.Logger)
	if err != nil {
		return nil, fmt.Errorf("postgres new from pools: failed to create resolver: %w", err)
	}

	return &Client{
		cfg:             cfg,
		metricsRecorder: cfg.MetricsRecorder,
		resolver:        resolver,
		primary:         primary,
		replica:         replica,
	}, nil
}
