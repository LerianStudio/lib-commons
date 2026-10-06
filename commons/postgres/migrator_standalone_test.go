//go:build unit

package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons"
	"github.com/golang-migrate/migrate/v4"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capturingLogger records every log line, message and fields, so a test can
// assert a secret never reached the logs in any shape.
type capturingLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *capturingLogger) Log(_ context.Context, level int, msg string, kv ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.lines = append(l.lines, fmt.Sprintf("%d %s %+v", level, msg, kv))
}

func (l *capturingLogger) Enabled(int) bool { return true }

func (l *capturingLogger) Sync(context.Context) error { return nil }

func (l *capturingLogger) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()

	return strings.Join(l.lines, "\n")
}

// leakCase is one DSN shape and every spelling of its password that must never
// surface: as written in the DSN, decoded, and the fragments a regex-only
// redaction would leave behind.
type leakCase struct {
	name    string
	dsn     string
	secrets []string
}

// leakCases point at 127.0.0.1:1, a closed local port: the dial fails fast and
// nothing outside this machine is contacted.
func leakCases() []leakCase {
	return []leakCase{
		{
			name:    "url",
			dsn:     "postgres://app:Sup3rS3cretPw@127.0.0.1:1/ledger?sslmode=disable&connect_timeout=2",
			secrets: []string{"Sup3rS3cretPw"},
		},
		{
			name:    "url percent-encoded",
			dsn:     "postgres://app:p%40ssW0rdS3cret@127.0.0.1:1/ledger?sslmode=disable&connect_timeout=2",
			secrets: []string{"p@ssW0rdS3cret", "p%40ssW0rdS3cret", "ssW0rdS3cret"},
		},
		{
			name:    "keyword quoted with a space",
			dsn:     "host=127.0.0.1 port=1 user=app password='Sup3r S3cretPw' dbname=ledger sslmode=disable connect_timeout=2",
			secrets: []string{"Sup3r S3cretPw", "S3cretPw"},
		},
		{
			name:    "keyword with an escaped quote",
			dsn:     `host=127.0.0.1 port=1 user=app password='it\'sAS3cretPw' dbname=ledger sslmode=disable connect_timeout=2`,
			secrets: []string{`it'sAS3cretPw`, `it\'sAS3cretPw`, "AS3cretPw"},
		},
	}
}

func assertNoSecret(t *testing.T, err error, logs string, secrets []string) {
	t.Helper()

	require.Error(t, err)

	rendered := err.Error() + "\n" + fmt.Sprintf("%+v", err) + "\n" + fmt.Sprintf("%#v", err) + "\n" + logs
	for _, secret := range secrets {
		assert.NotContains(t, rendered, secret, "the password leaked into the error or the logs")
	}

	var connectErr *pgconn.ConnectError
	assert.False(t, errors.As(err, &connectErr), "pgx's ConnectError carries the full config and must not be reachable")
}

// writeMigrationDir creates a directory holding the given up-migration files.
func writeMigrationDir(t *testing.T, names ...string) string {
	t.Helper()

	dir := t.TempDir()
	for _, name := range names {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name), []byte("SELECT 1;"), 0o600))
	}

	return dir
}

func TestMigratorUp_RealDialNeverLeaksPassword(t *testing.T) {
	dir := writeMigrationDir(t, "000001_init.up.sql")

	for _, tc := range leakCases() {
		t.Run(tc.name, func(t *testing.T) {
			logger := &capturingLogger{}

			m, err := NewMigrator(MigrationConfig{
				PrimaryDSN:     tc.dsn,
				DatabaseName:   "ledger",
				MigrationsPath: dir,
				Logger:         logger,
			})
			require.NoError(t, err)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			assertNoSecret(t, m.Up(ctx), logger.all(), tc.secrets)
		})
	}
}

// echoConnector is a driver whose dial error repeats the DSN verbatim: the
// worst a driver could do, and what the migrator must survive.
type echoConnector struct{ dsn string }

func (c echoConnector) Connect(context.Context) (driver.Conn, error) {
	return nil, fmt.Errorf("dial failed for %s (password is in there)", c.dsn)
}

