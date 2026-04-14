// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package middleware

import (
	"errors"
	"net/http"

	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	"github.com/gofiber/fiber/v2"
)

// mapDomainErrorToHTTP is a centralized error-to-HTTP mapping function used by
// TenantMiddleware to ensure consistent status codes for domain errors.
func mapDomainErrorToHTTP(c *fiber.Ctx, err error, tenantID string) error {
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
			"title":   "Tenant Not Found",
			"message": "tenant not found: " + tenantID,
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
			"message": "Service temporarily unavailable",
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
