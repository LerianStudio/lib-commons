//go:build unit

package http

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type tenantKey struct{}

func testTenantExtractor(ctx context.Context) string {
	v, _ := ctx.Value(tenantKey{}).(string)
	return v
}

func setupApp(t *testing.T, path string, h fiber.Handler) *fiber.App {
	t.Helper()
	app := fiber.New()
	app.Get(path, h)
	return app
}

// runInFiber runs a handler inside a real Fiber context so assertions
// that depend on Fiber's *fiber.Ctx work correctly.
func runInFiber(t *testing.T, path, url string, handler fiber.Handler) {
	t.Helper()

	app := setupApp(t, path, handler)
	req := httptest.NewRequest(http.MethodGet, url, nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

// ---------------------------------------------------------------------------
// ParseAndVerifyTenantScopedID
// ---------------------------------------------------------------------------

func TestParseAndVerifyTenantScopedID_HappyPath_Param(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	contextID := uuid.New()

	app := setupApp(t, "/contexts/:contextId", func(c *fiber.Ctx) error {
		c.SetUserContext(context.WithValue(context.Background(), tenantKey{}, tenantID.String()))

		gotContextID, gotTenantID, err := ParseAndVerifyTenantScopedID(
			c,
			"contextId",
			IDLocationParam,
			func(ctx context.Context, tID, resourceID uuid.UUID) error {
				if tID != tenantID || resourceID != contextID {
					return errors.New("bad verifier input")
				}
				return nil
			},
			testTenantExtractor,
			ErrMissingContextID,
			ErrInvalidContextID,
			ErrContextAccessDenied,
		)
		require.NoError(t, err)
		assert.Equal(t, contextID, gotContextID)
		assert.Equal(t, tenantID, gotTenantID)

		return c.SendStatus(fiber.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/contexts/"+contextID.String(), nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestParseAndVerifyTenantScopedID_HappyPath_Query(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	contextID := uuid.New()

	app := setupApp(t, "/search", func(c *fiber.Ctx) error {
		c.SetUserContext(context.WithValue(context.Background(), tenantKey{}, tenantID.String()))

		gotContextID, gotTenantID, err := ParseAndVerifyTenantScopedID(
			c,
			"contextId",
			IDLocationQuery,
			func(ctx context.Context, tID, resourceID uuid.UUID) error {
				if tID != tenantID || resourceID != contextID {
					return errors.New("bad verifier input")
				}
				return nil
			},
			testTenantExtractor,
			ErrMissingContextID,
			ErrInvalidContextID,
			ErrContextAccessDenied,
		)
		require.NoError(t, err)
		assert.Equal(t, contextID, gotContextID)
		assert.Equal(t, tenantID, gotTenantID)

		return c.SendStatus(fiber.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/search?contextId="+contextID.String(), nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestParseAndVerifyTenantScopedID_NilFiberContext(t *testing.T) {
	t.Parallel()

	_, _, err := ParseAndVerifyTenantScopedID(
		nil,
		"contextId",
		IDLocationParam,
		func(context.Context, uuid.UUID, uuid.UUID) error { return nil },
		testTenantExtractor,
		ErrMissingContextID,
		ErrInvalidContextID,
		ErrContextAccessDenied,
	)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrContextNotFound)
}

func TestParseAndVerifyTenantScopedID_NilVerifier(t *testing.T) {
	t.Parallel()

	resourceID := uuid.New()

	runInFiber(t, "/contexts/:contextId", "/contexts/"+resourceID.String(), func(c *fiber.Ctx) error {
		c.SetUserContext(context.WithValue(context.Background(), tenantKey{}, uuid.NewString()))

		_, _, err := ParseAndVerifyTenantScopedID(
			c,
			"contextId",
			IDLocationParam,
			nil, // verifier is nil
			testTenantExtractor,
			ErrMissingContextID,
			ErrInvalidContextID,
			ErrContextAccessDenied,
		)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrVerifierNotConfigured)

		return c.SendStatus(fiber.StatusOK)
	})
}

func TestParseAndVerifyTenantScopedID_NilTenantExtractor(t *testing.T) {
	t.Parallel()

	resourceID := uuid.New()

	runInFiber(t, "/contexts/:contextId", "/contexts/"+resourceID.String(), func(c *fiber.Ctx) error {
		_, _, err := ParseAndVerifyTenantScopedID(
			c,
			"contextId",
			IDLocationParam,
			func(context.Context, uuid.UUID, uuid.UUID) error { return nil },
			nil, // tenant extractor is nil
			ErrMissingContextID,
			ErrInvalidContextID,
			ErrContextAccessDenied,
		)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrTenantExtractorNil)

		return c.SendStatus(fiber.StatusOK)
	})
}

func TestParseAndVerifyTenantScopedID_MissingResourceID_Param(t *testing.T) {
	t.Parallel()

	// When route param is not defined in the path, Params returns "".
	runInFiber(t, "/contexts", "/contexts", func(c *fiber.Ctx) error {
		c.SetUserContext(context.WithValue(context.Background(), tenantKey{}, uuid.NewString()))

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
		assert.ErrorIs(t, err, ErrMissingContextID)

		return c.SendStatus(fiber.StatusOK)
	})
}

func TestParseAndVerifyTenantScopedID_MissingResourceID_Query(t *testing.T) {
	t.Parallel()

	runInFiber(t, "/search", "/search", func(c *fiber.Ctx) error {
		c.SetUserContext(context.WithValue(context.Background(), tenantKey{}, uuid.NewString()))

		_, _, err := ParseAndVerifyTenantScopedID(
			c,
			"contextId",
			IDLocationQuery,
			func(context.Context, uuid.UUID, uuid.UUID) error { return nil },
			testTenantExtractor,
			ErrMissingContextID,
			ErrInvalidContextID,
			ErrContextAccessDenied,
		)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrMissingContextID)

		return c.SendStatus(fiber.StatusOK)
	})
}

func TestParseAndVerifyTenantScopedID_InvalidResourceID(t *testing.T) {
	t.Parallel()

	runInFiber(t, "/contexts/:contextId", "/contexts/not-a-uuid", func(c *fiber.Ctx) error {
		c.SetUserContext(context.WithValue(context.Background(), tenantKey{}, uuid.NewString()))

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
		assert.ErrorIs(t, err, ErrInvalidContextID)
		assert.Contains(t, err.Error(), "not-a-uuid")

		return c.SendStatus(fiber.StatusOK)
	})
}

func TestParseAndVerifyTenantScopedID_InvalidResourceID_Query(t *testing.T) {
	t.Parallel()

	runInFiber(t, "/search", "/search?contextId=garbage-value", func(c *fiber.Ctx) error {
		c.SetUserContext(context.WithValue(context.Background(), tenantKey{}, uuid.NewString()))

		_, _, err := ParseAndVerifyTenantScopedID(
			c,
			"contextId",
			IDLocationQuery,
			func(context.Context, uuid.UUID, uuid.UUID) error { return nil },
			testTenantExtractor,
			ErrMissingContextID,
			ErrInvalidContextID,
			ErrContextAccessDenied,
		)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidContextID)
		assert.Contains(t, err.Error(), "garbage-value")

		return c.SendStatus(fiber.StatusOK)
	})
}

func TestParseAndVerifyTenantScopedID_EmptyTenantFromExtractor(t *testing.T) {
	t.Parallel()

	resourceID := uuid.New()
	emptyTenantExtractor := func(ctx context.Context) string { return "" }

	runInFiber(t, "/contexts/:contextId", "/contexts/"+resourceID.String(), func(c *fiber.Ctx) error {
		_, _, err := ParseAndVerifyTenantScopedID(
			c,
			"contextId",
			IDLocationParam,
			func(context.Context, uuid.UUID, uuid.UUID) error { return nil },
			emptyTenantExtractor,
			ErrMissingContextID,
			ErrInvalidContextID,
			ErrContextAccessDenied,
		)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrTenantIDNotFound)

		return c.SendStatus(fiber.StatusOK)
	})
}

func TestParseAndVerifyTenantScopedID_InvalidTenantID(t *testing.T) {
	t.Parallel()

	resourceID := uuid.New()
	badTenantExtractor := func(ctx context.Context) string { return "not-a-valid-uuid" }

	runInFiber(t, "/contexts/:contextId", "/contexts/"+resourceID.String(), func(c *fiber.Ctx) error {
		_, _, err := ParseAndVerifyTenantScopedID(
			c,
			"contextId",
			IDLocationParam,
			func(context.Context, uuid.UUID, uuid.UUID) error { return nil },
			badTenantExtractor,
			ErrMissingContextID,
			ErrInvalidContextID,
			ErrContextAccessDenied,
		)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidTenantID)

		return c.SendStatus(fiber.StatusOK)
	})
}

func TestParseAndVerifyTenantScopedID_InvalidIDLocation(t *testing.T) {
	t.Parallel()

	resourceID := uuid.New()

	runInFiber(t, "/contexts/:contextId", "/contexts/"+resourceID.String(), func(c *fiber.Ctx) error {
		c.SetUserContext(context.WithValue(context.Background(), tenantKey{}, uuid.NewString()))

		_, _, err := ParseAndVerifyTenantScopedID(
			c,
			"contextId",
			IDLocation("body"), // invalid location
			func(context.Context, uuid.UUID, uuid.UUID) error { return nil },
			testTenantExtractor,
			ErrMissingContextID,
			ErrInvalidContextID,
			ErrContextAccessDenied,
		)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidIDLocation)

		return c.SendStatus(fiber.StatusOK)
	})
}

