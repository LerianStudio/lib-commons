// Tenant-scoped storage methods for the MongoDB backend.
//
// These methods complement the global Set / Get / List surface by letting
// callers read, write, delete, and enumerate tenant-specific rows under the
// same (namespace, key) addressing scheme. The sentinel tenant_id "_global"
// is reserved for the non-tenant-scoped row; tenant callers always supply
// a non-empty tenantID that is enforced upstream by the Client.
//
// See TRD §2.3 for the interface contract and §4 for the full dataflow.

package mongodb

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/store"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.opentelemetry.io/otel/attribute"
)

// ErrInvalidTenantID is returned when a tenant-scoped read/write is called
// with a tenantID that equals the reserved "_global" sentinel. The sentinel
// addresses the shared row via the legacy Set/Get/List surface; tenant-scoped
// methods MUST fail closed on it rather than silently aliasing to a global
// read (M-S3-7).
var ErrInvalidTenantID = errors.New("mongodb store: tenantID must not be the '_global' sentinel")

// GetTenantValue returns the tenant-specific override for (namespace, key)
// scoped to tenantID. Returns (Entry{}, false, nil) when no override row
// exists; callers are expected to fall back to the "_global" row.
//
// The returned Entry has TenantID populated with the requested tenantID
// when found.
func (s *Store) GetTenantValue(
	ctx context.Context,
	tenantID, namespace, key string,
) (store.Entry, bool, error) {
	if s == nil || s.isClosed() {
		return store.Entry{}, false, store.ErrClosed
	}

	ctx, span := s.tracer.Start(ctx, "systemplane.mongodb.get_tenant_value")
	defer span.End()

	// Keep span attributes bounded: entry.namespace and entry.key are
	// enumerable across the registered-key set. tenant.id is deliberately
	// omitted because it is unbounded in a real multi-tenant deployment
	// and would inflate trace index cardinality on every store call.
	// Tenant ID flows through ctx and is recorded by the Client layer's
	// telemetry (which already pairs it with bounded enum labels).
	span.SetAttributes(
		attribute.String("entry.namespace", namespace),
		attribute.String("entry.key", key),
	)

	if tenantID == "" {
		err := errors.New("mongodb get_tenant_value: tenantID must not be empty")
		opentelemetry.HandleSpanBusinessErrorEvent(span, "validation failed", err)

		return store.Entry{}, false, err
	}

	if tenantID == store.SentinelGlobal {
		// Reject "_global" on the tenant surface so a caller misuse does
		// not silently alias to the legacy global read. The legacy Get
		// method is the correct path for the shared row. (M-S3-7)
		opentelemetry.HandleSpanBusinessErrorEvent(span, "validation failed", ErrInvalidTenantID)

		return store.Entry{}, false, ErrInvalidTenantID
	}

	doc, found, err := s.findOne(ctx, namespace, key, tenantID)
	if err != nil {
		opentelemetry.HandleSpanError(span, "mongodb get_tenant_value: find failed", err)
		return store.Entry{}, false, fmt.Errorf("mongodb store get_tenant_value: %w", err)
	}

	if !found {
		return store.Entry{}, false, nil
	}

	return doc.toEntry(), true, nil
}

// SetTenantValue persists an entry under tenantID using last-write-wins
// semantics. The backend writes tenantID into the row's tenant_id field and
// the change-stream / polling loop emits an Event whose TenantID equals
// tenantID. The Entry.TenantID field on input is ignored; tenantID is
// authoritative (mirrors TRD §4.1 step 6).
func (s *Store) SetTenantValue(ctx context.Context, tenantID string, e store.Entry) error {
	if s == nil || s.isClosed() {
		return store.ErrClosed
	}

	if !s.cfg.TenantSchemaEnabled {
		return store.ErrTenantSchemaNotEnabled
	}

	ctx, span := s.tracer.Start(ctx, "systemplane.mongodb.set_tenant_value")
	defer span.End()

	// See get_tenant_value: tenant.id stays off the span for cardinality.
	span.SetAttributes(
		attribute.String("entry.namespace", e.Namespace),
		attribute.String("entry.key", e.Key),
	)

	if tenantID == "" {
		err := errors.New("mongodb set_tenant_value: tenantID must not be empty")
		opentelemetry.HandleSpanBusinessErrorEvent(span, "validation failed", err)

		return err
	}

	if tenantID == store.SentinelGlobal {
		err := errors.New("mongodb set_tenant_value: tenantID must not be the '_global' sentinel")
		opentelemetry.HandleSpanBusinessErrorEvent(span, "validation failed", err)

		return err
	}

	if e.Namespace == "" || e.Key == "" {
		err := errors.New("mongodb set_tenant_value: namespace and key must be non-empty")
		opentelemetry.HandleSpanBusinessErrorEvent(span, "validation failed", err)

		return err
	}

	if err := s.upsert(ctx, e, tenantID); err != nil {
		opentelemetry.HandleSpanError(span, "mongodb set_tenant_value: upsert failed", err)
		return fmt.Errorf("mongodb store set_tenant_value: %w", err)
	}

	return nil
}

