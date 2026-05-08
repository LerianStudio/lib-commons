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

const (
	errorResponseCodeKey    = "code"
	errorResponseTitleKey   = "title"
	errorResponseMessageKey = "message"
	serviceUnavailableCode  = "SERVICE_UNAVAILABLE"
	serviceUnavailableTitle = "Service Unavailable"
	serviceUnavailableMsg   = "Service temporarily unavailable"
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
			errorResponseCodeKey:    "TENANT_NOT_FOUND",
			errorResponseTitleKey:   "Tenant Not Found",
			errorResponseMessageKey: "tenant not found: " + tenantID,
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
		return serviceUnavailableError(c)
	}

	// Circuit breaker open -> 503
	if errors.Is(err, core.ErrCircuitBreakerOpen) {
		return serviceUnavailableError(c)
	}

	// Connection errors -> 503
	if errors.Is(err, core.ErrConnectionFailed) {
		return serviceUnavailableError(c)
	}

	// Default -> 500
	return internalServerError(c, "TENANT_DB_ERROR", "Failed to resolve tenant database")
}

// forbiddenError sends an HTTP 403 Forbidden response.
// Used when the tenant-service association exists but is not active (suspended or purged).
func forbiddenError(c *fiber.Ctx, code, title, message string) error {
	return c.Status(http.StatusForbidden).JSON(fiber.Map{
		errorResponseCodeKey:    code,
		errorResponseTitleKey:   title,
		errorResponseMessageKey: message,
	})
}

func serviceUnavailableError(c *fiber.Ctx) error {
	return c.Status(http.StatusServiceUnavailable).JSON(fiber.Map{
		errorResponseCodeKey:    serviceUnavailableCode,
		errorResponseTitleKey:   serviceUnavailableTitle,
		errorResponseMessageKey: serviceUnavailableMsg,
	})
}

// internalServerError sends an HTTP 500 Internal Server Error response.
func internalServerError(c *fiber.Ctx, code, title string) error {
	return c.Status(http.StatusInternalServerError).JSON(fiber.Map{
		errorResponseCodeKey:    code,
		errorResponseTitleKey:   title,
		errorResponseMessageKey: "Internal server error",
	})
}

// unauthorizedError sends an HTTP 401 Unauthorized response.
func unauthorizedError(c *fiber.Ctx, code, message string) error {
	return c.Status(http.StatusUnauthorized).JSON(fiber.Map{
		errorResponseCodeKey:    code,
		errorResponseTitleKey:   "Unauthorized",
		errorResponseMessageKey: message,
	})
}
