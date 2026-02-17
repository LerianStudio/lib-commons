package tenantmanager

import (
	"context"
	"database/sql"
	"fmt"
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

// PostgresManager manages PostgreSQL database connections per tenant.
// It fetches credentials from Tenant Manager and caches connections.
// Credentials are provided directly by the tenant-manager settings endpoint.
type PostgresManager struct {
	client  *Client
	service string
	module  string
	logger  libLog.Logger

	mu          sync.RWMutex
	connections map[string]*libPostgres.PostgresConnection
	closed      bool

	maxOpenConns int
	maxIdleConns int

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

// NewPostgresManager creates a new PostgreSQL connection manager.
func NewPostgresManager(client *Client, service string, opts ...PostgresOption) *PostgresManager {
	p := &PostgresManager{
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

	conn := &libPostgres.PostgresConnection{
		ConnectionStringPrimary: primaryConnStr,
		ConnectionStringReplica: replicaConnStr,
		PrimaryDBName:           pgConfig.Database,
		ReplicaDBName:           replicaDBName,
		MaxOpenConnections:      p.maxOpenConns,
		MaxIdleConnections:      p.maxIdleConns,
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

	p.connections[tenantID] = conn

	logger.Infof("created connection for tenant %s (mode: %s)", tenantID, config.IsolationMode)

	return conn, nil
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
		TenantIDs:        tenantIDs,
		Closed:           p.closed,
	}
}

// PostgresStats contains statistics for the PostgresManager.
type PostgresStats struct {
	TotalConnections int      `json:"totalConnections"`
	TenantIDs        []string `json:"tenantIds"`
	Closed           bool     `json:"closed"`
}

func buildConnectionString(cfg *PostgreSQLConfig) string {
	sslmode := cfg.SSLMode
	if sslmode == "" {
		sslmode = "disable"
	}

	connStr := fmt.Sprintf(
		"host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		cfg.Host, cfg.Port, cfg.Username, cfg.Password, cfg.Database, sslmode,
	)

	if cfg.Schema != "" {
		connStr += fmt.Sprintf(" options=-csearch_path=\"%s\"", cfg.Schema)
	}

	return connStr
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

// buildDSN builds a PostgreSQL DSN (alias for backward compatibility).
func buildDSN(cfg *PostgreSQLConfig) string {
	return buildConnectionString(cfg)
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
		db.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return db, nil
}
