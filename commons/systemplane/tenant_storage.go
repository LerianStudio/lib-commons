// Thin dispatch layer around store.Store tenant methods.
//
// Responsibilities:
//   - Wrap each backend call in an OTEL span with tenant/namespace/key
//     attributes.
//   - Apply the canonical JSON round-trip on writes so the post-write cache
//     and the changefeed-refresh cache agree on type (see set.go:70-78 for the
//     legacy global-path precedent).
//   - Wrap errors with a consistent systemplane: prefix so callers can tell
//     at a glance which layer produced the failure.
//
// Non-responsibilities: this layer does NOT touch the registry, the cache,
// or the subscriber list. Those concerns live in the method handlers in
// tenant_scoped.go / refresh.go. Keeping this file narrow makes it trivial
// to reason about (and test) the "call the backend with spans" glue in
// isolation from the policy code above it.
package systemplane

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/store"
	"go.opentelemetry.io/otel/attribute"
)

// persistTenantValue marshals value to JSON, invokes store.SetTenantValue
// under a span, and returns the canonical form (JSON round-tripped value)
// alongside any error. The canonical form exists so the caller can write it
// into tenantCache without risking a type mismatch with what the changefeed
// refresh will later decode from the same bytes.
//
// The span always ends before return — the caller does not defer anything.
// If telemetry is unconfigured the span is a no-op (startSpan's contract).
func (c *Client) persistTenantValue(ctx context.Context, tenantID string, e store.Entry, value any) (canonical any, err error) {
	ctx, span, finish := c.startSpanWithAttrs(ctx, "systemplane.client.set_tenant_value",
		attribute.String("tenant.id", tenantID),
		attribute.String("systemplane.namespace", e.Namespace),
		attribute.String("systemplane.key", e.Key),
	)
	defer finish()

	// Marshal once; reuse bytes for both the store call and the canonical
	// round-trip. This matches set.go's pattern and avoids a double-encode.
	jsonBytes, err := json.Marshal(value)
	if err != nil {
		opentelemetry.HandleSpanError(span, "json marshal failed", err)

		return nil, fmt.Errorf("%w: value is not JSON-serializable: %w", ErrValidation, err)
	}

	e.Value = jsonBytes

	if e.UpdatedAt.IsZero() {
		e.UpdatedAt = time.Now().UTC()
	}

	if err := c.store.SetTenantValue(ctx, tenantID, e); err != nil {
		opentelemetry.HandleSpanError(span, "store set_tenant_value failed", err)

		return nil, fmt.Errorf("systemplane: SetTenantValue: %w", err)
	}

	// Canonical round-trip: unmarshal the bytes we just wrote so the cache
	// holds the same Go type a changefeed refresh would produce (e.g.
	// float64 rather than int for JSON numbers). Mirrors set.go:70-78.
	if err := json.Unmarshal(jsonBytes, &canonical); err != nil {
		// Should be unreachable since we just marshaled this value, but if
		// the value somehow fails round-trip we prefer to cache the raw
		// input over leaving the cache stale.
		canonical = value
	}

	return canonical, nil
}

// fetchTenantValue wraps store.GetTenantValue with a span and consistent
// error prefix. It returns the decoded Go value, a found flag, and any
// error. A (nil, false, nil) return means "no tenant override for this
// key" — the caller is responsible for the fallback to the global cache
// or registered default (TRD §4.2).
func (c *Client) fetchTenantValue(ctx context.Context, tenantID, namespace, key string) (any, bool, error) {
	ctx, span, finish := c.startSpanWithAttrs(ctx, "systemplane.client.get_tenant_value",
		attribute.String("tenant.id", tenantID),
		attribute.String("systemplane.namespace", namespace),
		attribute.String("systemplane.key", key),
	)
	defer finish()

	entry, found, err := c.store.GetTenantValue(ctx, tenantID, namespace, key)
	if err != nil {
		opentelemetry.HandleSpanError(span, "store get_tenant_value failed", err)

		return nil, false, fmt.Errorf("systemplane: GetTenantValue: %w", err)
	}

	if !found {
		return nil, false, nil
	}

	var decoded any
	if err := json.Unmarshal(entry.Value, &decoded); err != nil {
		opentelemetry.HandleSpanError(span, "json unmarshal failed", err)

		return nil, false, fmt.Errorf("systemplane: GetTenantValue: decode: %w", err)
	}

	return decoded, true, nil
}

// removeTenantValue wraps store.DeleteTenantValue with a span and consistent
// error prefix. Delete is idempotent at the backend (no error on missing
// row), so this wrapper does not distinguish "not found" from "deleted".
func (c *Client) removeTenantValue(ctx context.Context, tenantID, namespace, key, actor string) error {
	ctx, span, finish := c.startSpanWithAttrs(ctx, "systemplane.client.delete_tenant_value",
		attribute.String("tenant.id", tenantID),
		attribute.String("systemplane.namespace", namespace),
		attribute.String("systemplane.key", key),
		attribute.String("systemplane.actor", actor),
	)
	defer finish()

	if err := c.store.DeleteTenantValue(ctx, tenantID, namespace, key, actor); err != nil {
		opentelemetry.HandleSpanError(span, "store delete_tenant_value failed", err)

		return fmt.Errorf("systemplane: DeleteTenantValue: %w", err)
	}

	return nil
}

// listTenantsForKey wraps store.ListTenantsForKey with a span and consistent
// error prefix. The backend guarantees a sorted, deduplicated slice with the
// '_global' sentinel excluded AND a non-nil empty slice on no-match, so this
// wrapper does not re-coerce a nil return. A nil return from the store would be
// a contract violation, not something the dispatch layer should silently paper
// over.
func (c *Client) listTenantsForKey(ctx context.Context, namespace, key string) ([]string, error) {
	ctx, span, finish := c.startSpanWithAttrs(ctx, "systemplane.client.list_tenants_for_key",
		attribute.String("systemplane.namespace", namespace),
		attribute.String("systemplane.key", key),
	)
	defer finish()

	tenants, err := c.store.ListTenantsForKey(ctx, namespace, key)
	if err != nil {
		opentelemetry.HandleSpanError(span, "store list_tenants_for_key failed", err)

		return nil, fmt.Errorf("systemplane: ListTenantsForKey: %w", err)
	}

	return tenants, nil
}
