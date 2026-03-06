// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

// Package cache provides caching interfaces and implementations for tenant
// configuration data, reducing HTTP roundtrips to the Tenant Manager service.
package cache

import (
	"context"
	"errors"
	"time"
)

// ErrCacheMiss is returned when a requested key is not found in the cache
// or has expired.
var ErrCacheMiss = errors.New("cache miss")

// ConfigCache is the interface for tenant config caching.
// Implementations must be safe for concurrent use by multiple goroutines.
//
// Available implementations:
//   - InMemoryCache (default): Zero-dependency, process-local cache with TTL
//   - Custom implementations can be provided via client.WithCache()
type ConfigCache interface {
	// Get retrieves a cached value by key.
	// Returns ErrCacheMiss if the key is not found or has expired.
	Get(ctx context.Context, key string) (string, error)

	// Set stores a value with the given TTL.
	// A TTL of zero or negative means the entry never expires.
	Set(ctx context.Context, key string, value string, ttl time.Duration) error

	// Del removes a key from the cache.
	// Returns nil if the key does not exist.
	Del(ctx context.Context, key string) error
}
