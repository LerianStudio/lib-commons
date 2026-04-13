// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

//go:build unit

package tmratelimit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRateLimitLoader_Load(t *testing.T) {
	t.Parallel()

	t.Run("success caches settings", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)

			_, _ = w.Write([]byte(`{"rateLimit":{"default":{"max":100,"window":60}}}`))
		}))
		defer srv.Close()

		client := mustNewTestClient(t, srv.URL)
		cache := NewRateLimitCache()
		loader, err := NewRateLimitLoader(client, cache)
		require.NoError(t, err)

		err = loader.Load(context.Background(), "tenant-1")

		require.NoError(t, err)

		settings, ok := cache.Get("tenant-1")
		assert.True(t, ok)
		require.NotNil(t, settings)
		assert.Equal(t, 100, settings["default"].Max)
	})

	t.Run("404 caches nil settings", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		client := mustNewTestClient(t, srv.URL)
		cache := NewRateLimitCache()
		loader, err := NewRateLimitLoader(client, cache)
		require.NoError(t, err)

		err = loader.Load(context.Background(), "tenant-1")

		require.NoError(t, err)

		settings, ok := cache.Get("tenant-1")
		assert.True(t, ok, "nil settings should be cached as a hit")
		assert.Nil(t, settings, "settings should be nil for 404")
	})

	t.Run("client error does not cache", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		client := mustNewTestClient(t, srv.URL)
		cache := NewRateLimitCache()
		loader, err := NewRateLimitLoader(client, cache)
		require.NoError(t, err)

		err = loader.Load(context.Background(), "tenant-1")

		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to load rate limits")

		_, ok := cache.Get("tenant-1")
		assert.False(t, ok, "cache should not be populated on error")
	})

	t.Run("concurrent loads for same tenant make only one HTTP call", func(t *testing.T) {
		t.Parallel()

		var callCount atomic.Int64

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			callCount.Add(1)
			// Simulate latency so concurrent goroutines overlap.
			time.Sleep(50 * time.Millisecond)

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)

			_, _ = w.Write([]byte(`{"rateLimit":{"default":{"max":100,"window":60}}}`))
		}))
		defer srv.Close()

		client := mustNewTestClient(t, srv.URL)
		cache := NewRateLimitCache()
		loader, err := NewRateLimitLoader(client, cache)
		require.NoError(t, err)

		const goroutines = 10

		errCh := make(chan error, goroutines)

		for range goroutines {
			go func() {
				errCh <- loader.Load(context.Background(), "tenant-1")
			}()
		}

		for range goroutines {
			err := <-errCh
			assert.NoError(t, err)
		}

		// The per-tenant mutex + double-check cache pattern should prevent all but
		// the first goroutine from making an HTTP call.
		assert.Equal(t, int64(1), callCount.Load(),
			"only one HTTP call expected due to per-tenant lock + double-check")
	})

	t.Run("nil client returns constructor error", func(t *testing.T) {
		t.Parallel()

		cache := NewRateLimitCache()
		_, err := NewRateLimitLoader(nil, cache)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "client must not be nil")
	})

	t.Run("nil cache returns constructor error", func(t *testing.T) {
		t.Parallel()

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		client := mustNewTestClient(t, srv.URL)
		_, err := NewRateLimitLoader(client, nil)

		require.Error(t, err)
		assert.Contains(t, err.Error(), "cache must not be nil")
	})

	t.Run("double-check skips fetch when cache is populated", func(t *testing.T) {
		t.Parallel()

		var callCount atomic.Int64

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			callCount.Add(1)

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)

			_, _ = w.Write([]byte(`{"rateLimit":{"default":{"max":50,"window":30}}}`))
		}))
		defer srv.Close()

		client := mustNewTestClient(t, srv.URL)
		cache := NewRateLimitCache()
		loader, err := NewRateLimitLoader(client, cache)
		require.NoError(t, err)

		// Pre-populate cache.
		cache.Set("tenant-1", core.RateLimitSettings{
			"default": &core.TierLimit{Max: 200, Window: 120},
		})

		err = loader.Load(context.Background(), "tenant-1")

		require.NoError(t, err)
		assert.Equal(t, int64(0), callCount.Load(), "no HTTP call when cache is populated")

		// Verify original cached value is unchanged.
		settings, ok := cache.Get("tenant-1")
		assert.True(t, ok)
		assert.Equal(t, 200, settings["default"].Max)
	})

	t.Run("refresh bypasses double-check and re-fetches", func(t *testing.T) {
		t.Parallel()

		var callCount atomic.Int64

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			callCount.Add(1)

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)

			_, _ = w.Write([]byte(`{"rateLimit":{"default":{"max":999,"window":120}}}`))
		}))
		defer srv.Close()

		client := mustNewTestClient(t, srv.URL)
		cache := NewRateLimitCache()
		loader, err := NewRateLimitLoader(client, cache)
		require.NoError(t, err)

		// Pre-populate cache with stale data.
		cache.Set("tenant-1", core.RateLimitSettings{
			"default": &core.TierLimit{Max: 100, Window: 60},
		})

		err = loader.Refresh(context.Background(), "tenant-1")

		require.NoError(t, err)
		assert.Equal(t, int64(1), callCount.Load(), "Refresh should always fetch from API")

		// Verify cache was updated with fresh data.
		settings, ok := cache.Get("tenant-1")
		assert.True(t, ok)
		require.NotNil(t, settings)
		assert.Equal(t, 999, settings["default"].Max)
	})
}
