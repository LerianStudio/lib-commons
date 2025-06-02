package security

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gofiber/fiber/v2"
	"go.uber.org/zap"

	"github.com/LerianStudio/lib-commons/commons/log"
)

// SecurityMiddleware provides comprehensive security middleware for HTTP services
type SecurityMiddleware struct {
	config    SecurityConfig
	validator *SecurityValidator
	auditor   *SecurityAuditor
	logger    log.Logger

	// Rate limiting state
	rateLimiter map[string]*RateLimitEntry
	rateMutex   sync.RWMutex

	// Brute force protection
	bruteForceTracker map[string]*BruteForceEntry
	bruteForceMutex   sync.RWMutex
}

// RateLimitEntry tracks rate limiting for IP addresses
type RateLimitEntry struct {
	Requests     int
	LastRequest  time.Time
	Blocked      bool
	BlockedUntil time.Time
}

// BruteForceEntry tracks brute force attempts
type BruteForceEntry struct {
	Attempts     int
	FirstAttempt time.Time
	LastAttempt  time.Time
	Blocked      bool
	BlockedUntil time.Time
}

// SecurityEvent represents a security-related event for auditing
type SecurityEvent struct {
	Timestamp  time.Time              `json:"timestamp"`
	EventType  string                 `json:"event_type"`
	Severity   string                 `json:"severity"`
	Source     string                 `json:"source"`
	UserAgent  string                 `json:"user_agent"`
	IP         string                 `json:"ip"`
	Path       string                 `json:"path"`
	Method     string                 `json:"method"`
	StatusCode int                    `json:"status_code"`
	Message    string                 `json:"message"`
	Metadata   map[string]interface{} `json:"metadata"`
}

// NewSecurityMiddleware creates a new security middleware instance
func NewSecurityMiddleware(config SecurityConfig, logger log.Logger) *SecurityMiddleware {
	return &SecurityMiddleware{
		config:            config,
		validator:         NewSecurityValidator(config),
		auditor:           NewSecurityAuditor(config),
		logger:            logger,
		rateLimiter:       make(map[string]*RateLimitEntry),
		bruteForceTracker: make(map[string]*BruteForceEntry),
	}
}

// SecurityHeaders middleware adds security headers to all responses
func (sm *SecurityMiddleware) SecurityHeaders() fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Content Security Policy
		if sm.config.EnableCSP {
			csp := strings.Join(sm.config.CSPDirectives, "; ")
			c.Set("Content-Security-Policy", csp)
		}

		// HTTP Strict Transport Security
		if sm.config.EnableHSTS {
			hstsValue := fmt.Sprintf("max-age=%d; includeSubDomains; preload", sm.config.HSTSMaxAge)
			c.Set("Strict-Transport-Security", hstsValue)
		}

		// X-Frame-Options
		if sm.config.EnableFrameDeny {
			c.Set("X-Frame-Options", "DENY")
		}

		// X-Content-Type-Options
		if sm.config.EnableContentTypeDeny {
			c.Set("X-Content-Type-Options", "nosniff")
		}

		// Additional security headers
		c.Set("X-XSS-Protection", "1; mode=block")
		c.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		c.Set("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		c.Set("Cross-Origin-Embedder-Policy", "require-corp")
		c.Set("Cross-Origin-Opener-Policy", "same-origin")
		c.Set("Cross-Origin-Resource-Policy", "same-origin")

		return c.Next()
	}
}

// RateLimiting middleware implements rate limiting protection
func (sm *SecurityMiddleware) RateLimiting() fiber.Handler {
	return func(c *fiber.Ctx) error {
		clientIP := c.IP()

		sm.rateMutex.Lock()
		defer sm.rateMutex.Unlock()

		now := time.Now()
		entry, exists := sm.rateLimiter[clientIP]

		if !exists {
			entry = &RateLimitEntry{
				Requests:    1,
				LastRequest: now,
				Blocked:     false,
			}
			sm.rateLimiter[clientIP] = entry
		} else {
			// Check if we're in a new minute window
			if now.Sub(entry.LastRequest) > time.Minute {
				entry.Requests = 1
				entry.LastRequest = now
				entry.Blocked = false
			} else {
				entry.Requests++
				entry.LastRequest = now

				// Check if rate limit exceeded
				if entry.Requests > sm.config.MaxRequestsPerMinute {
					entry.Blocked = true
					entry.BlockedUntil = now.Add(time.Minute)

					// Log security event
					sm.logSecurityEvent(SecurityEvent{
						Timestamp:  now,
						EventType:  "rate_limit_exceeded",
						Severity:   "medium",
						Source:     clientIP,
						UserAgent:  c.Get("User-Agent"),
						IP:         clientIP,
						Path:       c.Path(),
						Method:     c.Method(),
						StatusCode: 429,
						Message:    "Rate limit exceeded",
						Metadata: map[string]interface{}{
							"requests_per_minute": entry.Requests,
							"limit":               sm.config.MaxRequestsPerMinute,
						},
					})

					return c.Status(429).JSON(fiber.Map{
						"error":       "Rate limit exceeded",
						"message":     "Too many requests, please try again later",
						"retry_after": 60,
					})
				}
			}
		}

		// Check if currently blocked
		if entry.Blocked && now.Before(entry.BlockedUntil) {
			return c.Status(429).JSON(fiber.Map{
				"error":       "Rate limit exceeded",
				"message":     "Too many requests, please try again later",
				"retry_after": int(entry.BlockedUntil.Sub(now).Seconds()),
			})
		}

		return c.Next()
	}
}

