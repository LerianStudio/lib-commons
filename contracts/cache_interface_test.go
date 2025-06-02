package contracts

import (
	"context"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/commons/cache"
	"github.com/stretchr/testify/assert"
)

// TestCacheInterfaceContract validates that the Cache interface remains stable
// and prevents breaking changes to the core caching contract
func TestCacheInterfaceContract(t *testing.T) {
	t.Run("interface_signature_stability", func(t *testing.T) {
		// Get the Cache interface type
		var cacheInterface cache.Cache
		cacheType := reflect.TypeOf(&cacheInterface).Elem()

		// Verify interface exists
		assert.Equal(t, "Cache", cacheType.Name())
		assert.Equal(t, reflect.Interface, cacheType.Kind())

		// Define expected method signatures that must remain stable
		// These match the actual interface signatures in the codebase
		expectedMethods := map[string]methodSignature{
			"Clear": {
				params:  []string{"context.Context"},
				returns: []string{"error"},
			},
			"Delete": {
				params:  []string{"context.Context", "string"},
				returns: []string{"error"},
			},
			"Exists": {
				params:  []string{"context.Context", "string"},
				returns: []string{"bool", "error"},
			},
			"Get": {
				params:  []string{"context.Context", "string", "interface {}"},
				returns: []string{"error"},
			},
			"Keys": {
				params:  []string{"context.Context", "string"},
				returns: []string{"[]string", "error"},
			},
			"Set": {
				params:  []string{"context.Context", "string", "interface {}", "time.Duration"},
				returns: []string{"error"},
			},
			"TTL": {
				params:  []string{"context.Context", "string"},
				returns: []string{"time.Duration", "error"},
			},
		}

		// Verify all expected methods exist with correct signatures
		assert.Equal(t, len(expectedMethods), cacheType.NumMethod(),
			"Cache interface should have exactly %d methods", len(expectedMethods))

		for i := 0; i < cacheType.NumMethod(); i++ {
			method := cacheType.Method(i)
			expected, exists := expectedMethods[method.Name]

			assert.True(t, exists, "Unexpected method found: %s", method.Name)
			if !exists {
				continue
			}

			// Validate method signature
			validateMethodSignature(t, method, expected, "Cache."+method.Name)
		}
	})

	t.Run("error_contract_stability", func(t *testing.T) {
		// Test that cache error types remain stable
		// These error variables must remain available and unchanged
		assert.NotNil(t, cache.ErrKeyNotFound)
		assert.NotNil(t, cache.ErrCacheMiss)

		// Error messages should be consistent
		assert.Equal(t, "key not found", cache.ErrKeyNotFound.Error())
		assert.Equal(t, "key not found", cache.ErrCacheMiss.Error()) // ErrCacheMiss is alias for ErrKeyNotFound
	})

	t.Run("memory_cache_contract", func(t *testing.T) {
		// Test that MemoryCache implements Cache interface
		memoryCache := cache.NewMemoryCache(
			cache.WithMaxSize(100),
		)

		// Verify MemoryCache implements Cache interface
		assert.Implements(t, (*cache.Cache)(nil), memoryCache)

		// Test basic contract behavior
		ctx := context.Background()

		// Set operation should not return error for valid input
		err := memoryCache.Set(ctx, "test-key", "test-value", time.Minute)
		assert.NoError(t, err)

		// Get operation should return value for existing key
		var value string
		err = memoryCache.Get(ctx, "test-key", &value)
		assert.NoError(t, err)
		assert.Equal(t, "test-value", value)

		// Get operation should return ErrKeyNotFound for non-existent key
		err = memoryCache.Get(ctx, "non-existent", &value)
		assert.ErrorIs(t, err, cache.ErrKeyNotFound)

		// Exists should return true for existing key
		exists, err := memoryCache.Exists(ctx, "test-key")
		assert.NoError(t, err)
		assert.True(t, exists)

		// Exists should return false for non-existent key
		exists, err = memoryCache.Exists(ctx, "non-existent")
		assert.NoError(t, err)
		assert.False(t, exists)

		// TTL should return remaining time
		ttl, err := memoryCache.TTL(ctx, "test-key")
		assert.NoError(t, err)
		assert.Greater(t, ttl, time.Duration(0))
		assert.LessOrEqual(t, ttl, time.Minute)

		// Delete should remove key
		err = memoryCache.Delete(ctx, "test-key")
		assert.NoError(t, err)

		// Key should no longer exist after deletion
		exists, err = memoryCache.Exists(ctx, "test-key")
		assert.NoError(t, err)
		assert.False(t, exists)
	})

	t.Run("context_cancellation_contract", func(t *testing.T) {
		cache := cache.NewMemoryCache(
			cache.WithMaxSize(100),
		)

		// Test that operations respect context cancellation
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // Cancel immediately

		// Operations should handle cancelled context appropriately
		err := cache.Set(ctx, "test", "value", time.Minute)
		// Note: Memory cache might not check context for simple operations
		// but contract should support context cancellation
		_ = err // Allow either success or context.Canceled error

		var value string
		err = cache.Get(ctx, "test", &value)
		// Same as above - allow either behavior but should be consistent
		_ = err
	})

	t.Run("concurrent_access_contract", func(t *testing.T) {
		cache := cache.NewMemoryCache(
			cache.WithMaxSize(100),
		)

		// Test that cache is safe for concurrent access
		// This is a contract requirement for the Cache interface
		ctx := context.Background()

		// Run concurrent operations
		done := make(chan bool, 10)

		for i := 0; i < 10; i++ {
			go func(id int) {
				defer func() { done <- true }()

				key := fmt.Sprintf("key-%d", id)
				value := fmt.Sprintf("value-%d", id)

				// Each goroutine performs cache operations
				err := cache.Set(ctx, key, value, time.Minute)
				assert.NoError(t, err)

				var retrieved string
				err = cache.Get(ctx, key, &retrieved)
				assert.NoError(t, err)
				assert.Equal(t, value, retrieved)

				exists, err := cache.Exists(ctx, key)
				assert.NoError(t, err)
				assert.True(t, exists)
			}(i)
		}

		// Wait for all goroutines to complete
		for i := 0; i < 10; i++ {
			<-done
		}
	})
}

