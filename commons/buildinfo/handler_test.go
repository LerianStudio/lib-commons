//go:build unit

package buildinfo

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func getVersion(t *testing.T, service, target string) (*http.Response, map[string]any) {
	t.Helper()

	app := fiber.New()
	app.Get("/version", Handler(service))

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, target, nil))
	require.NoError(t, err)

	t.Cleanup(func() { require.NoError(t, resp.Body.Close()) })

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	decoded := map[string]any{}
	require.NoError(t, json.Unmarshal(body, &decoded))

	return resp, decoded
}

// TestHandlerRespondsTheCompiledIdentity mutates the package-level injected
// Build, a process-global: no t.Parallel(), and Cleanup puts the zero value
// back.
func TestHandlerRespondsTheCompiledIdentity(t *testing.T) {
	t.Cleanup(func() { Set(Build{}) })

	Set(Build{Version: "4.0.3", Revision: "cafebabe", BuildTime: "2026-09-22T19:00:00Z"})

	resp, decoded := getVersion(t, "midaz-ledger", "/version")

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.True(t,
		strings.HasPrefix(resp.Header.Get(fiber.HeaderContentType), fiber.MIMEApplicationJSON),
		"content type must be JSON, got %q", resp.Header.Get(fiber.HeaderContentType))

	assert.Equal(t, "v1", decoded["schemaVersion"])
	assert.Equal(t, "midaz-ledger", decoded["service"])
	assert.Equal(t, "4.0.3", decoded["version"])
	assert.Equal(t, "cafebabe", decoded["revision"])
	assert.Equal(t, "2026-09-22T19:00:00Z", decoded["buildTime"])
	// The test binary carries no dirty VCS stamp.
	assert.Equal(t, false, decoded["modified"])
	assert.Equal(t, runtime.Version(), decoded["goVersion"])
	assert.Len(t, decoded, 7, "the endpoint serves the identity and nothing else")
	assert.NotContains(t, decoded, "dependencyManifest",
		"the manifest leaves the process only through --version")
}

// TestHandlerKeepsAnEmptyServiceField reads the package-level injected Build, a
// process-global a sibling test writes: no t.Parallel().
func TestHandlerKeepsAnEmptyServiceField(t *testing.T) {
	_, decoded := getVersion(t, "", "/version")

	value, ok := decoded["service"]
	assert.True(t, ok, "service is always present on the endpoint")
	assert.Equal(t, "", value)
}

// TestHandlerIgnoresTheFullQuery pins that no query parameter can pull the
// dependency manifest out over HTTP.
func TestHandlerIgnoresTheFullQuery(t *testing.T) {
	for _, target := range []string{"/version?full=1", "/version?full=true"} {
		t.Run(target, func(t *testing.T) {
			t.Parallel()

			_, decoded := getVersion(t, "midaz-ledger", target)
			assert.Len(t, decoded, 7)
			assert.NotContains(t, decoded, "dependencyManifest")
		})
	}
}
