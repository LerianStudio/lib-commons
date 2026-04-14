// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

//go:build unit

package tenantcache

import (
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

// TestMain ensures no goroutine leaks across the entire test suite.
// This package has no goroutines but we add goleak for consistency
// with the codebase convention.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestTenantCache_SetAndGet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		tenantID string
		config   *core.TenantConfig
		ttl      time.Duration
	}{
		{
			name:     "valid config",
			tenantID: "tenant-001",
			config:   &core.TenantConfig{ID: "tenant-001", TenantSlug: "acme", Service: "ledger"},
			ttl:      1 * time.Hour,
		},
		{
			name:     "minimal config",
			tenantID: "tenant-002",
			config:   &core.TenantConfig{ID: "tenant-002", TenantSlug: "beta"},
			ttl:      30 * time.Minute,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cache := NewTenantCache()
			cache.Set(tt.tenantID, tt.config, tt.ttl)

			entry, ok := cache.Get(tt.tenantID)
			require.True(t, ok, "Get should return true for existing entry")
			require.NotNil(t, entry)
			assert.Equal(t, tt.tenantID, entry.TenantID)
			assert.Equal(t, tt.config, entry.Config)
			assert.False(t, entry.IsExpired())
		})
	}
}

func TestTenantCache_Get_NotFound(t *testing.T) {
	t.Parallel()

	cache := NewTenantCache()

	entry, ok := cache.Get("nonexistent-tenant")
	assert.False(t, ok)
	assert.Nil(t, entry)
}

func TestTenantCache_Get_Expired(t *testing.T) {
	t.Parallel()

	cache := NewTenantCache()
	cache.Set("tenant-expired", &core.TenantConfig{ID: "tenant-expired"}, 1*time.Millisecond)
	time.Sleep(5 * time.Millisecond)

	entry, ok := cache.Get("tenant-expired")
	assert.False(t, ok, "Get should return false for expired entry (lazy eviction)")
	assert.Nil(t, entry)
	assert.Equal(t, 0, cache.Len(), "expired entry should be evicted from cache")
}

func TestTenantCache_Delete(t *testing.T) {
	t.Parallel()

	cache := NewTenantCache()
	cache.Set("tenant-delete", &core.TenantConfig{ID: "tenant-delete"}, 1*time.Hour)

	entry, ok := cache.Get("tenant-delete")
	require.True(t, ok)
	require.NotNil(t, entry)

	cache.Delete("tenant-delete")

	entry, ok = cache.Get("tenant-delete")
	assert.False(t, ok, "Get should return false after Delete")
	assert.Nil(t, entry)
	assert.Equal(t, 0, cache.Len())
}

func TestTenantCache_Delete_NonExistent(t *testing.T) {
	t.Parallel()

	cache := NewTenantCache()
	cache.Delete("nonexistent-tenant") // should not panic
	assert.Equal(t, 0, cache.Len())
}

func TestTenantCache_Touch(t *testing.T) {
	t.Parallel()

	cache := NewTenantCache()
	config := &core.TenantConfig{ID: "tenant-touch", TenantSlug: "touch-co"}
	cache.Set("tenant-touch", config, 10*time.Millisecond)

	touched := cache.Touch("tenant-touch", 1*time.Hour)
	assert.True(t, touched, "Touch should return true for existing entry")

	time.Sleep(15 * time.Millisecond) // past original TTL

	entry, ok := cache.Get("tenant-touch")
	require.True(t, ok, "Get should return true after Touch extended TTL")
	require.NotNil(t, entry)
	assert.Equal(t, "tenant-touch", entry.TenantID)
	assert.Equal(t, config, entry.Config)
}

func TestTenantCache_Touch_NonExistent(t *testing.T) {
	t.Parallel()

	cache := NewTenantCache()
	assert.False(t, cache.Touch("nonexistent", 1*time.Hour))
}

func TestTenantCache_Touch_Expired(t *testing.T) {
	t.Parallel()

	cache := NewTenantCache()
	cache.Set("tenant-exp", &core.TenantConfig{ID: "tenant-exp"}, 1*time.Millisecond)
	time.Sleep(5 * time.Millisecond)

	assert.False(t, cache.Touch("tenant-exp", 1*time.Hour), "Touch should return false for expired entry")
}

func TestTenantCache_IsExpired(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		tenantID    string
		ttl         time.Duration
		sleep       time.Duration
		wantExpired bool
		wantFound   bool
	}{
		{"not expired", "fresh", 1 * time.Hour, 0, false, true},
		{"expired", "stale", 1 * time.Millisecond, 5 * time.Millisecond, true, true},
		{"not found", "missing", 0, 0, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cache := NewTenantCache()
			if tt.ttl > 0 {
				cache.Set(tt.tenantID, &core.TenantConfig{ID: tt.tenantID}, tt.ttl)
			}
			if tt.sleep > 0 {
				time.Sleep(tt.sleep)
			}
			expired, found := cache.IsExpired(tt.tenantID)
			assert.Equal(t, tt.wantExpired, expired, "expired mismatch")
			assert.Equal(t, tt.wantFound, found, "found mismatch")
		})
	}
}