// InputValidation middleware validates request inputs for security threats
func (sm *SecurityMiddleware) InputValidation() fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Validate Content-Type
		contentType := c.Get("Content-Type")
		if contentType != "" {
			allowed := false
			for _, allowedType := range sm.config.AllowedContentTypes {
				if strings.Contains(contentType, allowedType) {
					allowed = true
					break
				}
			}
			if !allowed {
				sm.logSecurityEvent(SecurityEvent{
					Timestamp:  time.Now(),
					EventType:  "invalid_content_type",
					Severity:   "medium",
					Source:     c.IP(),
					UserAgent:  c.Get("User-Agent"),
					IP:         c.IP(),
					Path:       c.Path(),
					Method:     c.Method(),
					StatusCode: 400,
					Message:    "Invalid content type",
					Metadata: map[string]interface{}{
						"content_type":  contentType,
						"allowed_types": sm.config.AllowedContentTypes,
					},
				})

				return c.Status(400).JSON(fiber.Map{
					"error":   "Invalid content type",
					"message": "Content type not allowed",
				})
			}
		}

		// Validate request size
		if len(c.Body()) > sm.config.MaxInputLength {
			sm.logSecurityEvent(SecurityEvent{
				Timestamp:  time.Now(),
				EventType:  "request_too_large",
				Severity:   "medium",
				Source:     c.IP(),
				UserAgent:  c.Get("User-Agent"),
				IP:         c.IP(),
				Path:       c.Path(),
				Method:     c.Method(),
				StatusCode: 413,
				Message:    "Request entity too large",
				Metadata: map[string]interface{}{
					"request_size": len(c.Body()),
					"max_size":     sm.config.MaxInputLength,
				},
			})

			return c.Status(413).JSON(fiber.Map{
				"error":   "Request too large",
				"message": "Request entity too large",
			})
		}

		// Validate query parameters for injection attacks
		c.Context().QueryArgs().VisitAll(func(key, value []byte) {
			if err := sm.validator.ValidateInput(string(value), "query"); err != nil {
				sm.logSecurityEvent(SecurityEvent{
					Timestamp:  time.Now(),
					EventType:  "malicious_input_detected",
					Severity:   "high",
					Source:     c.IP(),
					UserAgent:  c.Get("User-Agent"),
					IP:         c.IP(),
					Path:       c.Path(),
					Method:     c.Method(),
					StatusCode: 400,
					Message:    "Malicious input detected in query parameters",
					Metadata: map[string]interface{}{
						"parameter": string(key),
						"value":     string(value),
						"error":     err.Error(),
					},
				})
			}
		})

		// Validate headers for suspicious content
		suspiciousHeaders := []string{"X-Forwarded-For", "X-Real-IP", "User-Agent", "Referer"}
		for _, header := range suspiciousHeaders {
			value := c.Get(header)
			if value != "" {
				if err := sm.validator.ValidateInput(value, "header"); err != nil {
					sm.logSecurityEvent(SecurityEvent{
						Timestamp:  time.Now(),
						EventType:  "malicious_header_detected",
						Severity:   "medium",
						Source:     c.IP(),
						UserAgent:  c.Get("User-Agent"),
						IP:         c.IP(),
						Path:       c.Path(),
						Method:     c.Method(),
						StatusCode: 400,
						Message:    "Malicious content detected in headers",
						Metadata: map[string]interface{}{
							"header": header,
							"value":  value,
							"error":  err.Error(),
						},
					})
				}
			}
		}

		return c.Next()
	}
}

