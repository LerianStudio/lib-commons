package cache

import (
	"context"
	"encoding/gob"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"
)

var (
	// ErrKeyNotFound is returned when a key doesn't exist in the cache
	ErrKeyNotFound = errors.New("key not found")
	// ErrCacheMiss is an alias for ErrKeyNotFound for compatibility
	ErrCacheMiss = ErrKeyNotFound
	// ErrCacheFull is returned when the cache is at capacity
	ErrCacheFull = errors.New("cache is full")
	// ErrInvalidTTL is returned when an invalid TTL is provided
	ErrInvalidTTL = errors.New("invalid TTL")
)

// copyValue copies src to dst using reflection
func copyValue(dst, src interface{}) error {
	dstVal := reflect.ValueOf(dst)
	if dstVal.Kind() != reflect.Ptr {
		return errors.New("destination must be a pointer")
	}

	dstElem := dstVal.Elem()
	srcVal := reflect.ValueOf(src)

	// Handle direct assignment if types match
	if dstElem.Type() == srcVal.Type() {
		dstElem.Set(srcVal)
		return nil
	}

	// Special handling for common type conversions
	switch dstElem.Interface().(type) {
	case string:
		if str, ok := src.(string); ok {
			dstElem.SetString(str)
			return nil
		}
	case int:
		if num, ok := src.(int); ok {
			dstElem.SetInt(int64(num))
			return nil
		}
	case bool:
		if b, ok := src.(bool); ok {
			dstElem.SetBool(b)
			return nil
		}
	}

	// Try to use JSON as intermediate format for complex types
	data, err := json.Marshal(src)
	if err != nil {
		return fmt.Errorf("failed to marshal source: %w", err)
	}

	if err := json.Unmarshal(data, dst); err != nil {
		return fmt.Errorf("failed to unmarshal to destination: %w", err)
	}

	return nil
}

// Cache defines the interface for cache implementations
type Cache interface {
	// Get retrieves a value from the cache
	Get(ctx context.Context, key string, value interface{}) error
	// Set stores a value in the cache with optional TTL
	Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error
	// Delete removes a value from the cache
	Delete(ctx context.Context, key string) error
	// Exists checks if a key exists in the cache
	Exists(ctx context.Context, key string) (bool, error)
	// Clear removes all values from the cache
	Clear(ctx context.Context) error
	// TTL returns the remaining TTL for a key
	TTL(ctx context.Context, key string) (time.Duration, error)
	// Keys returns all keys matching a pattern (* for all)
	Keys(ctx context.Context, pattern string) ([]string, error)
}

// LoadFunc defines a function that loads a value when not in cache
type LoadFunc func(ctx context.Context, key string) (interface{}, time.Duration, error)

// Serializer defines the interface for serializing cache values
type Serializer interface {
	Serialize(value interface{}) ([]byte, error)
	Deserialize(data []byte, value interface{}) error
}

// Metrics defines the interface for cache metrics
type Metrics interface {
	IncrHits(cache string)
	IncrMisses(cache string)
	IncrSets(cache string)
	IncrDeletes(cache string)
	IncrEvictions(cache string)
	ObserveSize(cache string, size int)
}

// EvictionPolicy defines cache eviction strategies
type EvictionPolicy string

const (
	// EvictionLRU removes least recently used items
	EvictionLRU EvictionPolicy = "lru"
	// EvictionLFU removes least frequently used items
	EvictionLFU EvictionPolicy = "lfu"
	// EvictionRandom removes random items
	EvictionRandom EvictionPolicy = "random"
)

// cacheEntry represents an entry in the memory cache
type cacheEntry struct {
	value      interface{}
	expiration time.Time
	hits       int
	lastAccess time.Time
}

// MemoryCache is an in-memory cache implementation
type MemoryCache struct {
	mu              sync.RWMutex
	data            map[string]*cacheEntry
	maxSize         int
	evictionPolicy  EvictionPolicy
	metrics         Metrics
	cleanupInterval time.Duration
	stopCleanup     chan struct{}
}

// MemoryCacheOption configures a MemoryCache
type MemoryCacheOption func(*MemoryCache)

// WithMaxSize sets the maximum number of entries
func WithMaxSize(size int) MemoryCacheOption {
	return func(c *MemoryCache) {
		c.maxSize = size
	}
}

// WithEvictionPolicy sets the eviction policy
func WithEvictionPolicy(policy EvictionPolicy) MemoryCacheOption {
	return func(c *MemoryCache) {
		c.evictionPolicy = policy
	}
}

// WithMetrics sets the metrics collector
func WithMetrics(metrics Metrics) MemoryCacheOption {
	return func(c *MemoryCache) {
		c.metrics = metrics
	}
}

