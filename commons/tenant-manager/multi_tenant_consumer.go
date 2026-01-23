// Package tenantmanager provides multi-tenant database and message queue connection management.
package tenantmanager

import (
	"context"
	"fmt"
	"sync"
	"time"

	libCommons "github.com/LerianStudio/lib-commons/v2/commons"
	libLog "github.com/LerianStudio/lib-commons/v2/commons/log"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v2/commons/opentelemetry"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/redis/go-redis/v9"
)

// ActiveTenantsKey is the Redis SET key for storing active tenant IDs.
// This key is managed by tenant-manager and read by consumers.
const ActiveTenantsKey = "tenant-manager:tenants:active"

// HandlerFunc is a function that processes messages from a queue.
// The context contains the tenant ID via SetTenantIDInContext.
type HandlerFunc func(ctx context.Context, delivery amqp.Delivery) error

// MultiTenantConfig holds configuration for the MultiTenantConsumer.
type MultiTenantConfig struct {
	// SyncInterval is the interval between tenant list synchronizations.
	// Default: 30 seconds
	SyncInterval time.Duration

	// WorkersPerQueue is the number of worker goroutines per queue per tenant.
	// Default: 1
	WorkersPerQueue int

	// PrefetchCount is the QoS prefetch count per channel.
	// Default: 10
	PrefetchCount int

	// TenantManagerURL is the fallback HTTP endpoint to fetch tenants if Redis cache misses.
	// Format: http://tenant-manager:4003
	TenantManagerURL string

	// Service is the service name to filter tenants by.
	// This is passed to tenant-manager when fetching tenant list.
	Service string
}

// DefaultMultiTenantConfig returns a MultiTenantConfig with sensible defaults.
func DefaultMultiTenantConfig() MultiTenantConfig {
	return MultiTenantConfig{
		SyncInterval:    30 * time.Second,
		WorkersPerQueue: 1,
		PrefetchCount:   10,
	}
}

// MultiTenantConsumer manages message consumption across multiple tenant vhosts.
// It dynamically discovers tenants from Redis cache and spawns consumer goroutines.
type MultiTenantConsumer struct {
	pool        *RabbitMQPool
	redisClient redis.UniversalClient
	pmClient    *Client // Tenant Manager client for fallback
	handlers    map[string]HandlerFunc
	tenants     map[string]context.CancelFunc // Active tenant goroutines
	config      MultiTenantConfig
	mu          sync.RWMutex
	logger      libLog.Logger
	closed      bool
}

// NewMultiTenantConsumer creates a new MultiTenantConsumer.
// Parameters:
//   - pool: RabbitMQ connection pool for tenant vhosts
//   - redisClient: Redis client for tenant cache access
//   - config: Consumer configuration
//   - logger: Logger for operational logging
func NewMultiTenantConsumer(
	pool *RabbitMQPool,
	redisClient redis.UniversalClient,
	config MultiTenantConfig,
	logger libLog.Logger,
) *MultiTenantConsumer {
	// Apply defaults
	if config.SyncInterval == 0 {
		config.SyncInterval = 30 * time.Second
	}
	if config.WorkersPerQueue == 0 {
		config.WorkersPerQueue = 1
	}
	if config.PrefetchCount == 0 {
		config.PrefetchCount = 10
	}

	consumer := &MultiTenantConsumer{
		pool:        pool,
		redisClient: redisClient,
		handlers:    make(map[string]HandlerFunc),
		tenants:     make(map[string]context.CancelFunc),
		config:      config,
		logger:      logger,
	}

	// Create Tenant Manager client for fallback if URL is configured
	if config.TenantManagerURL != "" {
		consumer.pmClient = NewClient(config.TenantManagerURL, logger)
	}

	return consumer
}

// Register adds a queue handler for all tenant vhosts.
// The handler will be invoked for messages from the specified queue in each tenant's vhost.
func (c *MultiTenantConsumer) Register(queueName string, handler HandlerFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.handlers[queueName] = handler
	c.logger.Infof("registered handler for queue: %s", queueName)
}

// Run starts the multi-tenant consumer.
// It performs an initial sync (blocking) and then starts background polling.
// Returns an error if the initial sync fails.
func (c *MultiTenantConsumer) Run(ctx context.Context) error {
	c.logger.Info("starting multi-tenant consumer")

	// Initial sync - BLOCKING (ensures tenants loaded before processing)
	if err := c.syncTenants(ctx); err != nil {
		c.logger.Errorf("initial tenant sync failed: %v", err)
		return fmt.Errorf("initial tenant sync failed: %w", err)
	}

	c.logger.Infof("initial sync complete, %d tenants active", len(c.tenants))

	// Background polling - ASYNC
	go c.runSyncLoop(ctx)

	return nil
}

