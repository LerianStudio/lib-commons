//go:build unit

package http

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// ErrorResponse edge cases
// ---------------------------------------------------------------------------

func TestErrorResponse_EmptyMessageReturnsEmpty(t *testing.T) {
	t.Parallel()

	errResp := ErrorResponse{
		Code:    400,
		Title:   "bad_request",
		Message: "",
	}

	assert.Equal(t, "", errResp.Error())
}

func TestErrorResponse_JSONDeserializationFromString(t *testing.T) {
	t.Parallel()

	jsonData := `{"code":503,"title":"service_unavailable","message":"try again later"}`

	var errResp ErrorResponse
	require.NoError(t, json.Unmarshal([]byte(jsonData), &errResp))

	assert.Equal(t, 503, errResp.Code)
	assert.Equal(t, "service_unavailable", errResp.Title)
	assert.Equal(t, "try again later", errResp.Message)
}

func TestErrorResponse_PartialJSONDeserializationOnlyCode(t *testing.T) {
	t.Parallel()

	// Only code field present
	jsonData := `{"code":400}`

	var errResp ErrorResponse
	require.NoError(t, json.Unmarshal([]byte(jsonData), &errResp))

	assert.Equal(t, 400, errResp.Code)
	assert.Equal(t, "", errResp.Title)
	assert.Equal(t, "", errResp.Message)
}

func TestErrorResponse_PartialJSONDeserializationOnlyMessage(t *testing.T) {
	t.Parallel()

	jsonData := `{"message":"something went wrong"}`

	var errResp ErrorResponse
	require.NoError(t, json.Unmarshal([]byte(jsonData), &errResp))

	assert.Equal(t, 0, errResp.Code)
	assert.Equal(t, "", errResp.Title)
	assert.Equal(t, "something went wrong", errResp.Message)
}

func TestErrorResponse_JSONRoundTripWithSpecialChars(t *testing.T) {
	t.Parallel()

	original := ErrorResponse{
		Code:    418,
		Title:   "im_a_teapot",
		Message: "I'm a teapot with \"quotes\" and <html>",
	}

	data, err := json.Marshal(original)
	require.NoError(t, err)

	var decoded ErrorResponse
	require.NoError(t, json.Unmarshal(data, &decoded))

	assert.Equal(t, original, decoded)
}

func TestErrorResponse_EmptyJSON(t *testing.T) {
	t.Parallel()

	jsonData := `{}`

	var errResp ErrorResponse
	require.NoError(t, json.Unmarshal([]byte(jsonData), &errResp))

	assert.Equal(t, 0, errResp.Code)
	assert.Equal(t, "", errResp.Title)
	assert.Equal(t, "", errResp.Message)
}

// ---------------------------------------------------------------------------
// Nil guard tests
// ---------------------------------------------------------------------------

func TestRenderError_NilContext(t *testing.T) {
	t.Parallel()

	err := RenderError(nil, ErrorResponse{Code: 400, Title: "bad", Message: "nil ctx"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrContextNotFound)
}

func TestRenderError_NilError(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return RenderError(c, nil)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	// RenderError(c, nil) returns nil, so no response body is written -> Fiber defaults to 200
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// ---------------------------------------------------------------------------
// RenderError code boundary tests
// ---------------------------------------------------------------------------

func TestRenderError_CodeBoundaryAt100(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return RenderError(c, ErrorResponse{
			Code:    100,
			Title:   "continue",
			Message: "boundary test at 100",
		})
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, 100, resp.StatusCode)
}

func TestRenderError_CodeBoundaryAt599(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return RenderError(c, ErrorResponse{
			Code:    599,
			Title:   "custom_error",
			Message: "boundary test at 599",
		})
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, 599, resp.StatusCode)
}

func TestRenderError_CodeAt99FallsBackTo500(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return RenderError(c, ErrorResponse{
			Code:    99,
			Title:   "test_error",
			Message: "code 99 should fall back",
		})
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, fiber.StatusInternalServerError, resp.StatusCode)
}

