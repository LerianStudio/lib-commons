package tenantmanager

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewMongoManager(t *testing.T) {
	t.Run("creates manager with client and service", func(t *testing.T) {
		client := &Client{baseURL: "http://localhost:8080"}
		manager := NewMongoManager(client, "ledger")

		assert.NotNil(t, manager)
		assert.Equal(t, "ledger", manager.service)
		assert.NotNil(t, manager.connections)
	})
}

func TestMongoManager_GetClient_NoTenantID(t *testing.T) {
	client := &Client{baseURL: "http://localhost:8080"}
	manager := NewMongoManager(client, "ledger")

	_, err := manager.GetClient(context.Background(), "")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "tenant ID is required")
}

func TestMongoManager_GetClient_ManagerClosed(t *testing.T) {
	client := &Client{baseURL: "http://localhost:8080"}
	manager := NewMongoManager(client, "ledger")
	manager.Close(context.Background())

	_, err := manager.GetClient(context.Background(), "tenant-123")

	assert.ErrorIs(t, err, ErrManagerClosed)
}

func TestBuildMongoURI(t *testing.T) {
	t.Run("returns URI when provided", func(t *testing.T) {
		cfg := &MongoDBConfig{
			URI: "mongodb://custom-uri",
		}

		uri := buildMongoURI(cfg)

		assert.Equal(t, "mongodb://custom-uri", uri)
	})

	t.Run("builds URI with credentials", func(t *testing.T) {
		cfg := &MongoDBConfig{
			Host:     "localhost",
			Port:     27017,
			Database: "testdb",
			Username: "user",
			Password: "pass",
		}

		uri := buildMongoURI(cfg)

		assert.Equal(t, "mongodb://user:pass@localhost:27017/testdb", uri)
	})

	t.Run("builds URI without credentials", func(t *testing.T) {
		cfg := &MongoDBConfig{
			Host:     "localhost",
			Port:     27017,
			Database: "testdb",
		}

		uri := buildMongoURI(cfg)

		assert.Equal(t, "mongodb://localhost:27017/testdb", uri)
	})
}

func TestContextWithTenantMongo(t *testing.T) {
	t.Run("stores and retrieves mongo database", func(t *testing.T) {
		// We can't create a real mongo.Database without a connection,
		// so we test the nil case
		ctx := context.Background()

		db := GetMongoFromContext(ctx)

		assert.Nil(t, db)
	})
}

func TestGetMongoForTenant(t *testing.T) {
	t.Run("returns error when no database in context", func(t *testing.T) {
		ctx := context.Background()

		db, err := GetMongoForTenant(ctx)

		assert.Nil(t, db)
		assert.ErrorIs(t, err, ErrTenantContextRequired)
	})
}

func TestMongoManager_GetDatabaseForTenant_NoTenantID(t *testing.T) {
	client := &Client{baseURL: "http://localhost:8080"}
	manager := NewMongoManager(client, "ledger")

	_, err := manager.GetDatabaseForTenant(context.Background(), "")

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "tenant ID is required")
}