// WithCleanupInterval sets the interval for cleaning expired entries
func WithCleanupInterval(interval time.Duration) MemoryCacheOption {
	return func(c *MemoryCache) {
		c.cleanupInterval = interval
	}
}

// NewMemoryCache creates a new in-memory cache
func NewMemoryCache(opts ...MemoryCacheOption) *MemoryCache {
	c := &MemoryCache{
		data:           make(map[string]*cacheEntry),
		maxSize:        1000,
		evictionPolicy: EvictionLRU,
		stopCleanup:    make(chan struct{}),
	}

	for _, opt := range opts {
		opt(c)
	}

	// Start cleanup goroutine if interval is set
	if c.cleanupInterval > 0 {
		go c.startCleanup()
	}

	return c
}

// Get retrieves a value from the cache
func (c *MemoryCache) Get(ctx context.Context, key string, value interface{}) error {
	c.mu.RLock()
	entry, exists := c.data[key]
	c.mu.RUnlock()

	if !exists {
		if c.metrics != nil {
			c.metrics.IncrMisses("memory")
		}
		return ErrKeyNotFound
	}

	// Check expiration
	if !entry.expiration.IsZero() && time.Now().After(entry.expiration) {
		c.mu.Lock()
		delete(c.data, key)
		c.mu.Unlock()
		if c.metrics != nil {
			c.metrics.IncrMisses("memory")
			c.metrics.IncrEvictions("memory")
		}
		return ErrKeyNotFound
	}

	// Update access stats
	c.mu.Lock()
	entry.hits++
	entry.lastAccess = time.Now()
	c.mu.Unlock()

	// Copy value using reflection for generic support
	if err := copyValue(value, entry.value); err != nil {
		return err
	}

	if c.metrics != nil {
		c.metrics.IncrHits("memory")
	}

	return nil
}

// Set stores a value in the cache with optional TTL
func (c *MemoryCache) Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	if ttl < 0 {
		return ErrInvalidTTL
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	// Check if we need to evict
	if c.maxSize > 0 && len(c.data) >= c.maxSize {
		if _, exists := c.data[key]; !exists {
			// Need to evict something
			c.evict()
		}
	}

	entry := &cacheEntry{
		value:      value,
		lastAccess: time.Now(),
	}

	if ttl > 0 {
		entry.expiration = time.Now().Add(ttl)
	}

	c.data[key] = entry

	if c.metrics != nil {
		c.metrics.IncrSets("memory")
		c.metrics.ObserveSize("memory", len(c.data))
	}

	return nil
}

// Delete removes a value from the cache
func (c *MemoryCache) Delete(ctx context.Context, key string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	delete(c.data, key)

	if c.metrics != nil {
		c.metrics.IncrDeletes("memory")
		c.metrics.ObserveSize("memory", len(c.data))
	}

	return nil
}

// Exists checks if a key exists in the cache
func (c *MemoryCache) Exists(ctx context.Context, key string) (bool, error) {
	c.mu.RLock()
	entry, exists := c.data[key]
	c.mu.RUnlock()

	if !exists {
		return false, nil
	}

	// Check expiration
	if !entry.expiration.IsZero() && time.Now().After(entry.expiration) {
		c.mu.Lock()
		delete(c.data, key)
		c.mu.Unlock()
		return false, nil
	}

	return true, nil
}

// Clear removes all values from the cache
func (c *MemoryCache) Clear(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.data = make(map[string]*cacheEntry)

	if c.metrics != nil {
		c.metrics.ObserveSize("memory", 0)
	}

	return nil
}

// TTL returns the remaining TTL for a key
func (c *MemoryCache) TTL(ctx context.Context, key string) (time.Duration, error) {
	c.mu.RLock()
	entry, exists := c.data[key]
	c.mu.RUnlock()

	if !exists {
		return 0, ErrKeyNotFound
	}

	if entry.expiration.IsZero() {
		return 0, nil // No expiration
	}

	ttl := time.Until(entry.expiration)
	if ttl < 0 {
		return 0, ErrKeyNotFound
	}

	return ttl, nil
}

// Keys returns all keys matching a pattern (* for all)
func (c *MemoryCache) Keys(ctx context.Context, pattern string) ([]string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	keys := make([]string, 0, len(c.data))
	now := time.Now()

	for key, entry := range c.data {
		// Skip expired entries
		if !entry.expiration.IsZero() && now.After(entry.expiration) {
			continue
		}

		if pattern == "*" {
			keys = append(keys, key)
		} else {
			// Simple pattern matching (just prefix for now)
			if len(pattern) > 0 && pattern[len(pattern)-1] == '*' {
				prefix := pattern[:len(pattern)-1]
				if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
					keys = append(keys, key)
				}
			} else if key == pattern {
				keys = append(keys, key)
			}
		}
	}

	return keys, nil
}

