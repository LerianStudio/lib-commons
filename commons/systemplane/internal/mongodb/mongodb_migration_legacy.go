// Legacy ObjectId-_id rewrite machinery for the Phase-2 MongoDB migration.
//
// Split out of mongodb_migration.go so the higher-level orchestration
// (ensureSchema / ensureLegacySchema) stays readable and this file can
// carry the batching / lease / decoder plumbing without inflating the
// main migration file past ~350 LOC.
//
// Two moving parts live here:
//
//   - acquireMigrationLease coordinates concurrent New() calls from
//     different processes so only one runs the O(N) rewrite at a time (H3).
//   - rewriteObjectIDDocuments drains the cursor of ObjectId-keyed rows,
//     then re-inserts / deletes them in chunked InsertMany / DeleteMany
//     batches so one large pre-migration corpus doesn't saturate the oplog
//     with N+1 single-document operations (H4).

package mongodb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LerianStudio/lib-observability/log"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/store"
	"github.com/google/uuid"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// migrationLeaseID is the sentinel _id value used for the cross-process
// migration coordination doc. Underscore-prefixed so it cannot collide with
// either a legacy ObjectId _id or a real compound _id (compoundID is a
// BSON sub-document, never a plain string).
const migrationLeaseID = "__systemplane_migration_lease__"

// migrationLeaseStaleAfter is the heartbeat staleness window after which a
// lease held by a peer is considered abandoned and the current caller may
// proceed. 5 minutes is a comfortable upper bound on rewriteObjectIDDocuments
// for the realistic corpus sizes (PRD §8 estimate: ~1,200 rows; even 10k
// rows complete well under a minute on a local replica set).
const migrationLeaseStaleAfter = 5 * time.Minute

// migrationBatchSize is the chunk size for InsertMany / DeleteMany during
// the legacy-row rewrite. MongoDB's bulkWrite command tolerates up to 100k
// documents per batch on 4.4+; 500 is a conservative number that keeps
// each RPC small (~250KB at the worst-case row shape) without introducing
// N+1 round-trips for realistic corpus sizes.
const migrationBatchSize = 500

// leaseDoc is the persisted shape of the migration lease.
type leaseDoc struct {
	ID        string    `bson:"_id"`
	Owner     string    `bson:"owner"`
	Heartbeat time.Time `bson:"heartbeat"`
	Acquired  time.Time `bson:"acquired_at"`
}

