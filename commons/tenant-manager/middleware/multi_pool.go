// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package middleware

import (
	"context"
	"fmt"
	"strings"

	libCommons "github.com/LerianStudio/lib-commons/v4/commons"
	"github.com/LerianStudio/lib-commons/v4/commons/log"
	libHTTP "github.com/LerianStudio/lib-commons/v4/commons/net/http"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v4/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/logcompat"
	tmmongo "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/mongo"
	tmpostgres "github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/postgres"
	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
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
	logger          *logcompat.Logger
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
		m.logger = logcompat.New(l)
	}
}

// NewMultiPoolMiddleware creates a new MultiPoolMiddleware with the given options.
// The middleware is enabled if at least one route has a PG or Mongo pool with
// IsMultiTenant() == true.
// By default, health probe paths (/healthz, /readyz, /livez, /health) are public
// and bypass JWT extraction. Additional paths can be added via WithPublicPaths().
func NewMultiPoolMiddleware(opts ...MultiPoolOption) *MultiPoolMiddleware {
	m := &MultiPoolMiddleware{
		publicPaths: []string{"/healthz", "/readyz", "/livez", "/health"},
	}

	for _, opt := range opts {
		opt(m)
	}

	// Enable if at least one route has a multi-tenant PG or Mongo pool
	for _, route := range m.routes {
		if (route.pgPool != nil && route.pgPool.IsMultiTenant()) ||
			(route.mongoPool != nil && route.mongoPool.IsMultiTenant()) {
			m.enabled = true

			break
		}
	}

	if !m.enabled && m.defaultRoute != nil {
		if (m.defaultRoute.pgPool != nil && m.defaultRoute.pgPool.IsMultiTenant()) ||
			(m.defaultRoute.mongoPool != nil && m.defaultRoute.mongoPool.IsMultiTenant()) {
			m.enabled = true
		}
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

	// Step 3: Multi-tenant check — skip only if neither pool is multi-tenant
	pgEnabled := route.pgPool != nil && route.pgPool.IsMultiTenant()
	mongoEnabled := route.mongoPool != nil && route.mongoPool.IsMultiTenant()

	if !pgEnabled && !mongoEnabled {
		return c.Next()
	}

	// Step 4: Extract context + telemetry
	ctx := m.initializeTracingContext(c)

	baseLogger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)
	logger := logcompat.New(baseLogger)

	ctx, span := tracer.Start(ctx, "middleware.multi_pool.with_tenant_db")
	defer span.End()

	// Step 5: Extract tenant ID from JWT
	tenantID, err := m.extractTenantID(c)
	if err != nil {
		logger.ErrorCtx(ctx, fmt.Sprintf("failed to extract tenant ID: %v", err))
		libOpentelemetry.HandleSpanBusinessErrorEvent(span, "failed to extract tenant ID", err)

		return m.handleTenantDBError(c, err, "")
	}

	logger.InfofCtx(ctx, "multi-pool tenant resolved: tenantID=%s, module=%s, path=%s",
		tenantID, route.module, c.Path())

	// Step 6: Set tenant ID in context
	ctx = core.ContextWithTenantID(ctx, tenantID)

	// Step 7: Resolve database connections BEFORE triggering consumer.
	// This ensures the tenant is actually resolvable (not suspended/purged)
	// before we start consuming messages for it.
	ctx, err = m.resolveAllConnections(ctx, route, tenantID, pgEnabled, mongoEnabled, logger, span)
	if err != nil {
		return m.handleTenantDBError(c, err, tenantID)
	}

	// Step 8: Trigger consumer AFTER successful resolution.
	// Only trigger for tenants whose connections are confirmed resolvable.
	if m.consumerTrigger != nil {
		m.consumerTrigger.EnsureConsumerStarted(ctx, tenantID)
	}

	// Step 9: Update context
	c.SetUserContext(ctx)

	logger.InfofCtx(ctx, "multi-pool connections injected: tenantID=%s, module=%s", tenantID, route.module)

	return c.Next()
}

