// Package circuitbreaker provides service-level circuit breaker orchestration
// and health-check-driven recovery helpers.
//
// Use NewManager to create and manage per-service breakers, then run calls through
// Manager.Execute so failures are tracked consistently across callers.
//
// Optional health-check integration can automatically reset breakers after
// downstream services recover.
package circuitbreaker
