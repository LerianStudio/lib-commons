package circuitbreaker

import "time"

// DefaultConfig provides balanced settings for most services
func DefaultConfig() Config {
	return Config{
		MaxRequests:         3,
		Interval:            2 * time.Minute,
		Timeout:             30 * time.Second,
		ConsecutiveFailures: 15,
		FailureRatio:        0.5,
		MinRequests:         10,
	}
}

// AggressiveConfig for services requiring fast failure detection
func AggressiveConfig() Config {
	return Config{
		MaxRequests:         2,
		Interval:            1 * time.Minute,
		Timeout:             10 * time.Second,
		ConsecutiveFailures: 5,
		FailureRatio:        0.4,
		MinRequests:         5,
	}
}

// ConservativeConfig for services that should tolerate more failures
func ConservativeConfig() Config {
	return Config{
		MaxRequests:         5,
		Interval:            5 * time.Minute,
		Timeout:             60 * time.Second,
		ConsecutiveFailures: 25,
		FailureRatio:        0.6,
		MinRequests:         20,
	}
}

// HTTPServiceConfig optimized for external HTTP APIs
// Faster failure detection with shorter timeout suitable for HTTP calls
func HTTPServiceConfig() Config {
	return Config{
		MaxRequests:         3,
		Interval:            2 * time.Minute,
		Timeout:             10 * time.Second, // Shorter for HTTP
		ConsecutiveFailures: 5,                // Faster failure detection
		FailureRatio:        0.5,
		MinRequests:         10,
	}
}

// DatabaseConfig optimized for database connections
// More tolerant of failures since databases should be stable and temporary
// network issues shouldn't immediately trip the breaker
func DatabaseConfig() Config {
	return Config{
		MaxRequests:         5,                // Allow more retry attempts
		Interval:            3 * time.Minute,  // Longer observation window
		Timeout:             45 * time.Second, // Longer timeout for complex queries
		ConsecutiveFailures: 20,               // More tolerant of consecutive failures
		FailureRatio:        0.6,              // Higher failure threshold
		MinRequests:         15,               // More samples before tripping
	}
}
