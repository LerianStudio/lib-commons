package postgres

import (
	"cmp"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"
	constant "github.com/LerianStudio/lib-observability/v4/constants"
	libOpentelemetry "github.com/LerianStudio/lib-observability/v4/tracing"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel/attribute"
)

// migrationUnlockTimeout bounds the explicit release of the migration lock.
// Closing the connection releases it anyway, so a slow unlock is abandoned.
const migrationUnlockTimeout = 5 * time.Second

// MigrationStatus is the database's migration state as Migrator.Status reads it.
type MigrationStatus struct {
	// Version is the last migration recorded; meaningful only when Applied.
	Version uint
	// Dirty reports a migration that started and did not finish: the schema
	// needs a human before the next Up.
	Dirty bool
	// Applied is false when no migration has been recorded yet.
	Applied bool
}

// migrationRun is everything one Up needs, resolved from MigrationConfig
// before any database is opened.
type migrationRun struct {
	sourceFS             fs.FS
	sourceDir            string
	sourceLabel          string
	sourceCount          int
	sourceMax            uint
	databaseName         string
	allowMultiStatements bool
	allowMissing         bool
	statementTimeout     time.Duration
	lockTimeout          time.Duration
	migrationsSchema     string
	migrationsTable      string
	logger               obs.Logger
	redactor             migrationRedactor
}

// baseRun is the run without a source: what Status needs.
func (m *Migrator) baseRun() migrationRun {
	return migrationRun{
		databaseName:         m.cfg.DatabaseName,
		allowMultiStatements: m.cfg.AllowMultiStatements,
		allowMissing:         m.cfg.AllowMissingMigrations,
		statementTimeout:     m.cfg.StatementTimeout,
		lockTimeout:          m.cfg.LockTimeout,
		migrationsSchema:     m.cfg.MigrationsSchema,
		migrationsTable:      m.cfg.MigrationsTable,
		logger:               m.cfg.Logger,
		redactor:             m.redactor,
	}
}

// newRun resolves the migration source (MigrationsFS, or the directory on
// disk) and reads its stats.
func (m *Migrator) newRun() (migrationRun, error) {
	run := m.baseRun()

	if m.cfg.MigrationsFS != nil {
		run.sourceFS = m.cfg.MigrationsFS
		run.sourceDir = m.cfg.fsPath()
		run.sourceLabel = "fs:" + run.sourceDir
	} else {
		dir, err := resolveMigrationsPath(m.cfg.MigrationsPath, m.cfg.Component)
		if err != nil {
			return migrationRun{}, err
		}

		run.sourceFS = os.DirFS(dir)
		run.sourceDir = "."
		run.sourceLabel = dir
	}

	count, maxVersion, err := migrationSourceStats(run.sourceFS, run.sourceDir)
	if err != nil {
		return migrationRun{}, err
	}

	run.sourceCount, run.sourceMax = count, maxVersion

	return run, nil
}

// enforceTLS applies the configured posture to PrimaryDSN.
func (m *Migrator) enforceTLS(ctx context.Context) error {
	return enforceConnTLS(ctx, m.cfg.Logger, "primary", m.cfg.PrimaryDSN, m.cfg.TLSPosture, m.cfg.MinSSLMode)
}

// openDatabase opens (without dialing) the migration database handle.
func (m *Migrator) openDatabase(ctx context.Context) (*sql.DB, error) {
	db, err := dbOpenFn("pgx", m.cfg.PrimaryDSN)
	if err != nil {
		sanitized := m.redactor.sanitize(err, "failed to open migration database")
		m.logAtLevel(ctx, obs.LevelError, "failed to open migration database", "error", sanitized.Error())

		return nil, sanitized
	}

	return db, nil
}

// Status reports the database's migration state: the last recorded version,
// whether it is dirty, and whether any migration was recorded at all. It
// applies the TLS posture before opening the database, and its wait for the
// migration lock is bounded like Up's, so it fails with
// ErrMigrationLockTimeout while a long migration holds the lock.
//
// Side effect: golang-migrate creates the version table when it does not
// exist yet, so Status needs the same database rights as Up.
func (m *Migrator) Status(ctx context.Context) (MigrationStatus, error) {
	if m == nil {
		return MigrationStatus{}, nilMigratorAssert("migrate_status")
	}

	if ctx == nil {
		return MigrationStatus{}, fmt.Errorf("postgres migrate_status: %w", ErrNilContext)
	}

	ctx, span := tracer.Start(ctx, "postgres.migrate_status")
	defer span.End()

	span.SetAttributes(
		attribute.String(constant.AttrDBSystem, constant.DBSystemPostgreSQL),
		attribute.String(constant.AttrDBName, m.cfg.DatabaseName),
	)

	status, err := m.status(ctx)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "Migration status failed", err)

		return MigrationStatus{}, fmt.Errorf("postgres migrate_status: %w", err)
	}

	return status, nil
}

