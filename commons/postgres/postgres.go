package postgres

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/LerianStudio/lib-commons/commons/log"
	"github.com/bxcodec/dbresolver/v2"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	// Import file migration source driver for golang-migrate to enable file:// URL schemes
	_ "github.com/golang-migrate/migrate/v4/source/file"
	// Import pgx driver for PostgreSQL database/sql compatibility
	_ "github.com/jackc/pgx/v5/stdlib"
)

// PostgresConnection is a hub which deal with postgres connections.
// The type name intentionally matches the package name for clarity in external usage.
//
type PostgresConnection struct {
	ConnectionStringPrimary string
	ConnectionStringReplica string
	PrimaryDBName           string
	ReplicaDBName           string
	ConnectionDB            dbresolver.DB
	Connected               bool
	Component               string
	MigrationsPath          string
	Logger                  log.Logger
	MaxOpenConnections      int
	MaxIdleConnections      int
}

// Connect keeps a singleton connection with postgres.
func (pc *PostgresConnection) Connect() error {
	pc.Logger.Info("Connecting to primary and replica databases...")

	// Primary database connection
	dbPrimary, err := sql.Open("pgx", pc.ConnectionStringPrimary)
	if err != nil {
		pc.Logger.Error("failed to open connection to primary database", zap.Error(err))
		return fmt.Errorf("failed to connect to primary database: %w", err)
	}

	// Test primary connection
	if err := dbPrimary.Ping(); err != nil {
		_ = dbPrimary.Close() // Ignore close error during cleanup

		pc.Logger.Error("failed to ping primary database", zap.Error(err))

		return fmt.Errorf("primary database health check failed: %w", err)
	}

	// Configure primary connection pool
	dbPrimary.SetMaxOpenConns(pc.MaxOpenConnections)
	dbPrimary.SetMaxIdleConns(pc.MaxIdleConnections)
	dbPrimary.SetConnMaxLifetime(time.Minute * 30)

	// Replica database connection
	dbReadOnlyReplica, err := sql.Open("pgx", pc.ConnectionStringReplica)
	if err != nil {
		_ = dbPrimary.Close() // Cleanup primary on replica failure, ignore close error

		pc.Logger.Error("failed to open connection to replica database", zap.Error(err))

		return fmt.Errorf("failed to connect to replica database: %w", err)
	}

	// Test replica connection
	if err := dbReadOnlyReplica.Ping(); err != nil {
		_ = dbPrimary.Close() // Ignore close errors during cleanup
		_ = dbReadOnlyReplica.Close()

		pc.Logger.Error("failed to ping replica database", zap.Error(err))

		return fmt.Errorf("replica database health check failed: %w", err)
	}

	// Configure replica connection pool
	dbReadOnlyReplica.SetMaxOpenConns(pc.MaxOpenConnections)
	dbReadOnlyReplica.SetMaxIdleConns(pc.MaxIdleConnections)
	dbReadOnlyReplica.SetConnMaxLifetime(time.Minute * 30)

	// Create dbresolver with both connections
	connectionDB := dbresolver.New(
		dbresolver.WithPrimaryDBs(dbPrimary),
		dbresolver.WithReplicaDBs(dbReadOnlyReplica),
		dbresolver.WithLoadBalancer(dbresolver.RoundRobinLB))

	// Run migrations
	if err := pc.runMigrations(dbPrimary); err != nil {
		_ = dbPrimary.Close() // Ignore close errors during cleanup
		_ = dbReadOnlyReplica.Close()

		return fmt.Errorf("failed to run migrations: %w", err)
	}

	// Final health check
	if err := connectionDB.Ping(); err != nil {
		_ = dbPrimary.Close() // Ignore close errors during cleanup
		_ = dbReadOnlyReplica.Close()

		pc.Logger.Error("final database health check failed", zap.Error(err))

		return fmt.Errorf("database health check failed: %w", err)
	}

	pc.ConnectionDB = connectionDB
	pc.Connected = true
	pc.Logger.Info("Successfully connected to primary and replica databases ✅")

	return nil
}

// runMigrations handles database migrations with proper error handling
func (pc *PostgresConnection) runMigrations(db *sql.DB) error {
	migrationsPath, err := pc.getMigrationsPath()
	if err != nil {
		return fmt.Errorf("failed to resolve migrations path: %w", err)
	}

	primaryURL, err := url.Parse(filepath.ToSlash(migrationsPath))
	if err != nil {
		pc.Logger.Error("failed to parse migrations path", zap.Error(err))
		return fmt.Errorf("failed to parse migrations path: %w", err)
	}

	primaryURL.Scheme = "file"

	primaryDriver, err := postgres.WithInstance(db, &postgres.Config{
		MultiStatementEnabled: true,
		DatabaseName:          pc.PrimaryDBName,
		SchemaName:            "public",
	})
	if err != nil {
		pc.Logger.Error("failed to create migration driver", zap.Error(err))
		return fmt.Errorf("failed to create migration driver: %w", err)
	}

	m, err := migrate.NewWithDatabaseInstance(primaryURL.String(), pc.PrimaryDBName, primaryDriver)
	if err != nil {
		pc.Logger.Error("failed to create migration instance", zap.Error(err))
		return fmt.Errorf("failed to create migration instance: %w", err)
	}

	if err := m.Up(); err != nil {
		if errors.Is(err, migrate.ErrNoChange) {
			pc.Logger.Info("No new migrations found. Skipping...")
		} else if strings.Contains(err.Error(), "file does not exist") {
			pc.Logger.Warn("No migration files found. Skipping migration step...")
		} else {
			pc.Logger.Error("Migration failed", zap.Error(err))
			return fmt.Errorf("failed to run migrations: %w", err)
		}
	} else {
		pc.Logger.Info("Migrations applied successfully")
	}

	return nil
}

// GetDB returns a pointer to the postgres connection, initializing it if necessary.
func (pc *PostgresConnection) GetDB() (dbresolver.DB, error) {
	if pc.ConnectionDB == nil {
		if err := pc.Connect(); err != nil {
			pc.Logger.Error("failed to establish database connection", zap.Error(err))
			return nil, fmt.Errorf("database connection failed: %w", err)
		}
	}

	return pc.ConnectionDB, nil
}

// getMigrationsPath returns the path to migration files, calculating it if not explicitly provided
func (pc *PostgresConnection) getMigrationsPath() (string, error) {
	if pc.MigrationsPath != "" {
		return pc.MigrationsPath, nil
	}

	calculatedPath, err := filepath.Abs(filepath.Join("components", pc.Component, "migrations"))
	if err != nil {
		pc.Logger.Error("failed to get migration filepath", zap.Error(err))
		return "", fmt.Errorf("failed to calculate migrations path: %w", err)
	}

	return calculatedPath, nil
}
