// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

//go:build unit

package ratelimit

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockTierResolver implements TierResolver for testing.
type mockTierResolver struct {
	resolveFunc  func(tenantID, tierName string) (Tier, bool)
	fallbackFunc func(tierName string) Tier
}

func (m *mockTierResolver) Resolve(tenantID, tierName string) (Tier, bool) {
	if m.resolveFunc != nil {
		return m.resolveFunc(tenantID, tierName)
	}

	return Tier{}, false
}

func (m *mockTierResolver) Fallback(tierName string) Tier {
	if m.fallbackFunc != nil {
		return m.fallbackFunc(tierName)
	}

	return DefaultTier()
}

func TestTierFromMethod(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		expected string
	}{
		{name: "POST maps to aggressive", method: fiber.MethodPost, expected: "aggressive"},
		{name: "PUT maps to aggressive", method: fiber.MethodPut, expected: "aggressive"},
		{name: "PATCH maps to aggressive", method: fiber.MethodPatch, expected: "aggressive"},
		{name: "DELETE maps to aggressive", method: fiber.MethodDelete, expected: "aggressive"},
		{name: "GET maps to default", method: fiber.MethodGet, expected: "default"},
		{name: "HEAD maps to default", method: fiber.MethodHead, expected: "default"},
		{name: "OPTIONS maps to default", method: fiber.MethodOptions, expected: "default"},
		{name: "UNKNOWN maps to default", method: "UNKNOWN", expected: "default"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TierFromMethod(tt.method)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestTenantMethodTierSelector_NilResolver(t *testing.T) {
	extractTenantID := func(_ context.Context) string {
		return "tenant-1"
	}

	tierFn := TenantMethodTierSelector(nil, extractTenantID)
	require.NotNil(t, tierFn)

	tier := invokeTierFunc(t, tierFn, fiber.MethodGet, "/test")

	expected := DefaultTier()
	assert.Equal(t, expected.Name, tier.Name)
	assert.Equal(t, expected.Max, tier.Max)
}

func TestTenantMethodTierSelector_NilExtractTenantID(t *testing.T) {
	resolver := &mockTierResolver{}

	tierFn := TenantMethodTierSelector(resolver, nil)
	require.NotNil(t, tierFn)

	tier := invokeTierFunc(t, tierFn, fiber.MethodPost, "/test")

	expected := DefaultTier()
	assert.Equal(t, expected.Name, tier.Name)
	assert.Equal(t, expected.Max, tier.Max)
}

func TestTenantMethodTierSelector_POSTWithTenantFound(t *testing.T) {
	customTier := Tier{Name: "custom-aggressive", Max: 50, Window: 30 * time.Second}

	resolver := &mockTierResolver{
		resolveFunc: func(tenantID, tierName string) (Tier, bool) {
			assert.Equal(t, "tenant-1", tenantID)
			assert.Equal(t, "aggressive", tierName)

			return customTier, true
		},
	}

	extractTenantID := func(_ context.Context) string {
		return "tenant-1"
	}

	tierFn := TenantMethodTierSelector(resolver, extractTenantID)
	tier := invokeTierFunc(t, tierFn, fiber.MethodPost, "/test")

	assert.Equal(t, customTier.Name, tier.Name)
	assert.Equal(t, customTier.Max, tier.Max)
	assert.Equal(t, customTier.Window, tier.Window)
}

func TestTenantMethodTierSelector_GETWithTenantFound(t *testing.T) {
	customTier := Tier{Name: "custom-default", Max: 200, Window: 60 * time.Second}

	resolver := &mockTierResolver{
		resolveFunc: func(tenantID, tierName string) (Tier, bool) {
			assert.Equal(t, "tenant-1", tenantID)
			assert.Equal(t, "default", tierName)

			return customTier, true
		},
	}

	extractTenantID := func(_ context.Context) string {
		return "tenant-1"
	}

	tierFn := TenantMethodTierSelector(resolver, extractTenantID)
	tier := invokeTierFunc(t, tierFn, fiber.MethodGet, "/test")

	assert.Equal(t, customTier.Name, tier.Name)
	assert.Equal(t, customTier.Max, tier.Max)
	assert.Equal(t, customTier.Window, tier.Window)
}

func TestTenantMethodTierSelector_POSTWithTenantNotFound(t *testing.T) {
	fallbackTier := Tier{Name: "fallback-aggressive", Max: 75, Window: 45 * time.Second}

	resolver := &mockTierResolver{
		resolveFunc: func(_, _ string) (Tier, bool) {
			return Tier{}, false
		},
		fallbackFunc: func(tierName string) Tier {
			assert.Equal(t, "aggressive", tierName)

			return fallbackTier
		},
	}

	extractTenantID := func(_ context.Context) string {
		return "tenant-1"
	}

	tierFn := TenantMethodTierSelector(resolver, extractTenantID)
	tier := invokeTierFunc(t, tierFn, fiber.MethodPost, "/test")

	assert.Equal(t, fallbackTier.Name, tier.Name)
	assert.Equal(t, fallbackTier.Max, tier.Max)
	assert.Equal(t, fallbackTier.Window, tier.Window)
}

func TestTenantMethodTierSelector_GETWithEmptyTenantID(t *testing.T) {
	fallbackTier := Tier{Name: "fallback-default", Max: 300, Window: 60 * time.Second}

	resolveCalled := false

	resolver := &mockTierResolver{
		resolveFunc: func(_, _ string) (Tier, bool) {
			resolveCalled = true

			return Tier{}, false
		},
		fallbackFunc: func(tierName string) Tier {
			assert.Equal(t, "default", tierName)

			return fallbackTier
		},
	}

	extractTenantID := func(_ context.Context) string {
		return ""
	}

	tierFn := TenantMethodTierSelector(resolver, extractTenantID)
	tier := invokeTierFunc(t, tierFn, fiber.MethodGet, "/test")

	assert.False(t, resolveCalled, "Resolve should not be called when tenantID is empty")
	assert.Equal(t, fallbackTier.Name, tier.Name)
	assert.Equal(t, fallbackTier.Max, tier.Max)
	assert.Equal(t, fallbackTier.Window, tier.Window)
}

func TestTenantMethodTierSelector_DELETEMapsToAggressive(t *testing.T) {
	var capturedTier string

	resolver := &mockTierResolver{
		resolveFunc: func(_, tierName string) (Tier, bool) {
			capturedTier = tierName

			return Tier{Name: "delete-tier", Max: 10, Window: 10 * time.Second}, true
		},
	}

	extractTenantID := func(_ context.Context) string {
		return "tenant-1"
	}

	tierFn := TenantMethodTierSelector(resolver, extractTenantID)
	_ = invokeTierFunc(t, tierFn, fiber.MethodDelete, "/test")

	assert.Equal(t, "aggressive", capturedTier)
}

func TestTenantMethodTierSelector_HEADMapsToDefault(t *testing.T) {
	var capturedTier string

	resolver := &mockTierResolver{
		resolveFunc: func(_, tierName string) (Tier, bool) {
			capturedTier = tierName

			return Tier{Name: "head-tier", Max: 10, Window: 10 * time.Second}, true
		},
	}

	extractTenantID := func(_ context.Context) string {
		return "tenant-1"
	}

	tierFn := TenantMethodTierSelector(resolver, extractTenantID)
	_ = invokeTierFunc(t, tierFn, fiber.MethodHead, "/test")

	assert.Equal(t, "default", capturedTier)
}

// ─── ForTier tests ───

// newTestRateLimiter creates a RateLimiter without Redis for option-level tests.
// ForTier tests exercise resolveTier logic; they don't call Redis.
func newTestRateLimiter(opts ...Option) *RateLimiter {
	rl := &RateLimiter{
		logger:       log.NewNop(),
		identityFunc: IdentityFromIP(),
		failOpen:     true,
		redisTimeout: 500 * time.Millisecond,
		tierFallbacks: map[string]Tier{
			"default":    DefaultTier(),
			"aggressive": AggressiveTier(),
			"relaxed":    RelaxedTier(),
		},
	}

	for _, opt := range opts {
		opt(rl)
	}

	return rl
}

func TestForTier_NilRateLimiter(t *testing.T) {
	var rl *RateLimiter

	handler := rl.ForTier("default")
	require.NotNil(t, handler, "nil RateLimiter should return a pass-through handler")
}

func TestForTier_SingleTenant_DefaultTiers(t *testing.T) {
	rl := newTestRateLimiter()

	// resolveTier with "default" → DefaultTier()
	tier := invokeResolveTier(t, rl, "default")
	expected := DefaultTier()
	assert.Equal(t, expected.Name, tier.Name)
	assert.Equal(t, expected.Max, tier.Max)

	// resolveTier with "aggressive" → AggressiveTier()
	tier = invokeResolveTier(t, rl, "aggressive")
	expected = AggressiveTier()
	assert.Equal(t, expected.Name, tier.Name)
	assert.Equal(t, expected.Max, tier.Max)
}

func TestForTier_SingleTenant_CustomFallback(t *testing.T) {
	exportTier := Tier{Name: "export", Max: 10, Window: 60 * time.Second}

	rl := newTestRateLimiter(
		WithTierFallback(exportTier),
	)

	tier := invokeResolveTier(t, rl, "export")
	assert.Equal(t, "export", tier.Name)
	assert.Equal(t, 10, tier.Max)
}

func TestForTier_SingleTenant_UnknownTierFallsToDefault(t *testing.T) {
	rl := newTestRateLimiter()

	tier := invokeResolveTier(t, rl, "nonexistent")
	expected := DefaultTier()
	assert.Equal(t, expected.Name, tier.Name)
}

func TestForTier_SingleTenant_FallbackOverride(t *testing.T) {
	customDefault := Tier{Name: "default", Max: 999, Window: 120 * time.Second}

	rl := newTestRateLimiter(
		WithTierFallback(customDefault),
	)

	tier := invokeResolveTier(t, rl, "default")
	assert.Equal(t, 999, tier.Max)
}

func TestForTier_MultiTenant_TenantFound(t *testing.T) {
	customTier := Tier{Name: "tenant-export", Max: 5, Window: 30 * time.Second}

	resolver := &mockTierResolver{
		resolveFunc: func(tenantID, tierName string) (Tier, bool) {
			if tenantID == "tenant-1" && tierName == "export" {
				return customTier, true
			}

			return Tier{}, false
		},
	}

	rl := newTestRateLimiter(
		WithTenantResolver(resolver, func(_ context.Context) string {
			return "tenant-1"
		}),
	)

	tier := invokeResolveTier(t, rl, "export")
	assert.Equal(t, "tenant-export", tier.Name)
	assert.Equal(t, 5, tier.Max)
}

func TestForTier_MultiTenant_TenantNotFound_UsesFallback(t *testing.T) {
	fallbackTier := Tier{Name: "resolver-fallback", Max: 300, Window: 60 * time.Second}

	resolver := &mockTierResolver{
		resolveFunc: func(_, _ string) (Tier, bool) {
			return Tier{}, false
		},
		fallbackFunc: func(_ string) Tier {
			return fallbackTier
		},
	}

	rl := newTestRateLimiter(
		WithTenantResolver(resolver, func(_ context.Context) string {
			return "unknown-tenant"
		}),
	)

	tier := invokeResolveTier(t, rl, "default")
	assert.Equal(t, "resolver-fallback", tier.Name)
}

func TestForTier_MultiTenant_EmptyTenantID_UsesFallback(t *testing.T) {
	fallbackTier := Tier{Name: "empty-fallback", Max: 100, Window: 60 * time.Second}
	resolveCalled := false

	resolver := &mockTierResolver{
		resolveFunc: func(_, _ string) (Tier, bool) {
			resolveCalled = true

			return Tier{}, false
		},
		fallbackFunc: func(_ string) Tier {
			return fallbackTier
		},
	}

	rl := newTestRateLimiter(
		WithTenantResolver(resolver, func(_ context.Context) string {
			return ""
		}),
	)

	tier := invokeResolveTier(t, rl, "default")
	assert.False(t, resolveCalled, "Resolve should not be called with empty tenantID")
	assert.Equal(t, "empty-fallback", tier.Name)
}

func TestForTier_MultiTenant_NilResolver_FallsToLocal(t *testing.T) {
	rl := newTestRateLimiter(
		WithTenantResolver(nil, nil),
	)

	tier := invokeResolveTier(t, rl, "aggressive")
	expected := AggressiveTier()
	assert.Equal(t, expected.Name, tier.Name)
}

func TestWithTierFallback_EmptyNameIgnored(t *testing.T) {
	rl := newTestRateLimiter(
		WithTierFallback(Tier{Name: "", Max: 1, Window: time.Second}),
	)

	// Should not have added an empty-name entry; "default" still works
	tier := invokeResolveTier(t, rl, "default")
	expected := DefaultTier()
	assert.Equal(t, expected.Name, tier.Name)
}

// invokeResolveTier calls rl.resolveTier in a Fiber context to test tier resolution.
func invokeResolveTier(t *testing.T, rl *RateLimiter, tierName string) Tier {
	t.Helper()

	app := fiber.New()

	var capturedTier Tier

	app.Get("/test", func(c *fiber.Ctx) error {
		capturedTier = rl.resolveTier(c, tierName)

		return c.SendStatus(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	return capturedTier
}

// invokeTierFunc creates a Fiber app, registers a route for the given method,
// sends a request via httptest, and returns the Tier selected by the TierFunc.
func invokeTierFunc(t *testing.T, tierFn TierFunc, method, path string) Tier {
	t.Helper()

	app := fiber.New()

	var capturedTier Tier

	handler := func(c *fiber.Ctx) error {
		capturedTier = tierFn(c)

		return c.SendStatus(http.StatusOK)
	}

	switch method {
	case fiber.MethodGet:
		app.Get(path, handler)
	case fiber.MethodPost:
		app.Post(path, handler)
	case fiber.MethodPut:
		app.Put(path, handler)
	case fiber.MethodPatch:
		app.Patch(path, handler)
	case fiber.MethodDelete:
		app.Delete(path, handler)
	case fiber.MethodHead:
		app.Head(path, handler)
	case fiber.MethodOptions:
		app.Options(path, handler)
	default:
		app.All(path, handler)
	}

	req := httptest.NewRequest(method, path, nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)

	return capturedTier
}
