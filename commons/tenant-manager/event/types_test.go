// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

//go:build unit

package event

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseEvent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		input       []byte
		wantErr     bool
		wantEventID string
		wantType    string
	}{
		{
			name: "parses valid tenant created event",
			input: []byte(`{
				"event_id": "evt-001",
				"event_type": "tenant.created",
				"timestamp": "2026-03-25T10:00:00Z",
				"tenant_id": "t-123",
				"tenant_slug": "acme",
				"environment": "staging",
				"payload": null
			}`),
			wantErr:     false,
			wantEventID: "evt-001",
			wantType:    EventTenantCreated,
		},
		{
			name: "parses event with raw payload",
			input: []byte(`{
				"event_id": "evt-002",
				"event_type": "tenant.service.associated",
				"timestamp": "2026-03-25T10:00:00Z",
				"tenant_id": "t-123",
				"tenant_slug": "acme",
				"environment": "production",
				"payload": {"service_name": "ledger"}
			}`),
			wantErr:     false,
			wantEventID: "evt-002",
			wantType:    EventTenantServiceAssociated,
		},
		{
			name:    "returns error for invalid JSON",
			input:   []byte(`{invalid`),
			wantErr: true,
		},
		{
			name:    "returns error for empty input",
			input:   []byte{},
			wantErr: true,
		},
		{
			name:    "returns error for nil input",
			input:   nil,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			event, err := ParseEvent(tt.input)

			if tt.wantErr {
				require.Error(t, err)
				assert.Nil(t, event)

				return
			}

			require.NoError(t, err)
			require.NotNil(t, event)
			assert.Equal(t, tt.wantEventID, event.EventID)
			assert.Equal(t, tt.wantType, event.EventType)
		})
	}
}

func TestChannelForTenant(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		tenantID string
		expected string
	}{
		{
			name:     "formats channel with tenant ID",
			tenantID: "t-123",
			expected: "tenant-events:t-123",
		},
		{
			name:     "formats channel with UUID tenant ID",
			tenantID: "550e8400-e29b-41d4-a716-446655440000",
			expected: "tenant-events:550e8400-e29b-41d4-a716-446655440000",
		},
		{
			name:     "formats channel with empty tenant ID",
			tenantID: "",
			expected: "tenant-events:",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := ChannelForTenant(tt.tenantID)

			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestSubscriptionPattern(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "tenant-events:*", SubscriptionPattern)
}

func TestChannelPrefix(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "tenant-events", ChannelPrefix)
}

func TestTenantLifecycleEvent_JSON(t *testing.T) {
	t.Parallel()

	original := TenantLifecycleEvent{
		EventID:     "evt-100",
		EventType:   EventTenantActivated,
		Timestamp:   time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC),
		TenantID:    "t-abc",
		TenantSlug:  "acme-corp",
		Environment: "staging",
		Payload:     json.RawMessage(`{"key":"value"}`),
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded TenantLifecycleEvent

	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, original.EventID, decoded.EventID)
	assert.Equal(t, original.EventType, decoded.EventType)
	assert.True(t, original.Timestamp.Equal(decoded.Timestamp))
	assert.Equal(t, original.TenantID, decoded.TenantID)
	assert.Equal(t, original.TenantSlug, decoded.TenantSlug)
	assert.Equal(t, original.Environment, decoded.Environment)
	assert.JSONEq(t, string(original.Payload), string(decoded.Payload))
}

