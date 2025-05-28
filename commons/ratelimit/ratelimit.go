package ratelimit

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Limiter is the interface for rate limiters
type Limiter interface {
	// Allow checks if a request is allowed
	Allow() bool
	
	// Wait blocks until a request is allowed or context is cancelled
	Wait(ctx context.Context) error
	
	// Reserve reserves n tokens for future use
	Reserve(n int) Reservation
	
	// Metrics returns current metrics
	Metrics() Metrics
}

// Reservation represents reserved tokens
type Reservation interface {
	// OK returns whether the reservation succeeded
	OK() bool
	
	// Cancel returns the reserved tokens
	Cancel()
	
	// Delay returns how long to wait before using the reservation
	Delay() time.Duration
}

// Metrics contains rate limiter statistics
type Metrics struct {
	Allowed int64
	Denied  int64
	Total   int64
}

// TokenBucket implements token bucket algorithm
type TokenBucket struct {
	capacity  float64
	tokens    float64
	refillRate float64
	lastRefill time.Time
	mu        sync.Mutex
	
	allowed atomic.Int64
	denied  atomic.Int64
}

// NewTokenBucket creates a new token bucket limiter
func NewTokenBucket(capacity float64, refillRate float64) *TokenBucket {
	return &TokenBucket{
		capacity:   capacity,
		tokens:     capacity,
		refillRate: refillRate,
		lastRefill: time.Now(),
	}
}

// Allow implements Limiter
func (tb *TokenBucket) Allow() bool {
	return tb.AllowN(1)
}

// AllowN checks if n tokens are available
func (tb *TokenBucket) AllowN(n float64) bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	
	tb.refill()
	
	if tb.tokens >= n {
		tb.tokens -= n
		tb.allowed.Add(int64(n))
		return true
	}
	
	tb.denied.Add(int64(n))
	return false
}

// refill adds tokens based on time elapsed
func (tb *TokenBucket) refill() {
	now := time.Now()
	elapsed := now.Sub(tb.lastRefill).Seconds()
	tokensToAdd := elapsed * tb.refillRate
	
	tb.tokens = min(tb.tokens+tokensToAdd, tb.capacity)
	tb.lastRefill = now
}

// Wait implements Limiter
func (tb *TokenBucket) Wait(ctx context.Context) error {
	return tb.WaitN(ctx, 1)
}

// WaitN waits for n tokens to be available
func (tb *TokenBucket) WaitN(ctx context.Context, n float64) error {
	// Check if we can proceed immediately
	if tb.AllowN(n) {
		return nil
	}
	
	// Calculate wait time
	tb.mu.Lock()
	tb.refill()
	tokensNeeded := n - tb.tokens
	waitTime := time.Duration(tokensNeeded/tb.refillRate*1000) * time.Millisecond
	tb.mu.Unlock()
	
	// Wait with context
	timer := time.NewTimer(waitTime)
	defer timer.Stop()
	
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		// Try again
		if tb.AllowN(n) {
			return nil
		}
		// Recursively wait for remaining time
		return tb.WaitN(ctx, n)
	}
}

// Reserve implements Limiter
func (tb *TokenBucket) Reserve(n int) Reservation {
	return tb.ReserveN(float64(n))
}

// ReserveN reserves n tokens
func (tb *TokenBucket) ReserveN(n float64) Reservation {
	tb.mu.Lock()
	defer tb.mu.Unlock()
	
	tb.refill()
	
	// Check if reservation is possible
	if n > tb.capacity {
		return &tokenReservation{ok: false}
	}
	
	// Calculate delay if tokens not immediately available
	var delay time.Duration
	if tb.tokens < n {
		tokensNeeded := n - tb.tokens
		delay = time.Duration(tokensNeeded/tb.refillRate*1000) * time.Millisecond
	}
	
	// Reserve the tokens
	tb.tokens -= n
	if tb.tokens < 0 {
		// Will go negative but will refill over time
	}
	
	return &tokenReservation{
		ok:       true,
		tokens:   n,
		limiter:  tb,
		delay:    delay,
		canceled: false,
	}
}