func TestParseAndVerifyTenantScopedID_VerifierReturnsContextNotFound(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	resourceID := uuid.New()

	runInFiber(t, "/contexts/:contextId", "/contexts/"+resourceID.String(), func(c *fiber.Ctx) error {
		c.SetUserContext(context.WithValue(context.Background(), tenantKey{}, tenantID.String()))

		_, _, err := ParseAndVerifyTenantScopedID(
			c,
			"contextId",
			IDLocationParam,
			func(context.Context, uuid.UUID, uuid.UUID) error { return ErrContextNotFound },
			testTenantExtractor,
			ErrMissingContextID,
			ErrInvalidContextID,
			ErrContextAccessDenied,
		)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrContextNotFound)

		return c.SendStatus(fiber.StatusOK)
	})
}

func TestParseAndVerifyTenantScopedID_VerifierReturnsContextNotOwned(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	resourceID := uuid.New()

	runInFiber(t, "/contexts/:contextId", "/contexts/"+resourceID.String(), func(c *fiber.Ctx) error {
		c.SetUserContext(context.WithValue(context.Background(), tenantKey{}, tenantID.String()))

		_, _, err := ParseAndVerifyTenantScopedID(
			c,
			"contextId",
			IDLocationParam,
			func(context.Context, uuid.UUID, uuid.UUID) error { return ErrContextNotOwned },
			testTenantExtractor,
			ErrMissingContextID,
			ErrInvalidContextID,
			ErrContextAccessDenied,
		)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrContextAccessDenied)

		return c.SendStatus(fiber.StatusOK)
	})
}

