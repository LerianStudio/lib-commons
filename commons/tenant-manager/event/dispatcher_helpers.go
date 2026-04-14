// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package event

import (
	"context"
	crand "crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/internal/logcompat"
)

// serviceNamePayload is a minimal struct used to extract the service_name field
// from any service-level event payload for filtering.
type serviceNamePayload struct {
	ServiceName string `json:"service_name"`
}

// matchesService extracts the service_name from the event payload and compares
// it to the dispatcher's configured service. Returns (true, nil) if the service
// matches, (false, nil) if it does not, or (false, err) if unmarshaling fails.
func (d *EventDispatcher) matchesService(evt TenantLifecycleEvent) (bool, error) {
	if len(evt.Payload) == 0 {
		return false, nil
	}

	var p serviceNamePayload
	if err := json.Unmarshal(evt.Payload, &p); err != nil {
		return false, fmt.Errorf("unmarshal service_name: %w", err)
	}

	return p.ServiceName == d.service, nil
}

// isServiceScopedEvent returns true if the event type targets a specific service
// and should be filtered by service_name in the payload.
func isServiceScopedEvent(eventType string) bool {
	return strings.HasPrefix(eventType, "tenant.service.") ||
		eventType == EventTenantCredentialsRotated ||
		eventType == EventTenantConnectionsUpdated
}

// isTenantLevelEvent returns true if the event type is a tenant-level event
// (not service-level, credential, or connection events).
func isTenantLevelEvent(eventType string) bool {
	switch eventType {
	case EventTenantCreated,
		EventTenantActivated,
		EventTenantSuspended,
		EventTenantDeleted,
		EventTenantUpdated:
		return true
	default:
		return false
	}
}

// RemoveTenant removes a tenant from cache, closes infrastructure connections,
// and notifies the consumer via the onTenantRemoved callback. It is exported so
// the consumer can call it directly from non-event code paths (e.g., when a
// tenant is detected as suspended during RabbitMQ channel acquisition).
func (d *EventDispatcher) RemoveTenant(ctx context.Context, tenantID string) {
	d.removeTenant(ctx, tenantID, d.logger)
}

// removeTenant is the internal implementation of RemoveTenant with a specific logger.
func (d *EventDispatcher) removeTenant(ctx context.Context, tenantID string, logger *logcompat.Logger) {
	logger.InfofCtx(ctx, "closing connections for tenant=%s", tenantID)

	// Remove from cache first
	d.cache.Delete(tenantID)

	// Close infrastructure connections (network I/O)
	if d.rabbitmq != nil {
		if err := d.rabbitmq.CloseConnection(ctx, tenantID); err != nil {
			logger.WarnfCtx(ctx, "failed to close RabbitMQ connection for tenant %s: %v", tenantID, err)
		}
	}

	if d.postgres != nil {
		if err := d.postgres.CloseConnection(ctx, tenantID); err != nil {
			logger.WarnfCtx(ctx, "failed to close PostgreSQL connection for tenant %s: %v", tenantID, err)
		}
	}

	if d.mongo != nil {
		if err := d.mongo.CloseConnection(ctx, tenantID); err != nil {
			logger.WarnfCtx(ctx, "failed to close MongoDB connection for tenant %s: %v", tenantID, err)
		}
	}

	// Notify consumer to stop goroutine
	if d.onTenantRemoved != nil {
		d.onTenantRemoved(ctx, tenantID)
	}
}

// isOwnedLocally reports whether the tenant is considered "owned" by this instance.
// When an ownsTenant checker is set (via WithTenantOwnershipChecker), it delegates
// to that function. Otherwise it falls back to a cache lookup, which may miss
// tenants whose cache entries have expired.
func (d *EventDispatcher) isOwnedLocally(tenantID string) bool {
	if d.ownsTenant != nil {
		return d.ownsTenant(tenantID)
	}

	// Fallback: check cache (may miss expired entries).
	_, ok := d.cache.Get(tenantID)

	return ok
}

// resolveCacheTTL returns the configured cache TTL or the default.
func (d *EventDispatcher) resolveCacheTTL() time.Duration {
	if d.cacheTTL > 0 {
		return d.cacheTTL
	}

	return defaultCacheTTL
}

// buildDatabasesFromSecretPaths converts secret path maps into a DatabaseConfig map.
// Secret paths carry Secrets Manager paths, not actual credentials; the connection
// manager resolves credentials lazily via pmClient.GetTenantConfig().
func buildDatabasesFromSecretPaths(secretPaths map[string]map[string]string) map[string]core.DatabaseConfig {
	if len(secretPaths) == 0 {
		return nil
	}

	databases := make(map[string]core.DatabaseConfig, len(secretPaths))

	for module, paths := range secretPaths {
		dbConfig := core.DatabaseConfig{}

		if pgRW, ok := paths["postgresql_rw"]; ok {
			dbConfig.PostgreSQL = &core.PostgreSQLConfig{Host: pgRW}
		}

		if pgRO, ok := paths["postgresql_ro"]; ok {
			dbConfig.PostgreSQLReplica = &core.PostgreSQLConfig{Host: pgRO}
		}

		if _, ok := paths["mongodb"]; ok {
			dbConfig.MongoDB = &core.MongoDBConfig{}
		}

		databases[module] = dbConfig
	}

	return databases
}

// buildConfigFromConnectionsPayload creates a minimal TenantConfig with connection
// settings from a ConnectionsUpdatedPayload, suitable for ApplyConnectionSettings.
func buildConfigFromConnectionsPayload(tenantID string, payload ConnectionsUpdatedPayload) *core.TenantConfig {
	return &core.TenantConfig{
		ID: tenantID,
		ConnectionSettings: &core.ConnectionSettings{
			MaxOpenConns:     payload.MaxOpenConns,
			MaxIdleConns:     payload.MaxIdleConns,
			StatementTimeout: payload.StatementTimeout,
		},
	}
}

// applyJitter sleeps for a random duration between 0 and maxJitterMillis milliseconds.
// The jitter prevents thundering herd effects when multiple tenants rotate credentials
// or reactivate simultaneously. Respects context cancellation.
func (d *EventDispatcher) applyJitter(ctx context.Context) {
	var b [4]byte

	_, _ = crand.Read(b[:])

	delay := time.Duration(binary.LittleEndian.Uint32(b[:])%maxJitterMillis) * time.Millisecond

	select {
	case <-time.After(delay):
	case <-ctx.Done():
	}
}
