package http

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"
	obsbridge "github.com/LerianStudio/lib-commons/v7/commons/obs/obsbridge"

	"github.com/LerianStudio/lib-commons/v7/commons"
	cn "github.com/LerianStudio/lib-commons/v7/commons/constants"
	libOpentelemetry "github.com/LerianStudio/lib-observability/v4/tracing"
	"github.com/gofiber/fiber/v3"
	"go.opentelemetry.io/otel/trace"
)

// Ping returns HTTP Status 200 with response "healthy".
func Ping(c fiber.Ctx) error {
	if c == nil {
		return ErrContextNotFound
	}

	return c.SendString("healthy")
}

// Version returns HTTP Status 200 with the service version from the VERSION
// environment variable (defaults to "0.0.0").
//
// NOTE: This endpoint intentionally exposes the build version. Callers that
// need to restrict visibility should gate this route behind authentication
// or omit it from public-facing routers.
//
// Deprecated: the runtime version is compiled in, not read from the
// environment. Use buildinfo.Handler, mounted on the admin port by
// server.NewAdminApp. Removed in v8.
func Version(c fiber.Ctx) error {
	return Respond(c, fiber.StatusOK, fiber.Map{
		"version":     commons.GetenvOrDefault("VERSION", "0.0.0"),
		"requestDate": time.Now().UTC(),
	})
}

// Welcome returns HTTP Status 200 with service info.
func Welcome(service string, description string) fiber.Handler {
	return func(c fiber.Ctx) error {
		if c == nil {
			return ErrContextNotFound
		}

		return c.JSON(fiber.Map{
			"service":     service,
			"description": description,
		})
	}
}

// NotImplementedEndpoint returns HTTP 501 with not implemented message.
func NotImplementedEndpoint(c fiber.Ctx) error {
	return RespondError(c, fiber.StatusNotImplemented, "not_implemented", "Not implemented yet")
}

// File serves a specific file.
func File(filePath string) fiber.Handler {
	return func(c fiber.Ctx) error {
		if c == nil {
			return ErrContextNotFound
		}

		return c.SendFile(filePath)
	}
}

// ExtractTokenFromHeader extracts a token from the Authorization header of a Fiber request,
// with the rules of ExtractTokenFromAuthorization.
func ExtractTokenFromHeader(c fiber.Ctx) string {
	if c == nil {
		return ""
	}

	return ExtractTokenFromAuthorization(c.Get(fiber.HeaderAuthorization))
}

// ExtractTokenFromAuthorization extracts a token from an Authorization header value, for servers
// that do not run on Fiber. It accepts `Bearer <token>` case-insensitively and the legacy raw-token
// form (a single field with no scheme); a malformed Bearer value or a non-Bearer multi-part value
// returns an empty string.
func ExtractTokenFromAuthorization(header string) string {
	fields := strings.Fields(header)

	switch {
	case len(fields) == 2 && strings.EqualFold(fields[0], cn.Bearer):
		return fields[1]
	case len(fields) == 1 && !strings.EqualFold(fields[0], cn.Bearer):
		return fields[0]
	default:
		return ""
	}
}

// FiberErrorHandler is the canonical Fiber error handler.
// It uses the structured logger from the request context so that error
// details pass through the sanitization pipeline instead of going to
// plain stdlib log.Printf.
func FiberErrorHandler(c fiber.Ctx, err error) error {
	if c == nil {
		if err != nil {
			return err
		}

		return ErrContextNotFound
	}

	// Safely end spans if user context exists
	ctx := c.Context()
	if ctx != nil {
		span := trace.SpanFromContext(ctx)
		libOpentelemetry.HandleSpanError(span, "handler error", err)
		span.End()
	}

	var fe *fiber.Error
	if errors.As(err, &fe) {
		return RenderError(c, ErrorResponse{
			Code:    fe.Code,
			Title:   cn.DefaultErrorTitle,
			Message: fe.Message,
		})
	}

	if ctx == nil {
		ctx = context.Background()
	}

	logger := obsbridge.LoggerFromContext(ctx)
	logger.Log(ctx, obs.LevelError,
		"handler error",
		"method", c.Method(),
		"path", c.Path(),
		"error", err,
	)

	return RenderError(c, err)
}