func TestParseAndVerifyTenantScopedID_VerifierReturnsContextNotActive(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	resourceID := uuid.New()

	runInFiber(t, "/contexts/:contextId", "/contexts/"+resourceID.String(), func(c *fiber.Ctx) error {
		c.SetUserContext(context.WithValue(context.Background(), tenantKey{}, tenantID.String()))

		_, _, err := ParseAndVerifyTenantScopedID(
			c,
			"contextId",
			IDLocationParam,
			func(context.Context, uuid.UUID, uuid.UUID) error { return ErrContextNotActive },
			testTenantExtractor,
			ErrMissingContextID,
			ErrInvalidContextID,
			ErrContextAccessDenied,
		)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrContextNotActive)

		return c.SendStatus(fiber.StatusOK)
	})
}

func TestParseAndVerifyTenantScopedID_VerifierReturnsContextAccessDenied(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	resourceID := uuid.New()

	runInFiber(t, "/contexts/:contextId", "/contexts/"+resourceID.String(), func(c *fiber.Ctx) error {
		c.SetUserContext(context.WithValue(context.Background(), tenantKey{}, tenantID.String()))

		_, _, err := ParseAndVerifyTenantScopedID(
			c,
			"contextId",
			IDLocationParam,
			func(context.Context, uuid.UUID, uuid.UUID) error { return ErrContextAccessDenied },
			testTenantExtractor,
			ErrMissingContextID,
			ErrInvalidContextID,
			ErrContextAccessDenied,
		)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrContextAccessDenied)

		return c.SendStatus(fiber.StatusOK)
	})
}

func TestParseAndVerifyTenantScopedID_VerifierReturnsUnknownError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	resourceID := uuid.New()

	runInFiber(t, "/contexts/:contextId", "/contexts/"+resourceID.String(), func(c *fiber.Ctx) error {
		c.SetUserContext(context.WithValue(context.Background(), tenantKey{}, tenantID.String()))

		_, _, err := ParseAndVerifyTenantScopedID(
			c,
			"contextId",
			IDLocationParam,
			func(context.Context, uuid.UUID, uuid.UUID) error { return errors.New("db connection lost") },
			testTenantExtractor,
			ErrMissingContextID,
			ErrInvalidContextID,
			ErrContextAccessDenied,
		)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrContextLookupFailed)
		assert.Contains(t, err.Error(), "db connection lost")

		return c.SendStatus(fiber.StatusOK)
	})
}

func TestParseAndVerifyTenantScopedID_WrappedVerifierError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	resourceID := uuid.New()

	// Wrap ErrContextNotFound in another error to verify errors.Is traversal works.
	wrappedErr := fmt.Errorf("database issue: %w", ErrContextNotFound)

	runInFiber(t, "/contexts/:contextId", "/contexts/"+resourceID.String(), func(c *fiber.Ctx) error {
		c.SetUserContext(context.WithValue(context.Background(), tenantKey{}, tenantID.String()))

		_, _, err := ParseAndVerifyTenantScopedID(
			c,
			"contextId",
			IDLocationParam,
			func(context.Context, uuid.UUID, uuid.UUID) error { return wrappedErr },
			testTenantExtractor,
			ErrMissingContextID,
			ErrInvalidContextID,
			ErrContextAccessDenied,
		)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrContextNotFound)

		return c.SendStatus(fiber.StatusOK)
	})
}

// ---------------------------------------------------------------------------
// ParseAndVerifyResourceScopedID
// ---------------------------------------------------------------------------

func TestParseAndVerifyResourceScopedID_HappyPath(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	exceptionID := uuid.New()

	app := setupApp(t, "/exceptions/:exceptionId", func(c *fiber.Ctx) error {
		c.SetUserContext(context.WithValue(context.Background(), tenantKey{}, tenantID.String()))

		gotID, gotTenantID, err := ParseAndVerifyResourceScopedID(
			c,
			"exceptionId",
			IDLocationParam,
			func(ctx context.Context, resourceID uuid.UUID) error {
				if resourceID != exceptionID {
					return ErrExceptionNotFound
				}
				return nil
			},
			testTenantExtractor,
			ErrMissingExceptionID,
			ErrInvalidExceptionID,
			ErrExceptionAccessDenied,
			"exception",
		)
		require.NoError(t, err)
		assert.Equal(t, exceptionID, gotID)
		assert.Equal(t, tenantID, gotTenantID)

		return c.SendStatus(fiber.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/exceptions/"+exceptionID.String(), nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
}

func TestParseAndVerifyResourceScopedID_NilFiberContext(t *testing.T) {
	t.Parallel()

	_, _, err := ParseAndVerifyResourceScopedID(
		nil,
		"exceptionId",
		IDLocationParam,
		func(context.Context, uuid.UUID) error { return nil },
		testTenantExtractor,
		ErrMissingExceptionID,
		ErrInvalidExceptionID,
		ErrExceptionAccessDenied,
		"exception",
	)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrContextNotFound)
}

func TestParseAndVerifyResourceScopedID_NilVerifier(t *testing.T) {
	t.Parallel()

	resourceID := uuid.New()

	runInFiber(t, "/exceptions/:exceptionId", "/exceptions/"+resourceID.String(), func(c *fiber.Ctx) error {
		c.SetUserContext(context.WithValue(context.Background(), tenantKey{}, uuid.NewString()))

		_, _, err := ParseAndVerifyResourceScopedID(
			c,
			"exceptionId",
			IDLocationParam,
			nil,
			testTenantExtractor,
			ErrMissingExceptionID,
			ErrInvalidExceptionID,
			ErrExceptionAccessDenied,
			"exception",
		)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrVerifierNotConfigured)

		return c.SendStatus(fiber.StatusOK)
	})
}

func TestParseAndVerifyResourceScopedID_NilTenantExtractor(t *testing.T) {
	t.Parallel()

	resourceID := uuid.New()

	runInFiber(t, "/exceptions/:exceptionId", "/exceptions/"+resourceID.String(), func(c *fiber.Ctx) error {
		_, _, err := ParseAndVerifyResourceScopedID(
			c,
			"exceptionId",
			IDLocationParam,
			func(context.Context, uuid.UUID) error { return nil },
			nil,
			ErrMissingExceptionID,
			ErrInvalidExceptionID,
			ErrExceptionAccessDenied,
			"exception",
		)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrTenantExtractorNil)

		return c.SendStatus(fiber.StatusOK)
	})
}

