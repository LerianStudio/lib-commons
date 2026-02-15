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

// moduleContextKey generates a dynamic context key for a given module name.
// This allows any module to store its own PostgreSQL connection in context
// without requiring changes to lib-commons.
func moduleContextKey(moduleName string) contextKey {
	return contextKey("tenantPGConnection:" + moduleName)
}

// ContextWithModulePGConnection stores a module-specific PostgreSQL connection in context.
// moduleName identifies the module (e.g., "onboarding", "transaction").
// This is used in multi-module processes where each module needs its own database connection
// in context to avoid cross-module conflicts.
func ContextWithModulePGConnection(ctx context.Context, moduleName string, db dbresolver.DB) context.Context {
	return context.WithValue(ctx, moduleContextKey(moduleName), db)
}

// GetModulePostgresForTenant returns the module-specific PostgreSQL connection from context.
// moduleName identifies the module (e.g., "onboarding", "transaction").
// Returns ErrTenantContextRequired if no connection is found for the given module.
// This function does NOT fallback to the generic tenantPGConnectionKey.
func GetModulePostgresForTenant(ctx context.Context, moduleName string) (dbresolver.DB, error) {
	if db, ok := ctx.Value(moduleContextKey(moduleName)).(dbresolver.DB); ok && db != nil {
		return db, nil
	}

	return nil, ErrTenantContextRequired
}

// Deprecated: Use ContextWithModulePGConnection(ctx, "onboarding", db) instead.
func ContextWithOnboardingPGConnection(ctx context.Context, db dbresolver.DB) context.Context {
	return ContextWithModulePGConnection(ctx, "onboarding", db)
}

// Deprecated: Use ContextWithModulePGConnection(ctx, "transaction", db) instead.
func ContextWithTransactionPGConnection(ctx context.Context, db dbresolver.DB) context.Context {
	return ContextWithModulePGConnection(ctx, "transaction", db)
}

// Deprecated: Use GetModulePostgresForTenant(ctx, "onboarding") instead.
func GetOnboardingPostgresForTenant(ctx context.Context) (dbresolver.DB, error) {
	return GetModulePostgresForTenant(ctx, "onboarding")
}

// Deprecated: Use GetModulePostgresForTenant(ctx, "transaction") instead.
func GetTransactionPostgresForTenant(ctx context.Context) (dbresolver.DB, error) {
	return GetModulePostgresForTenant(ctx, "transaction")
}
