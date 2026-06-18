// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package event

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/internal/logcompat"
	libLog "github.com/LerianStudio/lib-observability/log"
)

// handleTenantCreated is a no-op: new tenants are lazy-loaded on first request.
func (d *EventDispatcher) handleTenantCreated(
	ctx context.Context,
	evt TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	logger.Base().Log(ctx, libLog.LevelInfo, "tenant.created received: no-op (lazy-load on first request)",
		libLog.String("tenant_id", evt.TenantID))

	return nil
}

// handleTenantActivated touches the TTL for a cached tenant, keeping it fresh.
func (d *EventDispatcher) handleTenantActivated(
	ctx context.Context,
	evt TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	ttl := d.resolveCacheTTL()

	touched := d.cache.Touch(evt.TenantID, ttl)
	if touched {
		logger.Base().Log(ctx, libLog.LevelInfo, "tenant.activated: refreshed TTL",
			libLog.String("tenant_id", evt.TenantID))
	} else {
		logger.Base().Log(ctx, libLog.LevelDebug, "tenant.activated: tenant not in cache, skipping TTL refresh",
			libLog.String("tenant_id", evt.TenantID))
	}

	return nil
}

// handleTenantSuspended removes the tenant from cache and closes all pools.
func (d *EventDispatcher) handleTenantSuspended(
	ctx context.Context,
	evt TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	logger.Base().Log(ctx, libLog.LevelInfo, "tenant.suspended: evicting tenant",
		libLog.String("tenant_id", evt.TenantID))
	d.removeTenant(ctx, evt.TenantID, logger)

	return nil
}

// handleTenantDeleted removes the tenant from cache and closes all pools.
func (d *EventDispatcher) handleTenantDeleted(ctx context.Context, evt TenantLifecycleEvent, logger *logcompat.Logger) error {
	logger.Base().Log(ctx, libLog.LevelInfo, "tenant.deleted: evicting tenant",
		libLog.String("tenant_id", evt.TenantID))
	d.removeTenant(ctx, evt.TenantID, logger)

	return nil
}

// handleTenantUpdated touches the TTL for a cached tenant.
func (d *EventDispatcher) handleTenantUpdated(
	ctx context.Context,
	evt TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	ttl := d.resolveCacheTTL()

	touched := d.cache.Touch(evt.TenantID, ttl)
	if touched {
		logger.Base().Log(ctx, libLog.LevelInfo, "tenant.updated: refreshed TTL",
			libLog.String("tenant_id", evt.TenantID))
	} else {
		logger.Base().Log(ctx, libLog.LevelDebug, "tenant.updated: tenant not in cache, skipping TTL refresh",
			libLog.String("tenant_id", evt.TenantID))
	}

	return nil
}

// handleServiceAssociated adds the tenant to cache with a minimal config built
// from the event payload and invokes the onTenantAdded callback.
// Actual DB connections are created lazily when the first request arrives.
func (d *EventDispatcher) handleServiceAssociated(
	ctx context.Context,
	evt TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	var payload ServiceAssociatedPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		return fmt.Errorf("handleServiceAssociated: unmarshal payload: %w", err)
	}

	logger.Base().Log(ctx, libLog.LevelInfo, "tenant.service.associated: adding tenant",
		libLog.String("tenant_id", evt.TenantID),
		libLog.String("service", payload.ServiceName))

	// Build TenantConfig from payload with connection settings, databases, and messaging
	config := &core.TenantConfig{
		ID:            evt.TenantID,
		TenantSlug:    evt.TenantSlug,
		Service:       payload.ServiceName,
		IsolationMode: payload.IsolationMode,
	}

	// Populate ConnectionSettings from payload
	if payload.ConnectionSettings != nil {
		config.ConnectionSettings = &core.ConnectionSettings{
			MaxOpenConns:     payload.ConnectionSettings.MaxOpenConns,
			MaxIdleConns:     payload.ConnectionSettings.MaxIdleConns,
			StatementTimeout: payload.ConnectionSettings.StatementTimeout,
		}
	}

	// Populate Databases from secret_paths
	config.Databases = buildDatabasesFromSecretPaths(payload.SecretPaths)

	// Populate Messaging from payload
	if payload.MessagingConfig != nil && payload.MessagingConfig.RabbitMQSecretPath != "" {
		config.Messaging = &core.MessagingConfig{
			RabbitMQ: &core.RabbitMQConfig{
				// Secret path stored in Host field; actual credentials resolved lazily
				Host: payload.MessagingConfig.RabbitMQSecretPath,
			},
		}
	}

	ttl := d.resolveCacheTTL()
	d.cache.Set(evt.TenantID, config, ttl)

	// Notify consumer to start goroutine
	if d.onTenantAdded != nil {
		d.onTenantAdded(ctx, evt.TenantID)
	}

	return nil
}

