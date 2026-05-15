// Idempotent schema evolution for the MongoDB backend.
//
// ensureSchema is called once per Store construction (New) when phase-2 is
// enabled and performs the following migrations in order:
//
//  1. Lease acquisition: a sentinel document coordinates exclusion between
//     concurrent New() calls from different processes. Without it, two nodes
//     racing through rewriteObjectIDDocuments could each insert a migrated
//     copy and then each delete the legacy row — either is harmless on its
//     own, but the duplicate-insert path can trip E11000 on the compound
//     unique index created in step 5 (H3).
//  2. Pre-flight ambiguity check: documents where (namespace, key) already
//     has BOTH a tenant_id=missing and a tenant_id="_global" row would
//     collapse into a duplicate after backfill; abort loudly instead.
//  3. Backfill: documents missing the tenant_id field gain tenant_id="_global".
//  4. _id rewrite: documents whose _id is still an ObjectId (created by the
//     pre-tenant-scoping schema) are re-inserted with a compound _id
//     {namespace, key, tenant_id} and the original row is deleted. Uses
//     batched InsertMany / DeleteMany in chunks to keep the operation
//     bounded when the pre-migration row count is large (H4). The rewrite
//     lives in mongodb_migration_legacy.go.
//  5. Old index drop: the legacy "namespace_1_key_1" unique index is dropped.
//  6. New index create: a compound unique index on (namespace, key, tenant_id).
//
// Every step is crash-safe: on re-run of a half-applied migration, steps 2,
// 3, 5, and 6 are naturally idempotent, step 1 re-acquires a fresh lease,
// and step 4 becomes a no-op once no ObjectId _id documents remain.

package mongodb

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/store"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// indexNotFoundCode is the MongoDB server error code for IndexNotFound
// (https://www.mongodb.com/docs/manual/reference/error-codes/). Matches the
// code returned by dropIndexes when the named index does not exist.
const indexNotFoundCode = 27

// namespaceNotFoundCode is the MongoDB server error code for
// NamespaceNotFound, returned by dropIndexes (and similar operations) when
// the target collection itself does not exist. On a fresh installation
// ensureSchema runs before any document is inserted, so the collection is
// lazily created later; dropping a legacy index against a non-existent
// collection is semantically a no-op.
const namespaceNotFoundCode = 26

// legacyNamespaceKeyIndex is the name the driver assigns to the pre-tenant
// compound unique index on (namespace, key). mongo-driver/v2 names compound
// indexes by concatenating "<field>_<direction>" per key. The old index in
// mongodb.go pre-Task-4 had Keys: bson.D{{"namespace", 1}, {"key", 1}}, so
// the driver-assigned name is "namespace_1_key_1".
const legacyNamespaceKeyIndex = "namespace_1_key_1"

// ensureSchema runs the phase-2 migration steps. All steps are idempotent;
// re-running against a migrated collection is a no-op modulo the round-trip
// cost. The pre-flight duplicate-detection runs BEFORE backfill so an
// ambiguous pre-migration state aborts loudly rather than silently losing
// data at the $set stage (H6).
//
// Callers MUST only invoke this when Config.TenantSchemaEnabled is true;
// New() routes to ensureLegacySchema otherwise.
//
// logger is threaded in so the lease-skip WARN can be surfaced; it may be
// nil for tests that don't care about structured output.
func ensureSchema(ctx context.Context, coll *mongo.Collection, logger log.Logger) error {
	// H3: coordinate exclusion across processes. A lease held by a peer in
	// the middle of its migration causes this caller to skip — the peer
	// will complete the work. Staleness (lease heartbeat older than 5m) is
	// handled inside acquireMigrationLease.
	leaseCtx, releaseLease, acquired, err := acquireMigrationLease(ctx, coll, logger)
	if err != nil {
		return fmt.Errorf("acquire migration lease: %w", err)
	}

	if !acquired {
		// Peer is already migrating. Nothing to do; fall through to index
		// creation only, which is idempotent under the same shape.
		if logger != nil {
			logger.Log(ctx, log.LevelInfo,
				"systemplane/mongodb: migration lease held by peer, skipping _id rewrite",
			)
		}

		return createCompoundIndex(ctx, coll)
	}

	defer releaseLease()

	if err := verifyNoAmbiguousTenantDocs(leaseCtx, coll); err != nil {
		return err
	}

	if err := backfillTenantID(leaseCtx, coll); err != nil {
		return fmt.Errorf("backfill tenant_id: %w", err)
	}

	if err := rewriteObjectIDDocuments(leaseCtx, coll); err != nil {
		return fmt.Errorf("rewrite _id: %w", err)
	}

	if err := dropLegacyIndex(leaseCtx, coll); err != nil {
		return fmt.Errorf("drop legacy index: %w", err)
	}

	if err := createCompoundIndex(leaseCtx, coll); err != nil {
		return fmt.Errorf("create compound unique index: %w", err)
	}

	return nil
}

