// Package http provides standardized HTTP response functions and middleware components
// for building consistent REST APIs using the Fiber web framework.
//
// This package implements enterprise-grade HTTP utilities with:
//   - Standardized response functions for all HTTP status codes
//   - Consistent error response format across services
//   - Middleware for CORS, authentication, logging, and telemetry
//   - HTTP client with connection pooling and retry logic
//   - Cursor-based pagination support
//
// # Quick Start
//
// Basic usage with Fiber:
//
//	app := fiber.New()
//
//	// Add middleware
//	app.Use(http.WithCORS())
//	app.Use(http.WithHTTPLogging())
//
//	// Use standardized responses
//	app.Get("/users/:id", func(c *fiber.Ctx) error {
//	    user, err := userService.GetUser(c.Params("id"))
//	    if err != nil {
//	        return http.NotFound(c, "USER_001", "User Not Found", err.Error())
//	    }
//	    return http.OK(c, user)
//	})
//
// # Response Functions
//
// All response functions follow consistent patterns:
//   - Success responses (2xx): Accept data payload
//   - Error responses (4xx, 5xx): Accept structured error information
//   - All responses return fiber.Handler compatible errors
//
// # Error Response Format
//
// All error responses use the commons.Response struct:
//
//	type Response struct {
//	    Code    string `json:"code"`          // Error code (e.g., "USER_001")
//	    Title   string `json:"title"`         // Human readable title
//	    Message string `json:"message"`       // Detailed error message
//	}
//
// # Middleware Components
//
// Available middleware functions:
//   - WithCORS(): Cross-origin resource sharing
//   - WithBasicAuth(): HTTP basic authentication
//   - WithHTTPLogging(): Structured request/response logging
//   - WithTelemetry(): OpenTelemetry integration
//
// # Best Practices
//
//  1. Use consistent error codes across your service
//  2. Provide meaningful error titles and messages
//  3. Apply middleware in the correct order (CORS first)
//  4. Use appropriate HTTP status codes for each scenario
//  5. Include request correlation IDs for tracing
//
// See OpenAPI documentation in docs/openapi/ for complete API contracts.
package http

import (
	"net/http"
	"strconv"

	"github.com/LerianStudio/lib-commons/commons"
	"github.com/gofiber/fiber/v2"
)

// NotImplementedMessage is the default message for not implemented endpoints.
const NotImplementedMessage = "Not implemented yet"

// Unauthorized sends an HTTP 401 Unauthorized response with a custom code, title and message.
//
// Use this function when authentication is required but missing or invalid.
// The response follows the standard commons.Response format for consistent error handling.
//
// Example:
//
//	func LoginRequired(c *fiber.Ctx) error {
//	    token := c.Get("Authorization")
//	    if token == "" {
//	        return http.Unauthorized(c, "AUTH_001", "Authentication Required", "Bearer token required")
//	    }
//	    // ... validate token
//	}
//
// Response format:
//
//	{
//	    "code": "AUTH_001",
//	    "title": "Authentication Required",
//	    "message": "Bearer token required"
//	}
func Unauthorized(c *fiber.Ctx, code, title, message string) error {
	return c.Status(http.StatusUnauthorized).JSON(commons.Response{
		Code:    code,
		Title:   title,
		Message: message,
	})
}

// Forbidden sends an HTTP 403 Forbidden response with a custom code, title and message.
//
// Use this function when authentication succeeded but authorization failed.
// This indicates the user is authenticated but lacks permission for the requested resource.
//
// Example:
//
//	func AdminOnly(c *fiber.Ctx) error {
//	    userRole := c.Locals("user_role").(string)
//	    if userRole != "admin" {
//	        return http.Forbidden(c, "AUTH_003", "Access Denied", "Admin role required")
//	    }
//	    // ... proceed with admin operation
//	}
func Forbidden(c *fiber.Ctx, code, title, message string) error {
	return c.Status(http.StatusForbidden).JSON(commons.Response{
		Code:    code,
		Title:   title,
		Message: message,
	})
}

// BadRequest sends an HTTP 400 Bad Request response with a custom body.
//
// Use this function for client errors such as malformed requests, validation failures,
// or missing required parameters. Unlike other error responses, this accepts any payload
// to accommodate validation error details.
//
// Example with validation errors:
//
//	func ValidateUser(c *fiber.Ctx) error {
//	    var user User
//	    if err := c.BodyParser(&user); err != nil {
//	        return http.BadRequest(c, map[string]string{
//	            "error": "Invalid JSON body",
//	            "details": err.Error(),
//	        })
//	    }
//	    // ... validate user fields
//	}
func BadRequest(c *fiber.Ctx, s any) error {
	return c.Status(http.StatusBadRequest).JSON(s)
}

// Created sends an HTTP 201 Created response with a custom body.
//
// Use this function after successfully creating a new resource.
// The response body typically contains the created resource with its assigned ID.
//
// Example:
//
//	func CreateUser(c *fiber.Ctx) error {
//	    var req CreateUserRequest
//	    if err := c.BodyParser(&req); err != nil {
//	        return http.BadRequest(c, err)
//	    }
//
//	    user, err := userService.CreateUser(req)
//	    if err != nil {
//	        return http.InternalServerError(c, "USER_002", "Creation Failed", err.Error())
//	    }
//
//	    return http.Created(c, user)
//	}
func Created(c *fiber.Ctx, s any) error {
	return c.Status(http.StatusCreated).JSON(s)
}