func TestServiceAssociatedPayload_JSON(t *testing.T) {
	t.Parallel()

	original := ServiceAssociatedPayload{
		ServiceName:   "ledger",
		IsolationMode: "database",
		Modules: map[string]ModuleConfig{
			"onboarding": {
				DatabaseType: "postgresql",
				DatabaseName: "onboarding_db",
			},
			"transaction": {
				DatabaseType: "postgresql",
				DatabaseName: "transaction_db",
			},
		},
		SecretPaths: map[string]string{
			"db_password": "/secrets/tenant/t-123/db",
		},
		MessagingConfig: &MessagingEventConfig{
			RabbitMQ: &RabbitMQEventConfig{
				VHost: "tenant-vhost",
			},
		},
		ConnectionSettings: &ConnectionSettingsPayload{
			MaxOpenConns:     20,
			MaxIdleConns:     10,
			StatementTimeout: "30s",
		},
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded ServiceAssociatedPayload

	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, original.ServiceName, decoded.ServiceName)
	assert.Equal(t, original.IsolationMode, decoded.IsolationMode)
	require.Len(t, decoded.Modules, 2)
	assert.Equal(t, "postgresql", decoded.Modules["onboarding"].DatabaseType)
	assert.Equal(t, "onboarding_db", decoded.Modules["onboarding"].DatabaseName)
	assert.Equal(t, "postgresql", decoded.Modules["transaction"].DatabaseType)
	assert.Equal(t, "transaction_db", decoded.Modules["transaction"].DatabaseName)
	require.NotNil(t, decoded.SecretPaths)
	assert.Equal(t, "/secrets/tenant/t-123/db", decoded.SecretPaths["db_password"])
	require.NotNil(t, decoded.MessagingConfig)
	require.NotNil(t, decoded.MessagingConfig.RabbitMQ)
	assert.Equal(t, "tenant-vhost", decoded.MessagingConfig.RabbitMQ.VHost)
	require.NotNil(t, decoded.ConnectionSettings)
	assert.Equal(t, 20, decoded.ConnectionSettings.MaxOpenConns)
	assert.Equal(t, 10, decoded.ConnectionSettings.MaxIdleConns)
	assert.Equal(t, "30s", decoded.ConnectionSettings.StatementTimeout)
}

func TestServiceDisassociatedPayload_JSON(t *testing.T) {
	t.Parallel()

	original := ServiceDisassociatedPayload{
		ServiceName:  "ledger",
		PreserveData: true,
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded ServiceDisassociatedPayload

	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, original.ServiceName, decoded.ServiceName)
	assert.Equal(t, original.PreserveData, decoded.PreserveData)
}

func TestServiceSuspendedPayload_JSON(t *testing.T) {
	t.Parallel()

	original := ServiceSuspendedPayload{
		ServiceName:    "ledger",
		PreviousStatus: "active",
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded ServiceSuspendedPayload

	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, original.ServiceName, decoded.ServiceName)
	assert.Equal(t, original.PreviousStatus, decoded.PreviousStatus)
}

func TestServicePurgedPayload_JSON(t *testing.T) {
	t.Parallel()

	original := ServicePurgedPayload{
		ServiceName: "ledger",
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded ServicePurgedPayload

	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, original.ServiceName, decoded.ServiceName)
}

func TestServiceReactivatedPayload_JSON(t *testing.T) {
	t.Parallel()

	original := ServiceReactivatedPayload{
		ServiceName:    "ledger",
		PreviousStatus: "suspended",
		ReProvisioned:  true,
		SecretPaths: map[string]string{
			"db_password": "/secrets/tenant/t-123/db",
		},
		ConnectionSettings: &ConnectionSettingsPayload{
			MaxOpenConns:     25,
			MaxIdleConns:     12,
			StatementTimeout: "45s",
		},
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded ServiceReactivatedPayload

	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, original.ServiceName, decoded.ServiceName)
	assert.Equal(t, original.PreviousStatus, decoded.PreviousStatus)
	assert.Equal(t, original.ReProvisioned, decoded.ReProvisioned)
	require.NotNil(t, decoded.SecretPaths)
	assert.Equal(t, "/secrets/tenant/t-123/db", decoded.SecretPaths["db_password"])
	require.NotNil(t, decoded.ConnectionSettings)
	assert.Equal(t, 25, decoded.ConnectionSettings.MaxOpenConns)
}

