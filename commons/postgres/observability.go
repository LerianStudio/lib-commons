package postgres

import (
	"context"
	"database/sql"

	"github.com/LerianStudio/lib-observability/v2/log"
	"github.com/LerianStudio/lib-observability/v3/sqlobs"
	"go.opentelemetry.io/otel/attribute"
)

// dbNamespaceAttrKey is the OpenTelemetry semantic-convention attribute for the
// logical database name. It is an allowed label for db.client.operation.duration
// in the lib-observability metrics contract.
const dbNamespaceAttrKey = "db.namespace"

// instrumentPool applies the platform's default SQL telemetry to a freshly
// opened pool: db.client.operation.duration plus the db.sql.connection.* pool
// gauges, both tagged with db.system.name=postgresql and the primary/replica
// pool role.
//
// Centralizing this here is the point of the whole wiring: a service gets the
// same series, with the same names/labels/buckets, by bumping lib-commons —
// nothing to wire per service (lib-observability docs/metrics-contract.md).
//
// Providers are resolved from the process-global OpenTelemetry providers, which
// Telemetry.ApplyGlobals installs at bootstrap. A service that never calls
// ApplyGlobals gets no-op instrumentation, never an error.
//
// Instrumentation is never allowed to break connectivity: on failure the raw
// handle is returned untouched with a no-op cleanup, and the reason is logged
// at warn.
//
// The returned handle REPLACES raw (sqlobs.Setup closes it and backs the new
// handle with its own pool), so the caller must apply pool tuning AFTER this
// returns.
func (c *Client) instrumentPool(
	ctx context.Context,
	raw *sql.DB,
	dsn string,
	role sqlobs.PoolRole,
) (*sql.DB, sqlobs.CleanupFunc) {
	opts := []sqlobs.Option{
		sqlobs.WithDSN(dsn),
		sqlobs.WithPoolRole(role),
	}

	// db.namespace keeps pools of different databases in separate series. Without
	// it they share one label set, and because the pool gauges are asynchronous
	// instruments the last callback of the collection cycle simply wins.
	if c.cfg.DatabaseName != "" {
		opts = append(opts, sqlobs.WithAttributes(
			attribute.String(dbNamespaceAttrKey, c.cfg.DatabaseName)))
	}

	db, cleanup, err := sqlobs.Setup(raw, sqlobs.SystemPostgreSQL, opts...)
	if err != nil {
		c.logAtLevel(ctx, log.LevelWarn,
			"postgres auto-instrumentation degraded; continuing", log.Err(err))
	}

	// Setup contracts a usable handle and a non-nil cleanup even on error, but
	// stay defensive: a nil handle here would take the connection down.
	if db == nil {
		return raw, func() error { return nil }
	}

	return db, cleanup
}

// releaseCleanups unregisters the telemetry registrations bound to a set of
// pools. It MUST run whenever those pools are replaced or closed, otherwise the
// asynchronous gauge callbacks keep observing a dead pool after every reconnect.
//
// Cleanups are idempotent (sqlobs guarantees it), so calling this twice for the
// same set is safe.
func (c *Client) releaseCleanups(ctx context.Context, cleanups []sqlobs.CleanupFunc) {
	for _, cleanup := range cleanups {
		if cleanup == nil {
			continue
		}

		if err := cleanup(); err != nil {
			c.logAtLevel(ctx, log.LevelWarn,
				"failed to unregister postgres pool metrics", log.Err(err))
		}
	}
}
