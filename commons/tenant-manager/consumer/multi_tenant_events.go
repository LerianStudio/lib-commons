// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package consumer

import (
	"context"
	crand "crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	libCommons "github.com/LerianStudio/lib-commons/v4/commons"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v4/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/event"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/logcompat"
)

// maxJitterMillis is the upper bound in milliseconds for the random jitter
// applied during credential rotation and service reactivation pool rebuilds.
const maxJitterMillis = 5000

// handleLifecycleEvent dispatches a tenant lifecycle event to the appropriate handler
// based on the event type. Service-level events are filtered by service name before
// dispatch. Unknown event types are logged as warnings and skipped (no error returned).
func (c *MultiTenantConsumer) handleLifecycleEvent(ctx context.Context, evt event.TenantLifecycleEvent) error {
	baseLogger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)
	logger := logcompat.New(baseLogger)

	if c.logger != nil {
		logger = c.logger
	}

	ctx, span := tracer.Start(ctx, "consumer.multi_tenant_consumer.handle_lifecycle_event")
	defer span.End()

	logger.InfofCtx(ctx, "handling event type=%s tenant=%s event_id=%s", evt.EventType, evt.TenantID, evt.EventID)

	// Service-level events: filter by service name before dispatching.
	if isServiceScopedEvent(evt.EventType) {
		match, err := c.matchesService(evt)
		if err != nil {
			libOpentelemetry.HandleSpanError(span, "failed to unmarshal service event payload", err)
			return fmt.Errorf("handleLifecycleEvent: failed to unmarshal payload for %s: %w", evt.EventType, err)
		}

		if !match {
			logger.Debugf("skipping event type=%s tenant=%s: service mismatch", evt.EventType, evt.TenantID)
			return nil
		}
	}

	// Tenant-level events: only act for tenants already in local cache.
	if isTenantLevelEvent(evt.EventType) && evt.EventType != event.EventTenantCreated {
		if _, ok := c.cache.Get(evt.TenantID); !ok {
			logger.Debugf("skipping tenant-level event type=%s tenant=%s: not in local cache", evt.EventType, evt.TenantID)
			return nil
		}
	}

	return c.dispatchEvent(ctx, evt, logger)
}

// dispatchEvent routes the event to its handler method based on event type.
func (c *MultiTenantConsumer) dispatchEvent(ctx context.Context, evt event.TenantLifecycleEvent, logger *logcompat.Logger) error {
	switch evt.EventType {
	case event.EventTenantCreated:
		return c.handleTenantCreated(ctx, evt, logger)
	case event.EventTenantActivated:
		return c.handleTenantActivated(ctx, evt, logger)
	case event.EventTenantSuspended:
		return c.handleTenantSuspended(ctx, evt, logger)
	case event.EventTenantDeleted:
		return c.handleTenantDeleted(ctx, evt, logger)
	case event.EventTenantUpdated:
		return c.handleTenantUpdated(ctx, evt, logger)
	case event.EventTenantServiceAssociated:
		return c.handleServiceAssociated(ctx, evt, logger)
	case event.EventTenantServiceDisassociated:
		return c.handleServiceDisassociated(ctx, evt, logger)
	case event.EventTenantServiceSuspended:
		return c.handleServiceSuspended(ctx, evt, logger)
	case event.EventTenantServicePurged:
		return c.handleServicePurged(ctx, evt, logger)
	case event.EventTenantServiceReactivated:
		return c.handleServiceReactivated(ctx, evt, logger)
	case event.EventTenantCredentialsRotated:
		return c.handleCredentialsRotated(ctx, evt, logger)
	case event.EventTenantConnectionsUpdated:
		return c.handleConnectionsUpdated(ctx, evt, logger)
	default:
		logger.WarnfCtx(ctx, "unknown event type=%s tenant=%s event_id=%s, skipping", evt.EventType, evt.TenantID, evt.EventID)
		return nil
	}
}

// isServiceScopedEvent returns true if the event type targets a specific service
// and should be filtered by service_name in the payload.
func isServiceScopedEvent(eventType string) bool {
	return strings.HasPrefix(eventType, "tenant.service.") ||
		eventType == event.EventTenantCredentialsRotated ||
		eventType == event.EventTenantConnectionsUpdated
}

// isTenantLevelEvent returns true if the event type is a tenant-level event
// (not service-level, credential, or connection events).
func isTenantLevelEvent(eventType string) bool {
	switch eventType {
	case event.EventTenantCreated,
		event.EventTenantActivated,
		event.EventTenantSuspended,
		event.EventTenantDeleted,
		event.EventTenantUpdated:
		return true
	default:
		return false
	}
}

// serviceNamePayload is a minimal struct used to extract the service_name field
// from any service-level event payload for filtering.
type serviceNamePayload struct {
	ServiceName string `json:"service_name"`
}

// matchesService extracts the service_name from the event payload and compares
// it to the consumer's configured service. Returns (true, nil) if the service
// matches, (false, nil) if it does not, or (false, err) if unmarshaling fails.
func (c *MultiTenantConsumer) matchesService(evt event.TenantLifecycleEvent) (bool, error) {
	if len(evt.Payload) == 0 {
		return false, nil
	}

	var p serviceNamePayload
	if err := json.Unmarshal(evt.Payload, &p); err != nil {
		return false, fmt.Errorf("unmarshal service_name: %w", err)
	}

	return p.ServiceName == c.config.Service, nil
}

// closeTenantConnections closes RabbitMQ, PostgreSQL, and MongoDB connections
// for a tenant and removes it from the consumer's internal maps (tenants,
// knownTenants, cache).
func (c *MultiTenantConsumer) closeTenantConnections(ctx context.Context, tenantID string, logger *logcompat.Logger) {
	logger.InfofCtx(ctx, "closing connections for tenant=%s", tenantID)

	// Remove from cache first
	c.cache.Delete(tenantID)

	// Cancel consumer goroutine and remove from maps
	c.mu.Lock()

	if cancel, ok := c.tenants[tenantID]; ok {
		cancel()
		delete(c.tenants, tenantID)
	}

	delete(c.knownTenants, tenantID)
	c.mu.Unlock()

	// Close infrastructure connections (network I/O, outside lock)
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

// applyJitter sleeps for a random duration between 0 and maxJitterMillis milliseconds.
// The jitter prevents thundering herd effects when multiple tenants rotate credentials
// or reactivate simultaneously. Respects context cancellation.
func (c *MultiTenantConsumer) applyJitter(ctx context.Context) {
	var b [4]byte

	_, _ = crand.Read(b[:])

	delay := time.Duration(binary.LittleEndian.Uint32(b[:])%maxJitterMillis) * time.Millisecond

	select {
	case <-time.After(delay):
	case <-ctx.Done():
	}
}
