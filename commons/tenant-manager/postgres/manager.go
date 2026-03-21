// Package postgres provides multi-tenant PostgreSQL connection management.
// It fetches credentials from Tenant Manager and caches connections per tenant.
package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sync"
	"time"

	libCommons "github.com/LerianStudio/lib-commons/v4/commons"
	libLog "github.com/LerianStudio/lib-commons/v4/commons/log"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v4/commons/opentelemetry"
	libPostgres "github.com/LerianStudio/lib-commons/v4/commons/postgres"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/eviction"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/logcompat"
	"github.com/bxcodec/dbresolver/v2"
	_ "github.com/jackc/pgx/v5/stdlib"
	"go.opentelemetry.io/otel/trace"
)

// pingTimeout is the maximum duration for connection health check pings.
// Kept short to avoid blocking requests when a cached connection is stale.
const pingTimeout = 3 * time.Second

// defaultSettingsCheckInterval is the default interval between periodic
// connection pool settings revalidation checks. When a cached connection is
// returned by GetConnection and this interval has elapsed since the last check,
// fresh config is fetched from the Tenant Manager asynchronously.
const defaultSettingsCheckInterval = 30 * time.Second

// settingsRevalidationTimeout is the maximum duration for the HTTP call
// to the Tenant Manager during async settings revalidation.
const settingsRevalidationTimeout = 5 * time.Second

// IsolationMode constants define the tenant isolation strategies.
const (
	// IsolationModeIsolated indicates each tenant has a dedicated database.
	IsolationModeIsolated = "isolated"
	// IsolationModeSchema indicates tenants share a database but have separate schemas.
	IsolationModeSchema = "schema"
)

// fallbackMaxOpenConns is the default maximum number of open connections per tenant
// database pool. Used when per-tenant connectionSettings are absent from the Tenant
// Manager /connections response (i.e., the tenant has no explicit pool configuration),
// or when no Tenant Manager client is configured. Can be overridden per-manager via
// WithMaxOpenConns.
const fallbackMaxOpenConns = 25

// fallbackMaxIdleConns is the default maximum number of idle connections per tenant
// database pool. Used when per-tenant connectionSettings are absent from the Tenant
// Manager /connections response, or when no Tenant Manager client is configured.
// Can be overridden per-manager via WithMaxIdleConns.
const fallbackMaxIdleConns = 5

const defaultMaxAllowedOpenConns = 200

const defaultMaxAllowedIdleConns = 50

// defaultIdleTimeout is the default duration before a tenant connection becomes
// eligible for eviction. Connections accessed within this window are considered
// active and will not be evicted, allowing the pool to grow beyond maxConnections.
// Defined centrally in the eviction package; aliased here for local convenience.
var defaultIdleTimeout = eviction.DefaultIdleTimeout

// Manager manages PostgreSQL database connections per tenant.
// It fetches credentials from Tenant Manager and caches connections.
// Credentials are provided directly by the tenant-manager settings endpoint.
// When maxConnections is set (> 0), the manager uses LRU eviction with an idle
// timeout as a soft limit. Connections idle longer than the timeout are eligible
// for eviction when the pool exceeds maxConnections. If all connections are active
// (used within the idle timeout), the pool grows beyond the soft limit and
// naturally shrinks back as tenants become idle.
type Manager struct {
	client  *client.Client
	service string
	module  string
	logger  *logcompat.Logger

	mu          sync.RWMutex
	connections map[string]*PostgresConnection
	closed      bool

	maxOpenConns        int
	maxIdleConns        int
	maxAllowedOpenConns int
	maxAllowedIdleConns int
	maxConnections      int                  // soft limit for pool size (0 = unlimited)
	idleTimeout         time.Duration        // how long before a connection is eligible for eviction
	lastAccessed        map[string]time.Time // LRU tracking per tenant

	lastSettingsCheck     map[string]time.Time // tracks per-tenant last settings revalidation time
	settingsCheckInterval time.Duration        // configurable interval between settings revalidation checks

	// revalidateWG tracks in-flight revalidatePoolSettings goroutines so Close()
	// can wait for them to finish before returning. Without this, goroutines
	// spawned by GetConnection may access Manager state after Close() returns.
	revalidateWG sync.WaitGroup

	defaultConn *PostgresConnection
}

