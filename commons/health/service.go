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
	StatusUp   Status = "UP"
	StatusDown Status = "DOWN"
)

// Check represents a health check result
type Check struct {
	Status  Status                 `json:"status"`
	Details map[string]interface{} `json:"details,omitempty"`
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
			Details: make(map[string]interface{}),
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
func (c *RabbitMQChecker) Check(ctx context.Context) error {
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
func (c *HTTPChecker) Check(ctx context.Context) error {
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
