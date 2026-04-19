// Package admin provides Fiber HTTP handlers for inspecting and modifying
// systemplane configuration entries at runtime.
//
// Mount registers these routes on a Fiber router:
//
//	GET    /<prefix>/:namespace                              - list entries in a namespace
//	GET    /<prefix>/:namespace/:key                         - read a single entry
//	PUT    /<prefix>/:namespace/:key                         - write a single entry
//	GET    /<prefix>/:namespace/:key/tenants                 - list tenants with an override for (namespace, key)
//	PUT    /<prefix>/:namespace/:key/tenants/:tenantID       - write a tenant-scoped override
//	DELETE /<prefix>/:namespace/:key/tenants/:tenantID       - remove a tenant-scoped override
//
// The default path prefix is "/system".
//
// Authorization is deny-all by default on every route. The three legacy
// global routes use [WithAuthorizer]; the three tenant-scoped routes use
// [WithTenantAuthorizer]. Tenant routes do NOT fall back to WithAuthorizer
// when WithTenantAuthorizer is absent — the legacy hook does not know about
// tenants, so silent reuse would let a service accept tenant writes it was
// never authorized to handle. See [WithTenantAuthorizer] for the full
// rationale.
package admin

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
	commonshttp "github.com/LerianStudio/lib-commons/v5/commons/net/http"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane"
	"github.com/gofiber/fiber/v2"
)

// mountConfig holds options applied by MountOption functions.
type mountConfig struct {
	pathPrefix       string
	authorizer       func(*fiber.Ctx, string) error
	tenantAuthorizer func(*fiber.Ctx, string, string) error
	actorExtractor   func(*fiber.Ctx) string
	logger           log.Logger
}

// defaultMountConfig returns sensible defaults.
//
// Both authorizer hooks default to deny-all. The legacy global routes use
// [WithAuthorizer]; the tenant-scoped routes use [WithTenantAuthorizer].
// Tenant routes intentionally do NOT fall back to [WithAuthorizer] when
// [WithTenantAuthorizer] is absent — see [WithTenantAuthorizer] for the
// rationale behind this conservative escalation.
func defaultMountConfig() mountConfig {
	return mountConfig{
		pathPrefix: "/system",
		authorizer: func(_ *fiber.Ctx, _ string) error {
			return errors.New("admin: no authorizer configured — use admin.WithAuthorizer to set one")
		},
		tenantAuthorizer: func(_ *fiber.Ctx, _, _ string) error {
			return errors.New("admin: WithTenantAuthorizer not configured; tenant routes default to deny-all")
		},
		actorExtractor: func(_ *fiber.Ctx) string { return "" },
		logger:         log.NewNop(),
	}
}

// MountOption configures the admin route mount.
type MountOption func(*mountConfig)

// WithPathPrefix overrides the URL prefix for admin routes. Default: "/system".
func WithPathPrefix(p string) MountOption {
	return func(cfg *mountConfig) {
		if p != "" {
			cfg.pathPrefix = p
		}
	}
}

// WithAuthorizer sets an authorization check called before each legacy
// global-route handler. The action argument is "read" for GET requests and
// "write" for PUT requests. Return a non-nil error to reject the request
// with 403 Forbidden.
//
// Callers MUST supply a WithAuthorizer option to enable access. The default
// authorizer is deny-all: every global-route request returns 403 until a
// custom authorizer is provided.
//
// Note: WithAuthorizer does NOT apply to tenant-scoped routes
// (`:key/tenants` and `:key/tenants/:tenantID`). Tenant routes use
// [WithTenantAuthorizer] exclusively. See its docs for the rationale.
func WithAuthorizer(fn func(*fiber.Ctx, string) error) MountOption {
	return func(cfg *mountConfig) {
		if fn != nil {
			cfg.authorizer = fn
		}
	}
}

