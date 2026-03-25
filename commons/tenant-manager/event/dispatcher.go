// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package event

import (
	"context"
	"fmt"
	"time"

	libCommons "github.com/LerianStudio/lib-commons/v4/commons"
	libLog "github.com/LerianStudio/lib-commons/v4/commons/log"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v4/commons/opentelemetry"
	tmmongo "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/mongo"
	tmpostgres "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/postgres"
	tmrabbitmq "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/rabbitmq"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/tenantcache"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/logcompat"
)

// maxJitterMillis is the upper bound in milliseconds for the random jitter
// applied during credential rotation and service reactivation pool rebuilds.
const maxJitterMillis = 5000

// defaultCacheTTL is the default TTL for tenant cache entries when no TTL is configured.
const defaultCacheTTL = 12 * time.Hour

// EventDispatcher handles tenant lifecycle events independently of the consumer.
// It manages cache operations, infrastructure connection teardown, and notifies
// the consumer via callbacks when tenants are added or removed.
type EventDispatcher struct {
	cache    *tenantcache.TenantCache
	loader   *tenantcache.TenantLoader
	service  string
	cacheTTL time.Duration
	logger   *logcompat.Logger

	// Optional infrastructure managers for connection cleanup.
	postgres *tmpostgres.Manager
	mongo    *tmmongo.Manager
	rabbitmq *tmrabbitmq.Manager

	// Callbacks for consumer coordination.
	onTenantAdded   func(ctx context.Context, tenantID string)
	onTenantRemoved func(ctx context.Context, tenantID string)

	// ownsTenant checks if the tenant is owned locally (has been loaded at some point).
	// When nil, falls back to cache check (existing behavior).
	ownsTenant func(tenantID string) bool
}

// DispatcherOption configures an EventDispatcher.
type DispatcherOption func(*EventDispatcher)

// WithPostgres sets the postgres Manager on the dispatcher.
func WithPostgres(p *tmpostgres.Manager) DispatcherOption {
	return func(d *EventDispatcher) { d.postgres = p }
}

// WithMongo sets the mongo Manager on the dispatcher.
func WithMongo(m *tmmongo.Manager) DispatcherOption {
	return func(d *EventDispatcher) { d.mongo = m }
}

// WithRabbitMQ sets the rabbitmq Manager on the dispatcher.
func WithRabbitMQ(r *tmrabbitmq.Manager) DispatcherOption {
	return func(d *EventDispatcher) { d.rabbitmq = r }
}

// WithDispatcherLogger sets the logger for the dispatcher.
func WithDispatcherLogger(l libLog.Logger) DispatcherOption {
	return func(d *EventDispatcher) { d.logger = logcompat.New(l) }
}

// WithCacheTTL sets the TTL for tenant cache entries.
func WithCacheTTL(ttl time.Duration) DispatcherOption {
	return func(d *EventDispatcher) { d.cacheTTL = ttl }
}

// WithTenantOwnershipChecker sets a function that determines whether a tenant is
// "owned" locally (i.e., has been loaded or is being consumed by this instance).
// When set, tenant-level event gating uses this instead of cache lookups, which
// avoids silently dropping events for tenants whose cache entries have expired.
func WithTenantOwnershipChecker(fn func(tenantID string) bool) DispatcherOption {
	return func(d *EventDispatcher) { d.ownsTenant = fn }
}

// WithOnTenantAdded registers a callback invoked when a tenant is added to the cache
// via service association or reactivation events.
func WithOnTenantAdded(fn func(ctx context.Context, tenantID string)) DispatcherOption {
	return func(d *EventDispatcher) { d.onTenantAdded = fn }
}

// WithOnTenantRemoved registers a callback invoked when a tenant is removed from the
// cache via suspension, deletion, disassociation, or purge events.
func WithOnTenantRemoved(fn func(ctx context.Context, tenantID string)) DispatcherOption {
	return func(d *EventDispatcher) { d.onTenantRemoved = fn }
}

// SetOnTenantAdded replaces the callback invoked when a tenant is added to the cache
// via service association or reactivation events.
func (d *EventDispatcher) SetOnTenantAdded(fn func(ctx context.Context, tenantID string)) {
	d.onTenantAdded = fn
}

// SetOnTenantRemoved replaces the callback invoked when a tenant is removed from the
// cache via suspension, deletion, disassociation, or purge events.
func (d *EventDispatcher) SetOnTenantRemoved(fn func(ctx context.Context, tenantID string)) {
	d.onTenantRemoved = fn
}

// Cache returns the dispatcher's internal tenant cache.
// Returns nil only if the dispatcher was created with a nil cache and no safe default was applied.
func (d *EventDispatcher) Cache() *tenantcache.TenantCache {
	return d.cache
}