func TestRenderError_CodeAt600FallsBackTo500(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return RenderError(c, ErrorResponse{
			Code:    600,
			Title:   "test_error",
			Message: "code 600 should fall back",
		})
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, fiber.StatusInternalServerError, resp.StatusCode)
}

// ---------------------------------------------------------------------------
// RenderError with both empty title and message
// ---------------------------------------------------------------------------

func TestRenderError_EmptyTitleAndMessageDefaultsBoth(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return RenderError(c, ErrorResponse{
			Code:    500,
			Title:   "",
			Message: "",
		})
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

	// Both should be filled with defaults
	assert.Equal(t, "request_failed", result["title"])
	assert.Equal(t, "Internal Server Error", result["message"])
}

// ---------------------------------------------------------------------------
// RenderError response structure validation
// ---------------------------------------------------------------------------

func TestRenderError_ResponseHasExactlyThreeFields(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return RenderError(c, ErrorResponse{
			Code:    409,
			Title:   "conflict",
			Message: "resource already exists",
		})
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(body, &result))

	assert.Len(t, result, 3, "response should have exactly code, title, and message")
	assert.Contains(t, result, "code")
	assert.Contains(t, result, "title")
	assert.Contains(t, result, "message")
}

// ---------------------------------------------------------------------------
// RenderError across HTTP methods
// ---------------------------------------------------------------------------

func TestRenderError_WorksForAllHTTPMethods(t *testing.T) {
	t.Parallel()

	methods := []string{
		http.MethodGet,
		http.MethodPost,
		http.MethodPut,
		http.MethodPatch,
		http.MethodDelete,
	}

	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			t.Parallel()

			app := fiber.New()

			handler := func(c *fiber.Ctx) error {
				return RenderError(c, ErrorResponse{
					Code:    400,
					Title:   "bad_request",
					Message: "test",
				})
			}

			switch method {
			case http.MethodGet:
				app.Get("/test", handler)
			case http.MethodPost:
				app.Post("/test", handler)
			case http.MethodPut:
				app.Put("/test", handler)
			case http.MethodPatch:
				app.Patch("/test", handler)
			case http.MethodDelete:
				app.Delete("/test", handler)
			}

			req := httptest.NewRequest(method, "/test", nil)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			assert.Equal(t, fiber.StatusBadRequest, resp.StatusCode)
		})
	}
}

// ---------------------------------------------------------------------------
// RenderError with fiber.Error with default message
// ---------------------------------------------------------------------------

func TestRenderError_FiberErrorDefaultMessage(t *testing.T) {
	t.Parallel()

	// fiber.NewError with just a code uses the default HTTP status text
	fiberErr := fiber.NewError(fiber.StatusGatewayTimeout)

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return RenderError(c, fiberErr)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	assert.Equal(t, fiber.StatusGatewayTimeout, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var result map[string]any
	require.NoError(t, json.Unmarshal(body, &result))
	assert.Equal(t, "request_failed", result["title"])
}

// ---------------------------------------------------------------------------
// RenderError content type
// ---------------------------------------------------------------------------

func TestRenderError_ReturnsJSON(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return RenderError(c, ErrorResponse{
			Code:    400,
			Title:   "bad_request",
			Message: "test",
		})
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	contentType := resp.Header.Get("Content-Type")
	assert.Contains(t, contentType, "application/json")
}

// ---------------------------------------------------------------------------
// RenderError with various 2xx/3xx codes (unusual but valid)
// ---------------------------------------------------------------------------

func TestRenderError_UnusualValidCodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		code int
	}{
		{"200 OK (unusual for error)", 200},
		{"201 Created (unusual for error)", 201},
		{"301 Moved Permanently", 301},
		{"302 Found", 302},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app := fiber.New()
			app.Get("/test", func(c *fiber.Ctx) error {
				return RenderError(c, ErrorResponse{
					Code:    tt.code,
					Title:   "test",
					Message: "unusual code",
				})
			})

			req := httptest.NewRequest(http.MethodGet, "/test", nil)
			resp, err := app.Test(req)
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			// Valid HTTP codes between 100-599 should be used as-is
			assert.Equal(t, tt.code, resp.StatusCode)
		})
	}
}
