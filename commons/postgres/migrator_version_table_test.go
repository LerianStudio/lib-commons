//go:build unit

package postgres

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMigrationConfig_VersionTableValidation(t *testing.T) {
	t.Parallel()

	valid := []string{"", "schema_migrations", "schema_migrations_a", "_scope", "a", "a" + strings.Repeat("b", 62)}
	invalid := []string{
		"Schema_Migrations",           // the driver quotes it: mixed case would turn case-sensitive
		"1_migrations",                // must start with a letter or underscore
		"schema-migrations",           // punctuation
		"public.schema_migrations",    // a qualified name; the schema has its own field
		`schema"migrations`,           // quoting
		" schema_migrations",          // whitespace
		"a" + strings.Repeat("b", 63), // past PostgreSQL's 63-byte identifier limit
	}

	fields := map[string]func(*MigrationConfig, string){
		"migrations_table":  func(c *MigrationConfig, v string) { c.MigrationsTable = v },
		"migrations_schema": func(c *MigrationConfig, v string) { c.MigrationsSchema = v },
	}

	for field, set := range fields {
		for _, value := range valid {
			t.Run(field+" accepts "+value, func(t *testing.T) {
				t.Parallel()

				cfg := MigrationConfig{
					PrimaryDSN:     "postgres://app:pw@127.0.0.1:1/ledger?sslmode=disable",
					DatabaseName:   "ledger",
					MigrationsPath: "migrations",
				}
				set(&cfg, value)

				_, err := NewMigrator(cfg)
				require.NoError(t, err)
			})
		}

		for _, value := range invalid {
			t.Run(field+" refuses "+value, func(t *testing.T) {
				t.Parallel()

				cfg := MigrationConfig{
					PrimaryDSN:     "postgres://app:pw@127.0.0.1:1/ledger?sslmode=disable",
					DatabaseName:   "ledger",
					MigrationsPath: "migrations",
				}
				set(&cfg, value)

				_, err := NewMigrator(cfg)
				require.ErrorIs(t, err, ErrInvalidConfig)
				assert.Contains(t, err.Error(), field)
			})
		}
	}
}

func TestMigrationDriverConfig_VersionTable(t *testing.T) {
	t.Parallel()

	t.Run("defaults keep golang-migrate's public.schema_migrations", func(t *testing.T) {
		t.Parallel()

		cfg := migrationDriverConfig(migrationRun{databaseName: "ledger"})

		assert.Equal(t, "public", cfg.SchemaName)
		assert.Equal(t, postgres.DefaultMigrationsTable, cfg.MigrationsTable)
		assert.False(t, cfg.MigrationsTableQuoted)
	})

	t.Run("a configured schema and table reach the driver", func(t *testing.T) {
		t.Parallel()

		m, err := NewMigrator(MigrationConfig{
			PrimaryDSN:       "postgres://app:pw@127.0.0.1:1/ledger?sslmode=disable",
			DatabaseName:     "ledger",
			MigrationsPath:   "migrations",
			MigrationsTable:  "schema_migrations_b",
			MigrationsSchema: "scope_b",
		})
		require.NoError(t, err)

		cfg := migrationDriverConfig(m.baseRun())

		assert.Equal(t, "scope_b", cfg.SchemaName)
		assert.Equal(t, "schema_migrations_b", cfg.MigrationsTable)
		assert.False(t, cfg.MigrationsTableQuoted, "the identifier is validated, never parsed as schema.table")
	})
}

func TestVersionNotInSource_NamesTheVersionTable(t *testing.T) {
	t.Parallel()

	ahead := func(run migrationRun) string {
		run.sourceCount, run.sourceMax = 1, 1

		state := inspectMigrationState(fakeVersionReader{version: 3}, run)
		outcome := classifyMigrationError(os.ErrNotExist, false, state, migrationRedactor{})
		require.ErrorIs(t, outcome.err, ErrMigrationVersionAhead)

		return outcome.err.Error()
	}

	assert.Contains(t, ahead(migrationRun{}), "reconcile public.schema_migrations")
	assert.Contains(t, ahead(migrationRun{migrationsSchema: "scope_b", migrationsTable: "schema_migrations_b"}),
		"reconcile scope_b.schema_migrations_b")
}

