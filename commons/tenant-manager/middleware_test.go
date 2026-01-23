package tenantmanager

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewTenantMiddleware(t *testing.T) {
	t.Run("creates disabled middleware when no pools are configured", func(t *testing.T) {
		middleware := NewTenantMiddleware()

		assert.NotNil(t, middleware)
		assert.False(t, middleware.Enabled())
		assert.Nil(t, middleware.pool)
		assert.Nil(t, middleware.mongoPool)
	})

	t.Run("creates enabled middleware with PostgreSQL only", func(t *testing.T) {
		client := &Client{baseURL: "http://localhost:8080"}
		pool := NewPool(client, "ledger")

		middleware := NewTenantMiddleware(WithPostgresPool(pool))

		assert.NotNil(t, middleware)
		assert.True(t, middleware.Enabled())
		assert.Equal(t, pool, middleware.pool)
		assert.Nil(t, middleware.mongoPool)
	})

	t.Run("creates enabled middleware with MongoDB only", func(t *testing.T) {
		client := &Client{baseURL: "http://localhost:8080"}
		mongoPool := NewMongoPool(client, "ledger")

		middleware := NewTenantMiddleware(WithMongoPool(mongoPool))

		assert.NotNil(t, middleware)
		assert.True(t, middleware.Enabled())
		assert.Nil(t, middleware.pool)
		assert.Equal(t, mongoPool, middleware.mongoPool)
	})

	t.Run("creates middleware with both PostgreSQL and MongoDB pools", func(t *testing.T) {
		client := &Client{baseURL: "http://localhost:8080"}
		pgPool := NewPool(client, "ledger")
		mongoPool := NewMongoPool(client, "ledger")

		middleware := NewTenantMiddleware(
			WithPostgresPool(pgPool),
			WithMongoPool(mongoPool),
		)

		assert.NotNil(t, middleware)
		assert.True(t, middleware.Enabled())
		assert.Equal(t, pgPool, middleware.pool)
		assert.Equal(t, mongoPool, middleware.mongoPool)
	})
}

func TestWithPostgresPool(t *testing.T) {
	t.Run("sets postgres pool on middleware", func(t *testing.T) {
		client := &Client{baseURL: "http://localhost:8080"}
		pgPool := NewPool(client, "ledger")

		middleware := NewTenantMiddleware()
		assert.Nil(t, middleware.pool)
		assert.False(t, middleware.Enabled())

		// Apply option manually
		opt := WithPostgresPool(pgPool)
		opt(middleware)

		assert.Equal(t, pgPool, middleware.pool)
		assert.True(t, middleware.Enabled())
	})

	t.Run("enables middleware when postgres pool is set", func(t *testing.T) {
		client := &Client{baseURL: "http://localhost:8080"}
		pgPool := NewPool(client, "ledger")

		middleware := &TenantMiddleware{}
		assert.False(t, middleware.enabled)

		opt := WithPostgresPool(pgPool)
		opt(middleware)

		assert.True(t, middleware.enabled)
	})
}

func TestWithMongoPool(t *testing.T) {
	t.Run("sets mongo pool on middleware", func(t *testing.T) {
		client := &Client{baseURL: "http://localhost:8080"}
		mongoPool := NewMongoPool(client, "ledger")

		middleware := NewTenantMiddleware()
		assert.Nil(t, middleware.mongoPool)
		assert.False(t, middleware.Enabled())

		// Apply option manually
		opt := WithMongoPool(mongoPool)
		opt(middleware)

		assert.Equal(t, mongoPool, middleware.mongoPool)
		assert.True(t, middleware.Enabled())
	})

	t.Run("enables middleware when mongo pool is set", func(t *testing.T) {
		client := &Client{baseURL: "http://localhost:8080"}
		mongoPool := NewMongoPool(client, "ledger")

		middleware := &TenantMiddleware{}
		assert.False(t, middleware.enabled)

		opt := WithMongoPool(mongoPool)
		opt(middleware)

		assert.True(t, middleware.enabled)
	})
}

func TestTenantMiddleware_Enabled(t *testing.T) {
	t.Run("returns false when no pools are configured", func(t *testing.T) {
		middleware := NewTenantMiddleware()
		assert.False(t, middleware.Enabled())
	})

	t.Run("returns true when only PostgreSQL pool is set", func(t *testing.T) {
		client := &Client{baseURL: "http://localhost:8080"}
		pool := NewPool(client, "ledger")

		middleware := NewTenantMiddleware(WithPostgresPool(pool))
		assert.True(t, middleware.Enabled())
	})

	t.Run("returns true when only MongoDB pool is set", func(t *testing.T) {
		client := &Client{baseURL: "http://localhost:8080"}
		mongoPool := NewMongoPool(client, "ledger")

		middleware := NewTenantMiddleware(WithMongoPool(mongoPool))
		assert.True(t, middleware.Enabled())
	})

	t.Run("returns true when both pools are set", func(t *testing.T) {
		client := &Client{baseURL: "http://localhost:8080"}
		pgPool := NewPool(client, "ledger")
		mongoPool := NewMongoPool(client, "ledger")

		middleware := NewTenantMiddleware(
			WithPostgresPool(pgPool),
			WithMongoPool(mongoPool),
		)
		assert.True(t, middleware.Enabled())
	})
}
