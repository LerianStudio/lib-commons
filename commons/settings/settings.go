package settings

import (
	"context"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"github.com/LerianStudio/lib-commons/commons"
	"github.com/LerianStudio/lib-commons/commons/mongo"
	"github.com/LerianStudio/lib-commons/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/commons/redis"
	redisV9 "github.com/redis/go-redis/v9"
	"github.com/vmihailenco/msgpack"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	mongoDriver "go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"reflect"
	"strings"
	"time"
)

// Database name
const Database = "settings"

// Settings is the database model
type Settings struct {
	ID              primitive.ObjectID `bson:"_id,omitempty"`
	organizationID  string             `bson:"organization_id"`
	ledgerID        string             `bson:"ledger_id"`
	ApplicationName string             `bson:"application_name"`
	Settings        JSON               `bson:"settings"`
	CreatedAt       time.Time          `bson:"created_at"`
	UpdatedAt       time.Time          `bson:"updated_at"`
}

// JSON document to save on mongodb
type JSON map[string]any

// Value return marshall value data
func (s JSON) Value() (driver.Value, error) {
	return json.Marshal(s)
}

// Scan unmarshall value data
func (s *JSON) Scan(value any) error {
	b, ok := value.([]byte)
	if !ok {
		return errors.New("type assertion to []byte failed")
	}

	return json.Unmarshal(b, &s)
}

// SettingsRepository interface
type SettingsRepository interface {
	CreateSettings(ctx context.Context, settings *Settings) error
	FindSettings(ctx context.Context, organizationID, ledgerID, applicationName string) (*Settings, error)
	DeleteSettings(ctx context.Context, organizationID, ledgerID, applicationName string)
}

// SettingsConnection struct
type SettingsConnection struct {
	redis      *redis.RedisConnection
	mongo      *mongo.MongoConnection
	collection string
}

func NewSettingsConsumer(ctx context.Context, rc *redis.RedisConnection, mc mongo.MongoConnection) *SettingsConnection {
	r := &SettingsConnection{
		redis:      rc,
		mongo:      &mc,
		collection: strings.ToLower(reflect.TypeOf(Settings{}).Name()),
	}
	if _, err := r.redis.GetClient(ctx); err != nil {
		panic("Failed to connect on redis...")
	}

	if _, err := r.mongo.GetDB(ctx); err != nil {
		panic("Failed to connect mongodb...")
	}

	return r
}

func (sr *SettingsConnection) CreateSettings(ctx context.Context, settings *Settings) error {
	tracer := commons.NewTracerFromContext(ctx)
	logger := commons.NewLoggerFromContext(ctx)

	ctx, span := tracer.Start(ctx, "settings.create_settings")
	defer span.End()

	if err := sr.createMongo(ctx, settings); err != nil {
		return err
	}

	settingsInternalKey := commons.SettingsInternalKey(settings.organizationID, settings.ledgerID, settings.ApplicationName)

	logger.Infof("Creating settings on redis: %s", settingsInternalKey)

	data, err := msgpack.Marshal(settings)
	if err != nil {
		opentelemetry.HandleSpanError(&span, "failed to marshal msgpack", err)

		logger.Errorf("Failed to marshal msgpack: %s", err)

		return err
	}

	isLocked, err := sr.setNXRedis(ctx, settingsInternalKey, data, 60*time.Minute)
	if err != nil {
		opentelemetry.HandleSpanError(&span, "failed to setnx on redis", err)

		logger.Errorf("Failed to setnx on redis: %s", err)

		return err
	}

	if isLocked {
		logger.Infof("Settings already exists on redis: %s", settingsInternalKey)
	} else {
		logger.Infof("Settings created on redis: %s", settingsInternalKey)
	}

	return nil
}

