// Package tenantmanager provides multi-tenant database and message queue connection management.
package tenantmanager

import (
	"context"
	"fmt"
	"regexp"
	"sync"
	"time"

	libCommons "github.com/LerianStudio/lib-commons/v2/commons"
	libLog "github.com/LerianStudio/lib-commons/v2/commons/log"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v2/commons/opentelemetry"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/redis/go-redis/v9"
)

// maxTenantIDLength is the maximum allowed length for a tenant ID.
const maxTenantIDLength = 256

// absentSyncsBeforeRemoval is the number of consecutive syncs a tenant can be
// missing from the fetched list before it is removed from knownTenants and
// any active consumer is stopped. Prevents transient incomplete fetches from
// purging tenants immediately.
const absentSyncsBeforeRemoval = 3

// validTenantIDPattern enforces a character whitelist for tenant IDs.
// Only alphanumeric characters, hyphens, and underscores are allowed.
var validTenantIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// buildActiveTenantsKey returns an environment+service segmented Redis key for active tenants.
// The key format is always: "tenant-manager:tenants:active:{env}:{service}"
// The caller is responsible for providing valid env and service values.
func buildActiveTenantsKey(env, service string) string {
	return fmt.Sprintf("tenant-manager:tenants:active:%s:%s", env, service)
}

// HandlerFunc is a function that processes messages from a queue.
// The context contains the tenant ID via SetTenantIDInContext.
type HandlerFunc func(ctx context.Context, delivery amqp.Delivery) error

// MultiTenantConfig holds configuration for the MultiTenantConsumer.
type MultiTenantConfig struct {
	// SyncInterval is the interval between tenant list synchronizations.
	// Default: 30 seconds
	SyncInterval time.Duration

	// WorkersPerQueue is reserved for future use. It is currently not implemented
	// and has no effect on consumer behavior. Each queue runs a single consumer goroutine.
	// Default: 1
	//
	// Deprecated: This field is not yet implemented. Setting it has no effect.
	WorkersPerQueue int

	// PrefetchCount is the QoS prefetch count per channel.
	// Default: 10
	PrefetchCount int

	// MultiTenantURL is the fallback HTTP endpoint to fetch tenants if Redis cache misses.
	// Format: http://tenant-manager:4003
	MultiTenantURL string

	// Service is the service name to filter tenants by.
	// This is passed to tenant-manager when fetching tenant list.
	Service string

	// Environment is the deployment environment (e.g., "staging", "production").
	// Used to build environment-segmented Redis cache keys for active tenants.
	// When set together with Service, the Redis key becomes:
	// "tenant-manager:tenants:active:{Environment}:{Service}"
	Environment string

	// DiscoveryTimeout is the maximum time allowed for the initial tenant discovery
	// (fetching tenant IDs at startup). If zero, 500ms is used. Increase this for
	// high-latency or loaded environments where Redis or the tenant-manager API
	// may respond slowly; discovery is best-effort and the sync loop will retry.
	// Default: 500ms
	DiscoveryTimeout time.Duration
}

// DefaultMultiTenantConfig returns a MultiTenantConfig with sensible defaults.
func DefaultMultiTenantConfig() MultiTenantConfig {
	return MultiTenantConfig{
		SyncInterval:      30 * time.Second,
		WorkersPerQueue:   1,
		PrefetchCount:     10,
		DiscoveryTimeout: 500 * time.Millisecond,
	}
}

// retryStateEntry holds per-tenant retry state for connection failure resilience.
type retryStateEntry struct {
	mu         sync.Mutex
	retryCount int
	degraded   bool
}

// reset clears retry counters and degraded flag. Must be called with no other goroutine
// holding the entry's mutex (e.g. after Load from sync.Map).
func (e *retryStateEntry) reset() {
	e.mu.Lock()
	e.retryCount = 0
	e.degraded = false
	e.mu.Unlock()
}

// isDegraded returns whether the tenant is marked degraded.
func (e *retryStateEntry) isDegraded() bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	return e.degraded
}

// incRetryAndMaybeMarkDegraded increments retry count, optionally marks degraded if count >= max,
// and returns the backoff delay and current retry count. justMarkedDegraded is true only when
// the entry was not degraded and is now marked degraded by this call.
func (e *retryStateEntry) incRetryAndMaybeMarkDegraded(maxBeforeDegraded int) (delay time.Duration, retryCount int, justMarkedDegraded bool) {
	e.mu.Lock()
	defer e.mu.Unlock()

	delay = backoffDelay(e.retryCount)
	e.retryCount++

	prev := e.degraded
	if e.retryCount >= maxBeforeDegraded {
		e.degraded = true
	}

	justMarkedDegraded = !prev && e.degraded

	return delay, e.retryCount, justMarkedDegraded
}

