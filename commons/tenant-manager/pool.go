package tenantmanager

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"

	libCommons "github.com/LerianStudio/lib-commons/v2/commons"
	libLog "github.com/LerianStudio/lib-commons/v2/commons/log"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v2/commons/opentelemetry"
	libPostgres "github.com/LerianStudio/lib-commons/v2/commons/postgres"
	"github.com/bxcodec/dbresolver/v2"
	_ "github.com/jackc/pgx/v5/stdlib"
)

// IsolationMode constants define the tenant isolation strategies.
const (
	// IsolationModeIsolated indicates each tenant has a dedicated database.
	IsolationModeIsolated = "isolated"
	// IsolationModeSchema indicates tenants share a database but have separate schemas.
	IsolationModeSchema = "schema"
)

// SchemaNameFromTenantID generates a PostgreSQL schema name from a tenant ID.
// The schema name format is: tenant_{uuid_with_underscores}
// Example: tenant ID "550e8400-e29b-41d4-a716-446655440000" becomes "tenant_550e8400_e29b_41d4_a716_446655440000"
func SchemaNameFromTenantID(tenantID string) string {
	return "tenant_" + strings.ReplaceAll(tenantID, "-", "_")
}

// Pool manages database connections per tenant.
// It fetches credentials from Tenant Manager and caches connections.
type Pool struct {
	client  *Client
	service string
	module  string
	logger  libLog.Logger

	mu          sync.RWMutex
	connections map[string]*libPostgres.PostgresConnection
	closed      bool

	// Connection settings
	maxOpenConns int
	maxIdleConns int

	// Default connection for single-tenant mode fallback
	defaultConn *libPostgres.PostgresConnection
}

// PoolOption configures a Pool.
type PoolOption func(*Pool)

// WithPoolLogger sets the logger for the pool.
func WithPoolLogger(logger libLog.Logger) PoolOption {
	return func(p *Pool) {
		p.logger = logger
	}
}

// WithMaxOpenConns sets max open connections per tenant.
func WithMaxOpenConns(n int) PoolOption {
	return func(p *Pool) {
		p.maxOpenConns = n
	}
}

// WithMaxIdleConns sets max idle connections per tenant.
func WithMaxIdleConns(n int) PoolOption {
	return func(p *Pool) {
		p.maxIdleConns = n
	}
}

// WithModule sets the module name for the pool (e.g., "onboarding", "transaction").
func WithModule(module string) PoolOption {
	return func(p *Pool) {
		p.module = module
	}
}

// NewPool creates a new connection pool.
func NewPool(client *Client, service string, opts ...PoolOption) *Pool {
	p := &Pool{
		client:       client,
		service:      service,
		connections:  make(map[string]*libPostgres.PostgresConnection),
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
func (p *Pool) GetConnection(ctx context.Context, tenantID string) (*libPostgres.PostgresConnection, error) {
	if tenantID == "" {
		return nil, fmt.Errorf("tenant ID is required")
	}

	p.mu.RLock()
	if p.closed {
		p.mu.RUnlock()
		return nil, ErrPoolClosed
	}

	// Check if connection exists
	if conn, ok := p.connections[tenantID]; ok {
		p.mu.RUnlock()
		return conn, nil
	}
	p.mu.RUnlock()

	// Create new connection
	return p.createConnection(ctx, tenantID)
}

// createConnection fetches config from Tenant Manager and creates a connection.
func (p *Pool) createConnection(ctx context.Context, tenantID string) (*libPostgres.PostgresConnection, error) {
	logger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)
	ctx, span := tracer.Start(ctx, "pool.create_connection")
	defer span.End()

	p.mu.Lock()
	defer p.mu.Unlock()

	// Double-check after acquiring lock
	if conn, ok := p.connections[tenantID]; ok {
		return conn, nil
	}

	if p.closed {
		return nil, ErrPoolClosed
	}

	// Fetch tenant config from Tenant Manager
	config, err := p.client.GetTenantConfig(ctx, tenantID, p.service)
	if err != nil {
		logger.Errorf("failed to get tenant config: %v", err)
		libOpentelemetry.HandleSpanError(&span, "failed to get tenant config", err)
		return nil, fmt.Errorf("failed to get tenant config: %w", err)
	}

	// Get PostgreSQL config
	pgConfig := config.GetPostgreSQLConfig(p.service, p.module)
	if pgConfig == nil {
		logger.Errorf("no PostgreSQL config for tenant %s service %s module %s", tenantID, p.service, p.module)
		return nil, ErrServiceNotConfigured
	}

	// Build connection string
	connStr := buildConnectionString(pgConfig)

	// Create PostgresConnection
	// In multi-tenant mode: skip migrations (tenant databases should be provisioned separately)
	// In single-tenant mode: run migrations automatically
	conn := &libPostgres.PostgresConnection{
		ConnectionStringPrimary: connStr,
		ConnectionStringReplica: connStr,
		PrimaryDBName:           pgConfig.Database,
		ReplicaDBName:           pgConfig.Database,
		MaxOpenConnections:      p.maxOpenConns,
		MaxIdleConnections:      p.maxIdleConns,
		SkipMigrations:          p.IsMultiTenant(),
	}

	if p.logger != nil {
		conn.Logger = p.logger
	}

	// Connect
	if err := conn.Connect(); err != nil {
		logger.Errorf("failed to connect to tenant database: %v", err)
		libOpentelemetry.HandleSpanError(&span, "failed to connect", err)
		return nil, fmt.Errorf("failed to connect to tenant database: %w", err)
	}

	// For schema mode, set the search_path to the tenant's schema
	if config.IsSchemaMode() {
		schemaName := SchemaNameFromTenantID(tenantID)
		if err := p.setSearchPath(ctx, conn, schemaName); err != nil {
			logger.Errorf("failed to set search_path for tenant %s: %v", tenantID, err)
			libOpentelemetry.HandleSpanError(&span, "failed to set search_path", err)
			// Close the connection since it's not properly configured
			if conn.ConnectionDB != nil {
				(*conn.ConnectionDB).Close()
			}
			return nil, fmt.Errorf("failed to set search_path for schema mode: %w", err)
		}
		logger.Infof("set search_path to schema %s for tenant %s (schema mode)", schemaName, tenantID)
	}

	// Cache connection
	p.connections[tenantID] = conn

	logger.Infof("created connection for tenant %s (mode: %s)", tenantID, config.IsolationMode)

	return conn, nil
}

// setSearchPath sets the search_path for a PostgreSQL connection to the tenant's schema.
// This is used for schema-mode multi-tenancy where all tenants share the same database
// but have isolated schemas.
func (p *Pool) setSearchPath(ctx context.Context, conn *libPostgres.PostgresConnection, schemaName string) error {
	if conn.ConnectionDB == nil {
		return fmt.Errorf("connection not established")
	}

	db := *conn.ConnectionDB

	// Use quoted identifier to prevent SQL injection and handle special characters
	// The schema name format is already controlled (tenant_{uuid_with_underscores})
	query := fmt.Sprintf(`SET search_path TO "%s", public`, schemaName)

	_, err := db.ExecContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to execute SET search_path: %w", err)
	}

	return nil
}

