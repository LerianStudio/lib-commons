package http

import (
	"context"
	"slices"
	"strconv"
	"strings"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"

	"github.com/LerianStudio/lib-commons/v7/commons"
	"github.com/LerianStudio/lib-commons/v7/commons/internal/nilcheck"
	libLog "github.com/LerianStudio/lib-observability/v4/log"
	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/cors"
)

const (
	// defaultAccessControlAllowOrigin is the default value for the Access-Control-Allow-Origin header.
	defaultAccessControlAllowOrigin = "*"
	// defaultAccessControlAllowMethods is the default value for the Access-Control-Allow-Methods header.
	defaultAccessControlAllowMethods = "POST, GET, OPTIONS, PUT, DELETE, PATCH"
	// defaultAccessControlAllowHeaders is the default value for the Access-Control-Allow-Headers header.
	defaultAccessControlAllowHeaders = "Accept, Content-Type, Content-Length, Accept-Encoding, X-CSRF-Token, Authorization"
	// defaultAccessControlExposeHeaders is the default value for the Access-Control-Expose-Headers header.
	defaultAccessControlExposeHeaders = ""
	// defaultAllowCredentials is the default value for the Access-Control-Allow-Credentials header.
	defaultAllowCredentials = false
)

// CORSOption is a functional option for CORS middleware configuration.
type CORSOption func(*corsConfig)

type corsConfig struct {
	logger obs.Logger
	policy *CORSPolicy
}

// CORSPolicy is the CORS policy WithCORSPolicy hands to WithCORS. Fields reach
// Fiber's cors.Config as given (empty methods/headers take Fiber's defaults); an
// empty or "*" origin list takes the wildcard guard.
type CORSPolicy struct {
	AllowOrigins     []string
	AllowMethods     []string
	AllowHeaders     []string
	ExposeHeaders    []string
	AllowCredentials bool
}

// WithCORSLogger provides a structured logger for CORS security warnings.
// When not provided, warnings are logged via stdlib log.
func WithCORSLogger(logger obs.Logger) CORSOption {
	return func(c *corsConfig) {
		if !nilcheck.Interface(logger) {
			c.logger = logger
		}
	}
}

// WithCORSPolicy makes WithCORS take its policy from code and read none of the
// ACCESS_CONTROL_* env vars. The wildcard-origin guards apply to it unchanged.
func WithCORSPolicy(policy CORSPolicy) CORSOption {
	return func(c *corsConfig) {
		c.policy = &policy
	}
}

// WithCORS is a middleware that enables CORS.
// Reads configuration from environment variables with sensible defaults,
// or from WithCORSPolicy when given.
//
// WARNING: The default AllowOrigins is "*" (wildcard). For financial services,
// configure ACCESS_CONTROL_ALLOW_ORIGIN to specific trusted origins.
func WithCORS(opts ...CORSOption) fiber.Handler {
	cfg := &corsConfig{}

	for _, opt := range opts {
		opt(cfg)
	}

	// Default to GoLogger so CORS warnings are always emitted, even without explicit logger.
	if nilcheck.Interface(cfg.logger) {
		cfg.logger = &libLog.GoLogger{Level: libLog.LevelWarn}
	}

	policy, originSource := corsPolicyFromEnv(), "ACCESS_CONTROL_ALLOW_ORIGIN"
	if cfg.policy != nil {
		policy, originSource = *cfg.policy, "WithCORSPolicy AllowOrigins"
	}

	if allowsAnyOrigin(policy.AllowOrigins) {
		cfg.logger.Log(context.Background(), obs.LevelWarn,
			"CORS: AllowOrigins is set to wildcard (*); "+
				"this allows ANY website to make cross-origin requests to your API; "+
				"for financial services, set "+originSource+" to specific trusted origins",
		)
	}

	if allowsAnyOrigin(policy.AllowOrigins) && policy.AllowCredentials {
		cfg.logger.Log(context.Background(), obs.LevelWarn,
			"CORS: AllowOrigins=* with AllowCredentials=true is REJECTED by browsers per the CORS spec; "+
				"credentials will NOT work; configure specific origins via "+originSource,
		)
	}

	// Security policy: CORS wildcard origin is forbidden by default. Set
	// ALLOW_CORS_WILDCARD=true to permit wildcard (emits a WARN log line).
	denyAllOrigins := false

	if allowsAnyOrigin(policy.AllowOrigins) {
		if !commons.AllowCORSWildcard() {
			cfg.logger.Log(context.Background(), obs.LevelError,
				"CORS wildcard origin rejected; applying deny-all fallback (set "+
					commons.EnvAllowCORSWildcard+"=true to permit, or set "+originSource+" to specific trusted origins)",
			)

			denyAllOrigins = true
			policy.AllowOrigins = nil
			policy.AllowCredentials = false
		} else {
			cfg.logger.Log(context.Background(), obs.LevelWarn, "security bypass active",
				"feature", "cors_wildcard",
				"env_var", commons.EnvAllowCORSWildcard,
			)
		}
	}

	// Guard: prevent Fiber panic on wildcard + credentials (forbidden by CORS spec).
	if allowsAnyOrigin(policy.AllowOrigins) && policy.AllowCredentials {
		cfg.logger.Log(context.Background(), obs.LevelWarn,
			"CORS: AllowOrigins=* with AllowCredentials=true is forbidden by CORS spec "+
				"and causes Fiber panic; forcing AllowCredentials=false")

		policy.AllowCredentials = false
	}

	config := cors.Config{
		AllowOrigins:     policy.AllowOrigins,
		AllowMethods:     policy.AllowMethods,
		AllowHeaders:     policy.AllowHeaders,
		ExposeHeaders:    policy.ExposeHeaders,
		AllowCredentials: policy.AllowCredentials,
	}
	if denyAllOrigins {
		config.AllowOriginsFunc = func(string) bool { return false }
	}

	return cors.New(config)
}

