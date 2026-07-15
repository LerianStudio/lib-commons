package middleware

import (
	"context"
	"fmt"

	"github.com/LerianStudio/lib-commons/v6/commons/tenant-manager/core"
	tmmongo "github.com/LerianStudio/lib-commons/v6/commons/tenant-manager/mongo"
	tmpostgres "github.com/LerianStudio/lib-commons/v6/commons/tenant-manager/postgres"
)

// WithPG registers a PostgreSQL manager. If module is provided, the connection
// is stored with a module-specific context key in addition to the generic key.
// Multiple calls with different modules register multiple modules.
//
// Single manager:  WithPG(pgManager)
// Multi-module:    WithPG(onboardingPG, "onboarding"), WithPG(transactionPG, "transaction")
func WithPG(p *tmpostgres.Manager, module ...string) TenantMiddlewareOption {
	return func(m *TenantMiddleware) {
		mod := ""
		if len(module) > 0 {
			mod = module[0]
		}

		if mod == "" {
			// No module -- single manager mode (backward compat).
			m.postgres = p
		} else {
			// Module specified -- multi-module mode.
			if m.pgModules == nil {
				m.pgModules = make(map[string]*tmpostgres.Manager)
			}

			m.pgModules[mod] = p
		}

		m.enabled = true
	}
}

// WithMB registers a MongoDB manager. If module is provided, the connection
// is stored with a module-specific context key in addition to the generic key.
// Multiple calls with different modules register multiple modules.
//
// Single manager:  WithMB(mongoManager)
// Multi-module:    WithMB(onboardingMB, "onboarding"), WithMB(transactionMB, "transaction")
func WithMB(mg *tmmongo.Manager, module ...string) TenantMiddlewareOption {
	return func(mw *TenantMiddleware) {
		mod := ""
		if len(module) > 0 {
			mod = module[0]
		}

		if mod == "" {
			// No module -- single manager mode (backward compat).
			mw.mongo = mg
		} else {
			// Module specified -- multi-module mode.
			if mw.mongoModules == nil {
				mw.mongoModules = make(map[string]*tmmongo.Manager)
			}

			mw.mongoModules[mod] = mg
		}

		mw.enabled = true
	}
}

// resolvePostgres resolves PostgreSQL connections and stores them in context.
// Multi-module path (pgModules) takes precedence over single-manager path (postgres).
// With a module, ContextWithPG sets only the module-specific key. When BOTH a
// single-manager (WithPG without module) AND module-specific registrations exist,
// the generic key is also set so generic-key consumers continue to work alongside
// module-keyed ones.
func (m *TenantMiddleware) resolvePostgres(ctx context.Context, tenantID string) (context.Context, error) {
	// Multi-module path: iterate all registered modules.
	if len(m.pgModules) > 0 {
		localCtx := ctx

		for module, pgMgr := range m.pgModules {
			conn, err := pgMgr.GetConnection(localCtx, tenantID)
			if err != nil {
				return ctx, fmt.Errorf("module %s: %w", module, err)
			}

			db, err := conn.GetDB()
			if err != nil {
				return ctx, fmt.Errorf("module %s: failed to get database connection: %w", module, err)
			}

			// Sets only the module-specific key; generic key is not overwritten.
			localCtx = core.ContextWithPG(localCtx, db, module) //nolint:fatcontext // intentional: accumulates module-specific keys across iterations
		}

		// Equalize the generic key when a single-manager registration was also
		// made. Without this, callers registering WithPG(p) + WithPG(p, "module")
		// silently lose the generic key — generic-key consumers
		// (GetPGContext(ctx) without module) get nil and fail downstream.
		if m.postgres != nil {
			conn, err := m.postgres.GetConnection(localCtx, tenantID)
			if err != nil {
				return ctx, fmt.Errorf("generic single-manager: %w", err)
			}

			db, err := conn.GetDB()
			if err != nil {
				return ctx, fmt.Errorf("generic single-manager: failed to get database connection: %w", err)
			}

			localCtx = core.ContextWithPG(localCtx, db)
		}

		return localCtx, nil
	}

	// Single-manager path (backward compat) -- generic key only.
	if m.postgres != nil {
		conn, err := m.postgres.GetConnection(ctx, tenantID)
		if err != nil {
			return ctx, err
		}

		db, err := conn.GetDB()
		if err != nil {
			return ctx, fmt.Errorf("failed to get database connection: %w", err)
		}

		return core.ContextWithPG(ctx, db), nil
	}

	return ctx, nil
}

// resolveMongo resolves MongoDB connections and stores them in context.
// Multi-module path (mongoModules) takes precedence over single-manager path (mongo).
// With a module, ContextWithMB sets only the module-specific key. When BOTH a
// single-manager (WithMB without module) AND module-specific registrations exist,
// the generic key is also set so generic-key consumers continue to work alongside
// module-keyed ones.
func (m *TenantMiddleware) resolveMongo(ctx context.Context, tenantID string) (context.Context, error) {
	// Multi-module path: iterate all registered modules.
	if len(m.mongoModules) > 0 {
		localCtx := ctx

		for module, mgMgr := range m.mongoModules {
			mongoDB, err := mgMgr.GetDatabaseForTenant(localCtx, tenantID)
			if err != nil {
				return ctx, fmt.Errorf("module %s: %w", module, err)
			}

			// Sets only the module-specific key; generic key is not overwritten.
			localCtx = core.ContextWithMB(localCtx, mongoDB, module) //nolint:fatcontext // intentional: accumulates module-specific keys across iterations
		}

		// Equalize the generic key when a single-manager registration was also
		// made. Without this, callers registering WithMB(m) + WithMB(m, "module")
		// silently lose the generic key — generic-key consumers
		// (GetMBContext(ctx) without module) get nil and fail downstream.
		if m.mongo != nil {
			mongoDB, err := m.mongo.GetDatabaseForTenant(localCtx, tenantID)
			if err != nil {
				return ctx, fmt.Errorf("generic single-manager: %w", err)
			}

			localCtx = core.ContextWithMB(localCtx, mongoDB)
		}

		return localCtx, nil
	}

	// Single-manager path (backward compat) -- generic key only.
	if m.mongo != nil {
		mongoDB, err := m.mongo.GetDatabaseForTenant(ctx, tenantID)
		if err != nil {
			return ctx, err
		}

		return core.ContextWithMB(ctx, mongoDB), nil
	}

	return ctx, nil
}
