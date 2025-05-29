package http

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/LerianStudio/lib-commons/commons/retry"
)

// ClientConfig holds configuration for creating HTTP clients
type ClientConfig struct {
	// TLS Configuration
	TLSConfig *tls.Config

	// Connection timeouts
	Timeout             time.Duration
	DialTimeout         time.Duration
	TLSHandshakeTimeout time.Duration
	IdleConnTimeout     time.Duration

	// Connection limits
	MaxIdleConns        int
	MaxIdleConnsPerHost int
	MaxConnsPerHost     int

	// Security settings
	InsecureSkipVerify    bool   // Only for development/testing - never use in production
	MinTLSVersion         uint16 // Minimum TLS version (default: TLS 1.2)
	CipherSuites          []uint16
	AllowHTTPFallback     bool // Allow fallback to HTTP if HTTPS fails (for internal networks)
	PreferHTTPS           bool // Try HTTPS first, fallback to HTTP if enabled
	SSLVerificationStrict bool // Strict SSL verification (false for internal networks)

	// Proxy settings
	ProxyURL string

	// Retry settings
	EnableRetry bool                // Enable automatic retries
	MaxRetries  int                 // Maximum number of retry attempts
	RetryConfig *retry.JitterConfig // Retry configuration with jitter
}

// DefaultClientConfig returns a configuration with modern defaults
func DefaultClientConfig() *ClientConfig {
	return &ClientConfig{
		// Connection timeouts
		Timeout:             30 * time.Second,
		DialTimeout:         10 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
		IdleConnTimeout:     90 * time.Second,

		// Connection limits
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		MaxConnsPerHost:     10,

		// Security settings - enforce modern TLS but allow fallback for internal networks
		InsecureSkipVerify:    false,
		MinTLSVersion:         tls.VersionTLS12,
		AllowHTTPFallback:     true,  // Allow HTTP fallback for internal networks
		PreferHTTPS:           true,  // Always try HTTPS first
		SSLVerificationStrict: false, // More lenient for internal networks
		CipherSuites: []uint16{
			// TLS 1.3 cipher suites (preferred)
			tls.TLS_AES_128_GCM_SHA256,
			tls.TLS_AES_256_GCM_SHA384,
			tls.TLS_CHACHA20_POLY1305_SHA256,

			// TLS 1.2 cipher suites (secure fallback)
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
		},

		// Retry settings with jitter to prevent thundering herd
		EnableRetry: true,
		MaxRetries:  3,
		RetryConfig: &retry.JitterConfig{
			Type:       retry.EqualJitter,
			BaseDelay:  500 * time.Millisecond,
			MaxDelay:   10 * time.Second,
			Multiplier: 2.0,
		},
	}
}

// InternalNetworkClientConfig returns a configuration optimized for internal networks
func InternalNetworkClientConfig() *ClientConfig {
	config := DefaultClientConfig()

	// Internal network optimizations
	config.AllowHTTPFallback = true      // Allow HTTP fallback for internal services
	config.PreferHTTPS = true            // Try HTTPS first
	config.SSLVerificationStrict = false // Less strict for internal networks
	config.InsecureSkipVerify = true     // Allow self-signed certs in internal networks
	config.MinTLSVersion = tls.VersionTLS12
	config.Timeout = 30 * time.Second

	return config
}

// ProductionClientConfig returns an extra-secure configuration for production use
func ProductionClientConfig() *ClientConfig {
	config := DefaultClientConfig()

	// More restrictive settings for production
	config.MinTLSVersion = tls.VersionTLS13 // Require TLS 1.3 in production
	config.InsecureSkipVerify = false       // Never skip certificate verification
	config.AllowHTTPFallback = false        // No HTTP fallback in production
	config.PreferHTTPS = true               // Always HTTPS in production
	config.SSLVerificationStrict = true     // Strict verification in production
	config.Timeout = 15 * time.Second       // Shorter timeout
	config.DialTimeout = 5 * time.Second
	config.TLSHandshakeTimeout = 5 * time.Second

	return config
}