// DeleteTenantValue removes a single tenant's override row for
// (namespace, key). Returns nil when no matching row exists (delete is
// idempotent). The change-stream DELETE event surfaces tenantID via the
// compound _id on documentKey so downstream subscribers can re-derive the
// effective value from the "_global" row (TRD §4.4).
//
// The actor argument is accepted for audit parity with the Postgres backend;
// MongoDB has no equivalent of the trigger audit trail, so actor is recorded
// via the span attribute and — on a row that actually existed — an
// info-level audit log line (L-S3-sec-2).
func (s *Store) DeleteTenantValue(
	ctx context.Context,
	tenantID, namespace, key, actor string,
) error {
	if s == nil || s.isClosed() {
		return store.ErrClosed
	}

	if !s.cfg.TenantSchemaEnabled {
		return store.ErrTenantSchemaNotEnabled
	}

	ctx, span := s.tracer.Start(ctx, "systemplane.mongodb.delete_tenant_value")
	defer span.End()

	// See get_tenant_value: tenant.id and entry.actor stay off the span
	// for cardinality. Both values are recorded to the audit logger below
	// (line ~210) when a row actually went away, where the structured log
	// backend has a much higher cardinality budget than the trace index.
	span.SetAttributes(
		attribute.String("entry.namespace", namespace),
		attribute.String("entry.key", key),
	)

	if tenantID == "" {
		err := errors.New("mongodb delete_tenant_value: tenantID must not be empty")
		opentelemetry.HandleSpanBusinessErrorEvent(span, "validation failed", err)

		return err
	}

	if tenantID == store.SentinelGlobal {
		err := errors.New("mongodb delete_tenant_value: tenantID must not be the '_global' sentinel")
		opentelemetry.HandleSpanBusinessErrorEvent(span, "validation failed", err)

		return err
	}

	filter := bson.D{{Key: mongoFieldID, Value: compoundID{
		Namespace: namespace,
		Key:       key,
		TenantID:  tenantID,
	}}}

	// DeleteOne returns DeleteResult{DeletedCount: 0} and nil when no row
	// matches — idempotent by design. Only emit the audit log when a row
	// actually went away, otherwise the log becomes noise on retried
	// no-op deletes.
	res, err := s.coll.DeleteOne(ctx, filter)
	if err != nil {
		opentelemetry.HandleSpanError(span, "mongodb delete_tenant_value: delete failed", err)

		return fmt.Errorf("mongodb store delete_tenant_value: %w", err)
	}

	if res.DeletedCount > 0 {
		s.logInfo(ctx, "tenant_value_deleted",
			log.String("tenant_id", tenantID),
			log.String("namespace", namespace),
			log.String("key", key),
			log.String("actor", actor),
		)
	}

	return nil
}

