package cache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestMemoryCache(t *testing.T) {
	t.Run("basic get and set", func(t *testing.T) {
		cache := NewMemoryCache()
		ctx := context.Background()

		// Set a value
		err := cache.Set(ctx, "key1", "value1", 0)
		assert.NoError(t, err)

		// Get the value
		var result string
		err = cache.Get(ctx, "key1", &result)
		assert.NoError(t, err)
		assert.Equal(t, "value1", result)
	})

	t.Run("get non-existent key", func(t *testing.T) {
		cache := NewMemoryCache()
		ctx := context.Background()

		var result string
		err := cache.Get(ctx, "nonexistent", &result)
		assert.ErrorIs(t, err, ErrCacheMiss)
	})

	t.Run("set with TTL", func(t *testing.T) {
		cache := NewMemoryCache()
		ctx := context.Background()

		// Set with 100ms TTL
		err := cache.Set(ctx, "ttl-key", "ttl-value", 100*time.Millisecond)
		assert.NoError(t, err)

		// Should exist immediately
		var result string
		err = cache.Get(ctx, "ttl-key", &result)
		assert.NoError(t, err)
		assert.Equal(t, "ttl-value", result)

		// Wait for expiration
		time.Sleep(150 * time.Millisecond)

		// Should be gone
		err = cache.Get(ctx, "ttl-key", &result)
		assert.ErrorIs(t, err, ErrCacheMiss)
	})

	t.Run("delete", func(t *testing.T) {
		cache := NewMemoryCache()
		ctx := context.Background()

		// Set and delete
		cache.Set(ctx, "del-key", "value", 0)
		err := cache.Delete(ctx, "del-key")
		assert.NoError(t, err)

		// Should be gone
		var result string
		err = cache.Get(ctx, "del-key", &result)
		assert.ErrorIs(t, err, ErrCacheMiss)
	})

	t.Run("clear", func(t *testing.T) {
		cache := NewMemoryCache()
		ctx := context.Background()

		// Set multiple values
		cache.Set(ctx, "key1", "value1", 0)
		cache.Set(ctx, "key2", "value2", 0)
		cache.Set(ctx, "key3", "value3", 0)

		// Clear all
		err := cache.Clear(ctx)
		assert.NoError(t, err)

		// All should be gone
		var result string
		assert.ErrorIs(t, cache.Get(ctx, "key1", &result), ErrCacheMiss)
		assert.ErrorIs(t, cache.Get(ctx, "key2", &result), ErrCacheMiss)
		assert.ErrorIs(t, cache.Get(ctx, "key3", &result), ErrCacheMiss)
	})

	t.Run("exists", func(t *testing.T) {
		cache := NewMemoryCache()
		ctx := context.Background()

		// Check non-existent
		exists, err := cache.Exists(ctx, "key")
		assert.NoError(t, err)
		assert.False(t, exists)

		// Set and check
		cache.Set(ctx, "key", "value", 0)
		exists, err = cache.Exists(ctx, "key")
		assert.NoError(t, err)
		assert.True(t, exists)
	})

	t.Run("get multiple", func(t *testing.T) {
		cache := NewMemoryCache()
		ctx := context.Background()

		// Set multiple
		cache.Set(ctx, "key1", "value1", 0)
		cache.Set(ctx, "key2", "value2", 0)

		// Get multiple
		results, err := cache.GetMultiple(ctx, []string{"key1", "key2", "key3"})
		assert.NoError(t, err)
		assert.Equal(t, "value1", results["key1"])
		assert.Equal(t, "value2", results["key2"])
		_, exists := results["key3"]
		assert.False(t, exists)
	})

	t.Run("set multiple", func(t *testing.T) {
		cache := NewMemoryCache()
		ctx := context.Background()

		// Set multiple
		items := map[string]interface{}{
			"multi1": "value1",
			"multi2": "value2",
			"multi3": "value3",
		}
		err := cache.SetMultiple(ctx, items, 0)
		assert.NoError(t, err)

		// Verify all set
		var v1, v2, v3 string
		assert.NoError(t, cache.Get(ctx, "multi1", &v1))
		assert.NoError(t, cache.Get(ctx, "multi2", &v2))
		assert.NoError(t, cache.Get(ctx, "multi3", &v3))
		assert.Equal(t, "value1", v1)
		assert.Equal(t, "value2", v2)
		assert.Equal(t, "value3", v3)
	})

	t.Run("complex types", func(t *testing.T) {
		cache := NewMemoryCache()
		ctx := context.Background()

		type User struct {
			ID   int
			Name string
		}

		user := User{ID: 1, Name: "John"}
		err := cache.Set(ctx, "user:1", user, 0)
		assert.NoError(t, err)

		var result User
		err = cache.Get(ctx, "user:1", &result)
		assert.NoError(t, err)
		assert.Equal(t, user, result)
	})

	t.Run("concurrent access", func(t *testing.T) {
		cache := NewMemoryCache()
		ctx := context.Background()

		var wg sync.WaitGroup
		errors := make(chan error, 100)

		// Concurrent writes
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				key := string(rune('a' + n%26))
				if err := cache.Set(ctx, key, n, 0); err != nil {
					errors <- err
				}
			}(i)
		}

		// Concurrent reads
		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func(n int) {
				defer wg.Done()
				key := string(rune('a' + n%26))
				var val int
				cache.Get(ctx, key, &val)
			}(i)
		}

		wg.Wait()
		close(errors)

		// Check no errors
		for err := range errors {
			assert.NoError(t, err)
		}
	})

	t.Run("size limits", func(t *testing.T) {
		cache := NewMemoryCache(WithMaxSize(3))
		ctx := context.Background()

		// Fill cache
		cache.Set(ctx, "key1", "value1", 0)
		cache.Set(ctx, "key2", "value2", 0)
		cache.Set(ctx, "key3", "value3", 0)

		// Add one more (should evict oldest)
		cache.Set(ctx, "key4", "value4", 0)

		// key1 should be evicted
		var result string
		err := cache.Get(ctx, "key1", &result)
		assert.ErrorIs(t, err, ErrCacheMiss)

		// Others should exist
		assert.NoError(t, cache.Get(ctx, "key2", &result))
		assert.NoError(t, cache.Get(ctx, "key3", &result))
		assert.NoError(t, cache.Get(ctx, "key4", &result))
	})

	t.Run("cleanup expired", func(t *testing.T) {
		cache := NewMemoryCache(WithCleanupInterval(50 * time.Millisecond))
		defer cache.Stop()

		ctx := context.Background()

		// Set with short TTL
		cache.Set(ctx, "expire1", "value1", 30*time.Millisecond)
		cache.Set(ctx, "expire2", "value2", 30*time.Millisecond)
		cache.Set(ctx, "keep", "value3", 5*time.Second)

		// Wait for cleanup
		time.Sleep(100 * time.Millisecond)

		// Expired should be gone
		var result string
		assert.ErrorIs(t, cache.Get(ctx, "expire1", &result), ErrCacheMiss)
		assert.ErrorIs(t, cache.Get(ctx, "expire2", &result), ErrCacheMiss)

		// Non-expired should remain
		assert.NoError(t, cache.Get(ctx, "keep", &result))
	})
}