func (c echoConnector) Driver() driver.Driver { return echoDriver{dsn: c.dsn} }

type echoDriver struct{ dsn string }

func (d echoDriver) Open(string) (driver.Conn, error) {
	return echoConnector(d).Connect(context.Background())
}

func withEchoingDriver(t *testing.T) {
	t.Helper()

	original := dbOpenFn
	dbOpenFn = func(_, dsn string) (*sql.DB, error) { return sql.OpenDB(echoConnector{dsn: dsn}), nil }

	t.Cleanup(func() { dbOpenFn = original })
}

func TestMigratorUp_EchoingDriverNeverLeaksPassword(t *testing.T) {
	withEchoingDriver(t)

	dir := writeMigrationDir(t, "000001_init.up.sql")

	cases := append(leakCases(), leakCase{
		name:    "url with a raw at sign",
		dsn:     "postgres://app:Sup3r@S3cretPw@127.0.0.1:1/ledger?sslmode=disable",
		secrets: []string{"Sup3r@S3cretPw", "S3cretPw"},
	})

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			logger := &capturingLogger{}

			m, err := NewMigrator(MigrationConfig{
				PrimaryDSN:     tc.dsn,
				DatabaseName:   "ledger",
				MigrationsPath: dir,
				Logger:         logger,
			})
			require.NoError(t, err)

			assertNoSecret(t, m.Up(context.Background()), logger.all(), tc.secrets)
		})
	}
}

// A missing directory under AllowMissingMigrations is "nothing to do": it
// must succeed, and it must not need a database to say so.
func TestMigratorUp_MissingDirectoryAllowedWithoutDialing(t *testing.T) {
	openCalled := false
	original := dbOpenFn
	dbOpenFn = func(_, _ string) (*sql.DB, error) {
		openCalled = true
		return nil, errors.New("must not open")
	}

	t.Cleanup(func() { dbOpenFn = original })

	m, err := NewMigrator(MigrationConfig{
		PrimaryDSN:             "postgres://app:pw@127.0.0.1:1/ledger?sslmode=disable",
		DatabaseName:           "ledger",
		MigrationsPath:         filepath.Join(t.TempDir(), "does-not-exist"),
		AllowMissingMigrations: true,
	})
	require.NoError(t, err)

	require.NoError(t, m.Up(context.Background()))
	assert.False(t, openCalled, "a missing source must be classified before the database is opened")
}

func TestMigratorUp_MissingDirectoryRefusedWithoutDialing(t *testing.T) {
	openCalled := false
	original := dbOpenFn
	dbOpenFn = func(_, _ string) (*sql.DB, error) {
		openCalled = true
		return nil, errors.New("must not open")
	}

	t.Cleanup(func() { dbOpenFn = original })

	m, err := NewMigrator(MigrationConfig{
		PrimaryDSN:     "postgres://app:pw@127.0.0.1:1/ledger?sslmode=disable",
		DatabaseName:   "ledger",
		MigrationsPath: filepath.Join(t.TempDir(), "does-not-exist"),
	})
	require.NoError(t, err)

	err = m.Up(context.Background())
	require.ErrorIs(t, err, ErrMigrationsNotFound)
	assert.False(t, openCalled, "a missing source must be classified before the database is opened")
}

func TestMigratorStatus_RealDialNeverLeaksPassword(t *testing.T) {
	for _, tc := range leakCases() {
		t.Run(tc.name, func(t *testing.T) {
			logger := &capturingLogger{}

			m, err := NewMigrator(MigrationConfig{
				PrimaryDSN:     tc.dsn,
				DatabaseName:   "ledger",
				MigrationsPath: t.TempDir(),
				Logger:         logger,
			})
			require.NoError(t, err)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()

			_, err = m.Status(ctx)
			assertNoSecret(t, err, logger.all(), tc.secrets)
		})
	}
}

