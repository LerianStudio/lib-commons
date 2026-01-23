package tenantmanager

import (
	"context"
	"fmt"
	"sync"

	libCommons "github.com/LerianStudio/lib-commons/v2/commons"
	"github.com/LerianStudio/lib-commons/v2/commons/log"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v2/commons/opentelemetry"
	amqp "github.com/rabbitmq/amqp091-go"
)

// Context key for RabbitMQ
const tenantRabbitMQKey contextKey = "tenantRabbitMQ"

// RabbitMQPool manages RabbitMQ connections per tenant.
// Each tenant has a dedicated vhost, user, and credentials stored in Tenant Manager.
type RabbitMQPool struct {
	client  *Client
	service string
	module  string
	logger  log.Logger

	mu     sync.RWMutex
	pools  map[string]*amqp.Connection
	closed bool
}

// RabbitMQPoolOption configures a RabbitMQPool.
type RabbitMQPoolOption func(*RabbitMQPool)

// WithRabbitMQModule sets the module name for the RabbitMQ pool.
func WithRabbitMQModule(module string) RabbitMQPoolOption {
	return func(p *RabbitMQPool) {
		p.module = module
	}
}

// WithRabbitMQLogger sets the logger for the RabbitMQ pool.
func WithRabbitMQLogger(logger log.Logger) RabbitMQPoolOption {
	return func(p *RabbitMQPool) {
		p.logger = logger
	}
}

// NewRabbitMQPool creates a new RabbitMQ connection pool.
// Parameters:
//   - client: The Tenant Manager client for fetching tenant configurations
//   - service: The service name (e.g., "ledger")
//   - opts: Optional configuration options
func NewRabbitMQPool(client *Client, service string, opts ...RabbitMQPoolOption) *RabbitMQPool {
	p := &RabbitMQPool{
		client:  client,
		service: service,
		pools:   make(map[string]*amqp.Connection),
	}

	for _, opt := range opts {
		opt(p)
	}

	return p
}

// GetConnection returns a RabbitMQ connection for the tenant.
// Creates a new connection if one doesn't exist or the existing one is closed.
func (p *RabbitMQPool) GetConnection(ctx context.Context, tenantID string) (*amqp.Connection, error) {
	if tenantID == "" {
		return nil, fmt.Errorf("tenant ID is required")
	}

	p.mu.RLock()
	if p.closed {
		p.mu.RUnlock()
		return nil, ErrPoolClosed
	}

	if conn, ok := p.pools[tenantID]; ok && !conn.IsClosed() {
		p.mu.RUnlock()
		return conn, nil
	}
	p.mu.RUnlock()

	return p.createConnection(ctx, tenantID)
}

// createConnection fetches config from Tenant Manager and creates a RabbitMQ connection.
func (p *RabbitMQPool) createConnection(ctx context.Context, tenantID string) (*amqp.Connection, error) {
	logger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)
	ctx, span := tracer.Start(ctx, "rabbitmq_pool.create_connection")
	defer span.End()

	if p.logger != nil {
		logger = p.logger
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	// Double-check after acquiring lock
	if conn, ok := p.pools[tenantID]; ok && !conn.IsClosed() {
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

	// Get RabbitMQ config
	rabbitConfig := config.GetRabbitMQConfig()
	if rabbitConfig == nil {
		logger.Errorf("RabbitMQ not configured for tenant: %s", tenantID)
		libOpentelemetry.HandleSpanBusinessErrorEvent(&span, "RabbitMQ not configured", nil)
		return nil, ErrServiceNotConfigured
	}

	// Build connection URI with tenant's vhost
	uri := buildRabbitMQURI(rabbitConfig)

	logger.Infof("connecting to RabbitMQ vhost: tenant=%s, vhost=%s", tenantID, rabbitConfig.VHost)

	// Create connection
	conn, err := amqp.Dial(uri)
	if err != nil {
		logger.Errorf("failed to connect to RabbitMQ: %v", err)
		libOpentelemetry.HandleSpanError(&span, "failed to connect to RabbitMQ", err)
		return nil, fmt.Errorf("failed to connect to RabbitMQ: %w", err)
	}

	// Cache connection
	p.pools[tenantID] = conn

	logger.Infof("RabbitMQ connection created: tenant=%s, vhost=%s", tenantID, rabbitConfig.VHost)

	return conn, nil
}

// GetChannel returns a RabbitMQ channel for the tenant.
// Creates a new connection if one doesn't exist.
func (p *RabbitMQPool) GetChannel(ctx context.Context, tenantID string) (*amqp.Channel, error) {
	conn, err := p.GetConnection(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	channel, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("failed to open channel: %w", err)
	}

	return channel, nil
}

// Close closes all RabbitMQ connections.
func (p *RabbitMQPool) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.closed = true

	var lastErr error
	for tenantID, conn := range p.pools {
		if conn != nil && !conn.IsClosed() {
			if err := conn.Close(); err != nil {
				lastErr = err
			}
		}
		delete(p.pools, tenantID)
	}

	return lastErr
}

