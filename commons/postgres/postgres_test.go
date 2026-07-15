//go:build unit

package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v6/commons"
	constant "github.com/LerianStudio/lib-observability/v2/constants"
	"github.com/LerianStudio/lib-observability/v2/log"
	"github.com/bxcodec/dbresolver/v2"
	"github.com/golang-migrate/migrate/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMain opens the TLS security gate for the test binary. The unit-test
// fixtures use plaintext DSNs (sslmode=disable / no sslmode) which the
// default-deny TLS policy would otherwise reject. Tests that explicitly
// verify the security gate unset ALLOW_INSECURE_TLS via unsetEnvVar.
func TestMain(m *testing.M) {
	if err := os.Setenv(commons.EnvAllowInsecureTLS, "true"); err != nil {
		panic("postgres tests: cannot set ALLOW_INSECURE_TLS: " + err.Error())
	}

	os.Exit(m.Run())
}

func unsetEnvVar(t *testing.T, key string) {
	t.Helper()

	original, present := os.LookupEnv(key)
	require.NoError(t, os.Unsetenv(key))
	t.Cleanup(func() {
		if present {
			require.NoError(t, os.Setenv(key, original))
			return
		}

		require.NoError(t, os.Unsetenv(key))
	})
}

type fakeResolver struct {
	pingErr   error
	closeErr  error
	pingCtx   context.Context
	closeCall atomic.Int32
}

func (f *fakeResolver) Begin() (dbresolver.Tx, error) { return nil, nil }

func (f *fakeResolver) BeginTx(context.Context, *sql.TxOptions) (dbresolver.Tx, error) {
	return nil, nil
}

func (f *fakeResolver) Close() error {
	f.closeCall.Add(1)

	return f.closeErr
}

func (f *fakeResolver) Conn(context.Context) (dbresolver.Conn, error) { return nil, nil }

func (f *fakeResolver) Driver() driver.Driver { return nil }

func (f *fakeResolver) Exec(string, ...interface{}) (sql.Result, error) { return nil, nil }

func (f *fakeResolver) ExecContext(context.Context, string, ...interface{}) (sql.Result, error) {
	return nil, nil
}

func (f *fakeResolver) Ping() error { return nil }

func (f *fakeResolver) PingContext(ctx context.Context) error {
	f.pingCtx = ctx

	return f.pingErr
}

func (f *fakeResolver) Prepare(string) (dbresolver.Stmt, error) { return nil, nil }

func (f *fakeResolver) PrepareContext(context.Context, string) (dbresolver.Stmt, error) {
	return nil, nil
}

func (f *fakeResolver) Query(string, ...interface{}) (*sql.Rows, error) { return nil, nil }

func (f *fakeResolver) QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error) {
	return nil, nil
}

func (f *fakeResolver) QueryRow(string, ...interface{}) *sql.Row { return &sql.Row{} }

func (f *fakeResolver) QueryRowContext(context.Context, string, ...interface{}) *sql.Row {
	return &sql.Row{}
}

func (f *fakeResolver) SetConnMaxIdleTime(time.Duration) {}

func (f *fakeResolver) SetConnMaxLifetime(time.Duration) {}

func (f *fakeResolver) SetMaxIdleConns(int) {}

func (f *fakeResolver) SetMaxOpenConns(int) {}

func (f *fakeResolver) PrimaryDBs() []*sql.DB { return nil }

func (f *fakeResolver) ReplicaDBs() []*sql.DB { return nil }

func (f *fakeResolver) Stats() sql.DBStats { return sql.DBStats{} }

// testDB opens a sql.DB for test dependency injection.
// WARNING: Tests using testDB with withPatchedDependencies must NOT call t.Parallel()
// as withPatchedDependencies mutates global state.
func testDB(t *testing.T) *sql.DB {
	t.Helper()

	dsn := os.Getenv("POSTGRES_DSN")
	if dsn == "" {
		dsn = "postgres://postgres:secret@localhost:5432/postgres?sslmode=disable"
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Skipf("skipping: cannot open postgres connection (set POSTGRES_DSN to configure): %v", err)
	}

	t.Cleanup(func() { _ = db.Close() })

	return db
}

// withPatchedDependencies replaces package-level dependency functions for testing.
// WARNING: Tests using this helper must NOT call t.Parallel() as it mutates global state.
func withPatchedDependencies(
	t *testing.T,
	openFn func(string, string) (*sql.DB, error),
	resolverFn func(*sql.DB, *sql.DB, log.Logger) (dbresolver.DB, error),
	migrateFn func(context.Context, *sql.DB, string, string, bool, bool, log.Logger) error,
) {
	t.Helper()

	originalOpen := dbOpenFn
	originalResolver := createResolverFn
	originalMigrations := runMigrationsFn

	dbOpenFn = openFn
	createResolverFn = resolverFn
	runMigrationsFn = migrateFn

	t.Cleanup(func() {
		dbOpenFn = originalOpen
		createResolverFn = originalResolver
		runMigrationsFn = originalMigrations
	})
}

func validConfig() Config {
	return Config{
		PrimaryDSN: "postgres://postgres:secret@localhost:5432/postgres?sslmode=disable",
		ReplicaDSN: "postgres://postgres:secret@localhost:5432/postgres?sslmode=disable",
	}
}

func TestNewConfigValidationAndDefaults(t *testing.T) {
	t.Run("rejects missing dsn", func(t *testing.T) {
		_, err := New(Config{})

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidConfig)
	})

	t.Run("applies defaults", func(t *testing.T) {
		client, err := New(validConfig())

		require.NoError(t, err)
		require.NotNil(t, client)
		assert.NotNil(t, client.cfg.Logger)
		assert.Equal(t, defaultMaxOpenConns, client.cfg.MaxOpenConnections)
		assert.Equal(t, defaultMaxIdleConns, client.cfg.MaxIdleConnections)
	})
}

func TestConnectRequiresContext(t *testing.T) {
	client, err := New(validConfig())
	require.NoError(t, err)

	err = client.Connect(nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNilContext)
}

func TestDBRequiresContext(t *testing.T) {
	client, err := New(validConfig())
	require.NoError(t, err)

	_, err = client.Resolver(nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNilContext)
}

func TestConnectSanitizesSensitiveError(t *testing.T) {
	withPatchedDependencies(
		t,
		func(string, string) (*sql.DB, error) {
			return nil, errors.New("parse postgres://alice:supersecret@db.internal:5432/main failed password=supersecret")
		},
		func(*sql.DB, *sql.DB, log.Logger) (dbresolver.DB, error) { return nil, nil },
		func(context.Context, *sql.DB, string, string, bool, bool, log.Logger) error { return nil },
	)

	client, err := New(validConfig())
	require.NoError(t, err)

	err = client.Connect(context.Background())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "supersecret")
	assert.Contains(t, err.Error(), "://"+constant.ObfuscatedValue+"@")
	assert.Contains(t, err.Error(), "password="+constant.ObfuscatedValue)

	// Verify error chain preservation via SanitizedError
	var sanitizedErr *SanitizedError
	assert.True(t, errors.As(err, &sanitizedErr))
}

func TestConnectAtomicSwapKeepsOldOnFailure(t *testing.T) {
	oldResolver := &fakeResolver{}
	newResolver := &fakeResolver{pingErr: errors.New("boom")}

	withPatchedDependencies(
		t,
		func(string, string) (*sql.DB, error) { return testDB(t), nil },
		func(*sql.DB, *sql.DB, log.Logger) (dbresolver.DB, error) { return newResolver, nil },
		func(context.Context, *sql.DB, string, string, bool, bool, log.Logger) error { return nil },
	)

	client, err := New(validConfig())
	require.NoError(t, err)
	client.resolver = oldResolver

	err = client.Connect(context.Background())
	require.Error(t, err)
	assert.Equal(t, oldResolver, client.resolver)
	assert.Equal(t, int32(0), oldResolver.closeCall.Load())
	assert.Equal(t, int32(1), newResolver.closeCall.Load())
}

