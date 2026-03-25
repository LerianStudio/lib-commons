package http

import (
	"context"
	"strconv"

	"github.com/LerianStudio/lib-commons/v4/commons"
	"github.com/LerianStudio/lib-commons/v4/commons/internal/nilcheck"
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

type corsRuntimeConfig struct {
	logger           libLog.Logger
	origins          string
	allowMethods     string
	allowHeaders     string
	exposeHeaders    string
	allowCredentials bool
	denyAllOrigins   bool
}

// WithCORSLogger provides a structured logger for CORS security warnings.
// When not provided, warnings are logged via stdlib log.
func WithCORSLogger(logger libLog.Logger) CORSOption {
	return func(c *corsConfig) {
		if !nilcheck.Interface(logger) {
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
	runtimeCfg := buildCORSRuntimeConfig(opts...)
	warnCORSConfiguration(runtimeCfg)
	enforceCORSRuntimeConfig(runtimeCfg)
	guardCORSCredentials(runtimeCfg)

	return cors.New(buildFiberCORSConfig(runtimeCfg))
}

func buildCORSRuntimeConfig(opts ...CORSOption) *corsRuntimeConfig {
	config := &corsConfig{}
	for _, opt := range opts {
		opt(config)
	}

	return &corsRuntimeConfig{
		logger:           resolveCORSLogger(config),
		origins:          commons.GetenvOrDefault("ACCESS_CONTROL_ALLOW_ORIGIN", defaultAccessControlAllowOrigin),
		allowMethods:     commons.GetenvOrDefault("ACCESS_CONTROL_ALLOW_METHODS", defaultAccessControlAllowMethods),
		allowHeaders:     commons.GetenvOrDefault("ACCESS_CONTROL_ALLOW_HEADERS", defaultAccessControlAllowHeaders),
		exposeHeaders:    commons.GetenvOrDefault("ACCESS_CONTROL_EXPOSE_HEADERS", defaultAccessControlExposeHeaders),
		allowCredentials: readCORSAllowCredentials(),
	}
}

func resolveCORSLogger(cfg *corsConfig) libLog.Logger {
	if cfg != nil && !nilcheck.Interface(cfg.logger) {
		return cfg.logger
	}

	return &libLog.GoLogger{Level: libLog.LevelWarn}
}

func readCORSAllowCredentials() bool {
	allowCredentials := defaultAllowCredentials
	if parsed, err := strconv.ParseBool(commons.GetenvOrDefault("ACCESS_CONTROL_ALLOW_CREDENTIALS", "false")); err == nil {
		allowCredentials = parsed
	}

	return allowCredentials
}

func isWildcardCORSOrigin(origins string) bool {
	return origins == "*" || origins == ""
}

func warnCORSConfiguration(cfg *corsRuntimeConfig) {
	if cfg == nil {
		return
	}

	if isWildcardCORSOrigin(cfg.origins) {
		cfg.logger.Log(context.Background(), libLog.LevelWarn,
			"CORS: AllowOrigins is set to wildcard (*); "+
				"this allows ANY website to make cross-origin requests to your API; "+
				"for financial services, set ACCESS_CONTROL_ALLOW_ORIGIN to specific trusted origins",
		)
	}

	if cfg.origins == "*" && cfg.allowCredentials {
		cfg.logger.Log(context.Background(), libLog.LevelWarn,
			"CORS: AllowOrigins=* with AllowCredentials=true is REJECTED by browsers per the CORS spec; "+
				"credentials will NOT work; configure specific origins via ACCESS_CONTROL_ALLOW_ORIGIN",
		)
	}
}

func enforceCORSRuntimeConfig(cfg *corsRuntimeConfig) {
	if cfg == nil || commons.CurrentTier() < commons.TierModerate {
		return
	}

	result := commons.CheckSecurityRule(commons.RuleCORSWildcardOrigin, isWildcardCORSOrigin(cfg.origins))
	if err := commons.EnforceSecurityRule(context.Background(), cfg.logger, "cors", result); err != nil {
		cfg.logger.Log(context.Background(), libLog.LevelError,
			"CORS security rule enforcement failed, applying deny-all fallback",
			libLog.Err(err),
		)

		cfg.denyAllOrigins = true
		cfg.origins = ""
		cfg.allowCredentials = false

		cfg.logger.Log(context.Background(), libLog.LevelWarn,
			"CORS: enforcement active — origins restricted to none; "+
				"set ACCESS_CONTROL_ALLOW_ORIGIN to specific trusted origins")
	}
}

func guardCORSCredentials(cfg *corsRuntimeConfig) {
	if cfg == nil {
		return
	}

	if cfg.origins == "*" && cfg.allowCredentials {
		cfg.logger.Log(context.Background(), libLog.LevelWarn,
			"CORS: AllowOrigins=* with AllowCredentials=true is forbidden by CORS spec "+
				"and causes Fiber panic; forcing AllowCredentials=false")

		cfg.allowCredentials = false
	}
}

func buildFiberCORSConfig(cfg *corsRuntimeConfig) cors.Config {
	config := cors.Config{
		AllowOrigins:     cfg.origins,
		AllowMethods:     cfg.allowMethods,
		AllowHeaders:     cfg.allowHeaders,
		ExposeHeaders:    cfg.exposeHeaders,
		AllowCredentials: cfg.allowCredentials,
	}
	if cfg.denyAllOrigins {
		config.AllowOriginsFunc = func(string) bool { return false }
	}

	return config
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