// acquireMigrationLease attempts to claim the migration lease. On success,
// it returns the original ctx (unchanged for now — a future revision could
// attach a keepalive heartbeat goroutine), a release closure that MUST be
// deferred by the caller, and acquired=true. On "peer holds a fresh lease",
// it returns acquired=false without error so the caller can no-op the
// migration and proceed to createCompoundIndex (which is idempotent).
//
// The release closure uses a best-effort DeleteOne filtered by owner, so a
// lease that was already stolen by a staleness-window overrun is not
// accidentally deleted by the original owner. Errors during release are
// logged at WARN and swallowed; the sentinel document is tiny and next
// run's acquire will overwrite it.
func acquireMigrationLease(
	ctx context.Context,
	coll *mongo.Collection,
	logger log.Logger,
) (context.Context, func(), bool, error) {
	owner := uuid.NewString()
	now := time.Now().UTC()

	// findOneAndUpdate with upsert+return-after gives us the doc as it
	// exists after our write — either our just-written lease (acquired) or
	// the peer's lease (lost). We check the owner field to disambiguate.
	filter := bson.D{{Key: "_id", Value: migrationLeaseID}}

	// $setOnInsert seeds owner/acquired on first write; $set refreshes the
	// heartbeat unconditionally so a live owner re-entering its own lease
	// (e.g., process restart that reuses the owner UUID — unlikely but
	// cheap to tolerate) keeps the heartbeat current.
	update := bson.D{
		{Key: "$set", Value: bson.D{
			{Key: "heartbeat", Value: now},
		}},
		{Key: "$setOnInsert", Value: bson.D{
			{Key: "owner", Value: owner},
			{Key: "acquired_at", Value: now},
		}},
	}

	opts := options.FindOneAndUpdate().
		SetUpsert(true).
		SetReturnDocument(options.After)

	var current leaseDoc

	err := coll.FindOneAndUpdate(ctx, filter, update, opts).Decode(&current)
	if err != nil {
		return ctx, func() {}, false, fmt.Errorf("findOneAndUpdate lease: %w", err)
	}

	// Did we just insert it? If so, Owner == our UUID.
	if current.Owner == owner {
		release := func() {
			// Best-effort release. Filter by owner so a stolen lease is left
			// alone. Swallow errors — the document is a singleton sentinel
			// and the next acquire will overwrite it anyway.
			releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			_, _ = coll.DeleteOne(releaseCtx, bson.D{
				{Key: "_id", Value: migrationLeaseID},
				{Key: "owner", Value: owner},
			})
		}

		return ctx, release, true, nil
	}

	// Peer holds the lease. If their heartbeat is stale, forcibly steal it.
	if time.Since(current.Heartbeat) > migrationLeaseStaleAfter {
		if logger != nil {
			logger.Log(ctx, log.LevelWarn,
				"systemplane/mongodb: migration lease stale, stealing",
				log.String("previous_owner", current.Owner),
				log.String("new_owner", owner),
			)
		}

		// Atomically swap owner. Filter by the previous owner so we don't
		// race another stealer. If the CAS fails (peer refreshed between
		// our read and this write), treat it as "peer is alive" and skip.
		stealFilter := bson.D{
			{Key: "_id", Value: migrationLeaseID},
			{Key: "owner", Value: current.Owner},
		}
		stealUpdate := bson.D{{Key: "$set", Value: bson.D{
			{Key: "owner", Value: owner},
			{Key: "heartbeat", Value: now},
			{Key: "acquired_at", Value: now},
		}}}

		res, stealErr := coll.UpdateOne(ctx, stealFilter, stealUpdate)
		if stealErr != nil {
			return ctx, func() {}, false, fmt.Errorf("steal stale lease: %w", stealErr)
		}

		if res.ModifiedCount == 1 {
			release := func() {
				releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()

				_, _ = coll.DeleteOne(releaseCtx, bson.D{
					{Key: "_id", Value: migrationLeaseID},
					{Key: "owner", Value: owner},
				})
			}

			return ctx, release, true, nil
		}
	}

	// Fresh lease held by a live peer. Let them finish.
	return ctx, func() {}, false, nil
}