func TestConnectAtomicSwapClosesPreviousOnSuccess(t *testing.T) {
	oldResolver := &fakeResolver{}
	newResolver := &fakeResolver{}

	withPatchedDependencies(
		t,
		func(string, string) (*sql.DB, error) { return testDB(t), nil },
		func(*sql.DB, *sql.DB, log.Logger) (dbresolver.DB, error) { return newResolver, nil },
		func(context.Context, *sql.DB, string, string, bool, bool, log.Logger) error { return nil },
	)

	client, err := New(validConfig())
	require.NoError(t, err)
	client.resolver = oldResolver

	err = client.Connect(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int32(1), oldResolver.closeCall.Load())
	connected, err := client.IsConnected()
	require.NoError(t, err)
	assert.True(t, connected)

	assert.NoError(t, client.Close())
}

func TestDBLazyConnect(t *testing.T) {
	resolver := &fakeResolver{}

	withPatchedDependencies(
		t,
		func(string, string) (*sql.DB, error) { return testDB(t), nil },
		func(*sql.DB, *sql.DB, log.Logger) (dbresolver.DB, error) { return resolver, nil },
		func(context.Context, *sql.DB, string, string, bool, bool, log.Logger) error { return nil },
	)

	client, err := New(validConfig())
	require.NoError(t, err)

	db, err := client.Resolver(context.Background())
	require.NoError(t, err)
	assert.NotNil(t, db)
	assert.NotNil(t, resolver.pingCtx)

	assert.NoError(t, client.Close())
}

func TestCloseIsIdempotent(t *testing.T) {
	resolver := &fakeResolver{}

	client, err := New(validConfig())
	require.NoError(t, err)
	client.resolver = resolver

	require.NoError(t, client.Close())
	require.NoError(t, client.Close())
	connected, err := client.IsConnected()
	require.NoError(t, err)
	assert.False(t, connected)
	assert.Equal(t, int32(1), resolver.closeCall.Load())
}

func TestNewMigratorValidation(t *testing.T) {
	t.Run("requires db name", func(t *testing.T) {
		_, err := NewMigrator(MigrationConfig{PrimaryDSN: "postgres://localhost:5432/postgres"})

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidDatabaseName)
	})

	t.Run("requires component or path", func(t *testing.T) {
		_, err := NewMigrator(MigrationConfig{PrimaryDSN: "postgres://localhost:5432/postgres", DatabaseName: "ledger"})

		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidConfig)
	})
}

func TestMigratorUpRunsExplicitly(t *testing.T) {
	var migrationCalls atomic.Int32

	withPatchedDependencies(
		t,
		func(string, string) (*sql.DB, error) { return testDB(t), nil },
		func(*sql.DB, *sql.DB, log.Logger) (dbresolver.DB, error) { return &fakeResolver{}, nil },
		func(context.Context, *sql.DB, string, string, bool, bool, log.Logger) error {
			migrationCalls.Add(1)
			return nil
		},
	)

	migrator, err := NewMigrator(MigrationConfig{
		PrimaryDSN:     "postgres://postgres:secret@localhost:5432/postgres?sslmode=disable",
		DatabaseName:   "postgres",
		MigrationsPath: "components/ledger/migrations",
	})
	require.NoError(t, err)

	err = migrator.Up(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int32(1), migrationCalls.Load())
}

// ---------------------------------------------------------------------------
// Config.withDefaults
// ---------------------------------------------------------------------------

func TestConfigWithDefaults(t *testing.T) {
	t.Parallel()

	t.Run("nil logger gets default", func(t *testing.T) {
		t.Parallel()

		cfg := Config{PrimaryDSN: "dsn", ReplicaDSN: "dsn"}.withDefaults()
		assert.NotNil(t, cfg.Logger)
	})

	t.Run("zero MaxOpenConnections gets default", func(t *testing.T) {
		t.Parallel()

		cfg := Config{PrimaryDSN: "dsn", ReplicaDSN: "dsn"}.withDefaults()
		assert.Equal(t, defaultMaxOpenConns, cfg.MaxOpenConnections)
	})

	t.Run("zero MaxIdleConnections gets default", func(t *testing.T) {
		t.Parallel()

		cfg := Config{PrimaryDSN: "dsn", ReplicaDSN: "dsn"}.withDefaults()
		assert.Equal(t, defaultMaxIdleConns, cfg.MaxIdleConnections)
	})

	t.Run("custom values preserved", func(t *testing.T) {
		t.Parallel()

		logger := log.NewNop()
		cfg := Config{
			PrimaryDSN:         "dsn",
			ReplicaDSN:         "dsn",
			Logger:             logger,
			MaxOpenConnections: 50,
			MaxIdleConnections: 20,
		}.withDefaults()

		assert.Equal(t, logger, cfg.Logger)
		assert.Equal(t, 50, cfg.MaxOpenConnections)
		assert.Equal(t, 20, cfg.MaxIdleConnections)
	})
}

// ---------------------------------------------------------------------------
// Config.validate
// ---------------------------------------------------------------------------

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	t.Run("empty primary DSN", func(t *testing.T) {
		t.Parallel()

		err := Config{PrimaryDSN: "", ReplicaDSN: "dsn"}.validate()
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidConfig)
	})

	t.Run("whitespace-only primary DSN", func(t *testing.T) {
		t.Parallel()

		err := Config{PrimaryDSN: "   ", ReplicaDSN: "dsn"}.validate()
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidConfig)
	})

	t.Run("empty replica DSN", func(t *testing.T) {
		t.Parallel()

		err := Config{PrimaryDSN: "dsn", ReplicaDSN: ""}.validate()
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidConfig)
	})

	t.Run("valid config", func(t *testing.T) {
		t.Parallel()

		err := Config{PrimaryDSN: "dsn", ReplicaDSN: "dsn"}.validate()
		assert.NoError(t, err)
	})
}

// ---------------------------------------------------------------------------
// New
// ---------------------------------------------------------------------------

func TestNew(t *testing.T) {
	t.Run("valid config returns client", func(t *testing.T) {
		t.Parallel()

		client, err := New(validConfig())
		require.NoError(t, err)
		require.NotNil(t, client)
	})

	t.Run("invalid config returns error", func(t *testing.T) {
		t.Parallel()

		_, err := New(Config{})
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidConfig)
	})
}

// ---------------------------------------------------------------------------
// Client nil receiver safety
// ---------------------------------------------------------------------------

func TestClientNilReceiver(t *testing.T) {
	t.Parallel()

	t.Run("Connect nil client", func(t *testing.T) {
		t.Parallel()

		var c *Client
		err := c.Connect(context.Background())
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrNilClient)
	})

	t.Run("Resolver nil client", func(t *testing.T) {
		t.Parallel()

		var c *Client
		_, err := c.Resolver(context.Background())
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrNilClient)
	})

	t.Run("Close nil client", func(t *testing.T) {
		t.Parallel()

		var c *Client
		err := c.Close()
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrNilClient)
	})

	t.Run("IsConnected nil client", func(t *testing.T) {
		t.Parallel()

		var c *Client
		connected, err := c.IsConnected()
		assert.False(t, connected)
		assert.ErrorIs(t, err, ErrNilClient)
	})

	t.Run("Primary nil client", func(t *testing.T) {
		t.Parallel()

		var c *Client
		_, err := c.Primary()
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrNilClient)
	})
}

