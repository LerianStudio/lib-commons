package poolmanager

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewPool(t *testing.T) {
	t.Run("creates pool with client and service", func(t *testing.T) {
		client := &Client{baseURL: "http://localhost:8080"}
		pool := NewPool(client, "ledger")

		assert.NotNil(t, pool)
		assert.Equal(t, "ledger", pool.service)
		assert.NotNil(t, pool.connections)
	})
}

func TestPool_GetConnection_NoTenantID(t *testing.T) {
	client := &Client{baseURL: "http://localhost:8080"}
	pool := NewPool(client, "ledger")

	_, err := pool.GetConnection(context.Background(), "")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "tenant ID is required")
}

func TestPool_Close(t *testing.T) {
	client := &Client{baseURL: "http://localhost:8080"}
	pool := NewPool(client, "ledger")

	err := pool.Close()

	assert.NoError(t, err)
	assert.True(t, pool.closed)
}

func TestPool_GetConnection_PoolClosed(t *testing.T) {
	client := &Client{baseURL: "http://localhost:8080"}
	pool := NewPool(client, "ledger")
	pool.Close()

	_, err := pool.GetConnection(context.Background(), "tenant-123")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrPoolClosed)
}

func TestSchemaNameFromTenantID(t *testing.T) {
	tests := []struct {
		name     string
		tenantID string
		expected string
	}{
		{
			name:     "converts UUID with hyphens to underscores",
			tenantID: "550e8400-e29b-41d4-a716-446655440000",
			expected: "tenant_550e8400_e29b_41d4_a716_446655440000",
		},
		{
			name:     "handles UUID without hyphens",
			tenantID: "550e8400e29b41d4a716446655440000",
			expected: "tenant_550e8400e29b41d4a716446655440000",
		},
		{
			name:     "handles simple tenant ID",
			tenantID: "tenant123",
			expected: "tenant_tenant123",
		},
		{
			name:     "handles empty tenant ID",
			tenantID: "",
			expected: "tenant_",
		},
		{
			name:     "handles multiple consecutive hyphens",
			tenantID: "test--tenant---id",
			expected: "tenant_test__tenant___id",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := SchemaNameFromTenantID(tt.tenantID)

			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestIsolationModeConstants(t *testing.T) {
	t.Run("isolation mode constants have expected values", func(t *testing.T) {
		assert.Equal(t, "isolated", IsolationModeIsolated)
		assert.Equal(t, "schema", IsolationModeSchema)
	})
}
