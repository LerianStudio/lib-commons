// CRUD helpers for the MongoDB backend.
//
// Split from mongodb.go to keep file sizes manageable. Holds the low-level
// read/write primitives (findOne, upsert) that higher-level methods compose.
// Method receivers are unchanged, so callers see no behavioral difference.

package mongodb

import (
	"context"
	"errors"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/store"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// findOne runs a FindOne keyed on the compound (namespace, key, tenant_id)
// tuple. Returns (entryDoc{}, false, nil) when no document matches.
func (s *Store) findOne(ctx context.Context, namespace, key, tenantID string) (entryDoc, bool, error) {
	filter := bson.D{
		{Key: "namespace", Value: namespace},
		{Key: "key", Value: key},
		{Key: "tenant_id", Value: tenantID},
	}

	var doc entryDoc

	err := s.coll.FindOne(ctx, filter).Decode(&doc)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return entryDoc{}, false, nil
		}

		return entryDoc{}, false, err //nolint:wrapcheck // caller wraps with method context
	}

	return doc, true, nil
}

// upsert writes an entry under the given tenantID.
//
// Phase selection via s.cfg.TenantSchemaEnabled:
//
//   - Phase 1 (TenantSchemaEnabled=false): the upsert filter is the natural
//     tuple {namespace, key, tenant_id} rather than {_id: compoundID}. This
//     matches BOTH legacy ObjectId-_id rows (inserted by pre-tenant v5.0.x
//     binaries whose _id is a bare ObjectId) AND already-migrated compound-id
//     rows. Using {_id: compoundID} in phase 1 would miss the legacy shape
//     entirely and issue an insert that collides with the legacy unique
//     index on (namespace, key) — the exact E11000 condition a rolling
//     deploy is supposed to avoid (C1).
//   - Phase 2 (TenantSchemaEnabled=true): the migration has run, so every
//     row is compound-id and the filter can key directly on _id for a
//     natural primary-key lookup.
//
// In both phases the $set update rewrites the full field set, and an insert
// pins _id to the compound tuple so subsequent upserts observe the phase-2
// shape.
func (s *Store) upsert(ctx context.Context, e store.Entry, tenantID string) error {
	now := time.Now().UTC()
	if !e.UpdatedAt.IsZero() {
		now = e.UpdatedAt
	}

	id := compoundID{
		Namespace: e.Namespace,
		Key:       e.Key,
		TenantID:  tenantID,
	}

	var filter bson.D
	if s.cfg.TenantSchemaEnabled {
		filter = bson.D{{Key: "_id", Value: id}}
	} else {
		// Phase-1 rolling-deploy filter: match by the natural tuple so legacy
		// ObjectId-_id rows and compound-id rows both resolve.
		filter = bson.D{
			{Key: "namespace", Value: e.Namespace},
			{Key: "key", Value: e.Key},
			{Key: "tenant_id", Value: tenantID},
		}
	}

	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "namespace", Value: e.Namespace},
			{Key: "key", Value: e.Key},
			{Key: "tenant_id", Value: tenantID},
			{Key: "value", Value: string(e.Value)},
			{Key: "updated_at", Value: now},
			{Key: "updated_by", Value: e.UpdatedBy},
		}},
		// On insert, pin _id to the compound tuple. For phase-1 hits against
		// a legacy ObjectId row, this branch is skipped — MongoDB never
		// rewrites _id on update — so the legacy row retains its ObjectId
		// and is cleaned up by the subsequent phase-2 migration.
		{Key: "$setOnInsert", Value: bson.D{
			{Key: "_id", Value: id},
		}},
	}

	opts := options.UpdateOne().SetUpsert(true)

	if _, err := s.coll.UpdateOne(ctx, filter, update, opts); err != nil {
		return err //nolint:wrapcheck // caller wraps with method context
	}

	return nil
}