// ListTenantValues returns every row in the collection, including both the
// "_global" rows and every tenant-specific override. Hydrate paths that want
// only overrides use ListTenantOverrides; this method is kept for the
// contract test suite and any caller that needs the full listing.
//
// Returns an empty slice (not nil) when the collection is empty. Server-side
// sort on (namespace, key, tenant_id) keeps ordering deterministic without
// depending on WiredTiger's natural order.
func (s *Store) ListTenantValues(ctx context.Context) ([]store.Entry, error) {
	if s == nil || s.isClosed() {
		return nil, store.ErrClosed
	}

	ctx, span := s.tracer.Start(ctx, "systemplane.mongodb.list_tenant_values")
	defer span.End()

	findOpts := options.Find().SetSort(bson.D{
		{Key: "namespace", Value: 1},
		{Key: mongoFieldKey, Value: 1},
		{Key: mongoFieldTenantID, Value: 1},
	})

	cursor, err := s.coll.Find(ctx, bson.D{}, findOpts)
	if err != nil {
		opentelemetry.HandleSpanError(span, "mongodb list_tenant_values: find failed", err)
		return nil, fmt.Errorf("mongodb store list_tenant_values: %w", err)
	}
	defer cursor.Close(ctx)

	var docs []entryDoc
	if err := cursor.All(ctx, &docs); err != nil {
		opentelemetry.HandleSpanError(span, "mongodb list_tenant_values: decode failed", err)
		return nil, fmt.Errorf("mongodb store list_tenant_values: decode: %w", err)
	}

	entries := make([]store.Entry, len(docs))
	for i := range docs {
		entries[i] = docs[i].toEntry()
	}

	span.SetAttributes(attribute.Int("entries.count", len(entries)))

	return entries, nil
}

// ListTenantOverrides returns only the tenant-scoped rows (tenant_id !=
// '_global') in (namespace, key, tenant_id) ascending order. Used by the
// Client's hydrateTenantCache at Start() and by admin endpoints that page
// through overrides. The filter runs server-side so a large _global row
// count does not inflate hydration transfer.
//
// Keyset pagination is driven by the (afterNamespace, afterKey,
// afterTenantID) tuple: callers pass the empty strings on the first page,
// then repeat with the last-observed tuple for each subsequent page. A
// non-positive limit is treated as "no limit" (unbounded page — intended
// for the Client hydrate path, which consumes the entire result anyway).
//
// The returned slice is empty (not nil) when no overrides exist after the
// cursor. Callers detect end-of-stream when len(result) < limit on a
// bounded page, or when the returned slice is empty on an unbounded page.
//
// COORDINATION NOTE: the parallel refactor on internal/store/store.go is
// introducing a TenantEntry type and updating the Store interface signature
// to match this shape. Until that lands, this method returns []store.Entry
// (the existing type) — which the parallel refactor will rename / alias to
// TenantEntry in a single rename pass across every backend. The method
// body is the hard part; the type name is a mechanical follow-up.
func (s *Store) ListTenantOverrides(
	ctx context.Context,
	afterNamespace, afterKey, afterTenantID string,
	limit int,
) ([]store.Entry, error) {
	if s == nil || s.isClosed() {
		return nil, store.ErrClosed
	}

	ctx, span := s.tracer.Start(ctx, "systemplane.mongodb.list_tenant_overrides")
	defer span.End()

	// cursor.after_tenant is omitted for cardinality: it takes one of
	// {tenant ID values}, which is unbounded. The (afterNamespace,
	// afterKey) pair is bounded by the registered-key set and is kept for
	// debugging keyset pagination.
	span.SetAttributes(
		attribute.String("cursor.after_namespace", afterNamespace),
		attribute.String("cursor.after_key", afterKey),
		attribute.Int("cursor.limit", limit),
	)

	// Base filter: exclude the global rows. Keyset predicate (when
	// afterNamespace/afterKey/afterTenantID is non-empty) is layered on top
	// as a lexicographic "greater than" across the sort triple. Mongo has
	// no native tuple comparison, so the cursor is expressed as the
	// canonical OR-chain that matches how SQL's (a,b,c) > (x,y,z)
	// desugars.
	and := bson.A{
		bson.D{{Key: mongoFieldTenantID, Value: bson.D{{Key: mongoOperatorNE, Value: store.SentinelGlobal}}}},
	}

	if afterNamespace != "" || afterKey != "" || afterTenantID != "" {
		and = append(and, bson.D{{Key: "$or", Value: bson.A{
			bson.D{{Key: mongoFieldNamespace, Value: bson.D{{Key: mongoOperatorGT, Value: afterNamespace}}}},
			bson.D{
				{Key: mongoFieldNamespace, Value: afterNamespace},
				{Key: mongoFieldKey, Value: bson.D{{Key: mongoOperatorGT, Value: afterKey}}},
			},
			bson.D{
				{Key: mongoFieldNamespace, Value: afterNamespace},
				{Key: mongoFieldKey, Value: afterKey},
				{Key: mongoFieldTenantID, Value: bson.D{{Key: mongoOperatorGT, Value: afterTenantID}}},
			},
		}}})
	}

	filter := bson.D{{Key: "$and", Value: and}}

	findOpts := options.Find().SetSort(bson.D{
		{Key: mongoFieldNamespace, Value: 1},
		{Key: mongoFieldKey, Value: 1},
		{Key: mongoFieldTenantID, Value: 1},
	})

	if limit > 0 {
		findOpts = findOpts.SetLimit(int64(limit))
	}

	cursor, err := s.coll.Find(ctx, filter, findOpts)
	if err != nil {
		opentelemetry.HandleSpanError(span, "mongodb list_tenant_overrides: find failed", err)
		return nil, fmt.Errorf("mongodb store list_tenant_overrides: %w", err)
	}
	defer cursor.Close(ctx)

	var docs []entryDoc
	if err := cursor.All(ctx, &docs); err != nil {
		opentelemetry.HandleSpanError(span, "mongodb list_tenant_overrides: decode failed", err)
		return nil, fmt.Errorf("mongodb store list_tenant_overrides: decode: %w", err)
	}

	entries := make([]store.Entry, len(docs))
	for i := range docs {
		entries[i] = docs[i].toEntry()
	}

	span.SetAttributes(attribute.Int("entries.count", len(entries)))

	return entries, nil
}

