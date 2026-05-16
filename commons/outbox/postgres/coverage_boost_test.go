//go:build unit

package postgres

import (
	"context"
	"testing"
	"time"

	libPostgres "github.com/LerianStudio/lib-commons/v5/commons/postgres"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// -------------------------------------------------------------------
// SchemaResolver.RequiresTenant
// -------------------------------------------------------------------

func TestSchemaResolver_RequiresTenant_NilReceiver(t *testing.T) {
	t.Parallel()

	var r *SchemaResolver
	assert.True(t, r.RequiresTenant())
}

func TestSchemaResolver_RequiresTenant_NotRequired(t *testing.T) {
	t.Parallel()

	r := &SchemaResolver{requireTenant: false}
	assert.False(t, r.RequiresTenant())
}

func TestSchemaResolver_RequiresTenant_Required(t *testing.T) {
	t.Parallel()

	r := &SchemaResolver{requireTenant: true}
	assert.True(t, r.RequiresTenant())
}

// -------------------------------------------------------------------
// SchemaResolver.ApplyTenant — nil receiver / nil tx guards
// -------------------------------------------------------------------

func TestSchemaResolver_ApplyTenant_NilReceiver(t *testing.T) {
	t.Parallel()

	var r *SchemaResolver
	err := r.ApplyTenant(context.Background(), nil, "")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrConnectionRequired)
}

func TestSchemaResolver_ApplyTenant_NilTx(t *testing.T) {
	t.Parallel()

	r := &SchemaResolver{}
	err := r.ApplyTenant(context.Background(), nil, "tenant-1")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrTransactionRequired)
}

// -------------------------------------------------------------------
// NewSchemaResolver — valid client
// -------------------------------------------------------------------

func TestNewSchemaResolver_ValidClient(t *testing.T) {
	t.Parallel()

	client := &libPostgres.Client{}
	resolver, err := NewSchemaResolver(client)
	require.NoError(t, err)
	require.NotNil(t, resolver)
}

// -------------------------------------------------------------------
// ColumnResolver.cachedTenants — empty cache
// -------------------------------------------------------------------

func TestCachedTenants_EmptyState(t *testing.T) {
	t.Parallel()

	r := &ColumnResolver{}
	// cachedTenants with no cached data returns empty slice + false
	tenants, ok := r.cachedTenants(time.Now())
	assert.Empty(t, tenants)
	assert.False(t, ok)
}

// -------------------------------------------------------------------
// Repository.GetByID — not initialized
// -------------------------------------------------------------------

func TestRepository_GetByID_NotInitializedBoost(t *testing.T) {
	t.Parallel()

	repo, err := NewRepository(
		&libPostgres.Client{},
		noopTenantResolver{},
		noopTenantDiscoverer{},
	)
	require.NoError(t, err)

	// Without a real DB connection, GetByID will fail
	_, err = repo.GetByID(context.Background(), [16]byte{})
	require.Error(t, err)
}
