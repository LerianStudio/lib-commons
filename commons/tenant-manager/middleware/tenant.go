package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"

	libCommons "github.com/LerianStudio/lib-commons/v4/commons"
	liblog "github.com/LerianStudio/lib-commons/v4/commons/log"
	libHTTP "github.com/LerianStudio/lib-commons/v4/commons/net/http"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v4/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/logcompat"
	tmmongo "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/mongo"
	tmpostgres "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/postgres"
	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
)

// TenantMiddleware extracts tenantId from JWT token and resolves the database connection.
// It stores the connection in context for downstream handlers and repositories.
// Supports PostgreSQL only, MongoDB only, or both databases.
type TenantMiddleware struct {
	postgres *tmpostgres.Manager // PostgreSQL manager (optional)
	mongo    *tmmongo.Manager    // MongoDB manager (optional)
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

// WithTenantDB returns a Fiber handler that extracts tenant context and resolves
// DB connection.  It parses the JWT token to get tenantId and fetches the
// appropriate connection from Tenant Manager.  The connection is stored in the
// request context for use by repositories.
//
// # Upstream Authentication Requirement (v4 migration)
//
// Starting with lib-commons v4, WithTenantDB requires that an upstream
// authentication middleware has already verified the request before tenant
// resolution occurs.  The middleware detects this by checking Fiber locals:
//
//  1. Preferred: c.Locals("auth_verified", true) — set by the upstream auth
//     middleware after successful signature verification.
//  2. Fallback heuristic: any of "user_id", "userID", "user", "claims", or
//     "jwt" set to a non-empty, non-nil value.
//
// If neither signal is found, WithTenantDB returns 401 Unauthorized.
//
// Migration from lib-commons v3: add [SetAuthVerified] to your existing auth
// middleware's success path:
//
//	func AuthMiddleware(c *fiber.Ctx) error {
//	    // ... verify JWT signature ...
//	    middleware.SetAuthVerified(c)
//	    return c.Next()
//	}
//
// Usage in routes.go:
//
//	f.Use(authMiddleware)   // must run first
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

	_, tracer, _, _ := libCommons.NewTrackingFromContext(ctx) //nolint:dogsled
	logger := logcompat.Prefer(nil, logcompat.FromContext(ctx))

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

	if !hasUpstreamAuthAssertion(c) {
		logger.ErrorCtx(ctx, "missing upstream auth assertion before tenant middleware")
		libOpentelemetry.HandleSpanBusinessErrorEvent(span, "missing upstream auth assertion",
			core.ErrInvalidAuthorizationToken)

		return unauthorizedError(c, "UNAUTHORIZED", "Unauthorized")
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

	// Handle PostgreSQL if manager is configured
	if m.postgres != nil {
		conn, err := m.postgres.GetConnection(ctx, tenantID)
		if err != nil {
			logger.Base().Log(ctx, liblog.LevelError, "failed to get tenant PostgreSQL connection", liblog.Err(err))
			libOpentelemetry.HandleSpanError(span, "failed to get tenant PostgreSQL connection", err)

			return mapDomainErrorToHTTP(c, err)
		}

		// Get the database connection from PostgresConnection
		db, err := conn.GetDB()
		if err != nil {
			logger.Base().Log(ctx, liblog.LevelError, "failed to get database from PostgreSQL connection", liblog.Err(err))
			libOpentelemetry.HandleSpanError(span, "failed to get database from PostgreSQL connection", err)

			return internalServerError(c, "TENANT_DB_ERROR", "Failed to get tenant database connection")
		}

		// Store PostgreSQL connection in context
		ctx = core.ContextWithTenantPGConnection(ctx, db)
	}

	// Handle MongoDB if manager is configured
	if m.mongo != nil {
		mongoDB, err := m.mongo.GetDatabaseForTenant(ctx, tenantID)
		if err != nil {
			logger.Base().Log(ctx, liblog.LevelError, "failed to get tenant MongoDB connection", liblog.Err(err))
			libOpentelemetry.HandleSpanError(span, "failed to get tenant MongoDB connection", err)

			return mapDomainErrorToHTTP(c, err)
		}

		ctx = core.ContextWithTenantMongo(ctx, mongoDB)
	}

	// Update Fiber context
	c.SetUserContext(ctx)

	return c.Next()
}

// mapDomainErrorToHTTP is a centralized error-to-HTTP mapping function shared by
// both TenantMiddleware and MultiPoolMiddleware to ensure consistent status codes
// for the same domain errors.
func mapDomainErrorToHTTP(c *fiber.Ctx, err error) error {
	// Missing token or JWT errors -> 401
	if errors.Is(err, core.ErrAuthorizationTokenRequired) ||
		errors.Is(err, core.ErrInvalidAuthorizationToken) ||
		errors.Is(err, core.ErrInvalidTenantClaims) ||
		errors.Is(err, core.ErrMissingTenantIDClaim) {
		return unauthorizedError(c, "UNAUTHORIZED", "Unauthorized")
	}

	// Tenant not found -> 404
	if errors.Is(err, core.ErrTenantNotFound) {
		return c.Status(http.StatusNotFound).JSON(fiber.Map{
			"code":    "TENANT_NOT_FOUND",
			"title":   "Not Found",
			"message": "The requested tenant was not found",
		})
	}

	// Tenant suspended/purged -> 403
	var suspErr *core.TenantSuspendedError
	if errors.As(err, &suspErr) {
		return forbiddenError(c, "0131", "Service Suspended",
			"tenant service is "+suspErr.Status)
	}

	// Generic access denied (403 without parsed status) -> 403
	if errors.Is(err, core.ErrTenantServiceAccessDenied) {
		return forbiddenError(c, "0131", "Access Denied",
			"tenant service access denied")
	}

	// Manager closed or service not configured -> 503
	if errors.Is(err, core.ErrManagerClosed) || errors.Is(err, core.ErrServiceNotConfigured) {
		return c.Status(http.StatusServiceUnavailable).JSON(fiber.Map{
			"code":    "SERVICE_UNAVAILABLE",
			"title":   "Service Unavailable",
			"message": "Service temporarily unavailable",
		})
	}

	// Circuit breaker open -> 503
	if errors.Is(err, core.ErrCircuitBreakerOpen) {
		return c.Status(http.StatusServiceUnavailable).JSON(fiber.Map{
			"code":    "SERVICE_UNAVAILABLE",
			"title":   "Service Unavailable",
			"message": "Service temporarily unavailable",
		})
	}

	// Connection errors -> 503
	if errors.Is(err, core.ErrConnectionFailed) {
		return c.Status(http.StatusServiceUnavailable).JSON(fiber.Map{
			"code":    "SERVICE_UNAVAILABLE",
			"title":   "Service Unavailable",
			"message": "Failed to resolve tenant database",
		})
	}

	// Default -> 500
	return internalServerError(c, "TENANT_DB_ERROR", "Failed to resolve tenant database")
}

// forbiddenError sends an HTTP 403 Forbidden response.
// Used when the tenant-service association exists but is not active (suspended or purged).
func forbiddenError(c *fiber.Ctx, code, title, message string) error {
	return c.Status(http.StatusForbidden).JSON(fiber.Map{
		"code":    code,
		"title":   title,
		"message": message,
	})
}

// internalServerError sends an HTTP 500 Internal Server Error response.
func internalServerError(c *fiber.Ctx, code, title string) error {
	return c.Status(http.StatusInternalServerError).JSON(fiber.Map{
		"code":    code,
		"title":   title,
		"message": "Internal server error",
	})
}

// unauthorizedError sends an HTTP 401 Unauthorized response.
func unauthorizedError(c *fiber.Ctx, code, message string) error {
	return c.Status(http.StatusUnauthorized).JSON(fiber.Map{
		"code":    code,
		"title":   "Unauthorized",
		"message": message,
	})
}

// SetAuthVerified marks the Fiber context as having passed upstream
// authentication.  Call this in your auth middleware after successful JWT
// signature verification so that [TenantMiddleware.WithTenantDB] and
// [MultiPoolMiddleware] accept the request.
//
// This is the canonical way to satisfy the upstream auth assertion introduced
// in lib-commons v4.  See the migration notes on [TenantMiddleware.WithTenantDB].
func SetAuthVerified(c *fiber.Ctx) {
	if c != nil {
		c.Locals("auth_verified", true)
	}
}

func hasUpstreamAuthAssertion(c *fiber.Ctx) bool {
	if c == nil {
		return false
	}

	if verified, ok := c.Locals("auth_verified").(bool); ok {
		return verified
	}

	keys := []string{"user_id", "userID", "user", "claims", "jwt"}
	for _, key := range keys {
		value := c.Locals(key)
		if value == nil {
			continue
		}

		if stringValue, ok := value.(string); ok && strings.TrimSpace(stringValue) == "" {
			continue
		}

		// Reject boolean false stored by upstream middleware that explicitly
		// signals "authentication was attempted but failed".
		if boolValue, ok := value.(bool); ok && !boolValue {
			continue
		}

		return true
	}

	return false
}

// Enabled returns whether the middleware is enabled.
func (m *TenantMiddleware) Enabled() bool {
	return m.enabled
}
