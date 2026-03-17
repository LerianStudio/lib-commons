//go:build unit

package http

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHealthWithDependencies_EmptyDependencyName(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/health", HealthWithDependencies(DependencyCheck{Name: ""}))

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/health", nil))
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

	body := readHealthConfigBody(t, resp)
	var result map[string]any
	require.NoError(t, json.Unmarshal(body, &result))
	assert.Equal(t, "degraded", result["status"])
	assert.Equal(t, ErrEmptyDependencyName.Error(), result["error"])
	_, hasMessage := result["message"]
	_, hasDependencies := result["dependencies"]
	assert.False(t, hasMessage)
	assert.False(t, hasDependencies)
	assert.Len(t, result, 2)
}

func TestHealthWithDependencies_DuplicateDependencyName(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/health", HealthWithDependencies(
		DependencyCheck{Name: "database"},
		DependencyCheck{Name: "database"},
	))

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/health", nil))
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

	body := readHealthConfigBody(t, resp)
	var result map[string]any
	require.NoError(t, json.Unmarshal(body, &result))
	assert.Equal(t, "degraded", result["status"])
	assert.Equal(t, ErrDuplicateDependencyName.Error(), result["error"])
	_, hasMessage := result["message"]
	_, hasDependencies := result["dependencies"]
	assert.False(t, hasMessage)
	assert.False(t, hasDependencies)
	assert.Len(t, result, 2)
}

func TestHealthWithDependencies_CBWithoutServiceName_ConfigPayload(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/health", HealthWithDependencies(
		DependencyCheck{Name: "database", CircuitBreaker: &mockCBManager{}},
	))

	resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/health", nil))
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	assert.Equal(t, http.StatusServiceUnavailable, resp.StatusCode)

	body := readHealthConfigBody(t, resp)
	var result map[string]any
	require.NoError(t, json.Unmarshal(body, &result))
	assert.Equal(t, "degraded", result["status"])
	assert.Equal(t, ErrCBWithoutServiceName.Error(), result["error"])
	_, hasMessage := result["message"]
	_, hasDependencies := result["dependencies"]
	assert.False(t, hasMessage)
	assert.False(t, hasDependencies)
	assert.Len(t, result, 2)
}

func readHealthConfigBody(t *testing.T, resp *http.Response) []byte {
	t.Helper()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return body
}
