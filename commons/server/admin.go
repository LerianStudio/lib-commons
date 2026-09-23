package server

import (
	"github.com/LerianStudio/lib-commons/v7/commons/buildinfo"
	"github.com/gofiber/fiber/v3"
)

// NewAdminApp returns the Fiber app for the admin port, with GET /version
// already mounted and answering with the compiled identity of service.
//
// Nothing else is mounted. /health, /readyz (with its sub-routes) and /metrics
// stay with the service, which registers them on the returned app before
// passing it to WithAdminHTTPServer. No telemetry middleware either:
// operational endpoints do not produce spans.
//
// The startup banner is suppressed by the manager at Listen time, so the app
// needs no configuration of its own.
func NewAdminApp(service string) *fiber.App {
	app := fiber.New()

	app.Get("/version", buildinfo.Handler(service))

	return app
}