func TestParseAndVerifyResourceScopedID_MissingResourceID(t *testing.T) {
	t.Parallel()

	runInFiber(t, "/exceptions", "/exceptions", func(c *fiber.Ctx) error {
		c.SetUserContext(context.WithValue(context.Background(), tenantKey{}, uuid.NewString()))

		_, _, err := ParseAndVerifyResourceScopedID(
			c,
			"exceptionId",
			IDLocationParam,
			func(context.Context, uuid.UUID) error { return nil },
			testTenantExtractor,
			ErrMissingExceptionID,
			ErrInvalidExceptionID,
			ErrExceptionAccessDenied,
			"exception",
		)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrMissingExceptionID)

		return c.SendStatus(fiber.StatusOK)
	})
}

func TestParseAndVerifyResourceScopedID_InvalidResourceID(t *testing.T) {
	t.Parallel()

	runInFiber(t, "/exceptions/:exceptionId", "/exceptions/not-valid", func(c *fiber.Ctx) error {
		c.SetUserContext(context.WithValue(context.Background(), tenantKey{}, uuid.NewString()))

		_, _, err := ParseAndVerifyResourceScopedID(
			c,
			"exceptionId",
			IDLocationParam,
			func(context.Context, uuid.UUID) error { return nil },
			testTenantExtractor,
			ErrMissingExceptionID,
			ErrInvalidExceptionID,
			ErrExceptionAccessDenied,
			"exception",
		)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidExceptionID)
		assert.Contains(t, err.Error(), "not-valid")

		return c.SendStatus(fiber.StatusOK)
	})
}

func TestParseAndVerifyResourceScopedID_EmptyTenantFromExtractor(t *testing.T) {
	t.Parallel()

	resourceID := uuid.New()
	emptyTenantExtractor := func(ctx context.Context) string { return "" }

	runInFiber(t, "/exceptions/:exceptionId", "/exceptions/"+resourceID.String(), func(c *fiber.Ctx) error {
		_, _, err := ParseAndVerifyResourceScopedID(
			c,
			"exceptionId",
			IDLocationParam,
			func(context.Context, uuid.UUID) error { return nil },
			emptyTenantExtractor,
			ErrMissingExceptionID,
			ErrInvalidExceptionID,
			ErrExceptionAccessDenied,
			"exception",
		)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrTenantIDNotFound)

		return c.SendStatus(fiber.StatusOK)
	})
}

func TestParseAndVerifyResourceScopedID_InvalidTenantID(t *testing.T) {
	t.Parallel()

	resourceID := uuid.New()
	badExtractor := func(ctx context.Context) string { return "zzz-invalid" }

	runInFiber(t, "/exceptions/:exceptionId", "/exceptions/"+resourceID.String(), func(c *fiber.Ctx) error {
		_, _, err := ParseAndVerifyResourceScopedID(
			c,
			"exceptionId",
			IDLocationParam,
			func(context.Context, uuid.UUID) error { return nil },
			badExtractor,
			ErrMissingExceptionID,
			ErrInvalidExceptionID,
			ErrExceptionAccessDenied,
			"exception",
		)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidTenantID)

		return c.SendStatus(fiber.StatusOK)
	})
}

func TestParseAndVerifyResourceScopedID_VerifierReturnsExceptionNotFound(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	resourceID := uuid.New()

	runInFiber(t, "/exceptions/:exceptionId", "/exceptions/"+resourceID.String(), func(c *fiber.Ctx) error {
		c.SetUserContext(context.WithValue(context.Background(), tenantKey{}, tenantID.String()))

		_, _, err := ParseAndVerifyResourceScopedID(
			c,
			"exceptionId",
			IDLocationParam,
			func(context.Context, uuid.UUID) error { return ErrExceptionNotFound },
			testTenantExtractor,
			ErrMissingExceptionID,
			ErrInvalidExceptionID,
			ErrExceptionAccessDenied,
			"exception",
		)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrExceptionNotFound)

		return c.SendStatus(fiber.StatusOK)
	})
}

func TestParseAndVerifyResourceScopedID_VerifierReturnsExceptionAccessDenied(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	resourceID := uuid.New()

	runInFiber(t, "/exceptions/:exceptionId", "/exceptions/"+resourceID.String(), func(c *fiber.Ctx) error {
		c.SetUserContext(context.WithValue(context.Background(), tenantKey{}, tenantID.String()))

		_, _, err := ParseAndVerifyResourceScopedID(
			c,
			"exceptionId",
			IDLocationParam,
			func(context.Context, uuid.UUID) error { return ErrExceptionAccessDenied },
			testTenantExtractor,
			ErrMissingExceptionID,
			ErrInvalidExceptionID,
			ErrExceptionAccessDenied,
			"exception",
		)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrExceptionAccessDenied)

		return c.SendStatus(fiber.StatusOK)
	})
}