// MultiTenantConsumerOption configures a MultiTenantConsumer.
type MultiTenantConsumerOption func(*MultiTenantConsumer)

// WithConsumerPostgresManager sets the PostgresManager on the consumer.
// When set, database connections for removed tenants are automatically closed
// during tenant synchronization.
func WithConsumerPostgresManager(p *PostgresManager) MultiTenantConsumerOption {
	return func(c *MultiTenantConsumer) { c.postgres = p }
}

// WithConsumerMongoManager sets the MongoManager on the consumer.
// When set, MongoDB connections for removed tenants are automatically closed
// during tenant synchronization.
func WithConsumerMongoManager(m *MongoManager) MultiTenantConsumerOption {
	return func(c *MultiTenantConsumer) { c.mongo = m }
}

// MultiTenantConsumer manages message consumption across multiple tenant vhosts.
// It dynamically discovers tenants from Redis cache and spawns consumer goroutines.
// In lazy mode, Run() populates knownTenants without starting consumers immediately.
// Consumers are spawned on-demand via ensureConsumerStarted() when the first message
// or external trigger arrives for a tenant.
type MultiTenantConsumer struct {
	rabbitmq     *RabbitMQManager
	redisClient  redis.UniversalClient
	pmClient     *Client // Tenant Manager client for fallback
	handlers     map[string]HandlerFunc
	tenants      map[string]context.CancelFunc // Active tenant goroutines
	knownTenants map[string]bool               // Discovered tenants (lazy mode: populated without starting consumers)
	// tenantAbsenceCount tracks consecutive syncs each tenant was missing from the fetched list.
	// Used to avoid removing tenants on a single transient incomplete fetch.
	tenantAbsenceCount map[string]int
	config             MultiTenantConfig
	mu           sync.RWMutex
	logger       libLog.Logger
	closed       bool

	// postgres manages PostgreSQL connections per tenant.
	// When set, connections are closed automatically when a tenant is removed.
	postgres *PostgresManager

	// mongo manages MongoDB connections per tenant.
	// When set, connections are closed automatically when a tenant is removed.
	mongo *MongoManager

	// consumerLocks provides per-tenant mutexes for double-check locking in ensureConsumerStarted.
	// Key: tenantID, Value: *sync.Mutex
	consumerLocks sync.Map

	// retryState holds per-tenant retry counters for connection failure resilience.
	// Key: tenantID, Value: *retryStateEntry
	retryState sync.Map

	// parentCtx is the context passed to Run(), stored for use by ensureConsumerStarted.
	parentCtx context.Context
}

// NewMultiTenantConsumer creates a new MultiTenantConsumer.
// Parameters:
//   - rabbitmq: RabbitMQ connection manager for tenant vhosts (must not be nil)
//   - redisClient: Redis client for tenant cache access (must not be nil)
//   - config: Consumer configuration
//   - logger: Logger for operational logging
//   - opts: Optional configuration options (e.g., WithConsumerPostgresManager, WithConsumerMongoManager)
//
// Panics if rabbitmq or redisClient is nil, as they are required for core functionality.
func NewMultiTenantConsumer(
	rabbitmq *RabbitMQManager,
	redisClient redis.UniversalClient,
	config MultiTenantConfig,
	logger libLog.Logger,
	opts ...MultiTenantConsumerOption,
) *MultiTenantConsumer {
	if rabbitmq == nil {
		panic("tenantmanager.NewMultiTenantConsumer: rabbitmq must not be nil")
	}

	if redisClient == nil {
		panic("tenantmanager.NewMultiTenantConsumer: redisClient must not be nil")
	}

	// Guard against nil logger to prevent panics downstream
	if logger == nil {
		logger = &libLog.NoneLogger{}
	}

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
		rabbitmq:           rabbitmq,
		redisClient:        redisClient,
		handlers:           make(map[string]HandlerFunc),
		tenants:            make(map[string]context.CancelFunc),
		knownTenants:       make(map[string]bool),
		tenantAbsenceCount: make(map[string]int),
		config:             config,
		logger:             logger,
	}

	// Apply optional configurations
	for _, opt := range opts {
		opt(consumer)
	}

	// Create Tenant Manager client for fallback if URL is configured
	if config.MultiTenantURL != "" {
		consumer.pmClient = NewClient(config.MultiTenantURL, logger)
	}

	return consumer
}

