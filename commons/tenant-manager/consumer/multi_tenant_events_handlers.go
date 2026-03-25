// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package consumer

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/event"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/logcompat"
)

// handleTenantCreated is a no-op: new tenants are lazy-loaded on first request.
func (c *MultiTenantConsumer) handleTenantCreated(
	ctx context.Context,
	evt event.TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	logger.InfofCtx(ctx, "tenant.created received tenant=%s: no-op (lazy-load on first request)", evt.TenantID)
	return nil
}

// handleTenantActivated touches the TTL for a cached tenant, keeping it fresh.
func (c *MultiTenantConsumer) handleTenantActivated(
	ctx context.Context,
	evt event.TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	ttl := c.resolveCacheTTL()

	touched := c.cache.Touch(evt.TenantID, ttl)
	if touched {
		logger.InfofCtx(ctx, "tenant.activated: refreshed TTL for tenant=%s", evt.TenantID)
	} else {
		logger.Debugf("tenant.activated: tenant=%s not in cache, skipping TTL refresh", evt.TenantID)
	}

	return nil
}

// handleTenantSuspended removes the tenant from cache and closes all pools.
func (c *MultiTenantConsumer) handleTenantSuspended(
	ctx context.Context,
	evt event.TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	logger.InfofCtx(ctx, "tenant.suspended: evicting tenant=%s", evt.TenantID)
	c.closeTenantConnections(ctx, evt.TenantID, logger)

	return nil
}

// handleTenantDeleted removes the tenant from cache and closes all pools.
func (c *MultiTenantConsumer) handleTenantDeleted(
	ctx context.Context,
	evt event.TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	logger.InfofCtx(ctx, "tenant.deleted: evicting tenant=%s", evt.TenantID)
	c.closeTenantConnections(ctx, evt.TenantID, logger)

	return nil
}

// handleTenantUpdated touches the TTL for a cached tenant.
func (c *MultiTenantConsumer) handleTenantUpdated(
	ctx context.Context,
	evt event.TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	ttl := c.resolveCacheTTL()

	touched := c.cache.Touch(evt.TenantID, ttl)
	if touched {
		logger.InfofCtx(ctx, "tenant.updated: refreshed TTL for tenant=%s", evt.TenantID)
	} else {
		logger.Debugf("tenant.updated: tenant=%s not in cache, skipping TTL refresh", evt.TenantID)
	}

	return nil
}

// handleServiceAssociated adds the tenant to cache with a minimal config built
// from the event payload, marks it as known, and starts a consumer goroutine.
// Actual DB connections are created lazily when the first request arrives.
func (c *MultiTenantConsumer) handleServiceAssociated(
	ctx context.Context,
	evt event.TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	var payload event.ServiceAssociatedPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		return fmt.Errorf("handleServiceAssociated: unmarshal payload: %w", err)
	}

	logger.InfofCtx(ctx, "tenant.service.associated: adding tenant=%s service=%s", evt.TenantID, payload.ServiceName)

	// Build minimal TenantConfig from payload
	config := &core.TenantConfig{
		ID:            evt.TenantID,
		TenantSlug:    evt.TenantSlug,
		Service:       payload.ServiceName,
		IsolationMode: payload.IsolationMode,
	}

	ttl := c.resolveCacheTTL()
	c.cache.Set(evt.TenantID, config, ttl)

	c.mu.Lock()
	c.knownTenants[evt.TenantID] = true
	c.mu.Unlock()

	c.ensureConsumerStarted(ctx, evt.TenantID)

	return nil
}

// handleServiceDisassociated removes the tenant from cache and closes all pools.
func (c *MultiTenantConsumer) handleServiceDisassociated(
	ctx context.Context,
	evt event.TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	logger.InfofCtx(ctx, "tenant.service.disassociated: evicting tenant=%s", evt.TenantID)
	c.closeTenantConnections(ctx, evt.TenantID, logger)

	return nil
}

// handleServiceSuspended removes the tenant from cache and closes all pools.
func (c *MultiTenantConsumer) handleServiceSuspended(
	ctx context.Context,
	evt event.TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	logger.InfofCtx(ctx, "tenant.service.suspended: evicting tenant=%s", evt.TenantID)
	c.closeTenantConnections(ctx, evt.TenantID, logger)

	return nil
}

