// Package mongo provides MongoDB connection and operation utilities.
// It includes connection management, query helpers, and database operations.
package mongo

import (
	"context"
	"fmt"

	"github.com/LerianStudio/lib-commons/commons/log"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.uber.org/zap"
)

// MongoConnection is a hub which deal with mongodb connections.
// The type name intentionally matches the package name for clarity in external usage.
type MongoConnection struct {
	ConnectionStringSource string
	DB                     *mongo.Client
	Connected              bool
	Database               string
	Logger                 log.Logger
	MaxPoolSize            uint64
}

// Connect keeps a singleton connection with mongodb.
func (mc *MongoConnection) Connect(ctx context.Context) error {
	mc.Logger.Info("Connecting to mongodb...")

	clientOptions := options.
		Client().
		ApplyURI(mc.ConnectionStringSource).
		SetMaxPoolSize(mc.MaxPoolSize)

	noSQLDB, err := mongo.Connect(ctx, clientOptions)
	if err != nil {
		mc.Logger.Error("failed to open connect to mongodb", zap.Error(err))
		return fmt.Errorf("failed to connect to mongodb: %w", err)
	}

	if err := noSQLDB.Ping(ctx, nil); err != nil {
		// Close the connection if ping fails
		if disconnectErr := noSQLDB.Disconnect(ctx); disconnectErr != nil {
			mc.Logger.Error("failed to disconnect after ping failure", zap.Error(disconnectErr))
		}

		mc.Logger.Error("mongodb health check failed", zap.Error(err))

		return fmt.Errorf("mongodb health check failed: %w", err)
	}

	mc.Logger.Info("Connected to mongodb ✅")

	mc.Connected = true
	mc.DB = noSQLDB

	return nil
}

// GetDB returns a pointer to the mongodb connection, initializing it if necessary.
func (mc *MongoConnection) GetDB(ctx context.Context) (*mongo.Client, error) {
	if mc.DB == nil {
		err := mc.Connect(ctx)
		if err != nil {
			mc.Logger.Infof("ERRCONECT %s", err)
			return nil, err
		}
	}

	return mc.DB, nil
}
