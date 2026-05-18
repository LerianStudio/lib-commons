//go:build integration

package postgres

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/LerianStudio/lib-observability/log"
	"github.com/LerianStudio/lib-observability/metrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const postgresContainerStartupTimeout = 2 * time.Minute

func postgresContainerWaitStrategy() testcontainers.CustomizeRequestOption {
	return testcontainers.WithWaitStrategyAndDeadline(
		postgresContainerStartupTimeout,
		wait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).
			WithStartupTimeout(postgresContainerStartupTimeout),
		wait.ForListeningPort("5432/tcp").
			WithStartupTimeout(postgresContainerStartupTimeout).
			WithPollInterval(500*time.Millisecond),
	)
}

// setupPostgresContainer starts a disposable PostgreSQL container and returns
// the connection string plus a teardown function. The container is terminated
// when the returned cleanup function is invoked (typically via t.Cleanup).
func setupPostgresContainer(t *testing.T) (string, func()) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), postgresContainerStartupTimeout)
	defer cancel()

	container, err := tcpostgres.Run(ctx,
		"postgres:16-alpine",
		tcpostgres.WithDatabase("testdb"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		postgresContainerWaitStrategy(),
	)
	require.NoError(t, err)

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	return connStr, func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer closeCancel()

		require.NoError(t, container.Terminate(closeCtx))
	}
}

// newTestConfig builds a Config pointing both primary and replica at the same
// container DSN. This is intentional for integration tests — we are validating
// the connector lifecycle, not read/write splitting.
func newTestConfig(dsn string) Config {
	return Config{
		PrimaryDSN:     dsn,
		ReplicaDSN:     dsn,
		Logger:         log.NewNop(),
		MetricsFactory: metrics.NewNopFactory(),
	}
}

// ---------------------------------------------------------------------------
// TestIntegration_Postgres_ConnectAndResolve
// ---------------------------------------------------------------------------

func TestIntegration_Postgres_ConnectAndResolve(t *testing.T) {
	dsn, cleanup := setupPostgresContainer(t)
	t.Cleanup(cleanup)

	ctx := context.Background()

	client, err := New(newTestConfig(dsn))
	require.NoError(t, err, "New() should succeed with valid DSN")

	err = client.Connect(ctx)
	require.NoError(t, err, "Connect() should succeed against running container")

	resolver, err := client.Resolver(ctx)
	require.NoError(t, err, "Resolver() should return a live resolver after Connect()")
	require.NotNil(t, resolver, "resolver must not be nil")

	// Verify the resolver is actually connected to a live database.
	err = resolver.PingContext(ctx)
	assert.NoError(t, err, "PingContext on resolver should succeed")

	err = client.Close()
	assert.NoError(t, err, "Close() should release resources cleanly")
}

// ---------------------------------------------------------------------------
// TestIntegration_Postgres_PrimaryAccess
// ---------------------------------------------------------------------------

func TestIntegration_Postgres_PrimaryAccess(t *testing.T) {
	dsn, cleanup := setupPostgresContainer(t)
	t.Cleanup(cleanup)

	ctx := context.Background()

	client, err := New(newTestConfig(dsn))
	require.NoError(t, err)

	err = client.Connect(ctx)
	require.NoError(t, err, "Connect() should succeed")

	db, err := client.Primary()
	require.NoError(t, err, "Primary() should return the underlying *sql.DB")
	require.NotNil(t, db, "primary *sql.DB must not be nil")

	// Verify the raw *sql.DB is usable.
	err = db.PingContext(ctx)
	assert.NoError(t, err, "PingContext on primary *sql.DB should succeed")

	// Verify we can execute a trivial query to confirm connectivity beyond Ping.
	var result int
	err = db.QueryRowContext(ctx, "SELECT 1").Scan(&result)
	require.NoError(t, err, "trivial query should succeed")
	assert.Equal(t, 1, result, "SELECT 1 should return 1")

	err = client.Close()
	assert.NoError(t, err)
}

// ---------------------------------------------------------------------------
// TestIntegration_Postgres_IsConnected
// ---------------------------------------------------------------------------

