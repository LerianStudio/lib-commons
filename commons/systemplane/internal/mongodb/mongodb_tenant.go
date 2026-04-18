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

	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/store"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.opentelemetry.io/otel/attribute"
)

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

	span.SetAttributes(
		attribute.String("tenant.id", tenantID),
		attribute.String("entry.namespace", namespace),
		attribute.String("entry.key", key),
	)

	if tenantID == "" {
		err := errors.New("mongodb get_tenant_value: tenantID must not be empty")
		opentelemetry.HandleSpanBusinessErrorEvent(span, "validation failed", err)

		return store.Entry{}, false, err
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

	ctx, span := s.tracer.Start(ctx, "systemplane.mongodb.set_tenant_value")
	defer span.End()

	span.SetAttributes(
		attribute.String("tenant.id", tenantID),
		attribute.String("entry.namespace", e.Namespace),
		attribute.String("entry.key", e.Key),
	)

	if tenantID == "" {
		err := errors.New("mongodb set_tenant_value: tenantID must not be empty")
		opentelemetry.HandleSpanBusinessErrorEvent(span, "validation failed", err)

		return err
	}

	if tenantID == sentinelGlobal {
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
// only via the span attribute below.
func (s *Store) DeleteTenantValue(
	ctx context.Context,
	tenantID, namespace, key, actor string,
) error {
	if s == nil || s.isClosed() {
		return store.ErrClosed
	}

	ctx, span := s.tracer.Start(ctx, "systemplane.mongodb.delete_tenant_value")
	defer span.End()

	span.SetAttributes(
		attribute.String("tenant.id", tenantID),
		attribute.String("entry.namespace", namespace),
		attribute.String("entry.key", key),
		attribute.String("entry.actor", actor),
	)

	if tenantID == "" {
		err := errors.New("mongodb delete_tenant_value: tenantID must not be empty")
		opentelemetry.HandleSpanBusinessErrorEvent(span, "validation failed", err)

		return err
	}

	if tenantID == sentinelGlobal {
		err := errors.New("mongodb delete_tenant_value: tenantID must not be the '_global' sentinel")
		opentelemetry.HandleSpanBusinessErrorEvent(span, "validation failed", err)

		return err
	}

	filter := bson.D{{Key: "_id", Value: compoundID{
		Namespace: namespace,
		Key:       key,
		TenantID:  tenantID,
	}}}

	// DeleteOne returns ErrNoDocuments wrapped; we treat a missing row as a
	// no-op so callers can invoke DeleteTenantValue defensively without a
	// pre-check round-trip.
	if _, err := s.coll.DeleteOne(ctx, filter); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil
		}

		opentelemetry.HandleSpanError(span, "mongodb delete_tenant_value: delete failed", err)

		return fmt.Errorf("mongodb store delete_tenant_value: %w", err)
	}

	return nil
}

// ListTenantValues returns every row in the collection, including both the
// "_global" rows and every tenant-specific override. The Client uses this to
// hydrate tenantCache at Start(); callers filter by TenantID as needed.
//
// Returns an empty slice (not nil) when the collection is empty.
func (s *Store) ListTenantValues(ctx context.Context) ([]store.Entry, error) {
	if s == nil || s.isClosed() {
		return nil, store.ErrClosed
	}

	ctx, span := s.tracer.Start(ctx, "systemplane.mongodb.list_tenant_values")
	defer span.End()

	cursor, err := s.coll.Find(ctx, bson.D{})
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

	// Deterministic order: (namespace, key, tenant_id) ascending. Cursor
	// natural order is insertion order on WiredTiger, which is fine for the
	// Client hydrate path but unpredictable for tests. Sort in-process to
	// avoid depending on a compound sort index.
	sort.Slice(docs, func(i, j int) bool {
		if docs[i].Namespace != docs[j].Namespace {
			return docs[i].Namespace < docs[j].Namespace
		}

		if docs[i].Key != docs[j].Key {
			return docs[i].Key < docs[j].Key
		}

		return docs[i].TenantID < docs[j].TenantID
	})

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
		{Key: "namespace", Value: namespace},
		{Key: "key", Value: key},
		{Key: "tenant_id", Value: bson.D{{Key: "$ne", Value: sentinelGlobal}}},
	}

	// Distinct is the natural operator here: one index seek, constant memory,
	// no cursor iteration required. The result is decoded directly into a
	// []string for allocation-free conversion.
	var tenantIDs []string

	if err := s.coll.Distinct(ctx, "tenant_id", filter).Decode(&tenantIDs); err != nil {
		// ErrNoDocuments from a Distinct that matched nothing is benign —
		// return an empty slice rather than propagating.
		if errors.Is(err, mongo.ErrNoDocuments) {
			return []string{}, nil
		}

		opentelemetry.HandleSpanError(span, "mongodb list_tenants_for_key: distinct failed", err)

		return nil, fmt.Errorf("mongodb store list_tenants_for_key: %w", err)
	}

	sort.Strings(tenantIDs)

	if tenantIDs == nil {
		tenantIDs = []string{}
	}

	span.SetAttributes(attribute.Int("tenants.count", len(tenantIDs)))

	return tenantIDs, nil
}