type PostgresConnection struct {
	// Adapter type used by tenant-manager package; keep fields aligned with
	// tenant-manager migration contract and upstream lib-commons adapter semantics.
	ConnectionStringPrimary string         `json:"-"` // contains credentials, must not be serialized
	ConnectionStringReplica string         `json:"-"` // contains credentials, must not be serialized
	PrimaryDBName           string         `json:"primaryDBName,omitempty"`
	ReplicaDBName           string         `json:"replicaDBName,omitempty"`
	MaxOpenConnections      int            `json:"maxOpenConnections,omitempty"`
	MaxIdleConnections      int            `json:"maxIdleConnections,omitempty"`
	SkipMigrations          bool           `json:"skipMigrations,omitempty"`
	Logger                  libLog.Logger  `json:"-"`
	ConnectionDB            *dbresolver.DB `json:"-"`

	client *libPostgres.Client
}

func (c *PostgresConnection) Connect(ctx context.Context) error {
	if c == nil {
		return errors.New("postgres connection is nil")
	}

	pgClient, err := libPostgres.New(libPostgres.Config{
		PrimaryDSN:         c.ConnectionStringPrimary,
		ReplicaDSN:         c.ConnectionStringReplica,
		Logger:             c.Logger,
		MaxOpenConnections: c.MaxOpenConnections,
		MaxIdleConnections: c.MaxIdleConnections,
	})
	if err != nil {
		return err
	}

	if err := pgClient.Connect(ctx); err != nil {
		return err
	}

	resolver, err := pgClient.Resolver(ctx)
	if err != nil {
		return err
	}

	c.client = pgClient
	c.ConnectionDB = &resolver

	return nil
}

func (c *PostgresConnection) GetDB() (dbresolver.DB, error) {
	if c == nil || c.ConnectionDB == nil {
		return nil, errors.New("postgres resolver not initialized")
	}

	return *c.ConnectionDB, nil
}

// Stats contains statistics for the Manager.
type Stats struct {
	TotalConnections  int      `json:"totalConnections"`
	ActiveConnections int      `json:"activeConnections"`
	MaxConnections    int      `json:"maxConnections"`
	TenantIDs         []string `json:"tenantIds"`
	Closed            bool     `json:"closed"`
}

// Option configures a Manager.
type Option func(*Manager)

// WithLogger sets the logger for the Manager.
func WithLogger(logger libLog.Logger) Option {
	return func(p *Manager) {
		p.logger = logcompat.New(logger)
	}
}

// WithMaxOpenConns sets max open connections per tenant.
func WithMaxOpenConns(n int) Option {
	return func(p *Manager) {
		p.maxOpenConns = n
	}
}

// WithMaxIdleConns sets max idle connections per tenant.
func WithMaxIdleConns(n int) Option {
	return func(p *Manager) {
		p.maxIdleConns = n
	}
}

// WithConnectionLimitCaps sets hard maximums for per-tenant pool settings
// received from Tenant Manager.
func WithConnectionLimitCaps(maxOpen, maxIdle int) Option {
	return func(p *Manager) {
		if maxOpen > 0 {
			p.maxAllowedOpenConns = maxOpen
		}

		if maxIdle > 0 {
			p.maxAllowedIdleConns = maxIdle
		}
	}
}

// WithModule sets the module name for the Manager (e.g., "onboarding", "transaction").
func WithModule(module string) Option {
	return func(p *Manager) {
		p.module = module
	}
}

// WithMaxTenantPools sets the soft limit for the number of tenant connections in the pool.
// When the pool reaches this limit and a new tenant needs a connection, only connections
// that have been idle longer than the idle timeout are eligible for eviction. If all
// connections are active (used within the idle timeout), the pool grows beyond this limit.
// A value of 0 (default) means unlimited.
func WithMaxTenantPools(maxSize int) Option {
	return func(p *Manager) {
		p.maxConnections = maxSize
	}
}

// WithSettingsCheckInterval sets the interval between periodic connection pool settings
// revalidation checks. When GetConnection returns a cached connection and this interval
// has elapsed since the last check for that tenant, fresh config is fetched from the
// Tenant Manager asynchronously and pool settings are updated without recreating the connection.
//
// If d <= 0, revalidation is DISABLED (settingsCheckInterval is set to 0).
// When disabled, no async revalidation checks are performed on cache hits.
// Default: 30 seconds (defaultSettingsCheckInterval).
func WithSettingsCheckInterval(d time.Duration) Option {
	return func(p *Manager) {
		p.settingsCheckInterval = max(d, 0)
	}
}

