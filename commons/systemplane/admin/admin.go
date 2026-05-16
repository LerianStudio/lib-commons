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
// Authorization is deny-all by default on every route. Legacy routes deny-all
// when [WithAuthorizer] is absent; tenant routes ALSO deny-all when
// [WithTenantAuthorizer] is absent — the two authorizers are independent by
// design (silent privilege-escalation prevention). Configuring only
// [WithAuthorizer] does NOT implicitly grant access to tenant routes, and
// configuring only [WithTenantAuthorizer] does NOT implicitly grant access
// to the legacy global routes. See [WithTenantAuthorizer] for the full
// rationale.
package admin

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/LerianStudio/lib-observability/log"
	commonshttp "github.com/LerianStudio/lib-commons/v5/commons/net/http"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane"
	"github.com/gofiber/fiber/v2"
)

// Boundary limits for path segments. Enforced at the HTTP edge so malformed
// inputs never reach the Client layer. The caps are generous relative to any
// realistic namespace/key but short enough to prevent denial-of-service via
// pathologically long URLs.
const (
	maxNamespaceLen = 256
	maxKeyLen       = 512
)

// mountConfig holds options applied by MountOption functions.
type mountConfig struct {
	pathPrefix       string
	authorizer       func(*fiber.Ctx, string) error
	tenantAuthorizer func(*fiber.Ctx, string, string) error
	actorExtractor   func(*fiber.Ctx) string
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

// mounter binds a Client and its config together for handler generation.
type mounter struct {
	client *systemplane.Client
	cfg    mountConfig
	logger log.Logger // snapshot of client.Logger() at Mount time
}

// Mount registers the admin HTTP routes on router using the given Client.
// Nil client causes Mount to be a no-op (does not panic). Nil router causes
// Mount to be a no-op (does not panic).
//
// Mount panics if called with a nil [MountOption] — this is the conventional
// functional-options fail-fast contract and matches the systemplane Client's
// own Option handling.
//
// By default, all routes are deny-all (every request returns 403 Forbidden).
// Callers must supply [WithAuthorizer] to enable access to legacy routes and
// [WithTenantAuthorizer] to enable access to tenant routes (the two are
// independent; see [WithTenantAuthorizer]).
//
// The logger used by admin handlers for non-fatal observations is inherited
// from the Client (via [systemplane.WithLogger]); there is no separate
// admin-level logger option.
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

	m := &mounter{client: c, cfg: cfg, logger: c.Logger()}

	// Legacy global routes — use WithAuthorizer.
	router.Get(prefix+"/:namespace", m.validateNamespaceParam, m.authorize("read"), m.handleList)
	router.Get(prefix+"/:namespace/:key", m.validatePathParams, m.authorize("read"), m.handleGetOne)
	router.Put(prefix+"/:namespace/:key", m.validatePathParams, m.authorize("write"), m.handlePut)

	// Tenant-scoped routes — use WithTenantAuthorizer (default-deny when absent).
	// The tenant-list route carries no :tenantID segment; the authorizer
	// receives "" for the tenantID argument in that case.
	router.Get(prefix+"/:namespace/:key/tenants", m.validatePathParams, m.authorizeTenant("read"), m.handleListTenants)
	router.Put(prefix+"/:namespace/:key/tenants/:tenantID", m.validatePathParams, m.authorizeTenant("write"), m.handlePutTenant)
	router.Delete(prefix+"/:namespace/:key/tenants/:tenantID", m.validatePathParams, m.authorizeTenant("write"), m.handleDeleteTenant)
}

// authorize returns a per-route middleware that checks the configured authorizer.
//
// On rejection the response body carries a fixed "forbidden" message. The
// authorizer's own error string is NOT echoed on the wire — it can leak
// library-internal details (version fingerprints, policy IDs) that
// unauthorized callers should not see. The original error is logged at
// Debug level so operators can diagnose policy rejections without the
// leak.
func (m *mounter) authorize(action string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if err := m.cfg.authorizer(c, action); err != nil {
			m.logger.Log(c.UserContext(), log.LevelDebug, "admin: authorizer denied",
				log.String("action", action),
				log.Err(err),
			)

			return commonshttp.RespondError(c, http.StatusForbidden, "forbidden", "forbidden")
		}

		return c.Next()
	}
}

// authorizeTenant returns a per-route middleware that checks the tenant
// authorizer for tenant-scoped routes. The `:tenantID` URL param is passed
// through to the hook; for the tenant-list route it is empty. When
// [WithTenantAuthorizer] has not been configured, the default deny-all
// hook rejects every request with 403.
//
// As with [authorize], the authorizer's error string is redacted on the
// wire: the response body carries a fixed "forbidden" message and the
// original error is logged at Debug level.
func (m *mounter) authorizeTenant(action string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		tenantID := c.Params("tenantID")
		if err := m.cfg.tenantAuthorizer(c, action, tenantID); err != nil {
			m.logger.Log(c.UserContext(), log.LevelDebug, "admin: tenant authorizer denied",
				log.String("action", action),
				log.String("tenant_id", tenantID),
				log.Err(err),
			)

			return commonshttp.RespondError(c, http.StatusForbidden, "forbidden", "forbidden")
		}

		return c.Next()
	}
}

// validateNamespaceParam rejects namespaces longer than [maxNamespaceLen].
// Applied as middleware on routes that carry :namespace but no :key.
func (m *mounter) validateNamespaceParam(c *fiber.Ctx) error {
	if ns := c.Params("namespace"); len(ns) > maxNamespaceLen {
		return commonshttp.RespondError(c, http.StatusBadRequest, "validation_error",
			fmt.Sprintf("namespace exceeds maximum length of %d", maxNamespaceLen))
	}

	return c.Next()
}

// validatePathParams rejects namespaces longer than [maxNamespaceLen] and
// keys longer than [maxKeyLen]. Applied as middleware on routes that carry
// both :namespace and :key.
func (m *mounter) validatePathParams(c *fiber.Ctx) error {
	if ns := c.Params("namespace"); len(ns) > maxNamespaceLen {
		return commonshttp.RespondError(c, http.StatusBadRequest, "validation_error",
			fmt.Sprintf("namespace exceeds maximum length of %d", maxNamespaceLen))
	}

	if k := c.Params("key"); len(k) > maxKeyLen {
		return commonshttp.RespondError(c, http.StatusBadRequest, "validation_error",
			fmt.Sprintf("key exceeds maximum length of %d", maxKeyLen))
	}

	return c.Next()
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
		return commonshttp.RespondError(c, http.StatusNotFound, "not_found", "key not found")
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
		return commonshttp.RespondError(c, http.StatusBadRequest, "bad_request", "invalid request body")
	}

	if body.Value == nil {
		return commonshttp.RespondError(c, http.StatusBadRequest, "bad_request", "missing value field")
	}

	var value any
	if err := json.Unmarshal(body.Value, &value); err != nil {
		return commonshttp.RespondError(c, http.StatusBadRequest, "bad_request", "invalid value")
	}

	actor := m.cfg.actorExtractor(c)

	err := m.client.Set(c.UserContext(), namespace, key, value, actor)
	if err == nil {
		return c.SendStatus(fiber.StatusNoContent)
	}

	return mapSentinelErr(c, err)
}
