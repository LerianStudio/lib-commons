package core

import (
	"context"
	"strings"

	"github.com/bxcodec/dbresolver/v2"
	"go.mongodb.org/mongo-driver/mongo"
)

// PostgresFallback abstracts the static PostgreSQL connection used as fallback
// when no tenant-specific connection is found in context.
type PostgresFallback interface {
	GetDB() (dbresolver.DB, error)
}

// MongoFallback abstracts the static MongoDB connection used as fallback
// when no tenant-specific connection is found in context.
type MongoFallback interface {
	GetDB(ctx context.Context) (*mongo.Client, error)
}

// MultiTenantChecker is implemented by managers that know whether they are
// running in multi-tenant mode. The postgres, mongo and rabbitmq managers
// already satisfy this interface via their IsMultiTenant() method.
type MultiTenantChecker interface {
	IsMultiTenant() bool
}

// Context key types for storing tenant information
type contextKey string

const (
	// tenantIDKey is the context key for storing the tenant ID.
	tenantIDKey contextKey = "tenantID"
	// tenantPGConnectionKey is the context key for storing the resolved dbresolver.DB connection.
	tenantPGConnectionKey contextKey = "tenantPGConnection"
	// tenantMongoKey is the context key for storing the tenant MongoDB database.
	tenantMongoKey contextKey = "tenantMongo"
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

// ResolvePostgres returns the PostgreSQL connection from context (multi-tenant)
// or falls back to the static connection (single-tenant).
// When the fallback implements MultiTenantChecker and reports multi-tenant mode,
// the function returns ErrTenantContextRequired instead of falling back silently.
func ResolvePostgres(ctx context.Context, fallback PostgresFallback) (dbresolver.DB, error) {
	if db := GetTenantPGConnectionFromContext(ctx); db != nil {
		return db, nil
	}

	if checker, ok := fallback.(MultiTenantChecker); ok && checker.IsMultiTenant() {
		return nil, ErrTenantContextRequired
	}

	return fallback.GetDB()
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

// ResolveModuleDB returns the module-specific PostgreSQL connection from context (multi-tenant)
// or falls back to the static connection (single-tenant).
// moduleName identifies the module (e.g., "onboarding", "transaction").
// When the fallback implements MultiTenantChecker and reports multi-tenant mode,
// the function returns ErrTenantContextRequired instead of falling back silently.
func ResolveModuleDB(ctx context.Context, moduleName string, fallback PostgresFallback) (dbresolver.DB, error) {
	if db, ok := ctx.Value(moduleContextKey(moduleName)).(dbresolver.DB); ok && db != nil {
		return db, nil
	}

	if checker, ok := fallback.(MultiTenantChecker); ok && checker.IsMultiTenant() {
		return nil, ErrTenantContextRequired
	}

	return fallback.GetDB()
}

// ContextWithTenantMongo stores the MongoDB database in the context.
func ContextWithTenantMongo(ctx context.Context, db *mongo.Database) context.Context {
	return context.WithValue(ctx, tenantMongoKey, db)
}

// GetMongoFromContext retrieves the MongoDB database from the context.
// Returns nil if not found.
func GetMongoFromContext(ctx context.Context) *mongo.Database {
	if db, ok := ctx.Value(tenantMongoKey).(*mongo.Database); ok {
		return db
	}

	return nil
}

// ResolveMongo returns the MongoDB database from context (multi-tenant)
// or falls back to the static connection (single-tenant).
// When the fallback implements MultiTenantChecker and reports multi-tenant mode,
// the function returns ErrTenantContextRequired instead of falling back silently.
func ResolveMongo(ctx context.Context, fallback MongoFallback, dbName string) (*mongo.Database, error) {
	if db, ok := ctx.Value(tenantMongoKey).(*mongo.Database); ok && db != nil {
		return db, nil
	}

	if checker, ok := fallback.(MultiTenantChecker); ok && checker.IsMultiTenant() {
		return nil, ErrTenantContextRequired
	}

	client, err := fallback.GetDB(ctx)
	if err != nil {
		return nil, err
	}

	return client.Database(strings.ToLower(dbName)), nil
}
