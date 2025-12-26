package poolmanager

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bxcodec/dbresolver/v2"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContextKey_Constants tests that context key constants are defined correctly.
func TestContextKey_Constants(t *testing.T) {
	t.Run("Should have correct context key values", func(t *testing.T) {
		assert.Equal(t, ContextKey("tenantId"), TenantIDContextKey)
		assert.Equal(t, ContextKey("tenant_config"), TenantConfigContextKey)
		assert.Equal(t, ContextKey("tenant_pg"), TenantPGContextKey)
		assert.Equal(t, ContextKey("tenant_mongo"), TenantMongoContextKey)
		assert.Equal(t, ContextKey("tenant_valkey"), TenantValkeyContextKey)
		assert.Equal(t, ContextKey("tenant_rabbitmq"), TenantRabbitMQContextKey)
	})
}

// TestDBType_Constants tests that database type constants are defined correctly.
func TestDBType_Constants(t *testing.T) {
	t.Run("Should have correct database type values", func(t *testing.T) {
		assert.Equal(t, DBType("postgresql"), DBTypePostgreSQL)
		assert.Equal(t, DBType("mongodb"), DBTypeMongoDB)
	})
}

// TestGetDBFromContext tests the GetDBFromContext function.
func TestGetDBFromContext(t *testing.T) {
	t.Run("Should return nil when context is nil", func(t *testing.T) {
		result := GetDBFromContext(nil, DBTypePostgreSQL)
		assert.Nil(t, result)
	})

	t.Run("Should return nil when database type is unknown", func(t *testing.T) {
		ctx := context.Background()
		result := GetDBFromContext(ctx, DBType("unknown"))
		assert.Nil(t, result)
	})

	t.Run("Should return PostgreSQL config when type is postgresql", func(t *testing.T) {
		expected := &PostgreSQLConfig{
			Host:     "localhost",
			Port:     5432,
			Database: "test",
			Username: "user",
			Password: "pass",
		}
		ctx := WithTenantPG(context.Background(), expected)

		result := GetDBFromContext(ctx, DBTypePostgreSQL)
		require.NotNil(t, result)

		pgConfig, ok := result.(*PostgreSQLConfig)
		require.True(t, ok)
		assert.Equal(t, expected, pgConfig)
	})

	t.Run("Should return MongoDB config when type is mongodb", func(t *testing.T) {
		expected := &MongoDBConfig{
			URI:      "mongodb://localhost:27017",
			Database: "test",
		}
		ctx := WithTenantMongo(context.Background(), expected)

		result := GetDBFromContext(ctx, DBTypeMongoDB)
		require.NotNil(t, result)

		mongoConfig, ok := result.(*MongoDBConfig)
		require.True(t, ok)
		assert.Equal(t, expected, mongoConfig)
	})

	t.Run("Should return nil when no config is set", func(t *testing.T) {
		ctx := context.Background()

		resultPG := GetDBFromContext(ctx, DBTypePostgreSQL)
		assert.Nil(t, resultPG)

		resultMongo := GetDBFromContext(ctx, DBTypeMongoDB)
		assert.Nil(t, resultMongo)
	})
}

// TestWithTenantID tests the WithTenantID function.
func TestWithTenantID(t *testing.T) {
	t.Run("Should set tenant ID in context", func(t *testing.T) {
		ctx := WithTenantID(context.Background(), "tenant-123")

		tenantID := GetTenantID(ctx)
		assert.Equal(t, "tenant-123", tenantID)
	})

	t.Run("Should create context from nil", func(t *testing.T) {
		ctx := WithTenantID(nil, "tenant-123")
		require.NotNil(t, ctx)

		tenantID := GetTenantID(ctx)
		assert.Equal(t, "tenant-123", tenantID)
	})

	t.Run("Should handle empty tenant ID", func(t *testing.T) {
		ctx := WithTenantID(context.Background(), "")

		tenantID := GetTenantID(ctx)
		assert.Equal(t, "", tenantID)
	})
}

