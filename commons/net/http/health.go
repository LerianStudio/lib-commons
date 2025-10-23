package http

import (
	"github.com/LerianStudio/lib-commons/v2/commons/circuitbreaker"
	"github.com/LerianStudio/lib-commons/v2/commons/constants"
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
// The handler returns:
//   - HTTP 200 OK with status "available" when all dependencies are healthy
//   - HTTP 503 Service Unavailable with status "degraded" when any circuit breaker is open
//     or any custom health check returns false
//
// Response format:
//
//	{
//	  "status": "available" | "degraded",
//	  "dependencies": {
//	    "dependency-name": {
//	      "circuit_breaker_state": "closed" | "open" | "half-open",
//	      "healthy": true | false,
//	      "requests": 1234,
//	      "total_successes": 1200,
//	      "total_failures": 34,
//	      "consecutive_failures": 0
//	    }
//	  }
//	}
//
// Example usage with circuit breaker:
//
//	f.Get("/health", commonsHttp.HealthWithDependencies(
//	    commonsHttp.DependencyCheck{
//	        Name:           "casdoor",
//	        CircuitBreaker: cbManager,
//	        ServiceName:    "casdoor",
//	        HealthCheck:    func() bool { return casdoorClient.IsHealthy() },
//	    },
//	))
//
// Example usage with multiple dependencies:
//
//	f.Get("/health", commonsHttp.HealthWithDependencies(
//	    commonsHttp.DependencyCheck{
//	        Name:           "database",
//	        CircuitBreaker: cbManager,
//	        ServiceName:    "postgres",
//	    },
//	    commonsHttp.DependencyCheck{
//	        Name:           "cache",
//	        CircuitBreaker: cbManager,
//	        ServiceName:    "redis",
//	    },
//	))
//
// Example usage with custom health check only:
//
//	f.Get("/health", commonsHttp.HealthWithDependencies(
//	    commonsHttp.DependencyCheck{
//	        Name:        "database",
//	        HealthCheck: func() bool { return db.Ping() == nil },
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

				// If circuit breaker is open, service is degraded
				if cbState == circuitbreaker.StateOpen {
					overallStatus = constant.DataSourceStatusDegraded
					httpStatus = fiber.StatusServiceUnavailable
				}

				// Use circuit breaker's IsHealthy if available
				status.Healthy = dep.CircuitBreaker.IsHealthy(dep.ServiceName)
			}

			// Run custom health check if provided
			// This overrides the circuit breaker health status if both are provided
			if dep.HealthCheck != nil {
				healthy := dep.HealthCheck()
				status.Healthy = healthy
				if !healthy {
					overallStatus = constant.DataSourceStatusDegraded
					httpStatus = fiber.StatusServiceUnavailable
				}
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