// runSyncLoop periodically syncs the tenant list.
func (c *MultiTenantConsumer) runSyncLoop(ctx context.Context) {
	ticker := time.NewTicker(c.config.SyncInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := c.syncTenants(ctx); err != nil {
				c.logger.Warnf("tenant sync failed (continuing): %v", err)
			}
		case <-ctx.Done():
			c.logger.Info("sync loop stopped: context cancelled")
			return
		}
	}
}

// syncTenants fetches tenant IDs and manages consumer goroutines.
func (c *MultiTenantConsumer) syncTenants(ctx context.Context) error {
	logger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)
	ctx, span := tracer.Start(ctx, "multi_tenant_consumer.sync_tenants")
	defer span.End()

	// Fetch tenant IDs from Redis cache
	tenantIDs, err := c.fetchTenantIDs(ctx)
	if err != nil {
		logger.Errorf("failed to fetch tenant IDs: %v", err)
		libOpentelemetry.HandleSpanError(&span, "failed to fetch tenant IDs", err)
		return fmt.Errorf("failed to fetch tenant IDs: %w", err)
	}

	// Create a set of current tenant IDs for quick lookup
	currentTenants := make(map[string]bool)
	for _, id := range tenantIDs {
		currentTenants[id] = true
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return fmt.Errorf("consumer is closed")
	}

	// Identify NEW tenants (in current list but not running)
	var newTenants []string
	for _, tenantID := range tenantIDs {
		if _, exists := c.tenants[tenantID]; !exists {
			newTenants = append(newTenants, tenantID)
		}
	}

	// Identify REMOVED tenants (running but not in current list)
	var removedTenants []string
	for tenantID := range c.tenants {
		if !currentTenants[tenantID] {
			removedTenants = append(removedTenants, tenantID)
		}
	}

	// Stop removed tenants
	for _, tenantID := range removedTenants {
		logger.Infof("stopping consumer for removed tenant: %s", tenantID)
		if cancel, ok := c.tenants[tenantID]; ok {
			cancel()
			delete(c.tenants, tenantID)
		}
	}

	// Start new tenants in parallel using WaitGroup
	if len(newTenants) > 0 {
		var wg sync.WaitGroup
		wg.Add(len(newTenants))

		for _, tenantID := range newTenants {
			go func(tid string) {
				defer wg.Done()
				c.startTenantConsumer(ctx, tid)
			}(tenantID)
		}

		wg.Wait()
	}

	logger.Infof("sync complete: %d active, %d added, %d removed",
		len(c.tenants), len(newTenants), len(removedTenants))

	return nil
}

// fetchTenantIDs gets tenant IDs from Redis cache, falling back to Tenant Manager API.
func (c *MultiTenantConsumer) fetchTenantIDs(ctx context.Context) ([]string, error) {
	// Try Redis cache first
	tenantIDs, err := c.redisClient.SMembers(ctx, ActiveTenantsKey).Result()
	if err == nil && len(tenantIDs) > 0 {
		c.logger.Infof("fetched %d tenant IDs from cache", len(tenantIDs))
		return tenantIDs, nil
	}

	if err != nil {
		c.logger.Warnf("Redis cache fetch failed: %v", err)
	}

	// Fallback to Tenant Manager API
	if c.pmClient != nil && c.config.Service != "" {
		c.logger.Info("falling back to Tenant Manager API for tenant list")
		tenants, apiErr := c.pmClient.GetActiveTenantsByService(ctx, c.config.Service)
		if apiErr != nil {
			c.logger.Errorf("Tenant Manager API fallback failed: %v", apiErr)
			// Return Redis error if API also fails
			if err != nil {
				return nil, err
			}
			return nil, apiErr
		}

		// Extract IDs from tenant summaries
		ids := make([]string, len(tenants))
		for i, t := range tenants {
			ids[i] = t.ID
		}
		c.logger.Infof("fetched %d tenant IDs from Tenant Manager API", len(ids))
		return ids, nil
	}

	// No tenants available
	if err != nil {
		return nil, err
	}
	return []string{}, nil
}

// startTenantConsumer spawns a consumer goroutine for a tenant.
// MUST be called with c.mu held.
func (c *MultiTenantConsumer) startTenantConsumer(parentCtx context.Context, tenantID string) {
	// Create a cancellable context for this tenant
	tenantCtx, cancel := context.WithCancel(parentCtx)

	// Store the cancel function (caller holds lock)
	c.tenants[tenantID] = cancel

	c.logger.Infof("starting consumer for tenant: %s", tenantID)

	// Spawn consumer goroutine
	go c.consumeForTenant(tenantCtx, tenantID)
}