// TestWithTenantConfig tests the WithTenantConfig function.
func TestWithTenantConfig(t *testing.T) {
	t.Run("Should set tenant config in context", func(t *testing.T) {
		expected := &TenantConfig{
			ID:         "tenant-123",
			TenantName: "Test Tenant",
			Status:     "active",
		}
		ctx := WithTenantConfig(context.Background(), expected)

		config := GetTenantConfig(ctx)
		assert.Equal(t, expected, config)
	})

	t.Run("Should create context from nil", func(t *testing.T) {
		expected := &TenantConfig{ID: "tenant-123"}
		ctx := WithTenantConfig(nil, expected)
		require.NotNil(t, ctx)

		config := GetTenantConfig(ctx)
		assert.Equal(t, expected, config)
	})

	t.Run("Should handle nil config", func(t *testing.T) {
		ctx := WithTenantConfig(context.Background(), nil)

		config := GetTenantConfig(ctx)
		assert.Nil(t, config)
	})
}

// TestWithTenantPG tests the WithTenantPG function.
func TestWithTenantPG(t *testing.T) {
	t.Run("Should set PostgreSQL config in context", func(t *testing.T) {
		expected := &PostgreSQLConfig{
			Host:     "pg.local",
			Port:     5432,
			Database: "midaz",
			Username: "user",
			Password: "pass",
			SSLMode:  "disable",
		}
		ctx := WithTenantPG(context.Background(), expected)

		config := GetTenantPostgreSQL(ctx)
		assert.Equal(t, expected, config)
	})

	t.Run("Should create context from nil", func(t *testing.T) {
		expected := &PostgreSQLConfig{Host: "localhost"}
		ctx := WithTenantPG(nil, expected)
		require.NotNil(t, ctx)

		config := GetTenantPostgreSQL(ctx)
		assert.Equal(t, expected, config)
	})

	t.Run("Should handle nil config", func(t *testing.T) {
		ctx := WithTenantPG(context.Background(), nil)

		config := GetTenantPostgreSQL(ctx)
		assert.Nil(t, config)
	})
}

// TestWithTenantMongo tests the WithTenantMongo function.
func TestWithTenantMongo(t *testing.T) {
	t.Run("Should set MongoDB config in context", func(t *testing.T) {
		expected := &MongoDBConfig{
			URI:      "mongodb://localhost:27017",
			Database: "midaz",
		}
		ctx := WithTenantMongo(context.Background(), expected)

		config := GetTenantMongoDB(ctx)
		assert.Equal(t, expected, config)
	})

	t.Run("Should create context from nil", func(t *testing.T) {
		expected := &MongoDBConfig{URI: "mongodb://localhost"}
		ctx := WithTenantMongo(nil, expected)
		require.NotNil(t, ctx)

		config := GetTenantMongoDB(ctx)
		assert.Equal(t, expected, config)
	})

	t.Run("Should handle nil config", func(t *testing.T) {
		ctx := WithTenantMongo(context.Background(), nil)

		config := GetTenantMongoDB(ctx)
		assert.Nil(t, config)
	})
}

// TestWithTenantValkey tests the WithTenantValkey function.
func TestWithTenantValkey(t *testing.T) {
	t.Run("Should set Valkey config in context", func(t *testing.T) {
		expected := &ValkeyConfig{
			Addresses: []string{"localhost:6379"},
			Password:  "secret",
			DB:        0,
		}
		ctx := WithTenantValkey(context.Background(), expected)

		config := GetTenantValkey(ctx)
		assert.Equal(t, expected, config)
	})

	t.Run("Should create context from nil", func(t *testing.T) {
		expected := &ValkeyConfig{Addresses: []string{"localhost:6379"}}
		ctx := WithTenantValkey(nil, expected)
		require.NotNil(t, ctx)

		config := GetTenantValkey(ctx)
		assert.Equal(t, expected, config)
	})

	t.Run("Should handle nil config", func(t *testing.T) {
		ctx := WithTenantValkey(context.Background(), nil)

		config := GetTenantValkey(ctx)
		assert.Nil(t, config)
	})
}

// TestWithTenantRabbitMQ tests the WithTenantRabbitMQ function.
func TestWithTenantRabbitMQ(t *testing.T) {
	t.Run("Should set RabbitMQ config in context", func(t *testing.T) {
		expected := &RabbitMQConfig{
			URL: "amqp://localhost:5672",
		}
		ctx := WithTenantRabbitMQ(context.Background(), expected)

		config := GetTenantRabbitMQ(ctx)
		assert.Equal(t, expected, config)
	})

	t.Run("Should create context from nil", func(t *testing.T) {
		expected := &RabbitMQConfig{URL: "amqp://localhost:5672"}
		ctx := WithTenantRabbitMQ(nil, expected)
		require.NotNil(t, ctx)

		config := GetTenantRabbitMQ(ctx)
		assert.Equal(t, expected, config)
	})

	t.Run("Should handle nil config", func(t *testing.T) {
		ctx := WithTenantRabbitMQ(context.Background(), nil)

		config := GetTenantRabbitMQ(ctx)
		assert.Nil(t, config)
	})
}

