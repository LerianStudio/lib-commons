// Tenant-scoped admin route handlers.
//
// This file contains the three handlers mounted at:
//
//	GET    /<prefix>/:namespace/:key/tenants             — list tenants with an override
//	PUT    /<prefix>/:namespace/:key/tenants/:tenantID   — write a tenant-scoped override
//	DELETE /<prefix>/:namespace/:key/tenants/:tenantID   — remove a tenant-scoped override
//
// The sibling file [admin.go] holds the Mount entrypoint, the mountConfig,
// and the three legacy global-route handlers. These two files share:
//
//   - mounter (receiver) — defined in admin.go
//   - mapSentinelErr (error-to-HTTP translation) — defined in admin.go
//   - authorizeTenant (middleware) — defined in admin.go
//
// See admin.go's package doc and [WithTenantAuthorizer] for the default-deny
// escalation rationale for tenant-route authorization.

package admin

import (
	"encoding/json"
	"errors"
	"fmt"

	commonshttp "github.com/LerianStudio/lib-commons/v5/commons/net/http"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane"
	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	"github.com/gofiber/fiber/v2"
)

// ---------------------------------------------------------------------------
// Tenant DTOs
// ---------------------------------------------------------------------------

// listTenantsResponse is the response body for GET :key/tenants.
type listTenantsResponse struct {
	Namespace string   `json:"namespace"`
	Key       string   `json:"key"`
	Tenants   []string `json:"tenants"`
}

// tenantValueResponse is the response body for PUT :key/tenants/:tenantID.
// It echoes the written value (redaction-applied) so the caller can confirm
// the post-write state without a follow-up GET.
type tenantValueResponse struct {
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
	TenantID  string `json:"tenant_id"`
	Value     any    `json:"value"`
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// handleListTenants returns a sorted list of tenant IDs with an override for
// (namespace, key). The underlying Client.ListTenantsForKey swallows backend
// errors and returns an empty slice; the admin surface mirrors that
// contract — a degraded backend produces an empty list rather than a 5xx.
//
// The route does not require a :tenantID path segment; the authorizer is
// invoked with tenantID="" so policies can distinguish the reflection-style
// "list tenants" action from per-tenant operations.
func (m *mounter) handleListTenants(c *fiber.Ctx) error {
	namespace := c.Params("namespace")
	key := c.Params("key")

	tenants := m.client.ListTenantsForKey(namespace, key)

	// Client returns sorted, deduplicated results; the response mirrors that.
	// Coalesce a nil return to an empty slice so JSON encodes `[]` not `null`.
	if tenants == nil {
		tenants = []string{}
	}

	return c.Status(fiber.StatusOK).JSON(listTenantsResponse{
		Namespace: namespace,
		Key:       key,
		Tenants:   tenants,
	})
}

// handlePutTenant writes a tenant-specific override for (namespace, key).
// The :tenantID URL segment is validated before authorization to avoid
// leaking "tenant ID validity" through the 403/400 boundary (all callers
// see the same 400 for malformed IDs regardless of auth posture).
//
// The request body mirrors handlePut's shape:
//
//	{"value": <any JSON value>}
//
// On success the response carries the just-written value with redaction
// applied per the key's RedactPolicy, so the caller can verify the
// post-write state without a follow-up GET.
func (m *mounter) handlePutTenant(c *fiber.Ctx) error {
	tenantID := c.Params("tenantID")

	// Validate BEFORE parsing body and BEFORE authorization. core.IsValidTenantID
	// enforces a conservative regex; the "_global" sentinel is rejected here
	// too (Client.extractTenantID catches it as a defense-in-depth, but the
	// admin layer rejects earlier with a clearer error code).
	if err := validateTenantIDParam(tenantID); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(commonshttp.ErrorResponse{
			Code:    fiber.StatusBadRequest,
			Title:   "invalid_tenant_id",
			Message: err.Error(),
		})
	}

	namespace := c.Params("namespace")
	key := c.Params("key")

	var body putRequest
	if err := c.BodyParser(&body); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(commonshttp.ErrorResponse{
			Code:    fiber.StatusBadRequest,
			Title:   "bad_request",
			Message: "invalid request body",
		})
	}

	if body.Value == nil {
		return c.Status(fiber.StatusBadRequest).JSON(commonshttp.ErrorResponse{
			Code:    fiber.StatusBadRequest,
			Title:   "bad_request",
			Message: "missing value field",
		})
	}

	var value any
	if err := json.Unmarshal(body.Value, &value); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(commonshttp.ErrorResponse{
			Code:    fiber.StatusBadRequest,
			Title:   "bad_request",
			Message: "invalid value",
		})
	}

	// Inject the tenant ID into the downstream context so Client.SetForTenant
	// can extract it via core.GetTenantIDContext. The admin layer is the
	// authoritative source of the tenant ID — it comes from the URL path,
	// not from any ambient header or middleware (which could have been
	// populated by an earlier tenant-discovery layer that set a DIFFERENT
	// tenant). This keeps the admin surface independent of any particular
	// host-auth scheme.
	ctx := core.ContextWithTenantID(c.UserContext(), tenantID)
	actor := m.cfg.actorExtractor(c)

	if err := m.client.SetForTenant(ctx, namespace, key, value, actor); err != nil {
		return mapSentinelErr(c, err)
	}

	// Echo the just-written value. Use the same redaction policy applied by
	// the legacy GET handler so sensitive values never appear in the
	// response, even at the moment of writing them.
	policy := m.client.KeyRedaction(namespace, key)

	// We already have the canonical value pre-write; however, the Client
	// applies a JSON round-trip during SetForTenant and that canonical form
	// is what reads will observe. Perform a GetForTenant here so the
	// response matches what a subsequent read would return. Ignoring the
	// error path is deliberate: if the write succeeded but the read failed
	// (e.g. a transient backend blip for a lazy-mode Client), we still
	// report the write as successful, echoing the caller's submitted value
	// with redaction applied as a best effort.
	displayValue := value

	if v, found, _ := m.client.GetForTenant(ctx, namespace, key); found {
		displayValue = v
	}

	redacted := systemplane.ApplyRedaction(displayValue, policy)

	return c.Status(fiber.StatusOK).JSON(tenantValueResponse{
		Namespace: namespace,
		Key:       key,
		TenantID:  tenantID,
		Value:     redacted,
	})
}

