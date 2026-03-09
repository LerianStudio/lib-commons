package http

import (
	"net/http"

	"github.com/gofiber/fiber/v2"
)

// Respond sends a JSON response with explicit status.
func Respond(c *fiber.Ctx, status int, payload any) error {
	if c == nil {
		return ErrContextNotFound
	}

	if status < http.StatusContinue || status > 599 {
		status = http.StatusInternalServerError
	}

	return c.Status(status).JSON(payload)
}

// RespondStatus sends a status-only response with no body.
func RespondStatus(c *fiber.Ctx, status int) error {
	if c == nil {
		return ErrContextNotFound
	}

	if status < http.StatusContinue || status > 599 {
		status = http.StatusInternalServerError
	}

	return c.SendStatus(status)
}
