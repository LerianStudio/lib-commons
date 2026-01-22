package poolmanager

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

func TestGetDBForTenant(t *testing.T) {
	t.Run("returns error when no connection in context", func(t *testing.T) {
		ctx := context.Background()

		db, err := GetDBForTenant(ctx)

		assert.Nil(t, db)
		assert.ErrorIs(t, err, ErrConnectionNotFound)
	})
}

func TestSetMultiTenantModeInContext(t *testing.T) {
	t.Run("sets multi-tenant mode to true", func(t *testing.T) {
		ctx := context.Background()

		ctx = SetMultiTenantModeInContext(ctx, true)

		assert.True(t, IsMultiTenantMode(ctx))
	})

	t.Run("sets multi-tenant mode to false", func(t *testing.T) {
		ctx := context.Background()

		ctx = SetMultiTenantModeInContext(ctx, false)

		assert.False(t, IsMultiTenantMode(ctx))
	})
}

func TestIsMultiTenantMode(t *testing.T) {
	t.Run("returns false when not set", func(t *testing.T) {
		ctx := context.Background()

		assert.False(t, IsMultiTenantMode(ctx))
	})

	t.Run("returns true when set to true", func(t *testing.T) {
		ctx := SetMultiTenantModeInContext(context.Background(), true)

		assert.True(t, IsMultiTenantMode(ctx))
	})
}

func TestGetDBForTenantWithFallback(t *testing.T) {
	t.Run("returns error when multi-tenant mode enabled and no tenant context", func(t *testing.T) {
		ctx := SetMultiTenantModeInContext(context.Background(), true)

		db, err := GetDBForTenantWithFallback(ctx, nil)

		assert.Nil(t, db)
		assert.ErrorIs(t, err, ErrTenantContextRequired)
	})

	t.Run("returns error when single-tenant mode and no fallback connection", func(t *testing.T) {
		ctx := context.Background()

		db, err := GetDBForTenantWithFallback(ctx, nil)

		assert.Nil(t, db)
		assert.ErrorIs(t, err, ErrConnectionNotFound)
	})
}
