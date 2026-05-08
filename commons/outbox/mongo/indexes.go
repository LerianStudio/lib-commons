package mongo

import (
	"go.mongodb.org/mongo-driver/bson"
	mongodriver "go.mongodb.org/mongo-driver/mongo"
	mongooptions "go.mongodb.org/mongo-driver/mongo/options"
)

func buildIndexes(tenantField string) []mongodriver.IndexModel {
	keysWithTenant := func(keys bson.D) bson.D {
		if tenantField == "" {
			return keys
		}

		prefixed := make(bson.D, 0, len(keys)+1)
		prefixed = append(prefixed, bson.E{Key: tenantField, Value: 1})
		prefixed = append(prefixed, keys...)

		return prefixed
	}

	uniqueIDKeys := bson.D{{Key: bsonFieldID, Value: 1}}
	if tenantField != "" {
		uniqueIDKeys = keysWithTenant(bson.D{{Key: bsonFieldID, Value: 1}})
	}

	indexes := []mongodriver.IndexModel{
		{
			Keys:    uniqueIDKeys,
			Options: mongooptions.Index().SetUnique(true).SetName("outbox_id_scope_unique"),
		},
		{
			Keys:    keysWithTenant(bson.D{{Key: bsonFieldStatus, Value: 1}, {Key: bsonFieldCreatedAt, Value: 1}, {Key: bsonFieldID, Value: 1}}),
			Options: mongooptions.Index().SetName("outbox_pending_claim"),
		},
		{
			Keys:    keysWithTenant(bson.D{{Key: bsonFieldEventType, Value: 1}, {Key: bsonFieldStatus, Value: 1}, {Key: bsonFieldCreatedAt, Value: 1}, {Key: bsonFieldID, Value: 1}}),
			Options: mongooptions.Index().SetName("outbox_pending_by_type_claim"),
		},
		{
			Keys:    keysWithTenant(bson.D{{Key: bsonFieldStatus, Value: 1}, {Key: bsonFieldUpdatedAt, Value: 1}, {Key: bsonFieldID, Value: 1}}),
			Options: mongooptions.Index().SetName("outbox_state_updated_scan"),
		},
		{
			Keys:    keysWithTenant(bson.D{{Key: bsonFieldClaimToken, Value: 1}, {Key: bsonFieldStatus, Value: 1}, {Key: bsonFieldID, Value: 1}}),
			Options: mongooptions.Index().SetName("outbox_claim_token_lookup").SetSparse(true),
		},
	}

	if tenantField != "" {
		indexes = append(indexes, mongodriver.IndexModel{
			Keys:    bson.D{{Key: bsonFieldStatus, Value: 1}, {Key: tenantField, Value: 1}},
			Options: mongooptions.Index().SetName("outbox_status_tenant"),
		})
	}

	return indexes
}