// ensureLegacySchema is the phase-1 schema bootstrap. It creates the legacy
// unique index on (namespace, key) idempotently and leaves documents on
// their ObjectId _id shape. No backfill, no _id rewrite, no compound index —
// those belong to phase 2. Pre-tenant binaries that hit the same collection
// remain schema-compatible.
func ensureLegacySchema(ctx context.Context, coll *mongo.Collection) error {
	model := mongo.IndexModel{
		Keys: bson.D{
			{Key: bsonFieldNamespace, Value: 1},
			{Key: bsonFieldKey, Value: 1},
		},
		Options: options.Index().SetUnique(true).SetName(legacyNamespaceKeyIndex),
	}

	if _, err := coll.Indexes().CreateOne(ctx, model); err != nil {
		return fmt.Errorf("create legacy unique index: %w", err)
	}

	return nil
}

// verifyNoAmbiguousTenantDocs is the phase-2 pre-flight guard (H6 sibling
// of the Postgres H8). If a (namespace, key) pair has BOTH a document with
// tenant_id missing (or an empty string — both treated identically, M-S3-6)
// AND another document with tenant_id="_global", the subsequent backfill
// ($set tenant_id="_global" on the missing-field doc) would produce two
// "_global" documents for the same pair and — once the compound unique
// index is created — cause one of them to vanish on the next upsert. Fail
// loudly so operators consolidate the rows manually.
//
// The aggregation pipeline returns zero when the collection is empty or
// already consistent, so the check is a no-op on fresh deployments.
func verifyNoAmbiguousTenantDocs(ctx context.Context, coll *mongo.Collection) error {
	pipeline := mongo.Pipeline{
		// Project a "has_tenant_id" flag so we can count each shape per group.
		// The $cond folds two shapes — missing field ($ifNull) and empty
		// string — into the "__missing__" bucket so both collide with
		// "_global" the same way.
		{{Key: "$group", Value: bson.D{
			{Key: bsonFieldID, Value: bson.D{
				{Key: bsonFieldNamespace, Value: "$namespace"},
				{Key: bsonFieldKey, Value: "$key"},
			}},
			{Key: "tenantIDs", Value: bson.D{
				{Key: "$addToSet", Value: bson.D{
					{Key: "$cond", Value: bson.D{
						{Key: "if", Value: bson.D{
							{Key: "$or", Value: bson.A{
								bson.D{{Key: "$eq", Value: bson.A{bson.D{{Key: "$ifNull", Value: bson.A{"$tenant_id", ""}}}, ""}}},
							}},
						}},
						{Key: "then", Value: "__missing__"},
						{Key: "else", Value: "$tenant_id"},
					}},
				}},
			}},
		}}},
		// A colliding group is one that contains BOTH the missing marker AND
		// the _global sentinel.
		{{Key: "$match", Value: bson.D{
			{Key: "tenantIDs", Value: bson.D{{Key: "$all", Value: bson.A{"__missing__", store.SentinelGlobal}}}},
		}}},
		{{Key: "$limit", Value: 1}},
	}

	cursor, err := coll.Aggregate(ctx, pipeline)
	if err != nil {
		return fmt.Errorf("detect ambiguous docs: %w", err)
	}
	defer cursor.Close(ctx)

	if cursor.Next(ctx) {
		return errors.New(
			"systemplane/mongodb: ambiguous pre-migration state: documents exist with missing/empty tenant_id AND a '_global' sibling for the same (namespace, key) — consolidate before enabling phase 2",
		)
	}

	if err := cursor.Err(); err != nil {
		return fmt.Errorf("detect ambiguous docs cursor: %w", err)
	}

	return nil
}

