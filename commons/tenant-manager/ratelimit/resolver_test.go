// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

//go:build unit

package tmratelimit

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/net/http/ratelimit"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewCacheTierResolver(t *testing.T) {
	t.Parallel()

	t.Run("nil cache returns error", func(t *testing.T) {
		t.Parallel()

		r, err := NewCacheTierResolver(nil, nil)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "cache must not be nil")
		assert.Nil(t, r)
	})

	t.Run("valid cache creates resolver", func(t *testing.T) {
		t.Parallel()

		cache := NewRateLimitCache()

		r, err := NewCacheTierResolver(cache, nil)

		require.NoError(t, err)
		assert.NotNil(t, r)
	})

	t.Run("with fallbacks", func(t *testing.T) {
		t.Parallel()

		cache := NewRateLimitCache()
		fallbacks := map[string]ratelimit.Tier{
			"default": {Name: "default", Max: 100, Window: time.Minute},
		}

		r, err := NewCacheTierResolver(cache, fallbacks)

		require.NoError(t, err)
		assert.NotNil(t, r)
	})
}

func TestCacheTierResolver_Resolve(t *testing.T) {
	t.Parallel()

	t.Run("empty tenantID returns zero false", func(t *testing.T) {
		t.Parallel()

		cache := NewRateLimitCache()
		r, err := NewCacheTierResolver(cache, nil)
		require.NoError(t, err)

		tier, ok := r.Resolve("", "default")

		assert.False(t, ok)
		assert.Equal(t, ratelimit.Tier{}, tier)
	})

	t.Run("cache hit with tier configured returns tier", func(t *testing.T) {
		t.Parallel()

		cache := NewRateLimitCache()
		cache.Set("tenant-1", core.RateLimitSettings{
			"default": &core.TierLimit{Max: 100, Window: 60},
		})

		r, err := NewCacheTierResolver(cache, nil)
		require.NoError(t, err)

		tier, ok := r.Resolve("tenant-1", "default")

		assert.True(t, ok)
		assert.Equal(t, "default", tier.Name)
		assert.Equal(t, 100, tier.Max)
		assert.Equal(t, 60*time.Second, tier.Window)
	})

	t.Run("cache hit with tier not configured returns zero false", func(t *testing.T) {
		t.Parallel()

		cache := NewRateLimitCache()
		cache.Set("tenant-1", core.RateLimitSettings{
			"default": &core.TierLimit{Max: 100, Window: 60},
		})

		r, err := NewCacheTierResolver(cache, nil)
		require.NoError(t, err)

		tier, ok := r.Resolve("tenant-1", "aggressive")

		assert.False(t, ok)
		assert.Equal(t, ratelimit.Tier{}, tier)
	})

	t.Run("cache hit with nil settings returns zero false", func(t *testing.T) {
		t.Parallel()

		cache := NewRateLimitCache()
		cache.Set("tenant-1", nil)

		r, err := NewCacheTierResolver(cache, nil)
		require.NoError(t, err)

		tier, ok := r.Resolve("tenant-1", "default")

		assert.False(t, ok)
		assert.Equal(t, ratelimit.Tier{}, tier)
	})

	t.Run("cache miss without loader returns zero false", func(t *testing.T) {
		t.Parallel()

		cache := NewRateLimitCache()

		r, err := NewCacheTierResolver(cache, nil)
		require.NoError(t, err)

		tier, ok := r.Resolve("tenant-1", "default")

		assert.False(t, ok)
		assert.Equal(t, ratelimit.Tier{}, tier)
	})

	t.Run("cache miss with loader triggers async load", func(t *testing.T) {
		t.Parallel()

		loadCalled := make(chan struct{}, 1)

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)

			_, _ = w.Write([]byte(`{"rateLimit":{"default":{"max":100,"window":60}}}`))

			select {
			case loadCalled <- struct{}{}:
			default:
			}
		}))
		defer srv.Close()

		client := mustNewTestClient(t, srv.URL)
		cache := NewRateLimitCache()
		loader, err := NewRateLimitLoader(client, cache)
		require.NoError(t, err)

		r, err := NewCacheTierResolver(cache, nil, WithResolverLoader(loader))
		require.NoError(t, err)

		// First call: cache miss, triggers async load, returns (zero, false).
		tier, ok := r.Resolve("tenant-1", "default")

		assert.False(t, ok)
		assert.Equal(t, ratelimit.Tier{}, tier)

		// Wait for async load to complete.
		select {
		case <-loadCalled:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for async load")
		}

		// Give the goroutine time to finish caching.
		time.Sleep(50 * time.Millisecond)

		// Second call: cache should now be populated.
		tier, ok = r.Resolve("tenant-1", "default")

		assert.True(t, ok)
		assert.Equal(t, 100, tier.Max)
		assert.Equal(t, 60*time.Second, tier.Window)
	})
}

func TestCacheTierResolver_Fallback(t *testing.T) {
	t.Parallel()

	t.Run("known tier returns registered fallback", func(t *testing.T) {
		t.Parallel()

		cache := NewRateLimitCache()
		fallbacks := map[string]ratelimit.Tier{
			"default":    {Name: "default", Max: 100, Window: time.Minute},
			"aggressive": {Name: "aggressive", Max: 20, Window: 10 * time.Second},
		}

		r, err := NewCacheTierResolver(cache, fallbacks)
		require.NoError(t, err)

		tier := r.Fallback("default")

		assert.Equal(t, "default", tier.Name)
		assert.Equal(t, 100, tier.Max)
		assert.Equal(t, time.Minute, tier.Window)
	})

	t.Run("unknown tier returns zero tier", func(t *testing.T) {
		t.Parallel()

		cache := NewRateLimitCache()
		fallbacks := map[string]ratelimit.Tier{
			"default": {Name: "default", Max: 100, Window: time.Minute},
		}

		r, err := NewCacheTierResolver(cache, fallbacks)
		require.NoError(t, err)

		tier := r.Fallback("nonexistent")

		assert.Equal(t, ratelimit.Tier{}, tier)
	})

	t.Run("nil fallbacks returns zero tier", func(t *testing.T) {
		t.Parallel()

		cache := NewRateLimitCache()

		r, err := NewCacheTierResolver(cache, nil)
		require.NoError(t, err)

		tier := r.Fallback("default")

		assert.Equal(t, ratelimit.Tier{}, tier)
	})
}

func TestConvertToTier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		tierName string
		limit    *core.TierLimit
		want     ratelimit.Tier
	}{
		{
			name:     "60 second window",
			tierName: "default",
			limit:    &core.TierLimit{Max: 100, Window: 60},
			want:     ratelimit.Tier{Name: "default", Max: 100, Window: 60 * time.Second},
		},
		{
			name:     "10 second window",
			tierName: "aggressive",
			limit:    &core.TierLimit{Max: 20, Window: 10},
			want:     ratelimit.Tier{Name: "aggressive", Max: 20, Window: 10 * time.Second},
		},
		{
			name:     "large window",
			tierName: "relaxed",
			limit:    &core.TierLimit{Max: 1000, Window: 3600},
			want:     ratelimit.Tier{Name: "relaxed", Max: 1000, Window: 3600 * time.Second},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := convertToTier(tc.tierName, tc.limit)

			assert.Equal(t, tc.want, got)
		})
	}
}
