package middleware

import (
	"context"
	"fmt"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	tmmongo "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/mongo"
	tmpostgres "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/postgres"
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
// ContextWithPG always sets the generic key; with a module it also sets the module-specific key.
func (m *TenantMiddleware) resolvePostgres(ctx context.Context, tenantID string) (context.Context, error) {
	// Multi-module path: iterate all registered modules.
	if len(m.pgModules) > 0 {
		for module, pgMgr := range m.pgModules {
			conn, err := pgMgr.GetConnection(ctx, tenantID)
			if err != nil {
				return ctx, fmt.Errorf("module %s: %w", module, err)
			}

			db, err := conn.GetDB()
			if err != nil {
				return ctx, fmt.Errorf("module %s: failed to get database connection: %w", module, err)
			}

			// Sets both generic and module-specific keys in one call.
			ctx = core.ContextWithPG(ctx, db, module)
		}

		return ctx, nil
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

		ctx = core.ContextWithPG(ctx, db)
	}

	return ctx, nil
}

// resolveMongo resolves MongoDB connections and stores them in context.
// Multi-module path (mongoModules) takes precedence over single-manager path (mongo).
// ContextWithMB always sets the generic key; with a module it also sets the module-specific key.
func (m *TenantMiddleware) resolveMongo(ctx context.Context, tenantID string) (context.Context, error) {
	// Multi-module path: iterate all registered modules.
	if len(m.mongoModules) > 0 {
		for module, mgMgr := range m.mongoModules {
			mongoDB, err := mgMgr.GetDatabaseForTenant(ctx, tenantID)
			if err != nil {
				return ctx, fmt.Errorf("module %s: %w", module, err)
			}

			// Sets both generic and module-specific keys in one call.
			ctx = core.ContextWithMB(ctx, mongoDB, module)
		}

		return ctx, nil
	}

	// Single-manager path (backward compat) -- generic key only.
	if m.mongo != nil {
		mongoDB, err := m.mongo.GetDatabaseForTenant(ctx, tenantID)
		if err != nil {
			return ctx, err
		}

		ctx = core.ContextWithMB(ctx, mongoDB)
	}

	return ctx, nil
}