// handleDeleteTenant removes a tenant-specific override. The response is a
// 204 No Content on success, matching the REST convention for idempotent
// deletes. The Client's DeleteForTenant is itself idempotent (deleting a
// non-existent override is not an error), so repeated calls always produce
// 204 without cascading error paths.
func (m *mounter) handleDeleteTenant(c *fiber.Ctx) error {
	tenantID := c.Params("tenantID")

	if err := validateTenantIDParam(tenantID); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(commonshttp.ErrorResponse{
			Code:    fiber.StatusBadRequest,
			Title:   "invalid_tenant_id",
			Message: err.Error(),
		})
	}

	namespace := c.Params("namespace")
	key := c.Params("key")

	ctx := core.ContextWithTenantID(c.UserContext(), tenantID)
	actor := m.cfg.actorExtractor(c)

	if err := m.client.DeleteForTenant(ctx, namespace, key, actor); err != nil {
		return mapSentinelErr(c, err)
	}

	return c.SendStatus(fiber.StatusNoContent)
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// validateTenantIDParam enforces the same constraints the Client applies at
// extractTenantID — a char whitelist regex and a rejection of the "_global"
// sentinel. Rejecting at the admin layer produces a clearer error code and
// avoids a round-trip through the Client before the failure surfaces.
//
// The "_global" check here is defense-in-depth: Client.extractTenantID also
// rejects it, but callers should never be able to reach that path through
// this admin surface.
func validateTenantIDParam(tenantID string) error {
	if tenantID == "" {
		return errors.New("tenantID must not be empty")
	}

	// The "_global" sentinel collides with Client's shared-row marker.
	// Reject it explicitly so the error message is obvious; otherwise
	// IsValidTenantID would flag it via the leading-underscore rule anyway.
	if tenantID == "_global" {
		return fmt.Errorf("tenantID must not be the %q sentinel", "_global")
	}

	if !core.IsValidTenantID(tenantID) {
		return fmt.Errorf("tenantID must match ^[a-zA-Z0-9][a-zA-Z0-9_-]*$ and be at most %d characters", core.MaxTenantIDLength)
	}

	return nil
}
