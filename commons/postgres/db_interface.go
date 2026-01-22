package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"time"

	"github.com/bxcodec/dbresolver/v2"
)

// Begin starts a transaction on the primary database.
// This method allows PostgresConnection to implement the dbresolver.DB interface.
func (pc *PostgresConnection) Begin() (dbresolver.Tx, error) {
	if pc.ConnectionDB == nil {
		if err := pc.Connect(); err != nil {
			return nil, err
		}
	}
	return (*pc.ConnectionDB).Begin()
}

// BeginTx starts a transaction with the given context and options on the primary database.
func (pc *PostgresConnection) BeginTx(ctx context.Context, opts *sql.TxOptions) (dbresolver.Tx, error) {
	if pc.ConnectionDB == nil {
		if err := pc.Connect(); err != nil {
			return nil, err
		}
	}
	return (*pc.ConnectionDB).BeginTx(ctx, opts)
}

// Exec executes a query without returning any rows on the primary database.
func (pc *PostgresConnection) Exec(query string, args ...any) (sql.Result, error) {
	if pc.ConnectionDB == nil {
		if err := pc.Connect(); err != nil {
			return nil, err
		}
	}
	return (*pc.ConnectionDB).Exec(query, args...)
}

// ExecContext executes a query with context without returning any rows.
func (pc *PostgresConnection) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if pc.ConnectionDB == nil {
		if err := pc.Connect(); err != nil {
			return nil, err
		}
	}
	return (*pc.ConnectionDB).ExecContext(ctx, query, args...)
}

// Query executes a query that returns rows on the replica database (read operation).
func (pc *PostgresConnection) Query(query string, args ...any) (*sql.Rows, error) {
	if pc.ConnectionDB == nil {
		if err := pc.Connect(); err != nil {
			return nil, err
		}
	}
	return (*pc.ConnectionDB).Query(query, args...)
}

// QueryContext executes a query with context that returns rows.
func (pc *PostgresConnection) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if pc.ConnectionDB == nil {
		if err := pc.Connect(); err != nil {
			return nil, err
		}
	}
	return (*pc.ConnectionDB).QueryContext(ctx, query, args...)
}

// QueryRow executes a query that returns at most one row.
func (pc *PostgresConnection) QueryRow(query string, args ...any) *sql.Row {
	if pc.ConnectionDB == nil {
		if err := pc.Connect(); err != nil {
			pc.Logger.Errorf("failed to connect: %v", err)
			return nil
		}
	}
	return (*pc.ConnectionDB).QueryRow(query, args...)
}

// QueryRowContext executes a query with context that returns at most one row.
func (pc *PostgresConnection) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if pc.ConnectionDB == nil {
		if err := pc.Connect(); err != nil {
			pc.Logger.Errorf("failed to connect: %v", err)
			return nil
		}
	}
	return (*pc.ConnectionDB).QueryRowContext(ctx, query, args...)
}

// Ping verifies a connection to the database is still alive.
func (pc *PostgresConnection) Ping() error {
	if pc.ConnectionDB == nil {
		if err := pc.Connect(); err != nil {
			return err
		}
	}
	return (*pc.ConnectionDB).Ping()
}

// PingContext verifies a connection to the database is still alive with context.
func (pc *PostgresConnection) PingContext(ctx context.Context) error {
	if pc.ConnectionDB == nil {
		if err := pc.Connect(); err != nil {
			return err
		}
	}
	return (*pc.ConnectionDB).PingContext(ctx)
}

// Close closes the database connection.
func (pc *PostgresConnection) Close() error {
	if pc.ConnectionDB == nil {
		return nil
	}
	return (*pc.ConnectionDB).Close()
}

// Prepare creates a prepared statement for later queries or executions.
func (pc *PostgresConnection) Prepare(query string) (dbresolver.Stmt, error) {
	if pc.ConnectionDB == nil {
		if err := pc.Connect(); err != nil {
			return nil, err
		}
	}
	return (*pc.ConnectionDB).Prepare(query)
}

// PrepareContext creates a prepared statement with context.
func (pc *PostgresConnection) PrepareContext(ctx context.Context, query string) (dbresolver.Stmt, error) {
	if pc.ConnectionDB == nil {
		if err := pc.Connect(); err != nil {
			return nil, err
		}
	}
	return (*pc.ConnectionDB).PrepareContext(ctx, query)
}

// SetConnMaxIdleTime sets the maximum amount of time a connection may be idle.
func (pc *PostgresConnection) SetConnMaxIdleTime(d time.Duration) {
	if pc.ConnectionDB != nil {
		(*pc.ConnectionDB).SetConnMaxIdleTime(d)
	}
}

// SetConnMaxLifetime sets the maximum amount of time a connection may be reused.
func (pc *PostgresConnection) SetConnMaxLifetime(d time.Duration) {
	if pc.ConnectionDB != nil {
		(*pc.ConnectionDB).SetConnMaxLifetime(d)
	}
}

// SetMaxIdleConns sets the maximum number of connections in the idle connection pool.
func (pc *PostgresConnection) SetMaxIdleConns(n int) {
	if pc.ConnectionDB != nil {
		(*pc.ConnectionDB).SetMaxIdleConns(n)
	}
}

// SetMaxOpenConns sets the maximum number of open connections to the database.
func (pc *PostgresConnection) SetMaxOpenConns(n int) {
	if pc.ConnectionDB != nil {
		(*pc.ConnectionDB).SetMaxOpenConns(n)
	}
}

// Stats returns database statistics.
func (pc *PostgresConnection) Stats() sql.DBStats {
	if pc.ConnectionDB == nil {
		return sql.DBStats{}
	}
	return (*pc.ConnectionDB).Stats()
}

// Conn returns a single connection by either opening a new connection or returning an existing connection from the connection pool.
func (pc *PostgresConnection) Conn(ctx context.Context) (dbresolver.Conn, error) {
	if pc.ConnectionDB == nil {
		if err := pc.Connect(); err != nil {
			return nil, err
		}
	}
	return (*pc.ConnectionDB).Conn(ctx)
}

// Driver returns the database's underlying driver.
func (pc *PostgresConnection) Driver() driver.Driver {
	if pc.ConnectionDB == nil {
		return nil
	}
	return (*pc.ConnectionDB).Driver()
}

// PrimaryDBs returns the primary database connections.
// This method is required by the dbresolver.DB interface.
func (pc *PostgresConnection) PrimaryDBs() []*sql.DB {
	if pc.ConnectionDB == nil {
		if err := pc.Connect(); err != nil {
			return nil
		}
	}
	return (*pc.ConnectionDB).PrimaryDBs()
}

// ReplicaDBs returns the replica database connections.
// This method is required by the dbresolver.DB interface.
func (pc *PostgresConnection) ReplicaDBs() []*sql.DB {
	if pc.ConnectionDB == nil {
		if err := pc.Connect(); err != nil {
			return nil
		}
	}
	return (*pc.ConnectionDB).ReplicaDBs()
}
