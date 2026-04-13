// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

//go:build unit

package tmratelimit

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRateLimitCache(t *testing.T) {
	t.Parallel()

	t.Run("creates cache with default TTL", func(t *testing.T) {
		t.Parallel()

		c := NewRateLimitCache()

		require.NotNil(t, c)
		assert.Equal(t, DefaultRateLimitCacheTTL, c.ttl)
		assert.Equal(t, 0, c.Len())
	})

	t.Run("creates cache with custom TTL", func(t *testing.T) {
		t.Parallel()

		c := NewRateLimitCache(WithCacheTTL(10 * time.Minute))

		assert.Equal(t, 10*time.Minute, c.ttl)
	})

	t.Run("WithCacheTTL zero falls back to default", func(t *testing.T) {
		t.Parallel()

		c := NewRateLimitCache(WithCacheTTL(0))

		assert.Equal(t, DefaultRateLimitCacheTTL, c.ttl)
	})

	t.Run("WithCacheTTL negative falls back to default", func(t *testing.T) {
		t.Parallel()

		c := NewRateLimitCache(WithCacheTTL(-1 * time.Second))

		assert.Equal(t, DefaultRateLimitCacheTTL, c.ttl)
	})
}

func TestRateLimitCache_Get(t *testing.T) {
	t.Parallel()

	t.Run("empty cache returns miss", func(t *testing.T) {
		t.Parallel()

		c := NewRateLimitCache()

		settings, ok := c.Get("tenant-1")

		assert.False(t, ok)
		assert.Nil(t, settings)
	})

	t.Run("returns cached settings", func(t *testing.T) {
		t.Parallel()

		c := NewRateLimitCache()
		settings := core.RateLimitSettings{
			"default": &core.TierLimit{Max: 100, Window: 60},
		}

		c.Set("tenant-1", settings)

		got, ok := c.Get("tenant-1")

		assert.True(t, ok)
		assert.Equal(t, settings, got)
	})

	t.Run("nil settings distinguishes no-config from not-fetched", func(t *testing.T) {
		t.Parallel()

		c := NewRateLimitCache()

		// Set nil settings (tenant has no rate limits configured).
		c.Set("tenant-1", nil)

		got, ok := c.Get("tenant-1")

		assert.True(t, ok, "cache hit expected for nil settings")
		assert.Nil(t, got, "settings should be nil")
	})

	t.Run("expired entry returns miss and is evicted", func(t *testing.T) {
		t.Parallel()

		// Use short TTL so entries expire quickly. The 50ms TTL with 200ms sleep
		// provides a reliable margin even under CI load.
		c := NewRateLimitCache(WithCacheTTL(50 * time.Millisecond))
		c.Set("tenant-1", core.RateLimitSettings{
			"default": &core.TierLimit{Max: 50, Window: 30},
		})

		// Wait for entry to expire.
		time.Sleep(200 * time.Millisecond)

		got, ok := c.Get("tenant-1")

		assert.False(t, ok, "expired entry should be a cache miss")
		assert.Nil(t, got)
		assert.Equal(t, 0, c.Len(), "expired entry should be lazily evicted")
	})
}

func TestRateLimitCache_Delete(t *testing.T) {
	t.Parallel()

	t.Run("deletes existing entry", func(t *testing.T) {
		t.Parallel()

		c := NewRateLimitCache()
		c.Set("tenant-1", core.RateLimitSettings{
			"default": &core.TierLimit{Max: 100, Window: 60},
		})

		c.Delete("tenant-1")

		_, ok := c.Get("tenant-1")
		assert.False(t, ok)
		assert.Equal(t, 0, c.Len())
	})

	t.Run("delete non-existent key is no-op", func(t *testing.T) {
		t.Parallel()

		c := NewRateLimitCache()

		// Should not panic.
		c.Delete("nonexistent")

		assert.Equal(t, 0, c.Len())
	})
}

func TestRateLimitCache_SetThenDeleteThenGet(t *testing.T) {
	t.Parallel()

	c := NewRateLimitCache()
	c.Set("tenant-1", core.RateLimitSettings{
		"default": &core.TierLimit{Max: 100, Window: 60},
	})

	c.Delete("tenant-1")

	got, ok := c.Get("tenant-1")

	assert.False(t, ok)
	assert.Nil(t, got)
}

func TestRateLimitCache_Len(t *testing.T) {
	t.Parallel()

	c := NewRateLimitCache()

	assert.Equal(t, 0, c.Len())

	c.Set("tenant-1", nil)
	assert.Equal(t, 1, c.Len())

	c.Set("tenant-2", core.RateLimitSettings{"default": &core.TierLimit{Max: 10, Window: 60}})
	assert.Equal(t, 2, c.Len())

	c.Delete("tenant-1")
	assert.Equal(t, 1, c.Len())
}

func TestRateLimitCache_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	c := NewRateLimitCache()

	const goroutines = 50
	const iterations = 100

	var wg sync.WaitGroup

	wg.Add(goroutines)

	for i := range goroutines {
		go func(id int) {
			defer wg.Done()

			tenantID := fmt.Sprintf("tenant-%d", id%10)

			for range iterations {
				c.Set(tenantID, core.RateLimitSettings{
					"default": &core.TierLimit{Max: id, Window: 60},
				})

				c.Get(tenantID)
				c.Len()

				if id%3 == 0 {
					c.Delete(tenantID)
				}
			}
		}(i)
	}

	wg.Wait()

	// No assertion on final state — the goal is to verify no race condition via -race flag.
	assert.True(t, true, "concurrent access completed without race")
}