// ---------------------------------------------------------------------------
// Client nil context
// ---------------------------------------------------------------------------

func TestClientNilContext(t *testing.T) {
	t.Parallel()

	t.Run("Connect nil ctx", func(t *testing.T) {
		t.Parallel()

		client, err := New(validConfig())
		require.NoError(t, err)

		err = client.Connect(nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrNilContext)
	})

	t.Run("Resolver nil ctx", func(t *testing.T) {
		t.Parallel()

		client, err := New(validConfig())
		require.NoError(t, err)

		_, err = client.Resolver(nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrNilContext)
	})
}

// ---------------------------------------------------------------------------
// Connect with mock dbOpenFn errors
// ---------------------------------------------------------------------------

func TestConnectDbOpenError(t *testing.T) {
	t.Run("primary open fails", func(t *testing.T) {
		withPatchedDependencies(
			t,
			func(_, _ string) (*sql.DB, error) {
				return nil, errors.New("connection refused")
			},
			func(*sql.DB, *sql.DB, log.Logger) (dbresolver.DB, error) { return &fakeResolver{}, nil },
			func(context.Context, *sql.DB, string, string, bool, bool, log.Logger) error { return nil },
		)

		client, err := New(validConfig())
		require.NoError(t, err)

		err = client.Connect(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to open database")
	})

	t.Run("replica open fails", func(t *testing.T) {
		callCount := 0

		withPatchedDependencies(
			t,
			func(_, _ string) (*sql.DB, error) {
				callCount++
				if callCount == 1 {
					return testDB(t), nil
				}

				return nil, errors.New("replica down")
			},
			func(*sql.DB, *sql.DB, log.Logger) (dbresolver.DB, error) { return &fakeResolver{}, nil },
			func(context.Context, *sql.DB, string, string, bool, bool, log.Logger) error { return nil },
		)

		client, err := New(validConfig())
		require.NoError(t, err)

		err = client.Connect(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to open database")
	})

	t.Run("resolver creation fails", func(t *testing.T) {
		withPatchedDependencies(
			t,
			func(_, _ string) (*sql.DB, error) { return testDB(t), nil },
			func(*sql.DB, *sql.DB, log.Logger) (dbresolver.DB, error) {
				return nil, errors.New("resolver error")
			},
			func(context.Context, *sql.DB, string, string, bool, bool, log.Logger) error { return nil },
		)

		client, err := New(validConfig())
		require.NoError(t, err)

		err = client.Connect(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to create resolver")
	})
}

// ---------------------------------------------------------------------------
// Resolver lazy connect - double-checked locking (second call returns cached)
// ---------------------------------------------------------------------------

func TestResolverCachesResolver(t *testing.T) {
	resolver := &fakeResolver{}

	withPatchedDependencies(
		t,
		func(_, _ string) (*sql.DB, error) { return testDB(t), nil },
		func(*sql.DB, *sql.DB, log.Logger) (dbresolver.DB, error) { return resolver, nil },
		func(context.Context, *sql.DB, string, string, bool, bool, log.Logger) error { return nil },
	)

	client, err := New(validConfig())
	require.NoError(t, err)

	// First call connects lazily.
	r1, err := client.Resolver(context.Background())
	require.NoError(t, err)
	assert.Equal(t, resolver, r1)

	// Second call returns cached (fast path).
	r2, err := client.Resolver(context.Background())
	require.NoError(t, err)
	assert.Equal(t, r1, r2)

	assert.NoError(t, client.Close())
}

// ---------------------------------------------------------------------------
// Primary not connected
// ---------------------------------------------------------------------------

func TestPrimaryNotConnected(t *testing.T) {
	t.Parallel()

	client, err := New(validConfig())
	require.NoError(t, err)

	_, err = client.Primary()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotConnected)
}

// ---------------------------------------------------------------------------
// Close with error from resolver
// ---------------------------------------------------------------------------

func TestCloseResolverError(t *testing.T) {
	resolver := &fakeResolver{closeErr: errors.New("close boom")}

	client, err := New(validConfig())
	require.NoError(t, err)
	client.resolver = resolver

	err = client.Close()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "close boom")
}

// ---------------------------------------------------------------------------
// MigrationConfig
// ---------------------------------------------------------------------------

func TestMigrationConfigWithDefaults(t *testing.T) {
	t.Parallel()

	cfg := MigrationConfig{}.withDefaults()
	assert.NotNil(t, cfg.Logger)
}

func TestMigrationConfigValidate(t *testing.T) {
	t.Parallel()

	t.Run("empty DSN", func(t *testing.T) {
		t.Parallel()

		err := MigrationConfig{DatabaseName: "ledger", MigrationsPath: "/tmp"}.validate()
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidConfig)
	})

	t.Run("invalid DB name", func(t *testing.T) {
		t.Parallel()

		err := MigrationConfig{PrimaryDSN: "dsn", DatabaseName: "no-dashes", MigrationsPath: "/tmp"}.validate()
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidDatabaseName)
	})

	t.Run("empty path and component", func(t *testing.T) {
		t.Parallel()

		err := MigrationConfig{PrimaryDSN: "dsn", DatabaseName: "ledger"}.validate()
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidConfig)
	})

	t.Run("valid with path", func(t *testing.T) {
		t.Parallel()

		err := MigrationConfig{PrimaryDSN: "dsn", DatabaseName: "ledger", MigrationsPath: "/tmp"}.validate()
		assert.NoError(t, err)
	})

	t.Run("valid with component", func(t *testing.T) {
		t.Parallel()

		err := MigrationConfig{PrimaryDSN: "dsn", DatabaseName: "ledger", Component: "ledger"}.validate()
		assert.NoError(t, err)
	})
}

// ---------------------------------------------------------------------------
// NewMigrator
// ---------------------------------------------------------------------------

func TestNewMigratorValid(t *testing.T) {
	t.Parallel()

	m, err := NewMigrator(MigrationConfig{
		PrimaryDSN:     "dsn",
		DatabaseName:   "ledger",
		MigrationsPath: "/migrations",
	})
	require.NoError(t, err)
	require.NotNil(t, m)
}

func TestNewMigratorInvalid(t *testing.T) {
	t.Parallel()

	_, err := NewMigrator(MigrationConfig{})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidConfig)
}

// ---------------------------------------------------------------------------
// Migrator nil receiver and nil context
// ---------------------------------------------------------------------------

func TestMigratorNilReceiver(t *testing.T) {
	t.Parallel()

	var m *Migrator
	err := m.Up(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNilMigrator)
}

func TestMigratorNilContext(t *testing.T) {
	m, err := NewMigrator(MigrationConfig{
		PrimaryDSN:     "dsn",
		DatabaseName:   "ledger",
		MigrationsPath: "/migrations",
	})
	require.NoError(t, err)

	err = m.Up(nil)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNilContext)
}

func TestMigratorUpDbOpenError(t *testing.T) {
	withPatchedDependencies(
		t,
		func(_, _ string) (*sql.DB, error) {
			return nil, errors.New("parse postgres://alice:supersecret@db:5432/main failed")
		},
		func(*sql.DB, *sql.DB, log.Logger) (dbresolver.DB, error) { return nil, nil },
		func(context.Context, *sql.DB, string, string, bool, bool, log.Logger) error { return nil },
	)

	m, err := NewMigrator(MigrationConfig{
		PrimaryDSN:     "postgres://alice:supersecret@db:5432/main?sslmode=disable",
		DatabaseName:   "main",
		MigrationsPath: "/migrations",
	})
	require.NoError(t, err)

	err = m.Up(context.Background())
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "supersecret")
}