// OK sends an HTTP 200 OK response with a custom body.
//
// Use this function for successful operations that return data.
// This is the most commonly used success response function.
//
// Example:
//
//	func GetUser(c *fiber.Ctx) error {
//	    userID := c.Params("id")
//	    user, err := userService.GetUser(userID)
//	    if err != nil {
//	        return http.NotFound(c, "USER_001", "User Not Found", err.Error())
//	    }
//	    return http.OK(c, user)
//	}
func OK(c *fiber.Ctx, s any) error {
	return c.Status(http.StatusOK).JSON(s)
}

// NoContent sends an HTTP 204 No Content response without anybody.
//
// Use this function for successful operations that don't return data,
// such as DELETE operations or updates that don't need to return the modified resource.
//
// Example:
//
//	func DeleteUser(c *fiber.Ctx) error {
//	    userID := c.Params("id")
//	    err := userService.DeleteUser(userID)
//	    if err != nil {
//	        return http.InternalServerError(c, "USER_004", "Delete Failed", err.Error())
//	    }
//	    return http.NoContent(c)
//	}
func NoContent(c *fiber.Ctx) error {
	return c.SendStatus(http.StatusNoContent)
}

// Accepted sends an HTTP 202 Accepted response with a custom body.
func Accepted(c *fiber.Ctx, s any) error {
	return c.Status(http.StatusAccepted).JSON(s)
}

// PartialContent sends an HTTP 206 Partial Content response with a custom body.
func PartialContent(c *fiber.Ctx, s any) error {
	return c.Status(http.StatusPartialContent).JSON(s)
}

// RangeNotSatisfiable sends an HTTP 416 Requested Range Not Satisfiable response.
func RangeNotSatisfiable(c *fiber.Ctx) error {
	return c.SendStatus(http.StatusRequestedRangeNotSatisfiable)
}

// NotFound sends an HTTP 404 Not Found response with a custom code, title and message.
//
// Use this function when a requested resource doesn't exist.
// Provide specific error codes and messages to help clients understand what wasn't found.
//
// Example:
//
//	func GetUser(c *fiber.Ctx) error {
//	    userID := c.Params("id")
//	    user, err := userService.GetUser(userID)
//	    if errors.Is(err, ErrUserNotFound) {
//	        return http.NotFound(c, "USER_001", "User Not Found",
//	            fmt.Sprintf("User with ID %s does not exist", userID))
//	    }
//	    return http.OK(c, user)
//	}
func NotFound(c *fiber.Ctx, code, title, message string) error {
	return c.Status(http.StatusNotFound).JSON(commons.Response{
		Code:    code,
		Title:   title,
		Message: message,
	})
}

// Conflict sends an HTTP 409 Conflict response with a custom code, title and message.
func Conflict(c *fiber.Ctx, code, title, message string) error {
	return c.Status(http.StatusConflict).JSON(commons.Response{
		Code:    code,
		Title:   title,
		Message: message,
	})
}

// NotImplemented sends an HTTP 501 Not Implemented response with a custom message.
func NotImplemented(c *fiber.Ctx, message string) error {
	return c.Status(http.StatusNotImplemented).JSON(commons.Response{
		Code:    strconv.Itoa(http.StatusNotImplemented),
		Title:   NotImplementedMessage,
		Message: message,
	})
}

// UnprocessableEntity sends an HTTP 422 Unprocessable Entity response with a custom code, title and message.
func UnprocessableEntity(c *fiber.Ctx, code, title, message string) error {
	return c.Status(http.StatusUnprocessableEntity).JSON(commons.Response{
		Code:    code,
		Title:   title,
		Message: message,
	})
}

// InternalServerError sends an HTTP 500 Internal Server Error response.
//
// Use this function for unexpected server-side errors that are not the client's fault.
// Be careful not to expose sensitive internal details in the error message.
//
// Example:
//
//	func GetUser(c *fiber.Ctx) error {
//	    user, err := userService.GetUser(c.Params("id"))
//	    if err != nil {
//	        // Log the actual error for debugging
//	        logger.Error("Database error", zap.Error(err))
//
//	        // Return generic error to client
//	        return http.InternalServerError(c, "SYS_001", "Database Error",
//	            "Unable to retrieve user information")
//	    }
//	    return http.OK(c, user)
//	}
func InternalServerError(c *fiber.Ctx, code, title, message string) error {
	return c.Status(http.StatusInternalServerError).JSON(commons.Response{
		Code:    code,
		Title:   title,
		Message: message,
	})
}

// JSONResponseError sends a JSON formatted error response with a custom error struct.
//
// Use this function when you have a pre-constructed commons.Response error object.
// The HTTP status code is automatically parsed from the error's Code field.
//
// Example:
//
//	func HandleBusinessLogicError(c *fiber.Ctx, businessErr error) error {
//	    errorResponse := commons.Response{
//	        Code:    "BUSINESS_001",
//	        Title:   "Business Rule Violation",
//	        Message: businessErr.Error(),
//	    }
//	    return http.JSONResponseError(c, errorResponse)
//	}
//
// Note: The Code field should be a valid HTTP status code (e.g., "400", "404", "500").
func JSONResponseError(c *fiber.Ctx, err commons.Response) error {
	code, _ := strconv.Atoi(err.Code)

	return c.Status(code).JSON(err)
}

// JSONResponse sends a custom status code and body as a JSON response.
//
// Use this function when you need to send a non-standard HTTP status code
// or when the predefined response functions don't fit your use case.
//
// Example:
//
//	func HandleCustomStatus(c *fiber.Ctx) error {
//	    // Send HTTP 418 I'm a teapot (RFC 2324)
//	    return http.JSONResponse(c, 418, map[string]string{
//	        "message": "I'm a teapot",
//	        "rfc": "2324",
//	    })
//	}
func JSONResponse(c *fiber.Ctx, status int, s any) error {
	return c.Status(status).JSON(s)
}