// BruteForceProtection middleware protects against brute force attacks
func (sm *SecurityMiddleware) BruteForceProtection() fiber.Handler {
	return func(c *fiber.Ctx) error {
		// Only apply to authentication endpoints
		authEndpoints := []string{"/login", "/auth", "/signin", "/authenticate"}
		isAuthEndpoint := false
		path := strings.ToLower(c.Path())

		for _, endpoint := range authEndpoints {
			if strings.Contains(path, endpoint) {
				isAuthEndpoint = true
				break
			}
		}

		if !isAuthEndpoint {
			return c.Next()
		}

		clientIP := c.IP()
		now := time.Now()

		sm.bruteForceMutex.Lock()
		defer sm.bruteForceMutex.Unlock()

		entry, exists := sm.bruteForceTracker[clientIP]
		if !exists {
			entry = &BruteForceEntry{
				Attempts:     1,
				FirstAttempt: now,
				LastAttempt:  now,
				Blocked:      false,
			}
			sm.bruteForceTracker[clientIP] = entry
		} else {
			// Check if we're outside the brute force window
			if now.Sub(entry.FirstAttempt) > sm.config.BruteForceWindow {
				// Reset the counter
				entry.Attempts = 1
				entry.FirstAttempt = now
				entry.LastAttempt = now
				entry.Blocked = false
			} else {
				entry.Attempts++
				entry.LastAttempt = now

				// Check if threshold exceeded
				if entry.Attempts > sm.config.BruteForceThreshold {
					entry.Blocked = true
					entry.BlockedUntil = now.Add(sm.config.BruteForceBlockTime)

					sm.logSecurityEvent(SecurityEvent{
						Timestamp:  now,
						EventType:  "brute_force_detected",
						Severity:   "high",
						Source:     clientIP,
						UserAgent:  c.Get("User-Agent"),
						IP:         clientIP,
						Path:       c.Path(),
						Method:     c.Method(),
						StatusCode: 429,
						Message:    "Brute force attack detected",
						Metadata: map[string]interface{}{
							"attempts":   entry.Attempts,
							"threshold":  sm.config.BruteForceThreshold,
							"window":     sm.config.BruteForceWindow.String(),
							"block_time": sm.config.BruteForceBlockTime.String(),
						},
					})

					return c.Status(429).JSON(fiber.Map{
						"error":       "Too many authentication attempts",
						"message":     "Account temporarily locked due to suspicious activity",
						"retry_after": int(sm.config.BruteForceBlockTime.Seconds()),
					})
				}
			}
		}

		// Check if currently blocked
		if entry.Blocked && now.Before(entry.BlockedUntil) {
			return c.Status(429).JSON(fiber.Map{
				"error":       "Account temporarily locked",
				"message":     "Too many failed authentication attempts",
				"retry_after": int(entry.BlockedUntil.Sub(now).Seconds()),
			})
		}

		return c.Next()
	}
}

// IPWhitelisting middleware restricts access to allowed IP ranges
func (sm *SecurityMiddleware) IPWhitelisting() fiber.Handler {
	return func(c *fiber.Ctx) error {
		if len(sm.config.AllowedIPRanges) == 0 {
			return c.Next() // No IP restrictions configured
		}

		clientIP := c.IP()
		if err := sm.validator.ValidateIPAddress(clientIP); err != nil {
			sm.logSecurityEvent(SecurityEvent{
				Timestamp:  time.Now(),
				EventType:  "ip_access_denied",
				Severity:   "medium",
				Source:     clientIP,
				UserAgent:  c.Get("User-Agent"),
				IP:         clientIP,
				Path:       c.Path(),
				Method:     c.Method(),
				StatusCode: 403,
				Message:    "IP address not in allowed ranges",
				Metadata: map[string]interface{}{
					"client_ip":      clientIP,
					"allowed_ranges": sm.config.AllowedIPRanges,
				},
			})

			return c.Status(403).JSON(fiber.Map{
				"error":   "Access denied",
				"message": "IP address not authorized",
			})
		}

		return c.Next()
	}
}

// SecurityAuditLogging middleware logs security events
func (sm *SecurityMiddleware) SecurityAuditLogging() fiber.Handler {
	return func(c *fiber.Ctx) error {
		start := time.Now()

		// Process request
		err := c.Next()

		// Log security-relevant requests
		statusCode := c.Response().StatusCode()
		duration := time.Since(start)

		// Log failed authentication attempts
		if statusCode == 401 || statusCode == 403 {
			sm.logSecurityEvent(SecurityEvent{
				Timestamp:  start,
				EventType:  "access_denied",
				Severity:   "medium",
				Source:     c.IP(),
				UserAgent:  c.Get("User-Agent"),
				IP:         c.IP(),
				Path:       c.Path(),
				Method:     c.Method(),
				StatusCode: statusCode,
				Message:    "Access denied or authentication failed",
				Metadata: map[string]interface{}{
					"duration_ms":   duration.Milliseconds(),
					"response_size": len(c.Response().Body()),
				},
			})
		}

		// Log suspicious 4xx errors
		if statusCode >= 400 && statusCode < 500 && statusCode != 404 {
			sm.logSecurityEvent(SecurityEvent{
				Timestamp:  start,
				EventType:  "client_error",
				Severity:   "low",
				Source:     c.IP(),
				UserAgent:  c.Get("User-Agent"),
				IP:         c.IP(),
				Path:       c.Path(),
				Method:     c.Method(),
				StatusCode: statusCode,
				Message:    "Client error response",
				Metadata: map[string]interface{}{
					"duration_ms": duration.Milliseconds(),
				},
			})
		}

		// Log server errors
		if statusCode >= 500 {
			sm.logSecurityEvent(SecurityEvent{
				Timestamp:  start,
				EventType:  "server_error",
				Severity:   "high",
				Source:     c.IP(),
				UserAgent:  c.Get("User-Agent"),
				IP:         c.IP(),
				Path:       c.Path(),
				Method:     c.Method(),
				StatusCode: statusCode,
				Message:    "Server error response",
				Metadata: map[string]interface{}{
					"duration_ms": duration.Milliseconds(),
				},
			})
		}

		return err
	}
}