// TestGetTenantValkey tests the GetTenantValkey function.
func TestGetTenantValkey(t *testing.T) {
	t.Run("Should return nil when context is nil", func(t *testing.T) {
		config := GetTenantValkey(nil)
		assert.Nil(t, config)
	})

	t.Run("Should return nil when value is not set", func(t *testing.T) {
		ctx := context.Background()
		config := GetTenantValkey(ctx)
		assert.Nil(t, config)
	})

	t.Run("Should return nil when value is wrong type", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), TenantValkeyContextKey, "invalid")
		config := GetTenantValkey(ctx)
		assert.Nil(t, config)
	})

	t.Run("Should return config when set correctly", func(t *testing.T) {
		expected := &ValkeyConfig{
			Addresses: []string{"localhost:6379"},
			Password:  "secret",
			DB:        1,
		}
		ctx := context.WithValue(context.Background(), TenantValkeyContextKey, expected)

		config := GetTenantValkey(ctx)
		assert.Equal(t, expected, config)
	})
}

// TestGetTenantRabbitMQ tests the GetTenantRabbitMQ function.
func TestGetTenantRabbitMQ(t *testing.T) {
	t.Run("Should return nil when context is nil", func(t *testing.T) {
		config := GetTenantRabbitMQ(nil)
		assert.Nil(t, config)
	})

	t.Run("Should return nil when value is not set", func(t *testing.T) {
		ctx := context.Background()
		config := GetTenantRabbitMQ(ctx)
		assert.Nil(t, config)
	})

	t.Run("Should return nil when value is wrong type", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), TenantRabbitMQContextKey, "invalid")
		config := GetTenantRabbitMQ(ctx)
		assert.Nil(t, config)
	})

	t.Run("Should return config when set correctly", func(t *testing.T) {
		expected := &RabbitMQConfig{
			URL: "amqp://user:pass@localhost:5672/vhost",
		}
		ctx := context.WithValue(context.Background(), TenantRabbitMQContextKey, expected)

		config := GetTenantRabbitMQ(ctx)
		assert.Equal(t, expected, config)
	})
}

// TestWithAllTenantContext tests the WithAllTenantContext function.
func TestWithAllTenantContext(t *testing.T) {
	t.Run("Should set all tenant context values", func(t *testing.T) {
		tenantID := "tenant-123"
		tenantConfig := &TenantConfig{
			ID:         "tenant-123",
			TenantName: "Test Tenant",
			Status:     "active",
		}
		pgConfig := &PostgreSQLConfig{
			Host:     "pg.local",
			Port:     5432,
			Database: "midaz",
		}
		mongoConfig := &MongoDBConfig{
			URI:      "mongodb://localhost:27017",
			Database: "midaz",
		}
		valkeyConfig := &ValkeyConfig{
			Addresses: []string{"localhost:6379"},
		}
		rabbitConfig := &RabbitMQConfig{
			URL: "amqp://localhost:5672",
		}

		ctx := WithAllTenantContext(
			context.Background(),
			tenantID,
			tenantConfig,
			pgConfig,
			mongoConfig,
			valkeyConfig,
			rabbitConfig,
		)

		assert.Equal(t, tenantID, GetTenantID(ctx))
		assert.Equal(t, tenantConfig, GetTenantConfig(ctx))
		assert.Equal(t, pgConfig, GetTenantPostgreSQL(ctx))
		assert.Equal(t, mongoConfig, GetTenantMongoDB(ctx))
		assert.Equal(t, valkeyConfig, GetTenantValkey(ctx))
		assert.Equal(t, rabbitConfig, GetTenantRabbitMQ(ctx))
	})

	t.Run("Should create context from nil", func(t *testing.T) {
		ctx := WithAllTenantContext(nil, "tenant-123", nil, nil, nil, nil, nil)
		require.NotNil(t, ctx)

		assert.Equal(t, "tenant-123", GetTenantID(ctx))
	})

	t.Run("Should handle partial values", func(t *testing.T) {
		pgConfig := &PostgreSQLConfig{Host: "localhost"}

		ctx := WithAllTenantContext(
			context.Background(),
			"tenant-123",
			nil,
			pgConfig,
			nil,
			nil,
			nil,
		)

		assert.Equal(t, "tenant-123", GetTenantID(ctx))
		assert.Nil(t, GetTenantConfig(ctx))
		assert.Equal(t, pgConfig, GetTenantPostgreSQL(ctx))
		assert.Nil(t, GetTenantMongoDB(ctx))
		assert.Nil(t, GetTenantValkey(ctx))
		assert.Nil(t, GetTenantRabbitMQ(ctx))
	})

	t.Run("Should skip empty tenant ID", func(t *testing.T) {
		ctx := WithAllTenantContext(
			context.Background(),
			"",
			nil,
			nil,
			nil,
			nil,
			nil,
		)

		assert.Equal(t, "", GetTenantID(ctx))
	})
}

