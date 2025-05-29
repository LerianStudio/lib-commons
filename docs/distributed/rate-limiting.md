# Rate Limiting

The rate limiting package provides various algorithms to control the rate of requests in your applications, protecting services from being overwhelmed.

## Overview

Supported algorithms:
- Token Bucket
- Sliding Window
- Fixed Window
- Per-key rate limiting with automatic cleanup

## Basic Usage

```go
import (
    "github.com/LerianStudio/lib-commons/commons/ratelimit"
    "time"
)

// Create a token bucket rate limiter
// 100 requests per second with burst of 10
limiter := ratelimit.NewTokenBucket(100, 10)

// Check if request is allowed
if limiter.Allow() {
    // Process request
    processRequest()
} else {
    // Rate limit exceeded
    return errors.New("rate limit exceeded")
}
```

## Rate Limiting Algorithms

### Token Bucket

Best for: Allowing burst traffic while maintaining average rate

```go
// 50 requests per second with burst capacity of 100
limiter := ratelimit.NewTokenBucket(50, 100)

// Check if allowed
if limiter.Allow() {
    handleRequest()
}

// Or wait for token
err := limiter.Wait(ctx)
if err != nil {
    return err
}
handleRequest()

// Check multiple tokens
if limiter.AllowN(5) {
    handleBatchRequest(5)
}
```

### Sliding Window

Best for: Smooth rate limiting without sudden resets

```go
// 1000 requests per minute
limiter := ratelimit.NewSlidingWindow(1000, time.Minute)

if limiter.Allow() {
    processRequest()
}

// Get current usage
count := limiter.Count()
fmt.Printf("Current requests in window: %d\n", count)
```

### Fixed Window

Best for: Simple rate limiting with predictable reset times

```go
// 100 requests per 10 seconds
limiter := ratelimit.NewFixedWindow(100, 10*time.Second)

if limiter.Allow() {
    processRequest()
} else {
    // Get time until reset
    resetTime := limiter.ResetTime()
    waitDuration := time.Until(resetTime)
    return fmt.Errorf("rate limit exceeded, retry after %v", waitDuration)
}
```

## Per-Key Rate Limiting

Rate limit different users/IPs/API keys separately:

```go
// Create a rate limiter manager
manager := ratelimit.NewRateLimiterManager(
    ratelimit.WithDefaultLimit(100, time.Minute),
    ratelimit.WithCleanupInterval(5*time.Minute),
)

// Rate limit by user ID
userID := "user123"
if manager.Allow(userID) {
    handleUserRequest(userID)
} else {
    return errors.New("user rate limit exceeded")
}

// Custom limits for specific keys
manager.SetLimit("premium-user", 1000, time.Minute)
manager.SetLimit("basic-user", 100, time.Minute)

// Remove a limiter
manager.Remove("user123")
```

## HTTP Middleware

### Basic Rate Limiting Middleware

```go
func RateLimitMiddleware(limiter ratelimit.Limiter) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            if !limiter.Allow() {
                http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
                return
            }
            next.ServeHTTP(w, r)
        })
    }
}

// Usage
limiter := ratelimit.NewTokenBucket(100, 10)
handler := RateLimitMiddleware(limiter)(mainHandler)
```

### Per-IP Rate Limiting

```go
func PerIPRateLimitMiddleware(manager *ratelimit.RateLimiterManager) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            ip := getClientIP(r)
            
            if !manager.Allow(ip) {
                w.Header().Set("Retry-After", "60")
                http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
                return
            }
            
            next.ServeHTTP(w, r)
        })
    }
}

func getClientIP(r *http.Request) string {
    // Check X-Forwarded-For header
    if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
        return strings.Split(xff, ",")[0]
    }
    // Check X-Real-IP header
    if xri := r.Header.Get("X-Real-IP"); xri != "" {
        return xri
    }
    // Fall back to RemoteAddr
    return strings.Split(r.RemoteAddr, ":")[0]
}
```

### API Key Rate Limiting

```go
type APIKeyLimiter struct {
    manager *ratelimit.RateLimiterManager
    limits  map[string]RateLimit
}

type RateLimit struct {
    Requests int
    Window   time.Duration
}

func NewAPIKeyLimiter() *APIKeyLimiter {
    return &APIKeyLimiter{
        manager: ratelimit.NewRateLimiterManager(),
        limits: map[string]RateLimit{
            "free":       {Requests: 100, Window: time.Hour},
            "basic":      {Requests: 1000, Window: time.Hour},
            "premium":    {Requests: 10000, Window: time.Hour},
            "enterprise": {Requests: 100000, Window: time.Hour},
        },
    }
}

func (l *APIKeyLimiter) Middleware(next http.Handler) http.Handler {
    return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        apiKey := r.Header.Get("X-API-Key")
        if apiKey == "" {
            http.Error(w, "API key required", http.StatusUnauthorized)
            return
        }
        
        // Get user tier from API key
        tier := getUserTier(apiKey)
        limit := l.limits[tier]
        
        // Set custom limit for this API key
        l.manager.SetLimit(apiKey, limit.Requests, limit.Window)
        
        if !l.manager.Allow(apiKey) {
            w.Header().Set("X-RateLimit-Limit", strconv.Itoa(limit.Requests))
            w.Header().Set("X-RateLimit-Remaining", "0")
            w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Add(limit.Window).Unix(), 10))
            http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
            return
        }
        
        // Add rate limit headers
        remaining := l.manager.Remaining(apiKey)
        w.Header().Set("X-RateLimit-Limit", strconv.Itoa(limit.Requests))
        w.Header().Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
        
        next.ServeHTTP(w, r)
    })
}
```

