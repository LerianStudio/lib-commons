# Caching Abstractions

The caching package provides flexible caching implementations with TTL support, eviction policies, and namespace isolation.

## Table of Contents

- [Overview](#overview)
- [Memory Cache](#memory-cache)
- [Loading Cache](#loading-cache)
- [Cache Serialization](#cache-serialization)
- [Eviction Policies](#eviction-policies)
- [Namespace Support](#namespace-support)
- [Monitoring and Metrics](#monitoring-and-metrics)
- [Best Practices](#best-practices)

## Overview

The caching package provides multiple cache implementations to suit different use cases:

- **Memory Cache**: In-memory cache with TTL and eviction policies
- **Loading Cache**: Automatic cache population with loader functions
- **Serialized Cache**: Support for complex object caching with serialization

### Features

- Time-to-live (TTL) support
- Multiple eviction policies (LRU, LFU, Random)
- Namespace isolation for multi-tenant applications
- Automatic cleanup of expired entries
- Thread-safe operations
- Metrics and monitoring support
- Serialization support for complex objects

## Memory Cache

### Basic Usage

```go
package main

import (
    "context"
    "fmt"
    "time"
    
    "github.com/yourusername/commons-go/commons/cache"
)

func main() {
    // Create a new memory cache
    cfg := cache.Config{
        MaxSize:        1000,
        DefaultTTL:     5 * time.Minute,
        CleanupInterval: 1 * time.Minute,
        EvictionPolicy: cache.EvictionLRU,
    }
    
    memCache := cache.NewMemoryCache(cfg)
    defer memCache.Close()
    
    ctx := context.Background()
    
    // Set a value
    err := memCache.Set(ctx, "user:123", "John Doe", 10*time.Minute)
    if err != nil {
        panic(err)
    }
    
    // Get a value
    value, exists, err := memCache.Get(ctx, "user:123")
    if err != nil {
        panic(err)
    }
    
    if exists {
        fmt.Printf("Found user: %s\n", value)
    }
    
    // Delete a value
    err = memCache.Delete(ctx, "user:123")
    if err != nil {
        panic(err)
    }
}
```

### TTL Management

```go
// Set with custom TTL
err := cache.Set(ctx, "session:abc123", sessionData, 30*time.Minute)

// Set with default TTL (from config)
err = cache.Set(ctx, "config:app", appConfig, 0) // Uses DefaultTTL

// Update TTL for existing key
err = cache.Touch(ctx, "session:abc123", 1*time.Hour)

// Get remaining TTL
ttl, err := cache.TTL(ctx, "session:abc123")
if err == nil && ttl > 0 {
    fmt.Printf("Key expires in: %v\n", ttl)
}
```

## Loading Cache

Loading cache automatically populates cache entries using a loader function when a cache miss occurs.

```go
// Define a loader function
userLoader := func(ctx context.Context, key string) (interface{}, error) {
    // Extract user ID from key
    userID := strings.TrimPrefix(key, "user:")
    
    // Load from database
    user, err := db.GetUser(ctx, userID)
    if err != nil {
        return nil, err
    }
    
    return user, nil
}

// Create loading cache
loadingCache := cache.NewLoadingCache(cfg, userLoader)

// Get value - will load automatically if not in cache
user, err := loadingCache.Get(ctx, "user:123")
if err != nil {
    log.Printf("Failed to get user: %v", err)
}

// Force refresh
user, err = loadingCache.Refresh(ctx, "user:123")
```

### Advanced Loading Cache

```go
// Loading cache with refresh ahead
type RefreshConfig struct {
    RefreshAfter time.Duration
    AsyncRefresh bool
}

cfg := cache.Config{
    MaxSize:    1000,
    DefaultTTL: 1 * time.Hour,
    RefreshConfig: &RefreshConfig{
        RefreshAfter: 45 * time.Minute, // Refresh 15 minutes before expiry
        AsyncRefresh: true,              // Don't block on refresh
    },
}

cache := cache.NewLoadingCache(cfg, loader)

// Batch loading
keys := []string{"user:1", "user:2", "user:3"}
results, err := cache.GetAll(ctx, keys)
```

## Cache Serialization

For caching complex objects, use serialization support:

```go
type User struct {
    ID        string    `json:"id"`
    Name      string    `json:"name"`
    Email     string    `json:"email"`
    CreatedAt time.Time `json:"created_at"`
}

// Create serialized cache with JSON
jsonCache := cache.NewSerializedCache(memCache, cache.SerializerJSON)

// Store complex object
user := &User{
    ID:        "123",
    Name:      "John Doe",
    Email:     "john@example.com",
    CreatedAt: time.Now(),
}

err := jsonCache.Set(ctx, "user:123", user, 5*time.Minute)

// Retrieve and deserialize
var retrieved User
err = jsonCache.Get(ctx, "user:123", &retrieved)
if err == nil {
    fmt.Printf("Retrieved user: %+v\n", retrieved)
}
```

### Custom Serialization

```go
// Implement custom serializer
type CustomSerializer struct{}

func (s *CustomSerializer) Serialize(value interface{}) ([]byte, error) {
    // Custom serialization logic
    return msgpack.Marshal(value)
}

func (s *CustomSerializer) Deserialize(data []byte, target interface{}) error {
    // Custom deserialization logic
    return msgpack.Unmarshal(data, target)
}

// Use custom serializer
customCache := cache.NewSerializedCache(memCache, &CustomSerializer{})
```

## Eviction Policies

### LRU (Least Recently Used)

```go
cfg := cache.Config{
    MaxSize:        1000,
    EvictionPolicy: cache.EvictionLRU,
}

cache := cache.NewMemoryCache(cfg)

// Least recently used items are evicted first when cache is full
```

### LFU (Least Frequently Used)

```go
cfg := cache.Config{
    MaxSize:        1000,
    EvictionPolicy: cache.EvictionLFU,
}

cache := cache.NewMemoryCache(cfg)

// Items with lowest access frequency are evicted first
```

### Custom Eviction Policy

```go
type PriorityEvictionPolicy struct{}

func (p *PriorityEvictionPolicy) ShouldEvict(entry *cache.Entry) bool {
    // Custom logic based on entry metadata
    priority, ok := entry.Metadata["priority"].(int)
    if !ok {
        return true // Evict if no priority
    }
    return priority < 5 // Evict low priority items
}

cfg := cache.Config{
    MaxSize:        1000,
    EvictionPolicy: &PriorityEvictionPolicy{},
}
```

## Namespace Support

Namespaces provide isolation between different cache domains:

```go
// Create base cache
baseCache := cache.NewMemoryCache(cfg)

// Create namespaced caches
userCache := cache.NewNamespacedCache(baseCache, "users")
sessionCache := cache.NewNamespacedCache(baseCache, "sessions")
configCache := cache.NewNamespacedCache(baseCache, "config")

// Keys are automatically prefixed with namespace
userCache.Set(ctx, "123", userData, 5*time.Minute)     // Stored as "users:123"
sessionCache.Set(ctx, "123", sessionData, 30*time.Minute) // Stored as "sessions:123"

// Clear entire namespace
userCache.Clear(ctx) // Only clears "users:*" keys
```

### Multi-Tenant Caching

```go
func getTenantCache(tenantID string) cache.Cache {
    namespace := fmt.Sprintf("tenant:%s", tenantID)
    return cache.NewNamespacedCache(baseCache, namespace)
}

// Use tenant-specific cache
tenantCache := getTenantCache("acme-corp")
tenantCache.Set(ctx, "settings", tenantSettings, 1*time.Hour)
```

## Monitoring and Metrics

### Cache Statistics

```go
stats := cache.Stats()
fmt.Printf("Cache Statistics:\n")
fmt.Printf("  Hits: %d\n", stats.Hits)
fmt.Printf("  Misses: %d\n", stats.Misses)
fmt.Printf("  Hit Rate: %.2f%%\n", stats.HitRate()*100)
fmt.Printf("  Evictions: %d\n", stats.Evictions)
fmt.Printf("  Size: %d/%d\n", stats.CurrentSize, stats.MaxSize)
```

### Metrics Integration

```go
// Prometheus metrics
var (
    cacheHits = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Name: "cache_hits_total",
            Help: "Total number of cache hits",
        },
        []string{"cache_name"},
    )
    
    cacheMisses = prometheus.NewCounterVec(
        prometheus.CounterOpts{
            Name: "cache_misses_total",
            Help: "Total number of cache misses",
        },
        []string{"cache_name"},
    )
)

// Wrap cache with metrics
type MetricsCache struct {
    cache.Cache
    name string
}

func (m *MetricsCache) Get(ctx context.Context, key string) (interface{}, bool, error) {
    value, exists, err := m.Cache.Get(ctx, key)
    
    if exists {
        cacheHits.WithLabelValues(m.name).Inc()
    } else {
        cacheMisses.WithLabelValues(m.name).Inc()
    }
    
    return value, exists, err
}
```

### Cache Events

```go
// Register event handlers
cache.OnEviction(func(key string, value interface{}, reason cache.EvictionReason) {
    log.Printf("Evicted key %s: %v", key, reason)
})

cache.OnExpiration(func(key string, value interface{}) {
    log.Printf("Expired key %s", key)
})

cache.OnMiss(func(key string) {
    log.Printf("Cache miss for key %s", key)
})
```

## Best Practices

### 1. Choose Appropriate TTL

```go
// Short TTL for frequently changing data
sessionCache.Set(ctx, "session:123", data, 5*time.Minute)

// Longer TTL for stable data
configCache.Set(ctx, "app:config", config, 24*time.Hour)

// No expiration for static data
staticCache.Set(ctx, "static:data", data, 0) // Never expires
```

### 2. Handle Cache Stampede

```go
// Use singleflight to prevent cache stampede
var group singleflight.Group

func getCachedUser(ctx context.Context, userID string) (*User, error) {
    key := fmt.Sprintf("user:%s", userID)
    
    // Check cache first
    if cached, exists, _ := cache.Get(ctx, key); exists {
        return cached.(*User), nil
    }
    
    // Use singleflight for loading
    result, err, _ := group.Do(key, func() (interface{}, error) {
        // Check cache again (double-check pattern)
        if cached, exists, _ := cache.Get(ctx, key); exists {
            return cached.(*User), nil
        }
        
        // Load from database
        user, err := db.GetUser(ctx, userID)
        if err != nil {
            return nil, err
        }
        
        // Cache the result
        cache.Set(ctx, key, user, 5*time.Minute)
        return user, nil
    })
    
    if err != nil {
        return nil, err
    }
    
    return result.(*User), nil
}
```

### 3. Cache Warming

```go
func warmCache(ctx context.Context) error {
    // Preload frequently accessed data
    users, err := db.GetPopularUsers(ctx, 100)
    if err != nil {
        return err
    }
    
    for _, user := range users {
        key := fmt.Sprintf("user:%s", user.ID)
        cache.Set(ctx, key, user, 1*time.Hour)
    }
    
    log.Printf("Warmed cache with %d users", len(users))
    return nil
}
```

### 4. Cache Aside Pattern

```go
func GetProduct(ctx context.Context, productID string) (*Product, error) {
    key := fmt.Sprintf("product:%s", productID)
    
    // Try cache first
    if cached, exists, err := cache.Get(ctx, key); err == nil && exists {
        return cached.(*Product), nil
    }
    
    // Load from database
    product, err := db.GetProduct(ctx, productID)
    if err != nil {
        return nil, err
    }
    
    // Cache for next time
    cache.Set(ctx, key, product, 15*time.Minute)
    
    return product, nil
}

func UpdateProduct(ctx context.Context, product *Product) error {
    // Update database
    if err := db.UpdateProduct(ctx, product); err != nil {
        return err
    }
    
    // Invalidate cache
    key := fmt.Sprintf("product:%s", product.ID)
    cache.Delete(ctx, key)
    
    return nil
}
```

### 5. Batch Operations

```go
// Batch get with efficient loading
func GetProducts(ctx context.Context, productIDs []string) (map[string]*Product, error) {
    keys := make([]string, len(productIDs))
    for i, id := range productIDs {
        keys[i] = fmt.Sprintf("product:%s", id)
    }
    
    // Get from cache
    cached, err := cache.GetMany(ctx, keys)
    if err != nil {
        return nil, err
    }
    
    result := make(map[string]*Product)
    var missing []string
    
    // Process cached items
    for i, id := range productIDs {
        if cached[keys[i]] != nil {
            result[id] = cached[keys[i]].(*Product)
        } else {
            missing = append(missing, id)
        }
    }
    
    // Load missing items
    if len(missing) > 0 {
        products, err := db.GetProducts(ctx, missing)
        if err != nil {
            return nil, err
        }
        
        // Cache loaded items
        for _, product := range products {
            key := fmt.Sprintf("product:%s", product.ID)
            cache.Set(ctx, key, product, 15*time.Minute)
            result[product.ID] = product
        }
    }
    
    return result, nil
}
```

### 6. Circuit Breaker Integration

```go
type CacheWithCircuitBreaker struct {
    cache   cache.Cache
    breaker *circuitbreaker.CircuitBreaker
}

func (c *CacheWithCircuitBreaker) Get(ctx context.Context, key string) (interface{}, bool, error) {
    var value interface{}
    var exists bool
    
    err := c.breaker.Call(ctx, func(ctx context.Context) error {
        var err error
        value, exists, err = c.cache.Get(ctx, key)
        return err
    })
    
    if err != nil {
        // Circuit breaker open or call failed
        return nil, false, err
    }
    
    return value, exists, nil
}
```

### 7. Testing Cache

```go
func TestCacheOperations(t *testing.T) {
    ctx := context.Background()
    
    // Use test cache with short TTL
    cfg := cache.Config{
        MaxSize:    100,
        DefaultTTL: 100 * time.Millisecond,
    }
    
    testCache := cache.NewMemoryCache(cfg)
    defer testCache.Close()
    
    // Test set and get
    err := testCache.Set(ctx, "test", "value", 0)
    assert.NoError(t, err)
    
    value, exists, err := testCache.Get(ctx, "test")
    assert.NoError(t, err)
    assert.True(t, exists)
    assert.Equal(t, "value", value)
    
    // Test expiration
    time.Sleep(150 * time.Millisecond)
    
    _, exists, err = testCache.Get(ctx, "test")
    assert.NoError(t, err)
    assert.False(t, exists)
}

// Mock cache for testing
type MockCache struct {
    data map[string]interface{}
    mu   sync.RWMutex
}

func (m *MockCache) Get(ctx context.Context, key string) (interface{}, bool, error) {
    m.mu.RLock()
    defer m.mu.RUnlock()
    
    value, exists := m.data[key]
    return value, exists, nil
}
```

## Examples

### Complete Example: API Response Caching

```go
package main

import (
    "context"
    "encoding/json"
    "fmt"
    "net/http"
    "time"
    
    "github.com/yourusername/commons-go/commons/cache"
)

type APIResponse struct {
    Data      interface{} `json:"data"`
    Timestamp time.Time   `json:"timestamp"`
}

type CachedAPIHandler struct {
    cache   cache.Cache
    client  *http.Client
    baseURL string
}

func NewCachedAPIHandler(baseURL string) *CachedAPIHandler {
    cfg := cache.Config{
        MaxSize:         10000,
        DefaultTTL:      5 * time.Minute,
        CleanupInterval: 1 * time.Minute,
        EvictionPolicy:  cache.EvictionLRU,
    }
    
    return &CachedAPIHandler{
        cache:   cache.NewMemoryCache(cfg),
        client:  &http.Client{Timeout: 10 * time.Second},
        baseURL: baseURL,
    }
}

func (h *CachedAPIHandler) GetData(ctx context.Context, endpoint string) (*APIResponse, error) {
    cacheKey := fmt.Sprintf("api:%s", endpoint)
    
    // Try cache first
    if cached, exists, err := h.cache.Get(ctx, cacheKey); err == nil && exists {
        return cached.(*APIResponse), nil
    }
    
    // Fetch from API
    url := fmt.Sprintf("%s%s", h.baseURL, endpoint)
    req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
    if err != nil {
        return nil, err
    }
    
    resp, err := h.client.Do(req)
    if err != nil {
        return nil, err
    }
    defer resp.Body.Close()
    
    if resp.StatusCode != http.StatusOK {
        return nil, fmt.Errorf("API returned status %d", resp.StatusCode)
    }
    
    var data interface{}
    if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
        return nil, err
    }
    
    response := &APIResponse{
        Data:      data,
        Timestamp: time.Now(),
    }
    
    // Cache the response
    // Use shorter TTL for error responses
    ttl := 5 * time.Minute
    if resp.StatusCode >= 400 {
        ttl = 30 * time.Second
    }
    
    h.cache.Set(ctx, cacheKey, response, ttl)
    
    return response, nil
}

func (h *CachedAPIHandler) InvalidateEndpoint(ctx context.Context, endpoint string) error {
    cacheKey := fmt.Sprintf("api:%s", endpoint)
    return h.cache.Delete(ctx, cacheKey)
}

func (h *CachedAPIHandler) GetCacheStats() cache.Stats {
    return h.cache.Stats()
}

// HTTP handler that uses the cached API
func (h *CachedAPIHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
    ctx := r.Context()
    endpoint := r.URL.Path
    
    // Add cache headers
    w.Header().Set("X-Cache", "MISS")
    
    // Check if response is cached
    cacheKey := fmt.Sprintf("api:%s", endpoint)
    if _, exists, _ := h.cache.Get(ctx, cacheKey); exists {
        w.Header().Set("X-Cache", "HIT")
    }
    
    // Get data (from cache or API)
    response, err := h.GetData(ctx, endpoint)
    if err != nil {
        http.Error(w, err.Error(), http.StatusInternalServerError)
        return
    }
    
    // Add cache info headers
    w.Header().Set("X-Cache-Time", response.Timestamp.Format(time.RFC3339))
    w.Header().Set("Content-Type", "application/json")
    
    json.NewEncoder(w).Encode(response)
}

func main() {
    handler := NewCachedAPIHandler("https://api.example.com")
    
    // Setup routes
    http.Handle("/api/", handler)
    
    // Cache stats endpoint
    http.HandleFunc("/cache/stats", func(w http.ResponseWriter, r *http.Request) {
        stats := handler.GetCacheStats()
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(stats)
    })
    
    // Cache invalidation endpoint
    http.HandleFunc("/cache/invalidate", func(w http.ResponseWriter, r *http.Request) {
        endpoint := r.URL.Query().Get("endpoint")
        if endpoint == "" {
            http.Error(w, "endpoint parameter required", http.StatusBadRequest)
            return
        }
        
        if err := handler.InvalidateEndpoint(r.Context(), endpoint); err != nil {
            http.Error(w, err.Error(), http.StatusInternalServerError)
            return
        }
        
        w.WriteHeader(http.StatusOK)
        fmt.Fprintln(w, "Cache invalidated")
    })
    
    fmt.Println("Server starting on :8080")
    http.ListenAndServe(":8080", nil)
}
```