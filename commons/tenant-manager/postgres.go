package tenantmanager

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	libCommons "github.com/LerianStudio/lib-commons/v3/commons"
	libLog "github.com/LerianStudio/lib-commons/v3/commons/log"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v3/commons/opentelemetry"
	libPostgres "github.com/LerianStudio/lib-commons/v3/commons/postgres"
	"github.com/bxcodec/dbresolver/v2"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// pingTimeout is the maximum duration for connection health check pings.
// Kept short to avoid blocking requests when a cached connection is stale.
const pingTimeout = 3 * time.Second

// IsolationMode constants define the tenant isolation strategies.
const (
	// IsolationModeIsolated indicates each tenant has a dedicated database.
	IsolationModeIsolated = "isolated"
	// IsolationModeSchema indicates tenants share a database but have separate schemas.
	IsolationModeSchema = "schema"
)

// defaultIdleTimeout is the default duration before a tenant connection becomes
// eligible for eviction. Connections accessed within this window are considered
// active and will not be evicted, allowing the pool to grow beyond maxConnections.
const defaultIdleTimeout = 5 * time.Minute

// PostgresManager manages PostgreSQL database connections per tenant.
// It fetches credentials from Tenant Manager and caches connections.
// Credentials are provided directly by the tenant-manager settings endpoint.
// When maxConnections is set (> 0), the manager uses LRU eviction with an idle
// timeout as a soft limit. Connections idle longer than the timeout are eligible
// for eviction when the pool exceeds maxConnections. If all connections are active
// (used within the idle timeout), the pool grows beyond the soft limit and
// naturally shrinks back as tenants become idle.
type PostgresManager struct {
	client  *Client
	service string
	module  string
	logger  libLog.Logger

	mu          sync.RWMutex
	connections map[string]*libPostgres.PostgresConnection
	closed      bool

	maxOpenConns   int
	maxIdleConns   int
	maxConnections int                  // soft limit for pool size (0 = unlimited)
	idleTimeout    time.Duration        // how long before a connection is eligible for eviction
	lastAccessed   map[string]time.Time // LRU tracking per tenant

	defaultConn *libPostgres.PostgresConnection
}

// PostgresOption configures a PostgresManager.
type PostgresOption func(*PostgresManager)

// WithPostgresLogger sets the logger for the PostgresManager.
func WithPostgresLogger(logger libLog.Logger) PostgresOption {
	return func(p *PostgresManager) {
		p.logger = logger
	}
}

// WithMaxOpenConns sets max open connections per tenant.
func WithMaxOpenConns(n int) PostgresOption {
	return func(p *PostgresManager) {
		p.maxOpenConns = n
	}
}

// WithMaxIdleConns sets max idle connections per tenant.
func WithMaxIdleConns(n int) PostgresOption {
	return func(p *PostgresManager) {
		p.maxIdleConns = n
	}
}

// WithModule sets the module name for the PostgresManager (e.g., "onboarding", "transaction").
func WithModule(module string) PostgresOption {
	return func(p *PostgresManager) {
		p.module = module
	}
}

// WithMaxTenantPools sets the soft limit for the number of tenant connections in the pool.
// When the pool reaches this limit and a new tenant needs a connection, only connections
// that have been idle longer than the idle timeout are eligible for eviction. If all
// connections are active (used within the idle timeout), the pool grows beyond this limit.
// A value of 0 (default) means unlimited.
func WithMaxTenantPools(maxSize int) PostgresOption {
	return func(p *PostgresManager) {
		p.maxConnections = maxSize
	}
}

// WithIdleTimeout sets the duration after which an unused tenant connection becomes
// eligible for eviction. Only connections idle longer than this duration will be
// evicted when the pool exceeds the soft limit (maxConnections). If all connections
// are active (used within the idle timeout), the pool is allowed to grow beyond the
// soft limit and naturally shrinks back as tenants become idle.
// Default: 5 minutes.
func WithIdleTimeout(d time.Duration) PostgresOption {
	return func(p *PostgresManager) {
		p.idleTimeout = d
	}
}

// Deprecated: Use WithMaxTenantPools instead.
func WithMaxConnections(maxSize int) PostgresOption { return WithMaxTenantPools(maxSize) }

