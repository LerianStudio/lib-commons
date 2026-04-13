// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package event

import "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"

// ServiceAssociatedPayload is the typed payload for EventTenantServiceAssociated events.
// It contains all information needed for a downstream service to provision
// database connections, messaging, and secrets for a newly associated tenant.
type ServiceAssociatedPayload struct {
	ServiceName        string                       `json:"service_name"`
	IsolationMode      string                       `json:"isolation_mode"`
	Modules            []string                     `json:"modules,omitempty"`
	SecretPaths        map[string]map[string]string `json:"secret_paths,omitempty"`
	MessagingConfig    *MessagingEventConfig        `json:"messaging_config,omitempty"`
	ConnectionSettings *ConnectionSettingsPayload   `json:"connection_settings,omitempty"`
}

// MessagingEventConfig holds messaging configuration for service-level events.
// The RabbitMQSecretPath points to a Secrets Manager path; actual credentials
// are resolved lazily by the connection manager.
type MessagingEventConfig struct {
	RabbitMQSecretPath string `json:"rabbitmq_secret_path,omitempty"`
}

// ConnectionSettingsPayload holds connection pool settings included in event payloads.
type ConnectionSettingsPayload struct {
	MaxOpenConns     int    `json:"max_open_conns"`
	MaxIdleConns     int    `json:"max_idle_conns"`
	StatementTimeout string `json:"statement_timeout,omitempty"`
}

// ServiceDisassociatedPayload is the typed payload for EventTenantServiceDisassociated events.
type ServiceDisassociatedPayload struct {
	ServiceName  string `json:"service_name"`
	PreserveData bool   `json:"preserve_data"`
}

// ServiceSuspendedPayload is the typed payload for EventTenantServiceSuspended events.
type ServiceSuspendedPayload struct {
	ServiceName    string `json:"service_name"`
	PreviousStatus string `json:"previous_status"`
}

// ServicePurgedPayload is the typed payload for EventTenantServicePurged events.
type ServicePurgedPayload struct {
	ServiceName string `json:"service_name"`
}

// ServiceReactivatedPayload is the typed payload for EventTenantServiceReactivated events.
type ServiceReactivatedPayload struct {
	ServiceName        string                       `json:"service_name"`
	PreviousStatus     string                       `json:"previous_status"`
	ReProvisioned      bool                         `json:"re_provisioned"`
	SecretPaths        map[string]map[string]string `json:"secret_paths,omitempty"`
	ConnectionSettings *ConnectionSettingsPayload   `json:"connection_settings,omitempty"`
}

// CredentialsRotatedPayload is the typed payload for EventTenantCredentialsRotated events.
type CredentialsRotatedPayload struct {
	ServiceName    string `json:"service_name"`
	CredentialType string `json:"credential_type"`
	NewSecretPath  string `json:"new_secret_path"`
}

// ConnectionsUpdatedPayload is the typed payload for EventTenantConnectionsUpdated events.
type ConnectionsUpdatedPayload struct {
	ServiceName      string `json:"service_name"`
	Module           string `json:"module"`
	MaxOpenConns     int    `json:"max_open_conns"`
	MaxIdleConns     int    `json:"max_idle_conns"`
	StatementTimeout string `json:"statement_timeout,omitempty"`
}

// RateLimitUpdatedPayload is the typed payload for EventTenantRateLimitUpdated events.
// Published when a tenant's rate limit configuration is changed in the tenant-manager.
//
// The RateLimiting field is part of the API contract for external consumers that
// may want to inspect the new configuration inline. The built-in event handler
// uses a push-to-fetch pattern: the event signals "go re-fetch from the API"
// rather than applying the inline data directly, ensuring the cache is populated
// via the same code path as initial loads.
type RateLimitUpdatedPayload struct {
	ServiceName  string                     `json:"service_name"`
	RateLimiting map[string]*core.TierLimit `json:"rate_limiting"`
}
