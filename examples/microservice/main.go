// Package main demonstrates a complete microservice implementation using lib-commons components.
// This example shows how to build a production-ready service with observability, health checks,
// database connections, and error handling.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/LerianStudio/lib-commons/commons/health"
	"github.com/LerianStudio/lib-commons/commons/mongo"
	httpCommons "github.com/LerianStudio/lib-commons/commons/net/http"
	"github.com/LerianStudio/lib-commons/commons/observability"
	"github.com/LerianStudio/lib-commons/commons/postgres"
	"github.com/LerianStudio/lib-commons/commons/redis"
	"github.com/LerianStudio/lib-commons/commons/validation"
	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/recover"
)

// ServiceConfig holds configuration for the microservice
type ServiceConfig struct {
	Port           string
	ServiceName    string
	ServiceVersion string
	Environment    string
	PostgresURL    string
	MongoURL       string
	RedisURL       string
	OTLPEndpoint   string
}

// UserService represents the main service structure
type UserService struct {
	config        *ServiceConfig
	app           *fiber.App
	postgresConn  *postgres.PostgresConnection
	mongoConn     *mongo.MongoConnection
	redisConn     *redis.RedisConnection
	healthService *health.Service
	obsProvider   observability.Provider
	validator     *validation.Validator
}

// User represents a user entity
type User struct {
	ID      string    `json:"id"      validate:"uuid"`
	Name    string    `json:"name"    validate:"required,min=2,max=100"`
	Email   string    `json:"email"   validate:"required,email"`
	Age     int       `json:"age"     validate:"min=13,max=120"`
	Created time.Time `json:"created"`
}

// CreateUserRequest represents the request body for user creation
type CreateUserRequest struct {
	Name  string `json:"name"  validate:"required,min=2,max=100"`
	Email string `json:"email" validate:"required,email"`
	Age   int    `json:"age"   validate:"min=13,max=120"`
}

// UpdateUserRequest represents the request body for user updates
type UpdateUserRequest struct {
	Name string `json:"name" validate:"omitempty,min=2,max=100"`
	Age  int    `json:"age"  validate:"omitempty,min=13,max=120"`
}

func main() {
	// Load configuration from environment variables
	config := &ServiceConfig{
		Port:           getEnvOrDefault("PORT", "8080"),
		ServiceName:    getEnvOrDefault("SERVICE_NAME", "user-service"),
		ServiceVersion: getEnvOrDefault("SERVICE_VERSION", "1.0.0"),
		Environment:    getEnvOrDefault("ENVIRONMENT", "development"),
		PostgresURL: getEnvOrDefault(
			"POSTGRES_URL",
			"postgres://postgres:password@localhost:5432/userdb",
		),
		MongoURL:     getEnvOrDefault("MONGO_URL", "mongodb://localhost:27017"),
		RedisURL:     getEnvOrDefault("REDIS_URL", "localhost:6379"),
		OTLPEndpoint: getEnvOrDefault("OTLP_ENDPOINT", ""),
	}

	// Create and start the service
	service, err := NewUserService(config)
	if err != nil {
		log.Fatalf("Failed to create user service: %v", err)
	}

	if err := service.Start(); err != nil {
		log.Fatalf("Failed to start user service: %v", err)
	}

	// Wait for interrupt signal to gracefully shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down user service...")
	if err := service.Stop(); err != nil {
		log.Printf("Error during shutdown: %v", err)
	}
}

// NewUserService creates a new UserService instance
func NewUserService(config *ServiceConfig) (*UserService, error) {
	service := &UserService{
		config: config,
	}

	// Initialize observability provider
	if err := service.initializeObservability(); err != nil {
		return nil, fmt.Errorf("failed to initialize observability: %w", err)
	}

	// Initialize database connections
	if err := service.initializeDatabases(); err != nil {
		return nil, fmt.Errorf("failed to initialize databases: %w", err)
	}

	// Initialize health service
	service.initializeHealthService()

	// Initialize validator
	service.validator = validation.NewValidator()

	// Initialize Fiber app with middleware
	service.initializeFiberApp()

	// Setup routes
	service.setupRoutes()

	return service, nil
}

