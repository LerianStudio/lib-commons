//go:build unit

// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v6/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v6/commons/tenant-manager/event"
	"github.com/LerianStudio/lib-commons/v6/commons/tenant-manager/tenantcache"
	"github.com/LerianStudio/lib-observability/v2/log"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	dashedTenantID   = "550e8400-e29b-41d4-a716-446655440000"
	dashlessTenantID = "550e8400e29b41d4a716446655440000"
)

// runMiddlewareWithClaim drives one request through WithTenantDB carrying the
// given tenantId claim, and reports the tenant ID seen by the downstream
// handler plus the paths requested from the tenant-manager API.
func runMiddlewareWithClaim(
	t *testing.T,
	claim string,
	cache *tenantcache.TenantCache,
) (contextTenantID, originalTenantID string, requestedPaths []string, status int) {
	t.Helper()

	var (
		mu        sync.Mutex
		requested []string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requested = append(requested, r.URL.Path)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(newTestTenantConfig(dashlessTenantID))
	}))
	t.Cleanup(server.Close)

	pmClient := newCacheTestClient(t, server.URL)
	loader := tenantcache.NewTenantLoader(
		pmClient, cache, "test-service",
		tenantcache.DefaultTenantCacheTTL, log.NewNop(),
	)

	mid := &TenantMiddleware{enabled: true, cache: cache, loader: loader}

	token := buildTestJWT(t, map[string]any{"sub": "user-123", "tenantId": claim})

	app := fiber.New()
	app.Use(simulateAuthMiddleware("user-123"))
	app.Use(mid.WithTenantDB)
	app.Get("/test", func(c fiber.Ctx) error {
		contextTenantID = core.GetTenantIDContext(c.Context())
		originalTenantID = core.GetOriginalTenantIDContext(c.Context())

		return c.SendString("ok")
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := app.Test(req, fiber.TestConfig{Timeout: 0})
	require.NoError(t, err)

	defer resp.Body.Close()

	mu.Lock()
	defer mu.Unlock()

	return contextTenantID, originalTenantID, requested, resp.StatusCode
}

func TestWithTenantDB_CanonicalizesDashedClaim(t *testing.T) {
	cache := tenantcache.NewTenantCache()

	contextTenantID, originalTenantID, requested, status := runMiddlewareWithClaim(t, dashedTenantID, cache)

	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, dashlessTenantID, contextTenantID,
		"a dashed JWT claim must be canonicalized before it reaches the request context")
	assert.Equal(t, dashedTenantID, originalTenantID,
		"the validated JWT spelling must remain available for rolling-upgrade key compatibility")

	_, found := cache.Get(dashlessTenantID)
	assert.True(t, found, "tenant must be cached under the canonical dashless key")

	_, foundDashed := cache.Get(dashedTenantID)
	assert.False(t, foundDashed, "no entry may be minted under the raw dashed claim")

	require.Len(t, requested, 1)
	assert.Contains(t, requested[0], dashlessTenantID,
		"the tenant-manager fetch must use the canonical tenant ID")
}

func TestWithTenantDB_CanonicalizesUppercaseClaim(t *testing.T) {
	cache := tenantcache.NewTenantCache()

	contextTenantID, originalTenantID, _, status := runMiddlewareWithClaim(t,
		"550E8400-E29B-41D4-A716-446655440000", cache)

	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, dashlessTenantID, contextTenantID)
	assert.Equal(t, "550E8400-E29B-41D4-A716-446655440000", originalTenantID)
}

func TestWithTenantDB_PassesNonUUIDClaimThrough(t *testing.T) {
	cache := tenantcache.NewTenantCache()

	contextTenantID, originalTenantID, _, status := runMiddlewareWithClaim(t, "tenant-cache-slug", cache)

	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "tenant-cache-slug", contextTenantID,
		"non-UUID tenant IDs remain supported and must pass through verbatim")
	assert.Equal(t, "tenant-cache-slug", originalTenantID)
}

func TestWithTenantDB_RejectsInvalidClaimFormat(t *testing.T) {
	cache := tenantcache.NewTenantCache()

	_, _, _, status := runMiddlewareWithClaim(t, "tenant/../../etc", cache)

	assert.Equal(t, http.StatusUnauthorized, status)
}

// TestDashedJWTAndDashlessEventShareOneKeyNamespace is the end-to-end proof for
// the production symptom: the tenant arrives on the HTTP path as a dashed JWT
// claim while the Tenant Manager publishes the lifecycle event with a dashless
// tenant_id. Both must address the same cache key or the eviction is a no-op.
func TestDashedJWTAndDashlessEventShareOneKeyNamespace(t *testing.T) {
	cache := tenantcache.NewTenantCache()

	contextTenantID, _, _, status := runMiddlewareWithClaim(t, dashedTenantID, cache)
	require.Equal(t, http.StatusOK, status)
	require.NotEmpty(t, contextTenantID)

	raw := []byte(`{
		"event_id": "evt-1",
		"event_type": "tenant.suspended",
		"tenant_id": "` + dashlessTenantID + `",
		"timestamp": "2026-08-04T00:00:00Z"
	}`)

	evt, err := event.ParseEvent(raw)
	require.NoError(t, err)

	dispatcher := event.NewEventDispatcher(cache, nil, "test-service",
		event.WithCacheTTL(1*time.Hour))

	require.NoError(t, dispatcher.HandleEvent(t.Context(), *evt))

	_, found := cache.Get(contextTenantID)
	assert.False(t, found,
		"the dashless event must evict the entry the dashed JWT claim created")
}
