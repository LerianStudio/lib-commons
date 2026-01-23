package tenantmanager

import (
	"context"

	"github.com/bxcodec/dbresolver/v2"
)

// Context key types for storing tenant information
type contextKey string

const (
	// tenantIDKey is the context key for storing the tenant ID.
	tenantIDKey contextKey = "tenantID"
	// tenantPGConnectionKey is the context key for storing the resolved dbresolver.DB connection.
	tenantPGConnectionKey contextKey = "tenantPGConnection"
)

// SetTenantIDInContext stores the tenant ID in the context.
func SetTenantIDInContext(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, tenantIDKey, tenantID)
}

// GetTenantIDFromContext retrieves the tenant ID from the context.
// Returns empty string if not found.
func GetTenantIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(tenantIDKey).(string); ok {
		return id
	}
	return ""
}

// GetTenantID is an alias for GetTenantIDFromContext.
// Returns the tenant ID from context, or empty string if not found.
func GetTenantID(ctx context.Context) string {
	return GetTenantIDFromContext(ctx)
}

// HasTenantContext returns true if the context has tenant information.
func HasTenantContext(ctx context.Context) bool {
	return GetTenantIDFromContext(ctx) != ""
}

// ContextWithTenantID stores the tenant ID in the context.
// Alias for SetTenantIDInContext for compatibility with middleware.
func ContextWithTenantID(ctx context.Context, tenantID string) context.Context {
	return SetTenantIDInContext(ctx, tenantID)
}

// ContextWithTenantPGConnection stores the resolved dbresolver.DB connection in the context.
// This is used by the middleware to store the tenant-specific database connection.
func ContextWithTenantPGConnection(ctx context.Context, db dbresolver.DB) context.Context {
	return context.WithValue(ctx, tenantPGConnectionKey, db)
}

// GetTenantPGConnectionFromContext retrieves the resolved dbresolver.DB from the context.
// Returns nil if not found.
func GetTenantPGConnectionFromContext(ctx context.Context) dbresolver.DB {
	if db, ok := ctx.Value(tenantPGConnectionKey).(dbresolver.DB); ok {
		return db
	}
	return nil
}

// GetPostgresForTenant returns the PostgreSQL database connection for the current tenant from context.
// If no tenant connection is found in context, returns ErrTenantContextRequired.
// This function ALWAYS requires tenant context - there is no fallback to default connections.
func GetPostgresForTenant(ctx context.Context) (dbresolver.DB, error) {
	if tenantDB := GetTenantPGConnectionFromContext(ctx); tenantDB != nil {
		return tenantDB, nil
	}

	return nil, ErrTenantContextRequired
}
