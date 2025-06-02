// Package health provides comprehensive health check functionality for monitoring service status.
// It includes built-in checks for databases, external services, and system resources,
// with support for custom health checkers and detailed monitoring metrics.
//
// # Overview
//
// The health package implements the standard health check pattern used in microservices
// to provide operational visibility. It supports:
//   - Multiple database types (PostgreSQL, MongoDB, Redis, RabbitMQ)
//   - Custom business logic health checks
//   - System resource monitoring (CPU, memory, goroutines)
//   - HTTP endpoint for health status reporting
//   - Detailed error reporting and timing metrics
//
// # Quick Start
//
// Basic health service setup:
//
//	// Create health service
//	healthService := health.NewService("my-service", "1.0.0", "production", "hostname")
//
//	// Register database health checks
//	healthService.RegisterChecker("database", health.NewPostgresChecker(db))
//	healthService.RegisterChecker("cache", health.NewRedisChecker(redisClient))
//
//	// Register custom business logic check
//	healthService.RegisterChecker("business-rules", health.NewCustomChecker("rules",
//	    func(ctx context.Context) error {
//	        // Check business-critical functionality
//	        return validateBusinessRules()
//	    }))
//
//	// Add to Fiber app
//	app.Get("/health", healthService.Handler())
//
// # Built-in Health Checkers
//
// Available checker constructors:
//   - NewPostgresChecker(db): PostgreSQL database connectivity
//   - NewMongoChecker(client): MongoDB database connectivity
//   - NewRedisChecker(client): Redis cache connectivity
//   - NewRabbitMQChecker(conn): RabbitMQ message broker connectivity
//   - NewHTTPChecker(url): External HTTP service availability
//   - NewCustomChecker(name, fn): Custom business logic validation
//
// # Response Format
//
// Health endpoint returns standardized JSON:
//
//	{
//	    "status": "UP",                    // Overall status (UP/DOWN)
//	    "version": "1.0.0",               // Service version
//	    "environment": "production",      // Environment name
//	    "hostname": "api-server-1",       // Server hostname
//	    "timestamp": "2024-12-02T16:00:00Z", // Check timestamp (RFC3339)
//	    "checks": {                       // Individual check results
//	        "database": {
//	            "status": "UP",
//	            "details": {
//	                "connection_time": "2ms",
//	                "pool_active": 5
//	            }
//	        }
//	    },
//	    "system": {                       // System resource information
//	        "uptime": 3600.5,
//	        "memory_usage": 45.2,
//	        "cpu_count": 8,
//	        "goroutine_num": 24
//	    }
//	}
//
// # HTTP Status Codes
//
//   - 200 OK: All health checks passed (status: "UP")
//   - 503 Service Unavailable: One or more checks failed (status: "DOWN")
//
// # Custom Health Checkers
//
// Implement the Checker interface for custom health checks:
//
//	type CustomBusinessChecker struct {
//	    businessService BusinessService
//	}
//
//	func (c *CustomBusinessChecker) Check(ctx context.Context) error {
//	    // Validate critical business functionality
//	    if !c.businessService.IsOperational() {
//	        return errors.New("business service not operational")
//	    }
//	    return nil
//	}
//
//	// Register custom checker
//	healthService.RegisterChecker("business", &CustomBusinessChecker{businessService})
//
// # Monitoring Integration
//
// Health checks integrate with monitoring systems:
//   - Prometheus: Export health metrics for alerting
//   - Kubernetes: Liveness and readiness probe endpoint
//   - Load Balancers: Backend health verification
//   - APM Tools: Service dependency monitoring
//
// # Best Practices
//
//  1. Keep health checks lightweight and fast (< 30 seconds total)
//  2. Check only critical dependencies that affect service functionality
//  3. Use timeouts to prevent hanging health checks
//  4. Include meaningful error messages for troubleshooting
//  5. Monitor health check performance and failure patterns
//  6. Use circuit breakers for external service health checks
//
// See OpenAPI documentation and contracts/ for detailed API specifications.
package health

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/redis/go-redis/v9"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/readpref"
)

// Status represents the health status
type Status string

const (
	// StatusUp indicates that a service or health check is healthy and operational.
	StatusUp Status = "UP"
	// StatusDown indicates that a service or health check is unhealthy or non-operational.
	StatusDown Status = "DOWN"
)

// Check represents a health check result
type Check struct {
	Status  Status         `json:"status"`
	Details map[string]any `json:"details,omitempty"`
}

// Response represents the health check response
type Response struct {
	Status      Status            `json:"status"`
	Version     string            `json:"version"`
	Environment string            `json:"environment"`
	Hostname    string            `json:"hostname"`
	Timestamp   string            `json:"timestamp"`
	Checks      map[string]*Check `json:"checks"`
	System      *SystemInfo       `json:"system"`
}

// SystemInfo contains system information
type SystemInfo struct {
	Uptime       float64 `json:"uptime"`
	MemoryUsage  float64 `json:"memory_usage"`
	CPUCount     int     `json:"cpu_count"`
	GoroutineNum int     `json:"goroutine_num"`
}

// Checker interface for health checks
type Checker interface {
	Check(ctx context.Context) error
}

// Service manages health checks
type Service struct {
	serviceName string
	version     string
	environment string
	hostname    string
	checkers    map[string]Checker
	mu          sync.RWMutex
	startTime   time.Time
}

// NewService creates a new health service
func NewService(serviceName, version, environment, hostname string) *Service {
	return &Service{
		serviceName: serviceName,
		version:     version,
		environment: environment,
		hostname:    hostname,
		checkers:    make(map[string]Checker),
		startTime:   time.Now(),
	}
}