func (m *Migrator) status(ctx context.Context) (MigrationStatus, error) {
	if err := ctx.Err(); err != nil {
		return MigrationStatus{}, fmt.Errorf("context already done: %w", err)
	}

	if err := m.enforceTLS(ctx); err != nil {
		return MigrationStatus{}, err
	}

	db, err := m.openDatabase(ctx)
	if err != nil {
		return MigrationStatus{}, err
	}
	defer db.Close()

	run := m.baseRun()

	session, err := openMigrationSession(ctx, db, run)
	if err != nil {
		m.logAtLevel(ctx, obs.LevelError, "failed to open migration database session", "error", err.Error())

		return MigrationStatus{}, err
	}
	defer session.close(ctx, run)

	version, dirty, err := session.driver.Version()
	if err != nil {
		sanitized := m.redactor.sanitize(err, "failed to read migration version")
		m.logAtLevel(ctx, obs.LevelError, "failed to read migration version", "error", sanitized.Error())

		return MigrationStatus{}, sanitized
	}

	if version < 0 {
		return MigrationStatus{Dirty: dirty}, nil
	}

	return MigrationStatus{Version: uint(version), Dirty: dirty, Applied: true}, nil
}

// defaultMigrationsSchema holds the version table when MigrationsSchema is empty.
const defaultMigrationsSchema = "public"

// schema is the schema holding the run's version table.
func (run migrationRun) schema() string {
	return cmp.Or(run.migrationsSchema, defaultMigrationsSchema)
}

// table is the run's version table name, unqualified.
func (run migrationRun) table() string {
	return cmp.Or(run.migrationsTable, postgres.DefaultMigrationsTable)
}

// versionTable is the run's version table, schema-qualified, for messages.
func (run migrationRun) versionTable() string {
	return run.schema() + "." + run.table()
}

// migrationDriverConfig is the golang-migrate driver config for a run. The
// table name is passed unquoted (MigrationsTableQuoted false): it was
// validated as a plain identifier, never parsed as schema.table.
func migrationDriverConfig(run migrationRun) *postgres.Config {
	return &postgres.Config{
		MultiStatementEnabled: run.allowMultiStatements,
		DatabaseName:          run.databaseName,
		SchemaName:            run.schema(),
		MigrationsTable:       run.table(),
		StatementTimeout:      run.statementTimeout,
	}
}

// ensureMigrationsSchema creates the schema that holds the version table when
// it is not public and does not exist yet: golang-migrate creates the table,
// never its schema. An existing schema is left alone, so a role without
// CREATE on the database works once the schema is provisioned. Another
// migrator creating the same schema at the same moment is not a failure.
func ensureMigrationsSchema(ctx context.Context, conn *sql.Conn, run migrationRun) error {
	schema := run.schema()
	if schema == defaultMigrationsSchema {
		return nil
	}

	var exists bool

	err := conn.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = $1)", schema).Scan(&exists)
	if err != nil {
		return run.redactor.failure(ctx, err, "failed to look up migrations schema "+schema)
	}

	if exists {
		return nil
	}

	_, err = conn.ExecContext(ctx, "CREATE SCHEMA IF NOT EXISTS "+pgx.Identifier{schema}.Sanitize())
	if err != nil && !matchesSQLState(err, uniqueViolation) && !matchesSQLState(err, duplicateSchema) {
		return run.redactor.failure(ctx, err, "failed to create migrations schema "+schema)
	}

	return nil
}

// migrationSession is one dedicated connection holding the migration lock,
// with golang-migrate's driver on top of it.
type migrationSession struct {
	conn   *sql.Conn
	driver *postgres.Postgres
	lockID string
}

// openMigrationSession dials one connection under ctx, takes the migration
// advisory lock on it under the lock budget, creates the version table's
// schema when needed, then builds the driver on that same connection. The
// lock is per version table (database, schema, table), as golang-migrate
// derives it, so modules with their own tables never wait on each other.
//
// golang-migrate's own lock waits with context.Background() and no bound, and
// its driver constructor takes it before any timeout applies. Taking the same
// lock first, on the same session, makes every later driver lock immediate
// (PostgreSQL advisory locks are re-entrant per session), so the only wait is
// this bounded one.
func openMigrationSession(ctx context.Context, db *sql.DB, run migrationRun) (*migrationSession, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, run.redactor.failure(ctx, err, "failed to connect to migration database")
	}

	cfg := migrationDriverConfig(run)

	lockID, err := database.GenerateAdvisoryLockId(cfg.DatabaseName, cfg.SchemaName, cfg.MigrationsTable)
	if err != nil {
		_ = conn.Close()

		return nil, run.redactor.sanitize(err, "failed to derive migration lock id")
	}

	if err := acquireMigrationLock(ctx, conn, lockID, run); err != nil {
		_ = conn.Close()

		return nil, err
	}

	session := &migrationSession{conn: conn, lockID: lockID}

	if err := ensureMigrationsSchema(ctx, conn, run); err != nil {
		session.unlock(ctx, run)

		_ = conn.Close()

		return nil, err
	}

	driver, err := postgres.WithConnection(ctx, conn, cfg)
	if err != nil {
		session.unlock(ctx, run)

		_ = conn.Close()

		return nil, run.redactor.failure(ctx, err, "failed to create postgres driver instance")
	}

	session.driver = driver

	return session, nil
}