func TestCacheWrapper(t *testing.T) {
	t.Run("load through cache", func(t *testing.T) {
		base := NewMemoryCache()
		loads := int32(0)

		loader := func(ctx context.Context, key string) (interface{}, time.Duration, error) {
			atomic.AddInt32(&loads, 1)
			return "loaded-" + key, 0, nil
		}

		cache := NewLoadingCache(base, loader)
		ctx := context.Background()

		// First get should load
		var result string
		err := cache.Get(ctx, "key1", &result)
		assert.NoError(t, err)
		assert.Equal(t, "loaded-key1", result)
		assert.Equal(t, int32(1), atomic.LoadInt32(&loads))

		// Second get should use cache
		err = cache.Get(ctx, "key1", &result)
		assert.NoError(t, err)
		assert.Equal(t, "loaded-key1", result)
		assert.Equal(t, int32(1), atomic.LoadInt32(&loads))
	})

	t.Run("loader error", func(t *testing.T) {
		base := NewMemoryCache()
		loader := func(ctx context.Context, key string) (interface{}, time.Duration, error) {
			return nil, 0, errors.New("load failed")
		}

		cache := NewLoadingCache(base, loader)
		ctx := context.Background()

		var result string
		err := cache.Get(ctx, "key1", &result)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "load failed")
	})

	t.Run("refresh on miss", func(t *testing.T) {
		base := NewMemoryCache()
		loads := int32(0)

		loader := func(ctx context.Context, key string) (interface{}, time.Duration, error) {
			n := atomic.AddInt32(&loads, 1)
			return "value-" + string(rune('0'+n)), 50 * time.Millisecond, nil
		}

		cache := NewLoadingCache(base, loader)
		ctx := context.Background()

		// Load value
		var result string
		cache.Get(ctx, "key", &result)
		assert.Equal(t, "value-1", result)

		// Wait for expiration
		time.Sleep(100 * time.Millisecond)

		// Should reload
		cache.Get(ctx, "key", &result)
		assert.Equal(t, "value-2", result)
		assert.Equal(t, int32(2), atomic.LoadInt32(&loads))
	})

	t.Run("namespace wrapper", func(t *testing.T) {
		base := NewMemoryCache()
		cache := NewNamespaceCache(base, "app1")
		ctx := context.Background()

		// Set in namespace
		cache.Set(ctx, "key", "value", 0)

		// Get from namespace
		var result string
		err := cache.Get(ctx, "key", &result)
		assert.NoError(t, err)
		assert.Equal(t, "value", result)

		// Direct access with full key
		err = base.Get(ctx, "app1:key", &result)
		assert.NoError(t, err)
		assert.Equal(t, "value", result)

		// Different namespace doesn't see it
		cache2 := NewNamespaceCache(base, "app2")
		err = cache2.Get(ctx, "key", &result)
		assert.ErrorIs(t, err, ErrCacheMiss)
	})
}