// WithTenantAuthorizer installs an authorization hook invoked before every
// tenant-scoped admin route handler. The function receives the Fiber context,
// the action ("read" | "write"), and the tenant ID from the URL path
// (:tenantID). For the tenant-list route (GET :key/tenants) the tenantID
// argument is empty. Returning a non-nil error aborts the request with 403
// Forbidden.
//
// Default-deny escalation: if WithTenantAuthorizer is NOT configured, every
// tenant route returns 403 Forbidden regardless of any WithAuthorizer hook.
// This is a deliberate conservative choice:
//
//  1. [WithAuthorizer] predates tenant support. Its signature
//     (*fiber.Ctx, action string) carries no tenant information, so it
//     cannot express policies like "only allow writes to tenants the caller
//     owns".
//  2. Silently falling back to WithAuthorizer on tenant routes would let a
//     service that configured only WithAuthorizer accept tenant writes it
//     was never authorized to handle — a silent privilege escalation.
//  3. Forcing consumers to opt in with WithTenantAuthorizer makes the
//     upgrade conscious and auditable.
//
// Migration: services already using WithAuthorizer for the legacy routes
// should add WithTenantAuthorizer when they want to expose tenant routes.
// The two hooks coexist; the legacy routes continue to use WithAuthorizer
// unchanged.
func WithTenantAuthorizer(fn func(c *fiber.Ctx, action, tenantID string) error) MountOption {
	return func(cfg *mountConfig) {
		if fn != nil {
			cfg.tenantAuthorizer = fn
		}
	}
}

// WithActorExtractor sets a function that extracts the actor identity from a
// Fiber request context. The returned string is passed as the actor argument
// to [systemplane.Client.Set].
func WithActorExtractor(fn func(*fiber.Ctx) string) MountOption {
	return func(cfg *mountConfig) {
		if fn != nil {
			cfg.actorExtractor = fn
		}
	}
}

// WithLogger attaches a logger used by admin handlers to record non-fatal
// observations (e.g. a transient GetForTenant miss after a successful
// SetForTenant in [mounter.handlePutTenant]). Defaults to a nop logger.
// Useful for operators who want a debug-level trail when tenant PUT
// responses fall back to echoing the submitted value.
func WithLogger(l log.Logger) MountOption {
	return func(cfg *mountConfig) {
		if l != nil {
			cfg.logger = l
		}
	}
}

// mounter binds a Client and its config together for handler generation.
type mounter struct {
	client *systemplane.Client
	cfg    mountConfig
}

// Mount registers the admin HTTP routes on router using the given Client.
// Nil client causes Mount to be a no-op (does not panic).
//
// By default, all routes are deny-all (every request returns 403 Forbidden).
// Callers must supply [WithAuthorizer] to enable access.
func Mount(router fiber.Router, c *systemplane.Client, opts ...MountOption) {
	if c == nil || router == nil {
		return
	}

	cfg := defaultMountConfig()
	for _, o := range opts {
		o(&cfg)
	}

	// Normalize the prefix: ensure it starts with "/" and does not end with "/".
	prefix := cfg.pathPrefix
	if !strings.HasPrefix(prefix, "/") {
		prefix = "/" + prefix
	}

	prefix = strings.TrimRight(prefix, "/")

	m := &mounter{client: c, cfg: cfg}

	// Legacy global routes — use WithAuthorizer.
	router.Get(prefix+"/:namespace", m.authorize("read"), m.handleList)
	router.Get(prefix+"/:namespace/:key", m.authorize("read"), m.handleGetOne)
	router.Put(prefix+"/:namespace/:key", m.authorize("write"), m.handlePut)

	// Tenant-scoped routes — use WithTenantAuthorizer (default-deny when absent).
	// The tenant-list route carries no :tenantID segment; the authorizer
	// receives "" for the tenantID argument in that case.
	router.Get(prefix+"/:namespace/:key/tenants", m.authorizeTenant("read"), m.handleListTenants)
	router.Put(prefix+"/:namespace/:key/tenants/:tenantID", m.authorizeTenant("write"), m.handlePutTenant)
	router.Delete(prefix+"/:namespace/:key/tenants/:tenantID", m.authorizeTenant("write"), m.handleDeleteTenant)
}

// authorize returns a per-route middleware that checks the configured authorizer.
func (m *mounter) authorize(action string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if err := m.cfg.authorizer(c, action); err != nil {
			return c.Status(fiber.StatusForbidden).JSON(commonshttp.ErrorResponse{
				Code:    fiber.StatusForbidden,
				Title:   "forbidden",
				Message: err.Error(),
			})
		}

		return c.Next()
	}
}

// authorizeTenant returns a per-route middleware that checks the tenant
// authorizer for tenant-scoped routes. The `:tenantID` URL param is passed
// through to the hook; for the tenant-list route it is empty. When
// [WithTenantAuthorizer] has not been configured, the default deny-all
// hook rejects every request with 403.
func (m *mounter) authorizeTenant(action string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		tenantID := c.Params("tenantID")
		if err := m.cfg.tenantAuthorizer(c, action, tenantID); err != nil {
			return c.Status(fiber.StatusForbidden).JSON(commonshttp.ErrorResponse{
				Code:    fiber.StatusForbidden,
				Title:   "forbidden",
				Message: err.Error(),
			})
		}

		return c.Next()
	}
}

