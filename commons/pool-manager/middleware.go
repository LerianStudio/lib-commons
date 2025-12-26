package poolmanager

import (
	"context"
	"errors"
	"strings"

	libLog "github.com/LerianStudio/lib-commons/v2/commons/log"
	"github.com/gofiber/fiber/v2"
)

// Context keys for storing tenant information in context.
// These are unexported to prevent external modification.
type contextKey string

const (
	tenantConfigContextKey       contextKey = "tenant_config"
	tenantPostgreSQLContextKey   contextKey = "tenant_postgresql"
	tenantMongoDBContextKey      contextKey = "tenant_mongodb"
	tenantPGConnectionContextKey contextKey = "tenant_pg_connection"
	tenantMongoDBContextKeyConn  contextKey = "tenant_mongo_connection"
)

// defaultSkipPaths contains the default paths that should bypass tenant validation.
var defaultSkipPaths = []string{
	"/health",
	"/ready",
	"/metrics",
	"/livez",
	"/readyz",
}

// Middleware defines the interface for the multi-tenant middleware.
type Middleware interface {
	// Handler returns the Fiber middleware handler.
	Handler() fiber.Handler
	// IsEnabled returns whether the middleware is enabled.
	IsEnabled() bool
}

// MiddlewareOption is a function that configures the middleware.
type MiddlewareOption func(*middlewareImpl)

// WithSkipPaths adds custom paths to the skip list.
// These paths will bypass tenant validation.
func WithSkipPaths(paths ...string) MiddlewareOption {
	return func(m *middlewareImpl) {
		m.skipPaths = append(m.skipPaths, paths...)
	}
}

// WithPostgresPoolManager sets the PostgreSQL pool manager for the middleware.
// When set, the middleware will inject actual database connections into the context
// instead of just configuration. This enables handlers to use GetTenantPGConnection()
// to get tenant-specific database connections.
func WithPostgresPoolManager(pgPoolMgr PostgresPoolManager) MiddlewareOption {
	return func(m *middlewareImpl) {
		m.pgPoolMgr = pgPoolMgr
	}
}

// WithMongoPoolManager sets the MongoDB pool manager for the middleware.
// When set, the middleware will inject actual database connections into the context
// instead of just configuration. This enables handlers to use GetTenantMongoDatabase()
// to get tenant-specific MongoDB database handles.
func WithMongoPoolManager(mongoPoolMgr MongoPoolManager) MiddlewareOption {
	return func(m *middlewareImpl) {
		m.mongoPoolMgr = mongoPoolMgr
	}
}

// WithMiddlewareLogger sets the logger for the middleware.
func WithMiddlewareLogger(logger libLog.Logger) MiddlewareOption {
	return func(m *middlewareImpl) {
		m.logger = logger
	}
}

// middlewareImpl is the default implementation of the Middleware interface.
type middlewareImpl struct {
	config       *Config
	resolver     Resolver
	extractor    Extractor
	skipPaths    []string
	pgPoolMgr    PostgresPoolManager // Optional: when set, injects actual PG connections
	mongoPoolMgr MongoPoolManager    // Optional: when set, injects actual MongoDB connections
	logger       libLog.Logger       // Optional: when set, enables logging
}

// NewMiddleware creates a new tenant middleware instance.
// Returns nil if config or resolver is nil.
func NewMiddleware(config *Config, resolver Resolver, opts ...MiddlewareOption) Middleware {
	if config == nil || resolver == nil {
		return nil
	}

	// Create extractor with configured claim key
	claimKey := config.TenantClaimKey
	if claimKey == "" {
		claimKey = "tenantId"
	}

	m := &middlewareImpl{
		config:    config,
		resolver:  resolver,
		extractor: NewExtractor(claimKey),
		skipPaths: make([]string, len(defaultSkipPaths)),
	}

	// Copy default skip paths
	copy(m.skipPaths, defaultSkipPaths)

	// Apply options
	for _, opt := range opts {
		opt(m)
	}

	return m
}

// IsEnabled returns whether the middleware is enabled.
func (m *middlewareImpl) IsEnabled() bool {
	return m.config.Enabled
}

// Handler returns the Fiber middleware handler.
func (m *middlewareImpl) Handler() fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Bypass if multi-tenant is disabled
		if !m.config.Enabled {
			return c.Next()
		}

		// Check if path should be skipped
		path := c.Path()
		if m.shouldSkipPath(path) {
			return c.Next()
		}

		// Step 1: Extract and validate token from Authorization header
		token, err := m.extractAndValidateToken(c)
		if err != nil {
			return err // Already returns proper fiber response
		}

		// Step 2: Extract tenant ID from JWT token
		tenantID, err := m.extractTenantIDFromToken(token, path)
		if err != nil {
			return c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
				"error": "failed to extract tenant ID from token",
			})
		}

		// Step 3: Resolve and validate tenant configuration
		tenantConfig, statusCode, errMsg := m.resolveAndValidateTenant(c.UserContext(), tenantID, path)
		if tenantConfig == nil {
			return c.Status(statusCode).JSON(fiber.Map{
				"error": errMsg,
			})
		}

		// Step 4: Inject tenant context and pool connections
		ctx := m.injectTenantContextAndPools(c.UserContext(), tenantID, tenantConfig)

		// Set the updated context and proceed
		c.SetUserContext(ctx)

		return c.Next()
	}
}