// WithIdleTimeout sets the duration after which an unused tenant connection becomes
// eligible for eviction. Only connections idle longer than this duration will be
// evicted when the pool exceeds the soft limit (maxConnections). If all connections
// are active (used within the idle timeout), the pool is allowed to grow beyond the
// soft limit and naturally shrinks back as tenants become idle.
// Default: 5 minutes.
func WithIdleTimeout(d time.Duration) Option {
	return func(p *Manager) {
		p.idleTimeout = d
	}
}

// NewManager creates a new PostgreSQL connection manager.
func NewManager(c *client.Client, service string, opts ...Option) *Manager {
	p := &Manager{
		client:                c,
		service:               service,
		logger:                logcompat.New(nil),
		connections:           make(map[string]*PostgresConnection),
		lastAccessed:          make(map[string]time.Time),
		lastSettingsCheck:     make(map[string]time.Time),
		settingsCheckInterval: defaultSettingsCheckInterval,
		maxOpenConns:          fallbackMaxOpenConns,
		maxIdleConns:          fallbackMaxIdleConns,
		maxAllowedOpenConns:   defaultMaxAllowedOpenConns,
		maxAllowedIdleConns:   defaultMaxAllowedIdleConns,
	}

	for _, opt := range opts {
		opt(p)
	}

	return p
}

// GetConnection returns a database connection for the tenant.
// Creates a new connection if one doesn't exist.
// If a cached connection fails a health check (e.g., due to credential rotation
// after a tenant purge+re-associate), the stale connection is evicted and a new
// one is created with fresh credentials from the Tenant Manager.
func (p *Manager) GetConnection(ctx context.Context, tenantID string) (*PostgresConnection, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	if tenantID == "" {
		return nil, errors.New("tenant ID is required")
	}

	p.mu.RLock()

	if p.closed {
		p.mu.RUnlock()
		return nil, core.ErrManagerClosed
	}

	if conn, ok := p.connections[tenantID]; ok {
		p.mu.RUnlock()

		// Validate cached connection is still healthy (e.g., credentials may have changed)
		if conn.ConnectionDB != nil {
			pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)

			pingErr := (*conn.ConnectionDB).PingContext(pingCtx)

			cancel() // Release timer immediately; we no longer need the ping context.

			if pingErr != nil {
				if p.logger != nil {
					p.logger.WarnCtx(ctx, fmt.Sprintf("cached postgres connection unhealthy for tenant %s, reconnecting: %v", tenantID, pingErr))
				}

				if closeErr := p.CloseConnection(ctx, tenantID); closeErr != nil && p.logger != nil {
					p.logger.WarnCtx(ctx, fmt.Sprintf("failed to close stale postgres connection for tenant %s: %v", tenantID, closeErr))
				}

				// Fall through to create a new connection with fresh credentials
				return p.createConnection(ctx, tenantID)
			}
		}

		// Update LRU tracking on cache hit and check if settings revalidation is due
		now := time.Now()

		p.mu.Lock()

		// TOCTOU re-check: connection may have been evicted while we were pinging.
		if _, stillExists := p.connections[tenantID]; !stillExists {
			p.mu.Unlock()
			// Connection was evicted while we were pinging; create fresh.
			return p.createConnection(ctx, tenantID)
		}

		p.lastAccessed[tenantID] = now

		// Only revalidate if settingsCheckInterval > 0 (means revalidation is enabled)
		shouldRevalidate := p.client != nil && p.settingsCheckInterval > 0 && time.Since(p.lastSettingsCheck[tenantID]) > p.settingsCheckInterval
		if shouldRevalidate {
			// Update timestamp BEFORE spawning goroutine to prevent multiple
			// concurrent revalidation checks for the same tenant.
			p.lastSettingsCheck[tenantID] = now
		}

		p.mu.Unlock()

		if shouldRevalidate {
			p.revalidateWG.Go(func() { //#nosec G118 -- intentional: revalidatePoolSettings creates its own timeout context; must not use request-scoped context as this outlives the request
				p.revalidatePoolSettings(tenantID)
			})
		}

		return conn, nil
	}

	p.mu.RUnlock()

	return p.createConnection(ctx, tenantID)
}