// NewPostgresManager creates a new PostgreSQL connection manager.
func NewPostgresManager(client *Client, service string, opts ...PostgresOption) *PostgresManager {
	p := &PostgresManager{
		client:       client,
		service:      service,
		connections:  make(map[string]*libPostgres.PostgresConnection),
		lastAccessed: make(map[string]time.Time),
		maxOpenConns: 25,
		maxIdleConns: 5,
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
func (p *PostgresManager) GetConnection(ctx context.Context, tenantID string) (*libPostgres.PostgresConnection, error) {
	if tenantID == "" {
		return nil, fmt.Errorf("tenant ID is required")
	}

	p.mu.RLock()

	if p.closed {
		p.mu.RUnlock()
		return nil, ErrManagerClosed
	}

	if conn, ok := p.connections[tenantID]; ok {
		p.mu.RUnlock()

		// Validate cached connection is still healthy (e.g., credentials may have changed)
		if conn.ConnectionDB != nil {
			pingCtx, cancel := context.WithTimeout(ctx, pingTimeout)
			defer cancel()

			if pingErr := (*conn.ConnectionDB).PingContext(pingCtx); pingErr != nil {
				if p.logger != nil {
					p.logger.Warnf("cached postgres connection unhealthy for tenant %s, reconnecting: %v", tenantID, pingErr)
				}

				_ = p.CloseConnection(tenantID)

				// Fall through to create a new connection with fresh credentials
				return p.createConnection(ctx, tenantID)
			}
		}

		// Update LRU tracking on cache hit
		p.mu.Lock()
		p.lastAccessed[tenantID] = time.Now()
		p.mu.Unlock()

		return conn, nil
	}

	p.mu.RUnlock()

	return p.createConnection(ctx, tenantID)
}

// createConnection fetches config from Tenant Manager and creates a connection.
func (p *PostgresManager) createConnection(ctx context.Context, tenantID string) (*libPostgres.PostgresConnection, error) {
	logger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)

	ctx, span := tracer.Start(ctx, "postgres.create_connection")
	defer span.End()

	p.mu.Lock()
	defer p.mu.Unlock()

	if conn, ok := p.connections[tenantID]; ok {
		return conn, nil
	}

	if p.closed {
		return nil, ErrManagerClosed
	}

	// Fetch tenant config from Tenant Manager
	config, err := p.client.GetTenantConfig(ctx, tenantID, p.service)
	if err != nil {
		// Propagate TenantSuspendedError directly so callers (e.g., middleware)
		// can detect suspended/purged tenants without unwrapping generic messages.
		var suspErr *TenantSuspendedError
		if errors.As(err, &suspErr) {
			logger.Warnf("tenant service is %s: tenantID=%s", suspErr.Status, tenantID)
			libOpentelemetry.HandleSpanBusinessErrorEvent(&span, "tenant service suspended", err)

			return nil, err
		}

		logger.Errorf("failed to get tenant config: %v", err)
		libOpentelemetry.HandleSpanError(&span, "failed to get tenant config", err)

		return nil, fmt.Errorf("failed to get tenant config: %w", err)
	}

	pgConfig := config.GetPostgreSQLConfig(p.service, p.module)
	if pgConfig == nil {
		logger.Errorf("no PostgreSQL config for tenant %s service %s module %s", tenantID, p.service, p.module)
		return nil, ErrServiceNotConfigured
	}

	primaryConnStr := buildConnectionString(pgConfig)

	// Check for replica configuration; fall back to primary if not available
	replicaConnStr := primaryConnStr
	replicaDBName := pgConfig.Database

	pgReplicaConfig := config.GetPostgreSQLReplicaConfig(p.service, p.module)
	if pgReplicaConfig != nil {
		replicaConnStr = buildConnectionString(pgReplicaConfig)
		replicaDBName = pgReplicaConfig.Database
		logger.Infof("using separate replica connection for tenant %s (replica host: %s)", tenantID, pgReplicaConfig.Host)
	}

	// Resolve connection pool settings (module-level overrides global defaults)
	maxOpen, maxIdle := p.resolveConnectionPoolSettings(config, tenantID, logger)

	conn := &libPostgres.PostgresConnection{
		ConnectionStringPrimary: primaryConnStr,
		ConnectionStringReplica: replicaConnStr,
		PrimaryDBName:           pgConfig.Database,
		ReplicaDBName:           replicaDBName,
		MaxOpenConnections:      maxOpen,
		MaxIdleConnections:      maxIdle,
		SkipMigrations:          p.IsMultiTenant(),
	}

	if p.logger != nil {
		conn.Logger = p.logger
	}

	if config.IsSchemaMode() && pgConfig.Schema == "" {
		logger.Errorf("schema mode requires schema in config for tenant %s", tenantID)
		return nil, fmt.Errorf("schema mode requires schema in config for tenant %s", tenantID)
	}

	if err := conn.Connect(); err != nil {
		logger.Errorf("failed to connect to tenant database: %v", err)
		libOpentelemetry.HandleSpanError(&span, "failed to connect", err)

		return nil, fmt.Errorf("failed to connect to tenant database: %w", err)
	}

	if pgConfig.Schema != "" {
		logger.Infof("connection configured with search_path=%s for tenant %s (mode: %s)", pgConfig.Schema, tenantID, config.IsolationMode)
	}

	// Evict least recently used connection if pool is full
	p.evictLRU(logger)

	p.connections[tenantID] = conn
	p.lastAccessed[tenantID] = time.Now()

	logger.Infof("created connection for tenant %s (mode: %s)", tenantID, config.IsolationMode)

	return conn, nil
}

// resolveConnectionPoolSettings determines the effective maxOpen and maxIdle connection
// settings for a tenant. It checks module-level settings first (new format), then falls
// back to top-level settings (legacy), and finally uses global defaults.
func (p *PostgresManager) resolveConnectionPoolSettings(config *TenantConfig, tenantID string, logger libLog.Logger) (maxOpen, maxIdle int) {
	maxOpen = p.maxOpenConns
	maxIdle = p.maxIdleConns

	// Apply per-module connection pool settings from Tenant Manager (overrides global defaults).
	// First check module-level settings (new format), then fall back to top-level settings (legacy).
	var connSettings *ConnectionSettings

	if p.module != "" {
		if db, ok := config.Databases[p.module]; ok && db.ConnectionSettings != nil {
			connSettings = db.ConnectionSettings
		}
	}

	// Fall back to top-level ConnectionSettings for backward compatibility with older data
	if connSettings == nil && config.ConnectionSettings != nil {
		connSettings = config.ConnectionSettings
	}

	if connSettings != nil {
		if connSettings.MaxOpenConns > 0 {
			maxOpen = connSettings.MaxOpenConns
			logger.Infof("applying per-module maxOpenConns=%d for tenant %s module %s (global default: %d)", maxOpen, tenantID, p.module, p.maxOpenConns)
		}

		if connSettings.MaxIdleConns > 0 {
			maxIdle = connSettings.MaxIdleConns
			logger.Infof("applying per-module maxIdleConns=%d for tenant %s module %s (global default: %d)", maxIdle, tenantID, p.module, p.maxIdleConns)
		}
	}

	return maxOpen, maxIdle
}

// evictLRU removes the least recently used idle connection when the pool reaches the
// soft limit. Only connections that have been idle longer than the idle timeout are
// eligible for eviction. If all connections are active (used within the idle timeout),
// the pool is allowed to grow beyond the soft limit.
// Caller MUST hold p.mu write lock.
func (p *PostgresManager) evictLRU(logger libLog.Logger) {
	if p.maxConnections <= 0 || len(p.connections) < p.maxConnections {
		return
	}

	now := time.Now()

	idleTimeout := p.idleTimeout
	if idleTimeout == 0 {
		idleTimeout = defaultIdleTimeout
	}

	// Find the oldest connection that has been idle longer than the timeout
	var oldestID string

	var oldestTime time.Time

	for id, t := range p.lastAccessed {
		idleDuration := now.Sub(t)
		if idleDuration < idleTimeout {
			continue // still active, skip
		}

		if oldestID == "" || t.Before(oldestTime) {
			oldestID = id
			oldestTime = t
		}
	}

	if oldestID == "" {
		// All connections are active (used within idle timeout)
		// Allow pool to grow beyond soft limit
		return
	}

	// Evict the idle connection
	if conn, ok := p.connections[oldestID]; ok {
		if conn.ConnectionDB != nil {
			_ = (*conn.ConnectionDB).Close()
		}

		delete(p.connections, oldestID)
		delete(p.lastAccessed, oldestID)

		if logger != nil {
			logger.Infof("LRU evicted idle postgres connection for tenant %s (idle for %s)", oldestID, now.Sub(oldestTime))
		}
	}
}

// GetDB returns a dbresolver.DB for the tenant.
func (p *PostgresManager) GetDB(ctx context.Context, tenantID string) (dbresolver.DB, error) {
	conn, err := p.GetConnection(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	return conn.GetDB()
}

// Close closes all connections and marks the manager as closed.
func (p *PostgresManager) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

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
	}

	return errors.Join(errs...)
}