// Register adds a queue handler for all tenant vhosts.
// The handler will be invoked for messages from the specified queue in each tenant's vhost.
//
// Handlers should be registered before calling Run(). Handlers registered after Run()
// has been called will only take effect for tenants whose consumers are spawned after
// the registration; already-running tenant consumers will NOT pick up the new handler.
func (c *MultiTenantConsumer) Register(queueName string, handler HandlerFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.handlers[queueName] = handler
	c.logger.Infof("registered handler for queue: %s", queueName)
}

// Run starts the multi-tenant consumer in lazy mode.
// It discovers tenants without starting consumers (non-blocking) and starts
// background polling. Returns nil even on discovery failure (soft failure).
func (c *MultiTenantConsumer) Run(ctx context.Context) error {
	logger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)

	ctx, span := tracer.Start(ctx, "consumer.multi_tenant_consumer.run")
	defer span.End()

	// Store parent context for use by ensureConsumerStarted
	c.parentCtx = ctx

	// Discover tenants without blocking (soft failure - does not start consumers)
	c.discoverTenants(ctx)

	// Capture count under lock to avoid concurrent read race
	c.mu.RLock()
	knownCount := len(c.knownTenants)
	c.mu.RUnlock()

	logger.Infof("starting multi-tenant consumer, connection_mode=lazy, known_tenants=%d",
		knownCount)

	// Background polling - ASYNC
	go c.runSyncLoop(ctx)

	return nil
}

// discoverTenants fetches tenant IDs and populates knownTenants without starting consumers.
// This is the lazy mode discovery step: it records which tenants exist but defers consumer
// creation to background sync or on-demand triggers. Failures are logged as warnings
// (soft failure) and do not propagate errors to the caller.
// A short timeout is applied to avoid blocking startup on unresponsive infrastructure.
func (c *MultiTenantConsumer) discoverTenants(ctx context.Context) {
	logger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)

	ctx, span := tracer.Start(ctx, "consumer.multi_tenant_consumer.discover_tenants")
	defer span.End()

	// Apply a short timeout to prevent blocking startup when infrastructure is down.
	// Discovery is best-effort; the background sync loop will retry periodically.
	discoveryTimeout := c.config.DiscoveryTimeout
	if discoveryTimeout == 0 {
		discoveryTimeout = 500 * time.Millisecond
	}

	discoveryCtx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()

	tenantIDs, err := c.fetchTenantIDs(discoveryCtx)
	if err != nil {
		logger.Warnf("tenant discovery failed (soft failure, will retry in background): %v", err)
		libOpentelemetry.HandleSpanBusinessErrorEvent(&span, "tenant discovery failed (soft failure)", err)

		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for _, id := range tenantIDs {
		c.knownTenants[id] = true
	}

	logger.Infof("discovered %d tenants (lazy mode, no consumers started)", len(tenantIDs))
}

// runSyncLoop periodically syncs the tenant list.
// Each iteration creates its own span to avoid accumulating events on a long-lived span.
func (c *MultiTenantConsumer) runSyncLoop(ctx context.Context) {
	logger, _, _, _ := libCommons.NewTrackingFromContext(ctx) //nolint:dogsled

	ticker := time.NewTicker(c.config.SyncInterval)
	defer ticker.Stop()

	logger.Info("sync loop started")

	for {
		select {
		case <-ticker.C:
			c.runSyncIteration(ctx)
		case <-ctx.Done():
			logger.Info("sync loop stopped: context cancelled")
			return
		}
	}
}

// runSyncIteration executes a single sync iteration with its own span.
func (c *MultiTenantConsumer) runSyncIteration(ctx context.Context) {
	logger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)

	ctx, span := tracer.Start(ctx, "consumer.multi_tenant_consumer.sync_iteration")
	defer span.End()

	if err := c.syncTenants(ctx); err != nil {
		logger.Warnf("tenant sync failed (continuing): %v", err)
		libOpentelemetry.HandleSpanBusinessErrorEvent(&span, "tenant sync failed (continuing)", err)
	}

	// Revalidate connection settings for active tenants.
	// This runs outside syncTenants to avoid holding c.mu during HTTP calls.
	c.revalidateConnectionSettings(ctx)
}

