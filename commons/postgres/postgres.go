// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package postgres

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	// File system migration source. We need to import it to be able to use it as source in migrate.NewWithSourceInstance

	"github.com/LerianStudio/lib-commons/v2/commons/log"
	"github.com/bxcodec/dbresolver/v2"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// PostgresConnection is a hub which deal with postgres connections.
type PostgresConnection struct {
	ConnectionStringPrimary string
	ConnectionStringReplica string
	PrimaryDBName           string
	ReplicaDBName           string
	ConnectionDB            *dbresolver.DB
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

	dbPrimary, err := sql.Open("pgx", pc.ConnectionStringPrimary)
	if err != nil {
		pc.Logger.Errorf("failed to connect to primary database: %v", err)
		return fmt.Errorf("failed to connect to primary database: %w", err)
	}

	dbPrimary.SetMaxOpenConns(pc.MaxOpenConnections)
	dbPrimary.SetMaxIdleConns(pc.MaxIdleConnections)
	dbPrimary.SetConnMaxLifetime(time.Minute * 30)
	dbPrimary.SetConnMaxIdleTime(5 * time.Minute)

	dbReadOnlyReplica, err := sql.Open("pgx", pc.ConnectionStringReplica)
	if err != nil {
		pc.Logger.Errorf("failed to connect to replica database: %v", err)
		return fmt.Errorf("failed to connect to replica database: %w", err)
	}

	dbReadOnlyReplica.SetMaxOpenConns(pc.MaxOpenConnections)
	dbReadOnlyReplica.SetMaxIdleConns(pc.MaxIdleConnections)
	dbReadOnlyReplica.SetConnMaxLifetime(time.Minute * 30)
	dbReadOnlyReplica.SetConnMaxIdleTime(5 * time.Minute)

	connectionDB := dbresolver.New(
		dbresolver.WithPrimaryDBs(dbPrimary),
		dbresolver.WithReplicaDBs(dbReadOnlyReplica),
		dbresolver.WithLoadBalancer(dbresolver.RoundRobinLB))

	migrationsPath, err := pc.getMigrationsPath()
	if err != nil {
		return err
	}

	primaryURL, err := url.Parse(filepath.ToSlash(migrationsPath))
	if err != nil {
		pc.Logger.Errorf("failed to parse migrations url: %v", err)
		return fmt.Errorf("failed to parse migrations url: %w", err)
	}

	primaryURL.Scheme = "file"

	primaryDriver, err := postgres.WithInstance(dbPrimary, &postgres.Config{
		MultiStatementEnabled: true,
		DatabaseName:          pc.PrimaryDBName,
		SchemaName:            "public",
	})
	if err != nil {
		pc.Logger.Errorf("failed to create postgres driver instance: %v", err)
		return fmt.Errorf("failed to create postgres driver instance: %w", err)
	}

	m, err := migrate.NewWithDatabaseInstance(primaryURL.String(), pc.PrimaryDBName, primaryDriver)
	if err != nil {
		pc.Logger.Errorf("failed to get migrations: %v", err)
		return fmt.Errorf("failed to create migration instance: %w", err)
	}

	if err := m.Up(); err != nil {
		if errors.Is(err, migrate.ErrNoChange) {
			pc.Logger.Info("No new migrations found. Skipping...")
		} else if strings.Contains(err.Error(), "file does not exist") {
			pc.Logger.Warn("No migration files found. Skipping migration step...")
		} else {
			pc.Logger.Errorf("Migration failed: %v", err)
			return fmt.Errorf("migration failed: %w", err)
		}
	}

	if err := connectionDB.Ping(); err != nil {
		pc.Logger.Errorf("PostgresConnection.Ping failed: %v", err)
		return fmt.Errorf("failed to ping database: %w", err)
	}

	pc.Connected = true
	pc.ConnectionDB = &connectionDB

	pc.Logger.Info("Connected to postgres ✅ \n")

	return nil
}

// GetDB returns a pointer to the postgres connection, initializing it if necessary.
func (pc *PostgresConnection) GetDB() (dbresolver.DB, error) {
	if pc.ConnectionDB == nil {
		if err := pc.Connect(); err != nil {
			pc.Logger.Infof("ERRCONECT %s", err)
			return nil, err
		}
	}

	return *pc.ConnectionDB, nil
}

// getMigrationsPath returns the path to migration files, calculating it if not explicitly provided
func (pc *PostgresConnection) getMigrationsPath() (string, error) {
	if pc.MigrationsPath != "" {
		return pc.MigrationsPath, nil
	}

	calculatedPath, err := filepath.Abs(filepath.Join("components", pc.Component, "migrations"))
	if err != nil {
		pc.Logger.Errorf("failed to get migration filepath: %v", err)

		return "", err
	}

	return calculatedPath, nil
}
