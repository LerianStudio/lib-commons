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

	// Module-specific PostgreSQL connection keys for multi-tenant unified mode.
	// These keys allow each module to have its own database connection in context,
	// solving the issue where in-process calls between modules would get the wrong connection.

	// tenantOnboardingPGConnectionKey is the context key for storing the onboarding module's PostgreSQL connection.
	tenantOnboardingPGConnectionKey contextKey = "tenantOnboardingPGConnection"
	// tenantTransactionPGConnectionKey is the context key for storing the transaction module's PostgreSQL connection.
	tenantTransactionPGConnectionKey contextKey = "tenantTransactionPGConnection"
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

// ContextWithOnboardingPGConnection stores the onboarding module's PostgreSQL connection in context.
// This is used in multi-tenant unified mode where multiple modules run in the same process
// and each module needs its own database connection.
func ContextWithOnboardingPGConnection(ctx context.Context, db dbresolver.DB) context.Context {
	return context.WithValue(ctx, tenantOnboardingPGConnectionKey, db)
}

// ContextWithTransactionPGConnection stores the transaction module's PostgreSQL connection in context.
// This is used in multi-tenant unified mode where multiple modules run in the same process
// and each module needs its own database connection.
func ContextWithTransactionPGConnection(ctx context.Context, db dbresolver.DB) context.Context {
	return context.WithValue(ctx, tenantTransactionPGConnectionKey, db)
}

// GetOnboardingPostgresForTenant returns the onboarding PostgreSQL connection from context.
// Returns ErrTenantContextRequired if not found.
// This function does NOT fallback to the generic tenantPGConnectionKey - it strictly returns
// only the module-specific connection. This ensures proper isolation in multi-tenant unified mode.
func GetOnboardingPostgresForTenant(ctx context.Context) (dbresolver.DB, error) {
	if db, ok := ctx.Value(tenantOnboardingPGConnectionKey).(dbresolver.DB); ok && db != nil {
		return db, nil
	}

	return nil, ErrTenantContextRequired
}

// GetTransactionPostgresForTenant returns the transaction PostgreSQL connection from context.
// Returns ErrTenantContextRequired if not found.
// This function does NOT fallback to the generic tenantPGConnectionKey - it strictly returns
// only the module-specific connection. This ensures proper isolation in multi-tenant unified mode.
func GetTransactionPostgresForTenant(ctx context.Context) (dbresolver.DB, error) {
	if db, ok := ctx.Value(tenantTransactionPGConnectionKey).(dbresolver.DB); ok && db != nil {
		return db, nil
	}

	return nil, ErrTenantContextRequired
}
