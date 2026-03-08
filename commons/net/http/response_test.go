//go:build unit

package http

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRespond_NegativeStatus(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/", func(c *fiber.Ctx) error {
		return Respond(c, -1, fiber.Map{"ok": true})
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
}

func TestRespond_Status599IsValid(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/", func(c *fiber.Ctx) error {
		return Respond(c, 599, fiber.Map{"ok": true})
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	assert.Equal(t, 599, resp.StatusCode)
}

func TestRespond_Status100IsValid(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/", func(c *fiber.Ctx) error {
		return Respond(c, http.StatusContinue, fiber.Map{"data": "x"})
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	assert.Equal(t, http.StatusContinue, resp.StatusCode)
}

func TestRespond_EmptyPayload(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/", func(c *fiber.Ctx) error {
		return Respond(c, http.StatusOK, fiber.Map{})
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)

	var result map[string]any
	err = json.NewDecoder(resp.Body).Decode(&result)
	require.NoError(t, err)
	assert.Empty(t, result)
}

func TestRespondStatus_Status600ClampedTo500(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/", func(c *fiber.Ctx) error {
		return RespondStatus(c, 600)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	assert.Equal(t, http.StatusInternalServerError, resp.StatusCode)
}

// ---------------------------------------------------------------------------
// Nil guard tests
// ---------------------------------------------------------------------------

func TestRespond_NilContext(t *testing.T) {
	t.Parallel()

	err := Respond(nil, 200, fiber.Map{"ok": true})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrContextNotFound)
}

func TestRespondStatus_NilContext(t *testing.T) {
	t.Parallel()

	err := RespondStatus(nil, 200)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrContextNotFound)
}

func TestRespondStatus_NoContent(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Delete("/", func(c *fiber.Ctx) error {
		return RespondStatus(c, http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodDelete, "/", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	assert.Equal(t, http.StatusNoContent, resp.StatusCode)
}
