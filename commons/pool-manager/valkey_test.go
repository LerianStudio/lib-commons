package poolmanager

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGetKey(t *testing.T) {
	t.Run("returns prefixed key when tenantID provided", func(t *testing.T) {
		key := GetKey("banco-acme", "account:123")
		assert.Equal(t, "tenant:banco-acme:account:123", key)
	})

	t.Run("returns original key when tenantID is empty", func(t *testing.T) {
		key := GetKey("", "account:123")
		assert.Equal(t, "account:123", key)
	})
}

func TestGetKeyFromContext(t *testing.T) {
	t.Run("returns prefixed key when tenantID in context", func(t *testing.T) {
		ctx := SetTenantIDInContext(context.Background(), "banco-acme")
		key := GetKeyFromContext(ctx, "account:123")
		assert.Equal(t, "tenant:banco-acme:account:123", key)
	})

	t.Run("returns original key when no tenantID in context", func(t *testing.T) {
		ctx := context.Background()
		key := GetKeyFromContext(ctx, "account:123")
		assert.Equal(t, "account:123", key)
	})
}

func TestGetPattern(t *testing.T) {
	t.Run("returns prefixed pattern when tenantID provided", func(t *testing.T) {
		pattern := GetPattern("banco-acme", "account:*")
		assert.Equal(t, "tenant:banco-acme:account:*", pattern)
	})

	t.Run("returns original pattern when tenantID is empty", func(t *testing.T) {
		pattern := GetPattern("", "account:*")
		assert.Equal(t, "account:*", pattern)
	})
}

func TestGetPatternFromContext(t *testing.T) {
	t.Run("returns prefixed pattern when tenantID in context", func(t *testing.T) {
		ctx := SetTenantIDInContext(context.Background(), "fintech-xyz")
		pattern := GetPatternFromContext(ctx, "cache:*")
		assert.Equal(t, "tenant:fintech-xyz:cache:*", pattern)
	})

	t.Run("returns original pattern when no tenantID in context", func(t *testing.T) {
		ctx := context.Background()
		pattern := GetPatternFromContext(ctx, "cache:*")
		assert.Equal(t, "cache:*", pattern)
	})
}

func TestStripTenantPrefix(t *testing.T) {
	t.Run("strips prefix from key", func(t *testing.T) {
		key := StripTenantPrefix("banco-acme", "tenant:banco-acme:account:123")
		assert.Equal(t, "account:123", key)
	})

	t.Run("returns key unchanged when tenantID is empty", func(t *testing.T) {
		key := StripTenantPrefix("", "tenant:banco-acme:account:123")
		assert.Equal(t, "tenant:banco-acme:account:123", key)
	})

	t.Run("returns key unchanged when prefix doesn't match", func(t *testing.T) {
		key := StripTenantPrefix("other-tenant", "tenant:banco-acme:account:123")
		assert.Equal(t, "tenant:banco-acme:account:123", key)
	})
}
