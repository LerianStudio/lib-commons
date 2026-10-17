package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"

	"github.com/LerianStudio/lib-observability/v4/sqlobs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

// NewWithNotice is New plus onNotice on every primary connection the client dials, never replicas.
// pgx calls it synchronously and concurrently on each connection's read path: keep it fast and
// nonblocking, and never query that connection from it. A nil onNotice behaves as New.
func NewWithNotice(cfg Config, onNotice pgconn.NoticeHandler) (*Client, error) {
	client, err := New(cfg)
	if err != nil {
		return nil, err
	}

	client.onNotice = onNotice

	return client, nil
}

// openPool opens one instrumented pool. A primary with a notice handler is built from its pgx connector,
// because sqlobs.Setup reopens by DSN through the generic driver and would drop the callback.
func (c *Client) openPool(ctx context.Context, dsn string, role sqlobs.PoolRole) (*sql.DB, sqlobs.CleanupFunc, error) {
	if c.onNotice == nil || role != sqlobs.PoolRolePrimary {
		raw, err := dbOpenFn("pgx", dsn)
		if err != nil {
			return nil, nil, err
		}

		db, cleanup := c.instrumentPool(ctx, raw, dsn, role)

		return db, cleanup, nil
	}

	connector := noticeConnector(dsn, c.onNotice)

	// A gauge-registration error still returns the usable connector-backed pool.
	db, cleanup, err := sqlobs.SetupConnector(connector, sqlobs.SystemPostgreSQL, c.poolOptions(dsn, role)...)
	if err != nil {
		c.logAtLevel(ctx, obs.LevelWarn, "postgres auto-instrumentation degraded; continuing", "error", err)
	}

	return db, cleanup, nil
}

// noticeConnector returns a pgx connector that hands server notices to onNotice. Like sql.Open,
// it parses dsn for every physical connection, so certificate, passfile and PG* environment
// changes reach new connections; a failed parse surfaces sanitized when that connection dials.
func noticeConnector(dsn string, onNotice pgconn.NoticeHandler) driver.Connector {
	// BeforeConnect replaces the whole copied config, so the seed config stays empty.
	return stdlib.GetConnector(pgx.ConnConfig{}, stdlib.OptionBeforeConnect(func(_ context.Context, cfg *pgx.ConnConfig) error {
		fresh, err := pgx.ParseConfig(dsn)
		if err != nil {
			return newSanitizedError(err, "failed to parse database dsn")
		}

		fresh.OnNotice = onNotice
		*cfg = *fresh

		return nil
	}))
}