// ListTenantsForKey returns a sorted, deduplicated list of distinct tenant
// IDs that have an override for (namespace, key). The "_global" sentinel is
// excluded. Returns an empty slice (not nil) when no tenant has overridden
// the key.
func (s *Store) ListTenantsForKey(
	ctx context.Context,
	namespace, key string,
) ([]string, error) {
	if s == nil || s.isClosed() {
		return nil, store.ErrClosed
	}

	ctx, span := s.tracer.Start(ctx, "systemplane.mongodb.list_tenants_for_key")
	defer span.End()

	span.SetAttributes(
		attribute.String("entry.namespace", namespace),
		attribute.String("entry.key", key),
	)

	filter := bson.D{
		{Key: mongoFieldNamespace, Value: namespace},
		{Key: mongoFieldKey, Value: key},
		{Key: mongoFieldTenantID, Value: bson.D{{Key: mongoOperatorNE, Value: store.SentinelGlobal}}},
	}

	// Distinct is the natural operator here: one index seek, constant memory,
	// no cursor iteration required. Check .Err() BEFORE .Decode — mongo-driver
	// v2's DistinctResult.Decode does NOT inspect its internal err field, so a
	// failed operation (auth error, network drop mid-flight, invalid collation)
	// would otherwise surface as a silent empty slice. Inspecting .Err() first
	// preserves fail-closed semantics.
	res := s.coll.Distinct(ctx, mongoFieldTenantID, filter)
	if err := res.Err(); err != nil {
		// ErrNoDocuments from a Distinct that matched nothing is benign —
		// return an empty slice rather than propagating.
		if errors.Is(err, mongo.ErrNoDocuments) {
			return []string{}, nil
		}

		opentelemetry.HandleSpanError(span, "mongodb list_tenants_for_key: distinct failed", err)

		return nil, fmt.Errorf("mongodb store list_tenants_for_key: %w", err)
	}

	var tenantIDs []string
	if err := res.Decode(&tenantIDs); err != nil {
		opentelemetry.HandleSpanError(span, "mongodb list_tenants_for_key: decode failed", err)

		return nil, fmt.Errorf("mongodb store list_tenants_for_key: decode: %w", err)
	}

	// Nil-coerce BEFORE sort.Strings so the empty-but-non-nil invariant
	// holds end-to-end (L-S3-3). sort.Strings(nil) is safe, but a later
	// reader could easily introduce a bug that depends on the slice being
	// non-nil — better to normalize early.
	if tenantIDs == nil {
		tenantIDs = []string{}
	}

	sort.Strings(tenantIDs)

	span.SetAttributes(attribute.Int("tenants.count", len(tenantIDs)))

	return tenantIDs, nil
}
