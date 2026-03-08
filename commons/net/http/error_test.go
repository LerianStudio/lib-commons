//go:build unit

package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// RespondError -- comprehensive status code and structure coverage
// ---------------------------------------------------------------------------

func TestRespondError_HappyPath(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return RespondError(c, fiber.StatusBadRequest, "test_error", "test message")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, fiber.StatusBadRequest, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var errResp ErrorResponse
	require.NoError(t, json.Unmarshal(body, &errResp))

	assert.Equal(t, 400, errResp.Code)
	assert.Equal(t, "test_error", errResp.Title)
	assert.Equal(t, "test message", errResp.Message)
}

func TestRespondError_AllStatusCodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status  int
		title   string
		message string
	}{
		{"400 Bad Request", 400, "bad_request", "Invalid input"},
		{"401 Unauthorized", 401, "unauthorized", "Missing token"},
		{"403 Forbidden", 403, "forbidden", "Access denied"},
		{"404 Not Found", 404, "not_found", "Resource not found"},
		{"405 Method Not Allowed", 405, "method_not_allowed", "POST not supported"},
		{"409 Conflict", 409, "conflict", "Resource already exists"},
		{"412 Precondition Failed", 412, "precondition_failed", "ETag mismatch"},
		{"422 Unprocessable Entity", 422, "unprocessable_entity", "Validation failed"},
		{"429 Too Many Requests", 429, "rate_limited", "Rate limit exceeded"},
		{"500 Internal Server Error", 500, "internal_error", "Something went wrong"},
		{"502 Bad Gateway", 502, "bad_gateway", "Upstream unavailable"},
		{"503 Service Unavailable", 503, "service_unavailable", "Service temporarily down"},
		{"504 Gateway Timeout", 504, "gateway_timeout", "Upstream timeout"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app := fiber.New()
			app.Get("/test", func(c *fiber.Ctx) error {
				return RespondError(c, tc.status, tc.title, tc.message)
			})

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			assert.Equal(t, tc.status, resp.StatusCode)

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			var errResp ErrorResponse
			require.NoError(t, json.Unmarshal(body, &errResp))

			assert.Equal(t, tc.status, errResp.Code)
			assert.Equal(t, tc.title, errResp.Title)
			assert.Equal(t, tc.message, errResp.Message)
		})
	}
}

func TestRespondError_NoLegacyField(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return RespondError(c, fiber.StatusUnauthorized, "invalid_credentials", "invalid")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))
	_, exists := parsed["error"]
	assert.False(t, exists, "response should not contain legacy 'error' field")
}

func TestRespondError_JSONStructureExactlyThreeFields(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return RespondError(c, fiber.StatusUnprocessableEntity, "validation_error", "field 'name' required")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(body, &parsed))

	assert.Len(t, parsed, 3, "response should have exactly 3 fields: code, title, message")
	assert.Contains(t, parsed, "code")
	assert.Contains(t, parsed, "title")
	assert.Contains(t, parsed, "message")

	assert.Equal(t, float64(422), parsed["code"])
	assert.Equal(t, "validation_error", parsed["title"])
	assert.Equal(t, "field 'name' required", parsed["message"])
}

func TestRespondError_EmptyTitleAndMessage(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return RespondError(c, fiber.StatusBadRequest, "", "")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, fiber.StatusBadRequest, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var errResp ErrorResponse
	require.NoError(t, json.Unmarshal(body, &errResp))
	assert.Equal(t, 400, errResp.Code)
	assert.Empty(t, errResp.Title)
	assert.Empty(t, errResp.Message)
}

func TestRespondError_LongMessage(t *testing.T) {
	t.Parallel()

	longMsg := "The request could not be processed because the 'transaction_amount' field exceeds " +
		"the maximum allowed value of 999999999.99 for the specified currency code (USD). " +
		"Please verify the amount and retry the request."

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return RespondError(c, fiber.StatusBadRequest, "amount_exceeded", longMsg)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var errResp ErrorResponse
	require.NoError(t, json.Unmarshal(body, &errResp))
	assert.Equal(t, longMsg, errResp.Message)
}

func TestRespondError_ContentTypeIsJSON(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return RespondError(c, fiber.StatusBadRequest, "bad", "bad request")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")
}