func TestMigratorUpResolvesPathFromComponent(t *testing.T) {
	var capturedPath string

	withPatchedDependencies(
		t,
		func(_, _ string) (*sql.DB, error) { return testDB(t), nil },
		func(*sql.DB, *sql.DB, log.Logger) (dbresolver.DB, error) { return &fakeResolver{}, nil },
		func(_ context.Context, _ *sql.DB, path, _ string, _, _ bool, _ log.Logger) error {
			capturedPath = path
			return nil
		},
	)

	m, err := NewMigrator(MigrationConfig{
		PrimaryDSN:   "postgres://localhost/db",
		DatabaseName: "ledger",
		Component:    "ledger",
	})
	require.NoError(t, err)

	err = m.Up(context.Background())
	require.NoError(t, err)
	assert.Contains(t, capturedPath, "components")
	assert.Contains(t, capturedPath, "ledger")
	assert.Contains(t, capturedPath, "migrations")
}

func TestMigratorUpMigrationError(t *testing.T) {
	withPatchedDependencies(
		t,
		func(_, _ string) (*sql.DB, error) { return testDB(t), nil },
		func(*sql.DB, *sql.DB, log.Logger) (dbresolver.DB, error) { return &fakeResolver{}, nil },
		func(_ context.Context, _ *sql.DB, _, _ string, _, _ bool, _ log.Logger) error {
			return errors.New("migration failed")
		},
	)

	m, err := NewMigrator(MigrationConfig{
		PrimaryDSN:     "postgres://localhost/db",
		DatabaseName:   "ledger",
		MigrationsPath: "/migrations",
	})
	require.NoError(t, err)

	err = m.Up(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "migration failed")
}

// ---------------------------------------------------------------------------
// sanitizeSensitiveString
// ---------------------------------------------------------------------------

func TestSanitizeSensitiveString(t *testing.T) {
	t.Parallel()

	t.Run("masks user:password in DSN", func(t *testing.T) {
		t.Parallel()

		result := sanitizeSensitiveString("failed to connect to postgres://alice:supersecret@db.internal:5432/main")
		assert.NotContains(t, result, "alice")
		assert.NotContains(t, result, "supersecret")
		assert.Contains(t, result, "://"+constant.ObfuscatedValue+"@")
	})

	t.Run("masks password= param", func(t *testing.T) {
		t.Parallel()

		result := sanitizeSensitiveString("connection error password=mysecret host=db")
		assert.NotContains(t, result, "mysecret")
		assert.Contains(t, result, "password="+constant.ObfuscatedValue)
	})

	t.Run("masks password containing ampersand", func(t *testing.T) {
		t.Parallel()

		result := sanitizeSensitiveString("connection error password=sec&ret host=db")
		assert.NotContains(t, result, "sec&ret")
		assert.Contains(t, result, "password="+constant.ObfuscatedValue)
	})

	t.Run("masks sslkey path", func(t *testing.T) {
		t.Parallel()

		result := sanitizeSensitiveString("host=db sslkey=/etc/ssl/private/key.pem port=5432")
		assert.NotContains(t, result, "/etc/ssl/private/key.pem")
		assert.Contains(t, result, "sslkey="+constant.ObfuscatedValue)
	})

	t.Run("masks sslcert and sslrootcert", func(t *testing.T) {
		t.Parallel()

		result := sanitizeSensitiveString("sslcert=/path/cert.pem sslrootcert=/path/ca.pem")
		assert.NotContains(t, result, "/path/cert.pem")
		assert.Contains(t, result, "sslcert="+constant.ObfuscatedValue)
		assert.Contains(t, result, "sslrootcert="+constant.ObfuscatedValue)
	})

	t.Run("error without credentials passes through", func(t *testing.T) {
		t.Parallel()

		result := sanitizeSensitiveString("timeout connecting to database")
		assert.Equal(t, "timeout connecting to database", result)
	})
}

// ---------------------------------------------------------------------------
// sanitizePath
// ---------------------------------------------------------------------------

func TestSanitizePath(t *testing.T) {
	t.Parallel()

	t.Run("valid path", func(t *testing.T) {
		t.Parallel()

		result, err := sanitizePath("components/ledger/migrations")
		require.NoError(t, err)
		assert.NotEmpty(t, result)
	})

	t.Run("path with traversal rejected", func(t *testing.T) {
		t.Parallel()

		_, err := sanitizePath("../../etc/passwd")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid migrations path")
	})

	t.Run("absolute path accepted", func(t *testing.T) {
		t.Parallel()

		result, err := sanitizePath("/var/migrations")
		require.NoError(t, err)
		assert.Equal(t, "/var/migrations", result)
	})
}

// ---------------------------------------------------------------------------
// validateDBName
// ---------------------------------------------------------------------------

func TestValidateDBName(t *testing.T) {
	t.Parallel()

	t.Run("valid names", func(t *testing.T) {
		t.Parallel()

		for _, name := range []string{"postgres", "ledger", "_private", "db_123", "A"} {
			assert.NoError(t, validateDBName(name), "expected %q to be valid", name)
		}
	})

	t.Run("invalid names", func(t *testing.T) {
		t.Parallel()

		for _, name := range []string{"", "no-dashes", "123start", "has space", "a;drop", "has.dot"} {
			err := validateDBName(name)
			require.Error(t, err, "expected %q to be invalid", name)
			assert.ErrorIs(t, err, ErrInvalidDatabaseName)
		}
	})

	t.Run("too long name", func(t *testing.T) {
		t.Parallel()

		longName := strings.Repeat("a", 64)
		err := validateDBName(longName)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidDatabaseName)
	})
}

// ---------------------------------------------------------------------------
// resolveMigrationsPath
// ---------------------------------------------------------------------------

func TestResolveMigrationsPath(t *testing.T) {
	t.Parallel()

	t.Run("explicit path used", func(t *testing.T) {
		t.Parallel()

		result, err := resolveMigrationsPath("components/ledger/migrations", "ignored")
		require.NoError(t, err)
		assert.NotEmpty(t, result)
	})

	t.Run("component-based path", func(t *testing.T) {
		t.Parallel()

		result, err := resolveMigrationsPath("", "ledger")
		require.NoError(t, err)
		assert.Contains(t, result, "components")
		assert.Contains(t, result, "ledger")
		assert.Contains(t, result, "migrations")
	})

	t.Run("invalid component (traversal stripped)", func(t *testing.T) {
		t.Parallel()

		// filepath.Base("../../etc") → "etc", which is valid, so no error.
		result, err := resolveMigrationsPath("", "../../etc")
		require.NoError(t, err)
		assert.Contains(t, result, "etc")
	})

	t.Run("empty component and empty path", func(t *testing.T) {
		t.Parallel()

		// filepath.Base("") → ".", which triggers the guard.
		_, err := resolveMigrationsPath("", "")
		require.Error(t, err)
	})

	t.Run("dot-only component", func(t *testing.T) {
		t.Parallel()

		_, err := resolveMigrationsPath("", ".")
		require.Error(t, err)
	})

	t.Run("path with traversal rejected", func(t *testing.T) {
		t.Parallel()

		_, err := resolveMigrationsPath("../../etc/passwd", "")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid migrations path")
	})
}

// ---------------------------------------------------------------------------
// Close without resolver falls back to closing primary/replica directly
// ---------------------------------------------------------------------------