// Start starts the user service
func (s *UserService) Start() error {
	ctx := context.Background()

	// Start observability provider
	if err := s.obsProvider.Start(ctx); err != nil {
		return fmt.Errorf("failed to start observability provider: %w", err)
	}

	// Connect to databases
	if err := s.connectDatabases(ctx); err != nil {
		return fmt.Errorf("failed to connect to databases: %w", err)
	}

	// Start HTTP server
	log.Printf("Starting %s v%s on port %s (environment: %s)",
		s.config.ServiceName, s.config.ServiceVersion, s.config.Port, s.config.Environment)

	go func() {
		if err := s.app.Listen(":" + s.config.Port); err != nil {
			log.Printf("Server error: %v", err)
		}
	}()

	return nil
}

// Stop gracefully stops the user service
func (s *UserService) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Shutdown HTTP server
	if err := s.app.Shutdown(); err != nil {
		log.Printf("HTTP server shutdown error: %v", err)
	}

	// Stop observability provider
	if err := s.obsProvider.Stop(ctx); err != nil {
		log.Printf("Observability provider stop error: %v", err)
	}

	log.Println("User service stopped gracefully")
	return nil
}

// initializeObservability sets up the observability provider
func (s *UserService) initializeObservability() error {
	config := observability.ProviderConfig{
		ServiceName:    s.config.ServiceName,
		ServiceVersion: s.config.ServiceVersion,
		Environment:    s.config.Environment,
		OTLPEndpoint:   s.config.OTLPEndpoint,
		MetricsEnabled: true,
		TracingEnabled: true,
	}

	var err error
	s.obsProvider, err = observability.NewProvider(config)
	if err != nil {
		return fmt.Errorf("failed to create observability provider: %w", err)
	}

	return nil
}

// initializeDatabases sets up database connections
func (s *UserService) initializeDatabases() error {
	// Initialize PostgreSQL connection
	s.postgresConn = &postgres.PostgresConnection{
		ConnectionStringPrimary: s.config.PostgresURL,
		PrimaryDBName:           "userdb",
		MaxOpenConnections:      25,
		MaxIdleConnections:      10,
		MigrationsPath:          "./migrations",
	}

	// Initialize MongoDB connection
	s.mongoConn = &mongo.MongoConnection{
		ConnectionStringSource: s.config.MongoURL,
		Database:               "userdb",
		MaxPoolSize:            20,
	}

	// Initialize Redis connection
	s.redisConn = &redis.RedisConnection{
		Addr: s.config.RedisURL,
	}

	return nil
}

// connectDatabases connects to all databases
func (s *UserService) connectDatabases(ctx context.Context) error {
	// Connect to PostgreSQL
	if err := s.postgresConn.Connect(ctx); err != nil {
		return fmt.Errorf("failed to connect to PostgreSQL: %w", err)
	}

	// Connect to MongoDB
	if err := s.mongoConn.Connect(ctx); err != nil {
		return fmt.Errorf("failed to connect to MongoDB: %w", err)
	}

	// Connect to Redis
	if err := s.redisConn.Connect(ctx); err != nil {
		return fmt.Errorf("failed to connect to Redis: %w", err)
	}

	log.Println("Successfully connected to all databases")
	return nil
}

// initializeHealthService sets up health checks
func (s *UserService) initializeHealthService() {
	s.healthService = health.NewService(
		s.config.ServiceName,
		s.config.ServiceVersion,
		s.config.Environment,
		getHostname(),
	)

	// Register database health checkers
	s.healthService.RegisterChecker(
		"postgres",
		health.NewPostgresChecker(nil),
	) // Pass actual DB in production
	s.healthService.RegisterChecker("mongodb", health.NewMongoChecker(s.mongoConn.DB))
	s.healthService.RegisterChecker("redis", health.NewRedisChecker(s.redisConn.Client))

	// Register custom business logic checker
	s.healthService.RegisterChecker("user-service-logic", health.NewCustomChecker("business-rules",
		func(ctx context.Context) error {
			// Add your business logic health checks here
			// For example: check if critical business rules are functional
			return s.validateBusinessRules(ctx)
		}))
}