// AllowFullOptionsWithCORS set r.Use(WithCORS) and answers every OPTIONS request with 204.
// OPTIONS is answered in middleware, not by an OPTIONS /* route: a catch-all route makes
// Fiber answer 405 instead of 404 to any other method on an unknown path.
func AllowFullOptionsWithCORS(app *fiber.App, opts ...CORSOption) {
	if app == nil {
		return
	}

	app.Use(WithCORS(opts...))

	app.Use(func(c fiber.Ctx) error {
		if c.Method() == fiber.MethodOptions {
			return c.SendStatus(fiber.StatusNoContent)
		}

		return c.Next()
	})
}

// corsPolicyFromEnv reads the ACCESS_CONTROL_* env vars, comma-separated, into
// the []string form Fiber v3's CORS config takes.
func corsPolicyFromEnv() CORSPolicy {
	allowCredentials := defaultAllowCredentials

	if parsed, err := strconv.ParseBool(commons.GetenvOrDefault("ACCESS_CONTROL_ALLOW_CREDENTIALS", "false")); err == nil {
		allowCredentials = parsed
	}

	return CORSPolicy{
		AllowOrigins:     splitCSV(commons.GetenvOrDefault("ACCESS_CONTROL_ALLOW_ORIGIN", defaultAccessControlAllowOrigin)),
		AllowMethods:     splitCSV(commons.GetenvOrDefault("ACCESS_CONTROL_ALLOW_METHODS", defaultAccessControlAllowMethods)),
		AllowHeaders:     splitCSV(commons.GetenvOrDefault("ACCESS_CONTROL_ALLOW_HEADERS", defaultAccessControlAllowHeaders)),
		ExposeHeaders:    splitCSV(commons.GetenvOrDefault("ACCESS_CONTROL_EXPOSE_HEADERS", defaultAccessControlExposeHeaders)),
		AllowCredentials: allowCredentials,
	}
}

// allowsAnyOrigin reports whether Fiber v3 would allow every origin: a "*" entry
// anywhere in the list, or no entry at all. Fiber panics on either one when
// AllowCredentials is true, so every wildcard guard inspects this, not a raw "*".
func allowsAnyOrigin(origins []string) bool {
	return len(origins) == 0 || slices.Contains(origins, "*")
}

// splitCSV converts a comma-separated header value into the []string form that
// Fiber v3's CORS config expects. Surrounding whitespace on each element is
// trimmed and empty elements are dropped, so "" yields a nil slice (no origins/
// headers configured) rather than a single empty entry.
func splitCSV(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}

	parts := strings.Split(value, ",")
	result := make([]string, 0, len(parts))

	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}

	return result
}
