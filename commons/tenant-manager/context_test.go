package tenantmanager

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSetTenantIDInContext(t *testing.T) {
	ctx := context.Background()

	ctx = SetTenantIDInContext(ctx, "tenant-123")

	assert.Equal(t, "tenant-123", GetTenantIDFromContext(ctx))
}

func TestGetTenantIDFromContext_NotSet(t *testing.T) {
	ctx := context.Background()

	id := GetTenantIDFromContext(ctx)

	assert.Equal(t, "", id)
}

func TestHasTenantContext(t *testing.T) {
	t.Run("returns true when tenant ID is set", func(t *testing.T) {
		ctx := SetTenantIDInContext(context.Background(), "tenant-123")

		assert.True(t, HasTenantContext(ctx))
	})

	t.Run("returns false when tenant ID is not set", func(t *testing.T) {
		ctx := context.Background()

		assert.False(t, HasTenantContext(ctx))
	})
}

func TestContextWithTenantID(t *testing.T) {
	ctx := context.Background()

	ctx = ContextWithTenantID(ctx, "tenant-456")

	assert.Equal(t, "tenant-456", GetTenantIDFromContext(ctx))
}

func TestGetPostgresForTenant(t *testing.T) {
	t.Run("returns error when no connection in context", func(t *testing.T) {
		ctx := context.Background()

		db, err := GetPostgresForTenant(ctx)

		assert.Nil(t, db)
		assert.ErrorIs(t, err, ErrTenantContextRequired)
	})
}
