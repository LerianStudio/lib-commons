// Package consumer provides multi-tenant message queue consumption management.
package consumer

import (
	"context"
	crand "crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"maps"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/redis/go-redis/v9"

	libCommons "github.com/LerianStudio/lib-commons/v4/commons"
	libLog "github.com/LerianStudio/lib-commons/v4/commons/log"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v4/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/logcompat"
	tmmongo "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/mongo"
	tmpostgres "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/postgres"
	tmrabbitmq "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/rabbitmq"
)

// absentSyncsBeforeRemoval is the number of consecutive syncs a tenant can be
// missing from the fetched list before it is removed from knownTenants and
// any active consumer is stopped. Prevents transient incomplete fetches from
// purging tenants immediately.
const absentSyncsBeforeRemoval = 3

// buildActiveTenantsKey returns an environment+service segmented Redis key for active tenants.
// The key format is always: "tenant-manager:tenants:active:{env}:{service}"
// The caller is responsible for providing valid env and service values.
func buildActiveTenantsKey(env, service string) string {
	return fmt.Sprintf("tenant-manager:tenants:active:%s:%s", env, service)
}

// HandlerFunc is a function that processes messages from a queue.
// The context contains the tenant ID via core.SetTenantIDInContext.
type HandlerFunc func(ctx context.Context, delivery amqp.Delivery) error

// MultiTenantConfig holds configuration for the MultiTenantConsumer.
type MultiTenantConfig struct {
	// SyncInterval is the interval between tenant list synchronizations.
	// Default: 30 seconds
	SyncInterval time.Duration

	// WorkersPerQueue is reserved for future use. It is currently not implemented
	// and has no effect on consumer behavior. Each queue runs a single consumer goroutine.
	// Setting this field is a no-op; it is retained only for backward compatibility.
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

	// EagerStart controls whether consumers are started automatically when new tenants
	// are discovered. When true (default), syncTenants() and Run() will call
	// ensureConsumerStarted() for each discovered tenant, starting consumer goroutines
	// immediately. When false, consumers are only started on-demand via the public
	// EnsureConsumerStarted() API (lazy mode).
	// Default: true
	EagerStart bool
}

