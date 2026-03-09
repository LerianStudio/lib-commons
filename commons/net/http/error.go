package http

import (
	"github.com/gofiber/fiber/v2"
)

// RespondError writes a structured error response using the ErrorResponse schema.
func RespondError(c *fiber.Ctx, status int, title, message string) error {
	return Respond(c, status, ErrorResponse{
		Code:    status,
		Title:   title,
		Message: message,
	})
}
