// Package client provides an HTTP client for interacting with the Tenant Manager service.
// It handles tenant-specific database connection retrieval for multi-tenant architectures.
package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
	"unicode/utf8"

	libCommons "github.com/LerianStudio/lib-commons/v4/commons"
	libLog "github.com/LerianStudio/lib-commons/v4/commons/log"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v4/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/cache"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"go.opentelemetry.io/otel/trace"
)

// maxResponseBodySize is the maximum allowed response body size (10 MB).
// This prevents unbounded memory allocation from malicious or malformed responses.
const maxResponseBodySize = 10 * 1024 * 1024

// defaultCacheTTL is the default time-to-live for cached tenant config entries.
const defaultCacheTTL = 1 * time.Hour

// cacheKeyPrefix matches the tenant-manager key format for debugging clarity.
const cacheKeyPrefix = "tenant-settings"

// cbState represents the circuit breaker state.
type cbState int

const (
	// cbClosed is the normal operating state. All requests are allowed through.
	cbClosed cbState = iota
	// cbOpen means the circuit breaker has tripped. Requests fail fast with ErrCircuitBreakerOpen.
	cbOpen
	// cbHalfOpen allows a single test request through to probe whether the service has recovered.
	cbHalfOpen
)

// TenantSummary represents a minimal tenant information for listing.
type TenantSummary struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// Client is an HTTP client for the Tenant Manager service.
// It fetches tenant-specific database configurations from the Tenant Manager API.
// An optional circuit breaker can be enabled via WithCircuitBreaker to fail fast
// when the Tenant Manager service is unresponsive.
type Client struct {
	baseURL       string
	httpClient    *http.Client
	logger        libLog.Logger
	serviceAPIKey string
	cache         cache.ConfigCache
	cacheTTL      time.Duration

	// allowInsecureHTTP permits http:// URLs when set to true.
	// By default, only https:// URLs are accepted unless explicitly opted in
	// via WithAllowInsecureHTTP().
	allowInsecureHTTP bool

	// Circuit breaker fields. When cbThreshold is 0, the circuit breaker is disabled (default).
	cbMu          sync.Mutex
	cbFailures    int
	cbLastFailure time.Time
	cbState       cbState
	cbThreshold   int           // consecutive failures before opening (0 = disabled)
	cbTimeout     time.Duration // how long to stay open before transitioning to half-open
}

// getConfigOpts holds options for a single GetTenantConfig call.
type getConfigOpts struct {
	skipCache bool
}

// GetConfigOption is a functional option for individual GetTenantConfig calls.
type GetConfigOption func(*getConfigOpts)

// WithSkipCache forces GetTenantConfig to bypass the cache and fetch directly.
func WithSkipCache() GetConfigOption {
	return func(o *getConfigOpts) {
		o.skipCache = true
	}
}

// ClientOption is a functional option for configuring the Client.
type ClientOption func(*Client)

// WithHTTPClient sets a custom HTTP client for the Client.
// If client is nil, the option is a no-op (the default HTTP client is preserved).
func WithHTTPClient(client *http.Client) ClientOption {
	return func(c *Client) {
		if client != nil {
			c.httpClient = client
		}
	}
}

// WithTimeout sets the HTTP client timeout.
// If the HTTP client has not been initialized yet, a new default client is created.
func WithTimeout(timeout time.Duration) ClientOption {
	return func(c *Client) {
		if c.httpClient == nil {
			c.httpClient = &http.Client{}
		}

		c.httpClient.Timeout = timeout
	}
}

// WithCircuitBreaker enables the circuit breaker on the Client.
// After threshold consecutive service failures (network errors or HTTP 5xx),
// the circuit breaker opens and subsequent requests fail fast with ErrCircuitBreakerOpen.
// After timeout elapses, one probe request is allowed through (half-open state).
// If the probe succeeds, the circuit breaker closes; if it fails, it reopens.
//
// A threshold of 0 disables the circuit breaker (default behavior).
// HTTP 4xx responses (400, 403, 404) are NOT counted as failures because they
// represent valid responses from the Tenant Manager, not service unavailability.
func WithCircuitBreaker(threshold int, timeout time.Duration) ClientOption {
	return func(c *Client) {
		c.cbThreshold = threshold
		c.cbTimeout = timeout
	}
}

