// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package tmratelimit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
)

// defaultHTTPClientTimeout is the timeout used when no custom HTTP client is provided.
const defaultHTTPClientTimeout = 10 * time.Second

// maxResponseBodySize is the maximum allowed response body size (10 MB).
// This prevents unbounded memory allocation from malicious or malformed responses.
const maxResponseBodySize = 10 * 1024 * 1024

// ClientConfig holds configuration for creating a RateLimitClient.
type ClientConfig struct {
	// BaseURL is the base URL of the tenant-manager service (e.g., "https://tenant-manager:8080").
	BaseURL string

	// APIKey is sent as the X-API-Key header on all requests. Required; NewRateLimitClient
	// returns an error if empty.
	APIKey string

	// Service is the service identifier used in the request path
	// (e.g., "ledger", "transaction").
	Service string

	// HTTPClient is an optional custom HTTP client. If nil, http.DefaultClient is used.
	HTTPClient *http.Client

	// Logger is used for request/response logging. If nil, a no-op logger is used.
	Logger log.Logger

	// AllowInsecureHTTP permits the use of http:// (plaintext) URLs. By default,
	// only https:// is accepted. Use only for local development or testing.
	AllowInsecureHTTP bool
}

// RateLimitClient is an HTTP client for fetching per-tenant rate limit settings
// from the tenant-manager service.
type RateLimitClient struct {
	baseURL    string
	httpClient *http.Client
	apiKey     string
	service    string
	logger     log.Logger
}

// rateLimitResponse is the JSON envelope returned by the tenant-manager
// rate-limit settings endpoint. The tier map is wrapped in a "rateLimit" field.
type rateLimitResponse struct {
	RateLimit core.RateLimitSettings `json:"rateLimit"`
}

// NewRateLimitClient creates a new client for the tenant-manager rate-limit settings endpoint.
//
// The BaseURL is validated at construction time to ensure it is a well-formed URL with
// a scheme and host. By default, only https:// URLs are accepted; set AllowInsecureHTTP
// to permit http:// for local development.
//
// Returns an error if:
//   - BaseURL is missing, malformed, or has no scheme/host.
//   - BaseURL uses http:// without AllowInsecureHTTP.
//   - APIKey is empty.
func NewRateLimitClient(cfg ClientConfig) (*RateLimitClient, error) {
	logger := cfg.Logger
	if logger == nil {
		logger = log.NewNop()
	}

	// Validate BaseURL: must be a well-formed URL with scheme and host.
	parsedURL, err := url.Parse(cfg.BaseURL)
	if err != nil || parsedURL.Scheme == "" || parsedURL.Host == "" {
		logger.Log(context.Background(), log.LevelError, "invalid tenant manager baseURL",
			log.String("base_url", cfg.BaseURL),
		)

		return nil, fmt.Errorf("invalid tenant manager baseURL: %q", cfg.BaseURL)
	}

	// Enforce HTTPS by default. Allow http:// only with explicit opt-in.
	if parsedURL.Scheme == "http" && !cfg.AllowInsecureHTTP {
		return nil, fmt.Errorf("tmratelimit.NewRateLimitClient: %w: got %q", core.ErrInsecureHTTP, cfg.BaseURL)
	}

	// Validate that a non-empty API key was provided.
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, fmt.Errorf("tmratelimit.NewRateLimitClient: %w", core.ErrServiceAPIKeyRequired)
	}

	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: defaultHTTPClientTimeout}
	}

	return &RateLimitClient{
		baseURL:    cfg.BaseURL,
		httpClient: httpClient,
		apiKey:     cfg.APIKey,
		service:    cfg.Service,
		logger:     logger,
	}, nil
}

// FetchSettings fetches the rate limit settings for a tenant from the tenant-manager.
//
// Endpoint: GET {baseURL}/v1/tenants/{tenantID}/associations/{service}/settings/rate-limit
//
// Return values:
//   - (settings, nil)  — 200 OK with valid JSON body.
//   - (nil, nil)       — 404 Not Found; the tenant has no rate limits configured.
//   - (nil, error)     — any other failure (network, 4xx/5xx, invalid JSON, empty tenantID).
func (c *RateLimitClient) FetchSettings(ctx context.Context, tenantID string) (core.RateLimitSettings, error) {
	if strings.TrimSpace(tenantID) == "" {
		return nil, fmt.Errorf("tmratelimit.FetchSettings: tenant ID must not be empty")
	}

	// Build the URL with properly escaped path parameters to prevent path traversal.
	requestURL := fmt.Sprintf("%s/v1/tenants/%s/associations/%s/settings/rate-limit",
		c.baseURL, url.PathEscape(tenantID), url.PathEscape(c.service))

	c.logger.Log(ctx, log.LevelDebug, "fetching rate limit settings",
		log.String("tenant_id", tenantID),
		log.String("service", c.service),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		c.logger.Log(ctx, log.LevelError, "failed to create request", log.Err(err))

		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-API-Key", c.apiKey)

	// #nosec G107 -- baseURL is validated at construction time and not user-controlled
	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.logger.Log(ctx, log.LevelError, "failed to execute request", log.Err(err))

		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body with size limit to prevent unbounded memory allocation.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
	if err != nil {
		c.logger.Log(ctx, log.LevelError, "failed to read response body", log.Err(err))

		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	switch {
	case resp.StatusCode == http.StatusOK:
		// Parse the JSON response into RateLimitSettings.
		var resp rateLimitResponse
		if err := json.Unmarshal(body, &resp); err != nil {
			c.logger.Log(ctx, log.LevelError, "failed to parse rate limit response",
				log.String("tenant_id", tenantID),
				log.Err(err),
			)

			return nil, fmt.Errorf("failed to parse rate limit response: %w", err)
		}

		c.logger.Log(ctx, log.LevelDebug, "successfully fetched rate limit settings",
			log.String("tenant_id", tenantID),
			log.String("service", c.service),
		)

		return resp.RateLimit, nil

	case resp.StatusCode == http.StatusNotFound:
		// 404 is not an error: the tenant simply has no rate limits configured.
		c.logger.Log(ctx, log.LevelDebug, "no rate limit settings found for tenant",
			log.String("tenant_id", tenantID),
			log.String("service", c.service),
		)

		return nil, nil

	default:
		c.logger.Log(ctx, log.LevelError, "tenant manager returned error for rate limit settings",
			log.String("tenant_id", tenantID),
			log.Int("status", resp.StatusCode),
		)

		return nil, fmt.Errorf("tenant manager returned status %d for tenant %s rate limit settings", resp.StatusCode, tenantID)
	}
}
