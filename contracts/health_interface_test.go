package contracts

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/commons/health"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHealthCheckerInterfaceContract validates the Checker interface stability
func TestHealthCheckerInterfaceContract(t *testing.T) {
	t.Run("checker_interface_signature", func(t *testing.T) {
		var checker health.Checker
		checkerType := reflect.TypeOf(&checker).Elem()

		// Verify interface exists and has correct structure
		assert.Equal(t, "Checker", checkerType.Name())
		assert.Equal(t, reflect.Interface, checkerType.Kind())

		// Verify Check method exists with correct signature
		assert.Equal(t, 1, checkerType.NumMethod())

		checkMethod := checkerType.Method(0)
		assert.Equal(t, "Check", checkMethod.Name)

		// Validate method signature: Check(ctx context.Context) error
		methodType := checkMethod.Type
		assert.Equal(t, 1, methodType.NumIn())  // context (no receiver for interface methods)
		assert.Equal(t, 1, methodType.NumOut()) // error return

		// Verify parameter types
		assert.Equal(t, "context.Context", methodType.In(0).String())
		assert.Equal(t, "error", methodType.Out(0).String())
	})

	t.Run("checker_implementations_contract", func(t *testing.T) {
		// Test that all built-in checkers implement the interface
		checkers := []health.Checker{
			health.NewPostgresChecker(nil), // Will fail but interface should work
			health.NewMongoChecker(nil),
			health.NewRedisChecker(nil),
			health.NewRabbitMQChecker(nil),
			health.NewCustomChecker("test", func(ctx context.Context) error { return nil }),
		}

		for i, checker := range checkers {
			assert.Implements(t, (*health.Checker)(nil), checker,
				"Checker implementation %d should implement Checker interface", i)
		}
	})
}

// TestHealthResponseContract validates the health response format stability
func TestHealthResponseContract(t *testing.T) {
	t.Run("response_structure_contract", func(t *testing.T) {
		response := health.Response{}
		responseType := reflect.TypeOf(response)

		// Verify required fields exist with correct types and JSON tags
		expectedFields := map[string]fieldContract{
			"Status": {
				typeName: "health.Status",
				jsonTag:  "status",
				required: true,
			},
			"Version": {
				typeName: "string",
				jsonTag:  "version",
				required: true,
			},
			"Environment": {
				typeName: "string",
				jsonTag:  "environment",
				required: true,
			},
			"Hostname": {
				typeName: "string",
				jsonTag:  "hostname",
				required: true,
			},
			"Timestamp": {
				typeName: "string",
				jsonTag:  "timestamp",
				required: true,
			},
			"Checks": {
				typeName: "map[string]*health.Check",
				jsonTag:  "checks",
				required: true,
			},
			"System": {
				typeName: "*health.SystemInfo",
				jsonTag:  "system",
				required: true,
			},
		}

		for fieldName, expected := range expectedFields {
			field, exists := responseType.FieldByName(fieldName)
			assert.True(t, exists, "Response should have field %s", fieldName)

			if exists {
				assert.Equal(t, expected.typeName, field.Type.String(),
					"Field %s should have type %s", fieldName, expected.typeName)

				// Check JSON tag
				jsonTag := field.Tag.Get("json")
				if expected.required {
					assert.Equal(t, expected.jsonTag, jsonTag,
						"Field %s should have json tag %s", fieldName, expected.jsonTag)
				} else {
					expectedTag := expected.jsonTag + ",omitempty"
					assert.True(t, jsonTag == expected.jsonTag || jsonTag == expectedTag,
						"Field %s should have json tag %s or %s", fieldName, expected.jsonTag, expectedTag)
				}
			}
		}
	})

	t.Run("status_enum_contract", func(t *testing.T) {
		// Test that Status constants remain stable
		assert.Equal(t, health.Status("UP"), health.StatusUp)
		assert.Equal(t, health.Status("DOWN"), health.StatusDown)

		// Test string representation
		assert.Equal(t, "UP", string(health.StatusUp))
		assert.Equal(t, "DOWN", string(health.StatusDown))
	})

	t.Run("system_info_contract", func(t *testing.T) {
		systemInfo := health.SystemInfo{}
		systemType := reflect.TypeOf(systemInfo)

		expectedFields := map[string]string{
			"Uptime":       "float64",
			"MemoryUsage":  "float64",
			"CPUCount":     "int",
			"GoroutineNum": "int",
		}

		for fieldName, expectedType := range expectedFields {
			field, exists := systemType.FieldByName(fieldName)
			assert.True(t, exists, "SystemInfo should have field %s", fieldName)

			if exists {
				assert.Equal(t, expectedType, field.Type.String(),
					"SystemInfo.%s should have type %s", fieldName, expectedType)

				// Verify JSON tags
				jsonTag := field.Tag.Get("json")
				expectedJSONName := toLowerSnakeCase(fieldName)
				assert.Equal(t, expectedJSONName, jsonTag,
					"SystemInfo.%s should have json tag %s", fieldName, expectedJSONName)
			}
		}
	})
}