// revalidatePoolSettings fetches fresh config from the Tenant Manager and applies
// updated connection pool settings to the cached connection for the given tenant.
// This runs asynchronously (in a goroutine) and must never block GetConnection.
// If the fetch fails, a warning is logged but the connection remains usable.
func (p *Manager) revalidatePoolSettings(tenantID string) {
	// Guard: recover from any panic to avoid crashing the process.
	// This goroutine runs asynchronously and must never bring down the service.
	defer func() {
		if r := recover(); r != nil {
			if p.logger != nil {
				p.logger.Warnf("recovered from panic during settings revalidation for tenant %s: %v", tenantID, r)
			}
		}
	}()

	revalidateCtx, cancel := context.WithTimeout(context.Background(), settingsRevalidationTimeout)
	defer cancel()

	config, err := p.client.GetTenantConfig(revalidateCtx, tenantID, p.service, client.WithSkipCache())
	if err != nil {
		// If tenant service was suspended/purged, evict the cached connection immediately.
		// The next request for this tenant will call createConnection, which fetches fresh
		// config from the Tenant Manager and receives the 403 error directly.
		if core.IsTenantSuspendedError(err) {
			if p.logger != nil {
				p.logger.Warnf("tenant %s service suspended, evicting cached connection", tenantID)
			}

			_ = p.CloseConnection(context.Background(), tenantID)

			return
		}

		if p.logger != nil {
			p.logger.Warnf("failed to revalidate connection settings for tenant %s: %v", tenantID, err)
		}

		return
	}

	p.ApplyConnectionSettings(tenantID, config)
}

// createConnection fetches config from Tenant Manager and creates a connection.
func (p *Manager) createConnection(ctx context.Context, tenantID string) (*PostgresConnection, error) {
	if p.client == nil {
		return nil, errors.New("tenant manager client is required for multi-tenant connections")
	}

	baseLogger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)
	logger := logcompat.New(baseLogger)

	ctx, span := tracer.Start(ctx, "postgres.create_connection")
	defer span.End()

	p.mu.Lock()
	if cachedConn, ok := p.tryReuseOrEvictCachedConnectionLocked(ctx, tenantID, logger); ok {
		p.mu.Unlock()

		return cachedConn, nil
	}

	if p.closed {
		p.mu.Unlock()

		return nil, core.ErrManagerClosed
	}

	p.mu.Unlock()

	config, pgConfig, err := p.getPostgresConfigForTenant(ctx, tenantID, logger, span)
	if err != nil {
		return nil, err
	}

	conn, err := p.buildTenantPostgresConnection(ctx, tenantID, config, pgConfig, logger, span)
	if err != nil {
		return nil, err
	}

	return p.cacheConnection(ctx, tenantID, conn, logger, config.IsolationMode)
}

func (p *Manager) tryReuseOrEvictCachedConnectionLocked(
	ctx context.Context,
	tenantID string,
	logger *logcompat.Logger,
) (*PostgresConnection, bool) {
	conn, ok := p.connections[tenantID]
	if !ok {
		return nil, false
	}

	if conn != nil && conn.ConnectionDB != nil {
		pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
		pingErr := (*conn.ConnectionDB).PingContext(pingCtx)

		cancel()

		if pingErr == nil {
			return conn, true
		}

		logger.WarnCtx(ctx, fmt.Sprintf("cached postgres connection unhealthy for tenant %s after lock, reconnecting: %v", tenantID, pingErr))

		_ = (*conn.ConnectionDB).Close()
	}

	delete(p.connections, tenantID)
	delete(p.lastAccessed, tenantID)
	delete(p.lastSettingsCheck, tenantID)

	return nil, false
}

func (p *Manager) getPostgresConfigForTenant(
	ctx context.Context,
	tenantID string,
	logger *logcompat.Logger,
	span trace.Span,
) (*core.TenantConfig, *core.PostgreSQLConfig, error) {
	config, err := p.client.GetTenantConfig(ctx, tenantID, p.service)
	if err != nil {
		var suspErr *core.TenantSuspendedError
		if errors.As(err, &suspErr) {
			logger.WarnCtx(ctx, fmt.Sprintf("tenant service is %s: tenantID=%s", suspErr.Status, tenantID))
			libOpentelemetry.HandleSpanBusinessErrorEvent(span, "tenant service suspended", err)

			return nil, nil, err
		}

		logger.ErrorCtx(ctx, fmt.Sprintf("failed to get tenant config: %v", err))
		libOpentelemetry.HandleSpanError(span, "failed to get tenant config", err)

		return nil, nil, fmt.Errorf("failed to get tenant config: %w", err)
	}

	pgConfig := config.GetPostgreSQLConfig(p.service, p.module)
	if pgConfig == nil {
		logger.ErrorCtx(ctx, fmt.Sprintf("no PostgreSQL config for tenant %s service %s module %s", tenantID, p.service, p.module))

		return nil, nil, core.ErrServiceNotConfigured
	}

	return config, pgConfig, nil
}

