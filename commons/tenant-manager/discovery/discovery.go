// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

// Package discovery provides a lightweight orchestrator for background workers
// that need to know which tenants are currently active. It bootstraps from the
// tenant-manager API and keeps the set up-to-date via Redis Pub/Sub events.
package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	libLog "github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/client"
	tmevent "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/event"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/logcompat"
	tmredis "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/redis"
)

// ActiveTenantsFetcher is the interface for fetching active tenants.
// Satisfied by *client.Client.
type ActiveTenantsFetcher interface {
	GetActiveTenantsByService(ctx context.Context, service string) ([]*client.TenantSummary, error)
}

// Config holds configuration for a TenantDiscovery instance.
type Config struct {
	ServiceName string
	TMClient    ActiveTenantsFetcher
	RedisConfig tmredis.TenantPubSubRedisConfig
	Logger      libLog.Logger
}

// TenantDiscovery maintains a set of active tenant IDs for background workers.
// It bootstraps from the tenant-manager API and subscribes to Redis Pub/Sub
// events to keep the set current.
type TenantDiscovery struct {
	cfg    Config
	logger *logcompat.Logger

	mu      sync.RWMutex
	tenants map[string]struct{}

	onAdded   func(ctx context.Context, tenantID string)
	onRemoved func(ctx context.Context, tenantID string)

	listener *tmevent.TenantEventListener
}

// NewTenantDiscovery creates a new TenantDiscovery instance.
// It validates the config but does not connect to any services.
func NewTenantDiscovery(cfg Config) (*TenantDiscovery, error) {
	if cfg.ServiceName == "" {
		return nil, errors.New("discovery.NewTenantDiscovery: ServiceName is required")
	}

	if cfg.TMClient == nil {
		return nil, errors.New("discovery.NewTenantDiscovery: TMClient is required")
	}

	if cfg.RedisConfig.Host == "" {
		return nil, errors.New("discovery.NewTenantDiscovery: RedisConfig.Host is required")
	}

	logger := cfg.Logger
	if logger == nil {
		logger = libLog.NewNop()
	}

	return &TenantDiscovery{
		cfg:     cfg,
		logger:  logcompat.New(logger),
		tenants: make(map[string]struct{}),
	}, nil
}

// Start bootstraps the tenant set from the API and subscribes to Redis Pub/Sub.
// If the API call fails, Start returns an error (fail-fast).
// If Redis is unavailable, Start logs a warning and continues (bootstrap-only mode).
func (d *TenantDiscovery) Start(ctx context.Context) error {
	// Bootstrap from tenant-manager API
	summaries, err := d.cfg.TMClient.GetActiveTenantsByService(ctx, d.cfg.ServiceName)
	if err != nil {
		return fmt.Errorf("discovery.Start: bootstrap failed: %w", err)
	}

	d.mu.Lock()
	for _, s := range summaries {
		d.tenants[s.ID] = struct{}{}
	}
	d.mu.Unlock()

	d.logger.InfofCtx(ctx, "discovery: bootstrapped %d tenants for service %s", len(summaries), d.cfg.ServiceName)

	// Connect to Redis Pub/Sub for live updates
	redisClient, redisErr := tmredis.NewTenantPubSubRedisClient(ctx, d.cfg.RedisConfig)
	if redisErr != nil {
		d.logger.WarnfCtx(ctx, "discovery: redis pub/sub unavailable, running in bootstrap-only mode: %v", redisErr)
		return nil
	}

	listener, listenerErr := tmevent.NewTenantEventListener(
		redisClient,
		d.HandleEvent,
		tmevent.WithListenerLogger(d.cfg.Logger),
		tmevent.WithService(d.cfg.ServiceName),
	)
	if listenerErr != nil {
		d.logger.WarnfCtx(ctx, "discovery: failed to create event listener: %v", listenerErr)
		return nil
	}

	if startErr := listener.Start(ctx); startErr != nil {
		d.logger.WarnfCtx(ctx, "discovery: failed to start event listener: %v", startErr)
		return nil
	}

	d.listener = listener

	d.logger.InfofCtx(ctx, "discovery: event listener started for service %s", d.cfg.ServiceName)

	return nil
}