// legacyDoc mirrors entryDoc but decodes _id as bson.RawValue so we can
// inspect its BSON type without forcing a compoundID decode that would fail
// on legacy ObjectId-valued rows.
//
// Namespace/Key/TenantID are duplicated from the top-level fields rather
// than derived from _id because the legacy-shape rows predate the compound
// _id convention: top-level scalars are the only reliable source on
// ObjectId-keyed rows. UpdatedAt/UpdatedBy round-trip so the migrated row
// preserves audit metadata verbatim. See rewriteObjectIDDocuments.
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
// ObjectId (from the pre-Task-4 schema) into one with a compound _id.
//
// Execution model (H4): the cursor is drained into memory first, then the
// migrated documents are issued in InsertMany batches of migrationBatchSize,
// followed by DeleteMany batches targeting the corresponding legacy _ids.
// This avoids the N round-trip cost of sequential InsertOne/DeleteOne on
// large corpora (the original shape — see git history).
//
// Crash safety: the insert-then-delete ordering keeps the row visible under
// the new _id if the process dies between the two batch operations; the
// stale ObjectId rows are cleaned up on the next run's rewrite pass. The
// lease held by the caller (acquireMigrationLease) ensures only one
// process runs this loop at a time, so inserts won't race each other into
// E11000.
//
// Not atomic across the collection: batches are committed independently.
// Mid-migration crashes leave a mix of old and new _id shapes, but re-
// running ensureSchema cleans up whatever is left.
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

	if len(docs) == 0 {
		return nil
	}

	// Build migrated documents and collect legacy _ids for deletion. Do the
	// transform up-front so the per-batch loop below is pure I/O.
	migrated := make([]any, 0, len(docs))
	legacyIDs := make([]bson.RawValue, 0, len(docs))

	for _, doc := range docs {
		tenantID := doc.TenantID
		if tenantID == "" {
			tenantID = store.SentinelGlobal
		}

		newID := compoundID{
			Namespace: doc.Namespace,
			Key:       doc.Key,
			TenantID:  tenantID,
		}

		// Rescue zero-valued updated_at timestamps. Pre-tenant rows
		// occasionally lacked the field (driver omitempty artifacts on
		// older BSON codecs), and BSON-decoding an absent time yields the
		// zero value. An unset timestamp would break the polling path's
		// watermark comparison (time.Time.After(time.Time{}) is always
		// true for any real time), so substitute "now" — the migration IS
		// the update event for this row. (L-S3-BL-2)
		updatedAt := doc.UpdatedAt
		if updatedAt.IsZero() {
			updatedAt = time.Now().UTC()
		}

		// bson.D for explicit field ordering, decoupled from entryDoc's
		// struct-tag shape so future entryDoc changes don't silently alter
		// the persisted migration payload.
		newDoc := bson.D{
			{Key: "_id", Value: newID},
			{Key: "namespace", Value: doc.Namespace},
			{Key: "key", Value: doc.Key},
			{Key: "tenant_id", Value: tenantID},
			{Key: "value", Value: doc.Value},
			{Key: "updated_at", Value: updatedAt},
			{Key: "updated_by", Value: doc.UpdatedBy},
		}

		migrated = append(migrated, newDoc)
		legacyIDs = append(legacyIDs, doc.ID)
	}

	// Insert in batches first, then delete in matching batches. Insert
	// uses Ordered(false) so a single E11000 (benign — prior run already
	// migrated this row) does not halt the batch; subsequent documents
	// continue to insert.
	insertOpts := options.InsertMany().SetOrdered(false)

	for start := 0; start < len(migrated); start += migrationBatchSize {
		end := min(start+migrationBatchSize, len(migrated))

		if _, err := coll.InsertMany(ctx, migrated[start:end], insertOpts); err != nil {
			// An InsertMany with ordered=false reports a BulkWriteException
			// containing every individual failure. Duplicate-key failures
			// are benign (a previous partial run migrated those rows) and
			// should not abort the migration. Any other per-document error
			// is re-raised so operators see the real cause.
			if !bulkWriteErrorsAllDuplicates(err) {
				return fmt.Errorf("insert migrated batch [%d:%d]: %w", start, end, err)
			}
		}
	}

	// Delete the legacy rows in batches keyed by their ObjectId _ids.
	for start := 0; start < len(legacyIDs); start += migrationBatchSize {
		end := min(start+migrationBatchSize, len(legacyIDs))

		batch := legacyIDs[start:end]

		values := make(bson.A, 0, len(batch))
		for _, id := range batch {
			values = append(values, id)
		}

		deleteFilter := bson.D{{Key: "_id", Value: bson.D{{Key: "$in", Value: values}}}}

		if _, err := coll.DeleteMany(ctx, deleteFilter); err != nil {
			return fmt.Errorf("delete legacy batch [%d:%d]: %w", start, end, err)
		}
	}

	return nil
}

// bulkWriteErrorsAllDuplicates reports whether every write error on a
// BulkWriteException is a duplicate-key (11000) — in which case the insert
// batch is a benign re-run of a previously-migrated subset and migration
// can continue. Any non-11000 error indicates something genuinely wrong
// (auth failure, schema mismatch) and must propagate.
func bulkWriteErrorsAllDuplicates(err error) bool {
	if err == nil {
		return false
	}

	var bwe mongo.BulkWriteException
	if !errors.As(err, &bwe) {
		return false
	}

	if len(bwe.WriteErrors) == 0 {
		return false
	}

	for _, we := range bwe.WriteErrors {
		if we.Code != 11000 {
			return false
		}
	}

	return true
}