// HTTPClient wraps an HTTP client with intelligent protocol handling.
// The type name intentionally matches the package name for clarity in external usage.
//
//nolint:revive // Intentional stuttering for external package clarity
type HTTPClient struct {
	httpsClient *http.Client
	httpClient  *http.Client
	config      *ClientConfig
}

// NewHTTPClient creates a client with smart protocol handling
func NewHTTPClient(config *ClientConfig) (*HTTPClient, error) {
	if config == nil {
		config = InternalNetworkClientConfig()
	}

	// Create HTTPS client
	httpsClient, err := newBaseHTTPClient(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create HTTPS client: %w", err)
	}

	// Create HTTP client (for fallback)
	var httpClient *http.Client

	if config.AllowHTTPFallback {
		// Create transport directly without TLS for HTTP fallback
		transport := &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   config.DialTimeout,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:          config.MaxIdleConns,
			MaxIdleConnsPerHost:   config.MaxIdleConnsPerHost,
			MaxConnsPerHost:       config.MaxConnsPerHost,
			IdleConnTimeout:       config.IdleConnTimeout,
			ExpectContinueTimeout: 1 * time.Second,
			ForceAttemptHTTP2:     false, // HTTP/2 not needed for HTTP
			DisableKeepAlives:     false,
			DisableCompression:    false,
		}

		httpClient = &http.Client{
			Transport: transport,
			Timeout:   config.Timeout,
		}
	}

	return &HTTPClient{
		httpsClient: httpsClient,
		httpClient:  httpClient,
		config:      config,
	}, nil
}

// Do performs an HTTP request with intelligent protocol handling
func (c *HTTPClient) Do(req *http.Request) (*http.Response, error) {
	if !c.config.PreferHTTPS {
		// If HTTPS is not preferred, try HTTP directly (for internal networks)
		if c.httpClient != nil {
			return c.httpClient.Do(req)
		}
	}

	// Try HTTPS first
	httpsReq := c.convertToHTTPS(req)
	resp, err := c.httpsClient.Do(httpsReq)

	// If HTTPS succeeds, return it
	if err == nil {
		return resp, nil
	}

	// If HTTPS fails and HTTP fallback is allowed, try HTTP
	if c.config.AllowHTTPFallback && c.httpClient != nil {
		httpReq := c.convertToHTTP(req)
		return c.httpClient.Do(httpReq)
	}

	// Return original HTTPS error if no fallback
	return nil, fmt.Errorf("HTTPS request failed and no HTTP fallback available: %w", err)
}

// DoWithRetry performs an HTTP request with retry logic and jitter
func (c *HTTPClient) DoWithRetry(req *http.Request) (*http.Response, error) {
	if !c.config.EnableRetry {
		return c.Do(req)
	}

	var lastErr error

	var lastResp *http.Response

	for attempt := 1; attempt <= c.config.MaxRetries+1; attempt++ {
		// Clone request for each attempt (in case body needs to be reread)
		reqClone := req.Clone(req.Context())

		resp, err := c.Do(reqClone)

		// Success case
		if !c.isRetryableStatus(resp.StatusCode) {
			return resp, nil
		}

		// Store last response/error
		lastResp = resp
		lastErr = err

		// Don't retry on last attempt
		if attempt > c.config.MaxRetries {
			break
		}

		// Don't retry if not retryable
		if err == nil && resp != nil && !c.isRetryableStatus(resp.StatusCode) {
			return resp, nil
		}

		// Close response body if we're going to retry
		if resp != nil {
			if err := resp.Body.Close(); err != nil {
				// Log the close error but don't fail the retry attempt
				// The original request error is more important
				// TODO: Add proper logging for close errors
				_ = err // Silence linter until proper logging is implemented
			}
		}

		// Calculate delay with jitter
		delay := c.config.RetryConfig.CalculateDelay(attempt)

		// Sleep with context cancellation support
		select {
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(delay):
			// Continue to next attempt
		}
	}

	// Return last response/error
	if lastResp != nil {
		return lastResp, nil
	}

	return nil, lastErr
}

