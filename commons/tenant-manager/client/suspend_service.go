package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"

	observability "github.com/LerianStudio/lib-observability/v2"
	libLog "github.com/LerianStudio/lib-observability/v2/log"
	libOpentelemetry "github.com/LerianStudio/lib-observability/v2/tracing"
	"go.opentelemetry.io/otel/trace"
)

// SuspendService suspends a tenant's service association via the Tenant
// Manager's PUT /v1/tenants/{tenantID}/associations/{serviceName}/suspend
// endpoint, which is RBAC-gated (backoffice:tm:associations:write). It mirrors
// CreateTenant's transport contract (circuit-breaker gating, service-account
// bearer plus X-API-Key, size-limited body, trace-context injection) but sends
// no request body.
//
// The transition is idempotent: suspending an already-suspended association
// returns 409, which this method tolerates as success. A 5xx is recorded as a
// service failure (feeds the circuit breaker); any other 4xx is a valid
// round-trip that resets the failure counter.
func (c *Client) SuspendService(ctx context.Context, tenantID, serviceName string) error {
	c.httpClientOnce.Do(func() {
		if c.httpClient == nil {
			c.httpClient = newDefaultHTTPClient()
		}
	})

	logger, tracer, _, _ := observability.NewTrackingFromContext(ctx)

	ctx, span := tracer.Start(ctx, "tenantmanager.client.suspend_service")
	defer span.End()

	// Path parameters are escaped to defend against path traversal / injection
	// (mirrors GetTenantConfig).
	requestURL := fmt.Sprintf("%s/v1/tenants/%s/associations/%s/suspend",
		c.baseURL, url.PathEscape(tenantID), url.PathEscape(serviceName))

	return c.doStateTransition(ctx, span, logger, requestURL, "suspend service")
}

// doStateTransition performs a body-less, idempotent PUT state transition
// (SuspendService, ReactivateTenant) against requestURL, applying CreateTenant's
// transport contract without a request body: circuit-breaker gating, the
// service-account bearer plus X-API-Key headers, a size-limited response body,
// and trace-context injection. Response classification is delegated to
// handleStateTransitionStatus.
func (c *Client) doStateTransition(ctx context.Context, span trace.Span, logger libLog.Logger, requestURL, op string) error {
	if err := c.checkCircuitBreaker(); err != nil {
		logger.Log(ctx, libLog.LevelWarn, "circuit breaker open, failing fast")
		libOpentelemetry.HandleSpanBusinessErrorEvent(span, "Circuit breaker open", err)

		return err
	}

	// No request body: pass nil and do NOT set Content-Type.
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPut, requestURL, nil)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "Failed to create HTTP request", err)

		return fmt.Errorf("failed to create request: %w", err)
	}

	httpReq.Header.Set("Accept", "application/json")

	if c.serviceAPIKey != "" {
		httpReq.Header.Set("X-API-Key", c.serviceAPIKey)
	}

	if c.bearerTokenProvider != nil {
		token, tokenErr := c.bearerTokenProvider(ctx)
		if tokenErr != nil {
			libOpentelemetry.HandleSpanError(span, "Failed to acquire bearer token", tokenErr)

			return fmt.Errorf("acquire service-account token: %w", tokenErr)
		}

		httpReq.Header.Set("Authorization", "Bearer "+token)
	}

	libOpentelemetry.InjectHTTPContext(ctx, httpReq.Header)

	// #nosec G107 -- baseURL is validated at construction time and not user-controlled
	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		c.recordFailure()
		libOpentelemetry.HandleSpanError(span, "HTTP request failed", err)

		return fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBodySize))
	if err != nil {
		c.recordFailure()
		libOpentelemetry.HandleSpanError(span, "Failed to read response body", err)

		return fmt.Errorf("failed to read response body: %w", err)
	}

	return c.handleStateTransitionStatus(ctx, span, logger, op, resp.StatusCode, body)
}

// handleStateTransitionStatus classifies the response to an idempotent,
// body-less state-transition PUT (SuspendService, ReactivateTenant), mirroring
// handleCreateTenantStatus's breaker discipline. Both a 2xx and a 409 Conflict
// are success (the transition is idempotent: suspending an already-suspended
// association or reactivating a not-suspended tenant is tolerated). A 5xx is a
// service failure that feeds the circuit breaker; any other 4xx is a valid
// round-trip carrying the truncated body; anything else (3xx, ...) is
// out-of-contract and leaves the breaker counters untouched.
func (c *Client) handleStateTransitionStatus(ctx context.Context, span trace.Span, logger libLog.Logger, op string, statusCode int, body []byte) error {
	switch {
	case statusCode >= http.StatusOK && statusCode < http.StatusMultipleChoices:
		c.recordSuccess()
		logger.Log(ctx, libLog.LevelInfo, "tenant manager state transition succeeded",
			libLog.String("op", op),
			libLog.Int("status", statusCode),
		)

		return nil
	case statusCode == http.StatusConflict:
		// Idempotency tolerance: already in the target state is success.
		c.recordSuccess()
		logger.Log(ctx, libLog.LevelWarn, "tenant manager reported already in target state (tolerated)",
			libLog.String("op", op),
			libLog.String("body", truncateBody(body)),
		)

		return nil
	case isServerError(statusCode):
		c.recordFailure()
		logger.Log(ctx, libLog.LevelError, "tenant manager returned error",
			libLog.String("op", op),
			libLog.Int("status", statusCode),
			libLog.String("body", truncateBody(body)),
		)
		libOpentelemetry.HandleSpanError(span, "Tenant Manager returned error", fmt.Errorf("status %d", statusCode))

		return fmt.Errorf("tenant manager returned status %d for %s: %s", statusCode, op, truncateBody(body))
	case statusCode >= http.StatusBadRequest && statusCode < http.StatusInternalServerError:
		// Any other 4xx: a valid round-trip (resets the breaker), surfaced with the
		// truncated body so the caller can diagnose the rejection.
		c.recordSuccess()
		logger.Log(ctx, libLog.LevelError, "tenant manager rejected state transition",
			libLog.String("op", op),
			libLog.Int("status", statusCode),
			libLog.String("body", truncateBody(body)),
		)
		libOpentelemetry.HandleSpanBusinessErrorEvent(span, "Tenant Manager rejected state transition", fmt.Errorf("status %d", statusCode))

		return fmt.Errorf("tenant manager returned status %d for %s: %s", statusCode, op, truncateBody(body))
	default:
		// Out-of-contract status (3xx, ...): neither success nor a service failure —
		// leave the breaker counters untouched (mirrors handleCreateTenantStatus's
		// default branch).
		logger.Log(ctx, libLog.LevelError, "tenant manager returned unexpected status",
			libLog.String("op", op),
			libLog.Int("status", statusCode),
			libLog.String("body", truncateBody(body)),
		)
		libOpentelemetry.HandleSpanError(span, "Tenant Manager returned unexpected status", fmt.Errorf("status %d", statusCode))

		return fmt.Errorf("tenant manager returned unexpected status %d for %s: %s", statusCode, op, truncateBody(body))
	}
}
