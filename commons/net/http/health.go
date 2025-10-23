package http

import (
	"github.com/LerianStudio/lib-commons/v3/commons/circuitbreaker"
	constant "github.com/LerianStudio/lib-commons/v3/commons/constants"
	"github.com/gofiber/fiber/v2"
)

// DependencyCheck represents a health check configuration for a single dependency.
//
// At minimum, provide a Name. For circuit breaker integration, provide both
// CircuitBreaker and ServiceName. For custom health logic, provide HealthCheck.
type DependencyCheck struct {
	// Name is the identifier for this dependency in the health response
	Name string

	// CircuitBreaker is the circuit breaker manager (optional)
	// When provided with ServiceName, health endpoint will report circuit breaker state
	CircuitBreaker circuitbreaker.Manager

	// ServiceName is the name used to register this dependency with the circuit breaker
	// Required if CircuitBreaker is provided
	ServiceName string

	// HealthCheck is a custom health check function (optional)
	// When provided, this function will be called to determine dependency health
	// Return true for healthy, false for unhealthy
	HealthCheck func() bool
}

// DependencyStatus represents the health status of a single dependency.
// This struct provides type-safe representation of dependency health metrics.
type DependencyStatus struct {
	// CircuitBreakerState indicates the current circuit breaker state (closed, open, half-open)
	// Only populated when circuit breaker is configured for this dependency
	CircuitBreakerState string `json:"circuit_breaker_state,omitempty"`

	// Healthy indicates whether the dependency is currently healthy
	Healthy bool `json:"healthy"`

	// Requests is the total number of requests processed by the circuit breaker
	// Only populated when circuit breaker is configured
	Requests uint32 `json:"requests,omitempty"`

	// TotalSuccesses is the cumulative count of successful requests
	// Only populated when circuit breaker is configured
	TotalSuccesses uint32 `json:"total_successes,omitempty"`

	// TotalFailures is the cumulative count of failed requests
	// Only populated when circuit breaker is configured
	TotalFailures uint32 `json:"total_failures,omitempty"`

	// ConsecutiveFailures is the count of consecutive failures
	// Resets to 0 on success. Only populated when circuit breaker is configured
	ConsecutiveFailures uint32 `json:"consecutive_failures,omitempty"`
}

// HealthWithDependencies creates a Fiber handler that reports health status
// based on circuit breaker states and custom health checks.
//
// Returns HTTP 200 (status: "available") when all dependencies are healthy,
// or HTTP 503 (status: "degraded") when any dependency fails.
//
// Example:
//
//	f.Get("/health", commonsHttp.HealthWithDependencies(
//	    commonsHttp.DependencyCheck{
//	        Name:           "database",
//	        CircuitBreaker: cbManager,
//	        ServiceName:    "postgres",
//	        HealthCheck:    func() bool { return db.Ping() == nil },
//	    },
//	    commonsHttp.DependencyCheck{
//	        Name:           "cache",
//	        CircuitBreaker: cbManager,
//	        ServiceName:    "redis",
//	    },
//	))
func HealthWithDependencies(dependencies ...DependencyCheck) fiber.Handler {
	return func(c *fiber.Ctx) error {
		overallStatus := constant.DataSourceStatusAvailable
		httpStatus := fiber.StatusOK

		depStatuses := make(map[string]*DependencyStatus)

		for _, dep := range dependencies {
			status := &DependencyStatus{
				Healthy: true, // Default to healthy unless proven otherwise
			}

			// Check circuit breaker state if provided
			if dep.CircuitBreaker != nil && dep.ServiceName != "" {
				cbState := dep.CircuitBreaker.GetState(dep.ServiceName)
				cbCounts := dep.CircuitBreaker.GetCounts(dep.ServiceName)

				status.CircuitBreakerState = string(cbState)
				status.Requests = cbCounts.Requests
				status.TotalSuccesses = cbCounts.TotalSuccesses
				status.TotalFailures = cbCounts.TotalFailures
				status.ConsecutiveFailures = cbCounts.ConsecutiveFailures

				// Use circuit breaker's IsHealthy if available
				status.Healthy = dep.CircuitBreaker.IsHealthy(dep.ServiceName)
			}

			// Run custom health check if provided
			// This overrides the circuit breaker health status if both are provided
			if dep.HealthCheck != nil {
				healthy := dep.HealthCheck()
				status.Healthy = healthy
			}

			// Update overall status based on final dependency health
			// Only after all checks have been evaluated
			if !status.Healthy {
				overallStatus = constant.DataSourceStatusDegraded
				httpStatus = fiber.StatusServiceUnavailable
			}

			// Store status for this dependency
			depStatuses[dep.Name] = status
		}

		return c.Status(httpStatus).JSON(fiber.Map{
			"status":       overallStatus,
			"dependencies": depStatuses,
		})
	}
}

// HealthSimple is an alias for the existing Ping function for backward compatibility.
// Use this when you don't need detailed dependency health checks.
//
// Returns:
//   - HTTP 200 OK with "healthy" text response
//
// Example usage:
//
//	f.Get("/health", commonsHttp.HealthSimple)
var HealthSimple = Ping