// extractAndValidateToken reads and validates the Authorization header.
// Returns the raw token string or a fiber error response.
func (m *middlewareImpl) extractAndValidateToken(c *fiber.Ctx) (string, error) {
	authHeader := c.Get("Authorization")
	if authHeader == "" {
		if m.logger != nil {
			m.logger.Warnf("Missing authorization header for path %s", c.Path())
		}

		return "", c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "missing authorization header",
		})
	}

	if !strings.HasPrefix(authHeader, "Bearer ") {
		if m.logger != nil {
			m.logger.Warnf("Invalid authorization header format for path %s", c.Path())
		}

		return "", c.Status(fiber.StatusUnauthorized).JSON(fiber.Map{
			"error": "invalid authorization header format",
		})
	}

	return strings.TrimPrefix(authHeader, "Bearer "), nil
}

// extractTenantIDFromToken extracts the tenant ID from a JWT token.
// Returns the tenant ID or an error.
func (m *middlewareImpl) extractTenantIDFromToken(token, path string) (string, error) {
	tenantID, err := m.extractor.ExtractFromJWT(token)
	if err != nil {
		if m.logger != nil {
			m.logger.Warnf("Failed to extract tenant ID from token: %v", err)
		}

		return "", err
	}

	if m.logger != nil {
		m.logger.Infof("Extracted tenant ID %s for path %s", tenantID, path)
	}

	return tenantID, nil
}

// resolveAndValidateTenant resolves the tenant configuration and validates its status.
// Returns the tenant config, or (nil, statusCode, errorMessage) if validation fails.
func (m *middlewareImpl) resolveAndValidateTenant(ctx context.Context, tenantID, path string) (*TenantConfig, int, string) {
	tenantConfig, err := m.resolver.ResolveWithService(ctx, tenantID, m.config.ApplicationName)
	if err != nil {
		return m.classifyResolverError(err, tenantID)
	}

	// Validate tenant status
	if !isActiveTenant(tenantConfig.Status) {
		if m.logger != nil {
			m.logger.Warnf("Tenant %s is not active (status: %s)", tenantID, tenantConfig.Status)
		}

		return nil, fiber.StatusForbidden, "tenant is not active"
	}

	if m.logger != nil {
		m.logger.Infof("Resolved tenant %s (isolation: %s) for path %s", tenantID, tenantConfig.IsolationMode, path)
	}

	return tenantConfig, 0, ""
}

// classifyResolverError classifies resolver errors and returns appropriate HTTP status and message.
func (m *middlewareImpl) classifyResolverError(err error, tenantID string) (*TenantConfig, int, string) {
	// Connection error (service unavailable)
	if isConnectionError(err) {
		if m.logger != nil {
			m.logger.Errorf("Tenant service unavailable for tenant %s: %v", tenantID, err)
		}

		return nil, fiber.StatusServiceUnavailable, "tenant service unavailable"
	}

	// Tenant not found
	if strings.Contains(err.Error(), "not found") {
		if m.logger != nil {
			m.logger.Warnf("Tenant not found: %s", tenantID)
		}

		return nil, fiber.StatusForbidden, "tenant not found"
	}

	// Other errors
	if m.logger != nil {
		m.logger.Errorf("Failed to resolve tenant configuration for %s: %v", tenantID, err)
	}

	return nil, fiber.StatusServiceUnavailable, "failed to resolve tenant configuration"
}

// injectTenantContextAndPools sets tenant context values and injects database connections.
func (m *middlewareImpl) injectTenantContextAndPools(ctx context.Context, tenantID string, tenantConfig *TenantConfig) context.Context {
	// Set tenant ID and config in context
	ctx = context.WithValue(ctx, TenantContextKey, tenantID)
	ctx = context.WithValue(ctx, tenantConfigContextKey, tenantConfig)

	// Inject database configurations if available
	if m.config.ApplicationName == "" {
		return ctx
	}

	dbServices, ok := tenantConfig.Databases[m.config.ApplicationName]
	if !ok {
		return ctx
	}

	// Inject PostgreSQL config and connection
	if dbServices.PostgreSQL != nil {
		ctx = context.WithValue(ctx, tenantPostgreSQLContextKey, dbServices.PostgreSQL)
		ctx = m.injectPostgresConnection(ctx, tenantID)
	}

	// Inject MongoDB config and connection
	if dbServices.MongoDB != nil {
		ctx = context.WithValue(ctx, tenantMongoDBContextKey, dbServices.MongoDB)
		ctx = m.injectMongoConnection(ctx, tenantID)
	}

	return ctx
}

