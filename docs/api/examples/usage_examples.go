package examples

import (
	"fmt"
	"time"

	"github.com/LerianStudio/lib-commons/commons"
	httpCommons "github.com/LerianStudio/lib-commons/commons/net/http"
	"github.com/gofiber/fiber/v2"
)

// ExampleUserAPI demonstrates proper usage of HTTP response functions
// in a typical REST API implementation
func ExampleUserAPI() {
	app := fiber.New()

	// Success Response Examples
	app.Get("/users/:id", func(c *fiber.Ctx) error {
		userID := c.Params("id")

		// Simulate user lookup
		user, err := getUserByID(userID)
		if err != nil {
			// Standard error response with structured error details
			return httpCommons.NotFound(c, "USR001", "User Not Found",
				fmt.Sprintf("User with ID %s does not exist", userID))
		}

		// Success response with user data
		return httpCommons.OK(c, map[string]interface{}{
			"user": user,
			"meta": map[string]interface{}{
				"request_id": c.Get("X-Request-ID"),
				"timestamp":  time.Now().UTC(),
			},
		})
	})

	// Create Resource Example
	app.Post("/users", func(c *fiber.Ctx) error {
		var userData CreateUserRequest
		if err := c.BodyParser(&userData); err != nil {
			// Bad request with custom validation error data
			return httpCommons.BadRequest(c, map[string]interface{}{
				"error":   "Invalid JSON format",
				"details": err.Error(),
			})
		}

		// Validate required fields
		if validationErrors := validateUser(userData); len(validationErrors) > 0 {
			// Unprocessable entity for validation errors
			return httpCommons.UnprocessableEntity(c, "VAL001", "Validation Failed",
				"The provided user data failed validation")
		}

		// Create user
		newUser, err := createUser(userData)
		if err != nil {
			// Check for business-specific errors
			if err.Error() == "email_exists" {
				return httpCommons.Conflict(c, "USR002", "Email Already Exists",
					"A user with this email address already exists")
			}

			// Generic server error
			return httpCommons.InternalServerError(c, "SYS001", "User Creation Failed",
				"An error occurred while creating the user")
		}

		// Resource created successfully
		return httpCommons.Created(c, map[string]interface{}{
			"user": newUser,
			"links": map[string]string{
				"self": fmt.Sprintf("/users/%s", newUser.ID),
			},
		})
	})

	// Update Resource Example
	app.Put("/users/:id", func(c *fiber.Ctx) error {
		userID := c.Params("id")

		// Check authorization
		if !isAuthorized(c, userID) {
			return httpCommons.Forbidden(c, "AUTH002", "Access Denied",
				"You don't have permission to modify this user")
		}

		var updateData UpdateUserRequest
		if err := c.BodyParser(&updateData); err != nil {
			return httpCommons.BadRequest(c, map[string]interface{}{
				"error":    "Invalid request body",
				"expected": "JSON object with user fields",
			})
		}

		// Update user
		updatedUser, err := updateUser(userID, updateData)
		if err != nil {
			return httpCommons.InternalServerError(c, "SYS002", "Update Failed",
				"Failed to update user information")
		}

		return httpCommons.OK(c, updatedUser)
	})

	// Delete Resource Example
	app.Delete("/users/:id", func(c *fiber.Ctx) error {
		userID := c.Params("id")

		if err := deleteUser(userID); err != nil {
			if err.Error() == "user_not_found" {
				return httpCommons.NotFound(c, "USR001", "User Not Found",
					fmt.Sprintf("User with ID %s not found", userID))
			}
			return httpCommons.InternalServerError(c, "SYS003", "Deletion Failed",
				"Failed to delete user")
		}

		// No content response for successful deletion
		return httpCommons.NoContent(c)
	})

	// Authentication Example
	app.Post("/auth/login", func(c *fiber.Ctx) error {
		var credentials LoginRequest
		if err := c.BodyParser(&credentials); err != nil {
			return httpCommons.BadRequest(c, map[string]string{
				"error": "Invalid credentials format",
			})
		}

		token, err := authenticate(credentials)
		if err != nil {
			// Unauthorized response for failed authentication
			return httpCommons.Unauthorized(c, "AUTH001", "Authentication Failed",
				"Invalid email or password")
		}

		return httpCommons.OK(c, map[string]interface{}{
			"token":      token,
			"expires_at": time.Now().Add(24 * time.Hour),
		})
	})

	// Async Operation Example
	app.Post("/users/:id/process", func(c *fiber.Ctx) error {
		userID := c.Params("id")

		// Start async processing
		jobID, err := startAsyncProcessing(userID)
		if err != nil {
			return httpCommons.InternalServerError(c, "PROC001", "Processing Failed",
				"Failed to start user processing")
		}

		// Accepted response for async operations
		return httpCommons.Accepted(c, map[string]interface{}{
			"job_id":    jobID,
			"status":    "processing",
			"check_url": fmt.Sprintf("/jobs/%s", jobID),
		})
	})

	// Partial Content Example (Pagination)
	app.Get("/users", func(c *fiber.Ctx) error {
		page := c.QueryInt("page", 1)
		limit := c.QueryInt("limit", 10)

		users, total, err := getUsersPaginated(page, limit)
		if err != nil {
			return httpCommons.InternalServerError(c, "SYS004", "Query Failed",
				"Failed to retrieve users")
		}

		// Use partial content for paginated responses
		if page*limit < total {
			return httpCommons.PartialContent(c, map[string]interface{}{
				"users": users,
				"pagination": map[string]interface{}{
					"page":      page,
					"limit":     limit,
					"total":     total,
					"has_more":  page*limit < total,
					"next_page": page + 1,
				},
			})
		}

		// Full content if this is the last page
		return httpCommons.OK(c, map[string]interface{}{
			"users": users,
			"pagination": map[string]interface{}{
				"page":     page,
				"limit":    limit,
				"total":    total,
				"has_more": false,
			},
		})
	})

	// Not Implemented Example
	app.Get("/users/:id/analytics", func(c *fiber.Ctx) error {
		// Feature not yet implemented
		return httpCommons.NotImplemented(c, "User analytics feature is planned for Q2 2024")
	})

	// Custom Status Code Example
	app.Get("/health/teapot", func(c *fiber.Ctx) error {
		// Custom status code using JSONResponse
		return httpCommons.JSONResponse(c, 418, map[string]interface{}{
			"message": "I'm a teapot",
			"status":  "brewing",
		})
	})

	// Business Error Mapping Example
	app.Post("/transactions", func(c *fiber.Ctx) error {
		var transaction TransactionRequest
		if err := c.BodyParser(&transaction); err != nil {
			return httpCommons.BadRequest(c, map[string]string{
				"error": "Invalid transaction format",
			})
		}

		// Process transaction
		result, err := processTransaction(transaction)
		if err != nil {
			// Use business error mapping for domain-specific errors
			mappedError := commons.ValidateBusinessError(err, "Transaction")

			// Convert to appropriate HTTP response
			if response, ok := mappedError.(commons.Response); ok {
				return httpCommons.JSONResponseError(c, response)
			}

			// Fallback to generic server error
			return httpCommons.InternalServerError(c, "TXN001", "Transaction Failed",
				"Transaction processing failed")
		}

		return httpCommons.Created(c, result)
	})

	// Start server (example - in real usage you'd handle the error)
	_ = app.Listen(":8080")
}