type mockMetrics struct {
	hits      int
	misses    int
	sets      int
	deletes   int
	evictions int
	size      int
}

func (m *mockMetrics) IncrHits(cache string)              { m.hits++ }
func (m *mockMetrics) IncrMisses(cache string)            { m.misses++ }
func (m *mockMetrics) IncrSets(cache string)              { m.sets++ }
func (m *mockMetrics) IncrDeletes(cache string)           { m.deletes++ }
func (m *mockMetrics) IncrEvictions(cache string)         { m.evictions++ }
func (m *mockMetrics) ObserveSize(cache string, size int) { m.size = size }

func TestCacheMetrics(t *testing.T) {
	t.Run("track hits and misses", func(t *testing.T) {
		metrics := &mockMetrics{}
		cache := NewMemoryCache(WithMetrics(metrics))
		ctx := context.Background()

		// Some hits and misses
		cache.Set(ctx, "key1", "value1", 0)

		var result string
		cache.Get(ctx, "key1", &result) // hit
		cache.Get(ctx, "key1", &result) // hit
		cache.Get(ctx, "key2", &result) // miss
		cache.Get(ctx, "key3", &result) // miss

		assert.Equal(t, 2, metrics.hits)
		assert.Equal(t, 2, metrics.misses)
		assert.Equal(t, 1, metrics.sets)
	})
}

func TestSerializer(t *testing.T) {
	t.Run("JSON serializer", func(t *testing.T) {
		s := &JSONSerializer{}

		type Data struct {
			Name  string
			Value int
		}

		original := Data{Name: "test", Value: 42}

		// Serialize
		bytes, err := s.Serialize(original)
		assert.NoError(t, err)

		// Deserialize
		var result Data
		err = s.Deserialize(bytes, &result)
		assert.NoError(t, err)
		assert.Equal(t, original, result)
	})

	t.Run("Gob serializer", func(t *testing.T) {
		s := &GobSerializer{}

		type Data struct {
			Name  string
			Value int
		}

		original := Data{Name: "test", Value: 42}

		// Serialize
		bytes, err := s.Serialize(original)
		assert.NoError(t, err)

		// Deserialize
		var result Data
		err = s.Deserialize(bytes, &result)
		assert.NoError(t, err)
		assert.Equal(t, original, result)
	})
}

func BenchmarkCache(b *testing.B) {
	b.Run("MemoryCache_Set", func(b *testing.B) {
		cache := NewMemoryCache()
		ctx := context.Background()
		b.ResetTimer()

		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				cache.Set(ctx, "key"+string(rune(i%100)), i, 0)
				i++
			}
		})
	})

	b.Run("MemoryCache_Get", func(b *testing.B) {
		cache := NewMemoryCache()
		ctx := context.Background()

		// Pre-populate
		for i := 0; i < 100; i++ {
			cache.Set(ctx, "key"+string(rune(i)), i, 0)
		}

		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			var result int
			for pb.Next() {
				cache.Get(ctx, "key"+string(rune(i%100)), &result)
				i++
			}
		})
	})
}
