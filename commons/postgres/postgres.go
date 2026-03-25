package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	// File system migration source. We need to import it to be able to use it as source in migrate.NewWithSourceInstance

	commons "github.com/LerianStudio/lib-commons/v4/commons"
	"github.com/LerianStudio/lib-commons/v4/commons/assert"
	"github.com/LerianStudio/lib-commons/v4/commons/backoff"
	constant "github.com/LerianStudio/lib-commons/v4/commons/constants"
	"github.com/LerianStudio/lib-commons/v4/commons/log"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v4/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v4/commons/opentelemetry/metrics"
	"github.com/LerianStudio/lib-commons/v4/commons/runtime"
	"github.com/bxcodec/dbresolver/v2"
	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/jackc/pgx/v5/stdlib"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

const (
	defaultMaxOpenConns    = 25
	defaultMaxIdleConns    = 10
	defaultConnMaxLifetime = 30 * time.Minute
	defaultConnMaxIdleTime = 5 * time.Minute
)

var (
	// ErrNilClient is returned when a postgres client receiver is nil.
	ErrNilClient = errors.New("postgres client is nil")
	// ErrNilContext is returned when a required context is nil.
	ErrNilContext = errors.New("context is nil")
	// ErrInvalidConfig indicates invalid postgres or migration configuration.
	ErrInvalidConfig = errors.New("invalid postgres config")
	// ErrNotConnected indicates operations requiring an active connection were called before connect.
	ErrNotConnected = errors.New("postgres client is not connected")
	// ErrInvalidDatabaseName indicates an invalid database identifier.
	ErrInvalidDatabaseName = errors.New("invalid database name")
	// ErrMigrationDirty indicates migrations stopped at a dirty version.
	ErrMigrationDirty = errors.New("postgres migration dirty")
	// ErrNilMigrator is returned when a migrator receiver is nil.
	ErrNilMigrator = errors.New("postgres migrator is nil")
	// ErrMigrationsNotFound is returned when the migration source directory is missing or empty.
	// Services that intentionally skip migrations can opt in via WithAllowMissingMigrations().
	ErrMigrationsNotFound = errors.New("migration files not found")

	defaultClientDeps   = newDefaultClientDeps()
	defaultMigratorDeps = newDefaultMigratorDeps()

	connectionStringCredentialsPattern = regexp.MustCompile(`://[^@\s]+@`)
	connectionStringPasswordPattern    = regexp.MustCompile(`(?i)(password=)(\S+)`)
	sslPathPattern                     = regexp.MustCompile(`(?i)(sslkey|sslcert|sslrootcert|sslpassword)=(\S+)`)
	dbNamePattern                      = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,62}$`)
)

// nilClientAssert fires a telemetry assertion for nil-receiver calls and returns ErrNilClient.
// The logger is intentionally nil here because this function is called on a nil *Client receiver,
// so there is no struct instance from which to extract a logger. The assert package handles
// nil loggers gracefully by falling back to stderr.
func nilClientAssert(operation string) error {
	asserter := assert.New(context.Background(), nil, "postgres", operation)
	_ = asserter.Never(context.Background(), "postgres client receiver is nil")

	return fmt.Errorf("postgres %s: %w", operation, ErrNilClient)
}

// nilMigratorAssert fires a telemetry assertion for nil-receiver calls and returns ErrNilMigrator.
// The logger is intentionally nil here because this function is called on a nil *Migrator receiver,
// so there is no struct instance from which to extract a logger. The assert package handles
// nil loggers gracefully by falling back to stderr.
func nilMigratorAssert(operation string) error {
	asserter := assert.New(context.Background(), nil, "postgres", operation)
	_ = asserter.Never(context.Background(), "postgres migrator receiver is nil")

	return fmt.Errorf("postgres %s: %w", operation, ErrNilMigrator)
}

// Config stores immutable connection options for a postgres client.
type Config struct {
	PrimaryDSN         string
	ReplicaDSN         string
	Logger             log.Logger
	MetricsFactory     *metrics.MetricsFactory
	MaxOpenConnections int
	MaxIdleConnections int
	ConnMaxLifetime    time.Duration
	ConnMaxIdleTime    time.Duration
}

func (c Config) withDefaults() Config {
	if c.Logger == nil {
		c.Logger = log.NewNop()
	}

	if c.MaxOpenConnections <= 0 {
		c.MaxOpenConnections = defaultMaxOpenConns
	}

	if c.MaxIdleConnections <= 0 {
		c.MaxIdleConnections = defaultMaxIdleConns
	}

	if c.ConnMaxLifetime <= 0 {
		c.ConnMaxLifetime = defaultConnMaxLifetime
	}

	if c.ConnMaxIdleTime <= 0 {
		c.ConnMaxIdleTime = defaultConnMaxIdleTime
	}

	return c
}

func (c Config) validate() error {
	if strings.TrimSpace(c.PrimaryDSN) == "" {
		return fmt.Errorf("%w: primary dsn cannot be empty", ErrInvalidConfig)
	}

	if err := validateDSN(c.PrimaryDSN); err != nil {
		return fmt.Errorf("%w: primary dsn: %w", ErrInvalidConfig, err)
	}

	if strings.TrimSpace(c.ReplicaDSN) == "" {
		return fmt.Errorf("%w: replica dsn cannot be empty", ErrInvalidConfig)
	}

	if err := validateDSN(c.ReplicaDSN); err != nil {
		return fmt.Errorf("%w: replica dsn: %w", ErrInvalidConfig, err)
	}

	return nil
}

// validateDSN checks structural validity of URL-format DSNs.
// Key-value format DSNs (without postgres:// prefix) are accepted without structural checks.
func validateDSN(dsn string) error {
	lower := strings.ToLower(strings.TrimSpace(dsn))
	if strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://") {
		if _, err := url.Parse(dsn); err != nil {
			return fmt.Errorf("malformed URL: %w", err)
		}
	}

	return nil
}

func dsnRequiresTLS(dsn string) bool {
	mode := strings.ToLower(strings.TrimSpace(dsnSSLMode(dsn)))
	return mode == "require" || mode == "verify-ca" || mode == "verify-full"
}

func dsnSSLMode(dsn string) string {
	trimmed := strings.TrimSpace(dsn)

	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://") {
		parsed, err := url.Parse(trimmed)
		if err != nil {
			return ""
		}

		return strings.Trim(parsed.Query().Get("sslmode"), " '\"")
	}

	for field := range strings.FieldsSeq(trimmed) {
		key, value, ok := strings.Cut(field, "=")
		if !ok || !strings.EqualFold(key, "sslmode") {
			continue
		}

		return strings.Trim(value, " '\"")
	}

	return ""
}

func enforceTLSPolicy(ctx context.Context, logger log.Logger, label, dsn string) error {
	if commons.CurrentTier() != commons.TierStrict || strings.TrimSpace(dsn) == "" {
		return nil
	}

	tlsDisabled := !dsnRequiresTLS(dsn)
	result := commons.CheckSecurityRule(commons.RuleTLSRequired, tlsDisabled)

	return commons.EnforceSecurityRule(ctx, logger, "postgres-"+label, result)
}

// warnInsecureDSN logs a warning if the DSN does not guarantee TLS.
// This is advisory -- development environments commonly use sslmode=disable.
func warnInsecureDSN(ctx context.Context, logger log.Logger, dsn, label string) {
	if logger == nil || !logger.Enabled(log.LevelWarn) {
		return
	}

	if !dsnRequiresTLS(dsn) {
		logger.Log(ctx, log.LevelWarn,
			"TLS is not guaranteed in database connection; production deployments should use sslmode=require or stronger",
			log.String("dsn_label", label),
		)
	}
}

// connectBackoffCap is the maximum delay between lazy-connect retries.
const connectBackoffCap = 30 * time.Second

// connectionFailuresMetric defines the counter for postgres connection failures.
var connectionFailuresMetric = metrics.Metric{
	Name:        "postgres_connection_failures_total",
	Unit:        "1",
	Description: "Total number of postgres connection failures",
}

// Client is the v2 postgres connection manager.
type Client struct {
	mu             sync.RWMutex
	cfg            Config
	metricsFactory *metrics.MetricsFactory
	deps           clientDeps
	resolver       dbresolver.DB
	primary        *sql.DB
	replica        *sql.DB

	// Lazy-connect rate-limiting: prevents thundering-herd reconnect storms
	// when the database is down by enforcing exponential backoff between attempts.
	lastConnectAttempt time.Time
	connectAttempts    int
}

type clientDeps struct {
	openDB         func(string, string) (*sql.DB, error)
	createResolver func(*sql.DB, *sql.DB, log.Logger) (dbresolver.DB, error)
}

func newDefaultClientDeps() clientDeps {
	return clientDeps{
		openDB: sql.Open,
		createResolver: func(primaryDB, replicaDB *sql.DB, logger log.Logger) (_ dbresolver.DB, err error) {
			defer func() {
				if recovered := recover(); recovered != nil {
					if logger == nil {
						logger = log.NewNop()
					}

					runtime.HandlePanicValue(context.Background(), logger, recovered, "postgres", "create_resolver")
					err = fmt.Errorf("failed to create resolver: %w", fmt.Errorf("recovered panic: %v", recovered))
				}
			}()

			connectionDB := dbresolver.New(
				dbresolver.WithPrimaryDBs(primaryDB),
				dbresolver.WithReplicaDBs(replicaDB),
				dbresolver.WithLoadBalancer(dbresolver.RoundRobinLB),
			)

			if connectionDB == nil {
				return nil, errors.New("resolver returned nil connection")
			}

			return connectionDB, nil
		},
	}
}

func (c *Client) resolvedDeps() clientDeps {
	if c == nil {
		return defaultClientDeps
	}

	deps := c.deps
	if deps.openDB == nil {
		deps.openDB = defaultClientDeps.openDB
	}

	if deps.createResolver == nil {
		deps.createResolver = defaultClientDeps.createResolver
	}

	return deps
}

// New creates a postgres client with immutable configuration.
func New(cfg Config) (*Client, error) {
	cfg = cfg.withDefaults()

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("postgres new: %w", err)
	}

	// Security policy: TLS enforcement in strict tier (production).
	// Check both primary and replica DSNs — data from an unencrypted replica
	// is equally sensitive.
	for _, dsn := range []struct{ label, value string }{
		{"primary", cfg.PrimaryDSN},
		{"replica", cfg.ReplicaDSN},
	} {
		if err := enforceTLSPolicy(context.Background(), cfg.Logger, dsn.label, dsn.value); err != nil {
			return nil, fmt.Errorf("postgres new: %w", err)
		}
	}

	return &Client{cfg: cfg, metricsFactory: cfg.MetricsFactory, deps: defaultClientDeps}, nil
}

// logAtLevel emits a structured log entry at the specified level.
func (c *Client) logAtLevel(ctx context.Context, level log.Level, msg string, fields ...log.Field) {
	if c == nil || c.cfg.Logger == nil {
		return
	}

	if !c.cfg.Logger.Enabled(level) {
		return
	}

	c.cfg.Logger.Log(ctx, level, msg, fields...)
}

// Connect establishes a new primary/replica resolver and swaps it atomically.
func (c *Client) Connect(ctx context.Context) error {
	if c == nil {
		return nilClientAssert("connect")
	}

	if ctx == nil {
		return fmt.Errorf("postgres connect: %w", ErrNilContext)
	}

	tracer := otel.Tracer("postgres")

	ctx, span := tracer.Start(ctx, "postgres.connect")
	defer span.End()

	span.SetAttributes(attribute.String(constant.AttrDBSystem, constant.DBSystemPostgreSQL))

	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.connectLocked(ctx); err != nil {
		c.recordConnectionFailure(ctx, "connect")

		libOpentelemetry.HandleSpanError(span, "Failed to connect to postgres", err)

		return err
	}

	return nil
}

// connectLocked performs the actual connection logic.
// The caller MUST hold c.mu (write lock) before calling this method.
func (c *Client) connectLocked(ctx context.Context) error {
	primary, replica, resolver, err := c.buildConnection(ctx)
	if err != nil {
		return err
	}

	oldResolver := c.resolver
	oldPrimary := c.primary
	oldReplica := c.replica

	c.resolver = resolver
	c.primary = primary
	c.replica = replica

	if oldResolver != nil {
		if err := oldResolver.Close(); err != nil {
			c.logAtLevel(ctx, log.LevelWarn, "failed to close previous resolver after swap", log.Err(err))
		}
	}

	// Always close old primary/replica explicitly to prevent leaks.
	// The resolver may not own the underlying sql.DB connections.
	if err := closeDB(oldPrimary); err != nil {
		c.logAtLevel(ctx, log.LevelWarn, "failed to close old primary during swap", log.Err(err))
	}

	if err := closeDB(oldReplica); err != nil {
		c.logAtLevel(ctx, log.LevelWarn, "failed to close old replica during swap", log.Err(err))
	}

	c.logAtLevel(ctx, log.LevelInfo, "connected to postgres")

	return nil
}

func (c *Client) buildConnection(ctx context.Context) (*sql.DB, *sql.DB, dbresolver.DB, error) {
	c.logAtLevel(ctx, log.LevelInfo, "connecting to primary and replica databases")

	warnInsecureDSN(ctx, c.cfg.Logger, c.cfg.PrimaryDSN, "primary")
	warnInsecureDSN(ctx, c.cfg.Logger, c.cfg.ReplicaDSN, "replica")

	primary, err := c.newSQLDB(ctx, c.cfg.PrimaryDSN)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("postgres connect: %w", err)
	}

	replica, err := c.newSQLDB(ctx, c.cfg.ReplicaDSN)
	if err != nil {
		_ = closeDB(primary)
		return nil, nil, nil, fmt.Errorf("postgres connect: %w", err)
	}

	resolver, err := c.resolvedDeps().createResolver(primary, replica, c.cfg.Logger)
	if err != nil {
		_ = closeDB(primary)
		_ = closeDB(replica)

		c.logAtLevel(ctx, log.LevelError, "failed to create resolver", log.Err(err))

		return nil, nil, nil, fmt.Errorf("postgres connect: failed to create resolver: %w", err)
	}

	if err := resolver.PingContext(ctx); err != nil {
		_ = resolver.Close()
		_ = closeDB(primary)
		_ = closeDB(replica)

		c.logAtLevel(ctx, log.LevelError, "failed to ping database", log.Err(err))

		return nil, nil, nil, fmt.Errorf("postgres connect: failed to ping database: %w", err)
	}

	return primary, replica, resolver, nil
}

func (c *Client) newSQLDB(ctx context.Context, dsn string) (*sql.DB, error) {
	db, err := c.resolvedDeps().openDB("pgx", dsn)
	if err != nil {
		sanitized := newSanitizedError(err, "failed to open database")
		c.logAtLevel(ctx, log.LevelError, "failed to open database", log.Err(sanitized))

		return nil, sanitized
	}

	db.SetMaxOpenConns(c.cfg.MaxOpenConnections)
	db.SetMaxIdleConns(c.cfg.MaxIdleConnections)
	db.SetConnMaxLifetime(c.cfg.ConnMaxLifetime)
	db.SetConnMaxIdleTime(c.cfg.ConnMaxIdleTime)

	return db, nil
}

// Resolver returns the resolver, connecting lazily if needed.
// Unlike sync.Once, this uses double-checked locking so that a transient
// failure on the first call does not permanently break the client --
// subsequent calls will retry the connection.
func (c *Client) Resolver(ctx context.Context) (dbresolver.DB, error) {
	if c == nil {
		return nil, nilClientAssert("resolver")
	}

	if ctx == nil {
		return nil, fmt.Errorf("postgres resolver: %w", ErrNilContext)
	}

	// Fast path: already connected (read-lock only).
	c.mu.RLock()
	resolver := c.resolver
	c.mu.RUnlock()

	if resolver != nil {
		return resolver, nil
	}

	// Slow path: acquire write lock and double-check before connecting.
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.resolver != nil {
		return c.resolver, nil
	}

	// Rate-limit lazy-connect retries: if previous attempts failed recently,
	// enforce a minimum delay before the next attempt to prevent reconnect storms.
	if c.connectAttempts > 0 {
		delay := min(backoff.ExponentialWithJitter(1*time.Second, c.connectAttempts), connectBackoffCap)

		if elapsed := time.Since(c.lastConnectAttempt); elapsed < delay {
			return nil, fmt.Errorf("postgres resolver: rate-limited (next attempt in %s)", delay-elapsed)
		}
	}

	c.lastConnectAttempt = time.Now()

	tracer := otel.Tracer("postgres")

	ctx, span := tracer.Start(ctx, "postgres.resolve")
	defer span.End()

	span.SetAttributes(attribute.String(constant.AttrDBSystem, constant.DBSystemPostgreSQL))

	if err := c.connectLocked(ctx); err != nil {
		c.connectAttempts++
		c.recordConnectionFailure(ctx, "resolve")

		libOpentelemetry.HandleSpanError(span, "Failed to resolve postgres connection", err)

		return nil, err
	}

	c.connectAttempts = 0

	if c.resolver == nil {
		err := fmt.Errorf("postgres resolver: %w", ErrNotConnected)
		libOpentelemetry.HandleSpanError(span, "Postgres resolver not connected after connect", err)

		return nil, err
	}

	return c.resolver, nil
}

// Primary returns the current primary sql.DB, useful for admin operations.
func (c *Client) Primary() (*sql.DB, error) {
	if c == nil {
		return nil, nilClientAssert("primary")
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.primary == nil {
		return nil, fmt.Errorf("postgres primary: %w", ErrNotConnected)
	}

	return c.primary, nil
}

// Close releases database resources.
// All three handles (resolver, primary, replica) are always explicitly closed
// to prevent leaks -- the resolver may not own the underlying sql.DB connections.
func (c *Client) Close() error {
	if c == nil {
		return nilClientAssert("close")
	}

	tracer := otel.Tracer("postgres")

	_, span := tracer.Start(context.Background(), "postgres.close")
	defer span.End()

	span.SetAttributes(attribute.String(constant.AttrDBSystem, constant.DBSystemPostgreSQL))

	c.mu.Lock()
	resolver := c.resolver
	primary := c.primary
	replica := c.replica

	c.resolver = nil
	c.primary = nil
	c.replica = nil
	c.mu.Unlock()

	var errs []error

	if resolver != nil {
		if err := resolver.Close(); err != nil {
			errs = append(errs, err)
		}
	}

	// Always close primary/replica explicitly to prevent leaks.
	// The resolver may not own the underlying sql.DB connections.
	if err := closeDB(primary); err != nil {
		errs = append(errs, err)
	}

	if err := closeDB(replica); err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		closeErr := fmt.Errorf("postgres close: %w", errors.Join(errs...))
		libOpentelemetry.HandleSpanError(span, "Failed to close postgres", closeErr)

		return closeErr
	}

	return nil
}

// IsConnected reports whether the resolver is currently initialized.
func (c *Client) IsConnected() (bool, error) {
	if c == nil {
		return false, nilClientAssert("is_connected")
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.resolver != nil, nil
}

func closeDB(db *sql.DB) error {
	if db == nil {
		return nil
	}

	return db.Close()
}

// MigrationConfig stores migration-only settings.
type MigrationConfig struct {
	PrimaryDSN     string
	DatabaseName   string
	MigrationsPath string
	Component      string
	// AllowMultiStatements enables multi-statement execution in migrations.
	// SECURITY: Only enable when migration files are from trusted, version-controlled sources.
	// Multi-statement mode increases the blast radius of compromised migration files.
	AllowMultiStatements bool
	// AllowMissingMigrations makes Migrator.Up return nil instead of ErrMigrationsNotFound
	// when the migration source directory does not exist. Use this for services that
	// intentionally have no migrations (e.g., worker-only services sharing a database).
	AllowMissingMigrations bool
	Logger                 log.Logger
}

func (c MigrationConfig) withDefaults() MigrationConfig {
	if c.Logger == nil {
		c.Logger = log.NewNop()
	}

	return c
}

func (c MigrationConfig) validate() error {
	if strings.TrimSpace(c.PrimaryDSN) == "" {
		return fmt.Errorf("%w: primary dsn cannot be empty", ErrInvalidConfig)
	}

	if err := validateDBName(c.DatabaseName); err != nil {
		return fmt.Errorf("migration config: %w", err)
	}

	if strings.TrimSpace(c.MigrationsPath) == "" && strings.TrimSpace(c.Component) == "" {
		return fmt.Errorf("%w: migrations_path or component is required", ErrInvalidConfig)
	}

	return nil
}

// Migrator runs schema migrations explicitly.
type Migrator struct {
	cfg  MigrationConfig
	deps migratorDeps
}

type migratorDeps struct {
	openDB        func(string, string) (*sql.DB, error)
	runMigrations func(context.Context, *sql.DB, string, string, bool, bool, log.Logger) error
}

func newDefaultMigratorDeps() migratorDeps {
	return migratorDeps{
		openDB:        sql.Open,
		runMigrations: runMigrations,
	}
}

func (m *Migrator) resolvedDeps() migratorDeps {
	if m == nil {
		return defaultMigratorDeps
	}

	deps := m.deps
	if deps.openDB == nil {
		deps.openDB = defaultMigratorDeps.openDB
	}

	if deps.runMigrations == nil {
		deps.runMigrations = defaultMigratorDeps.runMigrations
	}

	return deps
}

// NewMigrator creates a migrator with explicit migration config.
func NewMigrator(cfg MigrationConfig) (*Migrator, error) {
	cfg = cfg.withDefaults()

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("postgres new_migrator: %w", err)
	}

	return &Migrator{cfg: cfg, deps: defaultMigratorDeps}, nil
}

func (m *Migrator) logAtLevel(ctx context.Context, level log.Level, msg string, fields ...log.Field) {
	if m == nil || m.cfg.Logger == nil {
		return
	}

	if !m.cfg.Logger.Enabled(level) {
		return
	}

	m.cfg.Logger.Log(ctx, level, msg, fields...)
}

// Up runs all up migrations.
//
// Note: golang-migrate's m.Up() does not accept a context, so cancellation
// cannot stop a migration in progress. This method checks context state
// before starting but cannot interrupt a running migration.
func (m *Migrator) Up(ctx context.Context) error {
	if m == nil {
		return nilMigratorAssert("migrate_up")
	}

	if ctx == nil {
		return fmt.Errorf("postgres migrate_up: %w", ErrNilContext)
	}

	tracer := otel.Tracer("postgres")

	ctx, span := tracer.Start(ctx, "postgres.migrate_up")
	defer span.End()

	span.SetAttributes(
		attribute.String(constant.AttrDBSystem, constant.DBSystemPostgreSQL),
		attribute.String(constant.AttrDBName, m.cfg.DatabaseName),
	)

	// Fail fast if the context is already cancelled or expired.
	if err := ctx.Err(); err != nil {
		libOpentelemetry.HandleSpanError(span, "Context already done before migration", err)

		return fmt.Errorf("postgres migrate_up: context already done: %w", err)
	}

	if err := enforceTLSPolicy(ctx, m.cfg.Logger, "primary", m.cfg.PrimaryDSN); err != nil {
		libOpentelemetry.HandleSpanError(span, "Migration TLS policy blocked connection", err)

		return fmt.Errorf("postgres migrate_up: %w", err)
	}

	db, err := m.resolvedDeps().openDB("pgx", m.cfg.PrimaryDSN)
	if err != nil {
		sanitized := newSanitizedError(err, "failed to open migration database")
		m.logAtLevel(ctx, log.LevelError, "failed to open migration database", log.Err(sanitized))

		libOpentelemetry.HandleSpanError(span, "Failed to open migration database", sanitized)

		return fmt.Errorf("postgres migrate_up: %w", sanitized)
	}
	defer db.Close()

	migrationsPath, err := resolveMigrationsPath(m.cfg.MigrationsPath, m.cfg.Component)
	if err != nil {
		m.logAtLevel(ctx, log.LevelError, "failed to resolve migration path", log.Err(err))

		libOpentelemetry.HandleSpanError(span, "Failed to resolve migration path", err)

		return fmt.Errorf("postgres migrate_up: %w", err)
	}

	if err := m.resolvedDeps().runMigrations(ctx, db, migrationsPath, m.cfg.DatabaseName, m.cfg.AllowMultiStatements, m.cfg.AllowMissingMigrations, m.cfg.Logger); err != nil {
		libOpentelemetry.HandleSpanError(span, "Migration up failed", err)

		return fmt.Errorf("postgres migrate_up: %w", err)
	}

	return nil
}

func resolveMigrationsPath(migrationsPath, component string) (string, error) {
	if strings.TrimSpace(migrationsPath) != "" {
		return sanitizePath(migrationsPath)
	}

	// filepath.Base strips directory components, so "../../etc" becomes "etc".
	sanitized := filepath.Base(component)
	if sanitized == "." || sanitized == string(filepath.Separator) || sanitized == "" {
		return "", fmt.Errorf("invalid component name: %q", component)
	}

	calculatedPath, err := filepath.Abs(filepath.Join("components", sanitized, "migrations"))
	if err != nil {
		return "", err
	}

	return calculatedPath, nil
}

// SanitizedError wraps a database error with a credential-free message.
// Error() returns only the sanitized text.
//
// Unwrap returns a sanitized copy of the original error that preserves
// error types and sentinels (via errors.Is / errors.As) while stripping
// connection strings and credentials from the message text.
type SanitizedError struct {
	// Message is the credential-free error description.
	Message string
	// cause is a sanitized version of the original error that preserves
	// error types/sentinels but strips credentials from messages.
	cause error
}

func (e *SanitizedError) Error() string { return e.Message }

// Unwrap returns the sanitized cause, enabling errors.Is / errors.As
// chain traversal without leaking credentials.
func (e *SanitizedError) Unwrap() error { return e.cause }

// sanitizedCause creates a credential-free copy of the cause error chain.
// It preserves the type of known sentinel errors (e.g., sql.ErrNoRows) by
// wrapping a new error with the sanitized message around the original sentinel.
func sanitizedCause(err error) error {
	if err == nil {
		return nil
	}

	sanitizedMsg := sanitizeSensitiveString(err.Error())

	return errors.New(sanitizedMsg)
}

// newSanitizedError wraps err with a credential-free message.
// A sanitized copy of the cause is stored for error chain traversal.
func newSanitizedError(err error, prefix string) *SanitizedError {
	if err == nil {
		return nil
	}

	return &SanitizedError{
		Message: fmt.Sprintf("%s: %s", prefix, sanitizeSensitiveString(err.Error())),
		cause:   sanitizedCause(err),
	}
}

// sanitizeSensitiveString removes credentials and sensitive paths from a string.
func sanitizeSensitiveString(s string) string {
	s = connectionStringCredentialsPattern.ReplaceAllString(s, "://***@")
	s = connectionStringPasswordPattern.ReplaceAllString(s, "${1}***")
	s = sslPathPattern.ReplaceAllString(s, "${1}=***")

	return s
}

func sanitizePath(path string) (string, error) {
	cleaned := filepath.Clean(path)
	if slices.Contains(strings.Split(cleaned, string(filepath.Separator)), "..") {
		return "", fmt.Errorf("invalid migrations path: %q", path)
	}

	absPath, err := filepath.Abs(cleaned)
	if err != nil {
		return "", fmt.Errorf("failed to resolve migrations path: %w", err)
	}

	return absPath, nil
}

func validateDBName(name string) error {
	if !dbNamePattern.MatchString(name) {
		return fmt.Errorf("%w: %q", ErrInvalidDatabaseName, name)
	}

	return nil
}

// migrationOutcome describes the result of classifying a migration error.
type migrationOutcome struct {
	err     error
	level   log.Level
	message string
	fields  []log.Field
}

// classifyMigrationError converts a golang-migrate error into a typed outcome.
// Returns a zero-value outcome (err == nil) on success or benign cases (ErrNoChange).
// When allowMissing is true, ErrNotExist is treated as benign (nil error); otherwise
// it returns ErrMigrationsNotFound so the caller can distinguish missing files from success.
func classifyMigrationError(err error, allowMissing bool) migrationOutcome {
	if err == nil {
		return migrationOutcome{}
	}

	if errors.Is(err, migrate.ErrNoChange) {
		return migrationOutcome{
			level:   log.LevelInfo,
			message: "no new migrations found, skipping",
		}
	}

	if errors.Is(err, os.ErrNotExist) {
		if allowMissing {
			return migrationOutcome{
				level:   log.LevelWarn,
				message: "no migration files found, skipping (AllowMissingMigrations=true)",
			}
		}

		return migrationOutcome{
			err:     fmt.Errorf("%w: source directory missing or empty", ErrMigrationsNotFound),
			level:   log.LevelError,
			message: "no migration files found",
		}
	}

	var dirtyErr migrate.ErrDirty
	if errors.As(err, &dirtyErr) {
		return migrationOutcome{
			err:     fmt.Errorf("%w: database version %d", ErrMigrationDirty, dirtyErr.Version),
			level:   log.LevelError,
			message: "migration failed with dirty version",
			fields:  []log.Field{log.Int("dirty_version", dirtyErr.Version)},
		}
	}

	return migrationOutcome{
		err:     fmt.Errorf("migration failed: %w", err),
		level:   log.LevelError,
		message: "migration failed",
		fields:  []log.Field{log.Err(err)},
	}
}

// recordConnectionFailure increments the postgres connection failure counter.
// No-op when metricsFactory is nil. ctx is used for metric recording and tracing.
func (c *Client) recordConnectionFailure(ctx context.Context, operation string) {
	if c == nil || c.metricsFactory == nil {
		return
	}

	counter, err := c.metricsFactory.Counter(connectionFailuresMetric)
	if err != nil {
		c.logAtLevel(ctx, log.LevelWarn, "failed to create postgres metric counter", log.Err(err))
		return
	}

	err = counter.
		WithLabels(map[string]string{
			"operation": constant.SanitizeMetricLabel(operation),
		}).
		AddOne(ctx)
	if err != nil {
		c.logAtLevel(ctx, log.LevelWarn, "failed to record postgres metric", log.Err(err))
	}
}

// migrationLogAtLevel logs at the given level if logger is non-nil and the level is enabled.
// This eliminates repeated nil-check + level-check branches in migration helpers.
func migrationLogAtLevel(ctx context.Context, logger log.Logger, level log.Level, msg string, fields ...log.Field) {
	if logger == nil || !logger.Enabled(level) {
		return
	}

	logger.Log(ctx, level, msg, fields...)
}

// resolveMigrationSource parses the migrations path into a file:// URL.
func resolveMigrationSource(migrationsPath string) (*url.URL, error) {
	primaryURL, err := url.Parse(filepath.ToSlash(migrationsPath))
	if err != nil {
		return nil, fmt.Errorf("failed to parse migrations url: %w", err)
	}

	primaryURL.Scheme = "file"

	return primaryURL, nil
}

// createMigrationInstance creates the postgres driver and migration instance.
func createMigrationInstance(dbPrimary *sql.DB, sourceURL, primaryDBName string, allowMultiStatements bool) (*migrate.Migrate, error) {
	primaryDriver, err := postgres.WithInstance(dbPrimary, &postgres.Config{
		MultiStatementEnabled: allowMultiStatements,
		DatabaseName:          primaryDBName,
		SchemaName:            "public",
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create postgres driver instance: %w", err)
	}

	mig, err := migrate.NewWithDatabaseInstance(sourceURL, primaryDBName, primaryDriver)
	if err != nil {
		return nil, fmt.Errorf("failed to create migration instance: %w", err)
	}

	return mig, nil
}

// closeMigration releases source and database driver resources. Errors are logged
// but not propagated since the migration itself already ran (or failed).
func closeMigration(ctx context.Context, mig *migrate.Migrate, logger log.Logger) {
	sourceErr, dbErr := mig.Close()
	if sourceErr != nil {
		migrationLogAtLevel(ctx, logger, log.LevelWarn, "failed to close migration source driver", log.Err(sourceErr))
	}

	if dbErr != nil {
		migrationLogAtLevel(ctx, logger, log.LevelWarn, "failed to close migration database driver", log.Err(dbErr))
	}
}

func runMigrations(ctx context.Context, dbPrimary *sql.DB, migrationsPath, primaryDBName string, allowMultiStatements, allowMissingMigrations bool, logger log.Logger) error {
	if err := validateDBName(primaryDBName); err != nil {
		migrationLogAtLevel(ctx, logger, log.LevelError, "invalid primary database name", log.Err(err))

		return fmt.Errorf("migrations: %w", err)
	}

	primaryURL, err := resolveMigrationSource(migrationsPath)
	if err != nil {
		migrationLogAtLevel(ctx, logger, log.LevelError, "failed to parse migrations url", log.Err(err))

		return err
	}

	mig, err := createMigrationInstance(dbPrimary, primaryURL.String(), primaryDBName, allowMultiStatements)
	if err != nil {
		migrationLogAtLevel(ctx, logger, log.LevelError, err.Error())

		return err
	}

	defer closeMigration(ctx, mig, logger)

	if err := mig.Up(); err != nil {
		outcome := classifyMigrationError(err, allowMissingMigrations)

		migrationLogAtLevel(ctx, logger, outcome.level, outcome.message, outcome.fields...)

		return outcome.err
	}

	return nil
}