func TestMigratorStatus_EchoingDriverNeverLeaksPassword(t *testing.T) {
	withEchoingDriver(t)

	for _, tc := range leakCases() {
		t.Run(tc.name, func(t *testing.T) {
			logger := &capturingLogger{}

			m, err := NewMigrator(MigrationConfig{
				PrimaryDSN:     tc.dsn,
				DatabaseName:   "ledger",
				MigrationsPath: t.TempDir(),
				Logger:         logger,
			})
			require.NoError(t, err)

			_, err = m.Status(context.Background())
			assertNoSecret(t, err, logger.all(), tc.secrets)
		})
	}
}

// The dial is bounded by the caller's context: a dial that never answers
// returns the context's own error, with identity intact.
func TestMigratorUp_DialHonoursContextDeadline(t *testing.T) {
	original := dbOpenFn
	dbOpenFn = func(_, _ string) (*sql.DB, error) { return sql.OpenDB(blockingConnector{}), nil }

	t.Cleanup(func() { dbOpenFn = original })

	m, err := NewMigrator(MigrationConfig{
		PrimaryDSN:     "postgres://app:Sup3rS3cretPw@127.0.0.1:1/ledger?sslmode=disable",
		DatabaseName:   "ledger",
		MigrationsPath: writeMigrationDir(t, "000001_init.up.sql"),
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err = m.Up(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.NotContains(t, err.Error(), "Sup3rS3cretPw")
}

// blockingConnector dials until the context gives up.
type blockingConnector struct{}

func (blockingConnector) Connect(ctx context.Context) (driver.Conn, error) {
	<-ctx.Done()

	return nil, fmt.Errorf("dial postgres://app:Sup3rS3cretPw@127.0.0.1:1/ledger: %w", ctx.Err())
}

func (blockingConnector) Driver() driver.Driver { return echoDriver{} }

func embeddedMigrations() fstest.MapFS {
	return fstest.MapFS{
		"sql/000001_init.up.sql":   {Data: []byte("CREATE TABLE a (id int);")},
		"sql/000001_init.down.sql": {Data: []byte("DROP TABLE a;")},
		"sql/000002_more.up.sql":   {Data: []byte("CREATE TABLE b (id int);")},
		"sql/README.md":            {Data: []byte("not a migration")},
		"empty/README.md":          {Data: []byte("not a migration")},
	}
}

func TestMigrationSourceStats_FS(t *testing.T) {
	t.Parallel()

	count, maxVersion, err := migrationSourceStats(embeddedMigrations(), "sql")
	require.NoError(t, err)
	assert.Equal(t, 2, count)
	assert.Equal(t, uint(2), maxVersion)

	count, maxVersion, err = migrationSourceStats(embeddedMigrations(), "missing")
	require.NoError(t, err, "a missing directory is an empty source, not a failure")
	assert.Zero(t, count)
	assert.Zero(t, maxVersion)

	_, _, err = migrationSourceStats(unreadableFS{}, ".")
	require.Error(t, err, "an unreadable source is a failure, never an empty source")
}

type unreadableFS struct{}

func (unreadableFS) Open(string) (fs.File, error) { return nil, fs.ErrPermission }

func TestMigratorUp_EmbeddedSourceMissingOrEmptyWithoutDialing(t *testing.T) {
	for _, path := range []string{"missing", "empty"} {
		for _, allowMissing := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s allow_missing=%v", path, allowMissing), func(t *testing.T) {
				openCalled := false
				original := dbOpenFn
				dbOpenFn = func(_, _ string) (*sql.DB, error) {
					openCalled = true
					return nil, errors.New("must not open")
				}

				t.Cleanup(func() { dbOpenFn = original })

				m, err := NewMigrator(MigrationConfig{
					PrimaryDSN:             "postgres://app:pw@127.0.0.1:1/ledger?sslmode=disable",
					DatabaseName:           "ledger",
					MigrationsFS:           embeddedMigrations(),
					MigrationsPath:         path,
					AllowMissingMigrations: allowMissing,
				})
				require.NoError(t, err)

				err = m.Up(context.Background())
				if allowMissing {
					require.NoError(t, err)
				} else {
					require.ErrorIs(t, err, ErrMigrationsNotFound)
				}

				assert.False(t, openCalled, "a missing or empty source must be classified before the database is opened")
			})
		}
	}
}