// ---------------------------------------------------------------------------
// ErrorResponse interface and marshaling
// ---------------------------------------------------------------------------

func TestErrorResponse_ImplementsError(t *testing.T) {
	t.Parallel()

	errResp := ErrorResponse{
		Code:    400,
		Title:   "bad_request",
		Message: "invalid input",
	}

	var err error = errResp
	assert.Equal(t, "invalid input", err.Error())
}

func TestErrorResponse_MarshalUnmarshalRoundTrip(t *testing.T) {
	t.Parallel()

	errResp := ErrorResponse{
		Code:    404,
		Title:   "not_found",
		Message: "resource does not exist",
	}

	data, err := json.Marshal(errResp)
	require.NoError(t, err)

	var decoded ErrorResponse
	require.NoError(t, json.Unmarshal(data, &decoded))
	assert.Equal(t, errResp, decoded)
}

// ---------------------------------------------------------------------------
// RenderError -- extended edge cases (not covered in matcher_response_test.go)
// ---------------------------------------------------------------------------

func TestRenderError_ErrorResponseWithValidCodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		err         ErrorResponse
		wantCode    int
		wantTitle   string
		wantMessage string
	}{
		{
			name: "503 Service Unavailable",
			err: ErrorResponse{
				Code:    503,
				Title:   "service_unavailable",
				Message: "Maintenance mode",
			},
			wantCode:    503,
			wantTitle:   "service_unavailable",
			wantMessage: "Maintenance mode",
		},
		{
			name: "429 Too Many Requests",
			err: ErrorResponse{
				Code:    429,
				Title:   "rate_limited",
				Message: "Slow down",
			},
			wantCode:    429,
			wantTitle:   "rate_limited",
			wantMessage: "Slow down",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app := fiber.New()
			app.Get("/test", func(c *fiber.Ctx) error {
				return RenderError(c, tc.err)
			})

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			assert.Equal(t, tc.wantCode, resp.StatusCode)

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			var result map[string]any
			require.NoError(t, json.Unmarshal(body, &result))

			assert.Equal(t, float64(tc.wantCode), result["code"])
			assert.Equal(t, tc.wantTitle, result["title"])
			assert.Equal(t, tc.wantMessage, result["message"])
		})
	}
}

func TestRenderError_MultipleGenericErrorsSanitized(t *testing.T) {
	t.Parallel()

	genericErrors := []error{
		errors.New("password=secret123"),
		fmt.Errorf("wrapped: %w", errors.New("nested internal")),
		errors.New("sql: connection refused at 10.0.0.1:5432"),
	}

	for _, genericErr := range genericErrors {
		t.Run(genericErr.Error(), func(t *testing.T) {
			t.Parallel()

			app := fiber.New()
			app.Get("/test", func(c *fiber.Ctx) error {
				return RenderError(c, genericErr)
			})

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			assert.Equal(t, fiber.StatusInternalServerError, resp.StatusCode)

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			var result map[string]any
			require.NoError(t, json.Unmarshal(body, &result))

			assert.Equal(t, "request_failed", result["title"])
			assert.Equal(t, "An internal error occurred", result["message"])
			assert.NotContains(t, string(body), genericErr.Error(),
				"internal error message should not leak through to the client")
		})
	}
}

func TestRenderError_WrappedErrorResponseConflict(t *testing.T) {
	t.Parallel()

	original := ErrorResponse{
		Code:    409,
		Title:   "conflict",
		Message: "duplicate resource",
	}
	wrappedErr := fmt.Errorf("layer: %w", original)

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return RenderError(c, wrappedErr)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, 409, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(body, &result))
	assert.Equal(t, "conflict", result["title"])
	assert.Equal(t, "duplicate resource", result["message"])
}

func TestRenderError_WrappedFiberErrorForbidden(t *testing.T) {
	t.Parallel()

	wrappedErr := fmt.Errorf("context: %w", fiber.NewError(403, "forbidden resource"))

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return RenderError(c, wrappedErr)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, 403, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(body, &result))
	assert.Equal(t, "forbidden resource", result["message"])
}

// ---------------------------------------------------------------------------
// FiberErrorHandler
// ---------------------------------------------------------------------------

