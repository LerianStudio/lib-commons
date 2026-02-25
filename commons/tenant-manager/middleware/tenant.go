package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	libCommons "github.com/LerianStudio/lib-commons/v3/commons"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v3/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v3/commons/tenant-manager/core"
	tmmongo "github.com/LerianStudio/lib-commons/v3/commons/tenant-manager/mongo"
	tmpostgres "github.com/LerianStudio/lib-commons/v3/commons/tenant-manager/postgres"
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

	logger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)

	ctx, span := tracer.Start(ctx, "middleware.tenant.resolve_db")
	defer span.End()

	// Extract JWT token from Authorization header
	accessToken := extractTokenFromHeader(c)
	if accessToken == "" {
		logger.Errorf("no authorization token - multi-tenant mode requires JWT with tenantId")
		libOpentelemetry.HandleSpanBusinessErrorEvent(&span, "missing authorization token",
			errors.New("authorization token is required"))

		return unauthorizedError(c, "MISSING_TOKEN", "Authorization token is required")
	}

	// Parse JWT token (unverified - lib-auth already validated it)
	token, _, err := new(jwt.Parser).ParseUnverified(accessToken, jwt.MapClaims{})
	if err != nil {
		logger.Errorf("failed to parse JWT token: %v", err)
		libOpentelemetry.HandleSpanBusinessErrorEvent(&span, "failed to parse token", err)

		return unauthorizedError(c, "INVALID_TOKEN", "Failed to parse authorization token")
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		logger.Errorf("JWT claims are not in expected format")
		libOpentelemetry.HandleSpanBusinessErrorEvent(&span, "invalid claims format",
			errors.New("JWT claims are not in expected format"))

		return unauthorizedError(c, "INVALID_TOKEN", "JWT claims are not in expected format")
	}

	// Extract tenantId from claims
	tenantID, _ := claims["tenantId"].(string)
	if tenantID == "" {
		logger.Errorf("no tenantId in JWT - multi-tenant mode requires tenantId claim")
		libOpentelemetry.HandleSpanBusinessErrorEvent(&span, "missing tenantId in JWT",
			errors.New("tenantId is required in JWT token"))

		return unauthorizedError(c, "MISSING_TENANT", "tenantId is required in JWT token")
	}

	logger.Infof("tenant context resolved: tenantID=%s", tenantID)

	// Store tenant ID in context
	ctx = core.ContextWithTenantID(ctx, tenantID)

	// Handle PostgreSQL if manager is configured
	if m.postgres != nil {
		conn, err := m.postgres.GetConnection(ctx, tenantID)
		if err != nil {
			var suspErr *core.TenantSuspendedError
			if errors.As(err, &suspErr) {
				logger.Warnf("tenant service is %s: tenantID=%s", suspErr.Status, tenantID)
				libOpentelemetry.HandleSpanBusinessErrorEvent(&span, "tenant service suspended", err)

				return forbiddenError(c, "0131", "Service Suspended",
					fmt.Sprintf("tenant service is %s", suspErr.Status))
			}

			logger.Errorf("failed to get tenant PostgreSQL connection: %v", err)
			libOpentelemetry.HandleSpanError(&span, "failed to get tenant PostgreSQL connection", err)

			return internalServerError(c, "TENANT_DB_ERROR", "Failed to resolve tenant database", err.Error())
		}

		// Get the database connection from PostgresConnection
		db, err := conn.GetDB()
		if err != nil {
			logger.Errorf("failed to get database from PostgreSQL connection: %v", err)
			libOpentelemetry.HandleSpanError(&span, "failed to get database from PostgreSQL connection", err)

			return internalServerError(c, "TENANT_DB_ERROR", "Failed to get tenant database connection", err.Error())
		}

		// Store PostgreSQL connection in context
		ctx = core.ContextWithTenantPGConnection(ctx, db)
	}

	// Handle MongoDB if manager is configured
	if m.mongo != nil {
		mongoDB, err := m.mongo.GetDatabaseForTenant(ctx, tenantID)
		if err != nil {
			var suspErr *core.TenantSuspendedError
			if errors.As(err, &suspErr) {
				logger.Warnf("tenant service is %s: tenantID=%s", suspErr.Status, tenantID)
				libOpentelemetry.HandleSpanBusinessErrorEvent(&span, "tenant service suspended", err)

				return forbiddenError(c, "0131", "Service Suspended",
					fmt.Sprintf("tenant service is %s", suspErr.Status))
			}

			logger.Errorf("failed to get tenant MongoDB connection: %v", err)
			libOpentelemetry.HandleSpanError(&span, "failed to get tenant MongoDB connection", err)

			return internalServerError(c, "TENANT_MONGO_ERROR", "Failed to resolve tenant MongoDB database", err.Error())
		}

		ctx = core.ContextWithTenantMongo(ctx, mongoDB)
	}

	// Update Fiber context
	c.SetUserContext(ctx)

	return c.Next()
}

// extractTokenFromHeader extracts the Bearer token from the Authorization header.
// Only the "Bearer " scheme is accepted. Other schemes (e.g., "Basic ") return empty string.
func extractTokenFromHeader(c *fiber.Ctx) string {
	authHeader := c.Get("Authorization")
	if authHeader == "" {
		return ""
	}

	// Only accept "Bearer " scheme; reject other schemes (e.g., "Basic ")

	if strings.HasPrefix(authHeader, "Bearer ") {
		return strings.TrimPrefix(authHeader, "Bearer ")
	}

	return ""
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
func internalServerError(c *fiber.Ctx, code, title, message string) error {
	return c.Status(http.StatusInternalServerError).JSON(fiber.Map{
		"code":    code,
		"title":   title,
		"message": message,
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

// Enabled returns whether the middleware is enabled.
func (m *TenantMiddleware) Enabled() bool {
	return m.enabled
}