// handleServiceDisassociated removes the tenant from cache and closes all pools.
func (d *EventDispatcher) handleServiceDisassociated(ctx context.Context, evt TenantLifecycleEvent, logger *logcompat.Logger) error {
	logger.Base().Log(ctx, libLog.LevelInfo, "tenant.service.disassociated: evicting tenant",
		libLog.String("tenant_id", evt.TenantID))
	d.removeTenant(ctx, evt.TenantID, logger)

	return nil
}

// handleServiceSuspended removes the tenant from cache and closes all pools.
func (d *EventDispatcher) handleServiceSuspended(ctx context.Context, evt TenantLifecycleEvent, logger *logcompat.Logger) error {
	logger.Base().Log(ctx, libLog.LevelInfo, "tenant.service.suspended: evicting tenant",
		libLog.String("tenant_id", evt.TenantID))
	d.removeTenant(ctx, evt.TenantID, logger)

	return nil
}

// handleServicePurged removes the tenant from cache and closes all pools.
func (d *EventDispatcher) handleServicePurged(ctx context.Context, evt TenantLifecycleEvent, logger *logcompat.Logger) error {
	logger.Base().Log(ctx, libLog.LevelInfo, "tenant.service.purged: evicting tenant",
		libLog.String("tenant_id", evt.TenantID))
	d.removeTenant(ctx, evt.TenantID, logger)

	return nil
}

// handleServiceReactivated re-adds the tenant to cache and invokes the onTenantAdded callback.
// A random jitter (0-5s) is applied before rebuilding to prevent thundering herd.
func (d *EventDispatcher) handleServiceReactivated(
	ctx context.Context,
	evt TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	var payload ServiceReactivatedPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		return fmt.Errorf("handleServiceReactivated: unmarshal payload: %w", err)
	}

	logger.Base().Log(ctx, libLog.LevelInfo, "tenant.service.reactivated: re-adding tenant with jitter",
		libLog.String("tenant_id", evt.TenantID))

	d.applyJitter(ctx)

	// Build TenantConfig from payload with connection settings and databases
	config := &core.TenantConfig{
		ID:         evt.TenantID,
		TenantSlug: evt.TenantSlug,
		Service:    payload.ServiceName,
	}

	// Populate ConnectionSettings from payload
	if payload.ConnectionSettings != nil {
		config.ConnectionSettings = &core.ConnectionSettings{
			MaxOpenConns:     payload.ConnectionSettings.MaxOpenConns,
			MaxIdleConns:     payload.ConnectionSettings.MaxIdleConns,
			StatementTimeout: payload.ConnectionSettings.StatementTimeout,
		}
	}

	// Populate Databases from secret_paths
	config.Databases = buildDatabasesFromSecretPaths(payload.SecretPaths)

	ttl := d.resolveCacheTTL()
	d.cache.Set(evt.TenantID, config, ttl)

	// Notify consumer to start goroutine
	if d.onTenantAdded != nil {
		d.onTenantAdded(ctx, evt.TenantID)
	}

	return nil
}

// handleCredentialsRotated closes existing pools, applies jitter, and eagerly
// reloads tenant config via /connections to reconnect with new credentials.
// Unlike other events that rely on lazy-load, credential rotation requires
// immediate reconnection to avoid failed queries between the event and the
// next inbound request.
func (d *EventDispatcher) handleCredentialsRotated(
	ctx context.Context,
	evt TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	logger.Base().Log(ctx, libLog.LevelInfo, "tenant.credentials.rotated: closing pools",
		libLog.String("tenant_id", evt.TenantID))

	// Close existing pools and remove from cache
	d.removeTenant(ctx, evt.TenantID, logger)

	// Apply jitter to prevent thundering herd when multiple pods react simultaneously
	d.applyJitter(ctx)

	if d.loader == nil {
		logger.Base().Log(ctx, libLog.LevelWarn, "tenant.credentials.rotated: no loader configured, skipping eager reload",
			libLog.String("tenant_id", evt.TenantID))

		return nil
	}

	return d.reloadAndNotify(ctx, evt.TenantID, logger,
		"tenant.credentials.rotated", "tenant.credentials.rotated: reconnected with new credentials")
}