// DefaultMultiTenantConfig returns a MultiTenantConfig with sensible defaults.
func DefaultMultiTenantConfig() MultiTenantConfig {
	return MultiTenantConfig{
		SyncInterval:     30 * time.Second,
		PrefetchCount:    10,
		DiscoveryTimeout: 500 * time.Millisecond,
		EagerStart:       true,
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

// Option configures a MultiTenantConsumer.
type Option func(*MultiTenantConsumer)

// WithPostgresManager sets the postgres Manager on the consumer.
// When set, database connections for removed tenants are automatically closed
// during tenant synchronization.
func WithPostgresManager(p *tmpostgres.Manager) Option {
	return func(c *MultiTenantConsumer) { c.postgres = p }
}

// WithMongoManager sets the mongo Manager on the consumer.
// When set, MongoDB connections for removed tenants are automatically closed
// during tenant synchronization.
func WithMongoManager(m *tmmongo.Manager) Option {
	return func(c *MultiTenantConsumer) { c.mongo = m }
}

// MultiTenantConsumer manages message consumption across multiple tenant vhosts.
// It dynamically discovers tenants from Redis cache and spawns consumer goroutines.
// In lazy mode, Run() populates knownTenants without starting consumers immediately.
// Consumers are spawned on-demand via ensureConsumerStarted() when the first message
// or external trigger arrives for a tenant.
type MultiTenantConsumer struct {
	rabbitmq     *tmrabbitmq.Manager
	redisClient  redis.UniversalClient
	pmClient     *client.Client // Tenant Manager client for fallback
	handlers     map[string]HandlerFunc
	tenants      map[string]context.CancelFunc // Active tenant goroutines
	knownTenants map[string]bool               // Discovered tenants (lazy mode: populated without starting consumers)
	// tenantAbsenceCount tracks consecutive syncs each tenant was missing from the fetched list.
	// Used to avoid removing tenants on a single transient incomplete fetch.
	tenantAbsenceCount map[string]int
	config             MultiTenantConfig
	mu                 sync.RWMutex
	logger             *logcompat.Logger
	closed             bool

	// postgres manages PostgreSQL connections per tenant.
	// When set, connections are closed automatically when a tenant is removed.
	postgres *tmpostgres.Manager

	// mongo manages MongoDB connections per tenant.
	// When set, connections are closed automatically when a tenant is removed.
	mongo *tmmongo.Manager

	// consumerLocks provides per-tenant mutexes for double-check locking in ensureConsumerStarted.
	// Key: tenantID, Value: *sync.Mutex
	consumerLocks sync.Map

	// retryState holds per-tenant retry counters for connection failure resilience.
	// Key: tenantID, Value: *retryStateEntry
	retryState sync.Map

	// parentCtx is the context passed to Run(), stored for use by ensureConsumerStarted.
	parentCtx context.Context

	// syncLoopCancel cancels the context used by the sync loop goroutine.
	// Stored in Run() and called in Close() to ensure the sync loop stops
	// even when the original context (e.g., context.Background()) is never cancelled.
	syncLoopCancel context.CancelFunc
}

// NewMultiTenantConsumerWithError creates a new MultiTenantConsumer.
// Parameters:
//   - rabbitmq: RabbitMQ connection manager for tenant vhosts (must not be nil)
//   - redisClient: Redis client for tenant cache access (must not be nil)
//   - config: Consumer configuration
//   - logger: Logger for operational logging
//   - opts: Optional configuration options (e.g., WithPostgresManager, WithMongoManager)
//
// Returns an error if rabbitmq or redisClient is nil, as they are required for core functionality.
func NewMultiTenantConsumerWithError(
	rabbitmq *tmrabbitmq.Manager,
	redisClient redis.UniversalClient,
	config MultiTenantConfig,
	logger libLog.Logger,
	opts ...Option,
) (*MultiTenantConsumer, error) {
	if rabbitmq == nil {
		return nil, errors.New("consumer.NewMultiTenantConsumerWithError: rabbitmq must not be nil")
	}

	if redisClient == nil {
		return nil, errors.New("consumer.NewMultiTenantConsumerWithError: redisClient must not be nil")
	}

	// Guard against nil logger to prevent panics downstream
	if logger == nil {
		logger = libLog.NewNop()
	}

	// Apply defaults
	if config.SyncInterval <= 0 {
		config.SyncInterval = 30 * time.Second
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
		logger:             logcompat.New(logger),
	}

	// Apply optional configurations
	for _, opt := range opts {
		opt(consumer)
	}

	// Create Tenant Manager client for fallback if URL is configured
	if config.MultiTenantURL != "" {
		pmClient, err := client.NewClient(config.MultiTenantURL, consumer.logger.Base())
		if err != nil {
			return nil, fmt.Errorf("consumer.NewMultiTenantConsumerWithError: invalid MultiTenantURL: %w", err)
		}

		consumer.pmClient = pmClient
	}

	if config.WorkersPerQueue > 0 {
		consumer.logger.Base().Log(context.Background(), libLog.LevelWarn,
			"WorkersPerQueue is deprecated and has no effect; the field is reserved for future use",
			libLog.Int("workers_per_queue", config.WorkersPerQueue))
	}

	return consumer, nil
}

// Register adds a queue handler for all tenant vhosts.
// The handler will be invoked for messages from the specified queue in each tenant's vhost.
//
// Handlers should be registered before calling Run(). Handlers registered after Run()
// has been called will only take effect for tenants whose consumers are spawned after
// the registration; already-running tenant consumers will NOT pick up the new handler.
//
// Returns an error if handler is nil.
func (c *MultiTenantConsumer) Register(queueName string, handler HandlerFunc) error {
	if handler == nil {
		return fmt.Errorf("consumer.Register: queue %q: %w", queueName, core.ErrNilHandlerFunc)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.handlers[queueName] = handler
	c.logger.Infof("registered handler for queue: %s", queueName)

	return nil
}

// Run starts the multi-tenant consumer.
// It discovers tenants (non-blocking, soft failure) and starts background polling.
// When EagerStart is true (default), consumers are started immediately for all
// discovered tenants. When false, consumers are deferred to on-demand triggers.
// Returns nil even on discovery failure (soft failure).
func (c *MultiTenantConsumer) Run(ctx context.Context) error {
	baseLogger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)
	logger := logcompat.New(baseLogger)

	// Fall back to constructor logger when context has no logger attached
	// (e.g., context.Background()). This prevents silent log loss.
	if c.logger != nil {
		logger = c.logger
	}

	ctx, span := tracer.Start(ctx, "consumer.multi_tenant_consumer.run")
	defer span.End()

	// Store parent context for use by ensureConsumerStarted.
	// Protected by c.mu because ensureConsumerStarted reads it concurrently.
	c.mu.Lock()
	c.parentCtx = ctx
	c.mu.Unlock()

	connectionMode := "lazy"
	if c.config.EagerStart {
		connectionMode = "eager"
	}

	// Discover tenants without blocking (soft failure - does not start consumers)
	c.discoverTenants(ctx)

	// Capture count under lock to avoid concurrent read race
	c.mu.RLock()
	knownCount := len(c.knownTenants)
	c.mu.RUnlock()

	logger.InfofCtx(ctx, "starting multi-tenant consumer, connection_mode=%s, known_tenants=%d",
		connectionMode, knownCount)

	// Eager mode: start consumers for all discovered tenants immediately
	if c.config.EagerStart && knownCount > 0 {
		c.eagerStartKnownTenants(ctx)
	}

	// Background polling - ASYNC
	// Create a derived context so Close() can stop the sync loop even when
	// the caller passes a never-cancelled context (e.g., context.Background()).
	syncCtx, syncCancel := context.WithCancel(ctx) //#nosec G118 -- cancel is stored in c.syncLoopCancel and called by Close()
	c.syncLoopCancel = syncCancel

	go c.syncActiveTenants(syncCtx)

	return nil
}

// eagerStartKnownTenants starts consumers for all known tenants.
// Called during Run() when EagerStart is true and tenants were discovered.
func (c *MultiTenantConsumer) eagerStartKnownTenants(ctx context.Context) {
	c.mu.RLock()

	tenantIDs := make([]string, 0, len(c.knownTenants))
	for id := range c.knownTenants {
		tenantIDs = append(tenantIDs, id)
	}

	c.mu.RUnlock()

	c.logger.InfofCtx(ctx, "eager start: bootstrapping consumers for %d tenants", len(tenantIDs))

	for _, tenantID := range tenantIDs {
		c.ensureConsumerStarted(ctx, tenantID)
	}
}

// discoverTenants fetches tenant IDs and populates knownTenants without starting consumers.
// This is the lazy mode discovery step: it records which tenants exist but defers consumer
// creation to background sync or on-demand triggers. Failures are logged as warnings
// (soft failure) and do not propagate errors to the caller.
// A short timeout is applied to avoid blocking startup on unresponsive infrastructure.
func (c *MultiTenantConsumer) discoverTenants(ctx context.Context) {
	baseLogger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)
	logger := logcompat.New(baseLogger)

	if c.logger != nil {
		logger = c.logger
	}

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
		logger.WarnfCtx(ctx, "tenant discovery failed (soft failure, will retry in background): %v", err)
		libOpentelemetry.HandleSpanBusinessErrorEvent(span, "tenant discovery failed (soft failure)", err)

		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	for _, id := range tenantIDs {
		c.knownTenants[id] = true
	}

	logger.InfofCtx(ctx, "discovered %d tenants (lazy mode, no consumers started)", len(tenantIDs))
}

// syncActiveTenants periodically syncs the tenant list.
// Each iteration creates its own span to avoid accumulating events on a long-lived span.
func (c *MultiTenantConsumer) syncActiveTenants(ctx context.Context) {
	baseLogger, _, _, _ := libCommons.NewTrackingFromContext(ctx) //nolint:dogsled
	logger := logcompat.New(baseLogger)

	if c.logger != nil {
		logger = c.logger
	}

	ticker := time.NewTicker(c.config.SyncInterval)
	defer ticker.Stop()

	logger.InfoCtx(ctx, "sync loop started")

	for {
		select {
		case <-ticker.C:
			c.runSyncIteration(ctx)
		case <-ctx.Done():
			logger.InfoCtx(ctx, "sync loop stopped: context cancelled")
			return
		}
	}
}

// runSyncIteration executes a single sync iteration with its own span.
func (c *MultiTenantConsumer) runSyncIteration(ctx context.Context) {
	baseLogger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)
	logger := logcompat.New(baseLogger)

	if c.logger != nil {
		logger = c.logger
	}

	ctx, span := tracer.Start(ctx, "consumer.multi_tenant_consumer.sync_iteration")
	defer span.End()

	if err := c.syncTenants(ctx); err != nil {
		logger.WarnfCtx(ctx, "tenant sync failed (continuing): %v", err)
		libOpentelemetry.HandleSpanBusinessErrorEvent(span, "tenant sync failed (continuing)", err)
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
	baseLogger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)
	logger := logcompat.New(baseLogger)

	if c.logger != nil {
		logger = c.logger
	}

	ctx, span := tracer.Start(ctx, "consumer.multi_tenant_consumer.sync_tenants")
	defer span.End()

	// Fetch tenant IDs from Redis cache
	tenantIDs, err := c.fetchTenantIDs(ctx)
	if err != nil {
		logger.ErrorfCtx(ctx, "failed to fetch tenant IDs: %v", err)
		libOpentelemetry.HandleSpanError(span, "failed to fetch tenant IDs", err)

		return fmt.Errorf("failed to fetch tenant IDs: %w", err)
	}

	validTenantIDs, currentTenants := c.filterValidTenantIDs(ctx, tenantIDs, logger)

	c.mu.Lock()

	if c.closed {
		c.mu.Unlock()
		return errors.New("consumer is closed")
	}

	removedTenants := c.reconcileTenantPresence(currentTenants)
	newTenants := c.identifyNewTenants(validTenantIDs)
	c.cancelRemovedTenantConsumers(removedTenants)

	// Capture stats under lock for the final log line.
	knownCount := len(c.knownTenants)
	activeCount := len(c.tenants)

	c.mu.Unlock()

	// Close database connections for removed tenants outside the lock (network I/O).
	c.closeRemovedTenantConnections(ctx, removedTenants, logger)

	if len(newTenants) > 0 {
		if c.config.EagerStart {
			logger.InfofCtx(ctx, "discovered %d new tenants (eager mode, starting consumers): %v",
				len(newTenants), newTenants)
		} else {
			logger.InfofCtx(ctx, "discovered %d new tenants (lazy mode, consumers deferred): %v",
				len(newTenants), newTenants)
		}
	}

	logger.InfofCtx(ctx, "sync complete: %d known, %d active, %d discovered, %d removed",
		knownCount, activeCount, len(newTenants), len(removedTenants))

	// Eager mode: start consumers for newly discovered tenants.
	// ensureConsumerStarted is called outside the lock (already unlocked above).
	if c.config.EagerStart && len(newTenants) > 0 {
		for _, tenantID := range newTenants {
			c.ensureConsumerStarted(ctx, tenantID)
		}
	}

	return nil
}

