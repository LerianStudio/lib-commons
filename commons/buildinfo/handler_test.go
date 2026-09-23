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

func manifestOf(t *testing.T, decoded map[string]any) map[string]any {
	t.Helper()

	manifest, ok := decoded["dependencyManifest"].(map[string]any)
	require.True(t, ok, "dependencyManifest must be an object")

	return manifest
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
	assert.Len(t, decoded, 8, "FC-3 has exactly eight top-level keys")

	manifest := manifestOf(t, decoded)
	assert.Equal(t, "go-buildinfo-v1", manifest["format"])
	assert.Equal(t, "lerian", manifest["scope"])
	assert.NotNil(t, manifest["modules"], "modules is an empty array, never null")
	assert.Len(t, manifest, 3)
}

// TestHandlerKeepsAnEmptyServiceField reads the package-level injected Build, a
// process-global a sibling test writes: no t.Parallel().
func TestHandlerKeepsAnEmptyServiceField(t *testing.T) {
	_, decoded := getVersion(t, "", "/version")

	value, ok := decoded["service"]
	assert.True(t, ok, "service is always present on the endpoint")
	assert.Equal(t, "", value)
}

func TestHandlerScopeFromQuery(t *testing.T) {
	tests := []struct {
		target string
		want   string
	}{
		{"/version", "lerian"},
		{"/version?full=1", "full"},
		{"/version?full=true", "full"},
		{"/version?full=0", "lerian"},
		{"/version?full=yes", "lerian"},
		{"/version?full=", "lerian"},
	}

	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			t.Parallel()

			_, decoded := getVersion(t, "midaz-ledger", tt.target)
			assert.Equal(t, tt.want, manifestOf(t, decoded)["scope"])
		})
	}
}

func TestHandlerModulesUseContractKeys(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(manifest{
		Format: manifestFormat,
		Scope:  scopeFull,
		Modules: []Module{
			{Path: "github.com/LerianStudio/lib-commons/v7", Version: "v7.4.0", Sum: "h1:abc"},
			{
				Path:    "github.com/LerianStudio/lib-observability/v4",
				Version: "v4.1.0",
				Replace: &Module{Path: "../lib-observability", Version: "(devel)"},
			},
		},
	})
	require.NoError(t, err)

	decoded := map[string]any{}
	require.NoError(t, json.Unmarshal(encoded, &decoded))

	modules, ok := decoded["modules"].([]any)
	require.True(t, ok, "modules must be an array")
	require.Len(t, modules, 2)

	linked, ok := modules[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, map[string]any{
		"path":    "github.com/LerianStudio/lib-commons/v7",
		"version": "v7.4.0",
		"sum":     "h1:abc",
	}, linked, "an unreplaced module carries no replace object")

	replaced, ok := modules[1].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, map[string]any{
		"path":    "github.com/LerianStudio/lib-observability/v4",
		"version": "v4.1.0",
		"replace": map[string]any{"path": "../lib-observability", "version": "(devel)"},
	}, replaced, "a module without a sum omits the key, as does a replace target")
}

func TestHandlerServesAnEmptyManifestAsAnArray(t *testing.T) {
	_, decoded := getVersion(t, "midaz-ledger", "/version")

	assert.Equal(t, []any{}, manifestOf(t, decoded)["modules"],
		"a binary with no linked Lerian module answers [], never null")
}
