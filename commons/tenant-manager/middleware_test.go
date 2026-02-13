package tenantmanager

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewTenantMiddleware(t *testing.T) {
	t.Run("creates disabled middleware when no managers are configured", func(t *testing.T) {
		middleware := NewTenantMiddleware()

		assert.NotNil(t, middleware)
		assert.False(t, middleware.Enabled())
		assert.Nil(t, middleware.postgres)
		assert.Nil(t, middleware.mongo)
	})

	t.Run("creates enabled middleware with PostgreSQL only", func(t *testing.T) {
		client := &Client{baseURL: "http://localhost:8080"}
		pgManager := NewPostgresManager(client, "ledger")

		middleware := NewTenantMiddleware(WithPostgresManager(pgManager))

		assert.NotNil(t, middleware)
		assert.True(t, middleware.Enabled())
		assert.Equal(t, pgManager, middleware.postgres)
		assert.Nil(t, middleware.mongo)
	})

	t.Run("creates enabled middleware with MongoDB only", func(t *testing.T) {
		client := &Client{baseURL: "http://localhost:8080"}
		mongoManager := NewMongoManager(client, "ledger")

		middleware := NewTenantMiddleware(WithMongoManager(mongoManager))

		assert.NotNil(t, middleware)
		assert.True(t, middleware.Enabled())
		assert.Nil(t, middleware.postgres)
		assert.Equal(t, mongoManager, middleware.mongo)
	})

	t.Run("creates middleware with both PostgreSQL and MongoDB managers", func(t *testing.T) {
		client := &Client{baseURL: "http://localhost:8080"}
		pgManager := NewPostgresManager(client, "ledger")
		mongoManager := NewMongoManager(client, "ledger")

		middleware := NewTenantMiddleware(
			WithPostgresManager(pgManager),
			WithMongoManager(mongoManager),
		)

		assert.NotNil(t, middleware)
		assert.True(t, middleware.Enabled())
		assert.Equal(t, pgManager, middleware.postgres)
		assert.Equal(t, mongoManager, middleware.mongo)
	})
}

func TestWithPostgresManager(t *testing.T) {
	t.Run("sets postgres manager on middleware", func(t *testing.T) {
		client := &Client{baseURL: "http://localhost:8080"}
		pgManager := NewPostgresManager(client, "ledger")

		middleware := NewTenantMiddleware()
		assert.Nil(t, middleware.postgres)
		assert.False(t, middleware.Enabled())

		// Apply option manually
		opt := WithPostgresManager(pgManager)
		opt(middleware)

		assert.Equal(t, pgManager, middleware.postgres)
		assert.True(t, middleware.Enabled())
	})

	t.Run("enables middleware when postgres manager is set", func(t *testing.T) {
		client := &Client{baseURL: "http://localhost:8080"}
		pgManager := NewPostgresManager(client, "ledger")

		middleware := &TenantMiddleware{}
		assert.False(t, middleware.enabled)

		opt := WithPostgresManager(pgManager)
		opt(middleware)

		assert.True(t, middleware.enabled)
	})
}

func TestWithMongoManager(t *testing.T) {
	t.Run("sets mongo manager on middleware", func(t *testing.T) {
		client := &Client{baseURL: "http://localhost:8080"}
		mongoManager := NewMongoManager(client, "ledger")

		middleware := NewTenantMiddleware()
		assert.Nil(t, middleware.mongo)
		assert.False(t, middleware.Enabled())

		// Apply option manually
		opt := WithMongoManager(mongoManager)
		opt(middleware)

		assert.Equal(t, mongoManager, middleware.mongo)
		assert.True(t, middleware.Enabled())
	})

	t.Run("enables middleware when mongo manager is set", func(t *testing.T) {
		client := &Client{baseURL: "http://localhost:8080"}
		mongoManager := NewMongoManager(client, "ledger")

		middleware := &TenantMiddleware{}
		assert.False(t, middleware.enabled)

		opt := WithMongoManager(mongoManager)
		opt(middleware)

		assert.True(t, middleware.enabled)
	})
}

func TestTenantMiddleware_Enabled(t *testing.T) {
	t.Run("returns false when no managers are configured", func(t *testing.T) {
		middleware := NewTenantMiddleware()
		assert.False(t, middleware.Enabled())
	})

	t.Run("returns true when only PostgreSQL manager is set", func(t *testing.T) {
		client := &Client{baseURL: "http://localhost:8080"}
		pgManager := NewPostgresManager(client, "ledger")

		middleware := NewTenantMiddleware(WithPostgresManager(pgManager))
		assert.True(t, middleware.Enabled())
	})

	t.Run("returns true when only MongoDB manager is set", func(t *testing.T) {
		client := &Client{baseURL: "http://localhost:8080"}
		mongoManager := NewMongoManager(client, "ledger")

		middleware := NewTenantMiddleware(WithMongoManager(mongoManager))
		assert.True(t, middleware.Enabled())
	})

	t.Run("returns true when both managers are set", func(t *testing.T) {
		client := &Client{baseURL: "http://localhost:8080"}
		pgManager := NewPostgresManager(client, "ledger")
		mongoManager := NewMongoManager(client, "ledger")

		middleware := NewTenantMiddleware(
			WithPostgresManager(pgManager),
			WithMongoManager(mongoManager),
		)
		assert.True(t, middleware.Enabled())
	})
}