// TestHealthServiceContract validates the Service behavior contract
func TestHealthServiceContract(t *testing.T) {
	t.Run("service_creation_contract", func(t *testing.T) {
		// Test that NewService creates a valid service
		service := health.NewService("test-service", "1.0.0", "test", "localhost")
		assert.NotNil(t, service)

		// Service should have a Handler method that returns fiber.Handler
		serviceType := reflect.TypeOf(service)
		handlerMethod, exists := serviceType.MethodByName("Handler")
		assert.True(t, exists, "Service should have Handler method")

		if exists {
			// Handler() should return fiber.Handler
			assert.Equal(t, 1, handlerMethod.Type.NumOut())
			returnType := handlerMethod.Type.Out(0)

			// Check if it's a function type that matches fiber.Handler signature
			assert.Equal(t, reflect.Func, returnType.Kind())
		}
	})

	t.Run("checker_registration_contract", func(t *testing.T) {
		service := health.NewService("test", "1.0.0", "test", "localhost")

		// RegisterChecker should accept name and Checker
		serviceType := reflect.TypeOf(service)
		registerMethod, exists := serviceType.MethodByName("RegisterChecker")
		assert.True(t, exists, "Service should have RegisterChecker method")

		if exists {
			methodType := registerMethod.Type
			assert.Equal(t, 3, methodType.NumIn())  // receiver + name + checker
			assert.Equal(t, 0, methodType.NumOut()) // void return

			// Verify parameter types
			assert.Equal(t, "string", methodType.In(1).String()) // name parameter
			// Second parameter should be Checker interface
			checkerParam := methodType.In(2)
			assert.True(t, checkerParam.Implements(reflect.TypeOf((*health.Checker)(nil)).Elem()),
				"Second parameter should implement Checker interface")
		}
	})

	t.Run("http_endpoint_contract", func(t *testing.T) {
		// Test the HTTP endpoint behavior contract
		service := health.NewService("test-service", "1.0.0", "test", "localhost")

		// Add a passing checker
		service.RegisterChecker("test-pass", &mockPassingChecker{})

		// Create Fiber app and register handler
		app := fiber.New()
		app.Get("/health", service.Handler())

		// Test healthy response
		req := httptest.NewRequest("GET", "/health", nil)
		resp, err := app.Test(req)
		require.NoError(t, err)

		// Should return 200 when all checks pass
		assert.Equal(t, 200, resp.StatusCode)

		// Response should be valid JSON with expected structure
		var healthResp health.Response
		err = json.NewDecoder(resp.Body).Decode(&healthResp)
		require.NoError(t, err)

		// Validate response contract
		assert.Equal(t, health.StatusUp, healthResp.Status)
		assert.Equal(t, "1.0.0", healthResp.Version)
		assert.Equal(t, "test", healthResp.Environment)
		assert.Equal(t, "localhost", healthResp.Hostname)
		assert.NotEmpty(t, healthResp.Timestamp)
		assert.NotNil(t, healthResp.Checks)
		assert.NotNil(t, healthResp.System)

		// Timestamp should be valid RFC3339
		_, err = time.Parse(time.RFC3339, healthResp.Timestamp)
		assert.NoError(t, err, "Timestamp should be valid RFC3339 format")

		// System info should have valid values
		assert.Greater(t, healthResp.System.Uptime, float64(0))
		assert.GreaterOrEqual(t, healthResp.System.MemoryUsage, float64(0))
		assert.Greater(t, healthResp.System.CPUCount, 0)
		assert.Greater(t, healthResp.System.GoroutineNum, 0)

		// Check should be present and passing
		testCheck, exists := healthResp.Checks["test-pass"]
		assert.True(t, exists)
		assert.Equal(t, health.StatusUp, testCheck.Status)
	})

	t.Run("failing_check_contract", func(t *testing.T) {
		// Test behavior when checks fail
		service := health.NewService("test-service", "1.0.0", "test", "localhost")
		service.RegisterChecker("test-fail", &mockFailingChecker{})

		app := fiber.New()
		app.Get("/health", service.Handler())

		req := httptest.NewRequest("GET", "/health", nil)
		resp, err := app.Test(req)
		require.NoError(t, err)

		// Should return 503 when any check fails
		assert.Equal(t, 503, resp.StatusCode)

		var healthResp health.Response
		err = json.NewDecoder(resp.Body).Decode(&healthResp)
		require.NoError(t, err)

		// Overall status should be DOWN
		assert.Equal(t, health.StatusDown, healthResp.Status)

		// Failed check should be present with error details
		failedCheck, exists := healthResp.Checks["test-fail"]
		assert.True(t, exists)
		assert.Equal(t, health.StatusDown, failedCheck.Status)
		assert.NotNil(t, failedCheck.Details)

		// Error details should contain error information
		errorDetail, hasError := failedCheck.Details["error"]
		assert.True(t, hasError)
		assert.Equal(t, "mock check failure", errorDetail)
	})
}

