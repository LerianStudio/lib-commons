// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	libCommons "github.com/LerianStudio/lib-commons/v3/commons"
	"github.com/LerianStudio/lib-commons/v3/commons/log"
	libHTTP "github.com/LerianStudio/lib-commons/v3/commons/net/http"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v3/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v3/commons/tenant-manager/core"
	tmmongo "github.com/LerianStudio/lib-commons/v3/commons/tenant-manager/mongo"
	tmpostgres "github.com/LerianStudio/lib-commons/v3/commons/tenant-manager/postgres"
	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// ConsumerTrigger triggers on-demand consumer spawning for lazy mode.
// Implementations should ensure idempotent behavior: calling EnsureConsumerStarted
// multiple times for the same tenantID must be safe and return quickly after the
// first invocation.
type ConsumerTrigger interface {
	EnsureConsumerStarted(ctx context.Context, tenantID string)
}

// ErrorMapper converts tenant-manager errors into Fiber HTTP responses.
// If nil, the default error mapping is used.
type ErrorMapper func(c *fiber.Ctx, err error, tenantID string) error

// PoolRoute defines a path-based route to a module's database pools.
// Each route maps one or more URL path prefixes to a specific module and
// its associated PostgreSQL and/or MongoDB tenant connection managers.
type PoolRoute struct {
	paths     []string
	module    string
	pgPool    *tmpostgres.Manager
	mongoPool *tmmongo.Manager
}

// MultiPoolOption configures a MultiPoolMiddleware.
type MultiPoolOption func(*MultiPoolMiddleware)

// MultiPoolMiddleware routes requests to module-specific tenant pools
// based on URL path matching. It handles JWT extraction, pool resolution,
// connection injection, error mapping, and consumer triggering.
type MultiPoolMiddleware struct {
	routes          []*PoolRoute
	defaultRoute    *PoolRoute
	publicPaths     []string
	consumerTrigger ConsumerTrigger
	crossModule     bool
	errorMapper     ErrorMapper
	logger          log.Logger
	enabled         bool
}

// WithRoute registers a path-based route mapping URL prefixes to a module's
// database pools. Multiple routes can be registered; the first matching route
// wins. The paths parameter contains URL path prefixes to match against.
func WithRoute(paths []string, module string, pgPool *tmpostgres.Manager, mongoPool *tmmongo.Manager) MultiPoolOption {
	return func(m *MultiPoolMiddleware) {
		m.routes = append(m.routes, &PoolRoute{
			paths:     paths,
			module:    module,
			pgPool:    pgPool,
			mongoPool: mongoPool,
		})
	}
}

// WithDefaultRoute registers a fallback route used when no path-based route
// matches. If no default is set and no route matches, the middleware passes
// through to the next handler.
func WithDefaultRoute(module string, pgPool *tmpostgres.Manager, mongoPool *tmmongo.Manager) MultiPoolOption {
	return func(m *MultiPoolMiddleware) {
		m.defaultRoute = &PoolRoute{
			module:    module,
			pgPool:    pgPool,
			mongoPool: mongoPool,
		}
	}
}

// WithPublicPaths registers URL path prefixes that bypass tenant resolution.
// Requests matching any of the given prefixes skip JWT extraction and proceed
// directly to the next handler.
func WithPublicPaths(paths ...string) MultiPoolOption {
	return func(m *MultiPoolMiddleware) {
		m.publicPaths = append(m.publicPaths, paths...)
	}
}

// WithConsumerTrigger sets a ConsumerTrigger that is invoked after tenant ID
// extraction. This enables lazy consumer spawning in multi-tenant messaging
// architectures.
func WithConsumerTrigger(ct ConsumerTrigger) MultiPoolOption {
	return func(m *MultiPoolMiddleware) {
		m.consumerTrigger = ct
	}
}

// WithCrossModuleInjection enables resolution of database connections for all
// registered routes, not just the matched one. This is useful when a request
// handler needs access to multiple module databases (e.g., cross-module queries).
func WithCrossModuleInjection() MultiPoolOption {
	return func(m *MultiPoolMiddleware) {
		m.crossModule = true
	}
}

// WithErrorMapper sets a custom error mapper function that converts tenant-manager
// errors into Fiber HTTP responses. When nil (the default), the built-in
// mapDefaultError is used.
func WithErrorMapper(fn ErrorMapper) MultiPoolOption {
	return func(m *MultiPoolMiddleware) {
		m.errorMapper = fn
	}
}

