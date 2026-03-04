// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package middleware

import (
	"fmt"

	"github.com/gofiber/fiber/v2"
)

// WithTenantRoute composes a handler chain that guarantees authentication runs
// BEFORE tenant resolution. This prevents forged JWTs from triggering Tenant
// Manager API calls before the token signature is validated.
//
// When tenantMiddleware is nil (single-tenant mode), only the authHandler is
// returned. The caller appends route-specific handlers after the returned slice.
//
// Usage:
//
//	f.Post("/v1/resources",
//	    append(WithTenantRoute(auth.Authorize("app", "resource", "post"), tenantMid.WithTenantDB),
//	        resourceHandler.Create)...)
func WithTenantRoute(authHandler fiber.Handler, tenantMiddleware fiber.Handler) []fiber.Handler {
	if authHandler == nil {
		panic("middleware: authHandler must not be nil")
	}

	if tenantMiddleware == nil {
		return []fiber.Handler{authHandler}
	}

	return []fiber.Handler{authHandler, tenantMiddleware}
}

// WithTenantRouteChain is like WithTenantRoute but accepts multiple pre-tenant
// handlers (e.g., auth + rate-limiter). All pre-handlers run before tenant
// resolution.
//
// When tenantMiddleware is nil, only the pre-handlers are returned.
//
// Usage:
//
//	f.Post("/v1/resources",
//	    append(WithTenantRouteChain(tenantMid.WithTenantDB,
//	        auth.Authorize("app", "resource", "post"),
//	        rateLimiter.Handle),
//	        resourceHandler.Create)...)
func WithTenantRouteChain(tenantMiddleware fiber.Handler, preHandlers ...fiber.Handler) []fiber.Handler {
	for i, h := range preHandlers {
		if h == nil {
			panic(fmt.Sprintf("middleware: preHandlers[%d] must not be nil", i))
		}
	}

	if tenantMiddleware == nil {
		handlers := make([]fiber.Handler, len(preHandlers))
		copy(handlers, preHandlers)

		return handlers
	}

	handlers := make([]fiber.Handler, 0, len(preHandlers)+1)
	handlers = append(handlers, preHandlers...)
	handlers = append(handlers, tenantMiddleware)

	return handlers
}
