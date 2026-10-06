//go:build integration

package postgres

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/errgroup"
	"github.com/LerianStudio/lib-commons/v7/commons/obs"
	"github.com/golang-migrate/migrate/v4/database"

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
		Logger:               obs.Nop(),
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
		Logger:               obs.Nop(),
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

	// Status reports the same dirty state a standalone migration job reads.
	status, err := migrator2.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, MigrationStatus{Version: 1, Dirty: true, Applied: true}, status)
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
		Logger:         obs.Nop(),
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
		Logger:               obs.Nop(),
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
// Standalone migration binary: embed.FS source, Status, bounded lock wait,
// context stop between migrations.
// ---------------------------------------------------------------------------

//go:embed testdata/embedded_migrations
var embeddedMigrationsFS embed.FS

func TestIntegration_Migration_EmbedFSAndStatus(t *testing.T) {
	dsn, cleanup := setupPostgresContainer(t)
	t.Cleanup(cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	migrator, err := NewMigrator(MigrationConfig{
		PrimaryDSN:     dsn,
		DatabaseName:   "testdb",
		MigrationsFS:   embeddedMigrationsFS,
		MigrationsPath: "testdata/embedded_migrations",
		Logger:         obs.Nop(),
	})
	require.NoError(t, err)

	status, err := migrator.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, MigrationStatus{}, status, "a database with no migration recorded is not Applied")

	require.NoError(t, migrator.Up(ctx))

	status, err = migrator.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, MigrationStatus{Version: 2, Applied: true}, status)

	require.NoError(t, migrator.Up(ctx), "a second Up has nothing to do")
}

// TestIntegration_Migration_LockWaitIsBounded: while another session holds the
// migration lock, Up waits at most LockTimeout (ErrMigrationLockTimeout) and
// never past the caller's deadline (context.DeadlineExceeded), where
// golang-migrate alone would wait forever.
func TestIntegration_Migration_LockWaitIsBounded(t *testing.T) {
	dsn, cleanup := setupPostgresContainer(t)
	t.Cleanup(cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	holder, err := sql.Open("pgx", dsn)
	require.NoError(t, err)

	t.Cleanup(func() { _ = holder.Close() })

	conn, err := holder.Conn(ctx)
	require.NoError(t, err)

	t.Cleanup(func() { _ = conn.Close() })

	lockID, err := database.GenerateAdvisoryLockId("testdb", "public", "schema_migrations")
	require.NoError(t, err)

	_, err = conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", lockID)
	require.NoError(t, err)

	migDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(migDir, "000001_t.up.sql"), []byte("CREATE TABLE t (id int);"), 0o644))

	newMigrator := func(lockTimeout time.Duration) *Migrator {
		m, err := NewMigrator(MigrationConfig{
			PrimaryDSN:     dsn,
			DatabaseName:   "testdb",
			MigrationsPath: migDir,
			LockTimeout:    lockTimeout,
			Logger:         obs.Nop(),
		})
		require.NoError(t, err)

		return m
	}

	started := time.Now()
	err = newMigrator(500 * time.Millisecond).Up(ctx)
	require.ErrorIs(t, err, ErrMigrationLockTimeout)
	assert.Less(t, time.Since(started), 10*time.Second)

	_, err = newMigrator(500 * time.Millisecond).Status(ctx)
	require.ErrorIs(t, err, ErrMigrationLockTimeout)

	deadlineCtx, deadlineCancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer deadlineCancel()

	started = time.Now()
	err = newMigrator(0).Up(deadlineCtx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Less(t, time.Since(started), 10*time.Second, "the default 15s lock wait is capped by the deadline")

	_, err = conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", lockID)
	require.NoError(t, err)

	require.NoError(t, newMigrator(0).Up(ctx), "once the lock is free, Up proceeds")
}

