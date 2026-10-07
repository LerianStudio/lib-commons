//go:build unit

package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v3"
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

func TestExtractTokenFromAuthorization(t *testing.T) {
	t.Parallel()

	for header, want := range map[string]string{
		"Bearer my-jwt":      "my-jwt",
		"bearer my-jwt":      "my-jwt",
		"  Bearer   my-jwt ": "my-jwt",
		"raw-token":          "raw-token",
		"":                   "",
		"   ":                "",
		"Bearer":             "",
		"Bearer a b":         "",
		"Basic abc":          "",
	} {
		assert.Equal(t, want, ExtractTokenFromAuthorization(header), "header %q", header)
	}
}
