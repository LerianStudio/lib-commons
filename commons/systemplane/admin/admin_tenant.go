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

	"github.com/LerianStudio/lib-commons/v5/commons/log"
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
//
// Authorization runs first via the route middleware (see admin.go Mount).
// When the authorizer denies a request, a malformed :tenantID still
// produces 403 (not 400) — which is the correct security posture: never
// tell unauthorized callers whether a tenant ID is syntactically valid.
// In-handler validation via validateTenantIDParam catches malformed IDs
// on authorized paths and returns a uniform 400 regardless of whether
// the value is the "_global" sentinel or any other rejected shape.
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

	// Validate the :tenantID after authorization (authorizeTenant runs as
	// Fiber middleware before this handler; see admin.go Mount). Authorization
	// failure surfaces as 403 before any tenantID inspection. On authorized
	// paths, the regex-based validator below rejects malformed IDs — including
	// "_global" — with a uniform 400 error that does not leak the sentinel
	// name.
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
	// response matches what a subsequent read would return. On read failure
	// we still report the write as successful and echo the caller's
	// submitted value with redaction — best-effort, because the durable
	// state is already committed by the SetForTenant above. The read error
	// is logged at Debug level so operators have a trail when diagnosing
	// transient backend blips (e.g. a lazy-mode Client whose GetForTenant
	// timed out against the store).
	displayValue := value

	v, found, getErr := m.client.GetForTenant(ctx, namespace, key)
	switch {
	case getErr != nil:
		m.cfg.logger.Log(ctx, log.LevelDebug, "handlePutTenant: GetForTenant failed after successful write, echoing submitted value",
			log.String("namespace", namespace),
			log.String("key", key),
			log.String("tenant_id", tenantID),
			log.Err(getErr),
		)
	case found:
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
// extractTenantID — a char whitelist regex enforced by core.IsValidTenantID.
// Rejecting at the admin layer produces a clearer HTTP status (400 instead
// of round-tripping through the Client) and emits a uniform error message
// regardless of whether the caller submitted the "_global" sentinel, a
// leading hyphen, or any other malformed value — the admin surface
// deliberately does NOT distinguish "_global specifically" from generic
// regex rejection, so unauthorized callers cannot use error variants to
// probe for the sentinel's literal name.
func validateTenantIDParam(tenantID string) error {
	if tenantID == "" {
		return errors.New("tenantID must not be empty")
	}

	// core.IsValidTenantID rejects "_global" (leading underscore is not
	// alphanumeric) along with every other malformed value. A uniform
	// error keeps the sentinel's literal name out of the wire response.
	if !core.IsValidTenantID(tenantID) {
		return fmt.Errorf("tenantID must match ^[a-zA-Z0-9][a-zA-Z0-9_-]*$ and be at most %d characters", core.MaxTenantIDLength)
	}

	return nil
}