func TestFiberErrorHandler_FiberErrorNotFound(t *testing.T) {
	t.Parallel()

	app := fiber.New(fiber.Config{
		ErrorHandler: FiberErrorHandler,
	})
	app.Get("/test", func(c *fiber.Ctx) error {
		return fiber.NewError(fiber.StatusNotFound, "route not found")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, fiber.StatusNotFound, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(body, &result))
	assert.Equal(t, float64(404), result["code"])
	assert.Equal(t, "request_failed", result["title"])
	assert.Equal(t, "route not found", result["message"])
}

func TestFiberErrorHandler_GenericError(t *testing.T) {
	t.Parallel()

	app := fiber.New(fiber.Config{
		ErrorHandler: FiberErrorHandler,
	})
	app.Get("/test", func(c *fiber.Ctx) error {
		return errors.New("database connection refused")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, fiber.StatusInternalServerError, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(body, &result))

	assert.Equal(t, "request_failed", result["title"])
	assert.Equal(t, "An internal error occurred", result["message"])
}

func TestFiberErrorHandler_FiberErrorWithVariousStatusCodes(t *testing.T) {
	t.Parallel()

	codes := []int{400, 401, 403, 404, 405, 409, 422, 429, 500, 502, 503}

	for _, code := range codes {
		t.Run(fmt.Sprintf("status_%d", code), func(t *testing.T) {
			t.Parallel()

			app := fiber.New(fiber.Config{
				ErrorHandler: FiberErrorHandler,
			})
			msg := fmt.Sprintf("error with code %d", code)
			app.Get("/test", func(c *fiber.Ctx) error {
				return fiber.NewError(code, msg)
			})

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			assert.Equal(t, code, resp.StatusCode)

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			var result map[string]any
			require.NoError(t, json.Unmarshal(body, &result))
			assert.Equal(t, float64(code), result["code"])
			assert.Equal(t, msg, result["message"])
		})
	}
}

func TestFiberErrorHandler_ErrorResponseType(t *testing.T) {
	t.Parallel()

	app := fiber.New(fiber.Config{
		ErrorHandler: FiberErrorHandler,
	})
	app.Get("/test", func(c *fiber.Ctx) error {
		return ErrorResponse{
			Code:    422,
			Title:   "validation_error",
			Message: "field required",
		}
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(body, &result))
	assert.Equal(t, "validation_error", result["title"])
	assert.Equal(t, "field required", result["message"])
}

func TestFiberErrorHandler_RouteNotFound(t *testing.T) {
	t.Parallel()

	app := fiber.New(fiber.Config{
		ErrorHandler: FiberErrorHandler,
	})
	app.Get("/exists", func(c *fiber.Ctx) error {
		return c.SendStatus(fiber.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/does-not-exist", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, fiber.StatusNotFound, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(body, &result))
	assert.Equal(t, float64(404), result["code"])
	assert.Equal(t, "request_failed", result["title"])
}

func TestFiberErrorHandler_MethodNotAllowed(t *testing.T) {
	t.Parallel()

	app := fiber.New(fiber.Config{
		ErrorHandler: FiberErrorHandler,
	})
	app.Get("/test", func(c *fiber.Ctx) error {
		return c.SendStatus(fiber.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	// Fiber sends 404 by default unless MethodNotAllowed is enabled.
	assert.True(t, resp.StatusCode == 404 || resp.StatusCode == 405)
}

// ---------------------------------------------------------------------------
// Respond and RespondStatus helpers
// ---------------------------------------------------------------------------

func TestRespond_ValidPayload(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return Respond(c, fiber.StatusOK, fiber.Map{"result": "ok"})
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, fiber.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(body, &result))
	assert.Equal(t, "ok", result["result"])
}

func TestRespond_InvalidStatusDefaultsTo500(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
	}{
		{"negative status", -1},
		{"zero status", 0},
		{"status below 100", 99},
		{"status above 599", 600},
		{"very large status", 9999},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app := fiber.New()
			app.Get("/test", func(c *fiber.Ctx) error {
				return Respond(c, tc.status, fiber.Map{"msg": "test"})
			})

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			assert.Equal(t, fiber.StatusInternalServerError, resp.StatusCode)
		})
	}
}

func TestRespond_BoundaryStatusCodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		status     int
		wantStatus int
	}{
		{"100 Continue", 100, 100},
		{"599 custom", 599, 599},
		{"200 OK", 200, 200},
		{"204 No Content", 204, 204},
		{"301 Moved", 301, 301},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app := fiber.New()
			app.Get("/test", func(c *fiber.Ctx) error {
				return Respond(c, tc.status, fiber.Map{"msg": "test"})
			})

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			assert.Equal(t, tc.wantStatus, resp.StatusCode)
		})
	}
}

func TestRespondStatus_ValidStatus(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return RespondStatus(c, fiber.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, fiber.StatusNoContent, resp.StatusCode)
}

func TestRespondStatus_InvalidStatusDefaultsTo500(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return RespondStatus(c, -1)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, fiber.StatusInternalServerError, resp.StatusCode)
}

