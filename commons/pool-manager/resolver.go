package poolmanager

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	libLog "github.com/LerianStudio/lib-commons/v2/commons/log"
)

// TenantConfig holds the complete configuration for a tenant.
// This structure matches the response from the Tenant Service API.
type TenantConfig struct {
	ID            string                      `json:"_id"`
	TenantName    string                      `json:"tenant_name"`
	Status        string                      `json:"status"`
	IsolationMode string                      `json:"isolation_mode"`
	Databases     map[string]DatabaseServices `json:"databases,omitempty"`
	Valkey        *ValkeyConfig               `json:"valkey,omitempty"`
	RabbitMQ      *RabbitMQConfig             `json:"rabbitmq,omitempty"`
}

// DatabaseServices holds database configurations for a service.
type DatabaseServices struct {
	PostgreSQL *PostgreSQLConfig `json:"postgresql,omitempty"`
	MongoDB    *MongoDBConfig    `json:"mongodb,omitempty"`
}

// PostgreSQLConfig holds PostgreSQL connection configuration.
type PostgreSQLConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Database string `json:"database"`
	Username string `json:"username"`
	Password string `json:"password"`
	SSLMode  string `json:"sslmode,omitempty"`
}

// MongoDBConfig holds MongoDB connection configuration.
type MongoDBConfig struct {
	URI      string `json:"uri"`
	Database string `json:"database"`
}

// ValkeyConfig holds Valkey (Redis-compatible) connection configuration.
type ValkeyConfig struct {
	Addresses []string `json:"addresses"`
	Password  string   `json:"password"`
	DB        int      `json:"db"`
}

// RabbitMQConfig holds RabbitMQ connection configuration.
type RabbitMQConfig struct {
	URL string `json:"url"`
}

// Resolver defines the interface for resolving tenant configurations.
type Resolver interface {
	// Resolve retrieves the tenant configuration for the given tenant ID.
	// It returns the cached result if available and not expired.
	Resolve(ctx context.Context, tenantID string) (*TenantConfig, error)

	// ResolveWithService retrieves the tenant configuration filtered by service name.
	// It uses the service query parameter to filter the response.
	ResolveWithService(ctx context.Context, tenantID, serviceName string) (*TenantConfig, error)

	// InvalidateCache removes the cached configuration for a specific tenant.
	InvalidateCache(tenantID string)

	// InvalidateCacheAll removes all cached tenant configurations.
	InvalidateCacheAll()
}

// cacheEntry holds a cached tenant configuration with its expiration time.
type cacheEntry struct {
	config    *TenantConfig
	expiresAt time.Time
}

// resolverImpl is the default implementation of the Resolver interface.
type resolverImpl struct {
	serviceURL string
	httpClient *http.Client
	apiKey     string
	cacheTTL   time.Duration
	cache      map[string]*cacheEntry
	mu         sync.RWMutex
	logger     libLog.Logger
}

// ResolverOption is a function that configures a Resolver.
type ResolverOption func(*resolverImpl)

// WithCacheTTL sets the cache TTL for the resolver.
// Default is 24 hours.
func WithCacheTTL(ttl time.Duration) ResolverOption {
	return func(r *resolverImpl) {
		r.cacheTTL = ttl
	}
}

// WithHTTPClient sets a custom HTTP client for the resolver.
func WithHTTPClient(client *http.Client) ResolverOption {
	return func(r *resolverImpl) {
		r.httpClient = client
	}
}

// WithAPIKey sets the API key for authentication with the Tenant Service.
func WithAPIKey(apiKey string) ResolverOption {
	return func(r *resolverImpl) {
		r.apiKey = apiKey
	}
}

// WithResolverLogger sets the logger for the resolver.
func WithResolverLogger(logger libLog.Logger) ResolverOption {
	return func(r *resolverImpl) {
		r.logger = logger
	}
}

// NewResolver creates a new Resolver with the given service URL and options.
func NewResolver(serviceURL string, opts ...ResolverOption) Resolver {
	r := &resolverImpl{
		serviceURL: strings.TrimSuffix(serviceURL, "/"),
		httpClient: &http.Client{Timeout: 30 * time.Second},
		cacheTTL:   24 * time.Hour,
		cache:      make(map[string]*cacheEntry),
	}

	for _, opt := range opts {
		opt(r)
	}

	return r
}