// consumeForTenant runs the consumer loop for a single tenant.
func (c *MultiTenantConsumer) consumeForTenant(ctx context.Context, tenantID string) {
	// Set tenantID in context for handlers
	ctx = SetTenantIDInContext(ctx, tenantID)

	logger := c.logger.WithFields("tenant_id", tenantID)
	logger.Info("consumer started for tenant")

	// Get all registered handlers (read-only, no lock needed after initial registration)
	c.mu.RLock()
	handlers := make(map[string]HandlerFunc, len(c.handlers))
	for queue, handler := range c.handlers {
		handlers[queue] = handler
	}
	c.mu.RUnlock()

	// Consume from each registered queue
	for queueName, handler := range handlers {
		go c.consumeQueue(ctx, tenantID, queueName, handler, logger)
	}

	// Wait for context cancellation
	<-ctx.Done()
	logger.Info("consumer stopped for tenant")
}

// consumeQueue consumes messages from a specific queue for a tenant.
func (c *MultiTenantConsumer) consumeQueue(
	ctx context.Context,
	tenantID string,
	queueName string,
	handler HandlerFunc,
	logger libLog.Logger,
) {
	logger = logger.WithFields("queue", queueName)

	for {
		select {
		case <-ctx.Done():
			logger.Info("queue consumer stopped")
			return
		default:
		}

		// Get channel for this tenant's vhost
		ch, err := c.pool.GetChannel(ctx, tenantID)
		if err != nil {
			logger.Warnf("failed to get channel, retrying in 5s: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				continue
			}
		}

		// Set QoS
		if err := ch.Qos(c.config.PrefetchCount, 0, false); err != nil {
			logger.Warnf("failed to set QoS, retrying in 5s: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				continue
			}
		}

		// Start consuming
		msgs, err := ch.Consume(
			queueName,
			"",    // consumer tag
			false, // auto-ack
			false, // exclusive
			false, // no-local
			false, // no-wait
			nil,   // args
		)
		if err != nil {
			logger.Warnf("failed to start consuming, retrying in 5s: %v", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
				continue
			}
		}

		logger.Info("consuming started")

		// Setup channel close notification
		notifyClose := make(chan *amqp.Error, 1)
		ch.NotifyClose(notifyClose)

		// Process messages
		c.processMessages(ctx, tenantID, queueName, handler, msgs, notifyClose, logger)

		logger.Warn("channel closed, reconnecting...")
	}
}

// processMessages processes messages from the channel until it closes.
func (c *MultiTenantConsumer) processMessages(
	ctx context.Context,
	tenantID string,
	queueName string,
	handler HandlerFunc,
	msgs <-chan amqp.Delivery,
	notifyClose <-chan *amqp.Error,
	logger libLog.Logger,
) {
	for {
		select {
		case <-ctx.Done():
			return
		case err := <-notifyClose:
			if err != nil {
				logger.Warnf("channel closed with error: %v", err)
			}
			return
		case msg, ok := <-msgs:
			if !ok {
				logger.Warn("message channel closed")
				return
			}

			// Process message with tenant context
			msgCtx := SetTenantIDInContext(ctx, tenantID)

			// Extract trace context from message headers
			msgCtx = libOpentelemetry.ExtractTraceContextFromQueueHeaders(msgCtx, msg.Headers)

			if err := handler(msgCtx, msg); err != nil {
				logger.Errorf("handler error for queue %s: %v", queueName, err)
				// Nack with requeue
				if nackErr := msg.Nack(false, true); nackErr != nil {
					logger.Errorf("failed to nack message: %v", nackErr)
				}
			} else {
				// Ack on success
				if ackErr := msg.Ack(false); ackErr != nil {
					logger.Errorf("failed to ack message: %v", ackErr)
				}
			}
		}
	}
}

// Close stops all consumer goroutines and marks the consumer as closed.
func (c *MultiTenantConsumer) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.closed = true

	// Cancel all tenant contexts
	for tenantID, cancel := range c.tenants {
		c.logger.Infof("stopping consumer for tenant: %s", tenantID)
		cancel()
	}

	// Clear the map
	c.tenants = make(map[string]context.CancelFunc)

	c.logger.Info("multi-tenant consumer closed")
	return nil
}

// Stats returns statistics about the consumer.
func (c *MultiTenantConsumer) Stats() MultiTenantConsumerStats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	tenantIDs := make([]string, 0, len(c.tenants))
	for id := range c.tenants {
		tenantIDs = append(tenantIDs, id)
	}

	queueNames := make([]string, 0, len(c.handlers))
	for name := range c.handlers {
		queueNames = append(queueNames, name)
	}

	return MultiTenantConsumerStats{
		ActiveTenants:    len(c.tenants),
		TenantIDs:        tenantIDs,
		RegisteredQueues: queueNames,
		Closed:           c.closed,
	}
}

// MultiTenantConsumerStats holds statistics for the consumer.
type MultiTenantConsumerStats struct {
	ActiveTenants    int      `json:"activeTenants"`
	TenantIDs        []string `json:"tenantIds"`
	RegisteredQueues []string `json:"registeredQueues"`
	Closed           bool     `json:"closed"`
}