// initializeFiberApp sets up the Fiber application with middleware
func (s *UserService) initializeFiberApp() {
	s.app = fiber.New(fiber.Config{
		AppName:      s.config.ServiceName,
		ServerHeader: fmt.Sprintf("%s/%s", s.config.ServiceName, s.config.ServiceVersion),
		ErrorHandler: s.customErrorHandler,
	})

	// Add recovery middleware (must be first)
	s.app.Use(recover.New())

	// Add CORS middleware
	s.app.Use(cors.New(cors.Config{
		AllowOrigins: "*",
		AllowMethods: "GET,POST,PUT,DELETE,OPTIONS",
		AllowHeaders: "Origin,Content-Type,Accept,Authorization,X-Request-ID",
	}))

	// Add observability middleware
	s.app.Use(httpCommons.WithHTTPLogging())
	s.app.Use(httpCommons.WithTelemetry(s.obsProvider))

	// Add request ID middleware for correlation
	s.app.Use(func(c *fiber.Ctx) error {
		requestID := c.Get("X-Request-ID")
		if requestID == "" {
			requestID = generateRequestID()
			c.Set("X-Request-ID", requestID)
		}

		// Add request ID to context for logging/tracing
		ctx := observability.ContextWithHeaderID(c.Context(), requestID)
		c.SetUserContext(ctx)

		return c.Next()
	})
}

// setupRoutes configures all API routes
func (s *UserService) setupRoutes() {
	// Health check endpoint
	s.app.Get("/health", s.healthService.Handler())

	// API version group
	api := s.app.Group("/api/v1")

	// User endpoints
	users := api.Group("/users")
	users.Get("/", s.listUsers)
	users.Get("/:id", s.getUser)
	users.Post("/", s.createUser)
	users.Put("/:id", s.updateUser)
	users.Delete("/:id", s.deleteUser)

	// Search endpoint
	users.Get("/search", s.searchUsers)
}

// HTTP Handlers

// listUsers handles GET /api/v1/users
func (s *UserService) listUsers(c *fiber.Ctx) error {
	ctx := c.Context()

	// Get pagination parameters
	limit := c.QueryInt("limit", 20)
	if limit > 100 {
		limit = 100 // Maximum limit
	}

	cursor := c.Query("cursor", "")

	// Simulate database query (replace with actual implementation)
	users, nextCursor, err := s.getUsersFromDatabase(ctx, limit, cursor)
	if err != nil {
		return httpCommons.InternalServerError(c, "USER_DB_001", "Database Error",
			"Failed to retrieve users")
	}

	response := map[string]interface{}{
		"users":       users,
		"next_cursor": nextCursor,
		"limit":       limit,
	}

	return httpCommons.OK(c, response)
}

// getUser handles GET /api/v1/users/:id
func (s *UserService) getUser(c *fiber.Ctx) error {
	ctx := c.Context()
	userID := c.Params("id")

	// Validate UUID format
	if err := s.validator.Validate(struct {
		ID string `validate:"uuid"`
	}{ID: userID}); err != nil {
		return httpCommons.BadRequest(c, map[string]string{
			"error":   "Invalid user ID format",
			"details": err.Error(),
		})
	}

	// Try to get from cache first
	user, err := s.getUserFromCache(ctx, userID)
	if err == nil && user != nil {
		return httpCommons.OK(c, user)
	}

	// Get from database
	user, err = s.getUserFromDatabase(ctx, userID)
	if err != nil {
		if err == ErrUserNotFound {
			return httpCommons.NotFound(c, "USER_001", "User Not Found",
				fmt.Sprintf("User with ID %s does not exist", userID))
		}
		return httpCommons.InternalServerError(c, "USER_DB_002", "Database Error",
			"Failed to retrieve user")
	}

	// Cache the user for future requests
	go s.cacheUser(context.Background(), user)

	return httpCommons.OK(c, user)
}