// syncTenants fetches tenant IDs and updates the known tenant registry.
// In lazy mode, new tenants are added to knownTenants but consumers are NOT started.
// Consumer spawning is deferred to on-demand triggers (e.g., ensureConsumerStarted).
// Tenants missing from the fetched list are retained in knownTenants for up to
// absentSyncsBeforeRemoval consecutive syncs; only after that threshold are they
// removed from knownTenants and any active consumers stopped. This avoids purging
// tenants on a single transient incomplete fetch.
// Error handling: if fetchTenantIDs fails, syncTenants returns the error immediately
// without modifying the current tenant state. The caller (runSyncIteration) logs
// the failure and continues retrying on the next sync interval.
func (c *MultiTenantConsumer) syncTenants(ctx context.Context) error {
	logger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)

	ctx, span := tracer.Start(ctx, "consumer.multi_tenant_consumer.sync_tenants")
	defer span.End()

	// Fetch tenant IDs from Redis cache
	tenantIDs, err := c.fetchTenantIDs(ctx)
	if err != nil {
		logger.Errorf("failed to fetch tenant IDs: %v", err)
		libOpentelemetry.HandleSpanError(&span, "failed to fetch tenant IDs", err)

		return fmt.Errorf("failed to fetch tenant IDs: %w", err)
	}

	// Validate tenant IDs before processing

	validTenantIDs := make([]string, 0, len(tenantIDs))

	for _, id := range tenantIDs {
		if isValidTenantID(id) {
			validTenantIDs = append(validTenantIDs, id)
		} else {
			logger.Warnf("skipping invalid tenant ID: %q", id)
		}
	}

	// Create a set of current tenant IDs for quick lookup

	currentTenants := make(map[string]bool, len(validTenantIDs))
	for _, id := range validTenantIDs {
		currentTenants[id] = true
	}

	c.mu.Lock()

	defer c.mu.Unlock()

	if c.closed {
		return fmt.Errorf("consumer is closed")
	}

	// Snapshot previous known tenants so we can retain those missing briefly from the fetch.
	previousKnown := make(map[string]bool, len(c.knownTenants))
	for id := range c.knownTenants {
		previousKnown[id] = true
	}

	// Build new knownTenants: all currently fetched plus any previously known that are
	// missing for fewer than absentSyncsBeforeRemoval consecutive syncs.
	newKnown := make(map[string]bool, len(currentTenants)+len(previousKnown))

	var removedTenants []string

	for id := range currentTenants {
		newKnown[id] = true
		c.tenantAbsenceCount[id] = 0
	}

	for id := range previousKnown {
		if currentTenants[id] {
			continue
		}

		abs := c.tenantAbsenceCount[id] + 1

		c.tenantAbsenceCount[id] = abs
		if abs < absentSyncsBeforeRemoval {
			newKnown[id] = true
		} else {
			delete(c.tenantAbsenceCount, id)

			if _, running := c.tenants[id]; running {
				removedTenants = append(removedTenants, id)
			}
		}
	}

	c.knownTenants = newKnown

	// Identify NEW tenants (in current list but not running)

	var newTenants []string

	for _, tenantID := range validTenantIDs {
		if _, exists := c.tenants[tenantID]; !exists {
			newTenants = append(newTenants, tenantID)
		}
	}

	// Stop removed tenants and close their database connections
	c.stopRemovedTenants(ctx, removedTenants, logger)

	// Lazy mode: new tenants are recorded in knownTenants (already done above)
	// but consumers are NOT started here. Consumer spawning is deferred to
	// on-demand triggers (e.g., ensureConsumerStarted in T-002).
	if len(newTenants) > 0 {
		logger.Infof("discovered %d new tenants (lazy mode, consumers deferred): %v",
			len(newTenants), newTenants)
	}

	logger.Infof("sync complete: %d known, %d active, %d discovered, %d removed",
		len(c.knownTenants), len(c.tenants), len(newTenants), len(removedTenants))

	return nil
}

// stopRemovedTenants cancels consumer goroutines and closes database connections for
// tenants that have been removed from the known tenant registry.
// Caller MUST hold c.mu write lock.
func (c *MultiTenantConsumer) stopRemovedTenants(ctx context.Context, removedTenants []string, logger libLog.Logger) {
	for _, tenantID := range removedTenants {
		logger.Infof("stopping consumer for removed tenant: %s", tenantID)

		if cancel, ok := c.tenants[tenantID]; ok {
			cancel()
			delete(c.tenants, tenantID)
		}

		// Close database connections for removed tenant
		if c.rabbitmq != nil {
			if err := c.rabbitmq.CloseConnection(tenantID); err != nil {
				logger.Warnf("failed to close RabbitMQ connection for tenant %s: %v", tenantID, err)
			}
		}

		if c.postgres != nil {
			if err := c.postgres.CloseConnection(tenantID); err != nil {
				logger.Warnf("failed to close PostgreSQL connection for tenant %s: %v", tenantID, err)
			}
		}

		if c.mongo != nil {
			if err := c.mongo.CloseClient(ctx, tenantID); err != nil {
				logger.Warnf("failed to close MongoDB connection for tenant %s: %v", tenantID, err)
			}
		}
	}
}

