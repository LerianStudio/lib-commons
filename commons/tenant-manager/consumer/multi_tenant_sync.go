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

// syncTenants fetches tenant IDs from the tenant-manager API and updates the
// known tenant registry. New tenants are added and consumers started immediately.
// Tenants not in the API response are removed immediately -- the API is the
// single source of truth, so no grace period is needed.
// If fetchTenantIDs fails, syncTenants keeps the current state unchanged and
// returns nil (the caller logs the failure and retries on the next interval).
func (c *MultiTenantConsumer) syncTenants(ctx context.Context) error {
	baseLogger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)
	logger := logcompat.New(baseLogger)

	if c.logger != nil {
		logger = c.logger
	}

	ctx, span := tracer.Start(ctx, "consumer.multi_tenant_consumer.sync_tenants")
	defer span.End()

	// Fetch tenant IDs from tenant-manager API
	tenantIDs, err := c.fetchTenantIDs(ctx)
	if err != nil {
		// API failed -- keep current state, don't remove anyone
		logger.WarnfCtx(ctx, "tenant sync failed (keeping current state): %v", err)
		libOpentelemetry.HandleSpanBusinessErrorEvent(span, "tenant sync failed (keeping current state)", err)

		return nil
	}

	validTenantIDs, currentSet := c.filterValidTenantIDs(ctx, tenantIDs, logger)

	c.mu.Lock()

	if c.closed {
		c.mu.Unlock()
		return errors.New("consumer is closed")
	}

	// Find removed (in knownTenants but not in currentSet)
	var removed []string
	for id := range c.knownTenants {
		if !currentSet[id] {
			removed = append(removed, id)
		}
	}

	// Find added (in currentSet but not in knownTenants)
	var added []string
	for _, id := range validTenantIDs {
		if !c.knownTenants[id] {
			added = append(added, id)
		}
	}

	// Update knownTenants to match API response exactly
	c.knownTenants = make(map[string]bool, len(currentSet))
	for id := range currentSet {
		c.knownTenants[id] = true
	}

	// Remove tenants no longer in API response
	c.cancelRemovedTenantConsumers(removed)

	c.mu.Unlock()

	// Close database connections for removed tenants outside the lock (network I/O).
	c.closeRemovedTenantConnections(ctx, removed, logger)

	// Log only changes
	for _, id := range added {
		logger.InfofCtx(ctx, "tenant added tenant=%s service=%s", id, c.config.Service)
	}

	for _, id := range removed {
		logger.InfofCtx(ctx, "tenant removed tenant=%s service=%s", id, c.config.Service)
	}

	// Start consumers for newly discovered tenants.
	// ensureConsumerStarted is called outside the lock (already unlocked above).
	for _, tenantID := range added {
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

// fetchTenantIDs gets tenant IDs from the tenant-manager API.
func (c *MultiTenantConsumer) fetchTenantIDs(ctx context.Context) ([]string, error) {
	_, tracer, _, _ := libCommons.NewTrackingFromContext(ctx) //nolint:dogsled

	ctx, span := tracer.Start(ctx, "consumer.multi_tenant_consumer.fetch_tenant_ids")
	defer span.End()

	tenants, err := c.pmClient.GetActiveTenantsByService(ctx, c.config.Service)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to fetch active tenants from tenant-manager", err)
		return nil, fmt.Errorf("failed to fetch active tenants from tenant-manager: %w", err)
	}

	ids := make([]string, 0, len(tenants))
	for _, t := range tenants {
		if t != nil {
			ids = append(ids, t.ID)
		}
	}

	return ids, nil
}
