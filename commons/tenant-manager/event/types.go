// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

// Package event defines the shared contract for tenant lifecycle events
// published by tenant-manager and consumed by downstream services.
// It provides the event envelope, typed payload structs, and channel helpers
// for Valkey Streams-based event-driven tenant discovery.
package event

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Event type constants for all tenant lifecycle events.
const (
	// Tenant-level events.
	EventTenantCreated   = "tenant.created"
	EventTenantActivated = "tenant.activated"
	EventTenantSuspended = "tenant.suspended"
	EventTenantDeleted   = "tenant.deleted"
	EventTenantUpdated   = "tenant.updated"

	// Service-level events.
	EventTenantServiceAssociated    = "tenant.service.associated"
	EventTenantServiceDisassociated = "tenant.service.disassociated"
	EventTenantServiceSuspended     = "tenant.service.suspended"
	EventTenantServicePurged        = "tenant.service.purged"
	EventTenantServiceReactivated   = "tenant.service.reactivated"

	// Credential and connection events.
	EventTenantCredentialsRotated = "tenant.credentials.rotated" //nolint:gosec // Not a credential, event type constant
	EventTenantConnectionsUpdated = "tenant.connections.updated"
)

// Channel constants for Valkey Streams pub/sub.
const (
	// ChannelPrefix is the base prefix for all tenant event channels.
	ChannelPrefix = "tenant-events"

	// SubscriptionPattern is the glob pattern to subscribe to all tenant event channels.
	SubscriptionPattern = "tenant-events:*"
)

// TenantLifecycleEvent is the envelope for all tenant lifecycle events.
// The Payload field contains the event-specific data as raw JSON, allowing
// consumers to unmarshal into the appropriate typed payload struct based on EventType.
type TenantLifecycleEvent struct {
	EventID     string          `json:"event_id"`
	EventType   string          `json:"event_type"`
	Timestamp   time.Time       `json:"timestamp"`
	TenantID    string          `json:"tenant_id"`
	TenantSlug  string          `json:"tenant_slug"`
	Environment string          `json:"environment"`
	Payload     json.RawMessage `json:"payload,omitempty"`
}

// ChannelForTenant returns the Valkey Streams channel name for a specific tenant.
// Format: "tenant-events:{tenantID}"
func ChannelForTenant(tenantID string) string {
	return fmt.Sprintf("%s:%s", ChannelPrefix, tenantID)
}

// ParseEvent unmarshals raw JSON bytes into a TenantLifecycleEvent.
// Returns an error if the input is nil, empty, or not valid JSON.
func ParseEvent(data []byte) (*TenantLifecycleEvent, error) {
	if len(data) == 0 {
		return nil, errors.New("event data must not be empty")
	}

	var event TenantLifecycleEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return nil, fmt.Errorf("failed to parse tenant lifecycle event: %w", err)
	}

	return &event, nil
}
