package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/LerianStudio/lib-commons/v6/commons/tenant-manager/core"
	observability "github.com/LerianStudio/lib-observability/v2"
	libLog "github.com/LerianStudio/lib-observability/v2/log"
	libOpentelemetry "github.com/LerianStudio/lib-observability/v2/tracing"
	"go.opentelemetry.io/otel/trace"
)

// AssociateServiceRequest is the POST
// /v1/tenants/{tenantID}/associations/{serviceName} payload.
type AssociateServiceRequest struct {
	IsolationMode string `json:"isolationMode"`
	MaxOpenConns  int    `json:"maxOpenConns,omitempty"`
	MaxIdleConns  int    `json:"maxIdleConns,omitempty"`
}

// AssociateServiceResponse is the association record returned by a successful
// associate (or by an idempotent repeat, which returns 409 instead).
type AssociateServiceResponse struct {
	ServiceName string `json:"serviceName"`
	Status      string `json:"status"`
}

// AssociateService associates a service to a tenant via the Tenant Manager's
// POST /v1/tenants/{tenantID}/associations/{serviceName} endpoint, which is
// RBAC-gated (backoffice:tm:associations:write). It mirrors CreateTenant's
// transport contract exactly: circuit-breaker gating, the service-account
// bearer plus X-API-Key headers, a size-limited response body, and
// trace-context injection.
//
// The endpoint is idempotent by identity: a repeat associate returns 409
// Conflict, which this method maps to core.ErrAssociationConflict so the caller
// can converge via errors.Is. A 5xx is recorded as a service failure (feeds the
// circuit breaker); any other 4xx is a valid round-trip that resets the failure
// counter.
func (c *Client) AssociateService(ctx context.Context, tenantID, serviceName string, req AssociateServiceRequest) (*AssociateServiceResponse, error) {
	c.httpClientOnce.Do(func() {
		if c.httpClient == nil {
			c.httpClient = newDefaultHTTPClient()
		}
	})

	logger, tracer, _, _ := observability.NewTrackingFromContext(ctx)

	ctx, span := tracer.Start(ctx, "tenantmanager.client.associate_service")
	defer span.End()

	if err := c.checkCircuitBreaker(); err != nil {
		logger.Log(ctx, libLog.LevelWarn, "circuit breaker open, failing fast")
		libOpentelemetry.HandleSpanBusinessErrorEvent(span, "Circuit breaker open", err)

		return nil, err
	}

	payload, err := json.Marshal(req)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "Failed to marshal request", err)

		return nil, fmt.Errorf("failed to marshal associate service request: %w", err)
	}

	// Path parameters are escaped to defend against path traversal / injection
	// (mirrors GetTenantConfig).
	requestURL := fmt.Sprintf("%s/v1/tenants/%s/associations/%s",
		c.baseURL, url.PathEscape(tenantID), url.PathEscape(serviceName))

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, bytes.NewReader(payload))
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "Failed to create HTTP request", err)

		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/json")

	if c.serviceAPIKey != "" {
		httpReq.Header.Set("X-API-Key", c.serviceAPIKey)
	}

	if c.bearerTokenProvider != nil {
		token, tokenErr := c.bearerTokenProvider(ctx)
		if tokenErr != nil {
			libOpentelemetry.HandleSpanError(span, "Failed to acquire bearer token", tokenErr)

			return nil, fmt.Errorf("acquire service-account token: %w", tokenErr)
		}

		httpReq.Header.Set("Authorization", "Bearer "+token)
	}

	libOpentelemetry.InjectHTTPContext(ctx, httpReq.Header)

	// #nosec G107 -- baseURL is validated at construction time and not user-controlled
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		c.recordFailure()
		libOpentelemetry.HandleSpanError(span, "HTTP request failed", err)

		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
	if err != nil {
		c.recordFailure()
		libOpentelemetry.HandleSpanError(span, "Failed to read response body", err)

		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, c.handleAssociateServiceStatus(ctx, span, logger, resp.StatusCode, body)
	}

	var out AssociateServiceResponse
	if err := json.Unmarshal(body, &out); err != nil {
		libOpentelemetry.HandleSpanError(span, "Failed to parse response", err)

		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	c.recordSuccess()
	logger.Log(ctx, libLog.LevelInfo, "successfully associated service", libLog.String("service", out.ServiceName))

	return &out, nil
}

// handleAssociateServiceStatus maps a non-success associate response to a typed
// error, mirroring handleCreateTenantStatus exactly: a 409 is a conflict
// (ErrAssociationConflict — the association already exists), any other 4xx is a
// valid round-trip carrying the truncated body, and a 5xx is a service failure
// that feeds the circuit breaker. Only 5xx records a failure; 4xx resets the
// consecutive-failure counter. Anything else (202, 3xx, ...) is out-of-contract:
// surfaced as an error without touching the breaker counters.
func (c *Client) handleAssociateServiceStatus(ctx context.Context, span trace.Span, logger libLog.Logger, statusCode int, body []byte) error {
	switch {
	case statusCode == http.StatusConflict:
		c.recordSuccess()
		logger.Log(ctx, libLog.LevelWarn, "service association already exists",
			libLog.String("body", truncateBody(body)),
		)
		libOpentelemetry.HandleSpanBusinessErrorEvent(span, "Association conflict", core.ErrAssociationConflict)

		return fmt.Errorf("%w: %s", core.ErrAssociationConflict, truncateBody(body))
	case isServerError(statusCode):
		c.recordFailure()
		logger.Log(ctx, libLog.LevelError, "tenant manager returned error",
			libLog.Int("status", statusCode),
			libLog.String("body", truncateBody(body)),
		)
		libOpentelemetry.HandleSpanError(span, "Tenant Manager returned error", fmt.Errorf("status %d", statusCode))

		return fmt.Errorf("tenant manager returned status %d for associate service: %s", statusCode, truncateBody(body))
	case statusCode >= http.StatusBadRequest && statusCode < http.StatusInternalServerError:
		// Any other 4xx: a valid round-trip (resets the breaker), surfaced with the
		// truncated body so the caller can diagnose the rejection.
		c.recordSuccess()
		logger.Log(ctx, libLog.LevelError, "tenant manager rejected associate",
			libLog.Int("status", statusCode),
			libLog.String("body", truncateBody(body)),
		)
		libOpentelemetry.HandleSpanBusinessErrorEvent(span, "Tenant Manager rejected associate", fmt.Errorf("status %d", statusCode))

		return fmt.Errorf("tenant manager returned status %d for associate service: %s", statusCode, truncateBody(body))
	default:
		// Out-of-contract status (202, 3xx, ...): neither a business rejection nor a
		// service failure — leave the breaker counters untouched (mirrors
		// handleCreateTenantStatus's default branch).
		logger.Log(ctx, libLog.LevelError, "tenant manager returned unexpected status",
			libLog.Int("status", statusCode),
			libLog.String("body", truncateBody(body)),
		)
		libOpentelemetry.HandleSpanError(span, "Tenant Manager returned unexpected status", fmt.Errorf("status %d", statusCode))

		return fmt.Errorf("tenant manager returned unexpected status %d for associate service: %s", statusCode, truncateBody(body))
	}
}