// reloadAndNotify performs the shared tail of the eager-reload handlers
// (handleCredentialsRotated and the owned path of handleCacheInvalidate):
//
//  1. loader.LoadTenant — on error, warn and return nil (non-fatal: the next
//     inbound request triggers lazy-load as a fallback);
//  2. invoke onTenantAdded (if set) so the consumer starts its goroutine;
//  3. emit the per-event success log.
//
// evtName is the event-name prefix for the failure log and successMsg is the
// full success message, so each caller keeps its own exact wording (the two
// handlers' success messages differ beyond the prefix) while sharing the
// control flow.
//
// IMPORTANT: callers must invoke removeTenant, the nil-loader guard, and
// applyJitter THEMSELVES before calling this helper. Those steps are
// intentionally left in the callers because the relative order of applyJitter
// vs the nil-loader guard differs between the two handlers (credentials:
// jitter then nil-check; cache.invalidate: nil-check then jitter), an ordering
// quirk observable only when loader == nil. This helper assumes loader != nil.
func (d *EventDispatcher) reloadAndNotify(
	ctx context.Context,
	tenantID string,
	logger *logcompat.Logger,
	evtName string,
	successMsg string,
) error {
	if _, err := d.loader.LoadTenant(ctx, tenantID); err != nil {
		logger.Base().Log(ctx, libLog.LevelWarn, evtName+": eager reload failed (will retry on next request)",
			libLog.String("tenant_id", tenantID),
			libLog.Err(err))

		return nil // non-fatal: next request will trigger lazy-load as fallback
	}

	// LoadTenant cached the config; notify consumer to start goroutine.
	if d.onTenantAdded != nil {
		d.onTenantAdded(ctx, tenantID)
	}

	logger.Base().Log(ctx, libLog.LevelInfo, successMsg,
		libLog.String("tenant_id", tenantID))

	return nil
}

// handleCacheInvalidate handles operator-triggered per-service cache hot-reload.
// It evicts the tenant from both the local (tier-1) and client (tier-2) caches
// via removeTenant, then eagerly reloads the config ONLY when the tenant is
// owned locally. Non-owned tenants are evict-only (no reload), avoiding warming
// caches for tenants this instance does not serve.
//
// Ownership is captured BEFORE removeTenant because removeTenant deletes the
// tier-1 entry that the cache-fallback ownership check reads. Because
// tenant.cache.invalidate is service-scoped, the service has already matched by
// the time this handler runs (HandleEvent applies the service filter); the
// tenant-level ownership gate does NOT apply to service-scoped events, so the
// ownership decision is made here.
func (d *EventDispatcher) handleCacheInvalidate(
	ctx context.Context,
	evt TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	logger.Base().Log(ctx, libLog.LevelInfo, "tenant.cache.invalidate: evicting tenant",
		libLog.String("tenant_id", evt.TenantID))

	// Capture ownership before removeTenant clears the tier-1 cache entry.
	owned := d.isOwnedLocally(evt.TenantID)

	// Evict both tiers (tier-1 cache + tier-2 client cache) and close pools.
	d.removeTenant(ctx, evt.TenantID, logger)

	if !owned {
		logger.Base().Log(ctx, libLog.LevelDebug, "tenant.cache.invalidate: tenant not owned locally, evict only",
			libLog.String("tenant_id", evt.TenantID))

		return nil
	}

	if d.loader == nil {
		logger.Base().Log(ctx, libLog.LevelWarn, "tenant.cache.invalidate: no loader configured, skipping eager reload",
			libLog.String("tenant_id", evt.TenantID))

		return nil
	}

	// Apply jitter to prevent thundering herd when multiple pods react simultaneously.
	d.applyJitter(ctx)

	return d.reloadAndNotify(ctx, evt.TenantID, logger,
		"tenant.cache.invalidate", "tenant.cache.invalidate: evicted and reloaded")
}

// handleConnectionsUpdated applies new pool settings from the event payload.
// If the postgres manager is set, it delegates to ApplyConnectionSettings.
func (d *EventDispatcher) handleConnectionsUpdated(
	ctx context.Context,
	evt TenantLifecycleEvent,
	logger *logcompat.Logger,
) error {
	var payload ConnectionsUpdatedPayload
	if err := json.Unmarshal(evt.Payload, &payload); err != nil {
		return fmt.Errorf("handleConnectionsUpdated: unmarshal payload: %w", err)
	}

	logger.Base().Log(ctx, libLog.LevelInfo, "tenant.connections.updated: applying new pool settings",
		libLog.String("tenant_id", evt.TenantID),
		libLog.String("module", payload.Module),
		libLog.Int("max_open_conns", payload.MaxOpenConns),
		libLog.Int("max_idle_conns", payload.MaxIdleConns),
		libLog.String("statement_timeout", payload.StatementTimeout))

	if d.postgres != nil {
		config := buildConfigFromConnectionsPayload(evt.TenantID, payload)
		d.postgres.ApplyConnectionSettings(evt.TenantID, config)
	}

	return nil
}
