//go:build unit

package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LerianStudio/lib-commons/v7/commons/server"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// getAdmin issues target against a fresh admin app and returns the response
// plus its decoded body (nil when the body is not JSON).
func getAdmin(t *testing.T, service, target string) (*http.Response, map[string]any) {
	t.Helper()

	resp, err := server.NewAdminApp(service).Test(httptest.NewRequest(http.MethodGet, target, nil))
	require.NoError(t, err)

	t.Cleanup(func() { require.NoError(t, resp.Body.Close()) })

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	decoded := map[string]any{}
	if json.Unmarshal(body, &decoded) != nil {
		return resp, nil
	}

	return resp, decoded
}

func TestNewAdminAppServesTheVersionEndpoint(t *testing.T) {
	t.Parallel()

	resp, decoded := getAdmin(t, "midaz-ledger", "/version")

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t,
		strings.HasPrefix(resp.Header.Get(fiber.HeaderContentType), fiber.MIMEApplicationJSON),
		"content type must be JSON, got %q", resp.Header.Get(fiber.HeaderContentType))

	require.NotNil(t, decoded)
	assert.Equal(t, "v1", decoded["schemaVersion"])
	assert.Equal(t, "midaz-ledger", decoded["service"], "service comes from the NewAdminApp argument")
	assert.Contains(t, decoded, "dependencyManifest")
}

func TestNewAdminAppWidensTheManifestWithFull(t *testing.T) {
	t.Parallel()

	_, decoded := getAdmin(t, "midaz-ledger", "/version?full=1")

	require.NotNil(t, decoded)

	manifest, ok := decoded["dependencyManifest"].(map[string]any)
	require.True(t, ok, "dependencyManifest must be an object")
	assert.Equal(t, "full", manifest["scope"])
}

func TestNewAdminAppMountsNothingElse(t *testing.T) {
	t.Parallel()

	// /health, /readyz and /metrics belong to the service, not to this app.
	for _, path := range []string{"/health", "/readyz", "/metrics", "/"} {
		resp, _ := getAdmin(t, "midaz-ledger", path)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, "path %s must not be mounted", path)
	}
}
