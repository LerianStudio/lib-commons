package mongo

import (
	"context"
	"strings"

	"github.com/LerianStudio/lib-commons/v2/commons/log"
	"go.mongodb.org/mongo-driver/bson"
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

// EnsureIndexes guarantees an index exists for a given collection.
// Idempotent. Returns error if connection or index creation fails.
func (mc *MongoConnection) EnsureIndexes(ctx context.Context, collection string, index mongo.IndexModel) error {
	mc.Logger.Debugf("Ensuring indexes for collection: collection=%s", collection)

	client, err := mc.GetDB(ctx)
	if err != nil {
		mc.Logger.Warnf("Failed to get database connection for index creation: %v", err)
		return err
	}

	db := client.Database(mc.Database)

	coll := db.Collection(collection)

	fields := indexKeysString(index.Keys)

	mc.Logger.Debugf("Ensuring index: collection=%s, fields=%s", collection, fields)

	_, err = coll.Indexes().CreateOne(ctx, index)
	if err != nil {
		mc.Logger.Warnf("Failed to ensure index: collection=%s, fields=%s, err=%v", collection, fields, err)
		return err
	}

	mc.Logger.Infof("Index successfully ensured: collection=%s, fields=%s \n", collection, fields)

	return nil
}

// indexKeysString returns a string representation of the index keys.
// It's used to log the index keys in a human-readable format.
func indexKeysString(keys any) string {
	switch k := keys.(type) {
	case bson.D:
		parts := make([]string, 0, len(k))
		for _, e := range k {
			parts = append(parts, e.Key)
		}

		return strings.Join(parts, ",")
	case bson.M:
		parts := make([]string, 0, len(k))
		for key := range k {
			parts = append(parts, key)
		}

		return strings.Join(parts, ",")
	default:
		return "<unknown>"
	}
}