## Advanced Patterns

### Distributed Rate Limiting with Redis

```go
type RedisRateLimiter struct {
    client redis.Client
    limit  int
    window time.Duration
}

func (r *RedisRateLimiter) Allow(key string) bool {
    ctx := context.Background()
    now := time.Now().Unix()
    windowStart := now - int64(r.window.Seconds())
    
    pipe := r.client.Pipeline()
    
    // Remove old entries
    pipe.ZRemRangeByScore(ctx, key, "0", strconv.FormatInt(windowStart, 10))
    
    // Count current entries
    count := pipe.ZCard(ctx, key)
    
    // Add current request
    pipe.ZAdd(ctx, key, &redis.Z{
        Score:  float64(now),
        Member: now,
    })
    
    // Set expiry
    pipe.Expire(ctx, key, r.window)
    
    _, err := pipe.Exec(ctx)
    if err != nil {
        return false
    }
    
    return count.Val() < int64(r.limit)
}
```

### Rate Limiting with Backpressure

```go
type BackpressureLimiter struct {
    limiter  ratelimit.Limiter
    queue    chan Request
    workers  int
}

func NewBackpressureLimiter(limiter ratelimit.Limiter, queueSize, workers int) *BackpressureLimiter {
    bl := &BackpressureLimiter{
        limiter: limiter,
        queue:   make(chan Request, queueSize),
        workers: workers,
    }
    
    // Start workers
    for i := 0; i < workers; i++ {
        go bl.worker()
    }
    
    return bl
}

func (bl *BackpressureLimiter) Submit(req Request) error {
    select {
    case bl.queue <- req:
        return nil
    default:
        return errors.New("queue full")
    }
}

func (bl *BackpressureLimiter) worker() {
    for req := range bl.queue {
        // Wait for rate limit
        bl.limiter.Wait(context.Background())
        
        // Process request
        processRequest(req)
    }
}
```

### Adaptive Rate Limiting

```go
type AdaptiveRateLimiter struct {
    mu              sync.RWMutex
    baseRate        float64
    currentRate     float64
    errorThreshold  float64
    adjustmentRate  float64
    limiter         *ratelimit.TokenBucket
}

func (a *AdaptiveRateLimiter) RecordResult(success bool) {
    a.mu.Lock()
    defer a.mu.Unlock()
    
    if success {
        // Increase rate on success
        a.currentRate = math.Min(a.currentRate*a.adjustmentRate, a.baseRate*2)
    } else {
        // Decrease rate on failure
        a.currentRate = math.Max(a.currentRate/a.adjustmentRate, a.baseRate*0.1)
    }
    
    // Update limiter
    a.limiter = ratelimit.NewTokenBucket(
        int(a.currentRate),
        int(a.currentRate/10), // 10% burst
    )
}

func (a *AdaptiveRateLimiter) Allow() bool {
    a.mu.RLock()
    limiter := a.limiter
    a.mu.RUnlock()
    
    return limiter.Allow()
}
```

## Monitoring and Metrics

```go
type RateLimitMetrics struct {
    Allowed  int64
    Denied   int64
    Current  int64
}

func (m *RateLimitMetrics) RecordAllowed() {
    atomic.AddInt64(&m.Allowed, 1)
}

func (m *RateLimitMetrics) RecordDenied() {
    atomic.AddInt64(&m.Denied, 1)
}

func (m *RateLimitMetrics) GetStats() map[string]int64 {
    return map[string]int64{
        "allowed": atomic.LoadInt64(&m.Allowed),
        "denied":  atomic.LoadInt64(&m.Denied),
        "current": atomic.LoadInt64(&m.Current),
    }
}
```

## Best Practices

1. **Choose the right algorithm**: Token bucket for bursty traffic, sliding window for smooth limiting
2. **Set appropriate limits**: Based on service capacity and SLAs
3. **Provide clear feedback**: Return proper headers and error messages
4. **Monitor and adjust**: Track metrics and adjust limits as needed
5. **Handle distributed systems**: Use Redis or similar for multi-instance deployments
6. **Graceful degradation**: Queue or shed load appropriately

## Testing

```go
func TestRateLimiter(t *testing.T) {
    limiter := ratelimit.NewTokenBucket(10, 2)
    
    // Should allow first 2 requests (burst)
    assert.True(t, limiter.Allow())
    assert.True(t, limiter.Allow())
    
    // Should deny 3rd request
    assert.False(t, limiter.Allow())
    
    // Wait for refill
    time.Sleep(110 * time.Millisecond) // 10 req/s = 1 req/100ms
    
    // Should allow again
    assert.True(t, limiter.Allow())
}
```