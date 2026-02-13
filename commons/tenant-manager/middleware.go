package tenantmanager

import (
	"context"
	"net/http"
	"strings"

	libCommons "github.com/LerianStudio/lib-commons/v2/commons"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v2/commons/opentelemetry"
	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
)

// TenantMiddleware extracts tenantId from JWT token and resolves the database connection.
// It stores the connection in context for downstream handlers and repositories.
// Supports PostgreSQL only, MongoDB only, or both databases.
type TenantMiddleware struct {
	postgres *PostgresManager // PostgreSQL manager (optional)
	mongo    *MongoManager    // MongoDB manager (optional)
	enabled  bool
}

// TenantMiddlewareOption configures a TenantMiddleware.
type TenantMiddlewareOption func(*TenantMiddleware)

// WithPostgresManager sets the PostgreSQL manager for the tenant middleware.
// When configured, the middleware will resolve PostgreSQL connections for tenants.
func WithPostgresManager(postgres *PostgresManager) TenantMiddlewareOption {
	return func(m *TenantMiddleware) {
		m.postgres = postgres
		m.enabled = m.postgres != nil || m.mongo != nil
	}
}

// WithMongoManager sets the MongoDB manager for the tenant middleware.
// When configured, the middleware will resolve MongoDB connections for tenants.
func WithMongoManager(mongo *MongoManager) TenantMiddlewareOption {
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
//	mid := tenantmanager.NewTenantMiddleware(tenantmanager.WithPostgresManager(pgManager))
//
//	// MongoDB only
//	mid := tenantmanager.NewTenantMiddleware(tenantmanager.WithMongoManager(mongoManager))
//
//	// Both PostgreSQL and MongoDB
//	mid := tenantmanager.NewTenantMiddleware(
//	    tenantmanager.WithPostgresManager(pgManager),
//	    tenantmanager.WithMongoManager(mongoManager),
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
//	tenantMid := tenantmanager.NewTenantMiddleware(tenantmanager.WithPostgresManager(pgManager))
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
		libOpentelemetry.HandleSpanBusinessErrorEvent(&span, "missing authorization token", nil)
		return unauthorizedError(c, "MISSING_TOKEN", "Unauthorized", "Authorization token is required")
	}

	// Parse JWT token (unverified - lib-auth already validated it)
	token, _, err := new(jwt.Parser).ParseUnverified(accessToken, jwt.MapClaims{})
	if err != nil {
		logger.Errorf("failed to parse JWT token: %v", err)
		libOpentelemetry.HandleSpanBusinessErrorEvent(&span, "failed to parse token", err)
		return unauthorizedError(c, "INVALID_TOKEN", "Unauthorized", "Failed to parse authorization token")
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		logger.Errorf("JWT claims are not in expected format")
		libOpentelemetry.HandleSpanBusinessErrorEvent(&span, "invalid claims format", nil)
		return unauthorizedError(c, "INVALID_TOKEN", "Unauthorized", "JWT claims are not in expected format")
	}

	// Extract tenantId from claims
	tenantID, _ := claims["tenantId"].(string)
	if tenantID == "" {
		logger.Errorf("no tenantId in JWT - multi-tenant mode requires tenantId claim")
		libOpentelemetry.HandleSpanBusinessErrorEvent(&span, "missing tenantId in JWT", nil)
		return unauthorizedError(c, "MISSING_TENANT", "Unauthorized", "tenantId is required in JWT token")
	}

	logger.Infof("tenant context resolved: tenantID=%s", tenantID)

	// Store tenant ID in context
	ctx = ContextWithTenantID(ctx, tenantID)

	// Handle PostgreSQL if manager is configured
	if m.postgres != nil {
		conn, err := m.postgres.GetConnection(ctx, tenantID)
		if err != nil {
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
		ctx = ContextWithTenantPGConnection(ctx, db)
	}

	// Handle MongoDB if manager is configured
	if m.mongo != nil {
		mongoDB, err := m.mongo.GetDatabaseForTenant(ctx, tenantID)
		if err != nil {
			logger.Errorf("failed to get tenant MongoDB connection: %v", err)
			libOpentelemetry.HandleSpanError(&span, "failed to get tenant MongoDB connection", err)
			return internalServerError(c, "TENANT_MONGO_ERROR", "Failed to resolve tenant MongoDB database", err.Error())
		}
		ctx = ContextWithTenantMongo(ctx, mongoDB)
	}

	// Update Fiber context
	c.SetUserContext(ctx)

	return c.Next()
}

// extractTokenFromHeader extracts the Bearer token from the Authorization header.
func extractTokenFromHeader(c *fiber.Ctx) string {
	authHeader := c.Get("Authorization")
	if authHeader == "" {
		return ""
	}

	// Check if it's a Bearer token
	if strings.HasPrefix(authHeader, "Bearer ") {
		return strings.TrimPrefix(authHeader, "Bearer ")
	}

	return authHeader
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
func unauthorizedError(c *fiber.Ctx, code, title, message string) error {
	return c.Status(http.StatusUnauthorized).JSON(fiber.Map{
		"code":    code,
		"title":   title,
		"message": message,
	})
}

// Enabled returns whether the middleware is enabled.
func (m *TenantMiddleware) Enabled() bool {
	return m.enabled
}