func (p *Manager) buildTenantPostgresConnection(
	ctx context.Context,
	tenantID string,
	config *core.TenantConfig,
	pgConfig *core.PostgreSQLConfig,
	logger *logcompat.Logger,
	span trace.Span,
) (*PostgresConnection, error) {
	primaryConnStr, err := buildConnectionString(pgConfig)
	if err != nil {
		logger.ErrorCtx(ctx, fmt.Sprintf("invalid connection string for tenant %s: %v", tenantID, err))
		libOpentelemetry.HandleSpanError(span, "invalid connection string", err)

		return nil, fmt.Errorf("invalid connection string for tenant %s: %w", tenantID, err)
	}

	replicaConnStr, replicaDBName, err := p.resolveReplicaConnection(config, pgConfig, primaryConnStr, tenantID, logger)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "invalid replica connection string", err)

		return nil, fmt.Errorf("invalid replica connection string for tenant %s: %w", tenantID, err)
	}

	maxOpen, maxIdle := p.resolveConnectionPoolSettings(config, tenantID, logger)

	conn := &PostgresConnection{
		ConnectionStringPrimary: primaryConnStr,
		ConnectionStringReplica: replicaConnStr,
		PrimaryDBName:           pgConfig.Database,
		ReplicaDBName:           replicaDBName,
		MaxOpenConnections:      maxOpen,
		MaxIdleConnections:      maxIdle,
		SkipMigrations:          p.IsMultiTenant(),
	}

	if p.logger != nil {
		conn.Logger = p.logger.Base()
	}

	if config.IsSchemaMode() && pgConfig.Schema == "" {
		logger.ErrorCtx(ctx, "schema mode requires schema in config for tenant "+tenantID)

		return nil, fmt.Errorf("schema mode requires schema in config for tenant %s", tenantID)
	}

	if err := conn.Connect(ctx); err != nil {
		logger.ErrorCtx(ctx, fmt.Sprintf("failed to connect to tenant database: %v", err))
		libOpentelemetry.HandleSpanError(span, "failed to connect", err)

		return nil, fmt.Errorf("failed to connect to tenant database: %w", err)
	}

	if pgConfig.Schema != "" {
		logger.InfoCtx(ctx, fmt.Sprintf("connection configured with search_path=%s for tenant %s (mode: %s)", pgConfig.Schema, tenantID, config.IsolationMode))
	}

	return conn, nil
}

func (p *Manager) cacheConnection(
	ctx context.Context,
	tenantID string,
	conn *PostgresConnection,
	logger *logcompat.Logger,
	isolationMode string,
) (*PostgresConnection, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.closed {
		if conn.ConnectionDB != nil {
			_ = (*conn.ConnectionDB).Close()
		}

		return nil, core.ErrManagerClosed
	}

	if cached, ok := p.connections[tenantID]; ok && cached != nil && cached.ConnectionDB != nil {
		if conn.ConnectionDB != nil {
			_ = (*conn.ConnectionDB).Close()
		}

		p.lastAccessed[tenantID] = time.Now()

		return cached, nil
	}

	p.evictLRU(ctx, logger.Base())

	p.connections[tenantID] = conn
	p.lastAccessed[tenantID] = time.Now()

	logger.InfoCtx(ctx, fmt.Sprintf("created connection for tenant %s (mode: %s)", tenantID, isolationMode))

	return conn, nil
}

// resolveReplicaConnection resolves the replica connection string and database name.
// If a dedicated replica config exists for the service/module, it builds a separate
// connection string; otherwise it falls back to the primary connection string and database.
func (p *Manager) resolveReplicaConnection(
	config *core.TenantConfig,
	pgConfig *core.PostgreSQLConfig,
	primaryConnStr string,
	tenantID string,
	logger *logcompat.Logger,
) (connStr string, dbName string, err error) {
	pgReplicaConfig := config.GetPostgreSQLReplicaConfig(p.service, p.module)
	if pgReplicaConfig == nil {
		return primaryConnStr, pgConfig.Database, nil
	}

	replicaConnStr, buildErr := buildConnectionString(pgReplicaConfig)
	if buildErr != nil {
		logger.Errorf("invalid replica connection string for tenant %s: %v", tenantID, buildErr)
		return "", "", buildErr
	}

	logger.Infof("using separate replica connection for tenant %s (replica host: %s)", tenantID, pgReplicaConfig.Host)

	return replicaConnStr, pgReplicaConfig.Database, nil
}