// Metrics implements Limiter
func (tb *TokenBucket) Metrics() Metrics {
	allowed := tb.allowed.Load()
	denied := tb.denied.Load()
	return Metrics{
		Allowed: allowed,
		Denied:  denied,
		Total:   allowed + denied,
	}
}

type tokenReservation struct {
	ok       bool
	tokens   float64
	limiter  *TokenBucket
	delay    time.Duration
	canceled bool
	mu       sync.Mutex
}

func (r *tokenReservation) OK() bool {
	return r.ok
}

func (r *tokenReservation) Cancel() {
	r.mu.Lock()
	defer r.mu.Unlock()
	
	if !r.canceled && r.ok {
		r.limiter.mu.Lock()
		r.limiter.tokens = min(r.limiter.tokens+r.tokens, r.limiter.capacity)
		r.limiter.mu.Unlock()
		r.canceled = true
	}
}

func (r *tokenReservation) Delay() time.Duration {
	return r.delay
}

// SlidingWindow implements sliding window algorithm
type SlidingWindow struct {
	limit     int
	window    time.Duration
	requests  []time.Time
	mu        sync.Mutex
	allowed   atomic.Int64
	denied    atomic.Int64
}

// NewSlidingWindow creates a sliding window limiter
func NewSlidingWindow(limit int, window time.Duration) *SlidingWindow {
	return &SlidingWindow{
		limit:    limit,
		window:   window,
		requests: make([]time.Time, 0, limit),
	}
}

// Allow implements Limiter
func (sw *SlidingWindow) Allow() bool {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	
	now := time.Now()
	cutoff := now.Add(-sw.window)
	
	// Remove old requests
	validRequests := make([]time.Time, 0, len(sw.requests))
	for _, t := range sw.requests {
		if t.After(cutoff) {
			validRequests = append(validRequests, t)
		}
	}
	sw.requests = validRequests
	
	// Check limit
	if len(sw.requests) < sw.limit {
		sw.requests = append(sw.requests, now)
		sw.allowed.Add(1)
		return true
	}
	
	sw.denied.Add(1)
	return false
}

// Wait implements Limiter
func (sw *SlidingWindow) Wait(ctx context.Context) error {
	for {
		if sw.Allow() {
			return nil
		}
		
		// Calculate wait time
		sw.mu.Lock()
		if len(sw.requests) == 0 {
			sw.mu.Unlock()
			continue
		}
		oldestRequest := sw.requests[0]
		waitTime := sw.window - time.Since(oldestRequest) + time.Millisecond
		sw.mu.Unlock()
		
		if waitTime <= 0 {
			continue
		}
		
		timer := time.NewTimer(waitTime)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
			// Try again
		}
	}
}

// Reserve implements Limiter (not supported for sliding window)
func (sw *SlidingWindow) Reserve(n int) Reservation {
	return &tokenReservation{ok: false}
}

// Metrics implements Limiter
func (sw *SlidingWindow) Metrics() Metrics {
	allowed := sw.allowed.Load()
	denied := sw.denied.Load()
	return Metrics{
		Allowed: allowed,
		Denied:  denied,
		Total:   allowed + denied,
	}
}

// FixedWindow implements fixed window algorithm
type FixedWindow struct {
	limit      int
	window     time.Duration
	count      int
	windowStart time.Time
	mu         sync.Mutex
	allowed    atomic.Int64
	denied     atomic.Int64
}

// NewFixedWindow creates a fixed window limiter
func NewFixedWindow(limit int, window time.Duration) *FixedWindow {
	return &FixedWindow{
		limit:       limit,
		window:      window,
		windowStart: time.Now(),
	}
}

// Allow implements Limiter
func (fw *FixedWindow) Allow() bool {
	fw.mu.Lock()
	defer fw.mu.Unlock()
	
	now := time.Now()
	
	// Check if we're in a new window
	if now.Sub(fw.windowStart) >= fw.window {
		fw.count = 0
		fw.windowStart = now
	}
	
	// Check limit
	if fw.count < fw.limit {
		fw.count++
		fw.allowed.Add(1)
		return true
	}
	
	fw.denied.Add(1)
	return false
}