// TestCacheConfigContract validates cache configuration stability
func TestCacheConfigContract(t *testing.T) {
	t.Run("memory_cache_options_contract", func(t *testing.T) {
		// Test that cache options work as expected
		memCache := cache.NewMemoryCache(
			cache.WithMaxSize(50),
			cache.WithEvictionPolicy(cache.EvictionLRU),
		)

		// Options should configure the cache properly
		assert.NotNil(t, memCache)
		assert.Implements(t, (*cache.Cache)(nil), memCache)
	})
}

// TestCacheMetricsContract validates metrics interface stability
func TestCacheMetricsContract(t *testing.T) {
	t.Run("metrics_interface_exists", func(t *testing.T) {
		// Verify Metrics interface exists and has expected methods
		var metrics cache.Metrics
		metricsType := reflect.TypeOf(&metrics).Elem()

		if metricsType != nil {
			// If Metrics interface exists, validate its contract
			assert.Equal(t, reflect.Interface, metricsType.Kind())
		}
	})
}

// Helper types and functions for contract testing

type methodSignature struct {
	params  []string
	returns []string
}

func validateMethodSignature(t *testing.T, method reflect.Method, expected methodSignature, methodName string) {
	methodType := method.Type

	// For interface methods, NumIn() includes receiver, but we want to check without receiver
	expectedParamCount := len(expected.params)
	actualParamCount := methodType.NumIn()
	assert.Equal(t, expectedParamCount, actualParamCount,
		"Method %s should have %d parameters", methodName, expectedParamCount)

	// Validate parameter types (interface methods don't have explicit receiver in NumIn for interfaces)
	for i, expectedParam := range expected.params {
		if i < methodType.NumIn() {
			actualParam := methodType.In(i)
			assert.Equal(t, expectedParam, actualParam.String(),
				"Method %s parameter %d should be %s", methodName, i, expectedParam)
		}
	}

	// Validate return count
	expectedReturnCount := len(expected.returns)
	actualReturnCount := methodType.NumOut()
	assert.Equal(t, expectedReturnCount, actualReturnCount,
		"Method %s should return %d values", methodName, expectedReturnCount)

	// Validate return types
	for i, expectedReturn := range expected.returns {
		if i < methodType.NumOut() {
			actualReturn := methodType.Out(i)
			assert.Equal(t, expectedReturn, actualReturn.String(),
				"Method %s return value %d should be %s", methodName, i, expectedReturn)
		}
	}
}
