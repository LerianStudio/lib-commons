// Package tenantmanager provides a client for interacting with the Tenant Manager service.
// It handles tenant-specific database connection retrieval for multi-tenant architectures.
package tenantmanager

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	libCommons "github.com/LerianStudio/lib-commons/v2/commons"
	libLog "github.com/LerianStudio/lib-commons/v2/commons/log"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v2/commons/opentelemetry"
)

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
func WithHTTPClient(client *http.Client) ClientOption {
	return func(c *Client) {
		c.httpClient = client
	}
}

// WithTimeout sets the HTTP client timeout.
func WithTimeout(timeout time.Duration) ClientOption {
	return func(c *Client) {
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
// The API endpoint is: GET {baseURL}/tenants/{tenantID}/settings?service={service}
// Returns the fully resolved tenant configuration with database credentials.
func (c *Client) GetTenantConfig(ctx context.Context, tenantID, service string) (*TenantConfig, error) {
	logger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)
	ctx, span := tracer.Start(ctx, "tenantmanager.client.get_tenant_config")
	defer span.End()

	// Build the URL with service query parameter
	url := fmt.Sprintf("%s/tenants/%s/settings?service=%s", c.baseURL, tenantID, service)

	logger.Infof("Fetching tenant config: tenantID=%s, service=%s", tenantID, service)

	// Create request with context
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		logger.Errorf("Failed to create request: %v", err)
		libOpentelemetry.HandleSpanError(&span, "Failed to create HTTP request", err)
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	// Execute request
	resp, err := c.httpClient.Do(req)
	if err != nil {
		logger.Errorf("Failed to execute request: %v", err)
		libOpentelemetry.HandleSpanError(&span, "HTTP request failed", err)
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body
	body, err := io.ReadAll(resp.Body)
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