// revalidateConnectionSettings fetches current settings from the Tenant Manager
// for each active tenant and applies any changed connection pool settings to
// existing PostgreSQL and MongoDB connections.
//
// For PostgreSQL, SetMaxOpenConns/SetMaxIdleConns are thread-safe and take effect
// immediately for new connections from the pool without recreating the connection.
// For MongoDB, the driver does not support pool resize after creation, so a warning
// is logged and changes take effect on the next connection recreation.
//
// This method is called after syncTenants in each sync iteration. Errors fetching
// config for individual tenants are logged and skipped (will retry next cycle).
// If the Tenant Manager is down, the circuit breaker handles fast-fail.
func (c *MultiTenantConsumer) revalidateConnectionSettings(ctx context.Context) {
	if c.postgres == nil && c.mongo == nil {
		return
	}

	if c.pmClient == nil || c.config.Service == "" {
		return
	}

	logger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)

	ctx, span := tracer.Start(ctx, "consumer.multi_tenant_consumer.revalidate_connection_settings")
	defer span.End()

	// Snapshot current tenant IDs under lock to avoid holding the lock during HTTP calls
	c.mu.RLock()

	tenantIDs := make([]string, 0, len(c.tenants))
	for tenantID := range c.tenants {
		tenantIDs = append(tenantIDs, tenantID)
	}

	c.mu.RUnlock()

	if len(tenantIDs) == 0 {
		return
	}

	var revalidated int

	for _, tenantID := range tenantIDs {
		config, err := c.pmClient.GetTenantConfig(ctx, tenantID, c.config.Service)
		if err != nil {
			logger.Warnf("failed to fetch config for tenant %s during settings revalidation: %v", tenantID, err)
			continue // skip on error, will retry next cycle
		}

		if c.postgres != nil {
			c.postgres.ApplyConnectionSettings(tenantID, config)
		}

		if c.mongo != nil {
			c.mongo.ApplyConnectionSettings(tenantID, config)
		}

		revalidated++
	}

	if revalidated > 0 {
		logger.Infof("revalidated connection settings for %d/%d active tenants", revalidated, len(tenantIDs))
	}
}