// resolveConnectionSettingsFromConfig extracts connection settings from the tenant config,
// checking module-level settings first, then top-level for backward compatibility.
func (p *Manager) resolveConnectionSettingsFromConfig(config *core.TenantConfig) *core.ConnectionSettings {
	if config == nil {
		return nil
	}

	if p.module != "" && config.Databases != nil {
		if db, ok := config.Databases[p.module]; ok && db.ConnectionSettings != nil {
			return db.ConnectionSettings
		}
	}

	return config.ConnectionSettings
}

// clampPoolSettings enforces connection pool limits set by WithConnectionLimitCaps.
func (p *Manager) clampPoolSettings(maxOpen, maxIdle int, tenantID string, logger *logcompat.Logger) (int, int) {
	if p.maxAllowedOpenConns > 0 && maxOpen > p.maxAllowedOpenConns {
		if logger != nil {
			logger.Warnf("clamping maxOpenConns for tenant %s module %s from %d to %d", tenantID, p.module, maxOpen, p.maxAllowedOpenConns)
		}

		maxOpen = p.maxAllowedOpenConns
	}

	if p.maxAllowedIdleConns > 0 && maxIdle > p.maxAllowedIdleConns {
		if logger != nil {
			logger.Warnf("clamping maxIdleConns for tenant %s module %s from %d to %d", tenantID, p.module, maxIdle, p.maxAllowedIdleConns)
		}

		maxIdle = p.maxAllowedIdleConns
	}

	return maxOpen, maxIdle
}

// resolveConnectionPoolSettings determines the effective maxOpen and maxIdle connection
// settings for a tenant. It checks module-level settings first (new format), then falls
// back to top-level settings (legacy), and finally uses global defaults.
func (p *Manager) resolveConnectionPoolSettings(config *core.TenantConfig, tenantID string, logger *logcompat.Logger) (maxOpen, maxIdle int) {
	maxOpen = p.maxOpenConns
	maxIdle = p.maxIdleConns

	connSettings := p.resolveConnectionSettingsFromConfig(config)

	if connSettings != nil {
		if connSettings.MaxOpenConns > 0 {
			maxOpen = connSettings.MaxOpenConns
			logger.Infof("applying per-module maxOpenConns=%d for tenant %s module %s (global default: %d)", maxOpen, tenantID, p.module, p.maxOpenConns)
		} else {
			// connectionSettings present but MaxOpenConns is zero: restore manager default
			maxOpen = p.maxOpenConns
		}

		if connSettings.MaxIdleConns > 0 {
			maxIdle = connSettings.MaxIdleConns
			logger.Infof("applying per-module maxIdleConns=%d for tenant %s module %s (global default: %d)", maxIdle, tenantID, p.module, p.maxIdleConns)
		} else {
			// connectionSettings present but MaxIdleConns is zero: restore manager default
			maxIdle = p.maxIdleConns
		}
	}

	maxOpen, maxIdle = p.clampPoolSettings(maxOpen, maxIdle, tenantID, logger)

	return maxOpen, maxIdle
}

// evictLRU removes the least recently used idle connection when the pool reaches the
// soft limit. Only connections that have been idle longer than the idle timeout are
// eligible for eviction. If all connections are active (used within the idle timeout),
// the pool is allowed to grow beyond the soft limit.
// Caller MUST hold p.mu write lock.
func (p *Manager) evictLRU(_ context.Context, logger libLog.Logger) {
	candidateID, shouldEvict := eviction.FindLRUEvictionCandidate(
		len(p.connections), p.maxConnections, p.lastAccessed, p.idleTimeout, logger,
	)
	if !shouldEvict {
		return
	}

	// Manager-specific cleanup: close the postgres connection and remove from all maps.
	if conn, ok := p.connections[candidateID]; ok {
		if conn.ConnectionDB != nil {
			_ = (*conn.ConnectionDB).Close()
		}

		delete(p.connections, candidateID)
		delete(p.lastAccessed, candidateID)
		delete(p.lastSettingsCheck, candidateID)
	}
}