func acquireMigrationLock(ctx context.Context, conn *sql.Conn, lockID string, run migrationRun) error {
	budget := migrationLockBudget(ctx, run.lockTimeout)
	if budget <= 0 {
		return fmt.Errorf("waiting for the migration lock: %w", context.DeadlineExceeded)
	}

	lockCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	if _, err := conn.ExecContext(lockCtx, "SELECT pg_advisory_lock($1)", lockID); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("waiting for the migration lock: %w", ctxErr)
		}

		if lockCtx.Err() != nil {
			return fmt.Errorf("%w: waited %s", ErrMigrationLockTimeout, budget)
		}

		return run.redactor.sanitize(err, "failed to acquire migration lock")
	}

	return nil
}

// migrationLockBudget is how long the migration lock may be waited for:
// LockTimeout (golang-migrate's default when zero), never past ctx's deadline.
// Zero means the deadline already passed.
func migrationLockBudget(ctx context.Context, configured time.Duration) time.Duration {
	budget := configured
	if budget <= 0 {
		budget = migrate.DefaultLockTimeout
	}

	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return 0
		}

		budget = min(budget, remaining)
	}

	return budget
}

// unlock releases the session's migration lock. A failure is logged only:
// closing the connection releases the lock anyway.
func (s *migrationSession) unlock(ctx context.Context, run migrationRun) {
	unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), migrationUnlockTimeout)
	defer cancel()

	if _, err := s.conn.ExecContext(unlockCtx, "SELECT pg_advisory_unlock($1)", s.lockID); err != nil {
		migrationLogAtLevel(ctx, run.logger, obs.LevelWarn, "failed to release the migration lock",
			"error", run.redactor.redact(err.Error()))
	}
}

// close releases the lock and the driver (which closes the connection).
func (s *migrationSession) close(ctx context.Context, run migrationRun) {
	s.unlock(ctx, run)

	if err := s.driver.Close(); err != nil {
		migrationLogAtLevel(ctx, run.logger, obs.LevelWarn, "failed to close migration database driver",
			"error", run.redactor.redact(err.Error()))
	}
}

// migrationRedactor removes PrimaryDSN's password from any text the migrator
// returns or logs. It replaces the password by value, in every spelling it can
// take in a driver's message (as written, decoded, query-, path- and
// userinfo-encoded, keyword-escaped), then applies the pattern redaction that
// covers credentials it could not know in advance.
type migrationRedactor struct {
	secrets []string
}

func newMigrationRedactor(dsn string) migrationRedactor {
	var passwords []string

	if parsed, err := url.Parse(dsn); err == nil && parsed.User != nil {
		if password, ok := parsed.User.Password(); ok {
			passwords = append(passwords, password)
		}
	}

	// A URL a strict parser refuses (a raw space, say) still reaches drivers
	// that echo it; read its userinfo the way they print it.
	if _, rest, ok := strings.Cut(dsn, "://"); ok {
		if at := strings.LastIndexByte(rest, '@'); at >= 0 {
			if _, password, ok := strings.Cut(rest[:at], ":"); ok {
				passwords = append(passwords, password)

				if decoded, err := url.PathUnescape(password); err == nil {
					passwords = append(passwords, decoded)
				}
			}
		}
	}

	if password := dsnKeywordValue(dsn, "password"); password != "" {
		passwords = append(passwords, password)
	}

	keywordEscaper := strings.NewReplacer(`\`, `\\`, `'`, `\'`)

	var secrets []string

	for _, password := range passwords {
		if password == "" {
			continue
		}

		secrets = append(secrets,
			password,
			url.QueryEscape(password),
			url.PathEscape(password),
			strings.TrimPrefix(url.UserPassword("", password).String(), ":"),
			keywordEscaper.Replace(password),
		)
	}

	// Longest first, so a spelling that contains another is replaced whole.
	slices.SortFunc(secrets, func(a, b string) int { return cmp.Or(cmp.Compare(len(b), len(a)), strings.Compare(a, b)) })

	return migrationRedactor{secrets: slices.Compact(secrets)}
}

func (r migrationRedactor) redact(s string) string {
	for _, secret := range r.secrets {
		s = strings.ReplaceAll(s, secret, constant.ObfuscatedValue)
	}

	return sanitizeSensitiveString(s)
}

// sanitize wraps err in a *SanitizedError whose text is redacted and whose
// chain no longer reaches err.
func (r migrationRedactor) sanitize(err error, prefix string) *SanitizedError {
	msg := r.redact(err.Error())

	return &SanitizedError{Message: prefix + ": " + msg, cause: errors.New(msg)}
}

// failure is sanitize for a step bounded by ctx: when ctx is done, its error
// joins the chain so errors.Is(err, context.DeadlineExceeded) holds.
func (r migrationRedactor) failure(ctx context.Context, err error, prefix string) error {
	sanitized := r.sanitize(err, prefix)

	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("%w (%w)", sanitized, ctxErr)
	}

	return sanitized
}
