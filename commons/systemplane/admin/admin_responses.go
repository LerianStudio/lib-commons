// Response DTOs and the sentinel-to-HTTP translator for the admin surface.
//
// Split from admin.go to keep the handler file focused on routing and
// lifecycle; this file carries the data contracts and the single shared
// error-mapping helper consumed by handlePut, handlePutTenant, and
// handleDeleteTenant.

package admin

import (
	"encoding/json"
	"errors"
	"net/http"

	commonshttp "github.com/LerianStudio/lib-commons/v5/commons/net/http"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane"
	"github.com/gofiber/fiber/v2"
)

// ---------------------------------------------------------------------------
// Global-route DTOs
// ---------------------------------------------------------------------------

type listResponse struct {
	Namespace string          `json:"namespace"`
	Entries   []entryResponse `json:"entries"`
}

type entryResponse struct {
	Key         string `json:"key"`
	Value       any    `json:"value"`
	Description string `json:"description,omitempty"`
}

type getResponse struct {
	Namespace   string `json:"namespace"`
	Key         string `json:"key"`
	Value       any    `json:"value"`
	Description string `json:"description,omitempty"`
}

type putRequest struct {
	Value json.RawMessage `json:"value"`
}

// mapSentinelErr translates a Client error into a JSON error response with
// an appropriate HTTP status code. It handles both the legacy global-route
// sentinels (ErrUnknownKey, ErrValidation, ErrClosed, ErrNotStarted) and the
// tenant-scoped additions (ErrMissingTenantContext, ErrInvalidTenantID,
// ErrTenantScopeNotRegistered).
//
// The default branch returns 500 with a generic "internal_error" message to
// avoid leaking backend details through the wire response. Detailed error
// information stays in server-side logs (the Client's own telemetry path
// carries it; this helper deliberately does not log, to keep the admin
// package a thin HTTP adapter).
//
// Used by [mounter.handlePut], [mounter.handlePutTenant], and
// [mounter.handleDeleteTenant].
func mapSentinelErr(c *fiber.Ctx, err error) error {
	switch {
	case errors.Is(err, systemplane.ErrUnknownKey):
		// Preserved from the legacy handlePut behavior: 400 Bad Request.
		// Callers wishing to distinguish "not registered" from "malformed
		// body" use the "unknown_key" title string.
		return commonshttp.RespondError(c, http.StatusBadRequest, "unknown_key", "key is not registered")
	case errors.Is(err, systemplane.ErrValidation):
		return commonshttp.RespondError(c, http.StatusBadRequest, "validation_error", "value rejected by validator")
	case errors.Is(err, systemplane.ErrMissingTenantContext):
		// Defensive: handlers inject the tenant ID from :tenantID into ctx,
		// so this branch should never fire for tenant routes. A non-zero
		// rate would indicate a regression.
		return commonshttp.RespondError(c, http.StatusBadRequest, "missing_tenant_context", "tenant ID is required")
	case errors.Is(err, systemplane.ErrInvalidTenantID):
		return commonshttp.RespondError(c, http.StatusBadRequest, "invalid_tenant_id", "tenant ID is invalid")
	case errors.Is(err, systemplane.ErrTenantScopeNotRegistered):
		return commonshttp.RespondError(c, http.StatusBadRequest, "tenant_scope_not_registered",
			"key was not registered with RegisterTenantScoped; tenant overrides are not permitted")
	case errors.Is(err, systemplane.ErrNotStarted),
		errors.Is(err, systemplane.ErrClosed):
		return commonshttp.RespondError(c, http.StatusServiceUnavailable, "service_unavailable",
			"configuration service is not available")
	default:
		// Do not leak internal error details on the wire.
		return commonshttp.RespondError(c, fiber.StatusInternalServerError, "internal_error", "write failed")
	}
}
