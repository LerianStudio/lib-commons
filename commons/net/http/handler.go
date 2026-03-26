package http

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons"
	cn "github.com/LerianStudio/lib-commons/v4/commons/constants"
	libLog "github.com/LerianStudio/lib-commons/v4/commons/log"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v4/commons/opentelemetry"
	"github.com/gofiber/fiber/v2"
	"go.opentelemetry.io/otel/trace"
)

// Ping returns HTTP Status 200 with response "healthy".
func Ping(c *fiber.Ctx) error {
	if err := requireFiberContext(c); err != nil {
		return err
	}

	return c.SendString("healthy")
}

// Version returns HTTP Status 200 with the service version from the VERSION
// environment variable (defaults to "0.0.0").
//
// NOTE: This endpoint intentionally exposes the build version. Callers that
// need to restrict visibility should gate this route behind authentication
// or omit it from public-facing routers.
func Version(c *fiber.Ctx) error {
	return respondJSONMap(c, fiber.StatusOK, fiber.Map{
		"version":     commons.GetenvOrDefault("VERSION", "0.0.0"),
		"requestDate": time.Now().UTC(),
	})
}

// Welcome returns HTTP Status 200 with service info.
func Welcome(service string, description string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		return respondJSONMap(c, fiber.StatusOK, fiber.Map{
			"service":     service,
			"description": description,
		})
	}
}

// NotImplementedEndpoint returns HTTP 501 with not implemented message.
func NotImplementedEndpoint(c *fiber.Ctx) error {
	return RespondError(c, fiber.StatusNotImplemented, "not_implemented", "Not implemented yet")
}

// File serves a specific file.
func File(filePath string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if err := requireFiberContext(c); err != nil {
			return err
		}

		return c.SendFile(filePath)
	}
}

// ExtractTokenFromHeader extracts a token from the Authorization header.
// It accepts `Bearer <token>` case-insensitively and also preserves the
// legacy raw-token form when the header contains a single token with no scheme.
// Malformed Bearer values and non-Bearer multi-part values return an empty string.
func ExtractTokenFromHeader(c *fiber.Ctx) string {
	if c == nil {
		return ""
	}

	authHeader := strings.TrimSpace(c.Get(fiber.HeaderAuthorization))
	if authHeader == "" {
		return ""
	}

	fields := strings.Fields(authHeader)

	if len(fields) == 2 && strings.EqualFold(fields[0], cn.Bearer) {
		return fields[1]
	}

	if len(fields) > 2 && strings.EqualFold(fields[0], cn.Bearer) {
		return ""
	}

	if len(fields) == 1 {
		if strings.EqualFold(fields[0], cn.Bearer) {
			return ""
		}

		return fields[0]
	}

	return ""
}

// FiberErrorHandler is the canonical Fiber error handler.
// It uses the structured logger from the request context so that error
// details pass through the sanitization pipeline instead of going to
// plain stdlib log.Printf.
func FiberErrorHandler(c *fiber.Ctx, err error) error {
	if c == nil {
		if err != nil {
			return err
		}

		return ErrContextNotFound
	}

	// Safely end spans if user context exists
	ctx := c.UserContext()
	if ctx != nil {
		span := trace.SpanFromContext(ctx)
		libOpentelemetry.HandleSpanError(span, "handler error", err)
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

	logger := commons.NewLoggerFromContext(ctx)
	logger.Log(ctx, libLog.LevelError,
		"handler error",
		libLog.String("method", c.Method()),
		libLog.String("path", c.Path()),
		libLog.Err(err),
	)

	return RenderError(c, err)
}