// injectPostgresConnection attempts to get a PostgreSQL connection from the pool manager.
func (m *middlewareImpl) injectPostgresConnection(ctx context.Context, tenantID string) context.Context {
	if m.pgPoolMgr == nil {
		return ctx
	}

	pgConn, err := m.pgPoolMgr.GetConnection(ctx, tenantID, m.config.ApplicationName)
	if err != nil {
		if m.logger != nil {
			m.logger.Warnf("Failed to get PostgreSQL connection for tenant %s: %v", tenantID, err)
		}

		return ctx
	}

	return context.WithValue(ctx, tenantPGConnectionContextKey, pgConn)
}

// injectMongoConnection attempts to get a MongoDB database from the pool manager.
func (m *middlewareImpl) injectMongoConnection(ctx context.Context, tenantID string) context.Context {
	if m.mongoPoolMgr == nil {
		return ctx
	}

	mongoDb, err := m.mongoPoolMgr.GetDatabase(ctx, tenantID, m.config.ApplicationName)
	if err != nil {
		if m.logger != nil {
			m.logger.Warnf("Failed to get MongoDB database for tenant %s: %v", tenantID, err)
		}

		return ctx
	}

	return context.WithValue(ctx, tenantMongoDBContextKeyConn, mongoDb)
}

// shouldSkipPath checks if the given path should bypass tenant validation.
func (m *middlewareImpl) shouldSkipPath(path string) bool {
	for _, skipPath := range m.skipPaths {
		if strings.HasPrefix(path, skipPath) {
			return true
		}
	}
	return false
}

// isActiveTenant checks if the tenant status indicates an active tenant.
func isActiveTenant(status string) bool {
	return status == "active"
}

// isConnectionError checks if the error is a connection-related error.
func isConnectionError(err error) bool {
	if err == nil {
		return false
	}

	errMsg := err.Error()
	connectionIndicators := []string{
		"connection refused",
		"no such host",
		"network is unreachable",
		"dial tcp",
		"i/o timeout",
		"request failed",
	}

	for _, indicator := range connectionIndicators {
		if strings.Contains(strings.ToLower(errMsg), strings.ToLower(indicator)) {
			return true
		}
	}

	// Check for wrapped errors
	var netErr interface{ Timeout() bool }
	if errors.As(err, &netErr) {
		return true
	}

	return false
}

// GetTenantID retrieves the tenant ID from the context.
// Returns an empty string if not found.
func GetTenantID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}

	// First check with string key (as used in doc.go)
	if value := ctx.Value(TenantContextKey); value != nil {
		if tenantID, ok := value.(string); ok {
			return tenantID
		}
	}

	return ""
}

// GetTenantConfig retrieves the tenant configuration from the context.
// Returns nil if not found.
// Checks both the internal middleware key and the exported TenantConfigContextKey for compatibility.
func GetTenantConfig(ctx context.Context) *TenantConfig {
	if ctx == nil {
		return nil
	}

	// Check internal middleware key first (backward compatibility)
	value := ctx.Value(tenantConfigContextKey)
	if value != nil {
		if config, ok := value.(*TenantConfig); ok {
			return config
		}
	}

	// Check exported context key (from WithTenantConfig)
	value = ctx.Value(TenantConfigContextKey)
	if value == nil {
		return nil
	}

	config, ok := value.(*TenantConfig)
	if !ok {
		return nil
	}

	return config
}

// GetTenantPostgreSQL retrieves the PostgreSQL configuration from the context.
// Returns nil if not found.
// Checks both the internal middleware key and the exported TenantPGContextKey for compatibility.
func GetTenantPostgreSQL(ctx context.Context) *PostgreSQLConfig {
	if ctx == nil {
		return nil
	}

	// Check internal middleware key first (backward compatibility)
	value := ctx.Value(tenantPostgreSQLContextKey)
	if value != nil {
		if config, ok := value.(*PostgreSQLConfig); ok {
			return config
		}
	}

	// Check exported context key (from WithTenantPG)
	value = ctx.Value(TenantPGContextKey)
	if value == nil {
		return nil
	}

	config, ok := value.(*PostgreSQLConfig)
	if !ok {
		return nil
	}

	return config
}

// GetTenantMongoDB retrieves the MongoDB configuration from the context.
// Returns nil if not found.
// Checks both the internal middleware key and the exported TenantMongoContextKey for compatibility.
func GetTenantMongoDB(ctx context.Context) *MongoDBConfig {
	if ctx == nil {
		return nil
	}

	// Check internal middleware key first (backward compatibility)
	value := ctx.Value(tenantMongoDBContextKey)
	if value != nil {
		if config, ok := value.(*MongoDBConfig); ok {
			return config
		}
	}

	// Check exported context key (from WithTenantMongo)
	value = ctx.Value(TenantMongoContextKey)
	if value == nil {
		return nil
	}

	config, ok := value.(*MongoDBConfig)
	if !ok {
		return nil
	}

	return config
}