func TestRespond_NilPayload(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return Respond(c, fiber.StatusOK, nil)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, fiber.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "null", string(body))
}

// ---------------------------------------------------------------------------
// ExtractTokenFromHeader
// ---------------------------------------------------------------------------

func TestExtractTokenFromHeader_BearerToken(t *testing.T) {
	t.Parallel()

	app := fiber.New()

	var token string

	app.Get("/test", func(c *fiber.Ctx) error {
		token = ExtractTokenFromHeader(c)
		return c.SendStatus(fiber.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer my-jwt-token-123")
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, "my-jwt-token-123", token)
}

func TestExtractTokenFromHeader_BearerCaseInsensitive(t *testing.T) {
	t.Parallel()

	app := fiber.New()

	var token string

	app.Get("/test", func(c *fiber.Ctx) error {
		token = ExtractTokenFromHeader(c)
		return c.SendStatus(fiber.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "BEARER my-jwt-token-123")
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, "my-jwt-token-123", token)
}

func TestExtractTokenFromHeader_RawToken(t *testing.T) {
	t.Parallel()

	app := fiber.New()

	var token string

	app.Get("/test", func(c *fiber.Ctx) error {
		token = ExtractTokenFromHeader(c)
		return c.SendStatus(fiber.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "raw-token-no-bearer-prefix")
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, "raw-token-no-bearer-prefix", token)
}

func TestExtractTokenFromHeader_EmptyHeader(t *testing.T) {
	t.Parallel()

	app := fiber.New()

	var token string

	app.Get("/test", func(c *fiber.Ctx) error {
		token = ExtractTokenFromHeader(c)
		return c.SendStatus(fiber.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Empty(t, token)
}

func TestExtractTokenFromHeader_BearerWithExtraSpaces(t *testing.T) {
	t.Parallel()

	app := fiber.New()

	var token string

	app.Get("/test", func(c *fiber.Ctx) error {
		token = ExtractTokenFromHeader(c)
		return c.SendStatus(fiber.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer   my-token  ")
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	// strings.Fields collapses whitespace, so "Bearer   my-token  " => ["Bearer", "my-token"].
	// The token is correctly extracted regardless of extra whitespace.
	assert.Equal(t, "my-token", token, "extra spaces between Bearer and token should be handled correctly")
}

func TestExtractTokenFromHeader_BearerLowercase(t *testing.T) {
	t.Parallel()

	app := fiber.New()

	var token string

	app.Get("/test", func(c *fiber.Ctx) error {
		token = ExtractTokenFromHeader(c)
		return c.SendStatus(fiber.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "bearer my-token-lowercase")
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, "my-token-lowercase", token)
}

// ---------------------------------------------------------------------------
// Ping, Version, NotImplemented, Welcome handlers
// ---------------------------------------------------------------------------

func TestPing(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/ping", Ping)

	req := httptest.NewRequest(http.MethodGet, "/ping", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, fiber.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "pong", string(body))
}

func TestVersion(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/version", Version)

	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, fiber.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(body, &result))
	assert.Contains(t, result, "version")
	assert.Contains(t, result, "requestDate")
}

func TestNotImplementedEndpoint(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/test", NotImplementedEndpoint)

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, fiber.StatusNotImplemented, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(body, &result))
	assert.Equal(t, "not_implemented", result["title"])
	assert.Equal(t, "Not implemented yet", result["message"])
}

func TestWelcome(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/", Welcome("my-service", "A financial service"))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, fiber.StatusOK, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(body, &result))
	assert.Equal(t, "my-service", result["service"])
	assert.Equal(t, "A financial service", result["description"])
}
