//go:build unit

package postgres

import (
	"context"
	"testing"
	"time"

	libPostgres "github.com/LerianStudio/lib-commons/v4/commons/postgres"
	"github.com/stretchr/testify/require"
)

func TestNewColumnResolver_NilClient(t *testing.T) {
	t.Parallel()

	resolver, err := NewColumnResolver(nil)
	require.Nil(t, resolver)
	require.ErrorIs(t, err, ErrConnectionRequired)
}

func TestNewColumnResolver_ValidatesIdentifiers(t *testing.T) {
	t.Parallel()

	client := &libPostgres.Client{}

	_, err := NewColumnResolver(client, WithColumnResolverTableName(`public.outbox";drop`))
	require.Error(t, err)
	require.ErrorIs(t, err, ErrInvalidIdentifier)

	_, err = NewColumnResolver(client, WithColumnResolverTenantColumn(`tenant-id`))
	require.Error(t, err)
	require.ErrorIs(t, err, ErrInvalidIdentifier)
}

func TestColumnResolver_DiscoverTenantsNilReceiver(t *testing.T) {
	t.Parallel()

	var resolver *ColumnResolver

	tenants, err := resolver.DiscoverTenants(context.Background())
	require.Nil(t, tenants)
	require.ErrorIs(t, err, ErrConnectionRequired)
}

func TestNewColumnResolver_AppliesTenantDiscoveryTTLOption(t *testing.T) {
	t.Parallel()

	client := &libPostgres.Client{}

	resolver, err := NewColumnResolver(client, WithColumnResolverTenantDiscoveryTTL(2*time.Minute))
	require.NoError(t, err)
	require.Equal(t, 2*time.Minute, resolver.tenantTTL)
}

func TestColumnResolver_DiscoverTenantsReturnsCachedSnapshot(t *testing.T) {
	t.Parallel()

	resolver := &ColumnResolver{
		client:     &libPostgres.Client{},
		tenantTTL:  time.Minute,
		cache:      []string{"tenant-a", "tenant-b"},
		cacheSet:   true,
		cacheUntil: time.Now().UTC().Add(time.Minute),
	}

	tenants, err := resolver.DiscoverTenants(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"tenant-a", "tenant-b"}, tenants)

	tenants[0] = "mutated"
	again, err := resolver.DiscoverTenants(context.Background())
	require.NoError(t, err)
	require.Equal(t, []string{"tenant-a", "tenant-b"}, again)
}

func TestColumnResolver_DiscoverTenantsReturnsCachedEmptySnapshot(t *testing.T) {
	t.Parallel()

	resolver := &ColumnResolver{
		client:     &libPostgres.Client{},
		tenantTTL:  time.Minute,
		cache:      []string{},
		cacheSet:   true,
		cacheUntil: time.Now().UTC().Add(time.Minute),
	}

	tenants, err := resolver.DiscoverTenants(context.Background())
	require.NoError(t, err)
	require.Empty(t, tenants)
}