// handleServicePurged removes the tenant from cache and closes all pools.
func (c *MultiTenantConsumer) handleServicePurged(
	ctx context.Context,
	evt event.TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	logger.InfofCtx(ctx, "tenant.service.purged: evicting tenant=%s", evt.TenantID)
	c.closeTenantConnections(ctx, evt.TenantID, logger)

	return nil
}

// handleServiceReactivated re-adds the tenant to cache and starts a consumer.
// A random jitter (0-5s) is applied before rebuilding to prevent thundering herd.
func (c *MultiTenantConsumer) handleServiceReactivated(
	ctx context.Context,
	evt event.TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	var payload event.ServiceReactivatedPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		return fmt.Errorf("handleServiceReactivated: unmarshal payload: %w", err)
	}

	logger.InfofCtx(ctx, "tenant.service.reactivated: re-adding tenant=%s with jitter", evt.TenantID)

	c.applyJitter(ctx)

	// Build minimal TenantConfig from payload
	config := &core.TenantConfig{
		ID:         evt.TenantID,
		TenantSlug: evt.TenantSlug,
		Service:    c.config.Service,
	}

	ttl := c.resolveCacheTTL()
	c.cache.Set(evt.TenantID, config, ttl)

	c.mu.Lock()
	c.knownTenants[evt.TenantID] = true
	c.mu.Unlock()

	c.ensureConsumerStarted(ctx, evt.TenantID)

	return nil
}

// handleCredentialsRotated closes existing pools, applies jitter, and deletes
// the tenant from cache to force a lazy-reload with new credentials on the next request.
func (c *MultiTenantConsumer) handleCredentialsRotated(
	ctx context.Context,
	evt event.TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	logger.InfofCtx(ctx, "tenant.credentials.rotated: closing pools for tenant=%s with jitter", evt.TenantID)

	// Close existing pools
	c.closeTenantConnections(ctx, evt.TenantID, logger)

	// Apply jitter to prevent thundering herd
	c.applyJitter(ctx)

	// Cache was already deleted by closeTenantConnections.
	// Next request will trigger lazy-reload with new credentials.
	logger.InfofCtx(ctx, "tenant.credentials.rotated: tenant=%s ready for lazy-reload", evt.TenantID)

	return nil
}

// handleConnectionsUpdated applies new pool settings from the event payload.
// If the postgres manager is set, it delegates to ApplyConnectionSettings.
func (c *MultiTenantConsumer) handleConnectionsUpdated(
	ctx context.Context,
	evt event.TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	var payload event.ConnectionsUpdatedPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		return fmt.Errorf("handleConnectionsUpdated: unmarshal payload: %w", err)
	}

	logger.InfofCtx(ctx, "tenant.connections.updated: tenant=%s module=%s max_open=%d max_idle=%d",
		evt.TenantID, payload.Module, payload.MaxOpenConns, payload.MaxIdleConns)

	if c.postgres != nil {
		// Build a TenantConfig with the updated settings for ApplyConnectionSettings
		config := buildConfigFromConnectionsPayload(evt.TenantID, payload)
		c.postgres.ApplyConnectionSettings(evt.TenantID, config)
	}

	return nil
}

// buildConfigFromConnectionsPayload creates a minimal TenantConfig with connection
// settings from a ConnectionsUpdatedPayload, suitable for ApplyConnectionSettings.
func buildConfigFromConnectionsPayload(tenantID string, payload event.ConnectionsUpdatedPayload) *core.TenantConfig {
	return &core.TenantConfig{
		ID: tenantID,
		ConnectionSettings: &core.ConnectionSettings{
			MaxOpenConns:     payload.MaxOpenConns,
			MaxIdleConns:     payload.MaxIdleConns,
			StatementTimeout: payload.StatementTimeout,
		},
	}
}

// resolveCacheTTL returns the configured tenant cache TTL or the default.
func (c *MultiTenantConsumer) resolveCacheTTL() time.Duration {
	if c.config.TenantCacheTTL > 0 {
		return c.config.TenantCacheTTL
	}

	return DefaultTenantCacheTTL
}