// TestIntegration_Migration_ContextStopsBetweenMigrations: a deadline that
// passes during migration 1 lets it finish, stops before migration 2, and
// leaves the database clean at version 1.
func TestIntegration_Migration_ContextStopsBetweenMigrations(t *testing.T) {
	dsn, cleanup := setupPostgresContainer(t)
	t.Cleanup(cleanup)

	migDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(migDir, "000001_slow.up.sql"), []byte("SELECT pg_sleep(1.5);"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(migDir, "000002_next.up.sql"), []byte("CREATE TABLE next_step (id int);"), 0o644))

	migrator, err := NewMigrator(MigrationConfig{
		PrimaryDSN:     dsn,
		DatabaseName:   "testdb",
		MigrationsPath: migDir,
		Logger:         obs.Nop(),
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	err = migrator.Up(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Contains(t, err.Error(), "stopped at version 1")

	status, err := migrator.Status(context.Background())
	require.NoError(t, err)
	assert.Equal(t, MigrationStatus{Version: 1, Applied: true}, status, "stopped clean, never dirty")

	require.NoError(t, migrator.Up(context.Background()), "the next run resumes")

	status, err = migrator.Status(context.Background())
	require.NoError(t, err)
	assert.Equal(t, MigrationStatus{Version: 2, Applied: true}, status)
}

// ---------------------------------------------------------------------------
// One version table per module: several modules (or scopes) of one service
// migrate the same database, each with its own version and its own lock.
// ---------------------------------------------------------------------------

// moduleMigrations writes one module's migrations: version N creates table
// <prefix>_<N>.
func moduleMigrations(t *testing.T, prefix string, versions int) string {
	t.Helper()

	dir := t.TempDir()

	for v := 1; v <= versions; v++ {
		name := fmt.Sprintf("%06d_%s_%d.up.sql", v, prefix, v)
		body := fmt.Sprintf("CREATE TABLE %s_%d (id int);", prefix, v)
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644))
	}

	return dir
}

func moduleMigrator(t *testing.T, dsn, dir, table, schema string, lockTimeout time.Duration) *Migrator {
	t.Helper()

	m, err := NewMigrator(MigrationConfig{
		PrimaryDSN:       dsn,
		DatabaseName:     "testdb",
		MigrationsPath:   dir,
		MigrationsTable:  table,
		MigrationsSchema: schema,
		LockTimeout:      lockTimeout,
		Logger:           obs.Nop(),
	})
	require.NoError(t, err)

	return m
}

func TestIntegration_Migration_ModulesKeepSeparateVersionTables(t *testing.T) {
	dsn, cleanup := setupPostgresContainer(t)
	t.Cleanup(cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	moduleA := moduleMigrator(t, dsn, moduleMigrations(t, "mod_a", 3), "schema_migrations_a", "", 0)
	moduleB := moduleMigrator(t, dsn, moduleMigrations(t, "mod_b", 1), "schema_migrations_b", "", 0)

	require.NoError(t, moduleA.Up(ctx))
	require.NoError(t, moduleB.Up(ctx), "module B's version 1 is not 'behind' module A's version 3")
	require.NoError(t, moduleA.Up(ctx), "each module still has nothing to do on a second run")
	require.NoError(t, moduleB.Up(ctx))

	statusA, err := moduleA.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, MigrationStatus{Version: 3, Applied: true}, statusA)

	statusB, err := moduleB.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, MigrationStatus{Version: 1, Applied: true}, statusB)

	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)

	t.Cleanup(func() { _ = db.Close() })

	for _, table := range []string{"schema_migrations_a", "schema_migrations_b", "mod_a_3", "mod_b_1"} {
		assertTableExists(t, ctx, db, table)
	}

	assertTableAbsent(t, ctx, db, "public", "schema_migrations")
}

