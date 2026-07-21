package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	observability "github.com/LerianStudio/lib-observability/v2"
	libLog "github.com/LerianStudio/lib-observability/v2/log"
	libOpentelemetry "github.com/LerianStudio/lib-observability/v2/tracing"
)

// GetTenantMetadata fetches a tenant's free-form metadata map from the Tenant
// Manager. The API endpoint is: GET {baseURL}/v1/tenants/{tenantID}.
//
// The returned map is the tenant's metadata as stored on the tenant entity (it
// may be nil or empty); callers read only the keys they need. This is distinct
// from GetTenantConfig, which returns connection configuration from the
// per-service associations endpoint and does NOT carry tenant metadata.
//
// Transport, authentication, circuit-breaker and status-handling semantics
// mirror GetTenantConfig / GetActiveTenantsByService: only server errors (5xx)
// trip the circuit breaker; 4xx responses do not.
func (c *Client) GetTenantMetadata(ctx context.Context, tenantID string) (map[string]string, error) {
	c.httpClientOnce.Do(func() {
		if c.httpClient == nil {
			c.httpClient = newDefaultHTTPClient()
		}
	})

	logger, tracer, _, _ := observability.NewTrackingFromContext(ctx)

	ctx, span := tracer.Start(ctx, "tenantmanager.client.get_tenant_metadata")
	defer span.End()

	// Check circuit breaker before making the HTTP request.
	if err := c.checkCircuitBreaker(); err != nil {
		logger.Log(ctx, libLog.LevelWarn, "circuit breaker open, failing fast", libLog.String("tenant_id", tenantID))
		libOpentelemetry.HandleSpanBusinessErrorEvent(span, "Circuit breaker open", err)

		return nil, err
	}

	// Build the URL with a properly escaped path segment to prevent injection.
	requestURL := fmt.Sprintf("%s/v1/tenants/%s", c.baseURL, url.PathEscape(tenantID))

	logger.Log(ctx, libLog.LevelInfo, "fetching tenant metadata", libLog.String("tenant_id", tenantID))

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

	// Inject trace context into outgoing HTTP headers for distributed tracing.
	libOpentelemetry.InjectHTTPContext(ctx, req.Header)

	// #nosec G107 -- baseURL is validated at construction time and not user-controlled
	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.recordFailure()
		logger.Log(ctx, libLog.LevelError, "failed to execute request", libLog.Err(err))
		libOpentelemetry.HandleSpanError(span, "HTTP request failed", err)

		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	// Read response body with size limit to prevent unbounded memory allocation.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
	if err != nil {
		c.recordFailure()
		logger.Log(ctx, libLog.LevelError, "failed to read response body", libLog.Err(err))
		libOpentelemetry.HandleSpanError(span, "Failed to read response body", err)

		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// Server errors (5xx) count as failures; client errors (4xx) are a valid
		// round-trip and reset the consecutive-failure counter (mirrors
		// GetTenantConfig's 404/403 handling) so a lingering 5xx can't open the
		// breaker after a successful exchange.
		if isServerError(resp.StatusCode) {
			c.recordFailure()
		} else {
			c.recordSuccess()
		}

		logger.Log(ctx, libLog.LevelError, "tenant manager returned error",
			libLog.Int("status", resp.StatusCode),
			libLog.String("body", truncateBody(body)),
		)
		libOpentelemetry.HandleSpanError(span, "Tenant Manager returned error", fmt.Errorf("status %d", resp.StatusCode))

		return nil, fmt.Errorf("tenant manager returned status %d for tenant %s", resp.StatusCode, tenantID)
	}

	// Parse only the metadata field; the tenant entity carries other fields the
	// caller does not need here.
	var parsed struct {
		Metadata map[string]string `json:"metadata"`
	}

	if err := json.Unmarshal(body, &parsed); err != nil {
		logger.Log(ctx, libLog.LevelError, "failed to parse response", libLog.Err(err))
		libOpentelemetry.HandleSpanError(span, "Failed to parse response", err)

		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	c.recordSuccess()
	logger.Log(ctx, libLog.LevelInfo, "successfully fetched tenant metadata",
		libLog.Int("keys", len(parsed.Metadata)),
		libLog.String("tenant_id", tenantID),
	)

	return parsed.Metadata, nil
}
