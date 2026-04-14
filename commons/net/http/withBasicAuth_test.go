//go:build unit

package http

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	constant "github.com/LerianStudio/lib-commons/v5/commons/constants"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithBasicAuth_NilAuthFunc(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/", WithBasicAuth(nil, "realm"), func(c *fiber.Ctx) error {
		return c.SendStatus(http.StatusOK)
	})

	cred := base64.StdEncoding.EncodeToString([]byte("user:pass"))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(constant.Authorization, "Basic "+cred)

	res, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, res.Body.Close()) }()

	assert.Equal(t, http.StatusUnauthorized, res.StatusCode)
}

func TestWithBasicAuth_SanitizesRealmHeader(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/", WithBasicAuth(FixedBasicAuthFunc("user", "pass"), "safe\r\nrealm\"name"), func(c *fiber.Ctx) error {
		return c.SendStatus(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	res, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, res.Body.Close()) }()

	assert.Equal(t, `Basic realm="saferealmname"`, res.Header.Get(constant.WWWAuthenticate))
}

func TestWithBasicAuth_AllowsValidCredentials(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/", WithBasicAuth(FixedBasicAuthFunc("user", "pass"), "realm"), func(c *fiber.Ctx) error {
		return c.SendStatus(http.StatusOK)
	})

	cred := base64.StdEncoding.EncodeToString([]byte("user:pass"))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(constant.Authorization, "Basic "+cred)

	res, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, res.Body.Close()) }()

	assert.Equal(t, http.StatusOK, res.StatusCode)
}

func TestWithBasicAuth_RejectsMalformedAuthorization(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/", WithBasicAuth(FixedBasicAuthFunc("user", "pass"), "realm"), func(c *fiber.Ctx) error {
		return c.SendStatus(http.StatusOK)
	})

	testCases := []struct {
		name   string
		header string
	}{
		{name: "wrong scheme", header: "Bearer token"},
		{name: "invalid base64", header: "Basic !!!"},
		{name: "missing colon", header: "Basic " + base64.StdEncoding.EncodeToString([]byte("userpass"))},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set(constant.Authorization, tc.header)

			res, err := app.Test(req)
			require.NoError(t, err)
			defer func() { require.NoError(t, res.Body.Close()) }()

			assert.Equal(t, http.StatusUnauthorized, res.StatusCode)
		})
	}
}