// TestIntegration_Migration_SharedDefaultTableReportsAhead is the control: two
// modules on the default table collide, the failure the per-module table
// prevents.
func TestIntegration_Migration_SharedDefaultTableReportsAhead(t *testing.T) {
	dsn, cleanup := setupPostgresContainer(t)
	t.Cleanup(cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	require.NoError(t, moduleMigrator(t, dsn, moduleMigrations(t, "mod_a", 3), "", "", 0).Up(ctx))

	err := moduleMigrator(t, dsn, moduleMigrations(t, "mod_b", 1), "", "", 0).Up(ctx)
	require.ErrorIs(t, err, ErrMigrationVersionAhead)
	assert.Contains(t, err.Error(), "public.schema_migrations")
}

// TestIntegration_Migration_ModulesLockSeparately: each version table has its
// own advisory lock, so one module's lock never blocks another, and the
// modules migrate at the same time.
func TestIntegration_Migration_ModulesLockSeparately(t *testing.T) {
	dsn, cleanup := setupPostgresContainer(t)
	t.Cleanup(cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	dirA := moduleMigrations(t, "mod_a", 3)
	dirB := moduleMigrations(t, "mod_b", 2)

	holder, err := sql.Open("pgx", dsn)
	require.NoError(t, err)

	t.Cleanup(func() { _ = holder.Close() })

	conn, err := holder.Conn(ctx)
	require.NoError(t, err)

	t.Cleanup(func() { _ = conn.Close() })

	lockA, err := database.GenerateAdvisoryLockId("testdb", "public", "schema_migrations_a")
	require.NoError(t, err)

	_, err = conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", lockA)
	require.NoError(t, err)

	err = moduleMigrator(t, dsn, dirA, "schema_migrations_a", "", 500*time.Millisecond).Up(ctx)
	require.ErrorIs(t, err, ErrMigrationLockTimeout, "module A's lock is held")

	require.NoError(t, moduleMigrator(t, dsn, dirB, "schema_migrations_b", "", 500*time.Millisecond).Up(ctx),
		"module B never waits on module A's lock")

	_, err = conn.ExecContext(ctx, "SELECT pg_advisory_unlock($1)", lockA)
	require.NoError(t, err)

	moduleA := moduleMigrator(t, dsn, dirA, "schema_migrations_a", "", 0)
	moduleB := moduleMigrator(t, dsn, dirB, "schema_migrations_b", "", 0)

	group, groupCtx := errgroup.WithContext(ctx)
	group.Go(func() error { return moduleA.Up(groupCtx) })
	group.Go(func() error { return moduleB.Up(groupCtx) })
	require.NoError(t, group.Wait())

	statusA, err := moduleA.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, MigrationStatus{Version: 3, Applied: true}, statusA)
}

// TestIntegration_Migration_VersionTableInOwnSchema covers scopes (correios'
// multi-tenant scopes, say): each scope keeps its version table in its own
// schema, created on first use, while the migration SQL still runs in the
// connection's search_path.
func TestIntegration_Migration_VersionTableInOwnSchema(t *testing.T) {
	dsn, cleanup := setupPostgresContainer(t)
	t.Cleanup(cleanup)

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	scopeA := moduleMigrator(t, dsn, moduleMigrations(t, "scope_a", 2), "", "scope_a", 0)
	scopeB := moduleMigrator(t, dsn, moduleMigrations(t, "scope_b", 1), "", "scope_b", 0)

	status, err := scopeB.Status(ctx)
	require.NoError(t, err, "Status creates the missing schema like Up")
	assert.Equal(t, MigrationStatus{}, status)

	require.NoError(t, scopeA.Up(ctx))
	require.NoError(t, scopeB.Up(ctx))

	statusA, err := scopeA.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, MigrationStatus{Version: 2, Applied: true}, statusA)

	statusB, err := scopeB.Status(ctx)
	require.NoError(t, err)
	assert.Equal(t, MigrationStatus{Version: 1, Applied: true}, statusB)

	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)

	t.Cleanup(func() { _ = db.Close() })

	assertTableIn(t, ctx, db, "scope_a", "schema_migrations")
	assertTableIn(t, ctx, db, "scope_b", "schema_migrations")
	assertTableAbsent(t, ctx, db, "public", "schema_migrations")
	assertTableExists(t, ctx, db, "scope_b_1")
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

// assertTableIn verifies that schema.table exists.
func assertTableIn(t *testing.T, ctx context.Context, db *sql.DB, schema, table string) {
	t.Helper()

	assert.True(t, tableExists(t, ctx, db, schema, table), "table %s.%s should exist", schema, table)
}

// assertTableAbsent verifies that schema.table does not exist.
func assertTableAbsent(t *testing.T, ctx context.Context, db *sql.DB, schema, table string) {
	t.Helper()

	assert.False(t, tableExists(t, ctx, db, schema, table), "table %s.%s should not exist", schema, table)
}

func tableExists(t *testing.T, ctx context.Context, db *sql.DB, schema, table string) bool {
	t.Helper()

	var exists bool

	err := db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = $1 AND table_name = $2)`,
		schema, table,
	).Scan(&exists)
	require.NoError(t, err)

	return exists
}
