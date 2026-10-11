package http

import (
	"errors"
	"net/http"

	cn "github.com/LerianStudio/lib-commons/v7/commons/constants"
	observability "github.com/LerianStudio/lib-observability/v4"
	"github.com/gofiber/fiber/v3"
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
// A 5xx body hides err, so err is recorded as the cause the access line logs.
func RenderError(ctx fiber.Ctx, err error) error {
	if ctx == nil {
		return ErrContextNotFound
	}

	if err == nil {
		return nil
	}

	status, title, message := fiber.StatusInternalServerError, cn.DefaultErrorTitle, cn.DefaultInternalErrorMessage

	// errors.As with a value target matches both ErrorResponse and *ErrorResponse,
	// since ErrorResponse implements error via a value receiver.
	var responseErr ErrorResponse

	var fiberErr *fiber.Error

	switch {
	case errors.As(err, &responseErr):
		status, title, message = normalizeErrorResponse(responseErr)
	case errors.As(err, &fiberErr):
		status, message = fiberErr.Code, fiberErr.Message
	}

	if status >= fiber.StatusInternalServerError {
		observability.RecordErrorCause(ctx.Context(), err)
	}

	return RespondError(ctx, status, title, message)
}

// normalizeErrorResponse applies safe defaults to an ErrorResponse's status, title and message.
func normalizeErrorResponse(resp ErrorResponse) (int, string, string) {
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

	return status, title, message
}