// initializeTracingContext extracts HTTP trace context from the Fiber request,
// falling back to a background context if neither source provides one.
func (m *MultiPoolMiddleware) initializeTracingContext(c *fiber.Ctx) context.Context {
	baseCtx := c.UserContext()
	if baseCtx == nil {
		baseCtx = context.Background()
	}

	ctx := libOpentelemetry.ExtractHTTPContext(baseCtx, c)
	if ctx == nil {
		ctx = baseCtx
	}

	return ctx
}

// handleTenantDBError dispatches the error through the custom error mapper if
// configured, otherwise falls back to the default error mapping. For empty
// tenantID (auth errors), it returns a generic 401 when no mapper is set.
func (m *MultiPoolMiddleware) handleTenantDBError(c *fiber.Ctx, err error, tenantID string) error {
	if m.errorMapper != nil {
		return m.errorMapper(c, err, tenantID)
	}

	if tenantID == "" {
		return unauthorizedError(c, "UNAUTHORIZED", "Unauthorized")
	}

	return m.mapDefaultError(c, err, tenantID)
}

// resolveAllConnections resolves PG, cross-module, and Mongo connections for the
// matched route and tenant. It returns the enriched context or the first error.
func (m *MultiPoolMiddleware) resolveAllConnections(
	ctx context.Context,
	route *PoolRoute,
	tenantID string,
	pgEnabled, mongoEnabled bool,
	logger *logcompat.Logger,
	span trace.Span,
) (context.Context, error) {
	var err error

	if pgEnabled {
		ctx, err = m.resolvePGConnection(ctx, route, tenantID, logger, span)
		if err != nil {
			return ctx, err
		}
	}

	if m.crossModule {
		ctx = m.resolveCrossModuleConnections(ctx, route, tenantID, logger)
	}

	if mongoEnabled {
		ctx, err = m.resolveMongoConnection(ctx, route, tenantID, logger, span)
		if err != nil {
			return ctx, err
		}
	}

	return ctx, nil
}