func TestEnsureMigrationsSchema(t *testing.T) {
	t.Parallel()

	const existsQuery = `SELECT EXISTS \(SELECT 1 FROM pg_namespace WHERE nspname = \$1\)`

	withConn := func(t *testing.T, expect func(sqlmock.Sqlmock)) (*sql.Conn, sqlmock.Sqlmock) {
		t.Helper()

		db, mock, err := sqlmock.New()
		require.NoError(t, err)

		t.Cleanup(func() { _ = db.Close() })

		expect(mock)

		conn, err := db.Conn(context.Background())
		require.NoError(t, err)

		t.Cleanup(func() { _ = conn.Close() })

		return conn, mock
	}

	t.Run("the default schema is never touched", func(t *testing.T) {
		t.Parallel()

		conn, mock := withConn(t, func(sqlmock.Sqlmock) {})

		require.NoError(t, ensureMigrationsSchema(context.Background(), conn, migrationRun{}))
		require.NoError(t, ensureMigrationsSchema(context.Background(), conn, migrationRun{migrationsSchema: "public"}))
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("an existing schema is not created again", func(t *testing.T) {
		t.Parallel()

		conn, mock := withConn(t, func(mock sqlmock.Sqlmock) {
			mock.ExpectQuery(existsQuery).WithArgs("scope_b").
				WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
		})

		require.NoError(t, ensureMigrationsSchema(context.Background(), conn, migrationRun{migrationsSchema: "scope_b"}))
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("a missing schema is created", func(t *testing.T) {
		t.Parallel()

		conn, mock := withConn(t, func(mock sqlmock.Sqlmock) {
			mock.ExpectQuery(existsQuery).WithArgs("scope_b").
				WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
			mock.ExpectExec(`CREATE SCHEMA IF NOT EXISTS "scope_b"`).WillReturnResult(sqlmock.NewResult(0, 0))
		})

		require.NoError(t, ensureMigrationsSchema(context.Background(), conn, migrationRun{migrationsSchema: "scope_b"}))
		require.NoError(t, mock.ExpectationsWereMet())
	})

	for _, code := range []string{"23505", "42P06"} {
		t.Run("a schema another migrator created first is fine ("+code+")", func(t *testing.T) {
			t.Parallel()

			conn, mock := withConn(t, func(mock sqlmock.Sqlmock) {
				mock.ExpectQuery(existsQuery).WithArgs("scope_b").
					WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
				mock.ExpectExec(`CREATE SCHEMA IF NOT EXISTS "scope_b"`).WillReturnError(&pgconn.PgError{Code: code})
			})

			require.NoError(t, ensureMigrationsSchema(context.Background(), conn, migrationRun{migrationsSchema: "scope_b"}))
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}

	t.Run("any other failure is sanitized", func(t *testing.T) {
		t.Parallel()

		conn, _ := withConn(t, func(mock sqlmock.Sqlmock) {
			mock.ExpectQuery(existsQuery).WithArgs("scope_b").
				WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
			mock.ExpectExec(`CREATE SCHEMA IF NOT EXISTS "scope_b"`).
				WillReturnError(&pgconn.PgError{Code: "42501", Message: "permission denied for database ledger"})
		})

		run := migrationRun{migrationsSchema: "scope_b", redactor: newMigrationRedactor("postgres://app:Hunter2Secret@db/ledger")}

		err := ensureMigrationsSchema(context.Background(), conn, run)
		require.Error(t, err)

		var sanitized *SanitizedError
		require.ErrorAs(t, err, &sanitized)
		assert.False(t, errors.As(err, new(*pgconn.PgError)), "the driver error is not reachable")
		assert.Contains(t, err.Error(), "scope_b")
	})
}
