package mongo

import (
	"context"
	"github.com/LerianStudio/lib-commons/commons/log"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.uber.org/zap"
)

// MongoConnection is a hub which deal with mongodb connections.
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
		mc.Logger.Fatal("failed to open connect to mongodb", zap.Error(err))
		return err
	}

	if err := noSQLDB.Ping(ctx, nil); err != nil {
		mc.Logger.Infof("MongoDBConnection.Ping %v",
			zap.Error(err))

		return err
	}

	mc.Logger.Info("Connected to mongodb ✅ \n")

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
