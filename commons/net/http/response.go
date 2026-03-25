package http

import (
	"net/http"

	"github.com/gofiber/fiber/v2"
)

func normalizeHTTPStatus(status int) int {
	if status < http.StatusContinue || status > 599 {
		return http.StatusInternalServerError
	}

	return status
}

// Respond sends a JSON response with explicit status.
func Respond(c *fiber.Ctx, status int, payload any) error {
	if c == nil {
		return ErrContextNotFound
	}

	return c.Status(normalizeHTTPStatus(status)).JSON(payload)
}

// RespondStatus sends a status-only response with no body.
func RespondStatus(c *fiber.Ctx, status int) error {
	if c == nil {
		return ErrContextNotFound
	}

	return c.SendStatus(normalizeHTTPStatus(status))
}