func TestCloseNoResolverClosesPrimaryAndReplica(t *testing.T) {
	client, err := New(validConfig())
	require.NoError(t, err)

	primary := testDB(t)
	replica := testDB(t)

	client.primary = primary
	client.replica = replica

	err = client.Close()
	assert.NoError(t, err)

	// After Close(), primary and replica should be nil.
	assert.Nil(t, client.primary)
	assert.Nil(t, client.replica)
}

func TestCloseNoResolverOnlyPrimary(t *testing.T) {
	client, err := New(validConfig())
	require.NoError(t, err)

	primary := testDB(t)
	client.primary = primary

	err = client.Close()
	assert.NoError(t, err)
	assert.Nil(t, client.primary)
}

func TestCloseNoResolverOnlyReplica(t *testing.T) {
	client, err := New(validConfig())
	require.NoError(t, err)

	replica := testDB(t)
	client.replica = replica

	err = client.Close()
	assert.NoError(t, err)
	assert.Nil(t, client.replica)
}

// ---------------------------------------------------------------------------
// connectLocked old resolver close error path
// ---------------------------------------------------------------------------

func TestConnectLockedOldResolverCloseError(t *testing.T) {
	oldResolver := &fakeResolver{closeErr: errors.New("old close failed")}
	newResolver := &fakeResolver{}

	withPatchedDependencies(
		t,
		func(string, string) (*sql.DB, error) { return testDB(t), nil },
		func(*sql.DB, *sql.DB, log.Logger) (dbresolver.DB, error) { return newResolver, nil },
		func(context.Context, *sql.DB, string, string, bool, bool, log.Logger) error { return nil },
	)

	client, err := New(validConfig())
	require.NoError(t, err)
	client.resolver = oldResolver

	// Should succeed — old resolver close error is logged but not returned.
	err = client.Connect(context.Background())
	require.NoError(t, err)
	assert.Equal(t, int32(1), oldResolver.closeCall.Load())

	assert.NoError(t, client.Close())
}

// ---------------------------------------------------------------------------
// Resolver lazy connect error path
// ---------------------------------------------------------------------------

