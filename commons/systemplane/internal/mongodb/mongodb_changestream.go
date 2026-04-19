// Change-stream and polling subscriptions for the MongoDB backend.
//
// The change-stream path is the default and requires a replica set; the
// polling path is the fallback for standalone deployments and is enabled by
// setting Config.PollInterval to a positive duration. Both paths emit
// store.Event values whose TenantID is populated from the row's tenant_id
// field so downstream subscribers can route global-vs-tenant dispatch.
//
// Delete events carry neither a fullDocument nor (under stock MongoDB) any
// pre-image. This backend sidesteps the problem by storing a compound _id
// {namespace, key, tenant_id} on every row, so the documentKey that the
// server always includes is sufficient on its own to reconstruct the event.
// See TRD §3.2 and research.md §MongoDB.

package mongodb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/backoff"
	"github.com/LerianStudio/lib-commons/v5/commons/log"
	libRuntime "github.com/LerianStudio/lib-commons/v5/commons/runtime"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/store"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// operationTypeDelete matches the string the MongoDB server places in
// changeEvent.OperationType for delete events.
const operationTypeDelete = "delete"

// changeEventFullDoc captures the shape of an insert/update/replace event's
// fullDocument — the only branch where all three scoping fields are present
// in the payload.
type changeEventFullDoc struct {
	Namespace string `bson:"namespace"`
	Key       string `bson:"key"`
	TenantID  string `bson:"tenant_id"`
}

// changeEvent is the subset of a MongoDB change stream event we decode.
//
// DocumentKey decodes to the server-populated {_id: compoundID} document.
// On insert/update/replace events FullDocument is also populated courtesy
// of SetFullDocument(UpdateLookup); on delete events FullDocument is nil
// and DocumentKey is the only source of truth — hence the compound _id.
type changeEvent struct {
	OperationType string              `bson:"operationType"`
	FullDocument  *changeEventFullDoc `bson:"fullDocument,omitempty"`
	DocumentKey   struct {
		ID compoundID `bson:"_id"`
	} `bson:"documentKey"`
}

// Subscribe blocks until ctx is cancelled, watching for changes via
// change-streams or polling depending on cfg.PollInterval.
//
// Change-stream mode (PollInterval == 0) requires a MongoDB replica set.
// Polling mode (PollInterval > 0) works with standalone MongoDB deployments.
//
// Multiple concurrent Subscribe calls are independent — each gets its own
// stream or ticker.
func (s *Store) Subscribe(ctx context.Context, handler func(store.Event)) error {
	if s == nil || s.isClosed() {
		return store.ErrClosed
	}

	if handler == nil {
		return errors.New("mongodb store subscribe: handler must not be nil")
	}

	if s.cfg.PollInterval > 0 {
		return s.subscribePoll(ctx, handler)
	}

	return s.subscribeChangeStream(ctx, handler)
}

// subscribeChangeStream opens a MongoDB change stream on the collection and
// translates insert/update/replace/delete events into store.Event values.
// On stream error, it reconnects with exponential backoff.
func (s *Store) subscribeChangeStream(ctx context.Context, handler func(store.Event)) error {
	attempt := 0

	for {
		if err := s.watchOnce(ctx, handler); err != nil {
			// Context cancelled — clean exit regardless of the watch error.
			if ctx.Err() != nil {
				return ctx.Err() //nolint:wrapcheck // propagate cancellation as-is
			}

			// Log and reconnect with backoff.
			s.logWarn(ctx, "change stream disconnected, reconnecting",
				log.Err(err),
				log.Int("attempt", attempt),
			)

			delay := min(backoff.ExponentialWithJitter(reconnectBaseDelay, attempt), reconnectMaxDelay)
			attempt++

			if waitErr := backoff.WaitContext(ctx, delay); waitErr != nil {
				return waitErr //nolint:wrapcheck // propagate cancellation as-is
			}

			continue
		}

		return nil
	}
}

// watchOnce opens a single change stream session and processes events until
// error or context cancellation.
//
// # Resume token behavior (operational caveat)
//
// Resume tokens are NOT persisted across reconnects. When the change stream
// errors and subscribeChangeStream wakes the loop after its backoff delay,
// watchOnce opens a fresh stream that starts from the current oplog
// position — events that occurred during the disconnect window are lost.
//
// For active keys this is self-healing: the next write reinstates correct
// state via the debouncer's changefeed coalesce, so a missed intermediate
// event is eventually overwritten by a later one. For idle keys (no writes
// during or after the disconnect), the tenantCache may hold a stale value
// until the next explicit write or a Client restart re-hydrates from the
// source of truth in ensureSchema.
//
// Operators who need strict at-least-once delivery across reconnects
// should either:
//   - persist the resume token externally and thread it via
//     options.ChangeStream().SetResumeAfter(token) — this would require a
//     lib-commons API extension and is not currently exposed; or
//   - schedule a periodic ListTenantValues reconciliation at the Client
//     layer to cross-check the cache against the durable store.
//
// See also MIGRATION_TENANT_SCOPED.md §8.1 (Operational caveats).
func (s *Store) watchOnce(ctx context.Context, handler func(store.Event)) error {
	pipeline := mongo.Pipeline{
		{{Key: "$match", Value: bson.D{
			{Key: "operationType", Value: bson.D{
				{Key: "$in", Value: bson.A{"insert", "update", "replace", operationTypeDelete}},
			}},
		}}},
	}

	opts := options.ChangeStream().
		SetFullDocument(options.UpdateLookup)

	stream, err := s.coll.Watch(ctx, pipeline, opts)
	if err != nil {
		return fmt.Errorf("open change stream: %w", err)
	}
	defer stream.Close(ctx)

	for stream.Next(ctx) {
		var event changeEvent
		if err := stream.Decode(&event); err != nil {
			s.logWarn(ctx, "change stream decode error, skipping event", log.Err(err))
			continue
		}

		evt, ok := extractEvent(event)
		if !ok {
			s.logWarn(ctx, "change stream event dropped — missing identifiers",
				log.String("operationType", event.OperationType),
			)

			continue
		}

		s.safeInvokeHandler(ctx, handler, evt)
	}

	if ctx.Err() != nil {
		return nil
	}

	if err := stream.Err(); err != nil {
		return fmt.Errorf("change stream error: %w", err)
	}

	return nil
}

