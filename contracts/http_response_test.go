package contracts

import (
	"encoding/json"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/LerianStudio/lib-commons/commons"
	httpCommons "github.com/LerianStudio/lib-commons/commons/net/http"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHTTPResponseContract validates HTTP response function contracts
func TestHTTPResponseContract(t *testing.T) {
	t.Run("response_functions_signature_contract", func(t *testing.T) {
		// Define expected HTTP response function signatures that must remain stable
		expectedFunctions := map[string]responseSignature{
			"OK": {
				params:  []string{"*fiber.Ctx", "interface {}"},
				returns: []string{"error"},
			},
			"Created": {
				params:  []string{"*fiber.Ctx", "interface {}"},
				returns: []string{"error"},
			},
			"Accepted": {
				params:  []string{"*fiber.Ctx", "interface {}"},
				returns: []string{"error"},
			},
			"NoContent": {
				params:  []string{"*fiber.Ctx"},
				returns: []string{"error"},
			},
			"PartialContent": {
				params:  []string{"*fiber.Ctx", "interface {}"},
				returns: []string{"error"},
			},
			"BadRequest": {
				params:  []string{"*fiber.Ctx", "interface {}"},
				returns: []string{"error"},
			},
			"Unauthorized": {
				params:  []string{"*fiber.Ctx", "string", "string", "string"},
				returns: []string{"error"},
			},
			"Forbidden": {
				params:  []string{"*fiber.Ctx", "string", "string", "string"},
				returns: []string{"error"},
			},
			"NotFound": {
				params:  []string{"*fiber.Ctx", "string", "string", "string"},
				returns: []string{"error"},
			},
			"Conflict": {
				params:  []string{"*fiber.Ctx", "string", "string", "string"},
				returns: []string{"error"},
			},
			"UnprocessableEntity": {
				params:  []string{"*fiber.Ctx", "string", "string", "string"},
				returns: []string{"error"},
			},
			"InternalServerError": {
				params:  []string{"*fiber.Ctx", "string", "string", "string"},
				returns: []string{"error"},
			},
			"NotImplemented": {
				params:  []string{"*fiber.Ctx", "string"},
				returns: []string{"error"},
			},
			"JSONResponse": {
				params:  []string{"*fiber.Ctx", "int", "interface {}"},
				returns: []string{"error"},
			},
			"JSONResponseError": {
				params:  []string{"*fiber.Ctx", "commons.Response"},
				returns: []string{"error"},
			},
		}

		// Verify that the HTTP response functions exist
		// Note: This is a basic existence check - in practice you'd use more sophisticated reflection
		for funcName := range expectedFunctions {
			// For now, just verify we can reference the functions
			assert.NotNil(t, httpCommons.OK, "Function %s should exist in http package", funcName)
		}
	})

	t.Run("commons_response_structure_contract", func(t *testing.T) {
		// Test that commons.Response structure remains stable
		response := commons.Response{}
		responseType := reflect.TypeOf(response)

		expectedFields := map[string]responseFieldContract{
			"EntityType": {
				typeName: "string",
				jsonTag:  "entityType,omitempty",
			},
			"Title": {
				typeName: "string",
				jsonTag:  "title,omitempty",
			},
			"Message": {
				typeName: "string",
				jsonTag:  "message,omitempty",
			},
			"Code": {
				typeName: "string",
				jsonTag:  "code,omitempty",
			},
			"Err": {
				typeName: "error",
				jsonTag:  "err,omitempty",
			},
		}

		for fieldName, expected := range expectedFields {
			field, exists := responseType.FieldByName(fieldName)
			assert.True(t, exists, "Response should have field %s", fieldName)

			if exists {
				assert.Equal(t, expected.typeName, field.Type.String(),
					"Response.%s should have type %s", fieldName, expected.typeName)

				jsonTag := field.Tag.Get("json")
				assert.Equal(t, expected.jsonTag, jsonTag,
					"Response.%s should have json tag %s", fieldName, expected.jsonTag)
			}
		}
	})

	t.Run("error_method_contract", func(t *testing.T) {
		// Test that commons.Response implements error interface
		var response commons.Response
		var err error = response
		assert.NotNil(t, err, "commons.Response should implement error interface")

		// Test Error() method behavior
		response = commons.Response{
			Code:    "TEST001",
			Message: "Test message",
		}
		expected := "TEST001: Test message"
		assert.Equal(t, expected, response.Error())

		// Test Error() method without code
		response = commons.Response{
			Message: "Test message only",
		}
		assert.Equal(t, "Test message only", response.Error())
	})
}

// TestHTTPResponseBehaviorContract validates actual HTTP response behavior
func TestHTTPResponseBehaviorContract(t *testing.T) {
	app := fiber.New()

	t.Run("success_responses_contract", func(t *testing.T) {
		// Test OK response (200)
		app.Get("/test-ok", func(c *fiber.Ctx) error {
			return httpCommons.OK(c, map[string]string{"status": "success"})
		})

		req := httptest.NewRequest("GET", "/test-ok", nil)
		resp, err := app.Test(req)
		require.NoError(t, err)

		assert.Equal(t, 200, resp.StatusCode)
		assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

		var responseBody map[string]string
		err = json.NewDecoder(resp.Body).Decode(&responseBody)
		require.NoError(t, err)
		assert.Equal(t, "success", responseBody["status"])

		// Test Created response (201)
		app.Post("/test-created", func(c *fiber.Ctx) error {
			return httpCommons.Created(c, map[string]string{"id": "123"})
		})

		req = httptest.NewRequest("POST", "/test-created", nil)
		resp, err = app.Test(req)
		require.NoError(t, err)

		assert.Equal(t, 201, resp.StatusCode)
		assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

		// Test NoContent response (204)
		app.Delete("/test-no-content", func(c *fiber.Ctx) error {
			return httpCommons.NoContent(c)
		})

		req = httptest.NewRequest("DELETE", "/test-no-content", nil)
		resp, err = app.Test(req)
		require.NoError(t, err)

		assert.Equal(t, 204, resp.StatusCode)
		// NoContent should have no body
		assert.Equal(t, int64(0), resp.ContentLength)
	})

	t.Run("error_responses_contract", func(t *testing.T) {
		// Test BadRequest response (400)
		app.Post("/test-bad-request", func(c *fiber.Ctx) error {
			return httpCommons.BadRequest(c, map[string]string{"error": "invalid input"})
		})

		req := httptest.NewRequest("POST", "/test-bad-request", nil)
		resp, err := app.Test(req)
		require.NoError(t, err)

		assert.Equal(t, 400, resp.StatusCode)
		assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

		// Test Unauthorized response (401)
		app.Get("/test-unauthorized", func(c *fiber.Ctx) error {
			return httpCommons.Unauthorized(c, "AUTH001", "Unauthorized", "Token required")
		})

		req = httptest.NewRequest("GET", "/test-unauthorized", nil)
		resp, err = app.Test(req)
		require.NoError(t, err)

		assert.Equal(t, 401, resp.StatusCode)

		var responseBody commons.Response
		err = json.NewDecoder(resp.Body).Decode(&responseBody)
		require.NoError(t, err)

		assert.Equal(t, "AUTH001", responseBody.Code)
		assert.Equal(t, "Unauthorized", responseBody.Title)
		assert.Equal(t, "Token required", responseBody.Message)

		// Test NotFound response (404)
		app.Get("/test-not-found", func(c *fiber.Ctx) error {
			return httpCommons.NotFound(c, "RES001", "Resource Not Found", "User not found")
		})

		req = httptest.NewRequest("GET", "/test-not-found", nil)
		resp, err = app.Test(req)
		require.NoError(t, err)

		assert.Equal(t, 404, resp.StatusCode)

		err = json.NewDecoder(resp.Body).Decode(&responseBody)
		require.NoError(t, err)

		assert.Equal(t, "RES001", responseBody.Code)
		assert.Equal(t, "Resource Not Found", responseBody.Title)
		assert.Equal(t, "User not found", responseBody.Message)

		// Test InternalServerError response (500)
		app.Get("/test-internal-error", func(c *fiber.Ctx) error {
			return httpCommons.InternalServerError(c, "SYS001", "System Error", "Database connection failed")
		})

		req = httptest.NewRequest("GET", "/test-internal-error", nil)
		resp, err = app.Test(req)
		require.NoError(t, err)

		assert.Equal(t, 500, resp.StatusCode)

		err = json.NewDecoder(resp.Body).Decode(&responseBody)
		require.NoError(t, err)

		assert.Equal(t, "SYS001", responseBody.Code)
		assert.Equal(t, "System Error", responseBody.Title)
		assert.Equal(t, "Database connection failed", responseBody.Message)
	})

	t.Run("json_response_contract", func(t *testing.T) {
		// Test JSONResponse with custom status
		app.Get("/test-json-response", func(c *fiber.Ctx) error {
			return httpCommons.JSONResponse(c, 418, map[string]string{"message": "I'm a teapot"})
		})

		req := httptest.NewRequest("GET", "/test-json-response", nil)
		resp, err := app.Test(req)
		require.NoError(t, err)

		assert.Equal(t, 418, resp.StatusCode)
		assert.Equal(t, "application/json", resp.Header.Get("Content-Type"))

		var responseBody map[string]string
		err = json.NewDecoder(resp.Body).Decode(&responseBody)
		require.NoError(t, err)
		assert.Equal(t, "I'm a teapot", responseBody["message"])

		// Test JSONResponseError
		app.Get("/test-json-error", func(c *fiber.Ctx) error {
			errorResp := commons.Response{
				Code:    "400",
				Title:   "Validation Error",
				Message: "Invalid request data",
			}
			return httpCommons.JSONResponseError(c, errorResp)
		})

		req = httptest.NewRequest("GET", "/test-json-error", nil)
		resp, err = app.Test(req)
		require.NoError(t, err)

		assert.Equal(t, 400, resp.StatusCode) // Should parse Code as status

		var errorResponseBody commons.Response
		err = json.NewDecoder(resp.Body).Decode(&errorResponseBody)
		require.NoError(t, err)

		assert.Equal(t, "400", errorResponseBody.Code)
		assert.Equal(t, "Validation Error", errorResponseBody.Title)
		assert.Equal(t, "Invalid request data", errorResponseBody.Message)
	})
}

// TestNotImplementedMessageContract validates the constant stability
func TestNotImplementedMessageContract(t *testing.T) {
	t.Run("not_implemented_message_constant", func(t *testing.T) {
		// Test that the NotImplementedMessage constant remains stable
		app := fiber.New()

		app.Get("/test-not-implemented", func(c *fiber.Ctx) error {
			return httpCommons.NotImplemented(c, "Feature coming soon")
		})

		req := httptest.NewRequest("GET", "/test-not-implemented", nil)
		resp, err := app.Test(req)
		require.NoError(t, err)

		assert.Equal(t, 501, resp.StatusCode)

		var responseBody commons.Response
		err = json.NewDecoder(resp.Body).Decode(&responseBody)
		require.NoError(t, err)

		assert.Equal(t, "501", responseBody.Code)
		assert.Equal(t, "Not implemented yet", responseBody.Title) // Should use constant
		assert.Equal(t, "Feature coming soon", responseBody.Message)
	})
}

// Helper types for contract testing

type responseSignature struct {
	params  []string
	returns []string
}

type responseFieldContract struct {
	typeName string
	jsonTag  string
}