func TestIntegration_Postgres_IsConnected(t *testing.T) {
	dsn, cleanup := setupPostgresContainer(t)
	t.Cleanup(cleanup)

	ctx := context.Background()

	client, err := New(newTestConfig(dsn))
	require.NoError(t, err)

	// Before Connect(), IsConnected must be false.
	connected, err := client.IsConnected()
	require.NoError(t, err)
	assert.False(t, connected, "IsConnected() should be false before Connect()")

	err = client.Connect(ctx)
	require.NoError(t, err)

	// After Connect(), IsConnected must be true.
	connected, err = client.IsConnected()
	require.NoError(t, err)
	assert.True(t, connected, "IsConnected() should be true after Connect()")

	err = client.Close()
	require.NoError(t, err)

	// After Close(), IsConnected must be false again.
	connected, err = client.IsConnected()
	require.NoError(t, err)
	assert.False(t, connected, "IsConnected() should be false after Close()")
}

// ---------------------------------------------------------------------------
// TestIntegration_Postgres_LazyConnect
// ---------------------------------------------------------------------------

func TestIntegration_Postgres_LazyConnect(t *testing.T) {
	dsn, cleanup := setupPostgresContainer(t)
	t.Cleanup(cleanup)

	ctx := context.Background()

	client, err := New(newTestConfig(dsn))
	require.NoError(t, err)

	// Do NOT call Connect() — Resolver() must lazy-connect on first access.
	connected, err := client.IsConnected()
	require.NoError(t, err)
	assert.False(t, connected, "should not be connected before Resolver() call")

	resolver, err := client.Resolver(ctx)
	require.NoError(t, err, "Resolver() should lazy-connect successfully")
	require.NotNil(t, resolver)

	// After lazy connect, IsConnected must flip to true.
	connected, err = client.IsConnected()
	require.NoError(t, err)
	assert.True(t, connected, "IsConnected() should be true after lazy connect via Resolver()")

	// Verify the resolver is functional.
	err = resolver.PingContext(ctx)
	assert.NoError(t, err, "PingContext should succeed on lazily-connected resolver")

	err = client.Close()
	assert.NoError(t, err)
}

// ---------------------------------------------------------------------------
// TestIntegration_Postgres_Migration
// ---------------------------------------------------------------------------

func TestIntegration_Postgres_Migration(t *testing.T) {
	dsn, cleanup := setupPostgresContainer(t)
	t.Cleanup(cleanup)

	ctx := context.Background()

	// Create a temporary directory with migration files.
	migDir := t.TempDir()

	upSQL := "CREATE TABLE IF NOT EXISTS test_items (id SERIAL PRIMARY KEY, name TEXT NOT NULL);"
	downSQL := "DROP TABLE IF EXISTS test_items;"

	err := os.WriteFile(filepath.Join(migDir, "000001_create_test_table.up.sql"), []byte(upSQL), 0o644)
	require.NoError(t, err, "failed to write up migration file")

	err = os.WriteFile(filepath.Join(migDir, "000001_create_test_table.down.sql"), []byte(downSQL), 0o644)
	require.NoError(t, err, "failed to write down migration file")

	// Run the migrator.
	migrator, err := NewMigrator(MigrationConfig{
		PrimaryDSN:     dsn,
		DatabaseName:   "testdb",
		MigrationsPath: migDir,
		Component:      "integration_test",
		Logger:         log.NewNop(),
	})
	require.NoError(t, err, "NewMigrator() should succeed")

	err = migrator.Up(ctx)
	require.NoError(t, err, "Migrator.Up() should apply the migration successfully")

	// Verify the table exists by querying it through a fresh client.
	client, err := New(newTestConfig(dsn))
	require.NoError(t, err)

	err = client.Connect(ctx)
	require.NoError(t, err)

	db, err := client.Primary()
	require.NoError(t, err)

	// Insert a row to confirm the table schema is correct.
	_, err = db.ExecContext(ctx, "INSERT INTO test_items (name) VALUES ($1)", "integration_test_item")
	require.NoError(t, err, "INSERT into migrated table should succeed")

	// Read it back.
	var name string
	err = db.QueryRowContext(ctx, "SELECT name FROM test_items WHERE name = $1", "integration_test_item").Scan(&name)
	require.NoError(t, err, "SELECT from migrated table should succeed")
	assert.Equal(t, "integration_test_item", name, "should read back the inserted value")

	// Verify the table has exactly one row.
	var count int
	err = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM test_items").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count, "migrated table should contain exactly one row")

	err = client.Close()
	assert.NoError(t, err)
}
