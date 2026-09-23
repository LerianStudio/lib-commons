//go:build unit

package idempotency

import (
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	tmcore "github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/core"
	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tenantEchoApp routes POST /test through mw with ctxTenant stamped on the
// tenant-manager context above it, and answers 201 with the tenant-manager
// value the HANDLER reads, so a test can assert what survived the middleware.
// Every call counts, so a replay is visible as a handler that did not run.
func tenantEchoApp(mw fiber.Handler, ctxTenant string, calls *atomic.Int32) *fiber.App {
	app := fiber.New()
	app.Use(tenantMiddleware(ctxTenant))
	app.Use(mw)
	app.Post("/test", func(c fiber.Ctx) error {
		calls.Add(1)

		return c.Status(fiber.StatusCreated).SendString(tmcore.GetTenantIDContext(c.Context()))
	})

	return app
}

// TestTenantProvider_RootsRecordAtProviderTenant is the defect itself: the
// application's tenant must reach the record without the application having to
// overwrite the request context every reader below the middleware shares.
func TestTenantProvider_RootsRecordAtProviderTenant(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)

	var providerCalls, handlerCalls atomic.Int32

	m := New(newRedisClient(t, mr),
		WithKeyPrefix("idem:"),
		WithTenantProvider(func(fiber.Ctx) (string, error) {
			providerCalls.Add(1)

			return "tenant-from-provider", nil
		}),
	)
	app := tenantEchoApp(m.Check(), "tenant-from-tmcore", &handlerCalls)

	first := doPost(t, app, "k1")
	require.Equal(t, http.StatusCreated, first.StatusCode)
	assert.Equal(t, "tenant-from-tmcore", readBody(t, first),
		"the middleware must leave the tenant-manager context below it untouched")
	assert.Equal(t, []string{"idem:tenant-from-provider:k1"}, mr.Keys(),
		"the record must be rooted at the provider's tenant, not the tenant-manager one")
	assert.Equal(t, int32(1), providerCalls.Load(), "the provider is called once per request")

	second := doPost(t, app, "k1")
	require.Equal(t, http.StatusCreated, second.StatusCode)
	assert.Equal(t, "tenant-from-tmcore", readBody(t, second), "the replay returns the original response")
	assert.Equal(t, int32(1), handlerCalls.Load(), "the duplicate must replay, not re-run")
	assert.Equal(t, int32(2), providerCalls.Load())
}

// TestTenantProvider_IgnoresTenantManagerContext proves the provider is the
// ONLY source when set: an empty tenant-manager context does not bypass, and a
// populated one does not rescue an empty provider result.
func TestTenantProvider_IgnoresTenantManagerContext(t *testing.T) {
	t.Parallel()

	t.Run("no tenant-manager tenant, provider tenant keys the request", func(t *testing.T) {
		t.Parallel()

		mr := miniredis.RunT(t)

		var calls atomic.Int32

		m := New(newRedisClient(t, mr), WithKeyPrefix("idem:"),
			WithTenantProvider(func(fiber.Ctx) (string, error) { return "p1", nil }))

		resp := doPost(t, tenantEchoApp(m.Check(), "", &calls), "k1")
		resp.Body.Close()

		require.Equal(t, http.StatusCreated, resp.StatusCode)
		assert.Equal(t, []string{"idem:p1:k1"}, mr.Keys())
	})

	t.Run("tenant-manager tenant present, empty provider bypasses", func(t *testing.T) {
		t.Parallel()

		mr := miniredis.RunT(t)

		var calls atomic.Int32

		m := New(newRedisClient(t, mr),
			WithTenantProvider(func(fiber.Ctx) (string, error) { return "", nil }))

		resp := doPost(t, tenantEchoApp(m.Check(), "tenant-from-tmcore", &calls), "k1")
		resp.Body.Close()

		require.Equal(t, http.StatusCreated, resp.StatusCode)
		assert.Equal(t, int32(1), calls.Load())
		assert.Empty(t, mr.Keys(), "an empty provider tenant takes the absent-tenant bypass")
	})
}

// TestTenantProvider_EmptyOrError_TakesAbsentTenantPath pins that neither an
// empty result nor an error invents a new refusal: both are the shipped
// absent-tenant branch — bypass by default, the WithRequireTenant refusal when
// opted in.
func TestTenantProvider_EmptyOrError_TakesAbsentTenantPath(t *testing.T) {
	t.Parallel()

	providers := map[string]TenantProvider{
		"empty": func(fiber.Ctx) (string, error) { return "", nil },
		"error": func(fiber.Ctx) (string, error) { return "ignored", errors.New("no tenant claim") },
	}

	for name, provider := range providers {
		t.Run(name+"/bypass by default", func(t *testing.T) {
			t.Parallel()

			mr := miniredis.RunT(t)

			var calls atomic.Int32

			m := New(newRedisClient(t, mr), WithTenantProvider(provider))

			resp := doPost(t, tenantEchoApp(m.Check(), "tenant-from-tmcore", &calls), "k1")
			resp.Body.Close()

			require.Equal(t, http.StatusCreated, resp.StatusCode)
			assert.Equal(t, int32(1), calls.Load())
			assert.Empty(t, mr.Keys())
		})

		t.Run(name+"/refused under WithRequireTenant", func(t *testing.T) {
			t.Parallel()

			mr := miniredis.RunT(t)

			var calls atomic.Int32

			m := New(newRedisClient(t, mr), WithTenantProvider(provider), WithRequireTenant())

			resp := doPost(t, tenantEchoApp(m.Check(), "tenant-from-tmcore", &calls), "k1")

			assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
			assert.Equal(t, "IDEMPOTENCY_TENANT_REQUIRED", decodeErrorBody(t, resp).Title)
			assert.Equal(t, int32(0), calls.Load())
			assert.Empty(t, mr.Keys())
		})
	}
}

// TestTenantProvider_NilKeepsDefault pins that a nil provider is ignored and the
// tenant-manager context stays the source.
func TestTenantProvider_NilKeepsDefault(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)

	var calls atomic.Int32

	m := New(newRedisClient(t, mr), WithKeyPrefix("idem:"), WithTenantProvider(nil))

	resp := doPost(t, tenantEchoApp(m.Check(), "t1", &calls), "k1")
	resp.Body.Close()

	require.Equal(t, http.StatusCreated, resp.StatusCode)
	assert.Equal(t, []string{"idem:t1:k1"}, mr.Keys())
}