func (sr *SettingsConnection) FindSettings(ctx context.Context, organizationID, ledgerID, applicationName string) (*Settings, error) {
	tracer := commons.NewTracerFromContext(ctx)
	logger := commons.NewLoggerFromContext(ctx)

	ctx, span := tracer.Start(ctx, "settings.find_settings")
	defer span.End()

	settingsInternalKey := commons.SettingsInternalKey(organizationID, ledgerID, applicationName)

	logger.Infof("Finding settings on redis: %s", settingsInternalKey)

	data, err := sr.getRedis(ctx, settingsInternalKey)
	if err != nil {
		opentelemetry.HandleSpanError(&span, "failed to get on redis", err)

		logger.Errorf("Failed to get on redis: %s", err)

		return nil, err
	}

	if commons.IsNilOrEmpty(&data) {
		logger.Infof("Settings not found on redis: %s", settingsInternalKey)

		settings, err := sr.findMongo(ctx, organizationID, ledgerID, applicationName)
		if err != nil {
			opentelemetry.HandleSpanError(&span, "failed to find on mongo", err)

			logger.Errorf("Failed to find on mongo: %s", err)

			return nil, err

		}

		return settings, nil
	} else {
		var settings Settings
		if err := msgpack.Unmarshal([]byte(data), &settings); err != nil {
			opentelemetry.HandleSpanError(&span, "failed to unmarshal msgpack", err)

			logger.Errorf("Error to deserializar mensagem: %v", err)

			return nil, err
		}

		return &settings, nil
	}
}

func (sr *SettingsConnection) DeleteSettings(ctx context.Context, organizationID, ledgerID, applicationName string) {
	tracer := commons.NewTracerFromContext(ctx)
	logger := commons.NewLoggerFromContext(ctx)

	ctx, span := tracer.Start(ctx, "settings.delete_settings")
	defer span.End()

	if err := sr.deleteMongo(ctx, organizationID, ledgerID, applicationName); err != nil {
		opentelemetry.HandleSpanError(&span, "failed to delete on mongo", err)

		logger.Errorf("Failed to delete on mongo: %s", err)
	}

	settingsInternalKey := commons.SettingsInternalKey(organizationID, ledgerID, applicationName)

	logger.Infof("Deleting settings on redis: %s", settingsInternalKey)

	if err := sr.deleteRedis(ctx, settingsInternalKey); err != nil {
		opentelemetry.HandleSpanError(&span, "failed to delete on redis", err)

		logger.Errorf("Failed to delete on redis: %s", err)
	}
}

func (sr *SettingsConnection) createMongo(ctx context.Context, settings *Settings) error {
	tracer := commons.NewTracerFromContext(ctx)
	logger := commons.NewLoggerFromContext(ctx)

	ctx, span := tracer.Start(ctx, "settings.mongodb.create")
	defer span.End()

	db, err := sr.mongo.GetDB(ctx)
	if err != nil {
		opentelemetry.HandleSpanError(&span, "Failed to get database connection", err)

		logger.Errorf("Failed to get database connection: %s", err)

		return err
	}

	coll := db.Database(Database).Collection(sr.collection)

	ctxInsert, spanInsert := tracer.Start(ctx, "settings.mongodb.create.insert")
	spanInsert.End()

	insertResult, err := coll.InsertOne(ctxInsert, settings)
	if err != nil {
		opentelemetry.HandleSpanError(&spanInsert, "Failed to insert settings", err)

		logger.Errorf("Failed to insert settings: %s", err)

		return err
	}

	logger.Infoln("Inserted a document: ", insertResult.InsertedID)

	return nil
}

func (sr *SettingsConnection) findMongo(ctx context.Context, organizationID, ledgerID, applicationName string) (*Settings, error) {
	tracer := commons.NewTracerFromContext(ctx)
	logger := commons.NewLoggerFromContext(ctx)

	ctx, span := tracer.Start(ctx, "settings.mongodb.find")
	defer span.End()

	db, err := sr.mongo.GetDB(ctx)
	if err != nil {
		opentelemetry.HandleSpanError(&span, "Failed to get database", err)

		logger.Errorf("Failed to get database: %s", err)

		return nil, err
	}

	coll := db.Database(Database).Collection(sr.collection)

	var record Settings

	ctx, spanFindOne := tracer.Start(ctx, "settings.mongodb.find_one")
	spanFindOne.End()

	if err = coll.FindOne(ctx, bson.M{"organization_id": organizationID, "ledger_id": ledgerID, "application_name": applicationName}).Decode(&record); err != nil {
		opentelemetry.HandleSpanError(&spanFindOne, "Failed to find settings", err)

		if errors.Is(err, mongoDriver.ErrNoDocuments) {
			return nil, nil
		}

		logger.Errorf("Failed to find settings: %s", err)

		return nil, err
	}

	return &record, nil
}

