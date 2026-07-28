package client

import (
	"context"
	"fmt"
	"net/url"

	observability "github.com/LerianStudio/lib-observability/v2"
)

// ReactivateTenant reactivates a suspended tenant via the Tenant Manager's
// PUT /v1/tenants/{tenantID}/reactivate endpoint, which is RBAC-gated
// (backoffice:tm:tenants:write). It mirrors CreateTenant's transport contract
// (circuit-breaker gating, service-account bearer plus X-API-Key, size-limited
// body, trace-context injection) but sends no request body.
//
// The transition is idempotent: reactivating a not-suspended tenant returns
// 409, which this method tolerates as success. A 5xx is recorded as a service
// failure (feeds the circuit breaker); any other 4xx is a valid round-trip that
// resets the failure counter.
func (c *Client) ReactivateTenant(ctx context.Context, tenantID string) error {
	c.httpClientOnce.Do(func() {
		if c.httpClient == nil {
			c.httpClient = newDefaultHTTPClient()
		}
	})

	logger, tracer, _, _ := observability.NewTrackingFromContext(ctx)

	ctx, span := tracer.Start(ctx, "tenantmanager.client.reactivate_tenant")
	defer span.End()

	// Path parameter is escaped to defend against path traversal / injection
	// (mirrors GetTenantConfig).
	requestURL := fmt.Sprintf("%s/v1/tenants/%s/reactivate", c.baseURL, url.PathEscape(tenantID))

	return c.doStateTransition(ctx, span, logger, requestURL, "reactivate tenant")
}