// matchRoute finds the PoolRoute whose paths match the request path.
// Returns the defaultRoute if no specific route matches, or nil if no
// default is configured.
func (m *MultiPoolMiddleware) matchRoute(path string) *PoolRoute {
	for _, route := range m.routes {
		for _, prefix := range route.paths {
			if path == prefix || strings.HasPrefix(path, prefix+"/") {
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
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}

	return false
}

// extractTenantID extracts the tenant ID from the JWT token in the
// Authorization header. Token signature is validated by upstream auth middleware.
func (m *MultiPoolMiddleware) extractTenantID(c *fiber.Ctx) (string, error) {
	accessToken := libHTTP.ExtractTokenFromHeader(c)
	if accessToken == "" {
		return "", core.ErrAuthorizationTokenRequired
	}

	token, _, err := new(jwt.Parser).ParseUnverified(accessToken, jwt.MapClaims{})
	if err != nil {
		return "", fmt.Errorf("%w: %w", core.ErrInvalidAuthorizationToken, err)
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", core.ErrInvalidTenantClaims
	}

	tenantID, _ := claims["tenantId"].(string)
	if tenantID == "" {
		return "", core.ErrMissingTenantIDClaim
	}

	if !core.IsValidTenantID(tenantID) {
		return "", core.ErrInvalidTenantClaims
	}

	return tenantID, nil
}

// resolvePGConnection resolves the PostgreSQL connection for the given route
// and tenant, injecting it into the context using module-scoped context keys.
func (m *MultiPoolMiddleware) resolvePGConnection(
	ctx context.Context,
	route *PoolRoute,
	tenantID string,
	logger *logcompat.Logger,
	span trace.Span,
) (context.Context, error) {
	conn, err := route.pgPool.GetConnection(ctx, tenantID)
	if err != nil {
		logger.ErrorCtx(ctx, fmt.Sprintf("failed to get tenant PostgreSQL connection: module=%s, tenantID=%s, error=%v", route.module, tenantID, err))
		libOpentelemetry.HandleSpanError(span, "failed to get tenant PostgreSQL connection", err)

		return ctx, fmt.Errorf("%w: %w", core.ErrConnectionFailed, err)
	}

	db, err := conn.GetDB()
	if err != nil {
		logger.ErrorCtx(ctx, fmt.Sprintf("failed to get database from PostgreSQL connection: module=%s, tenantID=%s, error=%v", route.module, tenantID, err))
		libOpentelemetry.HandleSpanError(span, "failed to get database from PostgreSQL connection", err)

		return ctx, fmt.Errorf("%w: %w", core.ErrConnectionFailed, err)
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
	logger *logcompat.Logger,
) context.Context {
	for _, route := range m.routes {
		if route == matchedRoute || route.pgPool == nil || !route.pgPool.IsMultiTenant() {
			continue
		}

		ctx = m.resolveAndInjectCrossModule(ctx, route, tenantID, logger) //nolint:fatcontext // intentional accumulation of per-module connections into ctx across iterations
	}

	// Also resolve default route if it differs from matched
	if m.defaultRoute != nil && m.defaultRoute != matchedRoute &&
		m.defaultRoute.pgPool != nil && m.defaultRoute.pgPool.IsMultiTenant() {
		ctx = m.resolveAndInjectCrossModule(ctx, m.defaultRoute, tenantID, logger)
	}

	return ctx
}

// crossModuleErrorKey is a context key for storing cross-module resolution errors.
type crossModuleErrorKey struct{}

// ContextWithCrossModuleError stores a cross-module resolution error in context
// so downstream handlers can inspect it if needed.
func ContextWithCrossModuleError(ctx context.Context, err error) context.Context {
	return context.WithValue(ctx, crossModuleErrorKey{}, err)
}

// CrossModuleErrorFromContext retrieves the cross-module resolution error, if any.
func CrossModuleErrorFromContext(ctx context.Context) error {
	if err, ok := ctx.Value(crossModuleErrorKey{}).(error); ok {
		return err
	}

	return nil
}

// resolveAndInjectCrossModule resolves a single cross-module PG connection and
// injects it into the context. Errors are logged and stored in context for
// downstream visibility, but do not block the request.
func (m *MultiPoolMiddleware) resolveAndInjectCrossModule(
	ctx context.Context,
	route *PoolRoute,
	tenantID string,
	logger *logcompat.Logger,
) context.Context {
	conn, err := route.pgPool.GetConnection(ctx, tenantID)
	if err != nil {
		logger.WarnfCtx(ctx, "cross-module PG resolution failed: module=%s, tenantID=%s, error=%v",
			route.module, tenantID, err)

		return ContextWithCrossModuleError(ctx,
			fmt.Errorf("cross-module PG resolution failed for module %s: %w", route.module, err))
	}

	db, err := conn.GetDB()
	if err != nil {
		logger.WarnfCtx(ctx, "cross-module PG GetDB failed: module=%s, tenantID=%s, error=%v",
			route.module, tenantID, err)

		return ContextWithCrossModuleError(ctx,
			fmt.Errorf("cross-module PG GetDB failed for module %s: %w", route.module, err))
	}

	return core.ContextWithModulePGConnection(ctx, route.module, db)
}

// resolveMongoConnection resolves the MongoDB database for the given route
// and tenant, injecting it into the context.
func (m *MultiPoolMiddleware) resolveMongoConnection(
	ctx context.Context,
	route *PoolRoute,
	tenantID string,
	logger *logcompat.Logger,
	span trace.Span,
) (context.Context, error) {
	mongoDB, err := route.mongoPool.GetDatabaseForTenant(ctx, tenantID)
	if err != nil {
		logger.ErrorCtx(ctx, fmt.Sprintf("failed to get tenant MongoDB connection: module=%s, tenantID=%s, error=%v", route.module, tenantID, err))
		libOpentelemetry.HandleSpanError(span, "failed to get tenant MongoDB connection", err)

		return ctx, fmt.Errorf("%w: %w", core.ErrConnectionFailed, err)
	}

	ctx = core.ContextWithTenantMongo(ctx, mongoDB)

	return ctx, nil
}

// mapDefaultError delegates to the centralized mapDomainErrorToHTTP function
// to ensure consistent error-to-HTTP mapping across all middleware types.
func (m *MultiPoolMiddleware) mapDefaultError(c *fiber.Ctx, err error, tenantID string) error {
	return mapDomainErrorToHTTP(c, err, tenantID)
}

// Enabled returns whether the middleware is enabled.
// The middleware is enabled when at least one route has a multi-tenant PG or Mongo pool.
func (m *MultiPoolMiddleware) Enabled() bool {
	return m.enabled
}