// evict removes an entry based on the eviction policy
func (c *MemoryCache) evict() {
	if len(c.data) == 0 {
		return
	}

	var evictKey string

	switch c.evictionPolicy {
	case EvictionLRU:
		var oldestTime time.Time
		for key, entry := range c.data {
			if evictKey == "" || entry.lastAccess.Before(oldestTime) {
				evictKey = key
				oldestTime = entry.lastAccess
			}
		}
	case EvictionLFU:
		minHits := -1
		for key, entry := range c.data {
			if minHits == -1 || entry.hits < minHits {
				evictKey = key
				minHits = entry.hits
			}
		}
	case EvictionRandom:
		// Just pick the first one we iterate over
		for key := range c.data {
			evictKey = key
			break
		}
	}

	if evictKey != "" {
		delete(c.data, evictKey)
		if c.metrics != nil {
			c.metrics.IncrEvictions("memory")
		}
	}
}

// GetMultiple retrieves multiple values from the cache
func (c *MemoryCache) GetMultiple(ctx context.Context, keys []string) (map[string]interface{}, error) {
	result := make(map[string]interface{})

	c.mu.RLock()
	defer c.mu.RUnlock()

	now := time.Now()
	for _, key := range keys {
		entry, exists := c.data[key]
		if exists && (entry.expiration.IsZero() || now.Before(entry.expiration)) {
			result[key] = entry.value
			if c.metrics != nil {
				c.metrics.IncrHits("memory")
			}
		} else {
			if c.metrics != nil {
				c.metrics.IncrMisses("memory")
			}
		}
	}

	return result, nil
}

// SetMultiple stores multiple values in the cache
func (c *MemoryCache) SetMultiple(ctx context.Context, items map[string]interface{}, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	for key, value := range items {
		// Check if we need to evict
		if c.maxSize > 0 && len(c.data) >= c.maxSize {
			if _, exists := c.data[key]; !exists {
				c.evict()
			}
		}

		entry := &cacheEntry{
			value:      value,
			lastAccess: time.Now(),
		}

		if ttl > 0 {
			entry.expiration = time.Now().Add(ttl)
		}

		c.data[key] = entry

		if c.metrics != nil {
			c.metrics.IncrSets("memory")
		}
	}

	if c.metrics != nil {
		c.metrics.ObserveSize("memory", len(c.data))
	}

	return nil
}

// startCleanup runs the cleanup goroutine
func (c *MemoryCache) startCleanup() {
	ticker := time.NewTicker(c.cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			c.cleanup()
		case <-c.stopCleanup:
			return
		}
	}
}

// cleanup removes expired entries
func (c *MemoryCache) cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	for key, entry := range c.data {
		if !entry.expiration.IsZero() && now.After(entry.expiration) {
			delete(c.data, key)
			if c.metrics != nil {
				c.metrics.IncrEvictions("memory")
			}
		}
	}

	if c.metrics != nil {
		c.metrics.ObserveSize("memory", len(c.data))
	}
}

// Stop stops the cleanup goroutine
func (c *MemoryCache) Stop() {
	if c.cleanupInterval > 0 {
		close(c.stopCleanup)
	}
}

// LoadingCache wraps a cache with automatic loading
type LoadingCache struct {
	cache    Cache
	loadFunc LoadFunc
}

// NewLoadingCache creates a new loading cache
func NewLoadingCache(cache Cache, loadFunc LoadFunc) *LoadingCache {
	return &LoadingCache{
		cache:    cache,
		loadFunc: loadFunc,
	}
}

// Get retrieves a value, loading it if not present
func (lc *LoadingCache) Get(ctx context.Context, key string, value interface{}) error {
	// Try to get from cache first
	err := lc.cache.Get(ctx, key, value)
	if err == nil {
		return nil
	}

	if err != ErrKeyNotFound {
		return err
	}

	// Load value
	loaded, ttl, err := lc.loadFunc(ctx, key)
	if err != nil {
		return err
	}

	// Store in cache
	if err := lc.cache.Set(ctx, key, loaded, ttl); err != nil {
		return err
	}

	// Copy loaded value to output
	if err := copyValue(value, loaded); err != nil {
		return err
	}

	return nil
}

// Set stores a value in the cache
func (lc *LoadingCache) Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	return lc.cache.Set(ctx, key, value, ttl)
}

// Delete removes a value from the cache
func (lc *LoadingCache) Delete(ctx context.Context, key string) error {
	return lc.cache.Delete(ctx, key)
}

// Exists checks if a key exists in the cache
func (lc *LoadingCache) Exists(ctx context.Context, key string) (bool, error) {
	return lc.cache.Exists(ctx, key)
}