// WithMultiPoolLogger sets the logger for the MultiPoolMiddleware.
// When not set, the middleware extracts the logger from request context.
func WithMultiPoolLogger(l log.Logger) MultiPoolOption {
	return func(m *MultiPoolMiddleware) {
		m.logger = l
	}
}

// NewMultiPoolMiddleware creates a new MultiPoolMiddleware with the given options.
// The middleware is enabled if at least one route has a PG pool with
// IsMultiTenant() == true.
func NewMultiPoolMiddleware(opts ...MultiPoolOption) *MultiPoolMiddleware {
	m := &MultiPoolMiddleware{}

	for _, opt := range opts {
		opt(m)
	}

	// Enable if at least one route has a multi-tenant PG pool
	for _, route := range m.routes {
		if route.pgPool != nil && route.pgPool.IsMultiTenant() {
			m.enabled = true

			break
		}
	}

	if !m.enabled && m.defaultRoute != nil && m.defaultRoute.pgPool != nil && m.defaultRoute.pgPool.IsMultiTenant() {
		m.enabled = true
	}

	return m
}

// WithTenantDB is a Fiber handler that extracts tenant context from JWT,
// resolves the appropriate database connections based on URL path matching,
// and stores them in the request context for downstream handlers.
func (m *MultiPoolMiddleware) WithTenantDB(c *fiber.Ctx) error {
	// Step 1: Public path check
	if m.isPublicPath(c.Path()) {
		return c.Next()
	}

	// Step 2: Route matching
	route := m.matchRoute(c.Path())
	if route == nil {
		return c.Next()
	}

	// Step 3: Multi-tenant check
	if route.pgPool == nil || !route.pgPool.IsMultiTenant() {
		return c.Next()
	}

	// Step 4: Extract context + telemetry
	ctx := libOpentelemetry.ExtractHTTPContext(c)
	if ctx == nil {
		ctx = context.Background()
	}

	logger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)

	ctx, span := tracer.Start(ctx, "middleware.multi_pool.with_tenant_db")
	defer span.End()

	// Step 5: Extract tenant ID from JWT
	tenantID, err := m.extractTenantID(c)
	if err != nil {
		logger.Errorf("failed to extract tenant ID: %v", err)
		libOpentelemetry.HandleSpanBusinessErrorEvent(&span, "failed to extract tenant ID", err)

		if m.errorMapper != nil {
			return m.errorMapper(c, err, "")
		}

		return unauthorizedError(c, "MISSING_TOKEN", err.Error())
	}

	logger.Infof("multi-pool tenant resolved: tenantID=%s, module=%s, path=%s",
		tenantID, route.module, c.Path())

	// Step 6: Set tenant ID in context
	ctx = core.ContextWithTenantID(ctx, tenantID)

	// Step 6.1: Set tenant.id as span attribute on the middleware span and in the AttrBag
	// so ALL child spans (downstream DB queries, HTTP calls, etc.) inherit it
	// via the AttrBagSpanProcessor.
	span.SetAttributes(attribute.String("tenant.id", tenantID))
	ctx = libCommons.ContextWithSpanAttributes(ctx, attribute.String("tenant.id", tenantID))

	// Step 7: Consumer trigger
	if m.consumerTrigger != nil {
		m.consumerTrigger.EnsureConsumerStarted(ctx, tenantID)
	}

	// Step 8: Resolve PG connection for matched route
	ctx, err = m.resolvePGConnection(ctx, route, tenantID, logger, &span)
	if err != nil {
		if m.errorMapper != nil {
			return m.errorMapper(c, err, tenantID)
		}

		return m.mapDefaultError(c, err, tenantID)
	}

	// Step 9: Cross-module injection
	if m.crossModule {
		ctx = m.resolveCrossModuleConnections(ctx, route, tenantID, logger)
	}

	// Step 10: Resolve Mongo connection
	if route.mongoPool != nil {
		ctx, err = m.resolveMongoConnection(ctx, route, tenantID, logger, &span)
		if err != nil {
			if m.errorMapper != nil {
				return m.errorMapper(c, err, tenantID)
			}

			return m.mapDefaultError(c, err, tenantID)
		}
	}

	// Step 11: Update context
	c.SetUserContext(ctx)

	logger.Infof("multi-pool connections injected: tenantID=%s, module=%s", tenantID, route.module)

	return c.Next()
}

