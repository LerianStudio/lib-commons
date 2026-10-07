package client

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"
	obsbridge "github.com/LerianStudio/lib-commons/v7/commons/obs/obsbridge"

	libOpentelemetry "github.com/LerianStudio/lib-observability/v4/tracing"
)

// TenantPrincipal is one party a live credential of a tenant acts for: the
// principal's nature and the party reference its tokens carry in the atua_por
// claim, byte for byte. Nature is empty for a principal that declares none.
type TenantPrincipal struct {
	Nature   string `json:"nature"`
	PartyRef string `json:"partyRef"`
}

// GetTenantPrincipals lists the parties the tenant's active principals act for:
// GET {baseURL}/v1/tenants/{tenantID}/associations/{service}/principals.
// Never cached, so a revocation shows on the next call; statuses map as in GetTenantConfig.
func (c *Client) GetTenantPrincipals(ctx context.Context, tenantID, service string) ([]TenantPrincipal, error) {
	c.httpClientOnce.Do(func() {
		if c.httpClient == nil {
			c.httpClient = newDefaultHTTPClient()
		}
	})

	logger, tracer, _, _ := obsbridge.TrackingFromContext(ctx)

	ctx, span := tracer.Start(ctx, "tenantmanager.client.get_tenant_principals")
	defer span.End()

	if err := c.checkCircuitBreaker(); err != nil {
		logger.Log(ctx, obs.LevelWarn, "circuit breaker open, failing fast", "tenant_id", tenantID, "service", service)
		libOpentelemetry.HandleSpanBusinessErrorEvent(span, "Circuit breaker open", err)

		return nil, err
	}

	requestURL := fmt.Sprintf("%s/v1/tenants/%s/associations/%s/principals",
		c.baseURL, url.PathEscape(tenantID), url.PathEscape(service))

	logger.Log(ctx, obs.LevelDebug, "fetching tenant principals", "tenant_id", tenantID, "service", service)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		logger.Log(ctx, obs.LevelError, "failed to create request", "error", err)
		libOpentelemetry.HandleSpanError(span, "Failed to create HTTP request", err)

		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Accept", "application/json")

	if c.serviceAPIKey != "" {
		req.Header.Set("X-API-Key", c.serviceAPIKey)
	}

	libOpentelemetry.InjectHTTPContext(ctx, req.Header)

	// #nosec G107 -- baseURL is validated at construction time and not user-controlled
	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.recordFailure()
		logger.Log(ctx, obs.LevelError, "failed to execute request", "error", err)
		libOpentelemetry.HandleSpanError(span, "HTTP request failed", err)

		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
	if err != nil {
		c.recordFailure()
		logger.Log(ctx, obs.LevelError, "failed to read response body", "error", err)
		libOpentelemetry.HandleSpanError(span, "Failed to read response body", err)

		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if err := c.handleGetTenantConfigStatus(ctx, span, tenantID, service, resp.StatusCode, body); err != nil {
		return nil, err
	}

	var parsed struct {
		Items []TenantPrincipal `json:"items"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		logger.Log(ctx, obs.LevelError, "failed to parse response", "error", err)
		libOpentelemetry.HandleSpanError(span, "Failed to parse response", err)

		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	c.recordSuccess()
	logger.Log(ctx, obs.LevelDebug, "successfully fetched tenant principals",
		"count", len(parsed.Items), "tenant_id", tenantID, "service", service)

	return parsed.Items, nil
}