// fetchTenantIDs gets tenant IDs from Redis cache, falling back to Tenant Manager API.
func (c *MultiTenantConsumer) fetchTenantIDs(ctx context.Context) ([]string, error) {
	logger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)

	ctx, span := tracer.Start(ctx, "consumer.multi_tenant_consumer.fetch_tenant_ids")
	defer span.End()

	// Build environment+service segmented Redis key
	cacheKey := buildActiveTenantsKey(c.config.Environment, c.config.Service)

	// Try Redis cache first
	tenantIDs, err := c.redisClient.SMembers(ctx, cacheKey).Result()
	if err == nil && len(tenantIDs) > 0 {
		logger.Infof("fetched %d tenant IDs from cache", len(tenantIDs))
		return tenantIDs, nil
	}

	if err != nil {
		logger.Warnf("Redis cache fetch failed: %v", err)
		libOpentelemetry.HandleSpanBusinessErrorEvent(&span, "Redis cache fetch failed", err)
	}

	// Fallback to Tenant Manager API
	if c.pmClient != nil && c.config.Service != "" {
		logger.Info("falling back to Tenant Manager API for tenant list")

		tenants, apiErr := c.pmClient.GetActiveTenantsByService(ctx, c.config.Service)
		if apiErr != nil {
			logger.Errorf("Tenant Manager API fallback failed: %v", apiErr)
			libOpentelemetry.HandleSpanError(&span, "Tenant Manager API fallback failed", apiErr)
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

		logger.Infof("fetched %d tenant IDs from Tenant Manager API", len(ids))

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
	logger, tracer, _, _ := libCommons.NewTrackingFromContext(parentCtx)

	parentCtx, span := tracer.Start(parentCtx, "consumer.multi_tenant_consumer.start_tenant_consumer")
	defer span.End()

	// Create a cancellable context for this tenant
	tenantCtx, cancel := context.WithCancel(parentCtx)

	// Store the cancel function (caller holds lock)
	c.tenants[tenantID] = cancel

	logger.Infof("starting consumer for tenant: %s", tenantID)

	// Spawn consumer goroutine
	go c.consumeForTenant(tenantCtx, tenantID)
}

// consumeForTenant runs the consumer loop for a single tenant.
func (c *MultiTenantConsumer) consumeForTenant(ctx context.Context, tenantID string) {
	// Set tenantID in context for handlers
	ctx = SetTenantIDInContext(ctx, tenantID)

	logger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)

	ctx, span := tracer.Start(ctx, "consumer.multi_tenant_consumer.consume_for_tenant")
	defer span.End()

	logger = logger.WithFields("tenant_id", tenantID)
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
// Each connection attempt creates a short-lived span to avoid accumulating events
// on a long-lived span that would grow unbounded over the consumer's lifetime.
func (c *MultiTenantConsumer) consumeQueue(
	ctx context.Context,
	tenantID string,
	queueName string,
	handler HandlerFunc,
	_ libLog.Logger,
) {
	ctxLogger, _, _, _ := libCommons.NewTrackingFromContext(ctx) //nolint:dogsled
	logger := ctxLogger.WithFields("tenant_id", tenantID, "queue", queueName)

	// Guard against nil RabbitMQ manager (e.g., during lazy mode testing)

	if c.rabbitmq == nil {
		logger.Warn("RabbitMQ manager is nil, cannot consume from queue")
		return
	}

	for {
		select {
		case <-ctx.Done():
			logger.Info("queue consumer stopped")
			return
		default:
		}

		shouldContinue := c.attemptConsumeConnection(ctx, tenantID, queueName, handler, logger)
		if !shouldContinue {
			return
		}

		logger.Warn("channel closed, reconnecting...")
	}
}

// attemptConsumeConnection attempts to establish a channel and consume messages.
// Returns true if the loop should continue (reconnect), false if it should stop.
// Uses exponential backoff with per-tenant retry state for connection failures.
func (c *MultiTenantConsumer) attemptConsumeConnection(
	ctx context.Context,
	tenantID string,
	queueName string,
	handler HandlerFunc,
	logger libLog.Logger,
) bool {
	_, tracer, _, _ := libCommons.NewTrackingFromContext(ctx) //nolint:dogsled

	connCtx, span := tracer.Start(ctx, "consumer.multi_tenant_consumer.consume_connection")
	defer span.End()

	state := c.getRetryState(tenantID)

	// Get channel for this tenant's vhost
	ch, err := c.rabbitmq.GetChannel(connCtx, tenantID)
	if err != nil {
		delay, retryCount, justMarkedDegraded := state.incRetryAndMaybeMarkDegraded(maxRetryBeforeDegraded)
		if justMarkedDegraded {
			logger.Warnf("tenant %s marked as degraded after %d consecutive failures", tenantID, retryCount)
		}

		logger.Warnf("failed to get channel for tenant %s, retrying in %s (attempt %d): %v",
			tenantID, delay, retryCount, err)
		libOpentelemetry.HandleSpanError(&span, "failed to get channel", err)

		select {
		case <-ctx.Done():
			return false
		case <-time.After(delay):
			return true
		}
	}

	// Set QoS

	if err := ch.Qos(c.config.PrefetchCount, 0, false); err != nil {
		ch.Close() // Close channel to prevent leak

		delay, retryCount, justMarkedDegraded := state.incRetryAndMaybeMarkDegraded(maxRetryBeforeDegraded)
		if justMarkedDegraded {
			logger.Warnf("tenant %s marked as degraded after %d consecutive failures", tenantID, retryCount)
		}

		logger.Warnf("failed to set QoS for tenant %s, retrying in %s (attempt %d): %v",
			tenantID, delay, retryCount, err)
		libOpentelemetry.HandleSpanError(&span, "failed to set QoS", err)

		select {
		case <-ctx.Done():
			return false
		case <-time.After(delay):
			return true
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
		ch.Close() // Close channel to prevent leak

		delay, retryCount, justMarkedDegraded := state.incRetryAndMaybeMarkDegraded(maxRetryBeforeDegraded)
		if justMarkedDegraded {
			logger.Warnf("tenant %s marked as degraded after %d consecutive failures", tenantID, retryCount)
		}

		logger.Warnf("failed to start consuming for tenant %s, retrying in %s (attempt %d): %v",
			tenantID, delay, retryCount, err)
		libOpentelemetry.HandleSpanError(&span, "failed to start consuming", err)

		select {
		case <-ctx.Done():
			return false
		case <-time.After(delay):
			return true
		}
	}

	// Connection succeeded: reset retry state
	c.resetRetryState(tenantID)

	logger.Infof("consuming started for tenant %s on queue %s", tenantID, queueName)

	// Setup channel close notification
	notifyClose := make(chan *amqp.Error, 1)
	ch.NotifyClose(notifyClose)

	// Process messages (blocks until channel closes or context is cancelled)
	c.processMessages(ctx, tenantID, queueName, handler, msgs, notifyClose, logger)

	return true
}

// processMessages processes messages from the channel until it closes.
// Each message is processed with its own span to avoid accumulating events on a long-lived span.
func (c *MultiTenantConsumer) processMessages(
	ctx context.Context,
	tenantID string,
	queueName string,
	handler HandlerFunc,
	msgs <-chan amqp.Delivery,
	notifyClose <-chan *amqp.Error,
	_ libLog.Logger,
) {
	ctxLogger, _, _, _ := libCommons.NewTrackingFromContext(ctx) //nolint:dogsled
	logger := ctxLogger.WithFields("tenant_id", tenantID, "queue", queueName)

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

			c.handleMessage(ctx, tenantID, queueName, handler, msg, logger)
		}
	}
}