// NewEventDispatcher creates an EventDispatcher with the given cache, loader, service name,
// and optional configuration. The dispatcher is stateless with respect to consumer goroutines;
// it uses callbacks (onTenantAdded/onTenantRemoved) to coordinate with the consumer.
func NewEventDispatcher(
	cache *tenantcache.TenantCache,
	loader *tenantcache.TenantLoader,
	service string,
	opts ...DispatcherOption,
) *EventDispatcher {
	// Safe default: prevent nil-pointer panics in cache operations.
	if cache == nil {
		cache = tenantcache.NewTenantCache()
	}

	// loader can be nil (events still work for eviction; lazy-load just won't happen).
	d := &EventDispatcher{
		cache:    cache,
		loader:   loader,
		service:  service,
		cacheTTL: defaultCacheTTL,
		logger:   logcompat.New(nil),
	}

	for _, opt := range opts {
		opt(d)
	}

	return d
}

// HandleEvent dispatches a tenant lifecycle event to the appropriate handler based
// on the event type. Service-level events are filtered by service name before
// dispatch. Unknown event types are logged as warnings and skipped (no error returned).
func (d *EventDispatcher) HandleEvent(ctx context.Context, evt TenantLifecycleEvent) error {
	baseLogger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)
	logger := logcompat.New(baseLogger)

	if d.logger != nil {
		logger = d.logger
	}

	ctx, span := tracer.Start(ctx, "event.event_dispatcher.handle_event")
	defer span.End()

	logger.Base().Log(ctx, libLog.LevelInfo, "handling lifecycle event",
		libLog.String("event_type", evt.EventType),
		libLog.String("tenant_id", evt.TenantID),
		libLog.String("event_id", evt.EventID))

	// Service-level events: filter by service name before dispatching.
	if isServiceScopedEvent(evt.EventType) {
		match, err := d.matchesService(evt)
		if err != nil {
			libOpentelemetry.HandleSpanError(span, "failed to unmarshal service event payload", err)
			return fmt.Errorf("HandleEvent: failed to unmarshal payload for %s: %w", evt.EventType, err)
		}

		if !match {
			logger.Base().Log(ctx, libLog.LevelDebug, "skipping event: service mismatch",
				libLog.String("event_type", evt.EventType),
				libLog.String("tenant_id", evt.TenantID))

			return nil
		}
	}

	// Tenant-level events: only act for tenants already owned locally.
	if isTenantLevelEvent(evt.EventType) && evt.EventType != EventTenantCreated {
		if !d.isOwnedLocally(evt.TenantID) {
			logger.Base().Log(ctx, libLog.LevelDebug, "skipping tenant-level event: tenant not owned locally",
				libLog.String("event_type", evt.EventType),
				libLog.String("tenant_id", evt.TenantID))

			return nil
		}
	}

	return d.dispatchEvent(ctx, evt, logger)
}

// dispatchEvent routes the event to its handler method based on event type.
func (d *EventDispatcher) dispatchEvent(ctx context.Context, evt TenantLifecycleEvent, logger *logcompat.Logger) error {
	switch evt.EventType {
	case EventTenantCreated:
		return d.handleTenantCreated(ctx, evt, logger)
	case EventTenantActivated:
		return d.handleTenantActivated(ctx, evt, logger)
	case EventTenantSuspended:
		return d.handleTenantSuspended(ctx, evt, logger)
	case EventTenantDeleted:
		return d.handleTenantDeleted(ctx, evt, logger)
	case EventTenantUpdated:
		return d.handleTenantUpdated(ctx, evt, logger)
	case EventTenantServiceAssociated:
		return d.handleServiceAssociated(ctx, evt, logger)
	case EventTenantServiceDisassociated:
		return d.handleServiceDisassociated(ctx, evt, logger)
	case EventTenantServiceSuspended:
		return d.handleServiceSuspended(ctx, evt, logger)
	case EventTenantServicePurged:
		return d.handleServicePurged(ctx, evt, logger)
	case EventTenantServiceReactivated:
		return d.handleServiceReactivated(ctx, evt, logger)
	case EventTenantCredentialsRotated:
		return d.handleCredentialsRotated(ctx, evt, logger)
	case EventTenantConnectionsUpdated:
		return d.handleConnectionsUpdated(ctx, evt, logger)
	default:
		logger.Base().Log(ctx, libLog.LevelWarn, "unknown event type, skipping",
			libLog.String("event_type", evt.EventType),
			libLog.String("tenant_id", evt.TenantID),
			libLog.String("event_id", evt.EventID))

		return nil
	}
}