func TestCredentialsRotatedPayload_JSON(t *testing.T) {
	t.Parallel()

	original := CredentialsRotatedPayload{
		ServiceName:    "ledger",
		CredentialType: "database",
		NewSecretPath:  "/secrets/tenant/t-123/db/v2",
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded CredentialsRotatedPayload

	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, original.ServiceName, decoded.ServiceName)
	assert.Equal(t, original.CredentialType, decoded.CredentialType)
	assert.Equal(t, original.NewSecretPath, decoded.NewSecretPath)
}

func TestConnectionsUpdatedPayload_JSON(t *testing.T) {
	t.Parallel()

	original := ConnectionsUpdatedPayload{
		ServiceName:      "ledger",
		Module:           "onboarding",
		MaxOpenConns:     30,
		MaxIdleConns:     15,
		StatementTimeout: "60s",
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded ConnectionsUpdatedPayload

	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)

	assert.Equal(t, original.ServiceName, decoded.ServiceName)
	assert.Equal(t, original.Module, decoded.Module)
	assert.Equal(t, original.MaxOpenConns, decoded.MaxOpenConns)
	assert.Equal(t, original.MaxIdleConns, decoded.MaxIdleConns)
	assert.Equal(t, original.StatementTimeout, decoded.StatementTimeout)
}

func TestParseEvent_AllEventTypes(t *testing.T) {
	t.Parallel()

	eventTypes := []string{
		EventTenantCreated,
		EventTenantActivated,
		EventTenantSuspended,
		EventTenantDeleted,
		EventTenantUpdated,
		EventTenantServiceAssociated,
		EventTenantServiceDisassociated,
		EventTenantServiceSuspended,
		EventTenantServicePurged,
		EventTenantServiceReactivated,
		EventTenantCredentialsRotated,
		EventTenantConnectionsUpdated,
	}

	for _, eventType := range eventTypes {
		eventType := eventType
		t.Run(eventType, func(t *testing.T) {
			t.Parallel()

			raw := `{
				"event_id": "evt-` + eventType + `",
				"event_type": "` + eventType + `",
				"timestamp": "2026-03-25T10:00:00Z",
				"tenant_id": "t-123",
				"tenant_slug": "acme",
				"environment": "staging"
			}`

			event, err := ParseEvent([]byte(raw))

			require.NoError(t, err)
			require.NotNil(t, event)
			assert.Equal(t, eventType, event.EventType)
			assert.Equal(t, "evt-"+eventType, event.EventID)
			assert.Equal(t, "t-123", event.TenantID)
			assert.Equal(t, "acme", event.TenantSlug)
			assert.Equal(t, "staging", event.Environment)
		})
	}
}

func TestEventTypeConstants(t *testing.T) {
	t.Parallel()

	// Verify all 12 event type constants have the expected values
	tests := []struct {
		name     string
		constant string
		expected string
	}{
		{name: "tenant created", constant: EventTenantCreated, expected: "tenant.created"},
		{name: "tenant activated", constant: EventTenantActivated, expected: "tenant.activated"},
		{name: "tenant suspended", constant: EventTenantSuspended, expected: "tenant.suspended"},
		{name: "tenant deleted", constant: EventTenantDeleted, expected: "tenant.deleted"},
		{name: "tenant updated", constant: EventTenantUpdated, expected: "tenant.updated"},
		{name: "service associated", constant: EventTenantServiceAssociated, expected: "tenant.service.associated"},
		{name: "service disassociated", constant: EventTenantServiceDisassociated, expected: "tenant.service.disassociated"},
		{name: "service suspended", constant: EventTenantServiceSuspended, expected: "tenant.service.suspended"},
		{name: "service purged", constant: EventTenantServicePurged, expected: "tenant.service.purged"},
		{name: "service reactivated", constant: EventTenantServiceReactivated, expected: "tenant.service.reactivated"},
		{name: "credentials rotated", constant: EventTenantCredentialsRotated, expected: "tenant.credentials.rotated"},
		{name: "connections updated", constant: EventTenantConnectionsUpdated, expected: "tenant.connections.updated"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.expected, tt.constant)
		})
	}
}