// matchRoute finds the PoolRoute whose paths match the request path.
// Returns the defaultRoute if no specific route matches, or nil if no
// default is configured.
func (m *MultiPoolMiddleware) matchRoute(path string) *PoolRoute {
	for _, route := range m.routes {
		for _, prefix := range route.paths {
			if strings.HasPrefix(path, prefix) {
				return route
			}
		}
	}

	return m.defaultRoute
}

// isPublicPath checks whether the given path matches any registered public
// path prefix. Public paths bypass all tenant resolution logic.
func (m *MultiPoolMiddleware) isPublicPath(path string) bool {
	for _, prefix := range m.publicPaths {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}

	return false
}

// extractTenantID extracts the tenant ID from the JWT token in the
// Authorization header. It uses ParseUnverified because lib-auth has
// already validated the token upstream.
func (m *MultiPoolMiddleware) extractTenantID(c *fiber.Ctx) (string, error) {
	accessToken := libHTTP.ExtractTokenFromHeader(c)
	if accessToken == "" {
		return "", errors.New("authorization token is required")
	}

	token, _, err := new(jwt.Parser).ParseUnverified(accessToken, jwt.MapClaims{})
	if err != nil {
		return "", fmt.Errorf("failed to parse authorization token: %w", err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", errors.New("JWT claims are not in expected format")
	}

	tenantID, _ := claims["tenantId"].(string)
	if tenantID == "" {
		return "", errors.New("tenantId is required in JWT token")
	}

	return tenantID, nil
}

// resolvePGConnection resolves the PostgreSQL connection for the given route
// and tenant, injecting it into the context using module-scoped context keys.
func (m *MultiPoolMiddleware) resolvePGConnection(
	ctx context.Context,
	route *PoolRoute,
	tenantID string,
	logger log.Logger,
	span *trace.Span,
) (context.Context, error) {
	conn, err := route.pgPool.GetConnection(ctx, tenantID)
	if err != nil {
		logger.Errorf("failed to get tenant PostgreSQL connection: module=%s, tenantID=%s, error=%v",
			route.module, tenantID, err)
		libOpentelemetry.HandleSpanError(span, "failed to get tenant PostgreSQL connection", err)

		return ctx, err
	}

	db, err := conn.GetDB()
	if err != nil {
		logger.Errorf("failed to get database from PostgreSQL connection: module=%s, tenantID=%s, error=%v",
			route.module, tenantID, err)
		libOpentelemetry.HandleSpanError(span, "failed to get database from PostgreSQL connection", err)

		return ctx, err
	}

	ctx = core.ContextWithModulePGConnection(ctx, route.module, db)

	return ctx, nil
}

// resolveCrossModuleConnections resolves PG connections for all routes other
// than the matched one. Errors are logged but do not block the request.
func (m *MultiPoolMiddleware) resolveCrossModuleConnections(
	ctx context.Context,
	matchedRoute *PoolRoute,
	tenantID string,
	logger log.Logger,
) context.Context {
	for _, route := range m.routes {
		if route == matchedRoute || route.pgPool == nil || !route.pgPool.IsMultiTenant() {
			continue
		}

		conn, err := route.pgPool.GetConnection(ctx, tenantID)
		if err != nil {
			logger.Warnf("cross-module PG resolution failed: module=%s, tenantID=%s, error=%v",
				route.module, tenantID, err)

			continue
		}

		db, err := conn.GetDB()
		if err != nil {
			logger.Warnf("cross-module PG GetDB failed: module=%s, tenantID=%s, error=%v",
				route.module, tenantID, err)

			continue
		}

		ctx = core.ContextWithModulePGConnection(ctx, route.module, db)

		if route.mongoPool != nil {
			mongoDB, mongoErr := route.mongoPool.GetDatabaseForTenant(ctx, tenantID)
			if mongoErr != nil {
				logger.Warnf("cross-module MongoDB resolution failed: module=%s, tenantID=%s, error=%v",
					route.module, tenantID, mongoErr)
			} else {
				ctx = core.ContextWithModuleMongo(ctx, route.module, mongoDB)
			}
		}
	}

	// Also resolve default route if it differs from matched
	if m.defaultRoute != nil && m.defaultRoute != matchedRoute &&
		m.defaultRoute.pgPool != nil && m.defaultRoute.pgPool.IsMultiTenant() {
		conn, err := m.defaultRoute.pgPool.GetConnection(ctx, tenantID)
		if err != nil {
			logger.Warnf("cross-module PG resolution failed: module=%s, tenantID=%s, error=%v",
				m.defaultRoute.module, tenantID, err)

			return ctx
		}

		db, err := conn.GetDB()
		if err != nil {
			logger.Warnf("cross-module PG GetDB failed: module=%s, tenantID=%s, error=%v",
				m.defaultRoute.module, tenantID, err)

			return ctx
		}

		ctx = core.ContextWithModulePGConnection(ctx, m.defaultRoute.module, db)

		if m.defaultRoute.mongoPool != nil {
			mongoDB, mongoErr := m.defaultRoute.mongoPool.GetDatabaseForTenant(ctx, tenantID)
			if mongoErr != nil {
				logger.Warnf("cross-module MongoDB resolution failed: module=%s, tenantID=%s, error=%v",
					m.defaultRoute.module, tenantID, mongoErr)
			} else {
				ctx = core.ContextWithModuleMongo(ctx, m.defaultRoute.module, mongoDB)
			}
		}
	}

	return ctx
}

// resolveMongoConnection resolves the MongoDB database for the given route
// and tenant, injecting it into the context.
func (m *MultiPoolMiddleware) resolveMongoConnection(
	ctx context.Context,
	route *PoolRoute,
	tenantID string,
	logger log.Logger,
	span *trace.Span,
) (context.Context, error) {
	mongoDB, err := route.mongoPool.GetDatabaseForTenant(ctx, tenantID)
	if err != nil {
		logger.Errorf("failed to get tenant MongoDB connection: module=%s, tenantID=%s, error=%v",
			route.module, tenantID, err)
		libOpentelemetry.HandleSpanError(span, "failed to get tenant MongoDB connection", err)

		return ctx, err
	}

	ctx = core.ContextWithModuleMongo(ctx, route.module, mongoDB)
	ctx = core.ContextWithTenantMongo(ctx, mongoDB) // backward compatibility for consumers not yet using ResolveModuleMongo

	return ctx, nil
}

// mapDefaultError converts tenant-manager errors into appropriate HTTP responses.
// It follows the same response format as the existing TenantMiddleware.
func (m *MultiPoolMiddleware) mapDefaultError(c *fiber.Ctx, err error, tenantID string) error {
	// Missing token or JWT errors -> 401
	if strings.Contains(err.Error(), "authorization token") ||
		strings.Contains(err.Error(), "parse") ||
		strings.Contains(err.Error(), "tenantId") {
		return unauthorizedError(c, "UNAUTHORIZED", err.Error())
	}

	// Tenant not found -> 404
	if errors.Is(err, core.ErrTenantNotFound) {
		return c.Status(http.StatusNotFound).JSON(fiber.Map{
			"code":    "TENANT_NOT_FOUND",
			"title":   "Tenant Not Found",
			"message": fmt.Sprintf("tenant not found: %s", tenantID),
		})
	}

	// Tenant suspended -> 403
	var suspErr *core.TenantSuspendedError
	if errors.As(err, &suspErr) {
		return forbiddenError(c, "0131", "Service Suspended",
			fmt.Sprintf("tenant service is %s", suspErr.Status))
	}

	// Manager closed or service not configured -> 503
	if errors.Is(err, core.ErrManagerClosed) || errors.Is(err, core.ErrServiceNotConfigured) {
		return c.Status(http.StatusServiceUnavailable).JSON(fiber.Map{
			"code":    "SERVICE_UNAVAILABLE",
			"title":   "Service Unavailable",
			"message": err.Error(),
		})
	}

	// Connection errors -> 503
	if strings.Contains(err.Error(), "connection") {
		return c.Status(http.StatusServiceUnavailable).JSON(fiber.Map{
			"code":    "SERVICE_UNAVAILABLE",
			"title":   "Service Unavailable",
			"message": fmt.Sprintf("failed to resolve tenant database: %s", err.Error()),
		})
	}

	// Default -> 500
	return internalServerError(c, "TENANT_DB_ERROR", "Failed to resolve tenant database", err.Error())
}

// Enabled returns whether the middleware is enabled.
// The middleware is enabled when at least one route has a multi-tenant PG pool.
func (m *MultiPoolMiddleware) Enabled() bool {
	return m.enabled
}