// CleanupExpiredEntries removes expired rate limiting and brute force entries
func (sm *SecurityMiddleware) CleanupExpiredEntries() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			now := time.Now()

			// Cleanup rate limiting entries
			sm.rateMutex.Lock()
			for ip, entry := range sm.rateLimiter {
				if now.Sub(entry.LastRequest) > 10*time.Minute {
					delete(sm.rateLimiter, ip)
				}
			}
			sm.rateMutex.Unlock()

			// Cleanup brute force entries
			sm.bruteForceMutex.Lock()
			for ip, entry := range sm.bruteForceTracker {
				if now.Sub(entry.LastAttempt) > sm.config.BruteForceWindow*2 {
					delete(sm.bruteForceTracker, ip)
				}
			}
			sm.bruteForceMutex.Unlock()
		}
	}
}

// GetSecurityMetrics returns current security metrics
func (sm *SecurityMiddleware) GetSecurityMetrics() map[string]interface{} {
	sm.rateMutex.RLock()
	sm.bruteForceMutex.RLock()
	defer sm.rateMutex.RUnlock()
	defer sm.bruteForceMutex.RUnlock()

	rateLimitedIPs := 0
	bruteForceBlockedIPs := 0

	for _, entry := range sm.rateLimiter {
		if entry.Blocked {
			rateLimitedIPs++
		}
	}

	for _, entry := range sm.bruteForceTracker {
		if entry.Blocked {
			bruteForceBlockedIPs++
		}
	}

	return map[string]interface{}{
		"rate_limited_ips":        rateLimitedIPs,
		"brute_force_blocked_ips": bruteForceBlockedIPs,
		"total_tracked_ips":       len(sm.rateLimiter),
		"auth_attempt_trackers":   len(sm.bruteForceTracker),
		"security_config": map[string]interface{}{
			"max_requests_per_minute": sm.config.MaxRequestsPerMinute,
			"brute_force_threshold":   sm.config.BruteForceThreshold,
			"security_headers_enabled": map[string]bool{
				"csp":   sm.config.EnableCSP,
				"hsts":  sm.config.EnableHSTS,
				"frame": sm.config.EnableFrameDeny,
			},
		},
	}
}

// logSecurityEvent logs security events to the configured logger
func (sm *SecurityMiddleware) logSecurityEvent(event SecurityEvent) {
	if !sm.config.EnableAuditLogging {
		return
	}

	// Check if this event type is enabled
	eventTypeEnabled := false
	for _, enabledType := range sm.config.SecurityEventTypes {
		if enabledType == event.EventType {
			eventTypeEnabled = true
			break
		}
	}

	if !eventTypeEnabled {
		return
	}

	// Log based on severity
	switch event.Severity {
	case "high", "critical":
		sm.logger.Error("Security event detected",
			zap.String("event_type", event.EventType),
			zap.String("severity", event.Severity),
			zap.String("source", event.Source),
			zap.String("ip", event.IP),
			zap.String("path", event.Path),
			zap.String("method", event.Method),
			zap.Int("status_code", event.StatusCode),
			zap.String("message", event.Message),
			zap.Any("metadata", event.Metadata),
		)
	case "medium":
		sm.logger.Warn("Security event detected",
			zap.String("event_type", event.EventType),
			zap.String("severity", event.Severity),
			zap.String("source", event.Source),
			zap.String("ip", event.IP),
			zap.String("path", event.Path),
			zap.String("method", event.Method),
			zap.Int("status_code", event.StatusCode),
			zap.String("message", event.Message),
			zap.Any("metadata", event.Metadata),
		)
	default:
		sm.logger.Info("Security event detected",
			zap.String("event_type", event.EventType),
			zap.String("severity", event.Severity),
			zap.String("source", event.Source),
			zap.String("ip", event.IP),
			zap.String("path", event.Path),
			zap.String("method", event.Method),
			zap.Int("status_code", event.StatusCode),
			zap.String("message", event.Message),
			zap.Any("metadata", event.Metadata),
		)
	}
}