// WithCache sets a custom cache implementation for tenant config responses.
// Returns an error during NewClient if the cache is a typed-nil interface
// (e.g., (*InMemoryCache)(nil)), since that would cause nil-pointer panics.
func WithCache(cc cache.ConfigCache) ClientOption {
	return func(c *Client) {
		if cc != nil {
			c.cache = cc
		}
	}
}

// withCacheValidated is the internal validation that runs during NewClient
// after all options are applied. It detects typed-nil caches.
func withCacheValidated(c *Client) error {
	if c.cache != nil && core.IsNilInterface(c.cache) {
		return fmt.Errorf("client.NewClient: %w", core.ErrNilCache)
	}

	return nil
}

// WithCacheTTL sets the TTL for cached tenant config entries.
func WithCacheTTL(ttl time.Duration) ClientOption {
	return func(c *Client) {
		c.cacheTTL = ttl
	}
}

// WithAllowInsecureHTTP permits the use of http:// (plaintext) URLs for the
// Tenant Manager base URL. By default, only https:// is accepted. Use this
// option only for local development or testing environments.
func WithAllowInsecureHTTP() ClientOption {
	return func(c *Client) {
		c.allowInsecureHTTP = true
	}
}

// WithServiceAPIKey sets the API key sent as X-API-Key header on all HTTP
// requests to the Tenant Manager. The key MUST be non-empty; NewClient returns
// an error if no key is provided or the key is empty. Typically sourced from
// the MULTI_TENANT_SERVICE_API_KEY environment variable.
func WithServiceAPIKey(key string) ClientOption {
	return func(c *Client) {
		c.serviceAPIKey = key
	}
}

// NewClient creates a new Tenant Manager client.
// Parameters:
//   - baseURL: The base URL of the Tenant Manager service (e.g., "https://tenant-manager:8080")
//   - logger: Logger for request/response logging
//   - opts: Optional configuration options
//
// The baseURL is validated at construction time to ensure it is a well-formed URL with a scheme.
// This prevents SSRF risks by ensuring only trusted, pre-configured URLs are used for HTTP requests.
// By default, only https:// URLs are accepted. Use WithAllowInsecureHTTP() to permit http://.
func NewClient(baseURL string, logger libLog.Logger, opts ...ClientOption) (*Client, error) {
	if logger == nil {
		logger = libLog.NewNop()
	}

	// Validate baseURL to ensure it is a well-formed URL with a scheme.
	// This is a defense-in-depth measure: the baseURL is configured at deployment time
	// (not user-controlled), but we validate it to fail fast on misconfiguration.
	parsedURL, err := url.Parse(baseURL)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		logger.Log(context.Background(), libLog.LevelError, "invalid tenant manager baseURL",
			libLog.String("base_url", baseURL),
		)

		return nil, fmt.Errorf("invalid tenant manager baseURL: %q", baseURL)
	}

	c := &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger:   logger,
		cacheTTL: defaultCacheTTL,
	}

	for _, opt := range opts {
		opt(c)
	}

	// Enforce HTTPS by default. Allow http:// only with explicit opt-in.
	if parsedURL.Scheme == "http" && !c.allowInsecureHTTP {
		return nil, fmt.Errorf("client.NewClient: %w: got %q", core.ErrInsecureHTTP, baseURL)
	}

	// Validate that a non-empty service API key was provided.
	if c.serviceAPIKey == "" {
		return nil, fmt.Errorf("client.NewClient: %w", core.ErrServiceAPIKeyRequired)
	}

	// Validate that the cache is not a typed-nil interface.
	if err := withCacheValidated(c); err != nil {
		return nil, err
	}

	if c.cache == nil {
		c.cache = cache.NewInMemoryCache()
	}

	return c, nil
}

// checkCircuitBreaker checks if the circuit breaker allows a request to proceed.
// Returns ErrCircuitBreakerOpen if the circuit breaker is open and the timeout has not elapsed.
// Transitions from open to half-open when the timeout expires.
// When the circuit breaker is disabled (cbThreshold == 0), this is a no-op.
func (c *Client) checkCircuitBreaker() error {
	if c.cbThreshold <= 0 {
		return nil
	}

	c.cbMu.Lock()
	defer c.cbMu.Unlock()

	switch c.cbState {
	case cbOpen:
		if time.Since(c.cbLastFailure) > c.cbTimeout {
			c.cbState = cbHalfOpen
			return nil
		}

		return core.ErrCircuitBreakerOpen
	default:
		return nil
	}
}