// GetDB returns a dbresolver.DB for the tenant.
func (p *Pool) GetDB(ctx context.Context, tenantID string) (dbresolver.DB, error) {
	conn, err := p.GetConnection(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	return conn.GetDB()
}

// Close closes all connections and marks the pool as closed.
func (p *Pool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.closed = true

	var lastErr error
	for tenantID, conn := range p.connections {
		if conn.ConnectionDB != nil {
			if err := (*conn.ConnectionDB).Close(); err != nil {
				lastErr = err
			}
		}
		delete(p.connections, tenantID)
	}

	return lastErr
}

// CloseConnection closes the connection for a specific tenant.
func (p *Pool) CloseConnection(tenantID string) error {
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

	return err
}

// Stats returns pool statistics.
func (p *Pool) Stats() PoolStats {
	p.mu.RLock()
	defer p.mu.RUnlock()

	tenantIDs := make([]string, 0, len(p.connections))
	for id := range p.connections {
		tenantIDs = append(tenantIDs, id)
	}

	return PoolStats{
		TotalConnections: len(p.connections),
		TenantIDs:        tenantIDs,
		Closed:           p.closed,
	}
}

// PoolStats contains statistics for the pool.
type PoolStats struct {
	TotalConnections int      `json:"totalConnections"`
	TenantIDs        []string `json:"tenantIds"`
	Closed           bool     `json:"closed"`
}

// buildConnectionString builds a PostgreSQL connection string.
func buildConnectionString(cfg *PostgreSQLConfig) string {
	sslmode := cfg.SSLMode
	if sslmode == "" {
		sslmode = "disable"
	}

	return fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		cfg.Host, cfg.Port, cfg.Username, cfg.Password, cfg.Database, sslmode,
	)
}

// TenantConnectionPool is an alias for Pool for backward compatibility.
type TenantConnectionPool = Pool

// NewTenantConnectionPool is an alias for NewPool for backward compatibility.
func NewTenantConnectionPool(client *Client, service, module string, logger libLog.Logger) *Pool {
	return NewPool(client, service, WithPoolLogger(logger), WithModule(module))
}

// WithConnectionLimits sets the connection limits for the pool.
// Returns the pool for method chaining.
func (p *Pool) WithConnectionLimits(maxOpen, maxIdle int) *Pool {
	p.maxOpenConns = maxOpen
	p.maxIdleConns = maxIdle
	return p
}

// WithDefaultConnection sets a default connection to use when no tenant context is available.
// This enables backward compatibility with single-tenant deployments.
// Returns the pool for method chaining.
func (p *Pool) WithDefaultConnection(conn *libPostgres.PostgresConnection) *Pool {
	p.defaultConn = conn
	return p
}

// GetDefaultConnection returns the default connection configured for single-tenant mode.
func (p *Pool) GetDefaultConnection() *libPostgres.PostgresConnection {
	return p.defaultConn
}

// IsMultiTenant returns true if the pool is configured with a Tenant Manager client.
func (p *Pool) IsMultiTenant() bool {
	return p.client != nil
}

// buildDSN builds a PostgreSQL DSN (alias for backward compatibility).
func buildDSN(cfg *PostgreSQLConfig) string {
	return buildConnectionString(cfg)
}

// CreateDirectConnection creates a direct database connection from config.
// Useful when you have config but don't need full pool management.
func CreateDirectConnection(ctx context.Context, cfg *PostgreSQLConfig) (*sql.DB, error) {
	connStr := buildConnectionString(cfg)
	
	db, err := sql.Open("pgx", connStr)
	if err != nil {
		return nil, fmt.Errorf("failed to open connection: %w", err)
	}

	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return db, nil
}
