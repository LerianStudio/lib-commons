// Package mongo provides resilient MongoDB connection and index management helpers.
//
// The package wraps connection lifecycle concerns (connect, ping, close), offers
// EnsureIndexes for idempotent index creation with structured error reporting,
// and supports TLS configuration for encrypted connections.
package mongo