// GetDB returns a dbresolver.DB for the tenant.
func (p *Manager) GetDB(ctx context.Context, tenantID string) (dbresolver.DB, error) {
	conn, err := p.GetConnection(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	return conn.GetDB()
}

// Close closes all connections and marks the manager as closed.
// It waits for any in-flight revalidatePoolSettings goroutines to finish
// before returning, preventing goroutine leaks and use-after-close races.
func (p *Manager) Close(_ context.Context) error {
	// Phase 1: Under lock, mark closed and close all connections.
	p.mu.Lock()

	p.closed = true

	var errs []error

	for tenantID, conn := range p.connections {
		if conn.ConnectionDB != nil {
			if err := (*conn.ConnectionDB).Close(); err != nil {
				errs = append(errs, err)
			}
		}

		delete(p.connections, tenantID)
		delete(p.lastAccessed, tenantID)
		delete(p.lastSettingsCheck, tenantID)
	}

	p.mu.Unlock()

	// Phase 2: Wait for in-flight revalidatePoolSettings goroutines OUTSIDE the lock.
	// revalidatePoolSettings acquires p.mu internally (via CloseConnection and
	// ApplyConnectionSettings), so waiting with the lock held would deadlock.
	p.revalidateWG.Wait()

	return errors.Join(errs...)
}

// CloseConnection closes the connection for a specific tenant.
func (p *Manager) CloseConnection(_ context.Context, tenantID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	conn, ok := p.connections[tenantID]
	if !ok {
		return nil
	}

	var err error
	if conn.ConnectionDB != nil {
		err = (*conn.ConnectionDB).Close()
	}

	delete(p.connections, tenantID)
	delete(p.lastAccessed, tenantID)
	delete(p.lastSettingsCheck, tenantID)

	return err
}

// Stats returns connection statistics.
func (p *Manager) Stats() Stats {
	p.mu.RLock()
	defer p.mu.RUnlock()

	tenantIDs := make([]string, 0, len(p.connections))
	for id := range p.connections {
		tenantIDs = append(tenantIDs, id)
	}

	totalConns := len(p.connections)

	now := time.Now()

	idleTimeout := p.idleTimeout
	if idleTimeout == 0 {
		idleTimeout = defaultIdleTimeout
	}

	activeCount := 0

	for id := range p.connections {
		if t, ok := p.lastAccessed[id]; ok && now.Sub(t) < idleTimeout {
			activeCount++
		}
	}

	return Stats{
		TotalConnections:  totalConns,
		ActiveConnections: activeCount,
		MaxConnections:    p.maxConnections,
		TenantIDs:         tenantIDs,
		Closed:            p.closed,
	}
}

// validSchemaPattern validates PostgreSQL schema names to prevent injection
// in the options=-csearch_path= connection string parameter.
var validSchemaPattern = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func buildConnectionString(cfg *core.PostgreSQLConfig) (string, error) {
	if cfg == nil {
		return "", fmt.Errorf("postgres.buildConnectionString: %w", core.ErrNilConfig)
	}

	sslmode := cfg.SSLMode
	if sslmode == "" {
		// Default is "disable" for local development compatibility.
		// Production deployments should set SSLMode explicitly in PostgreSQLConfig.
		sslmode = "disable"
	}

	// Reject contradictory configuration: SSL is disabled but certificate
	// paths are provided. This likely indicates a misconfiguration that would
	// silently ignore the supplied certificates.
	if sslmode == "disable" && (cfg.SSLRootCert != "" || cfg.SSLCert != "" || cfg.SSLKey != "") {
		return "", fmt.Errorf("sslmode is %q but SSL certificate parameters are set (sslrootcert=%q, sslcert=%q, sslkey=%q); "+
			"either remove the certificate paths or use a TLS-enabled sslmode", sslmode, cfg.SSLRootCert, cfg.SSLCert, cfg.SSLKey)
	}

	connURL := &url.URL{
		Scheme: "postgres",
		Host:   fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		Path:   "/" + cfg.Database,
	}

	if cfg.Username != "" {
		connURL.User = url.UserPassword(cfg.Username, cfg.Password)
	}

	values := url.Values{}
	values.Set("sslmode", sslmode)

	if cfg.SSLRootCert != "" {
		values.Set("sslrootcert", cfg.SSLRootCert)
	}

	if cfg.SSLCert != "" {
		values.Set("sslcert", cfg.SSLCert)
	}

	if cfg.SSLKey != "" {
		values.Set("sslkey", cfg.SSLKey)
	}

	if cfg.Schema != "" {
		if !validSchemaPattern.MatchString(cfg.Schema) {
			return "", fmt.Errorf("invalid schema name %q: must match %s", cfg.Schema, validSchemaPattern.String())
		}

		values.Set("options", "-csearch_path="+cfg.Schema)
	}

	connURL.RawQuery = values.Encode()

	return connURL.String(), nil
}

// ApplyConnectionSettings applies updated connection pool settings to an existing
// cached connection for the given tenant without recreating the connection.
// This is called during the sync loop to revalidate settings that may have changed
// in the Tenant Manager (e.g., maxOpenConns adjusted from 10 to 30).
//
// Go's sql.DB.SetMaxOpenConns and SetMaxIdleConns are thread-safe and take effect
// immediately for new connections from the pool. Existing idle connections above the
// new limit are closed gradually.
//
// For MongoDB, the driver does not support changing pool size after client creation,
// so this method only applies to PostgreSQL connections.
func (p *Manager) ApplyConnectionSettings(tenantID string, config *core.TenantConfig) {
	p.mu.RLock()

	conn, ok := p.connections[tenantID]
	if !ok || conn == nil || conn.ConnectionDB == nil {
		p.mu.RUnlock()
		return // no cached connection, settings will be applied on next creation
	}

	connSettings := p.resolveConnectionSettingsFromConfig(config)

	// Determine effective settings: per-tenant if present, otherwise manager defaults.
	var maxOpen, maxIdle int
	if connSettings != nil {
		maxOpen = connSettings.MaxOpenConns
		maxIdle = connSettings.MaxIdleConns
	}

	// Fallback to manager defaults for absent/zero values
	if maxOpen <= 0 {
		maxOpen = p.maxOpenConns
	}

	if maxIdle <= 0 {
		maxIdle = p.maxIdleConns
	}

	db := *conn.ConnectionDB

	p.mu.RUnlock() // Release before thread-safe sql.DB operations

	compatLogger := logcompat.New(p.logger.Base())
	maxOpen, maxIdle = p.clampPoolSettings(maxOpen, maxIdle, tenantID, compatLogger)

	compatLogger.Infof("applying connection settings for tenant %s module %s: maxOpenConns=%d, maxIdleConns=%d",
		tenantID, p.module, maxOpen, maxIdle)

	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)
}