func TestMigratorUp_EmbeddedSourceReachesTheRun(t *testing.T) {
	var got migrationRun

	withPatchedDependencies(t,
		func(_, _ string) (*sql.DB, error) { return testDB(t), nil },
		nil,
		func(_ context.Context, _ *sql.DB, run migrationRun) error {
			got = run
			return nil
		},
	)

	m, err := NewMigrator(MigrationConfig{
		PrimaryDSN:       "postgres://app:pw@127.0.0.1:1/ledger?sslmode=disable",
		DatabaseName:     "ledger",
		MigrationsFS:     embeddedMigrations(),
		MigrationsPath:   "sql",
		StatementTimeout: 30 * time.Second,
		LockTimeout:      5 * time.Second,
	})
	require.NoError(t, err)

	require.NoError(t, m.Up(context.Background()))
	assert.Equal(t, "sql", got.sourceDir)
	assert.Equal(t, 2, got.sourceCount)
	assert.Equal(t, uint(2), got.sourceMax)
	assert.Equal(t, 30*time.Second, got.statementTimeout)
	assert.Equal(t, 5*time.Second, got.lockTimeout)
	assert.Equal(t, "ledger", got.databaseName)
}

func TestMigrationConfig_StandaloneValidation(t *testing.T) {
	t.Parallel()

	base := func() MigrationConfig {
		return MigrationConfig{
			PrimaryDSN:   "postgres://app:pw@127.0.0.1:1/ledger?sslmode=disable",
			DatabaseName: "ledger",
			MigrationsFS: embeddedMigrations(),
		}
	}

	t.Run("fs without a path reads its root", func(t *testing.T) {
		t.Parallel()

		_, err := NewMigrator(base())
		require.NoError(t, err)
	})

	invalid := map[string]func(*MigrationConfig){
		"fs with component":          func(c *MigrationConfig) { c.Component = "ledger" },
		"fs path escaping the root":  func(c *MigrationConfig) { c.MigrationsPath = "../sql" },
		"fs path that is absolute":   func(c *MigrationConfig) { c.MigrationsPath = "/sql" },
		"negative statement timeout": func(c *MigrationConfig) { c.StatementTimeout = -time.Second },
		"negative lock timeout":      func(c *MigrationConfig) { c.LockTimeout = -time.Second },
	}

	for name, mutate := range invalid {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			cfg := base()
			mutate(&cfg)

			_, err := NewMigrator(cfg)
			require.ErrorIs(t, err, ErrInvalidConfig)
		})
	}
}

func TestMigrationLockBudget(t *testing.T) {
	t.Parallel()

	assert.Equal(t, migrate.DefaultLockTimeout, migrationLockBudget(context.Background(), 0))
	assert.Equal(t, time.Second, migrationLockBudget(context.Background(), time.Second))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	budget := migrationLockBudget(ctx, time.Hour)
	assert.LessOrEqual(t, budget, 2*time.Second, "the lock wait never outlives the caller's deadline")
	assert.Positive(t, budget)

	expired, cancelExpired := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelExpired()

	assert.Zero(t, migrationLockBudget(expired, time.Hour))
}

func TestMigratorStatus_ContextAndNilSafety(t *testing.T) {
	var nilMigrator *Migrator

	_, err := nilMigrator.Status(context.Background())
	require.ErrorIs(t, err, ErrNilMigrator)

	m, err := NewMigrator(MigrationConfig{
		PrimaryDSN:     "postgres://app:pw@127.0.0.1:1/ledger?sslmode=disable",
		DatabaseName:   "ledger",
		MigrationsPath: t.TempDir(),
	})
	require.NoError(t, err)

	//nolint:staticcheck // a nil context is the case under test
	_, err = m.Status(nil)
	require.ErrorIs(t, err, ErrNilContext)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = m.Status(ctx)
	require.ErrorIs(t, err, context.Canceled)
}