func TestTenantCache_Len(t *testing.T) {
	t.Parallel()

	cache := NewTenantCache()
	assert.Equal(t, 0, cache.Len())

	cache.Set("t-1", &core.TenantConfig{ID: "t-1"}, 1*time.Hour)
	assert.Equal(t, 1, cache.Len())

	cache.Set("t-2", &core.TenantConfig{ID: "t-2"}, 1*time.Hour)
	assert.Equal(t, 2, cache.Len())

	cache.Delete("t-1")
	assert.Equal(t, 1, cache.Len())

	cache.Delete("t-2")
	assert.Equal(t, 0, cache.Len())
}

func TestTenantCache_TenantIDs(t *testing.T) {
	t.Parallel()

	cache := NewTenantCache()

	assert.Empty(t, cache.TenantIDs(), "empty cache should return empty slice")

	cache.Set("tenant-b", &core.TenantConfig{ID: "tenant-b"}, 1*time.Hour)
	cache.Set("tenant-a", &core.TenantConfig{ID: "tenant-a"}, 1*time.Hour)
	cache.Set("tenant-c", &core.TenantConfig{ID: "tenant-c"}, 1*time.Hour)

	ids := cache.TenantIDs()
	sort.Strings(ids)
	assert.Equal(t, []string{"tenant-a", "tenant-b", "tenant-c"}, ids)
}

func TestTenantCache_TenantIDs_ExcludesExpired(t *testing.T) {
	t.Parallel()

	cache := NewTenantCache()
	cache.Set("tenant-valid", &core.TenantConfig{ID: "tenant-valid"}, 1*time.Hour)
	cache.Set("tenant-expired", &core.TenantConfig{ID: "tenant-expired"}, 1*time.Millisecond)
	time.Sleep(5 * time.Millisecond)

	ids := cache.TenantIDs()
	assert.Equal(t, []string{"tenant-valid"}, ids, "TenantIDs should exclude expired entries")
}

func TestTenantCache_ConcurrentAccess(t *testing.T) {
	t.Parallel()

	cache := NewTenantCache()
	const goroutines = 50
	const opsPerGoroutine = 100

	tenantIDs := generateTenantIDs(goroutines)

	var wg sync.WaitGroup
	wg.Add(goroutines * 2) // writers + mixed-ops

	for i := range goroutines {
		go func(id int) {
			defer wg.Done()
			tenantID := tenantIDs[id]
			cfg := &core.TenantConfig{ID: tenantID, TenantSlug: tenantID}
			for range opsPerGoroutine {
				cache.Set(tenantID, cfg, 1*time.Hour)
				cache.Get(tenantID)
				cache.IsExpired(tenantID)
				cache.Touch(tenantID, 1*time.Hour)
				cache.Len()
				cache.TenantIDs()
			}
		}(i)
	}

	// Concurrent deleters
	for i := range goroutines {
		go func(id int) {
			defer wg.Done()
			tenantID := tenantIDs[id]
			for range opsPerGoroutine {
				cache.Delete(tenantID)
			}
		}(i)
	}

	wg.Wait()
	// No race detector complaints = success
	assert.True(t, true, "concurrent operations should not race")
}

func TestDefaultTenantCacheTTL(t *testing.T) {
	t.Parallel()

	assert.Equal(t, 12*time.Hour, DefaultTenantCacheTTL, "DefaultTenantCacheTTL should be 12 hours")
}

func TestTenantCacheEntry_IsExpired(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		expiresAt   time.Time
		wantExpired bool
	}{
		{"future expiration", time.Now().Add(1 * time.Hour), false},
		{"past expiration", time.Now().Add(-1 * time.Hour), true},
		{"just expired (1ms ago)", time.Now().Add(-1 * time.Millisecond), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			entry := &TenantCacheEntry{
				TenantID:  "test-tenant",
				Config:    &core.TenantConfig{ID: "test-tenant"},
				ExpiresAt: tt.expiresAt,
			}
			assert.Equal(t, tt.wantExpired, entry.IsExpired())
		})
	}
}

// generateTenantIDs creates a slice of N tenant IDs for testing.
func generateTenantIDs(n int) []string {
	ids := make([]string, n)
	for i := range n {
		ids[i] = fmt.Sprintf("tenant-%04d", i)
	}

	return ids
}
