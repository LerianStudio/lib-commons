package poolmanager

import (
	"context"

	libPostgres "github.com/LerianStudio/lib-commons/v2/commons/postgres"
	"github.com/bxcodec/dbresolver/v2"
)

// Context key types for storing tenant information
type contextKey string

const (
	// tenantIDKey is the context key for storing the tenant ID.
	tenantIDKey contextKey = "tenantID"
	// tenantDBKey is the context key for storing the tenant database connection.
	tenantDBKey contextKey = "tenantDB"
	// tenantPGConnectionKey is the context key for storing the resolved dbresolver.DB connection.
	tenantPGConnectionKey contextKey = "tenantPGConnection"
	// multiTenantModeKey is the context key for indicating multi-tenant mode is enabled.
	multiTenantModeKey contextKey = "multiTenantMode"
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

// SetTenantDBInContext stores the tenant database connection in the context.
func SetTenantDBInContext(ctx context.Context, conn *libPostgres.PostgresConnection) context.Context {
	return context.WithValue(ctx, tenantDBKey, conn)
}

// GetTenantDBFromContext retrieves the tenant database connection from the context.
// Returns nil if not found.
func GetTenantDBFromContext(ctx context.Context) *libPostgres.PostgresConnection {
	if conn, ok := ctx.Value(tenantDBKey).(*libPostgres.PostgresConnection); ok {
		return conn
	}
	return nil
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

// SetMultiTenantModeInContext stores the multi-tenant mode flag in the context.
// This should be set by middleware when Pool Manager is enabled.
func SetMultiTenantModeInContext(ctx context.Context, enabled bool) context.Context {
	return context.WithValue(ctx, multiTenantModeKey, enabled)
}

// IsMultiTenantMode returns true if multi-tenant mode is enabled in the context.
// Returns false if the flag is not set (single-tenant mode).
func IsMultiTenantMode(ctx context.Context) bool {
	if enabled, ok := ctx.Value(multiTenantModeKey).(bool); ok {
		return enabled
	}
	return false
}

// GetDBForTenant returns the database connection for the current tenant from context.
// If no tenant connection is found in context, returns ErrConnectionNotFound.
// For single-tenant mode support, use GetDBForTenantWithFallback instead.
func GetDBForTenant(ctx context.Context) (dbresolver.DB, error) {
	if tenantDB := GetTenantPGConnectionFromContext(ctx); tenantDB != nil {
		return tenantDB, nil
	}

	return nil, ErrConnectionNotFound
}

// GetDBForTenantWithFallback returns the database connection for the current tenant from context.
// If no tenant connection is found in context, the behavior depends on the mode:
//
// Multi-tenant mode (IsMultiTenantMode returns true):
//   - Returns ErrTenantContextRequired if no tenant connection is in context
//   - This ensures every request has proper tenant identification for data isolation
//
// Single-tenant mode (IsMultiTenantMode returns false):
//   - Falls back to the provided default connection
//   - This maintains backward compatibility with single-tenant deployments
func GetDBForTenantWithFallback(ctx context.Context, defaultConn *libPostgres.PostgresConnection) (dbresolver.DB, error) {
	// Try to get tenant connection from context
	if tenantDB := GetTenantPGConnectionFromContext(ctx); tenantDB != nil {
		return tenantDB, nil
	}

	// Check if multi-tenant mode is enabled
	if IsMultiTenantMode(ctx) {
		// In multi-tenant mode, we MUST have tenant context - no fallback allowed
		return nil, ErrTenantContextRequired
	}

	// Single-tenant mode: use fallback connection
	if defaultConn != nil {
		return defaultConn.GetDB()
	}

	return nil, ErrConnectionNotFound
}
