//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// TestIntegration_Migration_DirtyState
// ---------------------------------------------------------------------------
//
// Validates that golang-migrate's dirty-version mechanism is correctly
// classified by classifyMigrationError into ErrMigrationDirty.
//
// Key insight: golang-migrate's postgres driver runs single-statement migrations
// inside a transaction. If the statement fails, the transaction rolls back and
// the DB is NOT marked dirty. A dirty state only occurs with MultiStatementEnabled
// where the first statement commits but the second fails — leaving the schema
// partially applied.
//
// Scenario:
//  1. Migration 000001 (multi-statement, AllowMultiStatements=true):
//     - Statement 1: CREATE TABLE users (succeeds, commits)
//     - Statement 2: ALTER TABLE nonexistent_table (fails)
//  2. golang-migrate marks schema_migrations as (version=1, dirty=true).
//  3. The returned error MUST wrap ErrMigrationDirty.
//  4. The users table must exist (first statement was committed).

func TestIntegration_Migration_DirtyState(t *testing.T) {
	dsn, cleanup := setupPostgresContainer(t)
	t.Cleanup(cleanup)

	ctx := context.Background()

	migDir := t.TempDir()

	// Migration 1 — multi-statement: first succeeds, second fails.
	// With MultiStatementEnabled, statements execute outside a transaction,
	// so the first CREATE TABLE commits before the second ALTER fails.
	// This leaves the database in a dirty state at version 1.
	multiStatementSQL := `CREATE TABLE users (id SERIAL PRIMARY KEY, email TEXT NOT NULL);
ALTER TABLE nonexistent_table ADD COLUMN foo TEXT;`

	require.NoError(t, os.WriteFile(
		filepath.Join(migDir, "000001_partial_migration.up.sql"),
		[]byte(multiStatementSQL),
		0o644,
	))

	require.NoError(t, os.WriteFile(
		filepath.Join(migDir, "000001_partial_migration.down.sql"),
		[]byte("DROP TABLE IF EXISTS users;"),
		0o644,
	))

	migrator, err := NewMigrator(MigrationConfig{
		PrimaryDSN:           dsn,
		DatabaseName:         "testdb",
		MigrationsPath:       migDir,
		Component:            "dirty_state_test",
		AllowMultiStatements: true,
		Logger:               log.NewNop(),
	})
	require.NoError(t, err, "NewMigrator() should succeed")

	// --- Run migrations — expect failure partway through version 1 ----------

	err = migrator.Up(ctx)
	require.Error(t, err, "first Up() must fail because the second statement is invalid")

	// The first Up() returns the SQL execution error, NOT ErrDirty.
	// golang-migrate sets schema_migrations to (version=1, dirty=true) but
	// returns the raw error from the failed statement.

	// --- Second Up() detects the dirty state left by the first call ----------

	// Create a fresh migrator (same config) to simulate a process restart.
	migrator2, err := NewMigrator(MigrationConfig{
		PrimaryDSN:           dsn,
		DatabaseName:         "testdb",
		MigrationsPath:       migDir,
		Component:            "dirty_state_test",
		AllowMultiStatements: true,
		Logger:               log.NewNop(),
	})
	require.NoError(t, err, "NewMigrator() for second attempt should succeed")

	err = migrator2.Up(ctx)
	require.Error(t, err, "second Up() must fail with dirty state")

	// NOW the error chain must contain ErrMigrationDirty.
	assert.True(t,
		errors.Is(err, ErrMigrationDirty),
		"error should wrap ErrMigrationDirty; got: %v", err,
	)

	// --- Verify side-effects ------------------------------------------------

	client, err := New(newTestConfig(dsn))
	require.NoError(t, err)

	err = client.Connect(ctx)
	require.NoError(t, err)

	t.Cleanup(func() { _ = client.Close() })

	db, err := client.Primary()
	require.NoError(t, err)

	// First statement committed — users table must exist.
	assertTableExists(t, ctx, db, "users")

	// The schema_migrations table must show dirty=true at version 1.
	var version int

	var dirty bool

	err = db.QueryRowContext(ctx,
		"SELECT version, dirty FROM schema_migrations",
	).Scan(&version, &dirty)
	require.NoError(t, err, "schema_migrations should have exactly one row")
	assert.Equal(t, 1, version, "dirty version should be 1")
	assert.True(t, dirty, "dirty flag should be true")
}

