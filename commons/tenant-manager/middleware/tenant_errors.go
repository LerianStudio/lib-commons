// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package middleware

import (
	"errors"
	"net/http"

	"github.com/LerianStudio/lib-commons/v7/commons"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/core"
	"github.com/gofiber/fiber/v3"
)

const (
	errorFieldCode    = "code"
	errorFieldTitle   = "title"
	errorFieldMessage = "message"

	errorCodeServiceUnavailable    = "SERVICE_UNAVAILABLE"
	errorTitleServiceUnavailable   = "Service Unavailable"
	errorMessageServiceUnavailable = "Service temporarily unavailable"
)

// refusalError is a refusal handed to the app's ErrorHandler. It unwraps to the
// *fiber.Error (status, message), the commons.Response (code, title, message) and
// the domain error that caused it.
type refusalError struct {
	fiberErr *fiber.Error
	response commons.Response
	cause    error
}

func (e refusalError) Error() string { return e.fiberErr.Error() }

func (e refusalError) Unwrap() []error { return []error{e.fiberErr, e.response, e.cause} }

func newRefusal(cause error, status int, code, title, message string) refusalError {
	return refusalError{
		fiberErr: fiber.NewError(status, message),
		response: commons.Response{Code: code, Title: title, Message: message},
		cause:    cause,
	}
}

// refuse ends the chain: it writes the refusal body, or returns the refusal
// untouched when WithRefusalsToErrorHandler hands rendering to the app.
func (m *TenantMiddleware) refuse(c fiber.Ctx, r refusalError) error {
	if m.refusalsToErrorHandler {
		return r
	}

	return c.Status(r.fiberErr.Code).JSON(fiber.Map{
		errorFieldCode:    r.response.Code,
		errorFieldTitle:   r.response.Title,
		errorFieldMessage: r.response.Message,
	})
}

// domainRefusal maps a tenant resolution error to the refusal TenantMiddleware
// answers it with, keeping status codes consistent for domain errors.
func domainRefusal(err error, tenantID string) refusalError {
	// Missing token or JWT errors -> 401
	if errors.Is(err, core.ErrAuthorizationTokenRequired) ||
		errors.Is(err, core.ErrInvalidAuthorizationToken) ||
		errors.Is(err, core.ErrInvalidTenantClaims) ||
		errors.Is(err, core.ErrMissingTenantIDClaim) {
		return unauthorizedRefusal(err, "UNAUTHORIZED", "Unauthorized")
	}

	// Tenant not found -> 404
	if errors.Is(err, core.ErrTenantNotFound) {
		return newRefusal(err, http.StatusNotFound, "TENANT_NOT_FOUND", "Tenant Not Found",
			"tenant not found: "+tenantID)
	}

	// Tenant suspended/purged -> 403
	var suspErr *core.TenantSuspendedError
	if errors.As(err, &suspErr) {
		return newRefusal(err, http.StatusForbidden, "0131", "Service Suspended",
			"tenant service is "+suspErr.Status)
	}

	// Generic access denied (403 without parsed status) -> 403
	if errors.Is(err, core.ErrTenantServiceAccessDenied) {
		return newRefusal(err, http.StatusForbidden, "0131", "Access Denied",
			"tenant service access denied")
	}

	// Manager closed or service not configured -> 503
	if errors.Is(err, core.ErrManagerClosed) || errors.Is(err, core.ErrServiceNotConfigured) {
		return serviceUnavailableRefusal(err)
	}

	// Circuit breaker open -> 503
	if errors.Is(err, core.ErrCircuitBreakerOpen) {
		return serviceUnavailableRefusal(err)
	}

	// Connection errors -> 503
	if errors.Is(err, core.ErrConnectionFailed) {
		return serviceUnavailableRefusal(err)
	}

	// Default -> 500
	return newRefusal(err, http.StatusInternalServerError, "TENANT_DB_ERROR",
		"Failed to resolve tenant database", "Internal server error")
}

func serviceUnavailableRefusal(cause error) refusalError {
	return newRefusal(cause, http.StatusServiceUnavailable, errorCodeServiceUnavailable,
		errorTitleServiceUnavailable, errorMessageServiceUnavailable)
}

func unauthorizedRefusal(cause error, code, message string) refusalError {
	return newRefusal(cause, http.StatusUnauthorized, code, "Unauthorized", message)
}
