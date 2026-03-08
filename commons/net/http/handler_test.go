//go:build unit

package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFileHandler(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/file", File("../../../go.mod"))

	req := httptest.NewRequest(http.MethodGet, "/file", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { assert.NoError(t, resp.Body.Close()) }()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
}