// extractEvent converts a decoded changeEvent into a store.Event.
// Returns false if the event cannot be mapped (missing fields, etc.).
//
// Two paths:
//   - Delete: documentKey._id carries the full tuple (namespace, key,
//     tenant_id). No fullDocument is ever available from the server in this
//     branch. The tuple is already decoded into event.DocumentKey.ID by the
//     driver's BSON codec.
//   - Insert/update/replace: fullDocument is populated thanks to
//     SetFullDocument(UpdateLookup). We prefer fullDocument because on
//     update events documentKey may refer to a previous tenant_id shape
//     from a pre-migration row; the server always re-reads the current
//     shape into fullDocument.
//
// The delete branch is the critical one for tenant attribution; getting it
// wrong would silently fire OnChange for tenant deletes. Keep both paths in
// this single function so reviewers can see the invariant.
func extractEvent(ce changeEvent) (store.Event, bool) {
	if ce.OperationType == operationTypeDelete {
		id := ce.DocumentKey.ID
		if id.Namespace == "" || id.Key == "" || id.TenantID == "" {
			return store.Event{}, false
		}

		return store.Event{
			Namespace: id.Namespace,
			Key:       id.Key,
			TenantID:  id.TenantID,
		}, true
	}

	if ce.FullDocument == nil {
		return store.Event{}, false
	}

	fd := *ce.FullDocument
	if fd.Namespace == "" || fd.Key == "" {
		return store.Event{}, false
	}

	// A tenant_id coming through as "" is only expected on legacy rows that
	// slipped past ensureSchema's backfill. Treat as the sentinel rather
	// than dropping the event.
	tenantID := fd.TenantID
	if tenantID == "" {
		tenantID = store.SentinelGlobal
	}

	return store.Event{
		Namespace: fd.Namespace,
		Key:       fd.Key,
		TenantID:  tenantID,
	}, true
}

// subscribePoll uses a ticker to periodically query for entries updated since
// the last watermark and emits events for each changed entry. The polling
// path has no native "delete" signal — deletions that happen between two
// ticks are invisible to this path. Polling-mode consumers that need delete
// visibility should switch to change-streams (see Config.PollInterval).
func (s *Store) subscribePoll(ctx context.Context, handler func(store.Event)) error {
	watermark := time.Now().UTC()
	ticker := time.NewTicker(s.cfg.PollInterval)

	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			newWatermark, err := s.pollChanges(ctx, watermark, handler)
			if err != nil {
				// Transient error — log and retry on next tick.
				s.logWarn(ctx, "poll query failed", log.Err(err))
				continue
			}

			watermark = newWatermark
		}
	}
}

// pollChanges queries for documents updated after the watermark, invokes the
// handler for each, and returns the new watermark.
//
// Tenant parity: the emitted Event carries doc.TenantID, which makes the
// polling path's observable behavior consistent with the change-stream
// path for insert/update/replace events. Delete events are a change-stream
// exclusive.
func (s *Store) pollChanges(
	ctx context.Context,
	watermark time.Time,
	handler func(store.Event),
) (time.Time, error) {
	filter := bson.D{
		{Key: "updated_at", Value: bson.D{{Key: "$gt", Value: watermark}}},
	}

	findOpts := options.Find().SetSort(bson.D{{Key: "updated_at", Value: 1}})

	cursor, err := s.coll.Find(ctx, filter, findOpts)
	if err != nil {
		return watermark, fmt.Errorf("poll find: %w", err)
	}
	defer cursor.Close(ctx)

	newWatermark := watermark

	for cursor.Next(ctx) {
		var doc entryDoc
		if err := cursor.Decode(&doc); err != nil {
			s.logWarn(ctx, "poll decode error, skipping document", log.Err(err))
			continue
		}

		tenantID := doc.TenantID
		if tenantID == "" {
			tenantID = store.SentinelGlobal
		}

		evt := store.Event{
			Namespace: doc.Namespace,
			Key:       doc.Key,
			TenantID:  tenantID,
		}
		s.safeInvokeHandler(ctx, handler, evt)

		if doc.UpdatedAt.After(newWatermark) {
			newWatermark = doc.UpdatedAt
		}
	}

	if err := cursor.Err(); err != nil {
		return watermark, fmt.Errorf("poll cursor error: %w", err)
	}

	return newWatermark, nil
}

// safeInvokeHandler calls handler inside a deferred panic recovery.
func (s *Store) safeInvokeHandler(ctx context.Context, handler func(store.Event), evt store.Event) {
	defer libRuntime.RecoverAndLogWithContext(ctx, s.cfg.Logger, "systemplane", "mongodb.handler")

	handler(evt)
}
