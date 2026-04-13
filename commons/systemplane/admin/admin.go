// Package admin provides Fiber HTTP handlers for inspecting and modifying
// systemplane configuration entries at runtime.
//
// Mount registers three routes on a Fiber router:
//
//	GET    /<prefix>/:namespace          — list entries in a namespace
//	GET    /<prefix>/:namespace/:key     — read a single entry
//	PUT    /<prefix>/:namespace/:key     — write a single entry
//
// The default path prefix is "/system".
package admin

import (
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
		pathPrefix: "/system",
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
// Return a non-nil error to reject the request.
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

// Mount registers the admin HTTP routes on router using the given Client.
//
// TODO(phase-6): implement handlers
func Mount(router fiber.Router, c *systemplane.Client, opts ...MountOption) {
	cfg := defaultMountConfig()
	for _, o := range opts {
		o(&cfg)
	}

	// TODO(phase-6): register GET /:namespace, GET /:namespace/:key, PUT /:namespace/:key
	_ = router
	_ = c
	_ = cfg
}