// isRetryableStatus determines if an HTTP status code should be retried
func (c *HTTPClient) isRetryableStatus(statusCode int) bool {
	switch statusCode {
	case 408, // Request Timeout
		429, // Too Many Requests
		502, // Bad Gateway
		503, // Service Unavailable
		504: // Gateway Timeout
		return true
	default:
		return false
	}
}

// convertToHTTPS converts a request URL to HTTPS
func (c *HTTPClient) convertToHTTPS(req *http.Request) *http.Request {
	if req.URL.Scheme == "http" {
		httpsURL := *req.URL
		httpsURL.Scheme = "https"
		// Adjust port if using default HTTP port
		if httpsURL.Port() == "80" {
			httpsURL.Host = strings.Replace(httpsURL.Host, ":80", ":443", 1)
		}

		httpsReq := *req
		httpsReq.URL = &httpsURL

		return &httpsReq
	}

	return req
}

// convertToHTTP converts a request URL to HTTP
func (c *HTTPClient) convertToHTTP(req *http.Request) *http.Request {
	if req.URL.Scheme == "https" {
		httpURL := *req.URL
		httpURL.Scheme = "http"
		// Adjust port if using default HTTPS port
		if httpURL.Port() == "443" {
			httpURL.Host = strings.Replace(httpURL.Host, ":443", ":80", 1)
		}

		httpReq := *req
		httpReq.URL = &httpURL

		return &httpReq
	}

	return req
}

// newBaseHTTPClient creates a new HTTP client with secure defaults
func newBaseHTTPClient(config *ClientConfig) (*http.Client, error) {
	if config == nil {
		config = DefaultClientConfig()
	}

	// Validate security settings (less strict for internal networks)
	if err := validateConfig(config); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	// Create TLS configuration
	tlsConfig := &tls.Config{
		InsecureSkipVerify: config.InsecureSkipVerify,
		MinVersion:         config.MinTLSVersion,
		CipherSuites:       config.CipherSuites,
	}

	// Adjust TLS config for internal networks
	if !config.SSLVerificationStrict {
		// More lenient settings for internal networks
		tlsConfig.InsecureSkipVerify = config.InsecureSkipVerify
	}

	// Use provided TLS config if available
	if config.TLSConfig != nil {
		tlsConfig = config.TLSConfig
		// Override security-critical settings only if strict verification is enabled
		if config.SSLVerificationStrict && !config.InsecureSkipVerify {
			tlsConfig.InsecureSkipVerify = false
		}

		if config.MinTLSVersion > tlsConfig.MinVersion {
			tlsConfig.MinVersion = config.MinTLSVersion
		}
	}

	// Create transport with secure settings
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   config.DialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          config.MaxIdleConns,
		MaxIdleConnsPerHost:   config.MaxIdleConnsPerHost,
		MaxConnsPerHost:       config.MaxConnsPerHost,
		IdleConnTimeout:       config.IdleConnTimeout,
		TLSHandshakeTimeout:   config.TLSHandshakeTimeout,
		ExpectContinueTimeout: 1 * time.Second,
		TLSClientConfig:       tlsConfig,
		ForceAttemptHTTP2:     true,
		DisableKeepAlives:     false,
		DisableCompression:    false,
	}

	// Create and return the client
	client := &http.Client{
		Transport: transport,
		Timeout:   config.Timeout,
	}

	return client, nil
}

// validateConfig validates the configuration (more lenient for internal networks)
func validateConfig(config *ClientConfig) error {
	// Skip InsecureSkipVerify check for internal networks
	if config.SSLVerificationStrict && config.InsecureSkipVerify {
		return fmt.Errorf("InsecureSkipVerify is enabled with strict verification - this is a security risk")
	}

	if config.MinTLSVersion < tls.VersionTLS12 {
		return fmt.Errorf("minimum TLS version %d is below TLS 1.2 (0x0303)", config.MinTLSVersion)
	}

	if config.Timeout <= 0 {
		return fmt.Errorf("timeout must be positive, got %v", config.Timeout)
	}

	if config.DialTimeout <= 0 {
		return fmt.Errorf("dial timeout must be positive, got %v", config.DialTimeout)
	}

	if config.TLSHandshakeTimeout <= 0 {
		return fmt.Errorf("TLS handshake timeout must be positive, got %v", config.TLSHandshakeTimeout)
	}

	return nil
}

