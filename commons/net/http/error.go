package http

import (
	"github.com/gofiber/fiber/v2"
)

func buildErrorResponse(status int, title, message string) ErrorResponse {
	status = normalizeHTTPStatus(status)

	return ErrorResponse{
		Code:    status,
		Title:   title,
		Message: message,
	}
}

// RespondError writes a structured error response using the ErrorResponse schema.
func RespondError(c *fiber.Ctx, status int, title, message string) error {
	return Respond(c, status, buildErrorResponse(status, title, message))
}