// recordSuccess resets the circuit breaker to the closed state with zero failures.
// Called after a successful response from the Tenant Manager.
func (c *Client) recordSuccess() {
	if c.cbThreshold <= 0 {
		return
	}

	c.cbMu.Lock()
	defer c.cbMu.Unlock()

	c.cbFailures = 0
	c.cbState = cbClosed
}

// recordFailure increments the failure counter and opens the circuit breaker
// when the threshold is reached. Only service-level failures (network errors,
// HTTP 5xx) should trigger this - not client errors (4xx).
func (c *Client) recordFailure() {
	if c.cbThreshold <= 0 {
		return
	}

	c.cbMu.Lock()
	defer c.cbMu.Unlock()

	c.cbFailures++
	c.cbLastFailure = time.Now()

	if c.cbFailures >= c.cbThreshold {
		c.cbState = cbOpen
	}
}

// isServerError returns true if the HTTP status code indicates a server-side failure
// that should count toward the circuit breaker threshold.
// Only 5xx status codes are considered failures. 4xx responses (400, 403, 404)
// are valid responses from the Tenant Manager and do NOT indicate service unavailability.
func isServerError(statusCode int) bool {
	return statusCode >= http.StatusInternalServerError
}

// truncateBody returns the body as a string, truncated to maxLen bytes with a
// "...(truncated)" suffix if the body exceeds maxLen. This prevents large
// response bodies from being logged or included in error messages.
// The truncation point is adjusted to the last valid UTF-8 rune boundary
// to avoid splitting multi-byte characters.
func truncateBody(body []byte, maxLen int) string {
	if len(body) <= maxLen {
		return string(body)
	}

	// Find the last valid rune boundary at or before maxLen to avoid
	// splitting multi-byte UTF-8 sequences.
	truncated := body[:maxLen]
	for len(truncated) > 0 && !utf8.Valid(truncated) {
		truncated = truncated[:len(truncated)-1]
	}

	return string(truncated) + "...(truncated)"
}

func (c *Client) getCachedTenantConfig(ctx context.Context, cacheKey, tenantID, service string) (*core.TenantConfig, bool) {
	if c.cache == nil {
		return nil, false
	}

	cached, err := c.cache.Get(ctx, cacheKey)
	if err != nil {
		return nil, false
	}

	var config core.TenantConfig
	if jsonErr := json.Unmarshal([]byte(cached), &config); jsonErr == nil {
		c.logger.Log(ctx, libLog.LevelDebug, "tenant config cache hit",
			libLog.String("tenant_id", tenantID),
			libLog.String("service", service),
		)

		return &config, true
	}

	// Malformed cache entry: evict before refetching to prevent repeated
	// deserialization failures on the same corrupt data.
	c.logger.Log(ctx, libLog.LevelWarn, "invalid tenant config cache entry; evicting before refetch",
		libLog.String("tenant_id", tenantID),
		libLog.String("service", service),
	)

	_ = c.cache.Del(ctx, cacheKey)

	return nil, false
}

func (c *Client) handleGetTenantConfigStatus(
	ctx context.Context,
	span trace.Span,
	tenantID, service string,
	statusCode int,
	body []byte,
) error {
	switch statusCode {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		c.recordSuccess()
		c.logger.Log(ctx, libLog.LevelWarn, "tenant not found",
			libLog.String("tenant_id", tenantID),
			libLog.String("service", service),
		)
		libOpentelemetry.HandleSpanBusinessErrorEvent(span, "Tenant not found", core.ErrTenantNotFound)

		return core.ErrTenantNotFound
	case http.StatusForbidden:
		c.recordSuccess()
		c.logger.Log(ctx, libLog.LevelWarn, "tenant service access denied",
			libLog.String("tenant_id", tenantID),
			libLog.String("service", service),
		)
		libOpentelemetry.HandleSpanBusinessErrorEvent(span, "Tenant service suspended or purged", core.ErrTenantServiceAccessDenied)

		// All 403 responses wrap ErrTenantServiceAccessDenied so callers can
		// use errors.Is(err, core.ErrTenantServiceAccessDenied) reliably.
		// When the JSON body includes a status field, we enrich the error
		// with a TenantSuspendedError for more specific handling.
		var errResp struct {
			Code   string `json:"code"`
			Error  string `json:"error"`
			Status string `json:"status"`
		}

		if jsonErr := json.Unmarshal(body, &errResp); jsonErr == nil && errResp.Status != "" {
			return fmt.Errorf("%w: %w", core.ErrTenantServiceAccessDenied, &core.TenantSuspendedError{
				TenantID: tenantID,
				Status:   errResp.Status,
				Message:  errResp.Error,
			})
		}

		// Non-JSON or missing status: still wrap ErrTenantServiceAccessDenied
		return fmt.Errorf("tenant %s: %w", tenantID, core.ErrTenantServiceAccessDenied)
	default:
		if isServerError(statusCode) {
			c.recordFailure()
		}

		c.logger.Log(ctx, libLog.LevelError, "tenant manager returned error",
			libLog.Int("status", statusCode),
			libLog.String("body", truncateBody(body, 512)),
		)
		libOpentelemetry.HandleSpanError(span, "Tenant Manager returned error", fmt.Errorf("status %d", statusCode))

		return fmt.Errorf("tenant manager returned status %d for tenant %s", statusCode, tenantID)
	}
}

