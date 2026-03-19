package consumer

import (
	"context"
	"errors"
	"fmt"
	"time"

	libCommons "github.com/LerianStudio/lib-commons/v4/commons"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v4/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/logcompat"
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

// eagerStartKnownTenants starts consumers for all known tenants.
// Called during Run() and when new tenants are discovered during sync.
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
// This is the initial discovery step at startup. Actual consumer spawning is handled by
// eagerStartKnownTenants() after discovery completes. Failures are logged as warnings
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

	logger.InfofCtx(ctx, "discovered %d tenants", len(tenantIDs))
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
// New tenants are added to knownTenants and consumers are started immediately.
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

	previousKnown := c.snapshotKnownTenantsLocked()
	removedTenants := c.reconcileTenantPresence(previousKnown, currentTenants)
	newTenants := c.identifyNewTenants(validTenantIDs, previousKnown)
	c.cancelRemovedTenantConsumers(removedTenants)

	// Capture stats under lock for the final log line.
	knownCount := len(c.knownTenants)
	activeCount := len(c.tenants)

	c.mu.Unlock()

	// Close database connections for removed tenants outside the lock (network I/O).
	c.closeRemovedTenantConnections(ctx, removedTenants, logger)

	if len(newTenants) > 0 {
		logger.InfofCtx(ctx, "discovered %d new tenants (starting consumers): %v",
			len(newTenants), newTenants)
	}

	logger.InfofCtx(ctx, "sync complete: %d known, %d active, %d discovered, %d removed",
		knownCount, activeCount, len(newTenants), len(removedTenants))

	// Start consumers for newly discovered tenants.
	// ensureConsumerStarted is called outside the lock (already unlocked above).
	for _, tenantID := range newTenants {
		c.ensureConsumerStarted(ctx, tenantID)
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

// snapshotKnownTenantsLocked copies the current known-tenants set.
// MUST be called with c.mu held.
func (c *MultiTenantConsumer) snapshotKnownTenantsLocked() map[string]bool {
	previousKnown := make(map[string]bool, len(c.knownTenants))
	for id := range c.knownTenants {
		previousKnown[id] = true
	}

	return previousKnown
}

// reconcileTenantPresence updates knownTenants by merging the current fetch with
// previously known tenants, applying the absence-count threshold. It returns the
// list of tenant IDs that exceeded the threshold and should be removed.
// MUST be called with c.mu held.
func (c *MultiTenantConsumer) reconcileTenantPresence(previousKnown, currentTenants map[string]bool) []string {
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
// already running nor present in the pre-sync known-tenants snapshot.
// This prevents logging lazy-known tenants as "new" on every sync iteration
// while still correctly surfacing tenants first discovered in the current sync.
// MUST be called with c.mu held.
func (c *MultiTenantConsumer) identifyNewTenants(validTenantIDs []string, previousKnown map[string]bool) []string {
	var newTenants []string

	for _, tenantID := range validTenantIDs {
		if _, running := c.tenants[tenantID]; running {
			continue
		}

		// Only report as "new" if not already in the pre-sync known set.
		// Tenants that are known but not yet active are "pending", not "new".
		if previousKnown[tenantID] {
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
