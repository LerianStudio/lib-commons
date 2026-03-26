package middleware

import (
	"context"

	libCommons "github.com/LerianStudio/lib-commons/v4/commons"
	liblog "github.com/LerianStudio/lib-commons/v4/commons/log"
	libHTTP "github.com/LerianStudio/lib-commons/v4/commons/net/http"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v4/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/logcompat"
	tmmongo "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/mongo"
	tmpostgres "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/postgres"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/tenantcache"
	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
)

// TenantMiddleware extracts tenantId from JWT token and resolves the database connection.
// It stores the connection in context for downstream handlers and repositories.
// Supports PostgreSQL only, MongoDB only, or both databases.
//
// When a TenantCache and TenantLoader are configured, the middleware uses a cache-first
// strategy: it checks the shared cache before resolving connections. On cache miss or
// expiry, the loader fetches the tenant config from tenant-manager and caches it.
// This avoids unnecessary API calls and benefits from event-driven cache updates.
//
// When a module name is configured via WithModule, the middleware stores connections
// with module-aware context keys (ContextWithPG / ContextWithMB) in addition
// to the generic keys. This enables multi-module services where cross-module calls
// need access to connections from different modules simultaneously.
type TenantMiddleware struct {
	postgres *tmpostgres.Manager        // PostgreSQL manager (optional)
	mongo    *tmmongo.Manager           // MongoDB manager (optional)
	cache    *tenantcache.TenantCache   // shared tenant config cache (optional)
	loader   *tenantcache.TenantLoader  // lazy-loads tenant config on cache miss (optional)
	module   string                     // module name for module-aware context injection (optional)
	enabled  bool
}

// TenantMiddlewareOption configures a TenantMiddleware.
type TenantMiddlewareOption func(*TenantMiddleware)

// WithPostgresManager sets the PostgreSQL manager for the tenant middleware.
// When configured, the middleware will resolve PostgreSQL connections for tenants.
func WithPostgresManager(postgres *tmpostgres.Manager) TenantMiddlewareOption {
	return func(m *TenantMiddleware) {
		m.postgres = postgres
		m.enabled = m.postgres != nil || m.mongo != nil
	}
}

// WithMongoManager sets the MongoDB manager for the tenant middleware.
// When configured, the middleware will resolve MongoDB connections for tenants.
func WithMongoManager(mongo *tmmongo.Manager) TenantMiddlewareOption {
	return func(m *TenantMiddleware) {
		m.mongo = mongo
		m.enabled = m.postgres != nil || m.mongo != nil
	}
}

// WithTenantCache sets the shared tenant config cache for the middleware.
// When both cache and loader are configured, the middleware uses a cache-first strategy:
// on cache hit, it skips the loader; on miss or expiry, it calls the loader to fetch and cache.
func WithTenantCache(cache *tenantcache.TenantCache) TenantMiddlewareOption {
	return func(m *TenantMiddleware) {
		m.cache = cache
	}
}

// WithTenantLoader sets the lazy-load tenant loader for the middleware.
// When both cache and loader are configured, the middleware calls the loader on cache miss
// to fetch the tenant config from tenant-manager and cache it for subsequent requests.
func WithTenantLoader(loader *tenantcache.TenantLoader) TenantMiddlewareOption {
	return func(m *TenantMiddleware) {
		m.loader = loader
	}
}

// WithModule sets the module name for module-aware context injection.
// When set, connections are stored with core.ContextWithPG(ctx, module, db)
// and core.ContextWithMB(ctx, module, db) in addition to the generic
// core.ContextWithPGConnection and core.ContextWithMongo.
// This enables multi-module services (e.g., Midaz ledger) where cross-module
// calls need simultaneous access to connections from different modules.
func WithModule(module string) TenantMiddlewareOption {
	return func(m *TenantMiddleware) {
		m.module = module
	}
}

// NewTenantMiddleware creates a new TenantMiddleware with the given options.
// Use WithPostgresManager and/or WithMongoManager to configure which databases to use.
// The middleware is enabled if at least one manager is configured.
//
// Usage examples:
//
//	// PostgreSQL only
//	mid := middleware.NewTenantMiddleware(middleware.WithPostgresManager(pgManager))
//
//	// MongoDB only
//	mid := middleware.NewTenantMiddleware(middleware.WithMongoManager(mongoManager))
//
//	// Both PostgreSQL and MongoDB
//	mid := middleware.NewTenantMiddleware(
//	    middleware.WithPostgresManager(pgManager),
//	    middleware.WithMongoManager(mongoManager),
//	)
func NewTenantMiddleware(opts ...TenantMiddlewareOption) *TenantMiddleware {
	m := &TenantMiddleware{}

	for _, opt := range opts {
		opt(m)
	}

	// Enable if any manager is configured
	m.enabled = m.postgres != nil || m.mongo != nil

	return m
}