// CloseConnection closes the connection for a specific tenant.
func (p *PostgresManager) CloseConnection(tenantID string) error {
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

	return err
}

// Stats returns connection statistics.
func (p *PostgresManager) Stats() PostgresStats {
	p.mu.RLock()
	defer p.mu.RUnlock()

	tenantIDs := make([]string, 0, len(p.connections))
	for id := range p.connections {
		tenantIDs = append(tenantIDs, id)
	}

	return PostgresStats{
		TotalConnections: len(p.connections),
		MaxConnections:   p.maxConnections,
		TenantIDs:        tenantIDs,
		Closed:           p.closed,
	}
}

// PostgresStats contains statistics for the PostgresManager.
type PostgresStats struct {
	TotalConnections int      `json:"totalConnections"`
	MaxConnections   int      `json:"maxConnections"`
	TenantIDs        []string `json:"tenantIds"`
	Closed           bool     `json:"closed"`
}

func buildConnectionString(cfg *PostgreSQLConfig) string {
	sslmode := cfg.SSLMode
	if sslmode == "" {
		sslmode = "disable"
	}

	// Escape backslashes and single quotes in the password to prevent
	// injection in the key=value connection string format.
	escapedPassword := strings.NewReplacer(
		`\`, `\\`,
		`'`, `\'`,
	).Replace(cfg.Password)

	connStr := fmt.Sprintf(
		"host=%s port=%d user=%s password='%s' dbname=%s sslmode=%s",
		cfg.Host, cfg.Port, cfg.Username, escapedPassword, cfg.Database, sslmode,
	)

	if cfg.Schema != "" {
		connStr += fmt.Sprintf(" options=-csearch_path=\"%s\"", cfg.Schema)
	}

	return connStr
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
func (p *PostgresManager) ApplyConnectionSettings(tenantID string, config *TenantConfig) {
	p.mu.RLock()
	conn, ok := p.connections[tenantID]
	p.mu.RUnlock()

	if !ok || conn == nil || conn.ConnectionDB == nil {
		return // no cached connection, settings will be applied on next creation
	}

	// Resolve connection settings: module-level first, then top-level fallback
	var connSettings *ConnectionSettings

	if p.module != "" {
		if config.Databases != nil {
			if db, ok := config.Databases[p.module]; ok && db.ConnectionSettings != nil {
				connSettings = db.ConnectionSettings
			}
		}
	}

	// Fall back to top-level ConnectionSettings for backward compatibility
	if connSettings == nil && config.ConnectionSettings != nil {
		connSettings = config.ConnectionSettings
	}

	if connSettings == nil {
		return // no settings to apply
	}

	db := *conn.ConnectionDB

	if connSettings.MaxOpenConns > 0 {
		db.SetMaxOpenConns(connSettings.MaxOpenConns)
	}

	if connSettings.MaxIdleConns > 0 {
		db.SetMaxIdleConns(connSettings.MaxIdleConns)
	}
}

// TenantConnectionManager is an alias for PostgresManager for backward compatibility.
type TenantConnectionManager = PostgresManager

// NewTenantConnectionManager is an alias for NewPostgresManager for backward compatibility.
func NewTenantConnectionManager(client *Client, service, module string, logger libLog.Logger) *PostgresManager {
	return NewPostgresManager(client, service, WithPostgresLogger(logger), WithModule(module))
}

// WithConnectionLimits sets the connection limits for the manager.
// Returns the manager for method chaining.
func (p *PostgresManager) WithConnectionLimits(maxOpen, maxIdle int) *PostgresManager {
	p.maxOpenConns = maxOpen
	p.maxIdleConns = maxIdle

	return p
}

// WithDefaultConnection sets a default connection to use when no tenant context is available.
// This enables backward compatibility with single-tenant deployments.
// Returns the manager for method chaining.
func (p *PostgresManager) WithDefaultConnection(conn *libPostgres.PostgresConnection) *PostgresManager {
	p.defaultConn = conn
	return p
}

// GetDefaultConnection returns the default connection configured for single-tenant mode.
func (p *PostgresManager) GetDefaultConnection() *libPostgres.PostgresConnection {
	return p.defaultConn
}

// IsMultiTenant returns true if the manager is configured with a Tenant Manager client.
func (p *PostgresManager) IsMultiTenant() bool {
	return p.client != nil
}

// CreateDirectConnection creates a direct database connection from config.
// Useful when you have config but don't need full connection management.
func CreateDirectConnection(ctx context.Context, cfg *PostgreSQLConfig) (*sql.DB, error) {
	connStr := buildConnectionString(cfg)

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
