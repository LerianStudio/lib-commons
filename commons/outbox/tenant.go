package outbox

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
)

type tenantIDContextKey string

// TenantIDContextKey stores tenant id used by outbox multi-tenant operations.
//
// Deprecated: use tenantmanager/core.ContextWithTenantID and tenantmanager/core.GetTenantIDContext.
// This constant will be removed in v3.0.
const TenantIDContextKey tenantIDContextKey = "outbox.tenant_id"

// ErrTenantIDWhitespace is returned when a tenant ID contains leading or
// trailing whitespace. Callers should trim the ID before passing it.
var ErrTenantIDWhitespace = errors.New("tenant ID contains leading or trailing whitespace")

// TenantResolver applies tenant-scoping rules for a transaction.
type TenantResolver interface {
	ApplyTenant(ctx context.Context, tx *sql.Tx, tenantID string) error
}

// TenantDiscoverer lists tenant identifiers to dispatch events for.
type TenantDiscoverer interface {
	DiscoverTenants(ctx context.Context) ([]string, error)
}

// ContextWithTenantID returns a context carrying tenantID.
//
// If the tenant ID contains leading or trailing whitespace, it is trimmed
// before storing. An error is returned alongside the context to signal that
// the caller provided a malformed input.
func ContextWithTenantID(ctx context.Context, tenantID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}

	trimmed := strings.TrimSpace(tenantID)
	if trimmed == "" {
		return ctx
	}

	ctx = core.ContextWithTenantID(ctx, trimmed)

	return context.WithValue(ctx, TenantIDContextKey, trimmed)
}

// ContextWithTenantIDStrict returns a context carrying tenantID.
//
// Unlike ContextWithTenantID, this variant returns an error when the tenant ID
// contains leading or trailing whitespace instead of silently trimming. The
// trimmed value is still stored so the context is usable.
func ContextWithTenantIDStrict(ctx context.Context, tenantID string) (context.Context, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	trimmed := strings.TrimSpace(tenantID)
	if trimmed == "" {
		return ctx, nil
	}

	ctx = core.ContextWithTenantID(ctx, trimmed)
	ctx = context.WithValue(ctx, TenantIDContextKey, trimmed)

	if trimmed != tenantID {
		return ctx, ErrTenantIDWhitespace
	}

	return ctx, nil
}

// TenantIDFromContext reads tenant id from context.
func TenantIDFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}

	tenantID := core.GetTenantIDContext(ctx)

	trimmed := strings.TrimSpace(tenantID)
	if trimmed != "" {
		return trimmed, true
	}

	tenantID, ok := ctx.Value(TenantIDContextKey).(string)
	if !ok {
		return "", false
	}

	trimmed = strings.TrimSpace(tenantID)
	if trimmed == "" {
		return "", false
	}

	return trimmed, true
}