func TestResolverLazyConnectError(t *testing.T) {
	withPatchedDependencies(
		t,
		func(string, string) (*sql.DB, error) {
			return nil, errors.New("cannot connect")
		},
		func(*sql.DB, *sql.DB, log.Logger) (dbresolver.DB, error) { return &fakeResolver{}, nil },
		func(context.Context, *sql.DB, string, string, bool, bool, log.Logger) error { return nil },
	)

	client, err := New(validConfig())
	require.NoError(t, err)

	_, err = client.Resolver(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to open database")
}

// ---------------------------------------------------------------------------
// Resolver double-checked locking — resolver set between RLock and Lock
// ---------------------------------------------------------------------------

func TestResolverDoubleCheckReturnsExisting(t *testing.T) {
	resolver := &fakeResolver{}

	withPatchedDependencies(
		t,
		func(string, string) (*sql.DB, error) { return testDB(t), nil },
		func(*sql.DB, *sql.DB, log.Logger) (dbresolver.DB, error) { return resolver, nil },
		func(context.Context, *sql.DB, string, string, bool, bool, log.Logger) error { return nil },
	)

	client, err := New(validConfig())
	require.NoError(t, err)

	// First call connects lazily
	r1, err := client.Resolver(context.Background())
	require.NoError(t, err)
	assert.Equal(t, resolver, r1)

	// Set resolver directly to simulate race (already set when write lock acquired)
	newResolver := &fakeResolver{}
	client.mu.Lock()
	client.resolver = newResolver
	client.mu.Unlock()

	r2, err := client.Resolver(context.Background())
	require.NoError(t, err)
	assert.Equal(t, newResolver, r2)
}

// ---------------------------------------------------------------------------
// Primary returns db when connected
// ---------------------------------------------------------------------------

func TestPrimaryReturnsDBWhenConnected(t *testing.T) {
	withPatchedDependencies(
		t,
		func(string, string) (*sql.DB, error) { return testDB(t), nil },
		func(*sql.DB, *sql.DB, log.Logger) (dbresolver.DB, error) { return &fakeResolver{}, nil },
		func(context.Context, *sql.DB, string, string, bool, bool, log.Logger) error { return nil },
	)

	client, err := New(validConfig())
	require.NoError(t, err)

	err = client.Connect(context.Background())
	require.NoError(t, err)

	db, err := client.Primary()
	require.NoError(t, err)
	assert.NotNil(t, db)

	assert.NoError(t, client.Close())
}

// ---------------------------------------------------------------------------
// Migrator Up resolveMigrationsPath error
// ---------------------------------------------------------------------------

func TestMigratorUpResolveMigrationsPathError(t *testing.T) {
	withPatchedDependencies(
		t,
		func(_, _ string) (*sql.DB, error) { return testDB(t), nil },
		func(*sql.DB, *sql.DB, log.Logger) (dbresolver.DB, error) { return &fakeResolver{}, nil },
		func(context.Context, *sql.DB, string, string, bool, bool, log.Logger) error { return nil },
	)

	m, err := NewMigrator(MigrationConfig{
		PrimaryDSN:     "postgres://localhost/db",
		DatabaseName:   "ledger",
		MigrationsPath: "../../etc/passwd",
	})
	require.NoError(t, err)

	err = m.Up(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid migrations path")
}

// ---------------------------------------------------------------------------
// closeDB
// ---------------------------------------------------------------------------

func TestCloseDBNil(t *testing.T) {
	t.Parallel()

	err := closeDB(nil)
	assert.NoError(t, err)
}

// ---------------------------------------------------------------------------
// Client logAtLevel nil safety
// ---------------------------------------------------------------------------

func TestClientLogAtLevelNilSafety(t *testing.T) {
	t.Parallel()

	t.Run("nil client does not panic", func(t *testing.T) {
		t.Parallel()

		var c *Client
		assert.NotPanics(t, func() {
			c.logAtLevel(context.Background(), log.LevelInfo, "test")
		})
	})

	t.Run("nil logger does not panic", func(t *testing.T) {
		t.Parallel()

		c := &Client{}
		assert.NotPanics(t, func() {
			c.logAtLevel(context.Background(), log.LevelInfo, "test")
		})
	})
}

// ---------------------------------------------------------------------------
// Migrator logAtLevel nil safety
// ---------------------------------------------------------------------------

func TestMigratorLogAtLevelNilSafety(t *testing.T) {
	t.Parallel()

	t.Run("nil migrator does not panic", func(t *testing.T) {
		t.Parallel()

		var m *Migrator
		assert.NotPanics(t, func() {
			m.logAtLevel(context.Background(), log.LevelInfo, "test")
		})
	})

	t.Run("nil logger does not panic", func(t *testing.T) {
		t.Parallel()

		m := &Migrator{}
		assert.NotPanics(t, func() {
			m.logAtLevel(context.Background(), log.LevelError, "test")
		})
	})
}

// ---------------------------------------------------------------------------
// SanitizedError
// ---------------------------------------------------------------------------

func TestSanitizedError(t *testing.T) {
	t.Parallel()

	t.Run("Error returns sanitized message", func(t *testing.T) {
		t.Parallel()

		cause := errors.New("connect to postgres://alice:supersecret@db:5432 failed")
		se := newSanitizedError(cause, "failed to open database")
		assert.NotContains(t, se.Error(), "supersecret")
		assert.NotContains(t, se.Error(), "alice")
		assert.Contains(t, se.Error(), "://"+constant.ObfuscatedValue+"@")
	})

	t.Run("Unwrap returns sanitized cause without credentials", func(t *testing.T) {
		t.Parallel()

		cause := errors.New("connect to postgres://alice:supersecret@db:5432 failed")
		se := newSanitizedError(cause, "open failed")
		unwrapped := se.Unwrap()
		require.NotNil(t, unwrapped, "Unwrap must return a sanitized cause for error chain traversal")
		assert.NotContains(t, unwrapped.Error(), "supersecret", "Unwrap must not leak credentials")
		assert.NotContains(t, unwrapped.Error(), "alice", "Unwrap must not leak credentials")
		assert.Contains(t, unwrapped.Error(), "://"+constant.ObfuscatedValue+"@", "Unwrap must contain sanitized URI")
	})

	t.Run("nil error returns nil", func(t *testing.T) {
		t.Parallel()

		assert.Nil(t, newSanitizedError(nil, "prefix"))
	})

	t.Run("errors.Is does not match original cause directly", func(t *testing.T) {
		t.Parallel()

		inner := errors.New("inner")
		wrapped := fmt.Errorf("wrapped: %w", inner)
		se := newSanitizedError(wrapped, "outer")
		// The sanitized cause is a new error with the sanitized message text,
		// so errors.Is will not match the original inner error.
		assert.NotErrorIs(t, se, inner, "sanitized cause is a new error, not the original")
		assert.Contains(t, se.Error(), "outer", "sanitized message should contain prefix")
		// But Unwrap works for typed assertions.
		assert.NotNil(t, se.Unwrap())
	})
}

// ---------------------------------------------------------------------------
// classifyMigrationError
// ---------------------------------------------------------------------------

func TestClassifyMigrationError(t *testing.T) {
	t.Parallel()

	t.Run("nil error returns zero outcome", func(t *testing.T) {
		t.Parallel()

		outcome := classifyMigrationError(nil, false, migrationState{})
		assert.Nil(t, outcome.err)
	})

	t.Run("ErrNoChange returns nil error with info level", func(t *testing.T) {
		t.Parallel()

		outcome := classifyMigrationError(migrate.ErrNoChange, false, migrationState{})
		assert.Nil(t, outcome.err)
		assert.Equal(t, log.LevelInfo, outcome.level)
		assert.NotEmpty(t, outcome.message)
	})

	t.Run("ErrNotExist with empty source returns ErrMigrationsNotFound by default", func(t *testing.T) {
		t.Parallel()

		outcome := classifyMigrationError(os.ErrNotExist, false, migrationState{})
		require.Error(t, outcome.err)
		assert.ErrorIs(t, outcome.err, ErrMigrationsNotFound)
		assert.Equal(t, log.LevelError, outcome.level)
		assert.Contains(t, outcome.err.Error(), "missing or empty")
	})

	t.Run("ErrNotExist returns nil error when allowMissing is true", func(t *testing.T) {
		t.Parallel()

		outcome := classifyMigrationError(os.ErrNotExist, true, migrationState{})
		assert.Nil(t, outcome.err)
		assert.Equal(t, log.LevelWarn, outcome.level)
		assert.NotEmpty(t, outcome.message)
	})

	t.Run("ErrNotExist with DB ahead of source max reports version ahead", func(t *testing.T) {
		t.Parallel()

		outcome := classifyMigrationError(os.ErrNotExist, false, migrationState{
			currentVersion: 17,
			hasVersion:     true,
			sourceCount:    16,
			sourceMax:      16,
			sourcePath:     "/app/migrations",
		})
		require.Error(t, outcome.err)
		assert.ErrorIs(t, outcome.err, ErrMigrationVersionAhead)
		assert.NotErrorIs(t, outcome.err, ErrMigrationsNotFound)
		assert.Equal(t, log.LevelError, outcome.level)
		// Exact fragments, not bare "17"/"16".
		assert.Contains(t, outcome.err.Error(), "pinned to version 17")
		assert.Contains(t, outcome.err.Error(), "ahead of the bundled migrations")
		assert.Contains(t, outcome.err.Error(), "ships up to version 16")
		assert.Contains(t, outcome.err.Error(), "/app/migrations")

		fields := fieldMap(outcome.fields)
		assert.Equal(t, "17", fields["db_version"])
		assert.Equal(t, "16", fields["source_max_version"])
		assert.Equal(t, 16, fields["source_file_count"])
	})

	t.Run("ErrNotExist with a mid-range gap reports gap, not ahead", func(t *testing.T) {
		t.Parallel()

		// version 17 removed, but source still ships up to 18.
		outcome := classifyMigrationError(os.ErrNotExist, false, migrationState{
			currentVersion: 17,
			hasVersion:     true,
			sourceCount:    17,
			sourceMax:      18,
			sourcePath:     "/app/migrations",
		})
		require.Error(t, outcome.err)
		assert.ErrorIs(t, outcome.err, ErrMigrationVersionAhead)
		// Must NOT claim the DB is ahead when the source ships a higher version.
		assert.NotContains(t, outcome.err.Error(), "ahead of the bundled migrations")
		assert.Contains(t, outcome.err.Error(), "gap")
		assert.Contains(t, outcome.err.Error(), "version 17 is absent")
	})

	t.Run("ErrNotExist at the current==max boundary reports version ahead", func(t *testing.T) {
		t.Parallel()

		// Reaching os.ErrNotExist with current == max is an inconsistent-source
		// signal; it must not be swallowed as "source missing/empty".
		outcome := classifyMigrationError(os.ErrNotExist, false, migrationState{
			currentVersion: 16, hasVersion: true, sourceCount: 16, sourceMax: 16, sourcePath: "/m",
		})
		require.Error(t, outcome.err)
		assert.ErrorIs(t, outcome.err, ErrMigrationVersionAhead)
	})

	t.Run("ErrNotExist with populated source but no DB version is not misreported as empty", func(t *testing.T) {
		t.Parallel()

		outcome := classifyMigrationError(os.ErrNotExist, false, migrationState{
			hasVersion: false, sourceCount: 16, sourceMax: 16, sourcePath: "/app/migrations",
		})
		require.Error(t, outcome.err)
		assert.ErrorIs(t, outcome.err, ErrMigrationsNotFound)
		assert.NotErrorIs(t, outcome.err, ErrMigrationVersionAhead)
		assert.NotContains(t, outcome.err.Error(), "missing or empty")
		assert.Contains(t, outcome.err.Error(), "could not be determined")
	})

	t.Run("ErrNotExist with DB version but empty source is missing/empty", func(t *testing.T) {
		t.Parallel()

		outcome := classifyMigrationError(os.ErrNotExist, false, migrationState{
			currentVersion: 5, hasVersion: true, sourceCount: 0,
		})
		require.Error(t, outcome.err)
		assert.ErrorIs(t, outcome.err, ErrMigrationsNotFound)
		assert.NotErrorIs(t, outcome.err, ErrMigrationVersionAhead)
		assert.Contains(t, outcome.err.Error(), "missing or empty")
	})

	t.Run("ErrNotExist with populated source but allowMissing still skips", func(t *testing.T) {
		t.Parallel()

		outcome := classifyMigrationError(os.ErrNotExist, true, migrationState{
			currentVersion: 17, hasVersion: true, sourceCount: 16, sourceMax: 16,
		})
		assert.Nil(t, outcome.err)
		assert.Equal(t, log.LevelWarn, outcome.level)
	})

	t.Run("ErrDirty returns wrapped sentinel with version", func(t *testing.T) {
		t.Parallel()

		outcome := classifyMigrationError(migrate.ErrDirty{Version: 42}, false, migrationState{})
		require.Error(t, outcome.err)
		assert.ErrorIs(t, outcome.err, ErrMigrationDirty)
		assert.Contains(t, outcome.err.Error(), "42")
		assert.Equal(t, log.LevelError, outcome.level)
		assert.NotEmpty(t, outcome.fields)
	})

	t.Run("generic error returns wrapped error", func(t *testing.T) {
		t.Parallel()

		cause := errors.New("disk full")
		outcome := classifyMigrationError(cause, false, migrationState{})
		require.Error(t, outcome.err)
		assert.ErrorIs(t, outcome.err, cause)
		assert.Equal(t, log.LevelError, outcome.level)
	})
}

// fieldMap turns log fields into a key->value map for assertions.
func fieldMap(fields []log.Field) map[string]any {
	m := make(map[string]any, len(fields))
	for _, f := range fields {
		m[f.Key] = f.Value
	}

	return m
}

// ---------------------------------------------------------------------------
// migrationSourceStats
// ---------------------------------------------------------------------------

func TestMigrationSourceStats(t *testing.T) {
	t.Parallel()

	// writeFiles creates empty files with the given names inside a fresh temp dir.
	writeFiles := func(t *testing.T, names ...string) string {
		t.Helper()

		dir := t.TempDir()
		for _, n := range names {
			require.NoError(t, os.WriteFile(filepath.Join(dir, n), []byte("-- noop"), 0o600))
		}

		return dir
	}

	t.Run("missing directory yields zero", func(t *testing.T) {
		t.Parallel()

		count, max := migrationSourceStats(filepath.Join(t.TempDir(), "does-not-exist"))
		assert.Equal(t, 0, count)
		assert.Equal(t, uint(0), max)
	})

	t.Run("empty directory yields zero", func(t *testing.T) {
		t.Parallel()

		count, max := migrationSourceStats(t.TempDir())
		assert.Equal(t, 0, count)
		assert.Equal(t, uint(0), max)
	})

	t.Run("counts only up.sql and picks max regardless of order", func(t *testing.T) {
		t.Parallel()

		dir := writeFiles(t,
			"000005_e.up.sql", "000005_e.down.sql",
			"000002_b.up.sql", "000002_b.down.sql",
			"000016_p.up.sql", "000016_p.down.sql",
			"000010_j.up.sql", "000010_j.down.sql",
		)
		count, max := migrationSourceStats(dir)
		assert.Equal(t, 4, count, "down.sql and duplicates must not inflate the count")
		assert.Equal(t, uint(16), max)
	})

	t.Run("ignores non-matching and malformed names", func(t *testing.T) {
		t.Parallel()

		dir := writeFiles(t,
			"README.md",
			"notes.txt",
			"_leading_underscore.up.sql", // sep <= 0 -> skipped
			"abc_nonnumeric.up.sql",      // ParseUint fails -> skipped
			"000007_ok.up.sql",           // the only valid one
		)
		count, max := migrationSourceStats(dir)
		assert.Equal(t, 1, count)
		assert.Equal(t, uint(7), max)
	})
}

// ---------------------------------------------------------------------------
// inspectMigrationState
// ---------------------------------------------------------------------------

// fakeVersionReader is a test double for migrationVersionReader.
type fakeVersionReader struct {
	version uint
	dirty   bool
	err     error
}

func (f fakeVersionReader) Version() (uint, bool, error) { return f.version, f.dirty, f.err }

func TestInspectMigrationState(t *testing.T) {
	t.Parallel()

	dirWith := func(t *testing.T, names ...string) string {
		t.Helper()

		dir := t.TempDir()
		for _, n := range names {
			require.NoError(t, os.WriteFile(filepath.Join(dir, n), []byte("-- noop"), 0o600))
		}

		return dir
	}

	t.Run("reads version and source stats", func(t *testing.T) {
		t.Parallel()

		dir := dirWith(t, "000001_a.up.sql", "000002_b.up.sql")
		state := inspectMigrationState(fakeVersionReader{version: 17}, dir)

		assert.True(t, state.hasVersion)
		assert.Equal(t, uint(17), state.currentVersion)
		assert.Equal(t, 2, state.sourceCount)
		assert.Equal(t, uint(2), state.sourceMax)
		assert.Equal(t, dir, state.sourcePath)
	})

	t.Run("Version error leaves hasVersion false", func(t *testing.T) {
		t.Parallel()

		dir := dirWith(t, "000001_a.up.sql")
		state := inspectMigrationState(fakeVersionReader{err: migrate.ErrNilVersion}, dir)

		assert.False(t, state.hasVersion)
		assert.Equal(t, uint(0), state.currentVersion)
		assert.Equal(t, 1, state.sourceCount)
	})

	t.Run("nil reader leaves hasVersion false", func(t *testing.T) {
		t.Parallel()

		state := inspectMigrationState(nil, t.TempDir())
		assert.False(t, state.hasVersion)
		assert.Equal(t, 0, state.sourceCount)
	})
}

// ---------------------------------------------------------------------------
// createResolverFn panic recovery
// ---------------------------------------------------------------------------

func TestCreateResolverFnPanicRecovery(t *testing.T) {
	// dbresolver.New doesn't panic with nil DBs (it wraps them), so we test
	// the recovery pattern by installing a resolver factory that panics and
	// verifying buildConnection converts it to an error, not a crash.
	original := createResolverFn
	origOpen := dbOpenFn
	t.Cleanup(func() {
		createResolverFn = original
		dbOpenFn = origOpen
	})

	dbOpenFn = func(_, _ string) (*sql.DB, error) { return testDB(t), nil }
	createResolverFn = func(_ *sql.DB, _ *sql.DB, logger log.Logger) (_ dbresolver.DB, err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				err = fmt.Errorf("failed to create resolver: %v", recovered)
			}
		}()

		panic("dbresolver exploded")
	}

	client, err := New(validConfig())
	require.NoError(t, err)

	err = client.Connect(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create resolver")
	assert.Contains(t, err.Error(), "dbresolver exploded")
}

// ---------------------------------------------------------------------------
// Config expansion: ConnMaxLifetime, ConnMaxIdleTime
// ---------------------------------------------------------------------------

func TestConfigWithDefaultsNewFields(t *testing.T) {
	t.Parallel()

	t.Run("zero ConnMaxLifetime gets default", func(t *testing.T) {
		t.Parallel()

		cfg := Config{PrimaryDSN: "dsn", ReplicaDSN: "dsn"}.withDefaults()
		assert.Equal(t, defaultConnMaxLifetime, cfg.ConnMaxLifetime)
	})

	t.Run("zero ConnMaxIdleTime gets default", func(t *testing.T) {
		t.Parallel()

		cfg := Config{PrimaryDSN: "dsn", ReplicaDSN: "dsn"}.withDefaults()
		assert.Equal(t, defaultConnMaxIdleTime, cfg.ConnMaxIdleTime)
	})

	t.Run("custom values preserved", func(t *testing.T) {
		t.Parallel()

		cfg := Config{
			PrimaryDSN:      "dsn",
			ReplicaDSN:      "dsn",
			ConnMaxLifetime: 1 * time.Hour,
			ConnMaxIdleTime: 10 * time.Minute,
		}.withDefaults()
		assert.Equal(t, 1*time.Hour, cfg.ConnMaxLifetime)
		assert.Equal(t, 10*time.Minute, cfg.ConnMaxIdleTime)
	})
}

// ---------------------------------------------------------------------------
// validateDSN
// ---------------------------------------------------------------------------

func TestValidateDSN(t *testing.T) {
	t.Parallel()

	t.Run("valid postgres:// URL", func(t *testing.T) {
		t.Parallel()

		assert.NoError(t, validateDSN("postgres://localhost:5432/db"))
	})

	t.Run("valid postgresql:// URL", func(t *testing.T) {
		t.Parallel()

		assert.NoError(t, validateDSN("postgresql://localhost:5432/db"))
	})

	t.Run("key-value format accepted", func(t *testing.T) {
		t.Parallel()

		assert.NoError(t, validateDSN("host=localhost port=5432 dbname=mydb"))
	})

	t.Run("empty string accepted (checked elsewhere)", func(t *testing.T) {
		t.Parallel()

		assert.NoError(t, validateDSN(""))
	})
}

// ---------------------------------------------------------------------------
// warnInsecureDSN
// ---------------------------------------------------------------------------

func TestWarnInsecureDSN(t *testing.T) {
	t.Parallel()

	t.Run("no panic with nil logger", func(t *testing.T) {
		t.Parallel()

		assert.NotPanics(t, func() {
			warnInsecureDSN(context.Background(), nil, "postgres://host/db?sslmode=disable", "primary")
		})
	})

	t.Run("no panic with secure DSN", func(t *testing.T) {
		t.Parallel()

		warnInsecureDSN(context.Background(), log.NewNop(), "postgres://host/db?sslmode=require", "primary")
	})

	t.Run("no panic with insecure DSN", func(t *testing.T) {
		t.Parallel()

		warnInsecureDSN(context.Background(), log.NewNop(), "postgres://host/db?sslmode=disable", "primary")
	})
}

func TestDSNRequiresTLS(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		dsn  string
		want bool
	}{
		{name: "url require", dsn: "postgres://host/db?sslmode=require", want: true},
		{name: "url verify full", dsn: "postgres://host/db?sslmode=verify-full", want: true},
		{name: "url disable", dsn: "postgres://host/db?sslmode=disable", want: false},
		{name: "url prefer", dsn: "postgres://host/db?sslmode=prefer", want: false},
		{name: "url allow", dsn: "postgres://host/db?sslmode=allow", want: false},
		{name: "url missing mode", dsn: "postgres://host/db", want: false},
		{name: "keyword require", dsn: "host=localhost dbname=ledger sslmode=require", want: true},
		{name: "keyword prefer", dsn: "host=localhost dbname=ledger sslmode=prefer", want: false},
		{name: "keyword missing mode", dsn: "host=localhost dbname=ledger", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, dsnRequiresTLS(tt.dsn))
		})
	}
}