// Resolve retrieves the tenant configuration for the given tenant ID.
func (r *resolverImpl) Resolve(ctx context.Context, tenantID string) (*TenantConfig, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, fmt.Errorf("empty tenant ID")
	}

	// Check cache first
	if config := r.getFromCache(tenantID); config != nil {
		if r.logger != nil {
			r.logger.Infof("Cache hit for tenant %s", tenantID)
		}

		return config, nil
	}

	if r.logger != nil {
		r.logger.Infof("Cache miss for tenant %s, fetching from service", tenantID)
	}

	// Build request URL with URL-encoded path parameter to handle special characters safely
	requestURL := fmt.Sprintf("%s/tenants/%s/config", r.serviceURL, url.PathEscape(tenantID))

	// Fetch from service
	config, err := r.fetchConfig(ctx, requestURL)
	if err != nil {
		if r.logger != nil {
			r.logger.Errorf("Failed to resolve tenant %s: %v", tenantID, err)
		}

		return nil, err
	}

	// Store in cache
	r.setInCache(tenantID, config)

	if r.logger != nil {
		r.logger.Infof("Successfully resolved tenant %s (isolation: %s)", tenantID, config.IsolationMode)
	}

	return config, nil
}

// ResolveWithService retrieves the tenant configuration filtered by service name.
func (r *resolverImpl) ResolveWithService(ctx context.Context, tenantID, serviceName string) (*TenantConfig, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, fmt.Errorf("empty tenant ID")
	}

	if strings.TrimSpace(serviceName) == "" {
		return nil, fmt.Errorf("empty service name")
	}

	// Create cache key that includes service name
	cacheKey := fmt.Sprintf("%s:%s", tenantID, serviceName)

	// Check cache first
	if config := r.getFromCache(cacheKey); config != nil {
		if r.logger != nil {
			r.logger.Infof("Cache hit for tenant %s service %s", tenantID, serviceName)
		}

		return config, nil
	}

	if r.logger != nil {
		r.logger.Infof("Cache miss for tenant %s service %s, fetching from service", tenantID, serviceName)
	}

	// Build request URL with service query parameter
	// URL-encode path and query parameters to handle special characters safely
	requestURL := fmt.Sprintf("%s/tenants/%s/config?service=%s",
		r.serviceURL,
		url.PathEscape(tenantID),
		url.QueryEscape(serviceName))

	// Fetch from service
	config, err := r.fetchConfig(ctx, requestURL)
	if err != nil {
		if r.logger != nil {
			r.logger.Errorf("Failed to resolve tenant %s service %s: %v", tenantID, serviceName, err)
		}

		return nil, err
	}

	// Store in cache
	r.setInCache(cacheKey, config)

	if r.logger != nil {
		r.logger.Infof("Successfully resolved tenant %s service %s (isolation: %s)", tenantID, serviceName, config.IsolationMode)
	}

	return config, nil
}

// InvalidateCache removes the cached configuration for a specific tenant.
func (r *resolverImpl) InvalidateCache(tenantID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Delete exact match
	delete(r.cache, tenantID)

	// Also delete any service-specific cache entries for this tenant
	prefix := tenantID + ":"
	for key := range r.cache {
		if strings.HasPrefix(key, prefix) {
			delete(r.cache, key)
		}
	}

	if r.logger != nil {
		r.logger.Infof("Invalidated cache for tenant %s", tenantID)
	}
}

// InvalidateCacheAll removes all cached tenant configurations.
func (r *resolverImpl) InvalidateCacheAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache = make(map[string]*cacheEntry)

	if r.logger != nil {
		r.logger.Info("Invalidated all cached tenant configurations")
	}
}

// getFromCache retrieves a tenant configuration from the cache if it exists and is not expired.
func (r *resolverImpl) getFromCache(key string) *TenantConfig {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entry, ok := r.cache[key]
	if !ok {
		return nil
	}

	// Check if expired
	if time.Now().After(entry.expiresAt) {
		return nil
	}

	return entry.config
}

// setInCache stores a tenant configuration in the cache.
func (r *resolverImpl) setInCache(key string, config *TenantConfig) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.cache[key] = &cacheEntry{
		config:    config,
		expiresAt: time.Now().Add(r.cacheTTL),
	}
}

// fetchConfig makes an HTTP request to the Tenant Service and returns the configuration.
func (r *resolverImpl) fetchConfig(ctx context.Context, url string) (*TenantConfig, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	// Set headers
	req.Header.Set("Accept", "application/json")
	if r.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+r.apiKey)
	}

	// Execute request
	resp, err := r.httpClient.Do(req)
	if err != nil {
		// Check for context cancellation
		if ctx.Err() != nil {
			return nil, fmt.Errorf("request failed: %w", ctx.Err())
		}
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// Handle HTTP errors
	switch resp.StatusCode {
	case http.StatusOK:
		// Continue to decode response
	case http.StatusNotFound:
		return nil, fmt.Errorf("tenant not found: %w", ErrTenantNotFound)
	case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable:
		return nil, fmt.Errorf("server error: status %d", resp.StatusCode)
	default:
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	// Decode response
	var config TenantConfig
	if err := json.NewDecoder(resp.Body).Decode(&config); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &config, nil
}