func TestParseAndVerifyResourceScopedID_VerifierReturnsDisputeNotFound(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	resourceID := uuid.New()

	runInFiber(t, "/disputes/:disputeId", "/disputes/"+resourceID.String(), func(c *fiber.Ctx) error {
		c.SetUserContext(context.WithValue(context.Background(), tenantKey{}, tenantID.String()))

		_, _, err := ParseAndVerifyResourceScopedID(
			c,
			"disputeId",
			IDLocationParam,
			func(context.Context, uuid.UUID) error { return ErrDisputeNotFound },
			testTenantExtractor,
			ErrMissingDisputeID,
			ErrInvalidDisputeID,
			ErrDisputeAccessDenied,
			"dispute",
		)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrDisputeNotFound)

		return c.SendStatus(fiber.StatusOK)
	})
}

func TestParseAndVerifyResourceScopedID_VerifierReturnsDisputeAccessDenied(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	resourceID := uuid.New()

	runInFiber(t, "/disputes/:disputeId", "/disputes/"+resourceID.String(), func(c *fiber.Ctx) error {
		c.SetUserContext(context.WithValue(context.Background(), tenantKey{}, tenantID.String()))

		_, _, err := ParseAndVerifyResourceScopedID(
			c,
			"disputeId",
			IDLocationParam,
			func(context.Context, uuid.UUID) error { return ErrDisputeAccessDenied },
			testTenantExtractor,
			ErrMissingDisputeID,
			ErrInvalidDisputeID,
			ErrDisputeAccessDenied,
			"dispute",
		)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrDisputeAccessDenied)

		return c.SendStatus(fiber.StatusOK)
	})
}

func TestParseAndVerifyResourceScopedID_VerifierReturnsUnknownError(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	resourceID := uuid.New()

	runInFiber(t, "/exceptions/:exceptionId", "/exceptions/"+resourceID.String(), func(c *fiber.Ctx) error {
		c.SetUserContext(context.WithValue(context.Background(), tenantKey{}, tenantID.String()))

		_, _, err := ParseAndVerifyResourceScopedID(
			c,
			"exceptionId",
			IDLocationParam,
			func(context.Context, uuid.UUID) error { return errors.New("db exploded") },
			testTenantExtractor,
			ErrMissingExceptionID,
			ErrInvalidExceptionID,
			ErrExceptionAccessDenied,
			"exception",
		)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrLookupFailed)
		assert.Contains(t, err.Error(), "exception")
		assert.Contains(t, err.Error(), "db exploded")

		return c.SendStatus(fiber.StatusOK)
	})
}

func TestParseAndVerifyResourceScopedID_InvalidIDLocation(t *testing.T) {
	t.Parallel()

	resourceID := uuid.New()

	runInFiber(t, "/exceptions/:exceptionId", "/exceptions/"+resourceID.String(), func(c *fiber.Ctx) error {
		c.SetUserContext(context.WithValue(context.Background(), tenantKey{}, uuid.NewString()))

		_, _, err := ParseAndVerifyResourceScopedID(
			c,
			"exceptionId",
			IDLocation("cookie"),
			func(context.Context, uuid.UUID) error { return nil },
			testTenantExtractor,
			ErrMissingExceptionID,
			ErrInvalidExceptionID,
			ErrExceptionAccessDenied,
			"exception",
		)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidIDLocation)

		return c.SendStatus(fiber.StatusOK)
	})
}

// ---------------------------------------------------------------------------
// getIDValue
// ---------------------------------------------------------------------------

func TestGetIDValue_NilFiberContext(t *testing.T) {
	t.Parallel()

	_, err := getIDValue(nil, "id", IDLocationParam)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrContextNotFound)
}

func TestGetIDValue_Param(t *testing.T) {
	t.Parallel()

	resourceID := uuid.NewString()

	runInFiber(t, "/items/:id", "/items/"+resourceID, func(c *fiber.Ctx) error {
		val, err := getIDValue(c, "id", IDLocationParam)
		require.NoError(t, err)
		assert.Equal(t, resourceID, val)

		return c.SendStatus(fiber.StatusOK)
	})
}

func TestGetIDValue_Query(t *testing.T) {
	t.Parallel()

	resourceID := uuid.NewString()

	runInFiber(t, "/items", "/items?id="+resourceID, func(c *fiber.Ctx) error {
		val, err := getIDValue(c, "id", IDLocationQuery)
		require.NoError(t, err)
		assert.Equal(t, resourceID, val)

		return c.SendStatus(fiber.StatusOK)
	})
}

func TestGetIDValue_InvalidLocation(t *testing.T) {
	t.Parallel()

	runInFiber(t, "/items", "/items", func(c *fiber.Ctx) error {
		_, err := getIDValue(c, "id", IDLocation("header"))
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidIDLocation)

		return c.SendStatus(fiber.StatusOK)
	})
}

func TestGetIDValue_EmptyLocationString(t *testing.T) {
	t.Parallel()

	runInFiber(t, "/items", "/items", func(c *fiber.Ctx) error {
		_, err := getIDValue(c, "id", IDLocation(""))
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidIDLocation)

		return c.SendStatus(fiber.StatusOK)
	})
}