// Wait implements Limiter
func (fw *FixedWindow) Wait(ctx context.Context) error {
	for {
		if fw.Allow() {
			return nil
		}
		
		// Calculate wait time until next window
		fw.mu.Lock()
		waitTime := fw.window - time.Since(fw.windowStart) + time.Millisecond
		fw.mu.Unlock()
		
		if waitTime <= 0 {
			continue
		}
		
		timer := time.NewTimer(waitTime)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
			// Try again
		}
	}
}

// Reserve implements Limiter (not supported for fixed window)
func (fw *FixedWindow) Reserve(n int) Reservation {
	return &tokenReservation{ok: false}
}

// Metrics implements Limiter
func (fw *FixedWindow) Metrics() Metrics {
	allowed := fw.allowed.Load()
	denied := fw.denied.Load()
	return Metrics{
		Allowed: allowed,
		Denied:  denied,
		Total:   allowed + denied,
	}
}

// KeyedLimiter provides per-key rate limiting
type KeyedLimiter struct {
	factory        func(key string) Limiter
	limiters       map[string]*keyedEntry
	mu             sync.RWMutex
	cleanupTicker  *time.Ticker
	stopCh         chan struct{}
	maxIdleTime    time.Duration
}

type keyedEntry struct {
	limiter  Limiter
	lastUsed time.Time
}

// KeyedOption configures KeyedLimiter
type KeyedOption func(*KeyedLimiter)

// WithCleanupInterval sets cleanup interval
func WithCleanupInterval(d time.Duration) KeyedOption {
	return func(kl *KeyedLimiter) {
		kl.cleanupTicker = time.NewTicker(d)
	}
}

// WithMaxIdleTime sets max idle time before cleanup
func WithMaxIdleTime(d time.Duration) KeyedOption {
	return func(kl *KeyedLimiter) {
		kl.maxIdleTime = d
	}
}

// NewKeyedLimiter creates a new keyed limiter
func NewKeyedLimiter(factory func(key string) Limiter, opts ...KeyedOption) *KeyedLimiter {
	kl := &KeyedLimiter{
		factory:     factory,
		limiters:    make(map[string]*keyedEntry),
		stopCh:      make(chan struct{}),
		maxIdleTime: 5 * time.Minute,
	}
	
	for _, opt := range opts {
		opt(kl)
	}
	
	// Start cleanup if configured
	if kl.cleanupTicker != nil {
		go kl.cleanup()
	}
	
	return kl
}

// getLimiter gets or creates limiter for key
func (kl *KeyedLimiter) getLimiter(key string) Limiter {
	// Try read lock first
	kl.mu.RLock()
	if entry, ok := kl.limiters[key]; ok {
		entry.lastUsed = time.Now()
		kl.mu.RUnlock()
		return entry.limiter
	}
	kl.mu.RUnlock()
	
	// Need write lock to create
	kl.mu.Lock()
	defer kl.mu.Unlock()
	
	// Double check
	if entry, ok := kl.limiters[key]; ok {
		entry.lastUsed = time.Now()
		return entry.limiter
	}
	
	// Create new limiter
	limiter := kl.factory(key)
	kl.limiters[key] = &keyedEntry{
		limiter:  limiter,
		lastUsed: time.Now(),
	}
	
	return limiter
}

// Allow checks if request is allowed for key
func (kl *KeyedLimiter) Allow(key string) bool {
	return kl.getLimiter(key).Allow()
}

// Wait waits until request is allowed for key
func (kl *KeyedLimiter) Wait(ctx context.Context, key string) error {
	return kl.getLimiter(key).Wait(ctx)
}

// cleanup removes idle limiters
func (kl *KeyedLimiter) cleanup() {
	for {
		select {
		case <-kl.stopCh:
			return
		case <-kl.cleanupTicker.C:
			kl.mu.Lock()
			now := time.Now()
			for key, entry := range kl.limiters {
				if now.Sub(entry.lastUsed) > kl.maxIdleTime {
					delete(kl.limiters, key)
				}
			}
			kl.mu.Unlock()
		}
	}
}

// Stop stops the cleanup routine
func (kl *KeyedLimiter) Stop() {
	close(kl.stopCh)
	if kl.cleanupTicker != nil {
		kl.cleanupTicker.Stop()
	}
}

// min returns the minimum of two float64 values
func min(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}