// handleMessage processes a single message with its own span.
func (c *MultiTenantConsumer) handleMessage(
	ctx context.Context,
	tenantID string,
	queueName string,
	handler HandlerFunc,
	msg amqp.Delivery,
	logger libLog.Logger,
) {
	_, tracer, _, _ := libCommons.NewTrackingFromContext(ctx) //nolint:dogsled

	// Process message with tenant context
	msgCtx := SetTenantIDInContext(ctx, tenantID)

	// Extract trace context from message headers
	msgCtx = libOpentelemetry.ExtractTraceContextFromQueueHeaders(msgCtx, msg.Headers)

	// Create a per-message span
	msgCtx, span := tracer.Start(msgCtx, "consumer.multi_tenant_consumer.handle_message")
	defer span.End()

	if err := handler(msgCtx, msg); err != nil {
		logger.Errorf("handler error for queue %s: %v", queueName, err)
		libOpentelemetry.HandleSpanBusinessErrorEvent(&span, "handler error", err)

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

// initialBackoff is the base delay for exponential backoff on connection failures.
const initialBackoff = 5 * time.Second

// maxBackoff is the maximum delay between retry attempts.
const maxBackoff = 40 * time.Second

// maxRetryBeforeDegraded is the number of consecutive failures before marking a tenant as degraded.
const maxRetryBeforeDegraded = 3

// backoffDelay calculates the exponential backoff delay for a given retry count.
// The formula is: min(initialBackoff * 2^retryCount, maxBackoff).
// Sequence: 5s, 10s, 20s, 40s, 40s, ...
func backoffDelay(retryCount int) time.Duration {
	delay := initialBackoff
	for i := 0; i < retryCount; i++ {
		delay *= 2
		if delay > maxBackoff {
			return maxBackoff
		}
	}

	return delay
}

// getRetryState returns the retry state entry for a tenant, creating one if it does not exist.
func (c *MultiTenantConsumer) getRetryState(tenantID string) *retryStateEntry {
	entry, _ := c.retryState.LoadOrStore(tenantID, &retryStateEntry{})
	return entry.(*retryStateEntry)
}

// resetRetryState resets the retry counter and degraded flag for a tenant after a successful connection.
// It reuses the existing entry when present (reset in place) to avoid allocation churn; only stores
// a new entry when the tenant has no entry yet.
func (c *MultiTenantConsumer) resetRetryState(tenantID string) {
	if entry, ok := c.retryState.Load(tenantID); ok {
		if state, ok := entry.(*retryStateEntry); ok {
			state.reset()
			return
		}
	}

	c.retryState.Store(tenantID, &retryStateEntry{})
}

// ensureConsumerStarted ensures a consumer is running for the given tenant.
// It uses double-check locking with a per-tenant mutex to guarantee exactly-once
// consumer spawning under concurrent access.
// This is the primary entry point for on-demand consumer creation in lazy mode.
func (c *MultiTenantConsumer) ensureConsumerStarted(ctx context.Context, tenantID string) {
	logger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)

	ctx, span := tracer.Start(ctx, "consumer.multi_tenant_consumer.ensure_consumer_started")
	defer span.End()

	// Fast path: check if consumer is already active (read lock only)

	c.mu.RLock()

	_, exists := c.tenants[tenantID]
	closed := c.closed
	c.mu.RUnlock()

	if exists || closed {
		return
	}

	// Slow path: acquire per-tenant mutex for double-check locking
	lockVal, _ := c.consumerLocks.LoadOrStore(tenantID, &sync.Mutex{})
	tenantMu := lockVal.(*sync.Mutex)

	tenantMu.Lock()
	defer tenantMu.Unlock()

	// Double-check under per-tenant lock

	c.mu.RLock()
	_, exists = c.tenants[tenantID]
	closed = c.closed
	c.mu.RUnlock()

	if exists || closed {
		return
	}

	// Use stored parentCtx if available (from Run()), otherwise use the provided ctx
	startCtx := ctx
	if c.parentCtx != nil {
		startCtx = c.parentCtx
	}

	logger.Infof("on-demand consumer start for tenant: %s", tenantID)

	c.mu.Lock()
	c.startTenantConsumer(startCtx, tenantID)
	c.mu.Unlock()
}

// EnsureConsumerStarted is the public API for triggering on-demand consumer spawning.
// It is safe for concurrent use by multiple goroutines.
// If the consumer for the given tenant is already running, this is a no-op.
func (c *MultiTenantConsumer) EnsureConsumerStarted(ctx context.Context, tenantID string) {
	c.ensureConsumerStarted(ctx, tenantID)
}

// IsDegraded returns true if the given tenant is currently in a degraded state
// due to repeated connection failures (>= maxRetryBeforeDegraded consecutive failures).
func (c *MultiTenantConsumer) IsDegraded(tenantID string) bool {
	entry, ok := c.retryState.Load(tenantID)
	if !ok {
		return false
	}

	state, ok := entry.(*retryStateEntry)
	if !ok {
		return false
	}

	return state.isDegraded()
}

// isValidTenantID validates a tenant ID against security constraints.
// Valid tenant IDs must be non-empty, within the max length, and match the allowed character pattern.
func isValidTenantID(id string) bool {
	if id == "" || len(id) > maxTenantIDLength {
		return false
	}

	return validTenantIDPattern.MatchString(id)
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

	// Clear the maps

	c.tenants = make(map[string]context.CancelFunc)
	c.knownTenants = make(map[string]bool)
	c.tenantAbsenceCount = make(map[string]int)

	c.logger.Info("multi-tenant consumer closed")

	return nil
}

// Stats returns statistics about the consumer including lazy mode metadata.
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

	knownTenantIDs := make([]string, 0, len(c.knownTenants))
	for id := range c.knownTenants {
		knownTenantIDs = append(knownTenantIDs, id)
	}

	// Compute pending tenants (known but not yet active)

	pendingTenantIDs := make([]string, 0)

	for id := range c.knownTenants {
		if _, active := c.tenants[id]; !active {
			pendingTenantIDs = append(pendingTenantIDs, id)
		}
	}

	// Collect degraded tenants from retry state
	degradedTenantIDs := make([]string, 0)

	c.retryState.Range(func(key, value any) bool {
		if entry, ok := value.(*retryStateEntry); ok && entry.isDegraded() {
			degradedTenantIDs = append(degradedTenantIDs, key.(string))
		}

		return true
	})

	return MultiTenantConsumerStats{
		ActiveTenants:    len(c.tenants),
		TenantIDs:        tenantIDs,
		RegisteredQueues: queueNames,
		Closed:           c.closed,
		ConnectionMode:   "lazy",
		KnownTenants:     len(c.knownTenants),
		KnownTenantIDs:   knownTenantIDs,
		PendingTenants:   len(pendingTenantIDs),
		PendingTenantIDs: pendingTenantIDs,
		DegradedTenants:  degradedTenantIDs,
	}
}

