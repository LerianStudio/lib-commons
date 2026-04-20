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

// tenantCtxKey is the *pointer-valued* type for the tenant ID context key.
// A pointer key is used (instead of a struct value like contextKey) so that
// context.Context.Value lookups on the tenant ID — which sit on the hot
// read path — do not box the key into a heap-allocated any{} wrapper on
// every call. With a struct-value key, the compiler spills the boxed key
// to heap per call (16 B/op, ~12 ns/op overhead); a pointer key reuses the
// single package-level address and the boxed any is stack-friendly (0 B/op,
// ~2 ns/op). See the AC15 perf gate for the regression that drove this.
type tenantCtxKey struct {
	name string
}

var (
	// tenantIDKey is the context key for storing the tenant ID.
	// Pointer-valued to keep ctx.Value lookups allocation-free on the hot path.
	tenantIDKey = &tenantCtxKey{name: "tenantID"}
	// pgConnectionKey is the context key for storing the resolved dbresolver.DB connection.
	pgConnectionKey = contextKey{name: "pgConnection"}
	// mongoKey is the context key for storing the tenant MongoDB database.
	mongoKey = contextKey{name: "mongo"}
)

// ContextWithTenantID stores the tenant ID in the context.
func ContextWithTenantID(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(nonNilContext(ctx), tenantIDKey, tenantID)
}

// GetTenantIDContext retrieves the tenant ID from the context.
// Returns empty string if not found (including when ctx is nil).
//
// The nil-ctx guard is inlined here rather than delegated to nonNilContext
// because escape analysis cannot prove the fresh context.Background() value
// is unreachable after inlining, so the construction is spilled to heap on
// every call — a 16 B/op alloc the hot path cannot afford. Handling nil
// explicitly keeps GetTenantIDContext allocation-free. See bench_tenant_test.go
// (AC15 perf gate) for the measurement that caught this regression.
func GetTenantIDContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}

	if id, ok := ctx.Value(tenantIDKey).(string); ok {
		return id
	}

	return ""
}

// pgModuleKey is a context key type for module-specific PostgreSQL connections.
// Each module name produces a distinct key, so connections for different modules
// (e.g., "onboarding", "transaction") do not collide in the same context.
type pgModuleKey string

// ContextWithPG stores a PostgreSQL connection in the context.
// Without a module argument it sets only the generic connection key.
// With a module argument it sets only the module-specific key, so
// module-specific stores do not overwrite the generic key set by
// a different call (e.g., a different module or the single-manager path).
//
// Examples:
//
//	ctx = ContextWithPG(ctx, db)                   // generic only
//	ctx = ContextWithPG(ctx, db, "onboarding")     // module-specific only
func ContextWithPG(ctx context.Context, db dbresolver.DB, module ...string) context.Context {
	ctx = nonNilContext(ctx)

	if len(module) > 0 && module[0] != "" {
		return context.WithValue(ctx, pgModuleKey(module[0]), db)
	}

	return context.WithValue(ctx, pgConnectionKey, db)
}

// GetPGContext retrieves a PostgreSQL connection from the context.
// Without a module argument it reads the generic key.
// With a module argument it reads the module-specific key.
//
// Examples:
//
//	db := GetPGContext(ctx)                // generic
//	db := GetPGContext(ctx, "onboarding")  // module-specific
func GetPGContext(ctx context.Context, module ...string) dbresolver.DB {
	if len(module) > 0 && module[0] != "" {
		if db, ok := nonNilContext(ctx).Value(pgModuleKey(module[0])).(dbresolver.DB); ok {
			return db
		}

		return nil
	}

	if db, ok := nonNilContext(ctx).Value(pgConnectionKey).(dbresolver.DB); ok {
		return db
	}

	return nil
}

// mongoModuleKey is a context key type for module-specific MongoDB databases.
// Each module name produces a distinct key, so databases for different modules
// do not collide in the same context.
type mongoModuleKey string

// ContextWithMB stores a MongoDB database in the context.
// Without a module argument it sets only the generic MongoDB key.
// With a module argument it sets only the module-specific key, so
// module-specific stores do not overwrite the generic key set by
// a different call (e.g., a different module or the single-manager path).
//
// Examples:
//
//	ctx = ContextWithMB(ctx, db)                   // generic only
//	ctx = ContextWithMB(ctx, db, "onboarding")     // module-specific only
func ContextWithMB(ctx context.Context, db *mongo.Database, module ...string) context.Context {
	ctx = nonNilContext(ctx)

	if len(module) > 0 && module[0] != "" {
		return context.WithValue(ctx, mongoModuleKey(module[0]), db)
	}

	return context.WithValue(ctx, mongoKey, db)
}

// GetMBContext retrieves a MongoDB database from the context.
// Without a module argument it reads the generic key.
// With a module argument it reads the module-specific key.
//
// Examples:
//
//	db := GetMBContext(ctx)                // generic
//	db := GetMBContext(ctx, "onboarding")  // module-specific
func GetMBContext(ctx context.Context, module ...string) *mongo.Database {
	if len(module) > 0 && module[0] != "" {
		if db, ok := nonNilContext(ctx).Value(mongoModuleKey(module[0])).(*mongo.Database); ok {
			return db
		}

		return nil
	}

	if db, ok := nonNilContext(ctx).Value(mongoKey).(*mongo.Database); ok {
		return db
	}

	return nil
}