// WithConnectionLimits sets the default per-tenant connection limits.
func WithConnectionLimits(maxOpen, maxIdle int) Option {
	return func(p *Manager) {
		p.maxOpenConns = maxOpen
		p.maxIdleConns = maxIdle
	}
}

// WithDefaultConnection sets a default connection used in single-tenant mode.
func WithDefaultConnection(conn *PostgresConnection) Option {
	return func(p *Manager) {
		p.defaultConn = conn
	}
}

// Deprecated: prefer NewManager(..., WithConnectionLimits(...)).
func (p *Manager) WithConnectionLimits(maxOpen, maxIdle int) *Manager {
	WithConnectionLimits(maxOpen, maxIdle)(p)
	return p
}

// Deprecated: prefer NewManager(..., WithDefaultConnection(...)).
func (p *Manager) WithDefaultConnection(conn *PostgresConnection) *Manager {
	WithDefaultConnection(conn)(p)
	return p
}

// GetDefaultConnection returns the default connection configured for single-tenant mode.
func (p *Manager) GetDefaultConnection() *PostgresConnection {
	return p.defaultConn
}

// IsMultiTenant returns true if the manager is configured with a Tenant Manager client.
func (p *Manager) IsMultiTenant() bool {
	return p.client != nil
}

// CreateDirectConnection creates a direct database connection from config.
// Useful when you have config but don't need full connection management.
// Returns an error if cfg is nil.
func CreateDirectConnection(ctx context.Context, cfg *core.PostgreSQLConfig) (*sql.DB, error) {
	if cfg == nil {
		return nil, fmt.Errorf("postgres.CreateDirectConnection: %w", core.ErrNilConfig)
	}

	connStr, err := buildConnectionString(cfg)
	if err != nil {
		return nil, fmt.Errorf("invalid connection config: %w", err)
	}

	db, err := sql.Open("pgx", connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to open connection: %w", err)
	}

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return db, nil
}