// backfillTenantID sets tenant_id = "_global" for any document where the field
// is absent. Safe to re-run: $exists:false matches nothing after the first
// successful pass.
func backfillTenantID(ctx context.Context, coll *mongo.Collection) error {
	filter := bson.D{{Key: bsonFieldTenantID, Value: bson.D{{Key: "$exists", Value: false}}}}
	update := bson.D{{Key: bsonOpSet, Value: bson.D{{Key: bsonFieldTenantID, Value: store.SentinelGlobal}}}}

	if _, err := coll.UpdateMany(ctx, filter, update); err != nil {
		return err //nolint:wrapcheck // wrapped by ensureSchema
	}

	return nil
}

// dropLegacyIndex removes the pre-tenant (namespace, key) unique index. Not
// finding the index is a no-op — fresh installations never had one.
// Not finding the collection itself (NamespaceNotFound) is likewise a no-op:
// fresh installations create the collection lazily on the first write, so
// there is no index to drop yet.
func dropLegacyIndex(ctx context.Context, coll *mongo.Collection) error {
	if err := coll.Indexes().DropOne(ctx, legacyNamespaceKeyIndex); err != nil {
		if isIndexNotFoundErr(err) || isNamespaceNotFoundErr(err) {
			return nil
		}

		return err //nolint:wrapcheck // wrapped by ensureSchema
	}

	return nil
}

// createCompoundIndex creates the composite unique index on the
// (namespace, key, tenant_id) tuple. The _id compound already enforces
// uniqueness by construction, so this index is functionally a belt-and-
// suspenders guard that also serves as a natural covering index for the
// Distinct lookup in ListTenantsForKey.
func createCompoundIndex(ctx context.Context, coll *mongo.Collection) error {
	model := mongo.IndexModel{
		Keys: bson.D{
			{Key: bsonFieldNamespace, Value: 1},
			{Key: bsonFieldKey, Value: 1},
			{Key: bsonFieldTenantID, Value: 1},
		},
		Options: options.Index().SetUnique(true),
	}

	if _, err := coll.Indexes().CreateOne(ctx, model); err != nil {
		return err //nolint:wrapcheck // wrapped by ensureSchema
	}

	return nil
}

// isIndexNotFoundErr reports whether err is the MongoDB server's
// IndexNotFound (code 27). Handles two driver shapes: CommandError with
// Code == 27 (the canonical path) and the fallback string match for drivers
// that wrap the error differently across versions.
func isIndexNotFoundErr(err error) bool {
	if err == nil {
		return false
	}

	var cmdErr mongo.CommandError
	if errors.As(err, &cmdErr) {
		if cmdErr.HasErrorCode(indexNotFoundCode) {
			return true
		}
	}

	// Defensive fallback: mongo-driver/v2 sometimes wraps the server response
	// in a non-CommandError type for dropIndexes. The server-side message
	// text is stable: "index not found with name [%s]".
	return strings.Contains(err.Error(), "index not found")
}

// isNamespaceNotFoundErr reports whether err is the MongoDB server's
// NamespaceNotFound (code 26). This is the error returned by dropIndexes
// and similar commands when the target collection itself does not yet exist,
// which is the expected state during ensureSchema on a fresh installation.
// Like isIndexNotFoundErr, this handles both CommandError (canonical path)
// and the "ns not found" substring fallback for driver versions that wrap
// the server reply differently.
func isNamespaceNotFoundErr(err error) bool {
	if err == nil {
		return false
	}

	var cmdErr mongo.CommandError
	if errors.As(err, &cmdErr) {
		if cmdErr.HasErrorCode(namespaceNotFoundCode) {
			return true
		}
	}

	return strings.Contains(err.Error(), "ns not found")
}
