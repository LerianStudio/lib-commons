package http

import (
	"context"
	"strconv"

	"github.com/LerianStudio/lib-commons/v4/commons"
	libLog "github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
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
	logger libLog.Logger
}

// WithCORSLogger provides a structured logger for CORS security warnings.
// When not provided, warnings are logged via stdlib log.
func WithCORSLogger(logger libLog.Logger) CORSOption {
	return func(c *corsConfig) {
		if logger != nil {
			c.logger = logger
		}
	}
}

// WithCORS is a middleware that enables CORS.
// Reads configuration from environment variables with sensible defaults.
//
// WARNING: The default AllowOrigins is "*" (wildcard). For financial services,
// configure ACCESS_CONTROL_ALLOW_ORIGIN to specific trusted origins.
func WithCORS(opts ...CORSOption) fiber.Handler {
	cfg := &corsConfig{}

	for _, opt := range opts {
		opt(cfg)
	}

	// Default to GoLogger so CORS warnings are always emitted, even without explicit logger.
	if cfg.logger == nil {
		cfg.logger = &libLog.GoLogger{Level: libLog.LevelWarn}
	}

	allowCredentials := defaultAllowCredentials

	if parsed, err := strconv.ParseBool(commons.GetenvOrDefault("ACCESS_CONTROL_ALLOW_CREDENTIALS", "false")); err == nil {
		allowCredentials = parsed
	}

	origins := commons.GetenvOrDefault("ACCESS_CONTROL_ALLOW_ORIGIN", defaultAccessControlAllowOrigin)

	if origins == "*" || origins == "" {
		cfg.logger.Log(context.Background(), libLog.LevelWarn,
			"CORS: AllowOrigins is set to wildcard (*); "+
				"this allows ANY website to make cross-origin requests to your API; "+
				"for financial services, set ACCESS_CONTROL_ALLOW_ORIGIN to specific trusted origins",
		)
	}

	if origins == "*" && allowCredentials {
		cfg.logger.Log(context.Background(), libLog.LevelWarn,
			"CORS: AllowOrigins=* with AllowCredentials=true is REJECTED by browsers per the CORS spec; "+
				"credentials will NOT work; configure specific origins via ACCESS_CONTROL_ALLOW_ORIGIN",
		)
	}

	return cors.New(cors.Config{
		AllowOrigins:     origins,
		AllowMethods:     commons.GetenvOrDefault("ACCESS_CONTROL_ALLOW_METHODS", defaultAccessControlAllowMethods),
		AllowHeaders:     commons.GetenvOrDefault("ACCESS_CONTROL_ALLOW_HEADERS", defaultAccessControlAllowHeaders),
		ExposeHeaders:    commons.GetenvOrDefault("ACCESS_CONTROL_EXPOSE_HEADERS", defaultAccessControlExposeHeaders),
		AllowCredentials: allowCredentials,
	})
}

// AllowFullOptionsWithCORS set r.Use(WithCORS) and allow every request to use OPTION method.
func AllowFullOptionsWithCORS(app *fiber.App, opts ...CORSOption) {
	if app == nil {
		return
	}

	app.Use(WithCORS(opts...))

	app.Options("/*", func(c *fiber.Ctx) error {
		return c.SendStatus(fiber.StatusNoContent)
	})
}