func (sr *SettingsConnection) deleteMongo(ctx context.Context, organizationID, ledgerID, applicationName string) error {
	tracer := commons.NewTracerFromContext(ctx)
	logger := commons.NewLoggerFromContext(ctx)

	ctx, span := tracer.Start(ctx, "settings.mongodb.delete")
	defer span.End()

	db, err := sr.mongo.GetDB(ctx)
	if err != nil {
		opentelemetry.HandleSpanError(&span, "Failed to get database", err)

		logger.Errorf("Failed to get database: %s", err)

		return err
	}

	opts := options.Delete()

	coll := db.Database(Database).Collection(sr.collection)

	ctx, spanDelete := tracer.Start(ctx, "settings.mongodb.delete_one")

	deleted, err := coll.DeleteOne(ctx, bson.M{"organization_id": organizationID, "ledger_id": ledgerID, "application_name": applicationName}, opts)
	if err != nil {
		opentelemetry.HandleSpanError(&spanDelete, "Failed to delete settings", err)

		logger.Errorf("Failed to delete settings: %s", err)

		return err
	}

	spanDelete.End()

	if deleted.DeletedCount > 0 {
		logger.Infof("total deleted a document with success: %v", deleted.DeletedCount)
	}

	return nil
}

func (sr *SettingsConnection) setNXRedis(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error) {
	logger := commons.NewLoggerFromContext(ctx)
	tracer := commons.NewTracerFromContext(ctx)

	ctx, span := tracer.Start(ctx, "settings.redis.set_nx")
	defer span.End()

	rds, err := sr.redis.GetClient(ctx)
	if err != nil {
		opentelemetry.HandleSpanError(&span, "Failed to get redis", err)

		return false, err
	}

	logger.Infof("value of ttl: %v", ttl)

	isLocked, err := rds.SetNX(ctx, key, value, ttl).Result()
	if err != nil {
		opentelemetry.HandleSpanError(&span, "Failed to set nx on redis", err)

		return false, err
	}

	return isLocked, nil
}

func (sr *SettingsConnection) getRedis(ctx context.Context, key string) (string, error) {
	logger := commons.NewLoggerFromContext(ctx)
	tracer := commons.NewTracerFromContext(ctx)

	ctx, span := tracer.Start(ctx, "settings.redis.get")
	defer span.End()

	rds, err := sr.redis.GetClient(ctx)
	if err != nil {
		opentelemetry.HandleSpanError(&span, "Failed to connect on redis", err)

		logger.Errorf("Failed to connect on redis: %v", err)

		return "", err
	}

	val, err := rds.Get(ctx, key).Result()
	if err != nil && !errors.Is(err, redisV9.Nil) {
		opentelemetry.HandleSpanError(&span, "Failed to get on redis", err)

		logger.Errorf("Failed to get on redis: %v", err)

		return "", err
	}

	return val, nil
}

func (sr *SettingsConnection) deleteRedis(ctx context.Context, key string) error {
	logger := commons.NewLoggerFromContext(ctx)
	tracer := commons.NewTracerFromContext(ctx)

	ctx, span := tracer.Start(ctx, "settings.redis.del")
	defer span.End()

	rds, err := sr.redis.GetClient(ctx)
	if err != nil {
		opentelemetry.HandleSpanError(&span, "Failed to del redis", err)

		logger.Errorf("Failed to del redis: %v", err)

		return err
	}

	_, err = rds.Del(ctx, key).Result()
	if err != nil {
		opentelemetry.HandleSpanError(&span, "Failed to del on redis", err)

		logger.Errorf("Failed to del on redis: %v", err)

		return err
	}

	return nil
}
