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
//   - mapSentinelErr (error-to-HTTP translation) — defined in admin_responses.go
//   - authorizeTenant (middleware) — defined in admin.go
//
// See admin.go's package doc and [WithTenantAuthorizer] for the default-deny
// escalation rationale for tenant-route authorization.

package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

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
// (namespace, key).
//
// The handler distinguishes the following cases via [systemplane.Client.KeyStatus]:
//
//   - Unregistered key                        → 404 not_found
//   - Registered but not tenant-scoped        → 400 validation_error
//   - Tenant-scoped, no overrides configured  → 200 {"tenants":[]}
//   - Tenant-scoped, with overrides           → 200 {"tenants":[...]}
//
// The tenant-scoped empty-list case returns 200 with a DEBUG-level log line
// so operators can distinguish "legitimately empty" from the error paths
// above without relying on wire-level status codes alone.
//
// Backend errors from ListTenantsForKey (which swallows them and returns an
// empty slice) do NOT surface as 5xx here; the Client is the authoritative
// error boundary and we preserve its fail-soft contract.
//
// The route does not require a :tenantID path segment; the authorizer is
// invoked with tenantID="" so policies can distinguish the reflection-style
// "list tenants" action from per-tenant operations.
func (m *mounter) handleListTenants(c *fiber.Ctx) error {
	namespace := c.Params("namespace")
	key := c.Params("key")

	registered, tenantScoped := m.client.KeyStatus(namespace, key)

	switch {
	case !registered:
		return commonshttp.RespondError(c, http.StatusNotFound, "not_found", "key not found")
	case !tenantScoped:
		return commonshttp.RespondError(c, http.StatusBadRequest, "validation_error", "key is not tenant-scoped")
	}

	tenants := m.client.ListTenantsForKey(namespace, key)

	// Client returns sorted, deduplicated results; the response mirrors that.
	// Coalesce a nil return to an empty slice so JSON encodes `[]` not `null`.
	if tenants == nil {
		tenants = []string{}
	}

	if len(tenants) == 0 {
		// Operator observability for the 200-[] case. The empty response
		// is the correct security posture (no side-channel leakage) but
		// operators debugging "why does my tenant not appear" benefit
		// from a trail showing the handler served the request
		// successfully with zero overrides.
		m.logger.Log(c.UserContext(), log.LevelDebug, "admin: handleListTenants returned empty overrides",
			log.String("namespace", namespace),
			log.String("key", key),
			log.String("reason", "no_overrides"),
		)
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
		return commonshttp.RespondError(c, http.StatusBadRequest, "invalid_tenant_id", err.Error())
	}

	namespace := c.Params("namespace")
	key := c.Params("key")

	var body putRequest
	if err := c.BodyParser(&body); err != nil {
		return commonshttp.RespondError(c, http.StatusBadRequest, "bad_request", "invalid request body")
	}

	if body.Value == nil {
		return commonshttp.RespondError(c, http.StatusBadRequest, "bad_request", "missing value field")
	}

	var value any
	if err := json.Unmarshal(body.Value, &value); err != nil {
		return commonshttp.RespondError(c, http.StatusBadRequest, "bad_request", "invalid value")
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
		m.logger.Log(ctx, log.LevelDebug, "handlePutTenant: GetForTenant failed after successful write, echoing submitted value",
			log.String("namespace", namespace),
			log.String("key", key),
			log.String("tenant_id", tenantID),
			log.Err(getErr),
		)
	case !found:
		// Defense in depth: GetForTenant returned no error but reported the
		// row as absent immediately after a successful SetForTenant. This
		// is unreachable under the Client's current contract (write-through
		// cache makes the row visible synchronously), but if a future
		// backend refactor changes that invariant we want a loud signal
		// rather than silently echoing stale state. The write is already
		// durable, so we return a 500 with a generic message.
		m.logger.Log(ctx, log.LevelWarn,
			"handlePutTenant: GetForTenant returned nil error but not found — unexpected post-write state",
			log.String("namespace", namespace),
			log.String("key", key),
			log.String("tenant_id", tenantID),
		)

		return commonshttp.RespondError(c, fiber.StatusInternalServerError, "internal_error", "write succeeded but post-write read failed")
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
		return commonshttp.RespondError(c, http.StatusBadRequest, "invalid_tenant_id", err.Error())
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
