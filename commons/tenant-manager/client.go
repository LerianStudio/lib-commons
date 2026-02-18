// Package tenantmanager provides a client for interacting with the Tenant Manager service.
// It handles tenant-specific database connection retrieval for multi-tenant architectures.
package tenantmanager

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	libCommons "github.com/LerianStudio/lib-commons/v2/commons"
	libLog "github.com/LerianStudio/lib-commons/v2/commons/log"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v2/commons/opentelemetry"
)

// maxResponseBodySize is the maximum allowed response body size (10 MB).
// This prevents unbounded memory allocation from malicious or malformed responses.
const maxResponseBodySize = 10 * 1024 * 1024

// Client is an HTTP client for the Tenant Manager service.
// It fetches tenant-specific database configurations from the Tenant Manager API.
type Client struct {
	baseURL    string
	httpClient *http.Client
	logger     libLog.Logger
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

// NewClient creates a new Tenant Manager client.
// Parameters:
//   - baseURL: The base URL of the Tenant Manager service (e.g., "http://tenant-manager:8080")
//   - logger: Logger for request/response logging
//   - opts: Optional configuration options
func NewClient(baseURL string, logger libLog.Logger, opts ...ClientOption) *Client {
	c := &Client{
		baseURL: baseURL,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		logger: logger,
	}

	for _, opt := range opts {
		opt(c)
	}

	return c
}

// GetTenantConfig fetches tenant configuration from the Tenant Manager API.
// The API endpoint is: GET {baseURL}/tenants/{tenantID}/services/{service}/settings
// Returns the fully resolved tenant configuration with database credentials.
func (c *Client) GetTenantConfig(ctx context.Context, tenantID, service string) (*TenantConfig, error) {
	logger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)
	ctx, span := tracer.Start(ctx, "tenantmanager.client.get_tenant_config")
	defer span.End()

	// Build the URL with properly escaped path parameters to prevent path traversal
	requestURL := fmt.Sprintf("%s/tenants/%s/services/%s/settings",
		c.baseURL, url.PathEscape(tenantID), url.PathEscape(service))

	logger.Infof("Fetching tenant config: tenantID=%s, service=%s", tenantID, service)

	// Create request with context
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		logger.Errorf("Failed to create request: %v", err)
		libOpentelemetry.HandleSpanError(&span, "Failed to create HTTP request", err)
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	// Inject trace context into outgoing HTTP headers for distributed tracing
	libOpentelemetry.InjectHTTPContext(&req.Header, ctx)

	// Execute request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		logger.Errorf("Failed to execute request: %v", err)
		libOpentelemetry.HandleSpanError(&span, "HTTP request failed", err)
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body with size limit to prevent unbounded memory allocation
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
	if err != nil {
		logger.Errorf("Failed to read response body: %v", err)
		libOpentelemetry.HandleSpanError(&span, "Failed to read response body", err)
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// Check response status
	if resp.StatusCode == http.StatusNotFound {
		logger.Warnf("Tenant not found: tenantID=%s, service=%s", tenantID, service)
		libOpentelemetry.HandleSpanBusinessErrorEvent(&span, "Tenant not found", nil)
		return nil, ErrTenantNotFound
	}

	if resp.StatusCode != http.StatusOK {
		logger.Errorf("Tenant Manager returned error: status=%d, body=%s", resp.StatusCode, string(body))
		libOpentelemetry.HandleSpanError(&span, "Tenant Manager returned error", fmt.Errorf("status %d", resp.StatusCode))
		return nil, fmt.Errorf("tenant manager returned status %d: %s", resp.StatusCode, string(body))
	}

	// Parse response
	var config TenantConfig
	if err := json.Unmarshal(body, &config); err != nil {
		logger.Errorf("Failed to parse response: %v", err)
		libOpentelemetry.HandleSpanError(&span, "Failed to parse response", err)
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	logger.Infof("Successfully fetched tenant config: tenantID=%s, slug=%s", tenantID, config.TenantSlug)

	return &config, nil
}

// TenantSummary represents a minimal tenant information for listing.
type TenantSummary struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// GetActiveTenantsByService fetches active tenants for a service from Tenant Manager.
// This is used as a fallback when Redis cache is unavailable.
// The API endpoint is: GET {baseURL}/tenants/active?service={service}
func (c *Client) GetActiveTenantsByService(ctx context.Context, service string) ([]*TenantSummary, error) {
	logger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)
	ctx, span := tracer.Start(ctx, "tenantmanager.client.get_active_tenants")
	defer span.End()

	// Build the URL with properly escaped query parameter to prevent injection
	requestURL := fmt.Sprintf("%s/tenants/active?service=%s", c.baseURL, url.QueryEscape(service))

	logger.Infof("Fetching active tenants: service=%s", service)

	// Create request with context
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		logger.Errorf("Failed to create request: %v", err)
		libOpentelemetry.HandleSpanError(&span, "Failed to create HTTP request", err)
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	// Inject trace context into outgoing HTTP headers for distributed tracing
	libOpentelemetry.InjectHTTPContext(&req.Header, ctx)

	// Execute request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		logger.Errorf("Failed to execute request: %v", err)
		libOpentelemetry.HandleSpanError(&span, "HTTP request failed", err)
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body with size limit to prevent unbounded memory allocation
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
	if err != nil {
		logger.Errorf("Failed to read response body: %v", err)
		libOpentelemetry.HandleSpanError(&span, "Failed to read response body", err)
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	// Check response status
	if resp.StatusCode != http.StatusOK {
		logger.Errorf("Tenant Manager returned error: status=%d, body=%s", resp.StatusCode, string(body))
		libOpentelemetry.HandleSpanError(&span, "Tenant Manager returned error", fmt.Errorf("status %d", resp.StatusCode))
		return nil, fmt.Errorf("tenant manager returned status %d: %s", resp.StatusCode, string(body))
	}

	// Parse response
	var tenants []*TenantSummary
	if err := json.Unmarshal(body, &tenants); err != nil {
		logger.Errorf("Failed to parse response: %v", err)
		libOpentelemetry.HandleSpanError(&span, "Failed to parse response", err)
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	logger.Infof("Successfully fetched %d active tenants for service=%s", len(tenants), service)

	return tenants, nil
}