// NewHTTPClientForProduction creates a production-ready HTTP client
func NewHTTPClientForProduction() (*http.Client, error) {
	return newBaseHTTPClient(ProductionClientConfig())
}

// NewHTTPClientForInternalNetwork creates an internal network-friendly HTTP client
func NewHTTPClientForInternalNetwork() (*HTTPClient, error) {
	return NewHTTPClient(InternalNetworkClientConfig())
}

// ClientOption allows customization of HTTP clients
type ClientOption func(*ClientConfig) error

// WithTLSConfig sets a custom TLS configuration
func WithTLSConfig(tlsConfig *tls.Config) ClientOption {
	return func(config *ClientConfig) error {
		if tlsConfig == nil {
			return fmt.Errorf("TLS config cannot be nil")
		}

		config.TLSConfig = tlsConfig

		return nil
	}
}

// WithTimeouts sets custom timeout values
func WithTimeouts(timeout, dialTimeout, tlsTimeout time.Duration) ClientOption {
	return func(config *ClientConfig) error {
		if timeout <= 0 || dialTimeout <= 0 || tlsTimeout <= 0 {
			return fmt.Errorf("all timeouts must be positive")
		}

		config.Timeout = timeout
		config.DialTimeout = dialTimeout
		config.TLSHandshakeTimeout = tlsTimeout

		return nil
	}
}

// WithConnectionLimits sets custom connection limits
func WithConnectionLimits(maxIdle, maxIdlePerHost, maxPerHost int) ClientOption {
	return func(config *ClientConfig) error {
		if maxIdle <= 0 || maxIdlePerHost <= 0 || maxPerHost <= 0 {
			return fmt.Errorf("all connection limits must be positive")
		}

		config.MaxIdleConns = maxIdle
		config.MaxIdleConnsPerHost = maxIdlePerHost
		config.MaxConnsPerHost = maxPerHost

		return nil
	}
}

// WithHTTPFallback enables/disables HTTP fallback for internal networks
func WithHTTPFallback(allow bool) ClientOption {
	return func(config *ClientConfig) error {
		config.AllowHTTPFallback = allow
		return nil
	}
}

// WithInternalNetworkMode configures the client for internal network use
func WithInternalNetworkMode() ClientOption {
	return func(config *ClientConfig) error {
		config.AllowHTTPFallback = true
		config.PreferHTTPS = true
		config.SSLVerificationStrict = false
		config.InsecureSkipVerify = true // Allow self-signed certs

		return nil
	}
}

// WithRetryConfig configures retry behavior with jitter
func WithRetryConfig(enabled bool, maxRetries int, jitterConfig *retry.JitterConfig) ClientOption {
	return func(config *ClientConfig) error {
		config.EnableRetry = enabled
		config.MaxRetries = maxRetries

		if jitterConfig != nil {
			config.RetryConfig = jitterConfig
		}

		return nil
	}
}

// WithDefaultRetryJitter enables retry with sensible defaults
func WithDefaultRetryJitter() ClientOption {
	return func(config *ClientConfig) error {
		config.EnableRetry = true
		config.MaxRetries = 3
		config.RetryConfig = &retry.JitterConfig{
			Type:       retry.EqualJitter,
			BaseDelay:  500 * time.Millisecond,
			MaxDelay:   10 * time.Second,
			Multiplier: 2.0,
		}

		return nil
	}
}

// NewHTTPClientWithOptions creates an HTTP client with custom options
func NewHTTPClientWithOptions(opts ...ClientOption) (*http.Client, error) {
	config := DefaultClientConfig()

	for _, opt := range opts {
		if err := opt(config); err != nil {
			return nil, fmt.Errorf("failed to apply option: %w", err)
		}
	}

	return newBaseHTTPClient(config)
}