// filterValidTenantIDs validates the fetched tenant IDs and returns both the
// valid ID slice and a set for quick lookup.
func (c *MultiTenantConsumer) filterValidTenantIDs(
	ctx context.Context,
	tenantIDs []string,
	logger *logcompat.Logger,
) ([]string, map[string]bool) {
	validTenantIDs := make([]string, 0, len(tenantIDs))

	for _, id := range tenantIDs {
		if core.IsValidTenantID(id) {
			validTenantIDs = append(validTenantIDs, id)
		} else {
			logger.WarnfCtx(ctx, "skipping invalid tenant ID: %q", id)
		}
	}

	currentTenants := make(map[string]bool, len(validTenantIDs))
	for _, id := range validTenantIDs {
		currentTenants[id] = true
	}

	return validTenantIDs, currentTenants
}

// reconcileTenantPresence updates knownTenants by merging the current fetch with
// previously known tenants, applying the absence-count threshold. It returns the
// list of tenant IDs that exceeded the threshold and should be removed.
// MUST be called with c.mu held.
func (c *MultiTenantConsumer) reconcileTenantPresence(currentTenants map[string]bool) []string {
	previousKnown := make(map[string]bool, len(c.knownTenants))
	for id := range c.knownTenants {
		previousKnown[id] = true
	}

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

	return removedTenants
}