// CloseConnection closes the RabbitMQ connection for a specific tenant.
func (p *RabbitMQPool) CloseConnection(tenantID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	conn, ok := p.pools[tenantID]
	if !ok {
		return nil
	}

	var err error
	if conn != nil && !conn.IsClosed() {
		err = conn.Close()
	}
	delete(p.pools, tenantID)

	return err
}

// Stats returns pool statistics.
func (p *RabbitMQPool) Stats() RabbitMQPoolStats {
	p.mu.RLock()
	defer p.mu.RUnlock()

	tenantIDs := make([]string, 0, len(p.pools))
	activeConnections := 0

	for id, conn := range p.pools {
		tenantIDs = append(tenantIDs, id)
		if conn != nil && !conn.IsClosed() {
			activeConnections++
		}
	}

	return RabbitMQPoolStats{
		TotalConnections:  len(p.pools),
		ActiveConnections: activeConnections,
		TenantIDs:         tenantIDs,
		Closed:            p.closed,
	}
}

// RabbitMQPoolStats contains statistics for the RabbitMQ pool.
type RabbitMQPoolStats struct {
	TotalConnections  int      `json:"totalConnections"`
	ActiveConnections int      `json:"activeConnections"`
	TenantIDs         []string `json:"tenantIds"`
	Closed            bool     `json:"closed"`
}

// buildRabbitMQURI builds RabbitMQ connection URI from config.
func buildRabbitMQURI(cfg *RabbitMQConfig) string {
	return fmt.Sprintf("amqp://%s:%s@%s:%d/%s",
		cfg.Username, cfg.Password, cfg.Host, cfg.Port, cfg.VHost)
}

// ContextWithTenantRabbitMQ stores the RabbitMQ channel in the context.
func ContextWithTenantRabbitMQ(ctx context.Context, ch *amqp.Channel) context.Context {
	return context.WithValue(ctx, tenantRabbitMQKey, ch)
}

// GetRabbitMQFromContext retrieves the RabbitMQ channel from the context.
// Returns nil if not found.
func GetRabbitMQFromContext(ctx context.Context) *amqp.Channel {
	if ch, ok := ctx.Value(tenantRabbitMQKey).(*amqp.Channel); ok {
		return ch
	}
	return nil
}

// GetRabbitMQForTenant returns the RabbitMQ channel for the current tenant from context.
// If no tenant connection is found in context, returns ErrTenantContextRequired.
// This function ALWAYS requires tenant context - there is no fallback to default connections.
func GetRabbitMQForTenant(ctx context.Context) (*amqp.Channel, error) {
	if ch := GetRabbitMQFromContext(ctx); ch != nil {
		return ch, nil
	}

	return nil, ErrTenantContextRequired
}

// IsMultiTenant returns true if the pool is configured with a Tenant Manager client.
func (p *RabbitMQPool) IsMultiTenant() bool {
	return p.client != nil
}
