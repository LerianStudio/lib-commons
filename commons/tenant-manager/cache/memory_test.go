// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package cache

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestInMemoryCache_Get(t *testing.T) {
	tests := []struct {
		name          string
		setup         func(c *InMemoryCache)
		key           string
		wantValue     string
		wantErr       error
		wantErrString string
	}{
		{
			name:    "returns ErrCacheMiss for non-existent key",
			setup:   func(_ *InMemoryCache) {},
			key:     "missing-key",
			wantErr: ErrCacheMiss,
		},
		{
			name: "returns cached value for existing key",
			setup: func(c *InMemoryCache) {
				require.NoError(t, c.Set(context.Background(), "my-key", "my-value", time.Hour))
			},
			key:       "my-key",
			wantValue: "my-value",
		},
		{
			name: "returns ErrCacheMiss for expired key",
			setup: func(c *InMemoryCache) {
				require.NoError(t, c.Set(context.Background(), "expired-key", "old-value", time.Millisecond))
				time.Sleep(5 * time.Millisecond)
			},
			key:     "expired-key",
			wantErr: ErrCacheMiss,
		},
		{
			name: "returns value for key with zero TTL (never expires)",
			setup: func(c *InMemoryCache) {
				require.NoError(t, c.Set(context.Background(), "forever-key", "forever-value", 0))
			},
			key:       "forever-key",
			wantValue: "forever-value",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewInMemoryCache()
			defer func() { require.NoError(t, c.Close()) }()

			tt.setup(c)

			value, err := c.Get(context.Background(), tt.key)

			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
				assert.Empty(t, value)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.wantValue, value)
		})
	}
}

func TestInMemoryCache_Set(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		value string
		ttl   time.Duration
	}{
		{
			name:  "stores value with positive TTL",
			key:   "key-1",
			value: "value-1",
			ttl:   time.Hour,
		},
		{
			name:  "stores value with zero TTL (never expires)",
			key:   "key-2",
			value: "value-2",
			ttl:   0,
		},
		{
			name:  "stores value with negative TTL (never expires)",
			key:   "key-3",
			value: "value-3",
			ttl:   -1 * time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewInMemoryCache()
			defer func() { require.NoError(t, c.Close()) }()

			err := c.Set(context.Background(), tt.key, tt.value, tt.ttl)
			require.NoError(t, err)

			got, getErr := c.Get(context.Background(), tt.key)
			require.NoError(t, getErr)
			assert.Equal(t, tt.value, got)
		})
	}
}

func TestInMemoryCache_Set_Overwrites(t *testing.T) {
	c := NewInMemoryCache()
	defer func() { require.NoError(t, c.Close()) }()

	ctx := context.Background()

	require.NoError(t, c.Set(ctx, "key", "original", time.Hour))
	require.NoError(t, c.Set(ctx, "key", "updated", time.Hour))

	got, err := c.Get(ctx, "key")
	require.NoError(t, err)
	assert.Equal(t, "updated", got)
}

func TestInMemoryCache_Del(t *testing.T) {
	tests := []struct {
		name  string
		setup func(c *InMemoryCache)
		key   string
	}{
		{
			name: "deletes existing key",
			setup: func(c *InMemoryCache) {
				require.NoError(t, c.Set(context.Background(), "del-key", "value", time.Hour))
			},
			key: "del-key",
		},
		{
			name:  "returns nil for non-existent key",
			setup: func(_ *InMemoryCache) {},
			key:   "no-such-key",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := NewInMemoryCache()
			defer func() { require.NoError(t, c.Close()) }()

			tt.setup(c)

			err := c.Del(context.Background(), tt.key)
			require.NoError(t, err)

			// Verify key is gone
			_, getErr := c.Get(context.Background(), tt.key)
			assert.ErrorIs(t, getErr, ErrCacheMiss)
		})
	}
}

func TestInMemoryCache_TTLExpiration(t *testing.T) {
	c := NewInMemoryCache()
	defer func() { require.NoError(t, c.Close()) }()

	ctx := context.Background()

	// Set with a very short TTL
	require.NoError(t, c.Set(ctx, "short-lived", "value", 10*time.Millisecond))

	// Should be available immediately
	got, err := c.Get(ctx, "short-lived")
	require.NoError(t, err)
	assert.Equal(t, "value", got)

	// Wait for TTL to expire
	time.Sleep(20 * time.Millisecond)

	// Should now be expired (lazy eviction)
	_, err = c.Get(ctx, "short-lived")
	assert.ErrorIs(t, err, ErrCacheMiss)
}

func TestInMemoryCache_ConcurrentAccess(t *testing.T) {
	c := NewInMemoryCache()
	defer func() { require.NoError(t, c.Close()) }()

	ctx := context.Background()
	const goroutines = 50
	const iterations = 100

	var wg sync.WaitGroup

	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(id int) {
			defer wg.Done()

			for j := 0; j < iterations; j++ {
				key := "key"
				value := "value"

				// Mix of Set, Get, Del operations
				switch j % 3 {
				case 0:
					_ = c.Set(ctx, key, value, time.Hour)
				case 1:
					_, _ = c.Get(ctx, key)
				case 2:
					_ = c.Del(ctx, key)
				}
			}
		}(i)
	}

	wg.Wait()
}

func TestInMemoryCache_Close(t *testing.T) {
	t.Run("stops cleanup goroutine", func(t *testing.T) {
		c := NewInMemoryCache()

		err := c.Close()
		require.NoError(t, err)
	})

	t.Run("double close is safe", func(t *testing.T) {
		c := NewInMemoryCache()

		require.NoError(t, c.Close())
		require.NoError(t, c.Close())
	})
}

func TestInMemoryCache_EvictExpired(t *testing.T) {
	c := NewInMemoryCache()
	defer func() { require.NoError(t, c.Close()) }()

	ctx := context.Background()

	// Add entries: one expired, one still valid
	require.NoError(t, c.Set(ctx, "expired", "value", time.Millisecond))
	require.NoError(t, c.Set(ctx, "valid", "value", time.Hour))

	time.Sleep(5 * time.Millisecond)

	// Manually trigger eviction
	c.evictExpired()

	// Expired entry should be gone
	c.mu.RLock()
	_, expiredExists := c.entries["expired"]
	_, validExists := c.entries["valid"]
	c.mu.RUnlock()

	assert.False(t, expiredExists, "expired entry should have been evicted")
	assert.True(t, validExists, "valid entry should still exist")
}

func TestCacheEntry_IsExpired(t *testing.T) {
	tests := []struct {
		name      string
		entry     cacheEntry
		wantExpd  bool
	}{
		{
			name:     "zero expiresAt never expires",
			entry:    cacheEntry{value: "v", expiresAt: time.Time{}},
			wantExpd: false,
		},
		{
			name:     "future expiresAt is not expired",
			entry:    cacheEntry{value: "v", expiresAt: time.Now().Add(time.Hour)},
			wantExpd: false,
		},
		{
			name:     "past expiresAt is expired",
			entry:    cacheEntry{value: "v", expiresAt: time.Now().Add(-time.Second)},
			wantExpd: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantExpd, tt.entry.isExpired())
		})
	}
}