// identifyNewTenants returns tenant IDs from the valid list that are neither
// running a consumer nor already in knownTenants. This prevents logging
// lazy-known tenants as "new" on every sync iteration.
// MUST be called with c.mu held.
func (c *MultiTenantConsumer) identifyNewTenants(validTenantIDs []string) []string {
	var newTenants []string

	for _, tenantID := range validTenantIDs {
		if _, running := c.tenants[tenantID]; running {
			continue
		}

		// Only report as "new" if not already in knownTenants.
		// Tenants that are known but not yet active are "pending", not "new".
		if c.knownTenants[tenantID] {
			continue
		}

		newTenants = append(newTenants, tenantID)
	}

	return newTenants
}

// cancelRemovedTenantConsumers cancels goroutines and removes tenants from internal maps.
// MUST be called with c.mu held.
func (c *MultiTenantConsumer) cancelRemovedTenantConsumers(removedTenants []string) {
	for _, tenantID := range removedTenants {
		if cancel, ok := c.tenants[tenantID]; ok {
			cancel()
			delete(c.tenants, tenantID)
		}
	}
}

// closeRemovedTenantConnections closes database and messaging connections for
// tenants that have been removed from the known tenant registry.
// This method performs network I/O and MUST be called WITHOUT holding c.mu.
// The caller is responsible for cancelling goroutines and cleaning internal maps
// under the lock before invoking this function.
func (c *MultiTenantConsumer) closeRemovedTenantConnections(ctx context.Context, removedTenants []string, logger *logcompat.Logger) {
	for _, tenantID := range removedTenants {
		logger.InfofCtx(ctx, "closing connections for removed tenant: %s", tenantID)

		if c.rabbitmq != nil {
			if err := c.rabbitmq.CloseConnection(ctx, tenantID); err != nil {
				logger.WarnfCtx(ctx, "failed to close RabbitMQ connection for tenant %s: %v", tenantID, err)
			}
		}

		if c.postgres != nil {
			if err := c.postgres.CloseConnection(ctx, tenantID); err != nil {
				logger.WarnfCtx(ctx, "failed to close PostgreSQL connection for tenant %s: %v", tenantID, err)
			}
		}

		if c.mongo != nil {
			if err := c.mongo.CloseConnection(ctx, tenantID); err != nil {
				logger.WarnfCtx(ctx, "failed to close MongoDB connection for tenant %s: %v", tenantID, err)
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

	baseLogger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)
	logger := logcompat.New(baseLogger)

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
			// If tenant service was suspended/purged, stop consumer and close connections
			if core.IsTenantSuspendedError(err) {
				c.evictSuspendedTenant(ctx, tenantID, logger)
				continue
			}

			logger.WarnfCtx(ctx, "failed to fetch config for tenant %s during settings revalidation: %v", tenantID, err)

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
		logger.InfofCtx(ctx, "revalidated connection settings for %d/%d active tenants", revalidated, len(tenantIDs))
	}
}

// evictSuspendedTenant stops the consumer and closes all database connections for a
// tenant whose service was suspended or purged by the Tenant Manager. The tenant is
// removed from both tenants and knownTenants maps so it will not be restarted by the
// sync loop. The next request for this tenant will receive the 403 error directly.
func (c *MultiTenantConsumer) evictSuspendedTenant(ctx context.Context, tenantID string, logger *logcompat.Logger) {
	logger.WarnfCtx(ctx, "tenant %s service suspended, stopping consumer and closing connections", tenantID)

	c.mu.Lock()

	if cancel, ok := c.tenants[tenantID]; ok {
		cancel()
		delete(c.tenants, tenantID)
	}

	delete(c.knownTenants, tenantID)
	c.mu.Unlock()

	// Close database connections for suspended tenant
	if c.postgres != nil {
		if err := c.postgres.CloseConnection(ctx, tenantID); err != nil {
			logger.WarnfCtx(ctx, "failed to close PostgreSQL connection for suspended tenant %s: %v", tenantID, err)
		}
	}

	if c.mongo != nil {
		if err := c.mongo.CloseConnection(ctx, tenantID); err != nil {
			logger.WarnfCtx(ctx, "failed to close MongoDB connection for suspended tenant %s: %v", tenantID, err)
		}
	}

	if c.rabbitmq != nil {
		if err := c.rabbitmq.CloseConnection(ctx, tenantID); err != nil {
			logger.WarnfCtx(ctx, "failed to close RabbitMQ connection for suspended tenant %s: %v", tenantID, err)
		}
	}
}

// fetchTenantIDs gets tenant IDs from Redis cache, falling back to Tenant Manager API.
func (c *MultiTenantConsumer) fetchTenantIDs(ctx context.Context) ([]string, error) {
	baseLogger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)
	logger := logcompat.New(baseLogger)

	ctx, span := tracer.Start(ctx, "consumer.multi_tenant_consumer.fetch_tenant_ids")
	defer span.End()

	// Build environment+service segmented Redis key
	cacheKey := buildActiveTenantsKey(c.config.Environment, c.config.Service)

	// Try Redis cache first
	tenantIDs, err := c.redisClient.SMembers(ctx, cacheKey).Result()
	if err == nil && len(tenantIDs) > 0 {
		logger.InfofCtx(ctx, "fetched %d tenant IDs from cache", len(tenantIDs))
		return tenantIDs, nil
	}

	if err != nil {
		logger.WarnfCtx(ctx, "Redis cache fetch failed: %v", err)
		libOpentelemetry.HandleSpanBusinessErrorEvent(span, "Redis cache fetch failed", err)
	}

	// Fallback to Tenant Manager API
	if c.pmClient != nil && c.config.Service != "" {
		logger.InfoCtx(ctx, "falling back to Tenant Manager API for tenant list")

		tenants, apiErr := c.pmClient.GetActiveTenantsByService(ctx, c.config.Service)
		if apiErr != nil {
			logger.ErrorfCtx(ctx, "Tenant Manager API fallback failed: %v", apiErr)
			libOpentelemetry.HandleSpanError(span, "Tenant Manager API fallback failed", apiErr)
			// Return Redis error if API also fails
			if err != nil {
				return nil, err
			}

			return nil, apiErr
		}

		// Extract IDs from tenant summaries
		ids := make([]string, 0, len(tenants))
		for _, t := range tenants {
			if t == nil {
				continue
			}

			ids = append(ids, t.ID)
		}

		logger.InfofCtx(ctx, "fetched %d tenant IDs from Tenant Manager API", len(ids))

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
	baseLogger, tracer, _, _ := libCommons.NewTrackingFromContext(parentCtx)
	logger := logcompat.New(baseLogger)

	parentCtx, span := tracer.Start(parentCtx, "consumer.multi_tenant_consumer.start_tenant_consumer")
	defer span.End()

	// Create a cancellable context for this tenant
	tenantCtx, cancel := context.WithCancel(parentCtx) //#nosec G118 -- cancel stored in c.tenants[tenantID] and called when tenant consumer is stopped

	// Store the cancel function (caller holds lock)
	c.tenants[tenantID] = cancel

	logger.InfofCtx(parentCtx, "starting consumer for tenant: %s", tenantID)

	// Spawn consumer goroutine
	go c.superviseTenantQueues(tenantCtx, tenantID)
}

// superviseTenantQueues runs the consumer loop for a single tenant.
func (c *MultiTenantConsumer) superviseTenantQueues(ctx context.Context, tenantID string) {
	// Set tenantID in context for handlers
	ctx = core.SetTenantIDInContext(ctx, tenantID)

	baseLogger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)
	logger := logcompat.New(baseLogger)

	ctx, span := tracer.Start(ctx, "consumer.multi_tenant_consumer.consume_for_tenant")
	defer span.End()

	logger = logger.WithFields("tenant_id", tenantID)
	logger.InfoCtx(ctx, "consumer started for tenant")

	// Get all registered handlers (read-only, no lock needed after initial registration)
	c.mu.RLock()

	handlers := make(map[string]HandlerFunc, len(c.handlers))
	maps.Copy(handlers, c.handlers)

	c.mu.RUnlock()

	// Consume from each registered queue
	for queueName, handler := range handlers {
		go c.consumeTenantQueue(ctx, tenantID, queueName, handler, logger)
	}

	// Wait for context cancellation
	<-ctx.Done()
	logger.InfoCtx(ctx, "consumer stopped for tenant")
}