func TestGetIDValue_SpecialCharactersInQuery(t *testing.T) {
	t.Parallel()

	// Fiber URL-decodes query params, so %20 becomes a space.
	runInFiber(t, "/items", "/items?id=hello%20world", func(c *fiber.Ctx) error {
		val, err := getIDValue(c, "id", IDLocationQuery)
		require.NoError(t, err)
		assert.Equal(t, "hello world", val)

		return c.SendStatus(fiber.StatusOK)
	})
}

// ---------------------------------------------------------------------------
// classifyOwnershipError
// ---------------------------------------------------------------------------

func TestClassifyOwnershipError_AllSentinels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    error
		expected error
	}{
		{"context not found", ErrContextNotFound, ErrContextNotFound},
		{"context not owned", ErrContextNotOwned, ErrContextNotOwned},
		{"context not active", ErrContextNotActive, ErrContextNotActive},
		{"context access denied", ErrContextAccessDenied, ErrContextAccessDenied},
		{"wrapped context not found", fmt.Errorf("db: %w", ErrContextNotFound), ErrContextNotFound},
		{"wrapped context not owned", fmt.Errorf("db: %w", ErrContextNotOwned), ErrContextNotOwned},
		{"wrapped context not active", fmt.Errorf("db: %w", ErrContextNotActive), ErrContextNotActive},
		{"wrapped context access denied", fmt.Errorf("db: %w", ErrContextAccessDenied), ErrContextAccessDenied},
		{"unknown error", errors.New("something else"), ErrContextLookupFailed},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := classifyOwnershipError(tc.input, nil)
			assert.ErrorIs(t, err, tc.expected)
		})
	}
}

func TestClassifyOwnershipError_UnknownErrorPreservesOriginal(t *testing.T) {
	t.Parallel()

	originalErr := errors.New("network timeout")
	err := classifyOwnershipError(originalErr, nil)
	assert.ErrorIs(t, err, ErrContextLookupFailed)
	assert.Contains(t, err.Error(), "network timeout")
}

// ---------------------------------------------------------------------------
// classifyOwnershipError with non-nil accessErr
// ---------------------------------------------------------------------------

func TestClassifyOwnershipError_WithAccessErr_NotOwned(t *testing.T) {
	t.Parallel()

	customErr := errors.New("custom access denied")
	err := classifyOwnershipError(ErrContextNotOwned, customErr)
	assert.Equal(t, customErr, err)
}

func TestClassifyOwnershipError_WithAccessErr_AccessDenied(t *testing.T) {
	t.Parallel()

	customErr := errors.New("custom forbidden")
	err := classifyOwnershipError(ErrContextAccessDenied, customErr)
	assert.Equal(t, customErr, err)
}

func TestClassifyOwnershipError_WithAccessErr_NotFound(t *testing.T) {
	t.Parallel()

	// For not-found, accessErr is irrelevant -- returns ErrContextNotFound
	customErr := errors.New("custom err")
	err := classifyOwnershipError(ErrContextNotFound, customErr)
	assert.ErrorIs(t, err, ErrContextNotFound)
}

func TestClassifyOwnershipError_WithAccessErr_NotActive(t *testing.T) {
	t.Parallel()

	// For not-active, accessErr is irrelevant
	customErr := errors.New("custom err")
	err := classifyOwnershipError(ErrContextNotActive, customErr)
	assert.ErrorIs(t, err, ErrContextNotActive)
}

func TestClassifyOwnershipError_WithAccessErr_Unknown(t *testing.T) {
	t.Parallel()

	// For unknown errors, accessErr is irrelevant
	customErr := errors.New("custom err")
	err := classifyOwnershipError(errors.New("db timeout"), customErr)
	assert.ErrorIs(t, err, ErrContextLookupFailed)
}

// ---------------------------------------------------------------------------
// classifyResourceOwnershipError
// ---------------------------------------------------------------------------

func TestClassifyResourceOwnershipError_AllSentinels(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		label    string
		input    error
		expected error
	}{
		{"exception not found", "exception", ErrExceptionNotFound, ErrExceptionNotFound},
		{"exception access denied", "exception", ErrExceptionAccessDenied, ErrExceptionAccessDenied},
		{"dispute not found", "dispute", ErrDisputeNotFound, ErrDisputeNotFound},
		{"dispute access denied", "dispute", ErrDisputeAccessDenied, ErrDisputeAccessDenied},
		{"unknown error", "exception", errors.New("oops"), ErrLookupFailed},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := classifyResourceOwnershipError(tc.label, tc.input, nil)
			assert.ErrorIs(t, err, tc.expected)
		})
	}
}

// ---------------------------------------------------------------------------
// ResourceErrorMapping registry
// ---------------------------------------------------------------------------

func snapshotResourceRegistry() []ResourceErrorMapping {
	registryMu.RLock()
	defer registryMu.RUnlock()

	snapshot := make([]ResourceErrorMapping, len(resourceErrorRegistry))
	copy(snapshot, resourceErrorRegistry)

	return snapshot
}

func restoreResourceRegistry(snapshot []ResourceErrorMapping) {
	registryMu.Lock()
	defer registryMu.Unlock()

	resourceErrorRegistry = make([]ResourceErrorMapping, len(snapshot))
	copy(resourceErrorRegistry, snapshot)
}