// ActiveTenantIDs returns a snapshot copy of the current active tenant IDs.
func (d *TenantDiscovery) ActiveTenantIDs() []string {
	d.mu.RLock()
	defer d.mu.RUnlock()

	ids := make([]string, 0, len(d.tenants))
	for id := range d.tenants {
		ids = append(ids, id)
	}

	return ids
}

// OnTenantAdded registers a callback invoked when a tenant is added to the active set.
func (d *TenantDiscovery) OnTenantAdded(fn func(ctx context.Context, tenantID string)) {
	d.onAdded = fn
}

// OnTenantRemoved registers a callback invoked when a tenant is removed from the active set.
func (d *TenantDiscovery) OnTenantRemoved(fn func(ctx context.Context, tenantID string)) {
	d.onRemoved = fn
}

// Close stops the event listener if running.
func (d *TenantDiscovery) Close() error {
	if d.listener != nil {
		return d.listener.Stop()
	}

	return nil
}

// HandleEvent processes a tenant lifecycle event and updates the internal tenant set.
// This is exported so tests can inject events directly; it is also wired as the
// event handler for the TenantEventListener.
func (d *TenantDiscovery) HandleEvent(ctx context.Context, evt tmevent.TenantLifecycleEvent) error {
	switch evt.EventType {
	// Add events
	case tmevent.EventTenantServiceAssociated:
		return d.handleServiceAdd(ctx, evt)
	case tmevent.EventTenantServiceReactivated:
		return d.handleServiceAdd(ctx, evt)

	// Remove events
	case tmevent.EventTenantServiceDisassociated:
		return d.handleServiceRemove(ctx, evt)
	case tmevent.EventTenantSuspended:
		d.removeTenant(ctx, evt.TenantID)
		return nil
	case tmevent.EventTenantDeleted:
		d.removeTenant(ctx, evt.TenantID)
		return nil
	case tmevent.EventTenantServiceSuspended:
		return d.handleServiceRemove(ctx, evt)
	case tmevent.EventTenantServicePurged:
		return d.handleServiceRemove(ctx, evt)

	// No-op events
	case tmevent.EventTenantCreated,
		tmevent.EventTenantActivated,
		tmevent.EventTenantUpdated,
		tmevent.EventTenantCredentialsRotated,
		tmevent.EventTenantConnectionsUpdated:
		return nil

	default:
		d.logger.WarnfCtx(ctx, "discovery: unknown event type %s, skipping", evt.EventType)
		return nil
	}
}

// serviceNamePayload extracts service_name from event payloads for filtering.
type serviceNamePayload struct {
	ServiceName string `json:"service_name"`
}

// handleServiceAdd processes service-level add events (associated, reactivated).
func (d *TenantDiscovery) handleServiceAdd(ctx context.Context, evt tmevent.TenantLifecycleEvent) error {
	if !d.matchesService(evt) {
		return nil
	}

	d.addTenant(ctx, evt.TenantID)

	return nil
}

// handleServiceRemove processes service-level remove events (disassociated, suspended, purged).
func (d *TenantDiscovery) handleServiceRemove(ctx context.Context, evt tmevent.TenantLifecycleEvent) error {
	if !d.matchesService(evt) {
		return nil
	}

	d.removeTenant(ctx, evt.TenantID)

	return nil
}

// matchesService checks if the event payload's service_name matches our service.
func (d *TenantDiscovery) matchesService(evt tmevent.TenantLifecycleEvent) bool {
	if len(evt.Payload) == 0 {
		return false
	}

	var p serviceNamePayload
	if err := json.Unmarshal(evt.Payload, &p); err != nil {
		return false
	}

	return p.ServiceName == d.cfg.ServiceName
}

// addTenant adds a tenant to the active set and fires the onAdded callback.
func (d *TenantDiscovery) addTenant(ctx context.Context, tenantID string) {
	d.mu.Lock()
	d.tenants[tenantID] = struct{}{}
	d.mu.Unlock()

	if d.onAdded != nil {
		d.onAdded(ctx, tenantID)
	}
}

// removeTenant removes a tenant from the active set and fires the onRemoved callback.
func (d *TenantDiscovery) removeTenant(ctx context.Context, tenantID string) {
	d.mu.Lock()
	_, existed := d.tenants[tenantID]
	delete(d.tenants, tenantID)
	d.mu.Unlock()

	if existed && d.onRemoved != nil {
		d.onRemoved(ctx, tenantID)
	}
}
