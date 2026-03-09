package valkey

import (
	"context"
	"testing"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		tenantID string
		key      string
		expected string
	}{
		{name: "prefixes key with tenant", tenantID: "tenant-123", key: "orders", expected: "tenant:tenant-123:orders"},
		{name: "returns key unchanged when tenant empty", tenantID: "", key: "orders", expected: "orders"},
		{name: "handles empty key", tenantID: "tenant-123", key: "", expected: "tenant:tenant-123:"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := GetKey(tt.tenantID, tt.key)

			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetKey_RejectsDelimiterInTenantID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		tenantID string
	}{
		{name: "colon in middle", tenantID: "tenant:123"},
		{name: "colon at start", tenantID: ":tenant"},
		{name: "colon at end", tenantID: "tenant:"},
		{name: "multiple colons", tenantID: "a:b:c"},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := GetKey(tt.tenantID, "orders")

			require.Error(t, err)
			assert.Contains(t, err.Error(), "must not contain delimiter character ':'")
			assert.Empty(t, result)
		})
	}
}

func TestGetKeyFromContext(t *testing.T) {
	t.Parallel()

	ctx := core.SetTenantIDInContext(context.Background(), "tenant-ctx")

	result, err := GetKeyFromContext(ctx, "orders")
	require.NoError(t, err)
	assert.Equal(t, "tenant:tenant-ctx:orders", result)

	result, err = GetKeyFromContext(context.Background(), "orders")
	require.NoError(t, err)
	assert.Equal(t, "orders", result)

	result, err = GetKeyFromContext(nil, "orders")
	require.NoError(t, err)
	assert.Equal(t, "orders", result)
}

func TestGetPattern(t *testing.T) {
	t.Parallel()

	result, err := GetPattern("tenant-123", "orders:*")
	require.NoError(t, err)
	assert.Equal(t, "tenant:tenant-123:orders:*", result)

	result, err = GetPattern("", "orders:*")
	require.NoError(t, err)
	assert.Equal(t, "orders:*", result)
}

func TestGetPattern_RejectsDelimiterInTenantID(t *testing.T) {
	t.Parallel()

	result, err := GetPattern("tenant:123", "orders:*")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not contain delimiter character ':'")
	assert.Empty(t, result)
}

func TestGetPatternFromContext(t *testing.T) {
	t.Parallel()

	ctx := core.SetTenantIDInContext(context.Background(), "tenant-ctx")

	result, err := GetPatternFromContext(ctx, "orders:*")
	require.NoError(t, err)
	assert.Equal(t, "tenant:tenant-ctx:orders:*", result)

	result, err = GetPatternFromContext(context.Background(), "orders:*")
	require.NoError(t, err)
	assert.Equal(t, "orders:*", result)

	result, err = GetPatternFromContext(nil, "orders:*")
	require.NoError(t, err)
	assert.Equal(t, "orders:*", result)
}

func TestStripTenantPrefix(t *testing.T) {
	t.Parallel()

	result, err := StripTenantPrefix("tenant-123", "tenant:tenant-123:orders:1")
	require.NoError(t, err)
	assert.Equal(t, "orders:1", result)

	result, err = StripTenantPrefix("", "orders:1")
	require.NoError(t, err)
	assert.Equal(t, "orders:1", result)

	result, err = StripTenantPrefix("tenant-123", "tenant:other:orders:1")
	require.NoError(t, err)
	assert.Equal(t, "tenant:other:orders:1", result)
}

func TestStripTenantPrefix_RejectsDelimiterInTenantID(t *testing.T) {
	t.Parallel()

	result, err := StripTenantPrefix("tenant:123", "tenant:tenant:123:orders:1")

	require.Error(t, err)
	assert.Contains(t, err.Error(), "must not contain delimiter character ':'")
	assert.Empty(t, result)
}