// consumeTenantQueue consumes messages from a specific queue for a tenant.
// Each connection attempt creates a short-lived span to avoid accumulating events
// on a long-lived span that would grow unbounded over the consumer's lifetime.
func (c *MultiTenantConsumer) consumeTenantQueue(
	ctx context.Context,
	tenantID string,
	queueName string,
	handler HandlerFunc,
	_ *logcompat.Logger,
) {
	baseLogger, _, _, _ := libCommons.NewTrackingFromContext(ctx) //nolint:dogsled
	logger := logcompat.New(baseLogger).WithFields("tenant_id", tenantID, "queue", queueName)

	// Guard against nil RabbitMQ manager (e.g., during lazy mode testing)

	if c.rabbitmq == nil {
		logger.WarnCtx(ctx, "RabbitMQ manager is nil, cannot consume from queue")
		return
	}

	for {
		select {
		case <-ctx.Done():
			logger.InfoCtx(ctx, "queue consumer stopped")
			return
		default:
		}

		shouldContinue := c.attemptConsumeConnection(ctx, tenantID, queueName, handler, logger)
		if !shouldContinue {
			return
		}

		logger.WarnCtx(ctx, "channel closed, reconnecting...")
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
	logger *logcompat.Logger,
) bool {
	_, tracer, _, _ := libCommons.NewTrackingFromContext(ctx) //nolint:dogsled

	connCtx, span := tracer.Start(ctx, "consumer.multi_tenant_consumer.consume_connection")
	defer span.End()

	state := c.getRetryState(tenantID)

	// Get channel for this tenant's vhost
	ch, err := c.rabbitmq.GetChannel(connCtx, tenantID)
	if err != nil {
		// If the tenant is suspended or purged, stop the consumer instead of retrying.
		// Retrying a suspended/purged tenant would cause infinite reconnect loops.
		if core.IsTenantSuspendedError(err) || core.IsTenantPurgedError(err) {
			logger.WarnfCtx(ctx, "tenant %s is suspended/purged, stopping consumer: %v", tenantID, err)
			libOpentelemetry.HandleSpanBusinessErrorEvent(span, "tenant suspended/purged, stopping consumer", err)
			c.evictSuspendedTenant(ctx, tenantID, logger)

			return false
		}

		delay, retryCount, justMarkedDegraded := state.incRetryAndMaybeMarkDegraded(maxRetryBeforeDegraded)
		if justMarkedDegraded {
			logger.WarnfCtx(ctx, "tenant %s marked as degraded after %d consecutive failures", tenantID, retryCount)
		}

		logger.WarnfCtx(ctx, "failed to get channel for tenant %s, retrying in %s (attempt %d): %v",
			tenantID, delay, retryCount, err)
		libOpentelemetry.HandleSpanError(span, "failed to get channel", err)

		select {
		case <-ctx.Done():
			return false
		case <-time.After(delay):
			return true
		}
	}

	// Set QoS

	if err := ch.Qos(c.config.PrefetchCount, 0, false); err != nil {
		_ = ch.Close() // Close channel to prevent leak

		delay, retryCount, justMarkedDegraded := state.incRetryAndMaybeMarkDegraded(maxRetryBeforeDegraded)
		if justMarkedDegraded {
			logger.WarnfCtx(ctx, "tenant %s marked as degraded after %d consecutive failures", tenantID, retryCount)
		}

		logger.WarnfCtx(ctx, "failed to set QoS for tenant %s, retrying in %s (attempt %d): %v",
			tenantID, delay, retryCount, err)
		libOpentelemetry.HandleSpanError(span, "failed to set QoS", err)

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
		_ = ch.Close() // Close channel to prevent leak

		delay, retryCount, justMarkedDegraded := state.incRetryAndMaybeMarkDegraded(maxRetryBeforeDegraded)
		if justMarkedDegraded {
			logger.WarnfCtx(ctx, "tenant %s marked as degraded after %d consecutive failures", tenantID, retryCount)
		}

		logger.WarnfCtx(ctx, "failed to start consuming for tenant %s, retrying in %s (attempt %d): %v",
			tenantID, delay, retryCount, err)
		libOpentelemetry.HandleSpanError(span, "failed to start consuming", err)

		select {
		case <-ctx.Done():
			return false
		case <-time.After(delay):
			return true
		}
	}

	// Connection succeeded: reset retry state
	c.resetRetryState(tenantID)

	logger.InfofCtx(ctx, "consuming started for tenant %s on queue %s", tenantID, queueName)

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
	_ *logcompat.Logger,
) {
	baseLogger, _, _, _ := libCommons.NewTrackingFromContext(ctx) //nolint:dogsled
	logger := logcompat.New(baseLogger).WithFields("tenant_id", tenantID, "queue", queueName)

	for {
		select {
		case <-ctx.Done():
			return
		case err := <-notifyClose:
			if err != nil {
				logger.WarnfCtx(ctx, "channel closed with error: %v", err)
			}

			return
		case msg, ok := <-msgs:
			if !ok {
				logger.WarnCtx(ctx, "message channel closed")
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
	logger *logcompat.Logger,
) {
	_, tracer, _, _ := libCommons.NewTrackingFromContext(ctx) //nolint:dogsled

	// Process message with tenant context
	msgCtx := core.SetTenantIDInContext(ctx, tenantID)

	// Extract trace context from message headers
	msgCtx = libOpentelemetry.ExtractTraceContextFromQueueHeaders(msgCtx, msg.Headers)

	// Create a per-message span
	msgCtx, span := tracer.Start(msgCtx, "consumer.multi_tenant_consumer.handle_message")
	defer span.End()

	if err := handler(msgCtx, msg); err != nil {
		logger.ErrorfCtx(ctx, "handler error for queue %s: %v", queueName, err)
		libOpentelemetry.HandleSpanBusinessErrorEvent(span, "handler error", err)

		if nackErr := msg.Nack(false, true); nackErr != nil {
			logger.ErrorfCtx(ctx, "failed to nack message: %v", nackErr)
		}
	} else {
		// Ack on success
		if ackErr := msg.Ack(false); ackErr != nil {
			logger.ErrorfCtx(ctx, "failed to ack message: %v", ackErr)
		}
	}
}

// initialBackoff is the base delay for exponential backoff on connection failures.
const initialBackoff = 5 * time.Second

// maxBackoff is the maximum delay between retry attempts.
const maxBackoff = 40 * time.Second

// maxRetryBeforeDegraded is the number of consecutive failures before marking a tenant as degraded.
const maxRetryBeforeDegraded = 3

// backoffDelay calculates the exponential backoff delay for a given retry count
// with +/-25% jitter to prevent thundering herd when multiple tenants retry simultaneously.
// Base sequence: 5s, 10s, 20s, 40s, 40s, ... (before jitter).
func backoffDelay(retryCount int) time.Duration {
	delay := initialBackoff
	for range retryCount {
		delay *= 2
		if delay > maxBackoff {
			delay = maxBackoff

			break
		}
	}

	// Apply +/-25% jitter: multiply by a random factor in [0.75, 1.25).
	// Uses crypto/rand to satisfy gosec G404.
	var b [8]byte

	_, _ = crand.Read(b[:])

	jitter := 0.75 + float64(binary.LittleEndian.Uint64(b[:]))/(1<<64)*0.5

	return time.Duration(float64(delay) * jitter)
}

// getRetryState returns the retry state entry for a tenant, creating one if it does not exist.
func (c *MultiTenantConsumer) getRetryState(tenantID string) *retryStateEntry {
	entry, _ := c.retryState.LoadOrStore(tenantID, &retryStateEntry{})

	val, ok := entry.(*retryStateEntry)
	if !ok {
		return &retryStateEntry{}
	}

	return val
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
//
// Consumers are only started for tenants that are known (resolved via discovery or
// sync). Unknown tenants are rejected to prevent starting consumers for tenants
// that have not been validated by the sync loop.
func (c *MultiTenantConsumer) ensureConsumerStarted(ctx context.Context, tenantID string) {
	baseLogger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)
	logger := logcompat.New(baseLogger)

	ctx, span := tracer.Start(ctx, "consumer.multi_tenant_consumer.ensure_consumer_started")
	defer span.End()

	// Fast path: check if consumer is already active (read lock only)

	c.mu.RLock()

	_, exists := c.tenants[tenantID]
	known := c.knownTenants[tenantID]
	closed := c.closed
	c.mu.RUnlock()

	if exists || closed {
		return
	}

	// Reject unknown tenants: they haven't been discovered or validated yet.
	// The sync loop will add them to knownTenants when they appear.
	if !known {
		logger.WarnfCtx(ctx, "rejecting consumer start for unknown tenant: %s (not yet resolved by sync)", tenantID)

		return
	}

	// Slow path: acquire per-tenant mutex for double-check locking
	lockVal, _ := c.consumerLocks.LoadOrStore(tenantID, &sync.Mutex{})

	tenantMu, ok := lockVal.(*sync.Mutex)
	if !ok {
		return
	}

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

	// Use stored parentCtx if available (from Run()), otherwise use the provided ctx.
	// Protected by c.mu.RLock because Run() writes parentCtx concurrently.
	c.mu.RLock()

	startCtx := ctx
	if c.parentCtx != nil {
		startCtx = c.parentCtx
	}

	c.mu.RUnlock()

	logger.InfofCtx(ctx, "on-demand consumer start for tenant: %s", tenantID)

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

// Close stops all consumer goroutines and marks the consumer as closed.
// It also closes the fallback pmClient to prevent goroutine leaks from its
// InMemoryCache cleanup loop.
func (c *MultiTenantConsumer) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.closed = true

	// Cancel the sync loop context first, so the background polling goroutine
	// stops before we tear down individual tenant consumers.
	if c.syncLoopCancel != nil {
		c.syncLoopCancel()
	}

	// Cancel all tenant contexts
	for tenantID, cancel := range c.tenants {
		c.logger.Infof("stopping consumer for tenant: %s", tenantID)
		cancel()
	}

	// Clear the maps

	c.tenants = make(map[string]context.CancelFunc)
	c.knownTenants = make(map[string]bool)
	c.tenantAbsenceCount = make(map[string]int)

	// Close fallback pmClient to release its InMemoryCache cleanup goroutine.
	if c.pmClient != nil {
		if err := c.pmClient.Close(); err != nil {
			c.logger.Warnf("failed to close fallback tenant manager client: %v", err)
		}
	}

	c.logger.Info("multi-tenant consumer closed")

	return nil
}

// Stats returns statistics about the consumer including lazy mode metadata.
func (c *MultiTenantConsumer) Stats() Stats {
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
		tenantID, ok := key.(string)
		if !ok {
			return true
		}

		if entry, ok := value.(*retryStateEntry); ok && entry.isDegraded() {
			degradedTenantIDs = append(degradedTenantIDs, tenantID)
		}

		return true
	})

	return Stats{
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

// Stats holds statistics for the consumer.
type Stats struct {
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