// TestGetTenantIDFromFiber tests the GetTenantIDFromFiber function.
func TestGetTenantIDFromFiber(t *testing.T) {
	t.Run("Should return empty string when Fiber context is nil", func(t *testing.T) {
		tenantID := GetTenantIDFromFiber(nil)
		assert.Empty(t, tenantID)
	})

	t.Run("Should return tenant ID from Fiber context", func(t *testing.T) {
		app := fiber.New()

		var extractedTenantID string
		app.Get("/test", func(c *fiber.Ctx) error {
			// Set tenant ID in user context
			ctx := WithTenantID(c.UserContext(), "tenant-123")
			c.SetUserContext(ctx)

			extractedTenantID = GetTenantIDFromFiber(c)

			return c.SendString("OK")
		})

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		_, err := app.Test(req)
		require.NoError(t, err)

		assert.Equal(t, "tenant-123", extractedTenantID)
	})
}

// TestGetTenantConfigFromFiber tests the GetTenantConfigFromFiber function.
func TestGetTenantConfigFromFiber(t *testing.T) {
	t.Run("Should return nil when Fiber context is nil", func(t *testing.T) {
		config := GetTenantConfigFromFiber(nil)
		assert.Nil(t, config)
	})

	t.Run("Should return tenant config from Fiber context", func(t *testing.T) {
		app := fiber.New()

		expected := &TenantConfig{
			ID:         "tenant-123",
			TenantName: "Test Tenant",
			Status:     "active",
		}

		var extractedConfig *TenantConfig
		app.Get("/test", func(c *fiber.Ctx) error {
			ctx := WithTenantConfig(c.UserContext(), expected)
			c.SetUserContext(ctx)

			extractedConfig = GetTenantConfigFromFiber(c)

			return c.SendString("OK")
		})

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		_, err := app.Test(req)
		require.NoError(t, err)

		assert.Equal(t, expected, extractedConfig)
	})
}

// TestGetTenantPGFromFiber tests the GetTenantPGFromFiber function.
func TestGetTenantPGFromFiber(t *testing.T) {
	t.Run("Should return nil when Fiber context is nil", func(t *testing.T) {
		config := GetTenantPGFromFiber(nil)
		assert.Nil(t, config)
	})

	t.Run("Should return PostgreSQL config from Fiber context", func(t *testing.T) {
		app := fiber.New()

		expected := &PostgreSQLConfig{
			Host:     "pg.local",
			Port:     5432,
			Database: "midaz",
		}

		var extractedConfig *PostgreSQLConfig
		app.Get("/test", func(c *fiber.Ctx) error {
			ctx := WithTenantPG(c.UserContext(), expected)
			c.SetUserContext(ctx)

			extractedConfig = GetTenantPGFromFiber(c)

			return c.SendString("OK")
		})

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		_, err := app.Test(req)
		require.NoError(t, err)

		assert.Equal(t, expected, extractedConfig)
	})
}

