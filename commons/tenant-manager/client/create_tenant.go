package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/LerianStudio/lib-commons/v6/commons/tenant-manager/core"
	observability "github.com/LerianStudio/lib-observability/v2"
	libLog "github.com/LerianStudio/lib-observability/v2/log"
	libOpentelemetry "github.com/LerianStudio/lib-observability/v2/tracing"
	"go.opentelemetry.io/otel/trace"
)

// TokenProvider returns a bearer token for authenticating a write request to the
// Tenant Manager. It is called once per request, so an implementation is free to
// cache and refresh an OAuth client-credentials token behind it.
type TokenProvider func(ctx context.Context) (string, error)

// WithBearerTokenProvider configures the source of the service-account bearer
// token sent on RBAC-gated write requests (CreateTenant). The provider is
// invoked per request. When unset, write requests carry no Authorization header
// (only the always-required X-API-Key, which NewClient rejects an empty value
// for).
func WithBearerTokenProvider(provider TokenProvider) ClientOption {
	return func(c *Client) {
		c.bearerTokenProvider = provider
	}
}

// TenantOwner is the initial owner identity provisioned with a new tenant. The
// password is supplied pre-hashed (bcrypt) — the plaintext is never transported.
type TenantOwner struct {
	Email         string `json:"email"`
	PasswordHash  string `json:"passwordHash,omitempty"`
	EmailVerified bool   `json:"emailVerified"`
}

// CreateTenantRequest is the POST /v1/tenants payload.
type CreateTenantRequest struct {
	Name        string            `json:"name"`
	Slug        string            `json:"slug,omitempty"`
	Environment string            `json:"environment"`
	Isolation   string            `json:"isolation"`
	Metadata    map[string]string `json:"metadata,omitempty"`
	Owner       TenantOwner       `json:"owner"`
}

// CreateTenantResponse is the tenant record returned by a successful create (or
// by an idempotent repeat create of an existing tenant).
type CreateTenantResponse struct {
	ID     string `json:"id"`
	Slug   string `json:"slug"`
	Status string `json:"status"`
}

// CreateTenant provisions a tenant via the Tenant Manager's POST /v1/tenants
// endpoint, which is RBAC-gated (backoffice:tm:tenants:write). It sends the
// service-account bearer token from the configured provider plus the X-API-Key
// header, and mirrors the read methods' transport contract: circuit-breaker
// gating, a size-limited response body, and trace-context injection.
//
// The Tenant Manager is idempotent by tenant identity, returning the existing
// tenant with 200 OK on a repeat create; this method treats 200 and 201 as
// success. A 5xx is recorded as a service failure (feeds the circuit breaker);
// a 4xx is a valid round-trip that resets the failure counter.
func (c *Client) CreateTenant(ctx context.Context, req CreateTenantRequest) (*CreateTenantResponse, error) {
	c.httpClientOnce.Do(func() {
		if c.httpClient == nil {
			c.httpClient = newDefaultHTTPClient()
		}
	})

	logger, tracer, _, _ := observability.NewTrackingFromContext(ctx)

	ctx, span := tracer.Start(ctx, "tenantmanager.client.create_tenant")
	defer span.End()

	if err := c.checkCircuitBreaker(); err != nil {
		logger.Log(ctx, libLog.LevelWarn, "circuit breaker open, failing fast")
		libOpentelemetry.HandleSpanBusinessErrorEvent(span, "Circuit breaker open", err)

		return nil, err
	}

	payload, err := json.Marshal(req)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "Failed to marshal request", err)

		return nil, fmt.Errorf("failed to marshal create tenant request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/tenants", bytes.NewReader(payload))
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
		return nil, c.handleCreateTenantStatus(ctx, span, logger, resp.StatusCode, body)
	}

	var out CreateTenantResponse
	if err := json.Unmarshal(body, &out); err != nil {
		libOpentelemetry.HandleSpanError(span, "Failed to parse response", err)

		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	c.recordSuccess()
	logger.Log(ctx, libLog.LevelInfo, "successfully created tenant", libLog.String("slug", out.Slug))

	return &out, nil
}

// handleCreateTenantStatus maps a non-success create response to a typed error,
// mirroring handleGetTenantConfigStatus: a 409 is a conflict (ErrTenantConflict),
// any other 4xx is a valid round-trip carrying the truncated body, and a 5xx is a
// service failure that feeds the circuit breaker. Only 5xx records a failure; 4xx
// resets the consecutive-failure counter. Anything else (202, 3xx, ...) is
// out-of-contract: surfaced as an error without touching the breaker counters.
func (c *Client) handleCreateTenantStatus(ctx context.Context, span trace.Span, logger libLog.Logger, statusCode int, body []byte) error {
	switch {
	case statusCode == http.StatusConflict:
		c.recordSuccess()
		logger.Log(ctx, libLog.LevelWarn, "tenant already exists",
			libLog.String("body", truncateBody(body)),
		)
		libOpentelemetry.HandleSpanBusinessErrorEvent(span, "Tenant conflict", core.ErrTenantConflict)

		return fmt.Errorf("%w: %s", core.ErrTenantConflict, truncateBody(body))
	case isServerError(statusCode):
		c.recordFailure()
		logger.Log(ctx, libLog.LevelError, "tenant manager returned error",
			libLog.Int("status", statusCode),
			libLog.String("body", truncateBody(body)),
		)
		libOpentelemetry.HandleSpanError(span, "Tenant Manager returned error", fmt.Errorf("status %d", statusCode))

		return fmt.Errorf("tenant manager returned status %d for create tenant: %s", statusCode, truncateBody(body))
	case statusCode >= http.StatusBadRequest && statusCode < http.StatusInternalServerError:
		// Any other 4xx: a valid round-trip (resets the breaker), surfaced with the
		// truncated body so the caller can diagnose the rejection.
		c.recordSuccess()
		logger.Log(ctx, libLog.LevelError, "tenant manager rejected create",
			libLog.Int("status", statusCode),
			libLog.String("body", truncateBody(body)),
		)
		libOpentelemetry.HandleSpanBusinessErrorEvent(span, "Tenant Manager rejected create", fmt.Errorf("status %d", statusCode))

		return fmt.Errorf("tenant manager returned status %d for create tenant: %s", statusCode, truncateBody(body))
	default:
		// Out-of-contract status (202, 3xx, ...): neither a business rejection nor a
		// service failure — leave the breaker counters untouched (mirrors
		// handleGetTenantConfigStatus's default branch).
		logger.Log(ctx, libLog.LevelError, "tenant manager returned unexpected status",
			libLog.Int("status", statusCode),
			libLog.String("body", truncateBody(body)),
		)
		libOpentelemetry.HandleSpanError(span, "Tenant Manager returned unexpected status", fmt.Errorf("status %d", statusCode))

		return fmt.Errorf("tenant manager returned unexpected status %d for create tenant: %s", statusCode, truncateBody(body))
	}
}