func TestRegisterResourceErrors_CustomMapping(t *testing.T) {
	original := snapshotResourceRegistry()
	t.Cleanup(func() {
		restoreResourceRegistry(original)
	})

	// Define custom resource errors
	errInvoiceNotFound := errors.New("invoice not found")
	errInvoiceAccessDenied := errors.New("invoice access denied")

	// Register custom mappings for this test.
	RegisterResourceErrors(ResourceErrorMapping{
		NotFoundErr:     errInvoiceNotFound,
		AccessDeniedErr: errInvoiceAccessDenied,
	})

	// classifyResourceOwnershipError should recognize the new mapping
	err := classifyResourceOwnershipError("invoice", errInvoiceNotFound, nil)
	assert.ErrorIs(t, err, errInvoiceNotFound)

	err = classifyResourceOwnershipError("invoice", errInvoiceAccessDenied, nil)
	assert.ErrorIs(t, err, errInvoiceAccessDenied)
}

func TestClassifyResourceOwnershipError_WithAccessErr_ReturnsAccessErr(t *testing.T) {
	t.Parallel()

	customAccessErr := errors.New("custom forbidden for exception")

	// When verifier returns ErrExceptionAccessDenied and we provide a custom accessErr,
	// classifyResourceOwnershipError should return the custom accessErr.
	err := classifyResourceOwnershipError("exception", ErrExceptionAccessDenied, customAccessErr)
	assert.Equal(t, customAccessErr, err)
}

func TestClassifyResourceOwnershipError_WithAccessErr_NotFoundIgnoresAccessErr(t *testing.T) {
	t.Parallel()

	customAccessErr := errors.New("custom denied")

	// For not-found errors, accessErr is ignored -- the original error is returned.
	err := classifyResourceOwnershipError("exception", ErrExceptionNotFound, customAccessErr)
	assert.ErrorIs(t, err, ErrExceptionNotFound)
}

func TestClassifyResourceOwnershipError_LabelInMessage(t *testing.T) {
	t.Parallel()

	err := classifyResourceOwnershipError("my_resource", errors.New("db failure"), nil)
	assert.ErrorIs(t, err, ErrLookupFailed)
	assert.Contains(t, err.Error(), "my_resource")
	assert.Contains(t, err.Error(), "db failure")
}

// ---------------------------------------------------------------------------
// SetHandlerSpanAttributes
// ---------------------------------------------------------------------------

type mockSpan struct {
	trace.Span
	attrs []attribute.KeyValue
}

func (m *mockSpan) SetAttributes(kv ...attribute.KeyValue) {
	m.attrs = append(m.attrs, kv...)
}

func (m *mockSpan) findAttr(key string) (attribute.KeyValue, bool) {
	for _, a := range m.attrs {
		if string(a.Key) == key {
			return a, true
		}
	}
	return attribute.KeyValue{}, false
}

func TestSetHandlerSpanAttributes_AllFields(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	contextID := uuid.New()

	span := &mockSpan{}
	SetHandlerSpanAttributes(span, tenantID, contextID)

	tenantAttr, ok := span.findAttr("tenant.id")
	require.True(t, ok)
	assert.Equal(t, tenantID.String(), tenantAttr.Value.AsString())

	ctxAttr, ok := span.findAttr("context.id")
	require.True(t, ok)
	assert.Equal(t, contextID.String(), ctxAttr.Value.AsString())
}

func TestSetHandlerSpanAttributes_NilContextID(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()

	span := &mockSpan{}
	SetHandlerSpanAttributes(span, tenantID, uuid.Nil)

	tenantAttr, ok := span.findAttr("tenant.id")
	require.True(t, ok)
	assert.Equal(t, tenantID.String(), tenantAttr.Value.AsString())

	_, ok = span.findAttr("context.id")
	assert.False(t, ok, "context.id should not be set when contextID is uuid.Nil")
}

func TestSetHandlerSpanAttributes_NilSpan(t *testing.T) {
	t.Parallel()

	// Should not panic.
	SetHandlerSpanAttributes(nil, uuid.New(), uuid.New())
}

// ---------------------------------------------------------------------------
// SetTenantSpanAttribute
// ---------------------------------------------------------------------------

func TestSetTenantSpanAttribute_HappyPath(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	span := &mockSpan{}

	SetTenantSpanAttribute(span, tenantID)

	tenantAttr, ok := span.findAttr("tenant.id")
	require.True(t, ok)
	assert.Equal(t, tenantID.String(), tenantAttr.Value.AsString())
}

func TestSetTenantSpanAttribute_NilSpan(t *testing.T) {
	t.Parallel()

	// Should not panic.
	SetTenantSpanAttribute(nil, uuid.New())
}

// ---------------------------------------------------------------------------
// SetExceptionSpanAttributes
// ---------------------------------------------------------------------------

func TestSetExceptionSpanAttributes_HappyPath(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	exceptionID := uuid.New()
	span := &mockSpan{}

	SetExceptionSpanAttributes(span, tenantID, exceptionID)

	tenantAttr, ok := span.findAttr("tenant.id")
	require.True(t, ok)
	assert.Equal(t, tenantID.String(), tenantAttr.Value.AsString())

	exAttr, ok := span.findAttr("exception.id")
	require.True(t, ok)
	assert.Equal(t, exceptionID.String(), exAttr.Value.AsString())
}

func TestSetExceptionSpanAttributes_NilSpan(t *testing.T) {
	t.Parallel()

	// Should not panic.
	SetExceptionSpanAttributes(nil, uuid.New(), uuid.New())
}

// ---------------------------------------------------------------------------
// SetDisputeSpanAttributes
// ---------------------------------------------------------------------------

func TestSetDisputeSpanAttributes_HappyPath(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	disputeID := uuid.New()
	span := &mockSpan{}

	SetDisputeSpanAttributes(span, tenantID, disputeID)

	tenantAttr, ok := span.findAttr("tenant.id")
	require.True(t, ok)
	assert.Equal(t, tenantID.String(), tenantAttr.Value.AsString())

	dAttr, ok := span.findAttr("dispute.id")
	require.True(t, ok)
	assert.Equal(t, disputeID.String(), dAttr.Value.AsString())
}