// ---------------------------------------------------------------------------
// TestIntegration_Migration_NoChange
// ---------------------------------------------------------------------------
//
// Validates that running Up() twice is idempotent: the second call returns nil
// because classifyMigrationError converts migrate.ErrNoChange to a zero-value
// outcome (err == nil).

func TestIntegration_Migration_NoChange(t *testing.T) {
	dsn, cleanup := setupPostgresContainer(t)
	t.Cleanup(cleanup)

	ctx := context.Background()

	migDir := t.TempDir()

	require.NoError(t, os.WriteFile(
		filepath.Join(migDir, "000001_create_items.up.sql"),
		[]byte("CREATE TABLE items (id SERIAL PRIMARY KEY, name TEXT NOT NULL);"),
		0o644,
	))

	require.NoError(t, os.WriteFile(
		filepath.Join(migDir, "000001_create_items.down.sql"),
		[]byte("DROP TABLE IF EXISTS items;"),
		0o644,
	))

	migrator, err := NewMigrator(MigrationConfig{
		PrimaryDSN:     dsn,
		DatabaseName:   "testdb",
		MigrationsPath: migDir,
		Component:      "no_change_test",
		Logger:         log.NewNop(),
	})
	require.NoError(t, err)

	// First run — applies migration 1.
	err = migrator.Up(ctx)
	require.NoError(t, err, "first Up() should succeed")

	// Second run — no new migrations; ErrNoChange is suppressed to nil.
	err = migrator.Up(ctx)
	assert.NoError(t, err, "second Up() should return nil (ErrNoChange suppressed)")

	// Sanity: table still exists and is usable.
	client, err := New(newTestConfig(dsn))
	require.NoError(t, err)

	err = client.Connect(ctx)
	require.NoError(t, err)

	t.Cleanup(func() { _ = client.Close() })

	db, err := client.Primary()
	require.NoError(t, err)

	assertTableExists(t, ctx, db, "items")
}

// ---------------------------------------------------------------------------
// TestIntegration_Migration_MultiStatement
// ---------------------------------------------------------------------------
//
// Validates that AllowMultiStatements: true enables a single migration file
// containing multiple SQL statements separated by semicolons.

func TestIntegration_Migration_MultiStatement(t *testing.T) {
	dsn, cleanup := setupPostgresContainer(t)
	t.Cleanup(cleanup)

	ctx := context.Background()

	migDir := t.TempDir()

	multiSQL := `CREATE TABLE multi_a (id SERIAL PRIMARY KEY);
CREATE TABLE multi_b (id SERIAL PRIMARY KEY);`

	require.NoError(t, os.WriteFile(
		filepath.Join(migDir, "000001_create_multi_tables.up.sql"),
		[]byte(multiSQL),
		0o644,
	))

	require.NoError(t, os.WriteFile(
		filepath.Join(migDir, "000001_create_multi_tables.down.sql"),
		[]byte("DROP TABLE IF EXISTS multi_b; DROP TABLE IF EXISTS multi_a;"),
		0o644,
	))

	migrator, err := NewMigrator(MigrationConfig{
		PrimaryDSN:           dsn,
		DatabaseName:         "testdb",
		MigrationsPath:       migDir,
		Component:            "multi_stmt_test",
		AllowMultiStatements: true,
		Logger:               log.NewNop(),
	})
	require.NoError(t, err, "NewMigrator() should succeed with AllowMultiStatements")

	err = migrator.Up(ctx)
	require.NoError(t, err, "Up() should succeed with multi-statement migration")

	// Verify both tables were created.
	client, err := New(newTestConfig(dsn))
	require.NoError(t, err)

	err = client.Connect(ctx)
	require.NoError(t, err)

	t.Cleanup(func() { _ = client.Close() })

	db, err := client.Primary()
	require.NoError(t, err)

	assertTableExists(t, ctx, db, "multi_a")
	assertTableExists(t, ctx, db, "multi_b")
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// assertTableExists verifies that a table with the given name exists in the
// public schema of the connected database. It fails the test immediately if
// the table is missing.
func assertTableExists(t *testing.T, ctx context.Context, db *sql.DB, table string) {
	t.Helper()

	var exists bool

	err := db.QueryRowContext(ctx,
		`SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = $1
		)`,
		table,
	).Scan(&exists)
	require.NoError(t, err, fmt.Sprintf("query for table %q existence should succeed", table))
	assert.True(t, exists, fmt.Sprintf("table %q should exist in public schema", table))
}
