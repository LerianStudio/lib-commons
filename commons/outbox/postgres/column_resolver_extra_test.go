//go:build unit

package postgres

import (
	"context"
	"testing"

	libPostgres "github.com/LerianStudio/lib-commons/v5/commons/postgres"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestColumnResolver_RequiresTenant covers the RequiresTenant method.
func TestColumnResolver_RequiresTenant(t *testing.T) {
	t.Parallel()

	client := &libPostgres.Client{}
	resolver, err := NewColumnResolver(client)
	require.NoError(t, err)
	assert.True(t, resolver.RequiresTenant())
}

// TestColumnResolver_TenantColumn_NilReceiver covers the nil receiver guard.
func TestColumnResolver_TenantColumn_NilReceiver(t *testing.T) {
	t.Parallel()

	var resolver *ColumnResolver
	assert.Equal(t, "", resolver.TenantColumn())
}

// TestColumnResolver_TenantColumn_WithValue covers normal case.
func TestColumnResolver_TenantColumn_WithValue(t *testing.T) {
	t.Parallel()

	client := &libPostgres.Client{}
	resolver, err := NewColumnResolver(client, WithColumnResolverTenantColumn("tenant_id"))
	require.NoError(t, err)
	assert.Equal(t, "tenant_id", resolver.TenantColumn())
}

// TestColumnResolver_ApplyTenant covers the no-op method.
func TestColumnResolver_ApplyTenant(t *testing.T) {
	t.Parallel()

	client := &libPostgres.Client{}
	resolver, err := NewColumnResolver(client)
	require.NoError(t, err)

	// ApplyTenant is a no-op, should always return nil
	err = resolver.ApplyTenant(context.Background(), nil, "tenant-1")
	require.NoError(t, err)
}

// TestColumnResolver_DiscoverTenants_NilClient covers the nil client path.
func TestColumnResolver_DiscoverTenants_NilClient(t *testing.T) {
	t.Parallel()

	resolver := &ColumnResolver{client: nil}
	_, err := resolver.DiscoverTenants(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrConnectionRequired)
}
