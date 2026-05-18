// Package commons provides shared infrastructure helpers used across Lerian services.
//
// The package includes validation utilities, error adapters, and cross-cutting
// primitives used by higher-level subpackages.
//
// This package is intentionally dependency-light; specialized integrations live in
// subpackages such as mongo, redis, rabbitmq, and server.
package commons