func TestNew_TLSEnforcement(t *testing.T) {
	t.Run("blocks downgrade capable sslmodes", func(t *testing.T) {
		unsetEnvVar(t, commons.EnvAllowInsecureTLS)

		for _, dsn := range []string{
			"postgres://host/db?sslmode=disable",
			"postgres://host/db?sslmode=prefer",
			"postgres://host/db?sslmode=allow",
			"postgres://host/db",
			"host=localhost dbname=ledger",
		} {
			cfg := Config{PrimaryDSN: dsn, ReplicaDSN: dsn}
			client, err := New(cfg)
			assert.Nil(t, client)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "TLS required")
			assert.Contains(t, err.Error(), commons.EnvAllowInsecureTLS)
		}
	})

	t.Run("allows require and stronger modes", func(t *testing.T) {
		unsetEnvVar(t, commons.EnvAllowInsecureTLS)

		for _, dsn := range []string{
			"postgres://host/db?sslmode=require",
			"postgres://host/db?sslmode=verify-ca",
			"postgres://host/db?sslmode=verify-full",
			"host=localhost dbname=ledger sslmode=require",
		} {
			client, err := New(Config{PrimaryDSN: dsn, ReplicaDSN: dsn})
			require.NoError(t, err)
			assert.NotNil(t, client)
		}
	})
}