// ---------------------------------------------------------------------------
// DTOs
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

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// handleList returns all entries in a namespace with redaction applied.
func (m *mounter) handleList(c *fiber.Ctx) error {
	namespace := c.Params("namespace")

	entries := m.client.List(namespace)

	resp := listResponse{
		Namespace: namespace,
		Entries:   make([]entryResponse, 0, len(entries)),
	}

	for _, e := range entries {
		policy := m.client.KeyRedaction(namespace, e.Key)
		redacted := systemplane.ApplyRedaction(e.Value, policy)

		resp.Entries = append(resp.Entries, entryResponse{
			Key:         e.Key,
			Value:       redacted,
			Description: e.Description,
		})
	}

	return c.Status(fiber.StatusOK).JSON(resp)
}

// handleGetOne returns a single entry with redaction applied.
func (m *mounter) handleGetOne(c *fiber.Ctx) error {
	namespace := c.Params("namespace")
	key := c.Params("key")

	value, ok := m.client.Get(namespace, key)
	if !ok {
		return c.Status(fiber.StatusNotFound).JSON(commonshttp.ErrorResponse{
			Code:    fiber.StatusNotFound,
			Title:   "not_found",
			Message: "key not found",
		})
	}

	policy := m.client.KeyRedaction(namespace, key)
	redacted := systemplane.ApplyRedaction(value, policy)

	return c.Status(fiber.StatusOK).JSON(getResponse{
		Namespace:   namespace,
		Key:         key,
		Value:       redacted,
		Description: m.client.KeyDescription(namespace, key),
	})
}

// handlePut writes a new value for a single key.
func (m *mounter) handlePut(c *fiber.Ctx) error {
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

	actor := m.cfg.actorExtractor(c)

	err := m.client.Set(c.UserContext(), namespace, key, value, actor)
	if err == nil {
		return c.SendStatus(fiber.StatusNoContent)
	}

	return mapSentinelErr(c, err)
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
		return c.Status(fiber.StatusBadRequest).JSON(commonshttp.ErrorResponse{
			Code:    fiber.StatusBadRequest,
			Title:   "unknown_key",
			Message: "key is not registered",
		})
	case errors.Is(err, systemplane.ErrValidation):
		return c.Status(fiber.StatusBadRequest).JSON(commonshttp.ErrorResponse{
			Code:    fiber.StatusBadRequest,
			Title:   "validation_error",
			Message: "value rejected by validator",
		})
	case errors.Is(err, systemplane.ErrMissingTenantContext):
		// Defensive: handlers inject the tenant ID from :tenantID into ctx,
		// so this branch should never fire for tenant routes. A non-zero
		// rate would indicate a regression.
		return c.Status(fiber.StatusBadRequest).JSON(commonshttp.ErrorResponse{
			Code:    fiber.StatusBadRequest,
			Title:   "missing_tenant_context",
			Message: "tenant ID is required",
		})
	case errors.Is(err, systemplane.ErrInvalidTenantID):
		return c.Status(fiber.StatusBadRequest).JSON(commonshttp.ErrorResponse{
			Code:    fiber.StatusBadRequest,
			Title:   "invalid_tenant_id",
			Message: "tenant ID is invalid",
		})
	case errors.Is(err, systemplane.ErrTenantScopeNotRegistered):
		return c.Status(fiber.StatusBadRequest).JSON(commonshttp.ErrorResponse{
			Code:    fiber.StatusBadRequest,
			Title:   "tenant_scope_not_registered",
			Message: "key was not registered with RegisterTenantScoped; tenant overrides are not permitted",
		})
	case errors.Is(err, systemplane.ErrNotStarted),
		errors.Is(err, systemplane.ErrClosed):
		return c.Status(fiber.StatusServiceUnavailable).JSON(commonshttp.ErrorResponse{
			Code:    fiber.StatusServiceUnavailable,
			Title:   "service_unavailable",
			Message: "configuration service is not available",
		})
	default:
		// Do not leak internal error details on the wire.
		return c.Status(fiber.StatusInternalServerError).JSON(commonshttp.ErrorResponse{
			Code:    fiber.StatusInternalServerError,
			Title:   "internal_error",
			Message: "write failed",
		})
	}
}