// createUser handles POST /api/v1/users
func (s *UserService) createUser(c *fiber.Ctx) error {
	ctx := c.Context()
	var req CreateUserRequest

	// Parse request body
	if err := c.BodyParser(&req); err != nil {
		return httpCommons.BadRequest(c, map[string]string{
			"error":   "Invalid request body",
			"details": err.Error(),
		})
	}

	// Validate request
	if err := s.validator.Validate(req); err != nil {
		return httpCommons.BadRequest(c, map[string]string{
			"error":   "Validation failed",
			"details": err.Error(),
		})
	}

	// Check if user already exists
	existing, _ := s.getUserByEmail(ctx, req.Email)
	if existing != nil {
		return httpCommons.Conflict(c, "USER_002", "User Already Exists",
			fmt.Sprintf("User with email %s already exists", req.Email))
	}

	// Create user
	user := &User{
		ID:      generateUserID(),
		Name:    req.Name,
		Email:   req.Email,
		Age:     req.Age,
		Created: time.Now(),
	}

	// Save to database
	if err := s.saveUserToDatabase(ctx, user); err != nil {
		return httpCommons.InternalServerError(c, "USER_DB_003", "Database Error",
			"Failed to create user")
	}

	// Cache the new user
	go s.cacheUser(context.Background(), user)

	return httpCommons.Created(c, user)
}

// updateUser handles PUT /api/v1/users/:id
func (s *UserService) updateUser(c *fiber.Ctx) error {
	ctx := c.Context()
	userID := c.Params("id")
	var req UpdateUserRequest

	// Validate UUID format
	if err := s.validator.Validate(struct {
		ID string `validate:"uuid"`
	}{ID: userID}); err != nil {
		return httpCommons.BadRequest(c, map[string]string{
			"error": "Invalid user ID format",
		})
	}

	// Parse request body
	if err := c.BodyParser(&req); err != nil {
		return httpCommons.BadRequest(c, map[string]string{
			"error":   "Invalid request body",
			"details": err.Error(),
		})
	}

	// Validate request
	if err := s.validator.Validate(req); err != nil {
		return httpCommons.BadRequest(c, map[string]string{
			"error":   "Validation failed",
			"details": err.Error(),
		})
	}

	// Get existing user
	user, err := s.getUserFromDatabase(ctx, userID)
	if err != nil {
		if err == ErrUserNotFound {
			return httpCommons.NotFound(c, "USER_001", "User Not Found",
				fmt.Sprintf("User with ID %s does not exist", userID))
		}
		return httpCommons.InternalServerError(c, "USER_DB_002", "Database Error",
			"Failed to retrieve user")
	}

	// Update user fields
	if req.Name != "" {
		user.Name = req.Name
	}
	if req.Age > 0 {
		user.Age = req.Age
	}

	// Save to database
	if err := s.updateUserInDatabase(ctx, user); err != nil {
		return httpCommons.InternalServerError(c, "USER_DB_004", "Database Error",
			"Failed to update user")
	}

	// Update cache
	go s.cacheUser(context.Background(), user)

	return httpCommons.OK(c, user)
}

// deleteUser handles DELETE /api/v1/users/:id
func (s *UserService) deleteUser(c *fiber.Ctx) error {
	ctx := c.Context()
	userID := c.Params("id")

	// Validate UUID format
	if err := s.validator.Validate(struct {
		ID string `validate:"uuid"`
	}{ID: userID}); err != nil {
		return httpCommons.BadRequest(c, map[string]string{
			"error": "Invalid user ID format",
		})
	}

	// Check if user exists
	_, err := s.getUserFromDatabase(ctx, userID)
	if err != nil {
		if err == ErrUserNotFound {
			return httpCommons.NotFound(c, "USER_001", "User Not Found",
				fmt.Sprintf("User with ID %s does not exist", userID))
		}
		return httpCommons.InternalServerError(c, "USER_DB_002", "Database Error",
			"Failed to retrieve user")
	}

	// Delete from database
	if err := s.deleteUserFromDatabase(ctx, userID); err != nil {
		return httpCommons.InternalServerError(c, "USER_DB_005", "Database Error",
			"Failed to delete user")
	}

	// Remove from cache
	go s.removeUserFromCache(context.Background(), userID)

	return httpCommons.NoContent(c)
}

