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
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"

	// File system migration source. We need to import it to be able to use it as source in migrate.NewWithSourceInstance

	commons "github.com/LerianStudio/lib-commons/v7/commons"
	"github.com/LerianStudio/lib-commons/v7/commons/backoff"
	"github.com/LerianStudio/lib-observability/v4/assert"
	constant "github.com/LerianStudio/lib-observability/v4/constants"
	"github.com/LerianStudio/lib-observability/v4/runtime"
	"github.com/LerianStudio/lib-observability/v4/sqlobs"
	libOpentelemetry "github.com/LerianStudio/lib-observability/v4/tracing"
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
	// ErrMigrationVersionAhead is returned when the database schema version is higher
	// than (or absent from) the migration source — typically a rollback/downgrade where
	// a newer migration file was removed. Distinct from ErrMigrationsNotFound: here the
	// source is populated, but it does not contain the version the database is pinned to.
	ErrMigrationVersionAhead = errors.New("database version ahead of migration source")

	dbOpenFn = sql.Open

	createResolverFn = func(primaryDB, replicaDB *sql.DB, logger obs.Logger) (_ dbresolver.DB, err error) {
		defer func() {
			if recovered := recover(); recovered != nil {
				if logger == nil {
					logger = obs.Nop()
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
	}

	runMigrationsFn = runMigrations

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
	PrimaryDSN string
	ReplicaDSN string
	// DatabaseName is the logical database this client talks to, emitted as the
	// db.namespace metric label. Without it, pools from different databases (or
	// different tenants) share one label set and their pool gauges overwrite each
	// other, since those are asynchronous instruments. Optional: when empty, the
	// label is read off each pool's own DSN instead, and it is omitted only when
	// the DSN carries no database name either.
	DatabaseName       string
	Logger             obs.Logger
	MetricsRecorder    obs.MetricsRecorder
	MaxOpenConnections int
	MaxIdleConnections int
	ConnMaxLifetime    time.Duration
	ConnMaxIdleTime    time.Duration
}

func (c Config) withDefaults() Config {
	if c.Logger == nil {
		c.Logger = obs.Nop()
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

	return dsnKeywordValue(trimmed, "sslmode")
}

// dsnKeywordValue reads one keyword out of a libpq keyword/value DSN.
//
// Splitting on whitespace cannot read these DSNs: libpq allows spaces around
// "=", quoted values that contain spaces, and backslash escapes. A whitespace
// split truncates dbname='tenant alpha' to "tenant", and worse, it can pick a
// keyword out of the MIDDLE of another value -- password='p sslmode=require'
// then decides the TLS policy, and password='p dbname=x' decides a metric
// label. Scanning is the only correct read.
// https://www.postgresql.org/docs/current/libpq-connect.html
//
// Deliberate superset of libpq: double quotes are honoured like single quotes,
// because tooling emits dbname="x" and libpq would read the quotes as part of
// the name.
// dsnSpaceCutset is pgx's asciiSpace set (pgconn/config.go, v5.10.0).
const dsnSpaceCutset = " \t\n\r\v\f"

// dsnKeywordValue is a structural mirror of pgx v5.10.0's
// parseKeywordValueSettings (pgconn/config.go): key is everything up to the
// next "=" (trimmed, case-sensitive, so a malformed "host sslmode=require"
// yields the single key "host sslmode" and never a stray "require"); the LAST
// duplicate setting wins; only a single quote delimits a value; unescaping
// rewrites exactly `\\` and `\'`. Where pgx returns an error — no "=", empty
// key, trailing backslash, unterminated quote — this helper returns "" for
// the WHOLE string: pgx would refuse to connect at all, so no reading of a
// malformed DSN may ever satisfy the TLS policy or label a metric.
func dsnKeywordValue(dsn, keyword string) string {
	var found string

	s := strings.TrimLeft(dsn, dsnSpaceCutset)
	for len(s) > 0 {
		eq := strings.IndexByte(s, '=')
		if eq < 0 {
			return ""
		}

		key := strings.Trim(s[:eq], dsnSpaceCutset)
		if key == "" {
			return ""
		}

		s = strings.TrimLeft(s[eq+1:], dsnSpaceCutset)

		value, rest, ok := scanDSNValue(s)
		if !ok {
			return ""
		}

		s = rest

		if key == keyword {
			found = value
		}
	}

	return found
}

// scanDSNValue reads one value from the head of s and returns it unescaped,
// with the remaining input already left-trimmed. ok is false exactly where pgx
// errors: a trailing backslash, or an unterminated single quote.
func scanDSNValue(s string) (value, rest string, ok bool) {
	if len(s) == 0 {
		return "", "", true
	}

	if s[0] != '\'' {
		end := 0
		for ; end < len(s); end++ {
			if strings.IndexByte(dsnSpaceCutset, s[end]) >= 0 {
				break
			}

			if s[end] == '\\' {
				end++
				if end == len(s) {
					return "", "", false
				}
			}
		}

		return unescapeDSNValue(s[:end]), strings.TrimLeft(s[end:], dsnSpaceCutset), true
	}

	s = s[1:]

	end := 0
	for ; end < len(s); end++ {
		if s[end] == '\'' {
			break
		}

		if s[end] == '\\' {
			end++
		}
	}

	if end >= len(s) {
		return "", "", false
	}

	return unescapeDSNValue(s[:end]), strings.TrimLeft(s[end+1:], dsnSpaceCutset), true
}

// unescapeDSNValue applies pgx's exact unescape: `\\` -> `\` then `\'` -> `'`.
func unescapeDSNValue(v string) string {
	return strings.ReplaceAll(strings.ReplaceAll(v, `\\`, `\`), `\'`, `'`)
}

// dsnDatabaseName extracts the logical database name from a DSN, covering the
// same two forms as dsnSSLMode: the URL form (the path segment) and the
// key-value form (dbname=). It is used as the db.namespace metric label when
// Config.DatabaseName is not set.
//
// Best-effort by design: an unparseable or nameless DSN returns "", which simply
// omits the label. It never returns anything but the database name, so no
// credential from the DSN can reach a metric.
func dsnDatabaseName(dsn string) string {
	trimmed := strings.TrimSpace(dsn)

	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://") {
		parsed, err := url.Parse(trimmed)
		if err != nil {
			return ""
		}

		return strings.Trim(strings.TrimPrefix(parsed.Path, "/"), " '\"")
	}

	return dsnKeywordValue(trimmed, "dbname")
}

func enforceTLSPolicy(ctx context.Context, logger obs.Logger, label, dsn string) error {
	if strings.TrimSpace(dsn) == "" {
		return nil
	}

	if dsnRequiresTLS(dsn) {
		return nil
	}

	if !commons.AllowInsecureTLS() {
		return fmt.Errorf("postgres-%s: TLS required (set %s=true to bypass)", label, commons.EnvAllowInsecureTLS)
	}

	if logger != nil {
		logger.Log(ctx, obs.LevelWarn, "security bypass active",
			"feature", "postgres_tls",
			"dsn_label", label,
			"env_var", commons.EnvAllowInsecureTLS,
		)
	}

	return nil
}

// warnInsecureDSN logs a warning if the DSN does not guarantee TLS.
// This is advisory -- development environments commonly use sslmode=disable.
func warnInsecureDSN(ctx context.Context, logger obs.Logger, dsn, label string) {
	if logger == nil || !logger.Enabled(obs.LevelWarn) {
		return
	}

	if !dsnRequiresTLS(dsn) {
		logger.Log(ctx, obs.LevelWarn,
			"TLS is not guaranteed in database connection; production deployments should use sslmode=require or stronger",
			"dsn_label", label,
		)
	}
}

// connectBackoffCap is the maximum delay between lazy-connect retries.
const connectBackoffCap = 30 * time.Second

// connectionFailuresMetric defines the counter for postgres connection failures.
const (
	connectionFailuresMetricName        = "postgres_connection_failures_total"
	connectionFailuresMetricUnit        = "1"
	connectionFailuresMetricDescription = "Total number of postgres connection failures"
)

// Client is the v2 postgres connection manager.
type Client struct {
	mu              sync.RWMutex
	cfg             Config
	metricsRecorder obs.MetricsRecorder
	resolver        dbresolver.DB
	primary         *sql.DB
	replica         *sql.DB

	// statsCleanups releases the telemetry registrations bound to the CURRENT
	// primary/replica pools. Swapped together with the pools on reconnect and
	// drained on Close, so gauge callbacks never outlive the pool they observe.
	statsCleanups []sqlobs.CleanupFunc

	// Lazy-connect rate-limiting: prevents thundering-herd reconnect storms
	// when the database is down by enforcing exponential backoff between attempts.
	lastConnectAttempt time.Time
	connectAttempts    int
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

	return &Client{cfg: cfg, metricsRecorder: cfg.MetricsRecorder}, nil
}

// logAtLevel emits a structured log entry at the specified level.
func (c *Client) logAtLevel(ctx context.Context, level int, msg string, fields ...any) {
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
	built, err := c.buildConnection(ctx)
	if err != nil {
		return err
	}

	oldResolver := c.resolver
	oldPrimary := c.primary
	oldReplica := c.replica
	oldCleanups := c.statsCleanups

	c.resolver = built.resolver
	c.primary = built.primary
	c.replica = built.replica
	c.statsCleanups = built.cleanups

	// Release the metrics bound to the pools being replaced before closing them.
	c.releaseCleanups(ctx, oldCleanups)

	if oldResolver != nil {
		if err := oldResolver.Close(); err != nil {
			c.logAtLevel(ctx, obs.LevelWarn, "failed to close previous resolver after swap", "error", err)
		}
	}

	// Always close old primary/replica explicitly to prevent leaks.
	// The resolver may not own the underlying sql.DB connections.
	if err := closeDB(oldPrimary); err != nil {
		c.logAtLevel(ctx, obs.LevelWarn, "failed to close old primary during swap", "error", err)
	}

	if err := closeDB(oldReplica); err != nil {
		c.logAtLevel(ctx, obs.LevelWarn, "failed to close old replica during swap", "error", err)
	}

	c.logAtLevel(ctx, obs.LevelInfo, "connected to postgres")

	return nil
}

// pools is a freshly built connection set together with the telemetry
// registrations tied to it, so a failed build or a swap releases both together
// and a gauge can never outlive the pool it observes.
type pools struct {
	resolver dbresolver.DB
	primary  *sql.DB
	replica  *sql.DB
	cleanups []sqlobs.CleanupFunc
}

func (c *Client) buildConnection(ctx context.Context) (pools, error) {
	c.logAtLevel(ctx, obs.LevelInfo, "connecting to primary and replica databases")

	warnInsecureDSN(ctx, c.cfg.Logger, c.cfg.PrimaryDSN, "primary")
	warnInsecureDSN(ctx, c.cfg.Logger, c.cfg.ReplicaDSN, "replica")

	primary, primaryCleanup, err := c.newSQLDB(ctx, c.cfg.PrimaryDSN, sqlobs.PoolRolePrimary)
	if err != nil {
		return pools{}, fmt.Errorf("postgres connect: %w", err)
	}

	built := pools{primary: primary, cleanups: []sqlobs.CleanupFunc{primaryCleanup}}

	replica, replicaCleanup, err := c.newSQLDB(ctx, c.cfg.ReplicaDSN, sqlobs.PoolRoleReplica)
	if err != nil {
		c.releaseCleanups(ctx, built.cleanups)

		_ = closeDB(primary)

		return pools{}, fmt.Errorf("postgres connect: %w", err)
	}

	built.replica = replica
	built.cleanups = append(built.cleanups, replicaCleanup)

	resolver, err := createResolverFn(primary, replica, c.cfg.Logger)
	if err != nil {
		c.releaseCleanups(ctx, built.cleanups)

		_ = closeDB(primary)
		_ = closeDB(replica)

		c.logAtLevel(ctx, obs.LevelError, "failed to create resolver", "error", err)

		return pools{}, fmt.Errorf("postgres connect: failed to create resolver: %w", err)
	}

	built.resolver = resolver

	if err := resolver.PingContext(ctx); err != nil {
		c.releaseCleanups(ctx, built.cleanups)

		_ = resolver.Close()
		_ = closeDB(primary)
		_ = closeDB(replica)

		c.logAtLevel(ctx, obs.LevelError, "failed to ping database", "error", err)

		return pools{}, fmt.Errorf("postgres connect: failed to ping database: %w", err)
	}

	return built, nil
}

// newSQLDB opens a pool, instruments it, and applies the configured pool tuning
// — in that order: the instrumented handle is backed by its own pool, so the
// tuning must land on the handle actually kept (see instrumentPool).
func (c *Client) newSQLDB(
	ctx context.Context,
	dsn string,
	role sqlobs.PoolRole,
) (*sql.DB, sqlobs.CleanupFunc, error) {
	db, err := dbOpenFn("pgx", dsn)
	if err != nil {
		sanitized := newSanitizedError(err, "failed to open database")
		c.logAtLevel(ctx, obs.LevelError, "failed to open database", "error", sanitized)

		return nil, nil, sanitized
	}

	db, cleanup := c.instrumentPool(ctx, db, dsn, role)

	db.SetMaxOpenConns(c.cfg.MaxOpenConnections)
	db.SetMaxIdleConns(c.cfg.MaxIdleConnections)
	db.SetConnMaxLifetime(c.cfg.ConnMaxLifetime)
	db.SetConnMaxIdleTime(c.cfg.ConnMaxIdleTime)

	return db, cleanup, nil
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
	cleanups := c.statsCleanups

	c.resolver = nil
	c.primary = nil
	c.replica = nil
	c.statsCleanups = nil
	c.mu.Unlock()

	// Unregister the pool gauges before the pools they observe go away.
	c.releaseCleanups(context.Background(), cleanups)

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
	Logger                 obs.Logger
}

func (c MigrationConfig) withDefaults() MigrationConfig {
	if c.Logger == nil {
		c.Logger = obs.Nop()
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
	cfg MigrationConfig
}

// NewMigrator creates a migrator with explicit migration config.
func NewMigrator(cfg MigrationConfig) (*Migrator, error) {
	cfg = cfg.withDefaults()

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("postgres new_migrator: %w", err)
	}

	return &Migrator{cfg: cfg}, nil
}

func (m *Migrator) logAtLevel(ctx context.Context, level int, msg string, fields ...any) {
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

	db, err := dbOpenFn("pgx", m.cfg.PrimaryDSN)
	if err != nil {
		sanitized := newSanitizedError(err, "failed to open migration database")
		m.logAtLevel(ctx, obs.LevelError, "failed to open migration database", "error", sanitized)

		libOpentelemetry.HandleSpanError(span, "Failed to open migration database", sanitized)

		return fmt.Errorf("postgres migrate_up: %w", sanitized)
	}
	defer db.Close()

	migrationsPath, err := resolveMigrationsPath(m.cfg.MigrationsPath, m.cfg.Component)
	if err != nil {
		m.logAtLevel(ctx, obs.LevelError, "failed to resolve migration path", "error", err)

		libOpentelemetry.HandleSpanError(span, "Failed to resolve migration path", err)

		return fmt.Errorf("postgres migrate_up: %w", err)
	}

	if err := runMigrationsFn(ctx, db, migrationsPath, m.cfg.DatabaseName, m.cfg.AllowMultiStatements, m.cfg.AllowMissingMigrations, m.cfg.Logger); err != nil {
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
// Unwrap returns a fresh credential-free error carrying only the sanitized
// message text. The original error's TYPE and sentinel identity are
// intentionally NOT preserved: errors.Is / errors.As will not match the
// original cause. This is deliberate — preserving a connection error's
// concrete type would let callers reach the original error through Unwrap
// and re-expose the DSN and credentials it carries.
type SanitizedError struct {
	// Message is the credential-free error description.
	Message string
	// cause is a fresh error holding only the sanitized message text.
	// It does not preserve the original error's type or sentinel identity,
	// by design, to avoid leaking credentials through the error chain.
	cause error
}

func (e *SanitizedError) Error() string { return e.Message }

// Unwrap returns the credential-free cause. It carries only sanitized
// message text; the original error's type and sentinel identity are NOT
// recoverable through it (by design) — errors.Is / errors.As will not
// match the original cause. See the type doc for the rationale.
func (e *SanitizedError) Unwrap() error { return e.cause }

// sanitizedCause replaces the cause with a fresh credential-free error that
// carries only the sanitized message text. The original error's type and
// sentinel identity are intentionally NOT preserved (errors.Is / errors.As
// will not match the original), because keeping the original connection
// error reachable through Unwrap would re-expose the DSN and credentials.
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
	s = connectionStringCredentialsPattern.ReplaceAllString(s, "://"+constant.ObfuscatedValue+"@")
	s = connectionStringPasswordPattern.ReplaceAllString(s, "${1}"+constant.ObfuscatedValue)
	s = sslPathPattern.ReplaceAllString(s, "${1}="+constant.ObfuscatedValue)

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
	level   int
	message string
	fields  []any
}

// migrationState carries the database + source facts used to disambiguate an
// os.ErrNotExist from golang-migrate, which covers both an empty/missing source
// directory and a database pinned to a version the source no longer ships.
// hasVersion is false when the database has no schema_migrations row yet.
type migrationState struct {
	currentVersion uint
	hasVersion     bool
	sourceCount    int
	sourceMax      uint
	sourcePath     string
}

// migrationVersionReader is satisfied by *migrate.Migrate; the seam lets
// inspectMigrationState be unit-tested with a fake.
type migrationVersionReader interface {
	Version() (version uint, dirty bool, err error)
}

// migrationSourceStats scans the resolved migrations directory and reports how
// many up-migration files exist and the highest version present. A missing or
// unreadable directory yields (0, 0). Files that do not match the golang-migrate
// "<version>_<name>.up.sql" convention are ignored.
func migrationSourceStats(migrationsPath string) (count int, maxVersion uint) {
	entries, err := os.ReadDir(migrationsPath)
	if err != nil {
		return 0, 0
	}

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".up.sql") {
			continue
		}

		sep := strings.IndexByte(name, '_')
		if sep <= 0 {
			continue
		}

		// bitSize 0 = platform uint width, so values that would truncate on a
		// 32-bit build are rejected here instead of silently wrapping below.
		version, err := strconv.ParseUint(name[:sep], 10, 0)
		if err != nil {
			continue
		}

		count++

		if uint(version) > maxVersion {
			maxVersion = uint(version)
		}
	}

	return count, maxVersion
}

// inspectMigrationState gathers the DB version and source stats used to classify
// a failed migration. A nil reader or a Version() error leaves hasVersion false.
func inspectMigrationState(versions migrationVersionReader, migrationsPath string) migrationState {
	state := migrationState{sourcePath: migrationsPath}

	if versions != nil {
		if version, _, err := versions.Version(); err == nil {
			state.currentVersion = version
			state.hasVersion = true
		}
	}

	state.sourceCount, state.sourceMax = migrationSourceStats(migrationsPath)

	return state
}

// versionNotInSourceOutcome reports a database pinned to a version absent from a
// populated source, distinguishing "ahead of the newest migration" from a mid-range
// gap. Versions are logged as strings to avoid a lossy uint->int narrowing.
func versionNotInSourceOutcome(state migrationState) migrationOutcome {
	var cause string
	if state.currentVersion > state.sourceMax {
		cause = fmt.Sprintf("the database is ahead of the bundled migrations (source ships up to version %d)", state.sourceMax)
	} else {
		cause = fmt.Sprintf("version %d is absent from the migration source (a gap — the file was likely removed while higher versions, up to %d, remain)",
			state.currentVersion, state.sourceMax)
	}

	return migrationOutcome{
		err: fmt.Errorf("%w: database is pinned to version %d, which is not present in the migration source (%s); %s; "+
			"reconcile schema_migrations or restore the missing migration file(s)",
			ErrMigrationVersionAhead, state.currentVersion, state.sourcePath, cause),
		level:   obs.LevelError,
		message: "database version not present in migration source",
		fields: []any{
			"db_version", strconv.FormatUint(uint64(state.currentVersion), 10),
			"source_max_version", strconv.FormatUint(uint64(state.sourceMax), 10),
			"source_file_count", state.sourceCount,
		},
	}
}

// classifyMigrationError converts a golang-migrate error into a typed outcome.
// Returns a zero-value outcome (err == nil) on success or benign cases (ErrNoChange).
// When allowMissing is true, ErrNotExist is treated as benign (nil error); otherwise
// it returns ErrMigrationsNotFound (empty source) or ErrMigrationVersionAhead (the
// database is pinned to a version not present in a populated source), using state to
// tell those two os.ErrNotExist cases apart.
func classifyMigrationError(err error, allowMissing bool, state migrationState) migrationOutcome {
	if err == nil {
		return migrationOutcome{}
	}

	if errors.Is(err, migrate.ErrNoChange) {
		return migrationOutcome{
			level:   obs.LevelInfo,
			message: "no new migrations found, skipping",
		}
	}

	// Must precede the ErrDirty branch below: only correct because migrate.ErrDirty
	// does not wrap os.ErrNotExist. Do not reorder.
	if errors.Is(err, os.ErrNotExist) {
		if allowMissing {
			return migrationOutcome{
				level:   obs.LevelWarn,
				message: "no migration files found, skipping (AllowMissingMigrations=true)",
			}
		}

		// Populated source + known DB version: not "missing/empty" — the DB is
		// pinned to a version the source lacks.
		if state.sourceCount > 0 && state.hasVersion {
			return versionNotInSourceOutcome(state)
		}

		// Populated source but the DB version couldn't be read: don't claim empty.
		if state.sourceCount > 0 {
			return migrationOutcome{
				err: fmt.Errorf("%w: migration source (%s) contains %d file(s) but the database version could not be determined",
					ErrMigrationsNotFound, state.sourcePath, state.sourceCount),
				level:   obs.LevelError,
				message: "migration source populated but database version unknown",
			}
		}

		return migrationOutcome{
			err:     fmt.Errorf("%w: source directory missing or empty", ErrMigrationsNotFound),
			level:   obs.LevelError,
			message: "no migration files found",
		}
	}

	var dirtyErr migrate.ErrDirty
	if errors.As(err, &dirtyErr) {
		return migrationOutcome{
			err:     fmt.Errorf("%w: database version %d", ErrMigrationDirty, dirtyErr.Version),
			level:   obs.LevelError,
			message: "migration failed with dirty version",
			fields:  []any{"dirty_version", dirtyErr.Version},
		}
	}

	return migrationOutcome{
		err:     fmt.Errorf("migration failed: %w", err),
		level:   obs.LevelError,
		message: "migration failed",
		fields:  []any{"error", err},
	}
}

// recordConnectionFailure increments the postgres connection failure counter.
// No-op when metricsRecorder is nil. ctx is used for metric recording and tracing.
func (c *Client) recordConnectionFailure(ctx context.Context, operation string) {
	if c == nil || c.metricsRecorder == nil {
		return
	}

	err := c.metricsRecorder.AddCounter(
		ctx,
		connectionFailuresMetricName,
		connectionFailuresMetricDescription,
		connectionFailuresMetricUnit,
		map[string]string{"operation": constant.SanitizeMetricLabel(operation)},
		1,
	)
	if err != nil {
		c.logAtLevel(ctx, obs.LevelWarn, "failed to record postgres metric", "error", err)
	}
}

// migrationLogAtLevel logs at the given level if logger is non-nil and the level is enabled.
// This eliminates repeated nil-check + level-check branches in migration helpers.
func migrationLogAtLevel(ctx context.Context, logger obs.Logger, level int, msg string, fields ...any) {
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
func closeMigration(ctx context.Context, mig *migrate.Migrate, logger obs.Logger) {
	sourceErr, dbErr := mig.Close()
	if sourceErr != nil {
		migrationLogAtLevel(ctx, logger, obs.LevelWarn, "failed to close migration source driver", "error", sourceErr)
	}

	if dbErr != nil {
		migrationLogAtLevel(ctx, logger, obs.LevelWarn, "failed to close migration database driver", "error", dbErr)
	}
}

func runMigrations(ctx context.Context, dbPrimary *sql.DB, migrationsPath, primaryDBName string, allowMultiStatements, allowMissingMigrations bool, logger obs.Logger) error {
	if err := validateDBName(primaryDBName); err != nil {
		migrationLogAtLevel(ctx, logger, obs.LevelError, "invalid primary database name", "error", err)

		return fmt.Errorf("migrations: %w", err)
	}

	primaryURL, err := resolveMigrationSource(migrationsPath)
	if err != nil {
		migrationLogAtLevel(ctx, logger, obs.LevelError, "failed to parse migrations url", "error", err)

		return err
	}

	mig, err := createMigrationInstance(dbPrimary, primaryURL.String(), primaryDBName, allowMultiStatements)
	if err != nil {
		migrationLogAtLevel(ctx, logger, obs.LevelError, err.Error())

		return err
	}

	defer closeMigration(ctx, mig, logger)

	if err := mig.Up(); err != nil {
		outcome := classifyMigrationError(err, allowMissingMigrations, inspectMigrationState(mig, migrationsPath))

		migrationLogAtLevel(ctx, logger, outcome.level, outcome.message, outcome.fields...)

		return outcome.err
	}

	return nil
}