func TestSetDisputeSpanAttributes_NilSpan(t *testing.T) {
	t.Parallel()

	// Should not panic.
	SetDisputeSpanAttributes(nil, uuid.New(), uuid.New())
}

// ---------------------------------------------------------------------------
// IDLocation constants
// ---------------------------------------------------------------------------

func TestIDLocationConstants(t *testing.T) {
	t.Parallel()

	assert.Equal(t, IDLocation("param"), IDLocationParam)
	assert.Equal(t, IDLocation("query"), IDLocationQuery)
}

// ---------------------------------------------------------------------------
// Sentinel error identity
// ---------------------------------------------------------------------------

func TestSentinelErrorIdentity(t *testing.T) {
	t.Parallel()

	// Ensure all sentinel errors are distinct.
	sentinels := []error{
		ErrInvalidIDLocation,
		ErrMissingContextID,
		ErrInvalidContextID,
		ErrTenantIDNotFound,
		ErrTenantExtractorNil,
		ErrInvalidTenantID,
		ErrContextNotFound,
		ErrContextNotOwned,
		ErrContextAccessDenied,
		ErrContextNotActive,
		ErrContextLookupFailed,
		ErrLookupFailed,
		ErrMissingExceptionID,
		ErrInvalidExceptionID,
		ErrExceptionNotFound,
		ErrExceptionAccessDenied,
		ErrMissingDisputeID,
		ErrInvalidDisputeID,
		ErrDisputeNotFound,
		ErrDisputeAccessDenied,
	}

	for i, a := range sentinels {
		for j, b := range sentinels {
			if i != j {
				assert.NotEqual(t, a.Error(), b.Error(),
					"sentinel errors %d and %d have identical messages: %q", i, j, a.Error())
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Edge: special characters in param values
// ---------------------------------------------------------------------------

func TestParseAndVerifyTenantScopedID_UUIDWithUpperCase(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	contextID := uuid.New()
	// UUID strings are case-insensitive; pass an uppercase version.
	upperContextID := strings.ToUpper(contextID.String())

	runInFiber(t, "/contexts/:contextId", "/contexts/"+upperContextID, func(c *fiber.Ctx) error {
		c.SetUserContext(context.WithValue(context.Background(), tenantKey{}, tenantID.String()))

		gotContextID, gotTenantID, err := ParseAndVerifyTenantScopedID(
			c,
			"contextId",
			IDLocationParam,
			func(ctx context.Context, tID, resourceID uuid.UUID) error { return nil },
			testTenantExtractor,
			ErrMissingContextID,
			ErrInvalidContextID,
			ErrContextAccessDenied,
		)
		require.NoError(t, err)
		assert.Equal(t, contextID, gotContextID)
		assert.Equal(t, tenantID, gotTenantID)

		return c.SendStatus(fiber.StatusOK)
	})
}

// ---------------------------------------------------------------------------
// parseTenantAndResourceID returns correct context.Context to verifier
// ---------------------------------------------------------------------------

func TestParseTenantAndResourceID_ContextPassedToVerifier(t *testing.T) {
	t.Parallel()

	type ctxValueKey struct{}
	tenantID := uuid.New()
	resourceID := uuid.New()
	contextValue := "custom-value-12345"

	runInFiber(t, "/contexts/:contextId", "/contexts/"+resourceID.String(), func(c *fiber.Ctx) error {
		userCtx := context.WithValue(context.Background(), tenantKey{}, tenantID.String())
		userCtx = context.WithValue(userCtx, ctxValueKey{}, contextValue)
		c.SetUserContext(userCtx)

		_, _, err := ParseAndVerifyTenantScopedID(
			c,
			"contextId",
			IDLocationParam,
			func(ctx context.Context, tID, resID uuid.UUID) error {
				// Verify the context passed to verifier contains our custom value.
				val, ok := ctx.Value(ctxValueKey{}).(string)
				require.True(t, ok)
				assert.Equal(t, contextValue, val)
				return nil
			},
			testTenantExtractor,
			ErrMissingContextID,
			ErrInvalidContextID,
			ErrContextAccessDenied,
		)
		require.NoError(t, err)

		return c.SendStatus(fiber.StatusOK)
	})
}

// ---------------------------------------------------------------------------
// Dispute-specific scoped ID tests
// ---------------------------------------------------------------------------

func TestParseAndVerifyResourceScopedID_DisputeHappyPath(t *testing.T) {
	t.Parallel()

	tenantID := uuid.New()
	disputeID := uuid.New()

	runInFiber(t, "/disputes/:disputeId", "/disputes/"+disputeID.String(), func(c *fiber.Ctx) error {
		c.SetUserContext(context.WithValue(context.Background(), tenantKey{}, tenantID.String()))

		gotID, gotTenantID, err := ParseAndVerifyResourceScopedID(
			c,
			"disputeId",
			IDLocationParam,
			func(ctx context.Context, resourceID uuid.UUID) error {
				if resourceID != disputeID {
					return ErrDisputeNotFound
				}
				return nil
			},
			testTenantExtractor,
			ErrMissingDisputeID,
			ErrInvalidDisputeID,
			ErrDisputeAccessDenied,
			"dispute",
		)
		require.NoError(t, err)
		assert.Equal(t, disputeID, gotID)
		assert.Equal(t, tenantID, gotTenantID)

		return c.SendStatus(fiber.StatusOK)
	})
}
