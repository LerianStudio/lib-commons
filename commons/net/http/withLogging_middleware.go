package http

import (
	"strings"

	commons "github.com/LerianStudio/lib-commons/v5/commons"
	cn "github.com/LerianStudio/lib-commons/v5/commons/constants"
	observability "github.com/LerianStudio/lib-observability"
	obsLog "github.com/LerianStudio/lib-observability/log"
	obsMiddleware "github.com/LerianStudio/lib-observability/middleware"
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
)

// LogMiddlewareOption, WithCustomLogger, and WithObfuscationDisabled are re-exported
// from lib-observability/middleware so callers using this package don't need to change imports.
type LogMiddlewareOption = obsMiddleware.LogMiddlewareOption

var (
	WithCustomLogger    = obsMiddleware.WithCustomLogger
	WithObfuscationDisabled = obsMiddleware.WithObfuscationDisabled
)

// probeRoutes are logged at DEBUG level to reduce Grafana/Loki noise.
// Kubernetes liveness/readiness probes and Prometheus scrapes hit these paths
// every few seconds per pod; INFO would dominate production log aggregators.
var probeRoutes = map[string]bool{
	"/health":  true,
	"/readyz":  true,
	"/metrics": true,
}

// WithHTTPLogging logs HTTP access at INFO level.
// Requests to probe paths (/health, /readyz, /metrics) are logged at DEBUG to
// avoid noise from Kubernetes probes in production; enable debug logging to
// inspect probe traffic when needed.
func WithHTTPLogging(opts ...LogMiddlewareOption) fiber.Handler {
	inner := obsMiddleware.WithHTTPLogging(opts...)

	return func(c *fiber.Ctx) error {
		if !probeRoutes[c.Path()] {
			return inner(c)
		}

		// Probe route: set correlation ID and log at DEBUG.
		hid := strings.TrimSpace(c.Get(cn.HeaderID))
		if commons.IsNilOrEmpty(&hid) {
			hid = uuid.New().String()
		}

		c.Request().Header.Set(cn.HeaderID, hid)
		c.Set(cn.HeaderID, hid)
		c.Response().Header.Set(cn.HeaderID, hid)
		c.SetUserContext(observability.ContextWithHeaderID(c.UserContext(), hid))

		info := obsMiddleware.NewRequestInfo(c, false)

		err := c.Next()

		rw := obsMiddleware.ResponseMetricsWrapper{
			Context:    c,
			StatusCode: c.Response().StatusCode(),
			Size:       len(c.Response().Body()),
		}

		info.FinishRequestInfo(&rw)

		observability.NewLoggerFromContext(c.UserContext()).
			Log(c.UserContext(), obsLog.LevelDebug, info.CLFString())

		return err
	}
}