// Clear removes all values from the cache
func (lc *LoadingCache) Clear(ctx context.Context) error {
	return lc.cache.Clear(ctx)
}

// TTL returns the remaining TTL for a key
func (lc *LoadingCache) TTL(ctx context.Context, key string) (time.Duration, error) {
	return lc.cache.TTL(ctx, key)
}

// Keys returns all keys matching a pattern
func (lc *LoadingCache) Keys(ctx context.Context, pattern string) ([]string, error) {
	return lc.cache.Keys(ctx, pattern)
}

// Refresh reloads a value in the cache
func (lc *LoadingCache) Refresh(ctx context.Context, key string) error {
	loaded, ttl, err := lc.loadFunc(ctx, key)
	if err != nil {
		return err
	}

	return lc.cache.Set(ctx, key, loaded, ttl)
}

// NamespaceCache wraps a cache with namespace support
type NamespaceCache struct {
	cache     Cache
	namespace string
}

// NewNamespaceCache creates a new namespace cache
func NewNamespaceCache(cache Cache, namespace string) *NamespaceCache {
	return &NamespaceCache{
		cache:     cache,
		namespace: namespace,
	}
}

// nsKey creates a namespaced key
func (nc *NamespaceCache) nsKey(key string) string {
	return nc.namespace + ":" + key
}

// Get retrieves a value from the cache
func (nc *NamespaceCache) Get(ctx context.Context, key string, value interface{}) error {
	return nc.cache.Get(ctx, nc.nsKey(key), value)
}

// Set stores a value in the cache
func (nc *NamespaceCache) Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	return nc.cache.Set(ctx, nc.nsKey(key), value, ttl)
}

// Delete removes a value from the cache
func (nc *NamespaceCache) Delete(ctx context.Context, key string) error {
	return nc.cache.Delete(ctx, nc.nsKey(key))
}

// Exists checks if a key exists in the cache
func (nc *NamespaceCache) Exists(ctx context.Context, key string) (bool, error) {
	return nc.cache.Exists(ctx, nc.nsKey(key))
}

// Clear removes all values from the namespace
func (nc *NamespaceCache) Clear(ctx context.Context) error {
	keys, err := nc.cache.Keys(ctx, nc.namespace+":*")
	if err != nil {
		return err
	}

	for _, key := range keys {
		if err := nc.cache.Delete(ctx, key); err != nil {
			return err
		}
	}

	return nil
}

// TTL returns the remaining TTL for a key
func (nc *NamespaceCache) TTL(ctx context.Context, key string) (time.Duration, error) {
	return nc.cache.TTL(ctx, nc.nsKey(key))
}

// Keys returns all keys matching a pattern in the namespace
func (nc *NamespaceCache) Keys(ctx context.Context, pattern string) ([]string, error) {
	nsPattern := nc.namespace + ":" + pattern
	keys, err := nc.cache.Keys(ctx, nsPattern)
	if err != nil {
		return nil, err
	}

	// Remove namespace prefix from keys
	result := make([]string, len(keys))
	prefix := nc.namespace + ":"
	for i, key := range keys {
		result[i] = key[len(prefix):]
	}

	return result, nil
}

// JSONSerializer serializes values as JSON
type JSONSerializer struct{}

// Serialize converts a value to JSON bytes
func (js *JSONSerializer) Serialize(value interface{}) ([]byte, error) {
	return json.Marshal(value)
}

// Deserialize converts JSON bytes to a value
func (js *JSONSerializer) Deserialize(data []byte, value interface{}) error {
	return json.Unmarshal(data, value)
}

// GobSerializer serializes values using gob encoding
type GobSerializer struct{}

// Serialize converts a value to gob bytes
func (gs *GobSerializer) Serialize(value interface{}) ([]byte, error) {
	var buf []byte
	enc := gob.NewEncoder(&buffer{data: &buf})
	if err := enc.Encode(value); err != nil {
		return nil, err
	}
	return buf, nil
}

// Deserialize converts gob bytes to a value
func (gs *GobSerializer) Deserialize(data []byte, value interface{}) error {
	dec := gob.NewDecoder(&buffer{data: &data, read: true})
	return dec.Decode(value)
}

// buffer is a simple buffer for gob encoding/decoding
type buffer struct {
	data *[]byte
	read bool
	pos  int
}

func (b *buffer) Write(p []byte) (n int, err error) {
	*b.data = append(*b.data, p...)
	return len(p), nil
}

func (b *buffer) Read(p []byte) (n int, err error) {
	if b.pos >= len(*b.data) {
		return 0, errors.New("EOF")
	}
	n = copy(p, (*b.data)[b.pos:])
	b.pos += n
	return n, nil
}

// Distributed cache implementation would go here
// type DistributedCache struct { ... }