// TestGetTenantMongoFromFiber tests the GetTenantMongoFromFiber function.
func TestGetTenantMongoFromFiber(t *testing.T) {
	t.Run("Should return nil when Fiber context is nil", func(t *testing.T) {
		config := GetTenantMongoFromFiber(nil)
		assert.Nil(t, config)
	})

	t.Run("Should return MongoDB config from Fiber context", func(t *testing.T) {
		app := fiber.New()

		expected := &MongoDBConfig{
			URI:      "mongodb://localhost:27017",
			Database: "midaz",
		}

		var extractedConfig *MongoDBConfig
		app.Get("/test", func(c *fiber.Ctx) error {
			ctx := WithTenantMongo(c.UserContext(), expected)
			c.SetUserContext(ctx)

			extractedConfig = GetTenantMongoFromFiber(c)

			return c.SendString("OK")
		})

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		_, err := app.Test(req)
		require.NoError(t, err)

		assert.Equal(t, expected, extractedConfig)
	})
}

// TestGetTenantValkeyFromFiber tests the GetTenantValkeyFromFiber function.
func TestGetTenantValkeyFromFiber(t *testing.T) {
	t.Run("Should return nil when Fiber context is nil", func(t *testing.T) {
		config := GetTenantValkeyFromFiber(nil)
		assert.Nil(t, config)
	})

	t.Run("Should return Valkey config from Fiber context", func(t *testing.T) {
		app := fiber.New()

		expected := &ValkeyConfig{
			Addresses: []string{"localhost:6379"},
			Password:  "secret",
			DB:        0,
		}

		var extractedConfig *ValkeyConfig
		app.Get("/test", func(c *fiber.Ctx) error {
			ctx := WithTenantValkey(c.UserContext(), expected)
			c.SetUserContext(ctx)

			extractedConfig = GetTenantValkeyFromFiber(c)

			return c.SendString("OK")
		})

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		_, err := app.Test(req)
		require.NoError(t, err)

		assert.Equal(t, expected, extractedConfig)
	})
}

// TestGetTenantRabbitMQFromFiber tests the GetTenantRabbitMQFromFiber function.
func TestGetTenantRabbitMQFromFiber(t *testing.T) {
	t.Run("Should return nil when Fiber context is nil", func(t *testing.T) {
		config := GetTenantRabbitMQFromFiber(nil)
		assert.Nil(t, config)
	})

	t.Run("Should return RabbitMQ config from Fiber context", func(t *testing.T) {
		app := fiber.New()

		expected := &RabbitMQConfig{
			URL: "amqp://localhost:5672",
		}

		var extractedConfig *RabbitMQConfig
		app.Get("/test", func(c *fiber.Ctx) error {
			ctx := WithTenantRabbitMQ(c.UserContext(), expected)
			c.SetUserContext(ctx)

			extractedConfig = GetTenantRabbitMQFromFiber(c)

			return c.SendString("OK")
		})

		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		_, err := app.Test(req)
		require.NoError(t, err)

		assert.Equal(t, expected, extractedConfig)
	})
}

// TestContextIntegration tests that setters and getters work together across context boundaries.
func TestContextIntegration(t *testing.T) {
	t.Run("Should propagate tenant context through nested function calls", func(t *testing.T) {
		tenantID := "tenant-integration"
		pgConfig := &PostgreSQLConfig{
			Host:     "pg.integration.local",
			Port:     5432,
			Database: "integration",
		}

		ctx := WithTenantID(context.Background(), tenantID)
		ctx = WithTenantPG(ctx, pgConfig)

		// Simulate nested function calls
		result := processRequest(ctx)
		assert.Equal(t, "tenant-integration:pg.integration.local", result)
	})

	t.Run("Should work with middleware-set context values", func(t *testing.T) {
		// Simulate middleware setting context using internal keys
		ctx := context.Background()
		ctx = context.WithValue(ctx, TenantIDContextKey, "tenant-from-middleware")

		// WithTenantPG should work alongside middleware-set values
		pgConfig := &PostgreSQLConfig{Host: "localhost"}
		ctx = WithTenantPG(ctx, pgConfig)

		assert.Equal(t, "tenant-from-middleware", GetTenantID(ctx))
		assert.Equal(t, pgConfig, GetTenantPostgreSQL(ctx))
	})
}

// Helper function for integration test
func processRequest(ctx context.Context) string {
	tenantID := GetTenantID(ctx)
	pgConfig := GetTenantPostgreSQL(ctx)

	if tenantID == "" || pgConfig == nil {
		return "error"
	}

	return tenantID + ":" + pgConfig.Host
}

