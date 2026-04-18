// Idempotent schema evolution for the MongoDB backend.
//
// ensureSchema is called once per Store construction (New) and performs four
// distinct migrations in order:
//
//  1. Backfill: documents missing the tenant_id field gain tenant_id = "_global".
//  2. _id rewrite: documents whose _id is still an ObjectId (created by the
//     pre-tenant-scoping schema) are re-inserted with a compound _id
//     {namespace, key, tenant_id} and the original row is deleted. This is
//     what makes change-stream DELETE events self-describing — see TRD §3.2.
//  3. Old index drop: the legacy "namespace_1_key_1" unique index is
//     dropped. Index 27 (IndexNotFound) is treated as a no-op so fresh
//     installations succeed.
//  4. New index create: a compound unique index on (namespace, key, tenant_id)
//     is created. Duplicate index creation is idempotent under the same key
//     signature.
//
// Every step is crash-safe: on re-run of a half-applied migration, steps 1, 3,
// and 4 are naturally idempotent, and step 2 becomes a no-op once no
// ObjectId _id documents remain.

package mongodb

import (
	"context"
	"errors"
	"fmt"
	"time"

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
func ensureSchema(ctx context.Context, coll *mongo.Collection) error {
	if err := verifyNoAmbiguousTenantDocs(ctx, coll); err != nil {
		return err
	}

	if err := backfillTenantID(ctx, coll); err != nil {
		return fmt.Errorf("backfill tenant_id: %w", err)
	}

	if err := rewriteObjectIDDocuments(ctx, coll); err != nil {
		return fmt.Errorf("rewrite _id: %w", err)
	}

	if err := dropLegacyIndex(ctx, coll); err != nil {
		return fmt.Errorf("drop legacy index: %w", err)
	}

	if err := createCompoundIndex(ctx, coll); err != nil {
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
			{Key: "namespace", Value: 1},
			{Key: "key", Value: 1},
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
// tenant_id missing AND another document with tenant_id="_global", the
// subsequent backfill ($set tenant_id="_global" on the missing-field doc)
// would produce two "_global" documents for the same pair and — once the
// compound unique index is created — cause one of them to vanish on the
// next upsert. Fail loudly so operators consolidate the rows manually.
//
// The aggregation pipeline returns zero when the collection is empty or
// already consistent, so the check is a no-op on fresh deployments.
func verifyNoAmbiguousTenantDocs(ctx context.Context, coll *mongo.Collection) error {
	pipeline := mongo.Pipeline{
		// Project a "has_tenant_id" flag so we can count each shape per group.
		{{Key: "$group", Value: bson.D{
			{Key: "_id", Value: bson.D{
				{Key: "namespace", Value: "$namespace"},
				{Key: "key", Value: "$key"},
			}},
			{Key: "tenantIDs", Value: bson.D{
				{Key: "$addToSet", Value: bson.D{
					{Key: "$ifNull", Value: bson.A{"$tenant_id", "__missing__"}},
				}},
			}},
		}}},
		// A colliding group is one that contains BOTH the missing marker AND
		// the _global sentinel.
		{{Key: "$match", Value: bson.D{
			{Key: "tenantIDs", Value: bson.D{{Key: "$all", Value: bson.A{"__missing__", sentinelGlobal}}}},
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
			"systemplane/mongodb: ambiguous pre-migration state: documents exist with missing tenant_id AND a '_global' sibling for the same (namespace, key) — consolidate before enabling phase 2",
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
	filter := bson.D{{Key: "tenant_id", Value: bson.D{{Key: "$exists", Value: false}}}}
	update := bson.D{{Key: "$set", Value: bson.D{{Key: "tenant_id", Value: sentinelGlobal}}}}

	if _, err := coll.UpdateMany(ctx, filter, update); err != nil {
		return err //nolint:wrapcheck // wrapped by ensureSchema
	}

	return nil
}

// legacyDoc mirrors entryDoc but decodes _id as bson.RawValue so we can
// inspect its BSON type without forcing a compoundID decode that would fail
// on legacy ObjectId-valued rows.
type legacyDoc struct {
	ID        bson.RawValue `bson:"_id"`
	Namespace string        `bson:"namespace"`
	Key       string        `bson:"key"`
	TenantID  string        `bson:"tenant_id"`
	Value     string        `bson:"value"`
	UpdatedAt time.Time     `bson:"updated_at"`
	UpdatedBy string        `bson:"updated_by"`
}

// rewriteObjectIDDocuments converts any document whose _id is still a bare
// ObjectId (from the pre-Task-4 schema) into one with a compound _id. Runs
// sequentially — the migration is one-shot at construction and the expected
// document count is small (PRD §8 estimates ~1,200 entries across every
// known consumer) so batching machinery is unnecessary.
//
// Not atomic across the collection: a crash mid-loop leaves a mix of old
// and new _id shapes, but re-running ensureSchema cleans up whatever is
// left. The insert-then-delete order is chosen so that a crash after the
// insert but before the delete keeps the row visible under the new _id;
// the stale ObjectId row is cleaned on the next run.
func rewriteObjectIDDocuments(ctx context.Context, coll *mongo.Collection) error {
	filter := bson.D{{Key: "_id", Value: bson.D{{Key: "$type", Value: "objectId"}}}}

	cursor, err := coll.Find(ctx, filter)
	if err != nil {
		return err //nolint:wrapcheck // wrapped by ensureSchema
	}

	// Drain the cursor into memory BEFORE issuing any writes. Two reasons:
	//   1. We are about to insert new documents and delete old ones in the
	//      same collection; whether a still-open cursor would re-observe
	//      those writes depends on snapshot-vs-tailing semantics, which vary
	//      across server versions. Draining first removes the ambiguity.
	//   2. Expected worst-case cardinality is ~1,200 rows (PRD §8). Loading
	//      that into memory costs a few hundred KB — free.
	var docs []legacyDoc
	if err := cursor.All(ctx, &docs); err != nil {
		_ = cursor.Close(ctx)
		return fmt.Errorf("drain legacy cursor: %w", err)
	}

	if err := cursor.Close(ctx); err != nil {
		return fmt.Errorf("close legacy cursor: %w", err)
	}

	for _, doc := range docs {
		tenantID := doc.TenantID
		if tenantID == "" {
			tenantID = sentinelGlobal
		}

		newID := compoundID{
			Namespace: doc.Namespace,
			Key:       doc.Key,
			TenantID:  tenantID,
		}

		// Build the replacement document using bson.D so we control field
		// ordering and avoid the struct-tag coupling to entryDoc's internal
		// shape — migrations should be insulated from entryDoc churn.
		newDoc := bson.D{
			{Key: "_id", Value: newID},
			{Key: "namespace", Value: doc.Namespace},
			{Key: "key", Value: doc.Key},
			{Key: "tenant_id", Value: tenantID},
			{Key: "value", Value: doc.Value},
			{Key: "updated_at", Value: doc.UpdatedAt},
			{Key: "updated_by", Value: doc.UpdatedBy},
		}

		if _, err := coll.InsertOne(ctx, newDoc); err != nil {
			// A duplicate key error here means a previous run already
			// created the compound-id row; treat as benign and proceed
			// to delete the ObjectId original.
			if !mongo.IsDuplicateKeyError(err) {
				return fmt.Errorf("insert migrated doc: %w", err)
			}
		}

		if _, err := coll.DeleteOne(ctx, bson.D{{Key: "_id", Value: doc.ID}}); err != nil {
			return fmt.Errorf("delete legacy doc: %w", err)
		}
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
			{Key: "namespace", Value: 1},
			{Key: "key", Value: 1},
			{Key: "tenant_id", Value: 1},
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
	return containsSubstring(err.Error(), "index not found")
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

	return containsSubstring(err.Error(), "ns not found")
}

// containsSubstring is a minimal substring check shared by the error
// classifiers; keeping it here avoids importing strings solely for a
// single call site.
func containsSubstring(msg, needle string) bool {
	if len(needle) == 0 || len(msg) < len(needle) {
		return len(needle) == 0
	}

	for i := 0; i+len(needle) <= len(msg); i++ {
		if msg[i:i+len(needle)] == needle {
			return true
		}
	}

	return false
}
