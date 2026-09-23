package postgres

import (
	"database/sql"
	"fmt"
)

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
// POOLS — primary is required; a nil primary returns ErrNilPool. A nil replica,
// or a replica that IS the primary handle, means "no replica": the resolver is
// built with an empty replica set, so reads fall through to the primary,
// exactly as the dialing path does for a config whose ReplicaDSN is empty or
// equal to the primary's. The resolver is built by the same createResolverFn
// the dialing path uses, so load balancing and nil-replica handling cannot
// drift.
//
// NEVER DIALS — Resolver(ctx), Primary() and IsConnected() answer from the
// injected pools immediately, and the returned client is barred from dialing
// for the rest of its life: cfg is neither validated nor TLS-checked here, so
// cfg.PrimaryDSN is not a target this client may open. Connect(ctx) therefore
// returns ErrInjectedPools and changes nothing — it does not close the injected
// pools, and the client keeps serving them. So does the lazy-connect path, so a
// closed injected client reports the refusal instead of attaching itself to
// whatever the ambient libpq environment names. Config defaults (logger, pool
// sizing) are applied so the client behaves like one built by New for the
// fields it does read, but the sizing is NOT pushed onto the injected pools;
// their tuning belongs to whoever opened them.
//
// NO POOL TELEMETRY — the db.sql.connection.* gauges are deliberately not
// registered. Registering them goes through sqlobs.Setup, which refuses a
// config with no DSN outright and, when there is one, CLOSES the handle it is
// given and hands back a replacement backed by a fresh pool it dials. Closing
// the caller's handle is precisely what must not happen here. Query-level
// telemetry the caller already applied to its own handle is untouched.
//
// OWNERSHIP — Close() closes the injected pools, like it closes dialed ones.
// Handing the pools over means handing over the responsibility for closing
// them; after Close the client reports IsConnected false and Primary returns
// ErrNotConnected. Ownership transfers only on success: when this constructor
// returns an error it closes nothing, and the pools remain the caller's to
// close.
func NewFromPools(primary, replica *sql.DB, cfg Config) (*Client, error) {
	if primary == nil {
		return nil, fmt.Errorf("postgres new from pools: %w", ErrNilPool)
	}

	// Registering one handle as both roles would make the resolver ping and
	// read through a pool it already holds as primary. The dialing path refuses
	// the same shape via Config.hasReplica.
	if replica == primary {
		replica = nil
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
		injected:        true,
	}, nil
}