// WithTenantDB returns a Fiber handler that extracts tenant context and resolves DB connection.
// It parses the JWT token to get tenantId and fetches the appropriate connection from Tenant Manager.
// The connection is stored in the request context for use by repositories.
//
// Usage in routes.go:
//
//	tenantMid := middleware.NewTenantMiddleware(middleware.WithPostgresManager(pgManager))
//	f.Use(tenantMid.WithTenantDB)
func (m *TenantMiddleware) WithTenantDB(c *fiber.Ctx) error {
	// If middleware is disabled, pass through
	if !m.enabled {
		return c.Next()
	}

	ctx := c.UserContext()

	if ctx == nil {
		ctx = context.Background()
	}

	baseLogger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)
	logger := logcompat.New(baseLogger)

	ctx, span := tracer.Start(ctx, "middleware.tenant.resolve_db")
	defer span.End()

	// Extract JWT token from Authorization header
	accessToken := libHTTP.ExtractTokenFromHeader(c)
	if accessToken == "" {
		logger.ErrorCtx(ctx, "no authorization token - multi-tenant mode requires JWT with tenantId")
		libOpentelemetry.HandleSpanBusinessErrorEvent(span, "missing authorization token",
			core.ErrAuthorizationTokenRequired)

		return unauthorizedError(c, "MISSING_TOKEN", "Authorization token is required")
	}

	// Parse JWT token without signature verification.
	// Token signature is validated by upstream auth middleware before this point.
	token, _, err := new(jwt.Parser).ParseUnverified(accessToken, jwt.MapClaims{})
	if err != nil {
		logger.Base().Log(ctx, liblog.LevelError, "failed to parse JWT token", liblog.Err(err))
		libOpentelemetry.HandleSpanBusinessErrorEvent(span, "failed to parse token", err)

		return unauthorizedError(c, "INVALID_TOKEN", "Failed to parse authorization token")
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		logger.ErrorCtx(ctx, "JWT claims are not in expected format")
		libOpentelemetry.HandleSpanBusinessErrorEvent(span, "invalid claims format",
			core.ErrInvalidTenantClaims)

		return unauthorizedError(c, "INVALID_TOKEN", "JWT claims are not in expected format")
	}

	// Extract tenantId from claims
	tenantID, _ := claims["tenantId"].(string)
	if tenantID == "" {
		logger.ErrorCtx(ctx, "no tenantId in JWT - multi-tenant mode requires tenantId claim")
		libOpentelemetry.HandleSpanBusinessErrorEvent(span, "missing tenantId in JWT",
			core.ErrMissingTenantIDClaim)

		return unauthorizedError(c, "MISSING_TENANT", "tenantId is required in JWT token")
	}

	if !core.IsValidTenantID(tenantID) {
		logger.Base().Log(ctx, liblog.LevelError, "invalid tenantId format in JWT",
			liblog.String("tenant_id", tenantID))
		libOpentelemetry.HandleSpanBusinessErrorEvent(span, "invalid tenantId format",
			core.ErrInvalidTenantClaims)

		return unauthorizedError(c, "INVALID_TENANT", "tenantId has invalid format")
	}

	logger.Base().Log(ctx, liblog.LevelInfo, "tenant context resolved",
		liblog.String("tenant_id", tenantID))

	// Store tenant ID in context
	ctx = core.ContextWithTenantID(ctx, tenantID)

	// Cache-first path: ensure tenant is known before resolving connections.
	if err := m.ensureTenantCached(ctx, tenantID); err != nil {
		logger.Base().Log(ctx, liblog.LevelError, "failed to lazy-load tenant config",
			liblog.String("tenant_id", tenantID), liblog.Err(err))
		libOpentelemetry.HandleSpanError(span, "failed to lazy-load tenant config", err)

		return mapDomainErrorToHTTP(c, err, tenantID)
	}

	// Handle PostgreSQL if manager is configured
	if m.postgres != nil {
		conn, err := m.postgres.GetConnection(ctx, tenantID)
		if err != nil {
			logger.Base().Log(ctx, liblog.LevelError, "failed to get tenant PostgreSQL connection", liblog.Err(err))
			libOpentelemetry.HandleSpanError(span, "failed to get tenant PostgreSQL connection", err)

			return mapDomainErrorToHTTP(c, err, tenantID)
		}

		// Get the database connection from PostgresConnection
		db, err := conn.GetDB()
		if err != nil {
			logger.Base().Log(ctx, liblog.LevelError, "failed to get database from PostgreSQL connection", liblog.Err(err))
			libOpentelemetry.HandleSpanError(span, "failed to get database from PostgreSQL connection", err)

			return internalServerError(c, "TENANT_DB_ERROR", "Failed to get tenant database connection")
		}

		// Store PostgreSQL connection in context (generic key, always set for backward compat)
		ctx = core.ContextWithPGConnection(ctx, db)

		// Store module-specific connection when module is configured
		if m.module != "" {
			ctx = core.ContextWithPG(ctx, m.module, db)
		}
	}

	// Handle MongoDB if manager is configured
	if m.mongo != nil {
		mongoDB, err := m.mongo.GetDatabaseForTenant(ctx, tenantID)
		if err != nil {
			logger.Base().Log(ctx, liblog.LevelError, "failed to get tenant MongoDB connection", liblog.Err(err))
			libOpentelemetry.HandleSpanError(span, "failed to get tenant MongoDB connection", err)

			return mapDomainErrorToHTTP(c, err, tenantID)
		}

		ctx = core.ContextWithMongo(ctx, mongoDB)

		// Store module-specific database when module is configured
		if m.module != "" {
			ctx = core.ContextWithMB(ctx, m.module, mongoDB)
		}
	}

	// Update Fiber context
	c.SetUserContext(ctx)

	return c.Next()
}

// ensureTenantCached checks the shared cache for the tenant. On cache miss or expired entry,
// it calls the loader to fetch and cache the tenant config from tenant-manager.
// Returns nil if cache+loader are not configured (no-op for backward compatibility).
func (m *TenantMiddleware) ensureTenantCached(ctx context.Context, tenantID string) error {
	if m.cache == nil || m.loader == nil {
		return nil
	}

	if _, found := m.cache.Get(tenantID); found {
		return nil
	}

	_, err := m.loader.LoadTenant(ctx, tenantID)

	return err
}

// Enabled returns whether the middleware is enabled.
func (m *TenantMiddleware) Enabled() bool {
	return m.enabled
}