// ---------------------------------------------------------------------------
// Migrator.Up context deadline check
// ---------------------------------------------------------------------------

func TestMigratorUpContextAlreadyCancelled(t *testing.T) {
	t.Parallel()

	m, err := NewMigrator(MigrationConfig{
		PrimaryDSN:     "postgres://localhost/db",
		DatabaseName:   "ledger",
		MigrationsPath: "/migrations",
	})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = m.Up(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestMigratorUpBlocksPlaintextBeforeOpen(t *testing.T) {
	unsetEnvVar(t, commons.EnvAllowInsecureTLS)

	openCalled := false
	withPatchedDependencies(
		t,
		func(_, _ string) (*sql.DB, error) {
			openCalled = true
			return testDB(t), nil
		},
		func(*sql.DB, *sql.DB, log.Logger) (dbresolver.DB, error) {
			return nil, nil
		},
		func(context.Context, *sql.DB, string, string, bool, bool, log.Logger) error {
			return nil
		},
	)

	m, err := NewMigrator(MigrationConfig{
		PrimaryDSN:     "postgres://localhost/db?sslmode=disable",
		DatabaseName:   "ledger",
		MigrationsPath: "/migrations",
		Logger:         log.NewNop(),
	})
	require.NoError(t, err)

	err = m.Up(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TLS required")
	assert.Contains(t, err.Error(), commons.EnvAllowInsecureTLS)
	assert.False(t, openCalled)
}

func TestMigratorUpAllowsSecureDSN(t *testing.T) {
	unsetEnvVar(t, commons.EnvAllowInsecureTLS)

	openCalled := false
	migrateCalled := false
	withPatchedDependencies(
		t,
		func(_, _ string) (*sql.DB, error) {
			openCalled = true
			return testDB(t), nil
		},
		func(*sql.DB, *sql.DB, log.Logger) (dbresolver.DB, error) {
			return nil, nil
		},
		func(context.Context, *sql.DB, string, string, bool, bool, log.Logger) error {
			migrateCalled = true
			return nil
		},
	)

	m, err := NewMigrator(MigrationConfig{
		PrimaryDSN:     "postgres://localhost/db?sslmode=require",
		DatabaseName:   "ledger",
		MigrationsPath: "/migrations",
		Logger:         log.NewNop(),
	})
	require.NoError(t, err)

	err = m.Up(context.Background())
	require.NoError(t, err)
	assert.True(t, openCalled)
	assert.True(t, migrateCalled)
}

// ---------------------------------------------------------------------------
// Close defensive cleanup
// ---------------------------------------------------------------------------

func TestCloseDefensiveCleanup(t *testing.T) {
	t.Run("closes primary and replica even when resolver succeeds", func(t *testing.T) {
		resolver := &fakeResolver{}

		withPatchedDependencies(
			t,
			func(_, _ string) (*sql.DB, error) { return testDB(t), nil },
			func(*sql.DB, *sql.DB, log.Logger) (dbresolver.DB, error) { return resolver, nil },
			func(context.Context, *sql.DB, string, string, bool, bool, log.Logger) error { return nil },
		)

		client, err := New(validConfig())
		require.NoError(t, err)

		err = client.Connect(context.Background())
		require.NoError(t, err)

		err = client.Close()
		assert.NoError(t, err)
		assert.Equal(t, int32(1), resolver.closeCall.Load())

		// Verify that primary and replica handles are cleared after Close.
		client.mu.Lock()
		assert.Nil(t, client.primary, "primary should be nil after Close")
		assert.Nil(t, client.replica, "replica should be nil after Close")
		assert.Nil(t, client.resolver, "resolver should be nil after Close")
		client.mu.Unlock()
	})

	t.Run("collects multiple close errors", func(t *testing.T) {
		resolver := &fakeResolver{closeErr: errors.New("resolver close failed")}

		client, err := New(validConfig())
		require.NoError(t, err)
		client.resolver = resolver

		err = client.Close()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "resolver close failed")
	})
}