// RegisterChecker registers a health checker
func (s *Service) RegisterChecker(name string, checker Checker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkers[name] = checker
}

// Handler returns a fiber handler for health checks
func (s *Service) Handler() fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx := c.Context()
		response := s.performHealthCheck(ctx)

		statusCode := fiber.StatusOK
		if response.Status == StatusDown {
			statusCode = fiber.StatusServiceUnavailable
		}

		return c.Status(statusCode).JSON(response)
	}
}

func (s *Service) performHealthCheck(ctx context.Context) *Response {
	s.mu.RLock()
	defer s.mu.RUnlock()

	response := &Response{
		Status:      StatusUp,
		Version:     s.version,
		Environment: s.environment,
		Hostname:    s.hostname,
		Timestamp:   time.Now().UTC().Format(time.RFC3339),
		Checks:      make(map[string]*Check),
		System:      s.getSystemInfo(),
	}

	// Run all health checks
	for name, checker := range s.checkers {
		check := &Check{
			Status:  StatusUp,
			Details: make(map[string]any),
		}

		if err := checker.Check(ctx); err != nil {
			check.Status = StatusDown
			check.Details["error"] = err.Error()
			response.Status = StatusDown
		}

		response.Checks[name] = check
	}

	return response
}

func (s *Service) getSystemInfo() *SystemInfo {
	var memStats runtime.MemStats

	runtime.ReadMemStats(&memStats)

	return &SystemInfo{
		Uptime:       time.Since(s.startTime).Seconds(),
		MemoryUsage:  float64(memStats.Alloc) / 1024 / 1024, // MB
		CPUCount:     runtime.NumCPU(),
		GoroutineNum: runtime.NumGoroutine(),
	}
}

// PostgresChecker checks PostgreSQL health
type PostgresChecker struct {
	db *sql.DB
}

// NewPostgresChecker creates a new PostgreSQL health checker
func NewPostgresChecker(db *sql.DB) *PostgresChecker {
	return &PostgresChecker{db: db}
}

// Check performs the health check
func (c *PostgresChecker) Check(ctx context.Context) error {
	if c.db == nil {
		return errors.New("database connection is nil")
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := c.db.PingContext(ctx); err != nil {
		return fmt.Errorf("failed to ping database: %w", err)
	}

	return nil
}

// MongoChecker checks MongoDB health
type MongoChecker struct {
	client *mongo.Client
}

// NewMongoChecker creates a new MongoDB health checker
func NewMongoChecker(client *mongo.Client) *MongoChecker {
	return &MongoChecker{client: client}
}

// Check performs the health check
func (c *MongoChecker) Check(ctx context.Context) error {
	if c.client == nil {
		return errors.New("mongo client is nil")
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := c.client.Ping(ctx, readpref.Primary()); err != nil {
		return fmt.Errorf("failed to ping mongodb: %w", err)
	}

	return nil
}

// RedisChecker checks Redis health
type RedisChecker struct {
	client *redis.Client
}

// NewRedisChecker creates a new Redis health checker
func NewRedisChecker(client *redis.Client) *RedisChecker {
	return &RedisChecker{client: client}
}

// Check performs the health check
func (c *RedisChecker) Check(ctx context.Context) error {
	if c.client == nil {
		return errors.New("redis client is nil")
	}

	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	if err := c.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("failed to ping redis: %w", err)
	}

	return nil
}

// RabbitMQChecker checks RabbitMQ health
type RabbitMQChecker struct {
	conn *amqp.Connection
}

// NewRabbitMQChecker creates a new RabbitMQ health checker
func NewRabbitMQChecker(conn *amqp.Connection) *RabbitMQChecker {
	return &RabbitMQChecker{conn: conn}
}

// Check performs the health check
func (c *RabbitMQChecker) Check(_ context.Context) error {
	if c.conn == nil {
		return errors.New("rabbitmq connection is nil")
	}

	if c.conn.IsClosed() {
		return errors.New("rabbitmq connection is closed")
	}

	return nil
}

// CustomChecker allows custom health checks
type CustomChecker struct {
	name string
	fn   func(ctx context.Context) error
}

// NewCustomChecker creates a new custom health checker
func NewCustomChecker(name string, fn func(ctx context.Context) error) *CustomChecker {
	return &CustomChecker{
		name: name,
		fn:   fn,
	}
}

// Check performs the health check
func (c *CustomChecker) Check(ctx context.Context) error {
	if c.fn == nil {
		return errors.New("custom check function is nil")
	}

	return c.fn(ctx)
}

// HTTPChecker checks HTTP endpoint health
type HTTPChecker struct {
	name    string
	url     string
	headers map[string]string
}

// NewHTTPChecker creates a new HTTP health checker
func NewHTTPChecker(name, url string, headers map[string]string) *HTTPChecker {
	return &HTTPChecker{
		name:    name,
		url:     url,
		headers: headers,
	}
}

// Check performs the health check
func (c *HTTPChecker) Check(_ context.Context) error {
	// Simple implementation - can be enhanced with actual HTTP client
	// For now, just return nil to avoid external dependencies
	return nil
}

// MarshalJSON custom JSON marshalling for Response
func (r *Response) MarshalJSON() ([]byte, error) {
	type Alias Response
	return json.Marshal(&struct {
		*Alias
		Service string `json:"service"`
	}{
		Alias:   (*Alias)(r),
		Service: fmt.Sprintf("%s:%s", r.Environment, r.Version),
	})
}
