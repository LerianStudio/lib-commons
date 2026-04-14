// Package admin provides Fiber HTTP handlers for inspecting and modifying
// systemplane configuration entries at runtime.
//
// Mount registers three routes on a Fiber router:
//
//	GET    /<prefix>/:namespace          - list entries in a namespace
//	GET    /<prefix>/:namespace/:key     - read a single entry
//	PUT    /<prefix>/:namespace/:key     - write a single entry
//
// The default path prefix is "/system".
package admin

import (
	"errors"
	"strings"

	commonshttp "github.com/LerianStudio/lib-commons/v4/commons/net/http"
	"github.com/LerianStudio/lib-commons/v4/commons/systemplane"
	"github.com/gofiber/fiber/v2"
)

// mountConfig holds options applied by MountOption functions.
type mountConfig struct {
	pathPrefix     string
	authorizer     func(*fiber.Ctx, string) error
	actorExtractor func(*fiber.Ctx) string
}

// defaultMountConfig returns sensible defaults.
func defaultMountConfig() mountConfig {
	return mountConfig{
		pathPrefix:     "/system",
		authorizer:     func(_ *fiber.Ctx, _ string) error { return nil },
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

// WithAuthorizer sets an authorization check called before each handler.
// The action argument is "read" for GET requests and "write" for PUT requests.
// Return a non-nil error to reject the request with 403 Forbidden.
func WithAuthorizer(fn func(*fiber.Ctx, string) error) MountOption {
	return func(cfg *mountConfig) {
		if fn != nil {
			cfg.authorizer = fn
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
}

// Mount registers the admin HTTP routes on router using the given Client.
// Nil client causes Mount to be a no-op (does not panic).
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

	router.Get(prefix+"/:namespace", m.authorize("read"), m.handleList)
	router.Get(prefix+"/:namespace/:key", m.authorize("read"), m.handleGetOne)
	router.Put(prefix+"/:namespace/:key", m.authorize("write"), m.handlePut)
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

// ---------------------------------------------------------------------------
// DTOs
// ---------------------------------------------------------------------------

type listResponse struct {
	Namespace string          `json:"namespace"`
	Entries   []entryResponse `json:"entries"`
}

type entryResponse struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
}

type getResponse struct {
	Namespace string `json:"namespace"`
	Key       string `json:"key"`
	Value     any    `json:"value"`
}

type putRequest struct {
	Value any `json:"value"`
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
			Key:   e.Key,
			Value: redacted,
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
		Namespace: namespace,
		Key:       key,
		Value:     redacted,
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

	actor := m.cfg.actorExtractor(c)

	err := m.client.Set(c.UserContext(), namespace, key, body.Value, actor)
	if err == nil {
		return c.SendStatus(fiber.StatusNoContent)
	}

	// Map known sentinel errors to appropriate HTTP status codes.
	switch {
	case errors.Is(err, systemplane.ErrUnknownKey):
		return c.Status(fiber.StatusBadRequest).JSON(commonshttp.ErrorResponse{
			Code:    fiber.StatusBadRequest,
			Title:   "unknown_key",
			Message: "key is not registered",
		})
	case errors.Is(err, systemplane.ErrValidation):
		return c.Status(fiber.StatusBadRequest).JSON(commonshttp.ErrorResponse{
			Code:    fiber.StatusBadRequest,
			Title:   "validation_error",
			Message: err.Error(),
		})
	case errors.Is(err, systemplane.ErrNotStarted),
		errors.Is(err, systemplane.ErrClosed):
		return c.Status(fiber.StatusServiceUnavailable).JSON(commonshttp.ErrorResponse{
			Code:    fiber.StatusServiceUnavailable,
			Title:   "service_unavailable",
			Message: "configuration service is not available",
		})
	default:
		// Do not leak internal error details. Log if the client has a logger.
		return c.Status(fiber.StatusInternalServerError).JSON(commonshttp.ErrorResponse{
			Code:    fiber.StatusInternalServerError,
			Title:   "internal_error",
			Message: "write failed",
		})
	}
}