func (c *Client) cacheTenantConfig(ctx context.Context, cacheKey string, config *core.TenantConfig) {
	if c.cache == nil {
		return
	}

	if configJSON, marshalErr := json.Marshal(config); marshalErr == nil {
		_ = c.cache.Set(ctx, cacheKey, string(configJSON), c.cacheTTL)
	}
}

// GetTenantConfig fetches tenant configuration from the Tenant Manager API.
// The API endpoint is: GET {baseURL}/tenants/{tenantID}/services/{service}/settings.
// Successful responses are cached unless WithSkipCache is used.
func (c *Client) GetTenantConfig(ctx context.Context, tenantID, service string, opts ...GetConfigOption) (*core.TenantConfig, error) {
	if c.httpClient == nil {
		c.httpClient = &http.Client{Timeout: 30 * time.Second}
	}

	logger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)

	ctx, span := tracer.Start(ctx, "tenantmanager.client.get_tenant_config")
	defer span.End()

	callOpts := &getConfigOpts{}
	for _, opt := range opts {
		opt(callOpts)
	}

	cacheKey := fmt.Sprintf("%s:%s:%s", cacheKeyPrefix, tenantID, service)
	if !callOpts.skipCache {
		if cachedConfig, ok := c.getCachedTenantConfig(ctx, cacheKey, tenantID, service); ok {
			return cachedConfig, nil
		}
	}

	// Check circuit breaker before making the HTTP request
	if err := c.checkCircuitBreaker(); err != nil {
		logger.Log(ctx, libLog.LevelWarn, "circuit breaker open, failing fast",
			libLog.String("tenant_id", tenantID),
			libLog.String("service", service),
		)
		libOpentelemetry.HandleSpanBusinessErrorEvent(span, "Circuit breaker open", err)

		return nil, err
	}

	// Build the URL with properly escaped path parameters to prevent path traversal
	requestURL := fmt.Sprintf("%s/tenants/%s/services/%s/settings",
		c.baseURL, url.PathEscape(tenantID), url.PathEscape(service))

	logger.Log(ctx, libLog.LevelInfo, "fetching tenant config",
		libLog.String("tenant_id", tenantID),
		libLog.String("service", service),
	)

	// Create request with context
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		logger.Log(ctx, libLog.LevelError, "failed to create request", libLog.Err(err))
		libOpentelemetry.HandleSpanError(span, "Failed to create HTTP request", err)

		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	if c.serviceAPIKey != "" {
		req.Header.Set("X-API-Key", c.serviceAPIKey)
	}

	// Inject trace context into outgoing HTTP headers for distributed tracing
	libOpentelemetry.InjectHTTPContext(ctx, req.Header)

	// Execute request
	// #nosec G704 -- baseURL is validated at construction time and not user-controlled
	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.recordFailure()
		logger.Log(ctx, libLog.LevelError, "failed to execute request", libLog.Err(err))
		libOpentelemetry.HandleSpanError(span, "HTTP request failed", err)

		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body with size limit to prevent unbounded memory allocation
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
	if err != nil {
		c.recordFailure()
		logger.Log(ctx, libLog.LevelError, "failed to read response body", libLog.Err(err))
		libOpentelemetry.HandleSpanError(span, "Failed to read response body", err)

		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// Check response status
	// 404 and 403 are valid business responses - do NOT count as circuit breaker failures
	if err := c.handleGetTenantConfigStatus(ctx, span, tenantID, service, resp.StatusCode, body); err != nil {
		return nil, err
	}

	// Parse response
	var config core.TenantConfig
	if err := json.Unmarshal(body, &config); err != nil {
		logger.Log(ctx, libLog.LevelError, "failed to parse response", libLog.Err(err))
		libOpentelemetry.HandleSpanError(span, "Failed to parse response", err)

		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	c.recordSuccess()
	logger.Log(ctx, libLog.LevelInfo, "successfully fetched tenant config",
		libLog.String("tenant_id", tenantID),
		libLog.String("slug", config.TenantSlug),
	)

	c.cacheTenantConfig(ctx, cacheKey, &config)

	return &config, nil
}

// InvalidateConfig removes the cached tenant config for the given tenant and service.
func (c *Client) InvalidateConfig(ctx context.Context, tenantID, service string) error {
	if c.cache == nil {
		return nil
	}

	cacheKey := fmt.Sprintf("%s:%s:%s", cacheKeyPrefix, tenantID, service)

	return c.cache.Del(ctx, cacheKey)
}

// Close releases any resources held by the cache implementation.
func (c *Client) Close() error {
	type closer interface {
		Close() error
	}

	if cc, ok := c.cache.(closer); ok {
		return cc.Close()
	}

	return nil
}

// GetActiveTenantsByService fetches active tenants for a service from Tenant Manager.
// This is used as a fallback when Redis cache is unavailable.
// The API endpoint is: GET {baseURL}/tenants/active?service={service}
func (c *Client) GetActiveTenantsByService(ctx context.Context, service string) ([]*TenantSummary, error) {
	if c.httpClient == nil {
		c.httpClient = &http.Client{Timeout: 30 * time.Second}
	}

	logger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)

	ctx, span := tracer.Start(ctx, "tenantmanager.client.get_active_tenants")
	defer span.End()

	// Check circuit breaker before making the HTTP request
	if err := c.checkCircuitBreaker(); err != nil {
		logger.Log(ctx, libLog.LevelWarn, "circuit breaker open, failing fast", libLog.String("service", service))
		libOpentelemetry.HandleSpanBusinessErrorEvent(span, "Circuit breaker open", err)

		return nil, err
	}

	// Build the URL with properly escaped query parameter to prevent injection

	requestURL := fmt.Sprintf("%s/tenants/active?service=%s", c.baseURL, url.QueryEscape(service))

	logger.Log(ctx, libLog.LevelInfo, "fetching active tenants", libLog.String("service", service))

	// Create request with context
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		logger.Log(ctx, libLog.LevelError, "failed to create request", libLog.Err(err))
		libOpentelemetry.HandleSpanError(span, "Failed to create HTTP request", err)

		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	if c.serviceAPIKey != "" {
		req.Header.Set("X-API-Key", c.serviceAPIKey)
	}

	// Inject trace context into outgoing HTTP headers for distributed tracing
	libOpentelemetry.InjectHTTPContext(ctx, req.Header)

	// Execute request
	// #nosec G704 -- baseURL is validated at construction time and not user-controlled
	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.recordFailure()
		logger.Log(ctx, libLog.LevelError, "failed to execute request", libLog.Err(err))
		libOpentelemetry.HandleSpanError(span, "HTTP request failed", err)

		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body with size limit to prevent unbounded memory allocation
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
	if err != nil {
		c.recordFailure()
		logger.Log(ctx, libLog.LevelError, "failed to read response body", libLog.Err(err))
		libOpentelemetry.HandleSpanError(span, "Failed to read response body", err)

		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// Check response status
	if resp.StatusCode != http.StatusOK {
		// Only record failure for server errors (5xx), not client errors (4xx)
		if isServerError(resp.StatusCode) {
			c.recordFailure()
		}

		logger.Log(ctx, libLog.LevelError, "tenant manager returned error",
			libLog.Int("status", resp.StatusCode),
			libLog.String("body", truncateBody(body, 512)),
		)
		libOpentelemetry.HandleSpanError(span, "Tenant Manager returned error", fmt.Errorf("status %d", resp.StatusCode))

		return nil, fmt.Errorf("tenant manager returned status %d for service %s", resp.StatusCode, service)
	}

	// Parse response
	var tenants []*TenantSummary
	if err := json.Unmarshal(body, &tenants); err != nil {
		logger.Log(ctx, libLog.LevelError, "failed to parse response", libLog.Err(err))
		libOpentelemetry.HandleSpanError(span, "Failed to parse response", err)

		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	c.recordSuccess()
	logger.Log(ctx, libLog.LevelInfo, "successfully fetched active tenants",
		libLog.Int("count", len(tenants)),
		libLog.String("service", service),
	)

	return tenants, nil
}
