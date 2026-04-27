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
//   - Phase 1 (TenantSchemaEnabled=false): the upsert filter keys by
//     (namespace, key) ONLY. This matches BOTH legacy ObjectId-_id rows
//     (inserted by pre-tenant v5.0.x binaries whose _id is a bare ObjectId
//     and which have no tenant_id field at all) AND already-migrated
//     compound-id rows. Including tenant_id in the phase-1 filter would
//     miss legacy rows — they lack the field — and trigger an insert that
//     collides with the legacy unique index on (namespace, key), which is
//     the exact E11000 condition a rolling deploy is supposed to avoid
//     (C1). In phase 1 the SetTenantValue / DeleteTenantValue paths return
//     ErrTenantSchemaNotEnabled, so every upsert through this function is
//     a global (tenantID="_global") write, and the legacy unique index on
//     (namespace, key) already enforces "one row per (namespace, key)" —
//     making (namespace, key) a sufficient filter in phase 1.
//   - Phase 2 (TenantSchemaEnabled=true): the migration has run, so every
//     row is compound-id and the filter can key directly on _id for a
//     natural primary-key lookup.
//
// In both phases the $set update rewrites the full field set, and an insert
// pins _id to the compound tuple so subsequent upserts observe the phase-2
// shape. The legacy row retains its ObjectId _id on a phase-1 update
// because MongoDB never rewrites _id via $set; the subsequent phase-2
// migration cleans those up.
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
		// Phase-1 filter: match by (namespace, key) alone. Legacy rows
		// lack tenant_id entirely; including it in the filter turns the
		// upsert into an insert that E11000s on the legacy unique index.
		// Safe because phase-1 rejects tenant writes — only globals land
		// here — and the legacy unique index guarantees at most one row
		// per (namespace, key).
		filter = bson.D{
			{Key: "namespace", Value: e.Namespace},
			{Key: "key", Value: e.Key},
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
