package core

import (
	"context"

	"github.com/bxcodec/dbresolver/v2"
	"go.mongodb.org/mongo-driver/mongo"
)

// nonNilContext returns ctx if non-nil, otherwise context.Background().
// This guards every exported setter/getter against nil-context panics.
func nonNilContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}

	return ctx
}

// Context key types for storing tenant information.
// Use unexported struct keys to avoid collisions across packages.
type contextKey struct {
	name string
}

var (
	// tenantIDKey is the context key for storing the tenant ID.
	tenantIDKey = contextKey{name: "tenantID"}
	// tenantPGConnectionKey is the context key for storing the resolved dbresolver.DB connection.
	tenantPGConnectionKey = contextKey{name: "tenantPGConnection"}
	// tenantMongoKey is the context key for storing the tenant MongoDB database.
	tenantMongoKey = contextKey{name: "tenantMongo"}
)

// SetTenantIDInContext stores the tenant ID in the context.
func SetTenantIDInContext(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(nonNilContext(ctx), tenantIDKey, tenantID)
}

// GetTenantIDFromContext retrieves the tenant ID from the context.
// Returns empty string if not found.
func GetTenantIDFromContext(ctx context.Context) string {
	if id, ok := nonNilContext(ctx).Value(tenantIDKey).(string); ok {
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
	return context.WithValue(nonNilContext(ctx), tenantPGConnectionKey, db)
}

// GetTenantPGConnectionFromContext retrieves the resolved dbresolver.DB from the context.
// Returns nil if not found.
func GetTenantPGConnectionFromContext(ctx context.Context) dbresolver.DB {
	if db, ok := nonNilContext(ctx).Value(tenantPGConnectionKey).(dbresolver.DB); ok {
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

// ContextWithTenantMongo stores the MongoDB database in the context.
func ContextWithTenantMongo(ctx context.Context, db *mongo.Database) context.Context {
	return context.WithValue(nonNilContext(ctx), tenantMongoKey, db)
}

// GetMongoFromContext retrieves the MongoDB database from the context.
// Returns nil if not found.
func GetMongoFromContext(ctx context.Context) *mongo.Database {
	if db, ok := nonNilContext(ctx).Value(tenantMongoKey).(*mongo.Database); ok {
		return db
	}

	return nil
}

// GetMongoForTenant returns the MongoDB database for the current tenant from context.
// If no tenant connection is found in context, returns ErrTenantContextRequired.
// This function ALWAYS requires tenant context - there is no fallback to default connections.
func GetMongoForTenant(ctx context.Context) (*mongo.Database, error) {
	if db := GetMongoFromContext(ctx); db != nil {
		return db, nil
	}

	return nil, ErrTenantContextRequired
}
