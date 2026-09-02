// Package eviction provides shared LRU eviction logic for multi-tenant
// connection managers. Each manager (postgres, mongo, rabbitmq) delegates
// the "find oldest idle candidate" decision to this package and keeps only
// the technology-specific cleanup (closing the actual connection, removing
// from manager-specific maps).
package eviction

import (
	"context"
	"fmt"
	"time"

	"github.com/LerianStudio/lib-commons/v6/commons/obs"
)

// DefaultIdleTimeout is the default duration before a tenant connection becomes
// eligible for eviction. Connections accessed within this window are considered
// active and will not be evicted, allowing the pool to grow beyond maxConnections.
const DefaultIdleTimeout = 5 * time.Minute

// FindLRUEvictionCandidate finds the oldest idle connection that exceeds the
// idle timeout. It returns the ID to evict and true, or an empty string and
// false if no eviction is needed.
//
// The function performs two checks before scanning:
//  1. If maxConnections <= 0, eviction is disabled (unlimited pool) -- return immediately.
//  2. If connectionCount < maxConnections, the pool has room -- return immediately.
//
// When eviction IS needed, the function iterates lastAccessed and selects the
// entry with the oldest timestamp that has been idle longer than idleTimeout.
// If all connections are active (used within the idle timeout), the pool is
// allowed to grow beyond the soft limit and no eviction occurs.
func FindLRUEvictionCandidate(
	connectionCount int,
	maxConnections int,
	lastAccessed map[string]time.Time,
	idleTimeout time.Duration,
	logger obs.Logger,
) (string, bool) {
	if maxConnections <= 0 || connectionCount < maxConnections {
		return "", false
	}

	if idleTimeout <= 0 {
		idleTimeout = DefaultIdleTimeout
	}

	now := time.Now()

	var oldestID string

	var oldestTime time.Time

	for id, t := range lastAccessed {
		idleDuration := now.Sub(t)
		if idleDuration < idleTimeout {
			continue
		}

		if oldestID == "" || t.Before(oldestTime) {
			oldestID = id
			oldestTime = t
		}
	}

	if oldestID == "" {
		if logger != nil {
			logger.Log(context.Background(), obs.LevelWarn,
				"connection pool at capacity but no idle connections to evict",
				"connection_count", connectionCount,
				"max_connections", maxConnections,
			)
		}

		return "", false
	}

	if logger != nil {
		logger.Log(context.Background(), obs.LevelInfo,
			"evicting idle tenant connection",
			"tenant_id", oldestID,
			"idle_duration", fmt.Sprintf("%v", now.Sub(oldestTime)),
			"idle_timeout", fmt.Sprintf("%v", idleTimeout),
		)
	}

	return oldestID, true
}