// TestMigratorStatus_PostureRefusesBeforeOpen: Status reads the database, so it
// is held to the same TLS posture as Up, ALLOW_INSECURE_TLS notwithstanding.
func TestMigratorStatus_PostureRefusesBeforeOpen(t *testing.T) {
	t.Setenv(commons.EnvAllowInsecureTLS, "true")

	openCalled := false
	original := dbOpenFn
	dbOpenFn = func(_, _ string) (*sql.DB, error) {
		openCalled = true
		return nil, errors.New("must not open")
	}

	t.Cleanup(func() { dbOpenFn = original })

	m, err := NewMigrator(MigrationConfig{
		PrimaryDSN:     urlDSN("require"),
		DatabaseName:   "ledger",
		MigrationsPath: t.TempDir(),
		TLSPosture:     TLSPostureHardened,
	})
	require.NoError(t, err)

	_, err = m.Status(context.Background())
	require.ErrorIs(t, err, ErrWeakSSLMode)
	assert.NotContains(t, err.Error(), posturePassword)
	assert.False(t, openCalled, "the posture must refuse before the database is opened")
}

func TestMigrationRedactor(t *testing.T) {
	t.Parallel()

	for _, tc := range leakCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := newMigrationRedactor(tc.dsn)
			out := r.redact("echo: " + tc.dsn)

			for _, secret := range tc.secrets {
				assert.NotContains(t, out, secret)
			}

			assert.Contains(t, out, "echo: ")
		})
	}

	t.Run("no dsn redacts by pattern only", func(t *testing.T) {
		t.Parallel()

		assert.Equal(t, "plain message", newMigrationRedactor("").redact("plain message"))
	})
}

// The pattern redaction is the net under credentials the migrator could not
// know in advance (a pool's DSN echoed by some other layer): a quoted keyword
// password and a raw "@" in a URL password are consumed whole.
func TestSanitizeSensitiveString_QuotedAndRawAt(t *testing.T) {
	t.Parallel()

	for input, secret := range map[string]string{
		"dial host=db password='a b c' dbname=x":         "b c",
		`dial host=db password='it\'s b' dbname=x`:       "s b",
		"dial host=db password='unterminated tail":       "tail",
		"dial postgres://app:p@ss@db:5432/ledger failed": "ss@db",
		"dial host=db PASSWORD = spaced dbname=x":        "spaced",
	} {
		out := sanitizeSensitiveString(input)
		assert.NotContains(t, out, secret, "input %q", input)
		assert.True(t, strings.HasPrefix(out, "dial "), "the message itself survives: %q", out)
	}
}

func TestStoppedEarly(t *testing.T) {
	t.Parallel()

	run := migrationRun{sourceMax: 3, redactor: newMigrationRedactor("")}

	t.Run("a run that reached the newest version is complete", func(t *testing.T) {
		t.Parallel()

		require.NoError(t, stoppedEarly(context.Background(), fakeVersionReader{version: 3}, run, context.DeadlineExceeded))
	})

	t.Run("a run stopped short names the version and keeps the context error", func(t *testing.T) {
		t.Parallel()

		err := stoppedEarly(context.Background(), fakeVersionReader{version: 1}, run, context.DeadlineExceeded)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Contains(t, err.Error(), "stopped at version 1")
	})

	t.Run("a run stopped before the first migration is at version 0", func(t *testing.T) {
		t.Parallel()

		err := stoppedEarly(context.Background(), fakeVersionReader{err: migrate.ErrNilVersion}, run, context.Canceled)
		require.ErrorIs(t, err, context.Canceled)
		assert.Contains(t, err.Error(), "stopped at version 0")
	})

	t.Run("an unreadable version is reported, sanitized", func(t *testing.T) {
		t.Parallel()

		err := stoppedEarly(context.Background(), fakeVersionReader{err: errors.New("read failed password=hunter2")}, run, context.Canceled)
		require.ErrorIs(t, err, context.Canceled)
		assert.NotContains(t, err.Error(), "hunter2")
	})
}