// MultiTenantConsumerStats holds statistics for the consumer.
type MultiTenantConsumerStats struct {
	ActiveTenants    int      `json:"activeTenants"`
	TenantIDs        []string `json:"tenantIds"`
	RegisteredQueues []string `json:"registeredQueues"`
	Closed           bool     `json:"closed"`
	ConnectionMode   string   `json:"connectionMode"`
	KnownTenants     int      `json:"knownTenants"`
	KnownTenantIDs   []string `json:"knownTenantIds"`
	PendingTenants   int      `json:"pendingTenants"`
	PendingTenantIDs []string `json:"pendingTenantIds"`
	DegradedTenants  []string `json:"degradedTenants"`
}

// Prometheus-compatible metric name constants for multi-tenant consumer observability.
// These constants provide a standardized naming scheme for metrics instrumentation.
const (
	// MetricTenantConnectionsTotal tracks the total number of tenant connections established.
	MetricTenantConnectionsTotal = "tenant_connections_total"
	// MetricTenantConnectionErrors tracks connection errors by tenant.
	MetricTenantConnectionErrors = "tenant_connection_errors_total"
	// MetricTenantConsumersActive tracks the number of currently active tenant consumers.
	MetricTenantConsumersActive = "tenant_consumers_active"
	// MetricTenantMessageProcessed tracks the total number of messages processed per tenant.
	MetricTenantMessageProcessed = "tenant_messages_processed_total"
)