// Data structures for examples
type CreateUserRequest struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type UpdateUserRequest struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
}

type LoginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type User struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

type TransactionRequest struct {
	FromAccount string  `json:"from_account"`
	ToAccount   string  `json:"to_account"`
	Amount      float64 `json:"amount"`
	Currency    string  `json:"currency"`
}

// Mock functions for examples
func getUserByID(id string) (*User, error) {
	if id == "999" {
		return nil, fmt.Errorf("user not found")
	}
	return &User{
		ID:        id,
		Name:      "John Doe",
		Email:     "john@example.com",
		CreatedAt: time.Now(),
	}, nil
}

func createUser(userData CreateUserRequest) (*User, error) {
	if userData.Email == "existing@example.com" {
		return nil, fmt.Errorf("email_exists")
	}
	return &User{
		ID:        "new-user-123",
		Name:      userData.Name,
		Email:     userData.Email,
		CreatedAt: time.Now(),
	}, nil
}

func updateUser(id string, data UpdateUserRequest) (*User, error) {
	return &User{
		ID:        id,
		Name:      data.Name,
		Email:     data.Email,
		CreatedAt: time.Now().Add(-24 * time.Hour),
	}, nil
}

func deleteUser(id string) error {
	if id == "999" {
		return fmt.Errorf("user_not_found")
	}
	return nil
}

func validateUser(userData CreateUserRequest) []string {
	var errors []string
	if userData.Name == "" {
		errors = append(errors, "Name is required")
	}
	if userData.Email == "" {
		errors = append(errors, "Email is required")
	}
	return errors
}

func isAuthorized(c *fiber.Ctx, userID string) bool {
	// Mock authorization check
	authHeader := c.Get("Authorization")
	return authHeader != ""
}

func authenticate(credentials LoginRequest) (string, error) {
	if credentials.Email == "invalid@example.com" {
		return "", fmt.Errorf("invalid credentials")
	}
	return "jwt-token-123", nil
}

func startAsyncProcessing(userID string) (string, error) {
	return "job-123", nil
}

func getUsersPaginated(page, limit int) ([]*User, int, error) {
	users := []*User{
		{ID: "1", Name: "User 1", Email: "user1@example.com", CreatedAt: time.Now()},
		{ID: "2", Name: "User 2", Email: "user2@example.com", CreatedAt: time.Now()},
	}
	return users, 100, nil
}

func processTransaction(req TransactionRequest) (map[string]interface{}, error) {
	// Mock transaction processing that might return business errors
	return map[string]interface{}{
		"transaction_id": "txn-123",
		"status":         "completed",
	}, nil
}