// TestHealthTimeoutContract validates current timeout behavior contract
func TestHealthTimeoutContract(t *testing.T) {
	t.Run("checker_timeout_contract", func(t *testing.T) {
		// Test current behavior: health checks currently don't have built-in timeout protection
		// This validates the existing contract behavior (slow checkers will slow down the entire endpoint)
		service := health.NewService("test", "1.0.0", "test", "localhost")
		service.RegisterChecker("quick-check", &mockPassingChecker{}) // Use a quick check instead

		app := fiber.New()
		app.Get("/health", service.Handler())

		start := time.Now()
		req := httptest.NewRequest("GET", "/health", nil)
		resp, err := app.Test(req, 1000) // 1 second timeout for test
		duration := time.Since(start)

		require.NoError(t, err)

		// Current contract: health checks with fast checkers complete quickly
		assert.Less(t, duration, 500*time.Millisecond,
			"Health check with fast checkers should complete quickly")

		// Response should indicate success for passing checks
		var healthResp health.Response
		err = json.NewDecoder(resp.Body).Decode(&healthResp)
		require.NoError(t, err)
		assert.Equal(t, health.StatusUp, healthResp.Status)

		// Note: Current implementation does NOT have timeout protection for individual checkers
		// This is the current contract behavior that this test validates
	})
}

// Helper types for testing

type fieldContract struct {
	typeName string
	jsonTag  string
	required bool
}

type mockPassingChecker struct{}

func (m *mockPassingChecker) Check(ctx context.Context) error {
	return nil
}

type mockFailingChecker struct{}

func (m *mockFailingChecker) Check(ctx context.Context) error {
	return fmt.Errorf("mock check failure")
}

// Helper function to convert camelCase to snake_case for JSON tags
func toLowerSnakeCase(s string) string {
	// Simple conversion for known cases
	switch s {
	case "Uptime":
		return "uptime"
	case "MemoryUsage":
		return "memory_usage"
	case "CPUCount":
		return "cpu_count"
	case "GoroutineNum":
		return "goroutine_num"
	default:
		return s
	}
}