// TestIsMultiTenantContext tests the IsMultiTenantContext function.
func TestIsMultiTenantContext(t *testing.T) {
	t.Run("Should return false when context is nil", func(t *testing.T) {
		result := IsMultiTenantContext(nil)
		assert.False(t, result)
	})

	t.Run("Should return false when no tenant ID in context", func(t *testing.T) {
		ctx := context.Background()
		result := IsMultiTenantContext(ctx)
		assert.False(t, result)
	})

	t.Run("Should return false when tenant ID is empty string", func(t *testing.T) {
		ctx := WithTenantID(context.Background(), "")
		result := IsMultiTenantContext(ctx)
		assert.False(t, result)
	})

	t.Run("Should return true when tenant ID is set", func(t *testing.T) {
		ctx := WithTenantID(context.Background(), "tenant-123")
		result := IsMultiTenantContext(ctx)
		assert.True(t, result)
	})

	t.Run("Should return true when tenant ID is set via TenantIDContextKey", func(t *testing.T) {
		ctx := context.WithValue(context.Background(), TenantIDContextKey, "tenant-456")
		result := IsMultiTenantContext(ctx)
		assert.True(t, result)
	})
}

// mockDB is a mock implementation of dbresolver.DB for testing purposes.
type mockDB struct {
	dbresolver.DB
}

// TestGetDBForTenant tests the GetDBForTenant function.
func TestGetDBForTenant(t *testing.T) {
	t.Run("Should return error when no tenant connection in multi-tenant mode", func(t *testing.T) {
		// Multi-tenant mode (tenant ID is set but no connection)
		ctx := WithTenantID(context.Background(), "tenant-123")

		db, err := GetDBForTenant(ctx, nil)
		assert.Nil(t, db)
		assert.ErrorIs(t, err, ErrTenantConnectionRequired)
	})

	t.Run("Should return tenant connection in multi-tenant mode", func(t *testing.T) {
		// Set up multi-tenant context with tenant connection
		ctx := WithTenantID(context.Background(), "tenant-123")
		expectedDB := &mockDB{}
		ctx = WithTenantPGConnection(ctx, expectedDB)

		db, err := GetDBForTenant(ctx, nil)
		assert.NoError(t, err)
		assert.Equal(t, expectedDB, db)
	})

	t.Run("Should return error when no default connection in single-tenant mode", func(t *testing.T) {
		// Single-tenant mode (no tenant ID)
		ctx := context.Background()

		db, err := GetDBForTenant(ctx, nil)
		assert.Nil(t, db)
		assert.ErrorIs(t, err, ErrNoConnectionAvailable)
	})

	t.Run("Should return error when context is nil and no default connection", func(t *testing.T) {
		db, err := GetDBForTenant(nil, nil)
		assert.Nil(t, db)
		assert.ErrorIs(t, err, ErrNoConnectionAvailable)
	})
}

// TestGetMongoDatabaseForTenant tests the GetMongoDatabaseForTenant function.
func TestGetMongoDatabaseForTenant(t *testing.T) {
	t.Run("Should return error when no tenant database in multi-tenant mode", func(t *testing.T) {
		// Multi-tenant mode (tenant ID is set but no database)
		ctx := WithTenantID(context.Background(), "tenant-123")

		db, err := GetMongoDatabaseForTenant(ctx, nil)
		assert.Nil(t, db)
		assert.ErrorIs(t, err, ErrTenantConnectionRequired)
	})

	t.Run("Should return tenant database in multi-tenant mode", func(t *testing.T) {
		// Set up multi-tenant context with tenant database
		ctx := WithTenantID(context.Background(), "tenant-123")
		expectedDB := &TenantDatabase{
			tenantID:         "tenant-123",
			collectionPrefix: "tenant_123_",
		}
		ctx = WithTenantMongoDatabase(ctx, expectedDB)

		db, err := GetMongoDatabaseForTenant(ctx, nil)
		assert.NoError(t, err)
		assert.Equal(t, expectedDB, db)
	})

	t.Run("Should return error when no default connection in single-tenant mode", func(t *testing.T) {
		// Single-tenant mode (no tenant ID)
		ctx := context.Background()

		db, err := GetMongoDatabaseForTenant(ctx, nil)
		assert.Nil(t, db)
		assert.ErrorIs(t, err, ErrNoConnectionAvailable)
	})

	t.Run("Should return error when context is nil and no default connection", func(t *testing.T) {
		db, err := GetMongoDatabaseForTenant(nil, nil)
		assert.Nil(t, db)
		assert.ErrorIs(t, err, ErrNoConnectionAvailable)
	})
}