// searchUsers handles GET /api/v1/users/search
func (s *UserService) searchUsers(c *fiber.Ctx) error {
	ctx := c.Context()
	query := c.Query("q", "")

	if query == "" {
		return httpCommons.BadRequest(c, map[string]string{
			"error": "Search query is required",
		})
	}

	// Perform search (implement actual search logic)
	users, err := s.searchUsersInDatabase(ctx, query)
	if err != nil {
		return httpCommons.InternalServerError(c, "USER_SEARCH_001", "Search Error",
			"Failed to search users")
	}

	return httpCommons.OK(c, map[string]interface{}{
		"query":   query,
		"results": users,
		"count":   len(users),
	})
}

// Database operations (implement with actual database logic)

var ErrUserNotFound = fmt.Errorf("user not found")

func (s *UserService) getUsersFromDatabase(
	ctx context.Context,
	limit int,
	cursor string,
) ([]*User, string, error) {
	// Implement actual database query with cursor pagination
	// This is a placeholder implementation
	return []*User{}, "", nil
}

func (s *UserService) getUserFromDatabase(ctx context.Context, userID string) (*User, error) {
	// Implement actual database query
	// This is a placeholder implementation
	return nil, ErrUserNotFound
}

func (s *UserService) getUserByEmail(ctx context.Context, email string) (*User, error) {
	// Implement actual database query
	// This is a placeholder implementation
	return nil, ErrUserNotFound
}

func (s *UserService) saveUserToDatabase(ctx context.Context, user *User) error {
	// Implement actual database save
	// This is a placeholder implementation
	return nil
}

func (s *UserService) updateUserInDatabase(ctx context.Context, user *User) error {
	// Implement actual database update
	// This is a placeholder implementation
	return nil
}

func (s *UserService) deleteUserFromDatabase(ctx context.Context, userID string) error {
	// Implement actual database delete
	// This is a placeholder implementation
	return nil
}

func (s *UserService) searchUsersInDatabase(ctx context.Context, query string) ([]*User, error) {
	// Implement actual search logic
	// This is a placeholder implementation
	return []*User{}, nil
}

// Cache operations (implement with Redis)

func (s *UserService) getUserFromCache(ctx context.Context, userID string) (*User, error) {
	// Implement actual cache retrieval
	// This is a placeholder implementation
	return nil, fmt.Errorf("not found in cache")
}

func (s *UserService) cacheUser(ctx context.Context, user *User) {
	// Implement actual cache storage
	// This is a placeholder implementation
}

func (s *UserService) removeUserFromCache(ctx context.Context, userID string) {
	// Implement actual cache removal
	// This is a placeholder implementation
}

// Business logic validation

func (s *UserService) validateBusinessRules(ctx context.Context) error {
	// Implement your business logic health checks
	// For example: verify critical business rules are functional
	return nil
}

// Custom error handler

func (s *UserService) customErrorHandler(c *fiber.Ctx, err error) error {
	// Default to 500 server error
	code := fiber.StatusInternalServerError
	message := "Internal Server Error"

	// Check if it's a Fiber error
	if e, ok := err.(*fiber.Error); ok {
		code = e.Code
		message = e.Message
	}

	return httpCommons.JSONResponse(c, code, map[string]interface{}{
		"code":    fmt.Sprintf("%d", code),
		"title":   message,
		"message": err.Error(),
	})
}

// Utility functions

func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

func getHostname() string {
	hostname, err := os.Hostname()
	if err != nil {
		return "unknown"
	}
	return hostname
}

func generateRequestID() string {
	// In production, use a proper UUID library
	return fmt.Sprintf("req-%d", time.Now().UnixNano())
}

func generateUserID() string {
	// In production, use a proper UUID library
	return fmt.Sprintf("user-%d", time.Now().UnixNano())
}
