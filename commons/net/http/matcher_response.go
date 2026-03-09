package http

import (
	"errors"
	"net/http"

	cn "github.com/LerianStudio/lib-commons/v4/commons/constants"
	"github.com/gofiber/fiber/v2"
)

// ErrorResponse provides a consistent error structure for API responses.
// @Description Standard error response returned by all API endpoints
type ErrorResponse struct {
	// HTTP status code
	Code int `json:"code"            example:"400"`
	// Error type identifier
	Title string `json:"title"           example:"invalid_request"`
	// Human-readable error message
	Message string `json:"message"         example:"context name is required"`
}

// Error allows ErrorResponse to satisfy the error interface.
func (e ErrorResponse) Error() string {
	return e.Message
}

// RenderError writes all transport errors through a single, stable contract.
func RenderError(ctx *fiber.Ctx, err error) error {
	if ctx == nil {
		return ErrContextNotFound
	}

	if err == nil {
		return nil
	}

	// errors.As with a value target matches both ErrorResponse and *ErrorResponse,
	// since ErrorResponse implements error via a value receiver.
	var responseErr ErrorResponse
	if errors.As(err, &responseErr) {
		return renderErrorResponse(ctx, responseErr)
	}

	var fiberErr *fiber.Error
	if errors.As(err, &fiberErr) {
		return RespondError(ctx, fiberErr.Code, cn.DefaultErrorTitle, fiberErr.Message)
	}

	return RespondError(ctx, fiber.StatusInternalServerError, cn.DefaultErrorTitle, cn.DefaultInternalErrorMessage)
}

// renderErrorResponse normalizes and sends an ErrorResponse with safe defaults.
func renderErrorResponse(ctx *fiber.Ctx, resp ErrorResponse) error {
	status := fiber.StatusInternalServerError

	if resp.Code >= http.StatusContinue && resp.Code <= 599 {
		status = resp.Code
	}

	title := resp.Title
	if title == "" {
		title = cn.DefaultErrorTitle
	}

	message := resp.Message
	if message == "" {
		message = http.StatusText(status)
	}

	return RespondError(ctx, status, title, message)
}
