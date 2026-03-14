//go:build unit

package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseAndVerifyTenantScopedID_NilValidationErrorsFallbackToGenericSentinels(t *testing.T) {
	t.Parallel()

	tenantID := uuid.NewString()

	t.Run("missing id", func(t *testing.T) {
		t.Parallel()

		app := fiber.New()
		var gotErr error

		app.Get("/contexts", func(c *fiber.Ctx) error {
			_, _, gotErr = ParseAndVerifyTenantScopedID(
				c,
				"context_id",
				IDLocationQuery,
				func(ctx context.Context, tenantID, resourceID uuid.UUID) error { return nil },
				func(_ context.Context) string { return tenantID },
				nil,
				nil,
				nil,
			)
			return nil
		})

		resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/contexts", nil))
		require.NoError(t, err)
		defer func() { require.NoError(t, resp.Body.Close()) }()

		require.Error(t, gotErr)
		assert.ErrorIs(t, gotErr, ErrMissingContextID)
	})

	t.Run("invalid id", func(t *testing.T) {
		t.Parallel()

		app := fiber.New()
		var gotErr error

		app.Get("/contexts", func(c *fiber.Ctx) error {
			_, _, gotErr = ParseAndVerifyTenantScopedID(
				c,
				"context_id",
				IDLocationQuery,
				func(ctx context.Context, tenantID, resourceID uuid.UUID) error { return nil },
				func(_ context.Context) string { return tenantID },
				nil,
				nil,
				nil,
			)
			return nil
		})

		resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/contexts?context_id=not-a-uuid", nil))
		require.NoError(t, err)
		defer func() { require.NoError(t, resp.Body.Close()) }()

		require.Error(t, gotErr)
		assert.ErrorIs(t, gotErr, ErrInvalidContextID)
	})
}

func TestParseAndVerifyResourceScopedID_NilValidationErrorsFallbackToGenericSentinels(t *testing.T) {
	t.Parallel()

	tenantID := uuid.NewString()

	t.Run("missing id", func(t *testing.T) {
		t.Parallel()

		app := fiber.New()
		var gotErr error

		app.Get("/resources", func(c *fiber.Ctx) error {
			_, _, gotErr = ParseAndVerifyResourceScopedID(
				c,
				"resource_id",
				IDLocationQuery,
				func(ctx context.Context, resourceID uuid.UUID) error { return nil },
				func(_ context.Context) string { return tenantID },
				nil,
				nil,
				nil,
				"resource",
			)
			return nil
		})

		resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/resources", nil))
		require.NoError(t, err)
		defer func() { require.NoError(t, resp.Body.Close()) }()

		require.Error(t, gotErr)
		assert.ErrorIs(t, gotErr, ErrMissingResourceID)
	})

	t.Run("invalid id", func(t *testing.T) {
		t.Parallel()

		app := fiber.New()
		var gotErr error

		app.Get("/resources", func(c *fiber.Ctx) error {
			_, _, gotErr = ParseAndVerifyResourceScopedID(
				c,
				"resource_id",
				IDLocationQuery,
				func(ctx context.Context, resourceID uuid.UUID) error { return nil },
				func(_ context.Context) string { return tenantID },
				nil,
				nil,
				nil,
				"resource",
			)
			return nil
		})

		resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/resources?resource_id=not-a-uuid", nil))
		require.NoError(t, err)
		defer func() { require.NoError(t, resp.Body.Close()) }()

		require.Error(t, gotErr)
		assert.ErrorIs(t, gotErr, ErrInvalidResourceID)
	})
}

func TestParseAndVerifyTenantScopedID_DefaultFiberUserContextDoesNotPanic(t *testing.T) {
	t.Parallel()

	resourceID := uuid.New()

	assert.NotPanics(t, func() {
		runInFiber(t, "/contexts/:contextId", "/contexts/"+resourceID.String(), func(c *fiber.Ctx) error {
			_, _, err := ParseAndVerifyTenantScopedID(
				c,
				"contextId",
				IDLocationParam,
				func(context.Context, uuid.UUID, uuid.UUID) error { return nil },
				testTenantExtractor,
				ErrMissingContextID,
				ErrInvalidContextID,
				ErrContextAccessDenied,
			)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrTenantIDNotFound)

			return c.SendStatus(fiber.StatusOK)
		})
	})
}

func TestParseAndVerifyResourceScopedID_DefaultFiberUserContextDoesNotPanic(t *testing.T) {
	t.Parallel()

	resourceID := uuid.New()

	assert.NotPanics(t, func() {
		runInFiber(t, "/resources/:resourceId", "/resources/"+resourceID.String(), func(c *fiber.Ctx) error {
			_, _, err := ParseAndVerifyResourceScopedID(
				c,
				"resourceId",
				IDLocationParam,
				func(context.Context, uuid.UUID) error { return nil },
				testTenantExtractor,
				ErrMissingResourceID,
				ErrInvalidResourceID,
				nil,
				"resource",
			)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrTenantIDNotFound)

			return c.SendStatus(fiber.StatusOK)
		})
	})
}
