//go:build unit

package openapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/LerianStudio/lib-commons/v5/commons/net/http/problem"
	libLog "github.com/LerianStudio/lib-observability/log"
	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// echoInput / echoOutput back a throwaway operation used to force schema and
// route generation without pulling in any bounded-context type.
type echoInput struct {
	Body struct {
		Name string `json:"name"`
	}
}

type echoOutput struct {
	Body struct {
		Name string `json:"name"`
	}
}

func registerEcho(api huma.API) {
	huma.Register(api, huma.Operation{
		OperationID: "echo",
		Method:      http.MethodPost,
		Path:        "/echo",
	}, func(_ context.Context, in *echoInput) (*echoOutput, error) {
		out := &echoOutput{}
		out.Body.Name = in.Body.Name
		return out, nil
	})
}

func testConfig() Config {
	return Config{
		Title:       "Test API",
		Version:     "1.0.0",
		Description: "test description",
		Servers:     []string{"https://api.example.com", "https://api.staging.example.com"},
	}
}

// doReq runs a request through the real Fiber app and returns status + body.
func doReq(t *testing.T, app *fiber.App, method, path string) (int, string) {
	t.Helper()

	resp, err := app.Test(httptest.NewRequest(method, path, nil))
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return resp.StatusCode, string(body)
}

// TestNew_SuppressesAutoMount proves the wrapper registers NO spec/docs routes:
// /openapi.json and /docs on the group root must 404, even after an operation is
// registered.
func TestNew_SuppressesAutoMount(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	api := New(app, app.Group("/"), testConfig())
	registerEcho(api)

	for _, p := range []string{"/openapi.json", "/openapi.yaml", "/docs", "/openapi"} {
		status, _ := doReq(t, app, http.MethodGet, p)
		assert.Equalf(t, http.StatusNotFound, status, "auto-mount must be suppressed for %s", p)
	}
}

// TestNew_ServersPopulated proves supplied servers land in the spec.
func TestNew_ServersPopulated(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	api := New(app, app.Group("/"), testConfig())

	servers := api.OpenAPI().Servers
	require.Len(t, servers, 2)
	assert.Equal(t, "https://api.example.com", servers[0].URL)
	assert.Equal(t, "https://api.staging.example.com", servers[1].URL)

	assert.Equal(t, "Test API", api.OpenAPI().Info.Title)
	assert.Equal(t, "1.0.0", api.OpenAPI().Info.Version)
	assert.Equal(t, "test description", api.OpenAPI().Info.Description)
}

// TestNew_NoServers proves the empty-servers branch leaves Servers unset.
func TestNew_NoServers(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	cfg := testConfig()
	cfg.Servers = nil
	api := New(app, app.Group("/"), cfg)

	assert.Empty(t, api.OpenAPI().Servers)
}

// TestNew_NoSchemaLeak proves the SchemaLinkTransformer is stripped: response
// bodies serialize exactly as written, with no injected `$schema` field.
func TestNew_NoSchemaLeak(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	api := New(app, app.Group("/"), testConfig())
	registerEcho(api)

	req := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader(`{"name":"abc"}`))
	req.Header.Set("Content-Type", "application/json")

	resp, err := app.Test(req)
	require.NoError(t, err)

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	require.Equal(t, http.StatusOK, resp.StatusCode, "body=%s", body)

	var m map[string]any
	require.NoError(t, json.Unmarshal(body, &m))

	_, hasSchema := m["$schema"]
	assert.False(t, hasSchema, "Transformers must be cleared; got $schema leak: %s", body)
	assert.Equal(t, "abc", m["name"])
}

// TestDeclareBearerAuth_AddsScheme proves the BearerAuth scheme is registered.
func TestDeclareBearerAuth_AddsScheme(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	api := New(app, app.Group("/"), testConfig())

	DeclareBearerAuth(api)

	scheme := api.OpenAPI().Components.SecuritySchemes["BearerAuth"]
	require.NotNil(t, scheme)
	assert.Equal(t, "http", scheme.Type)
	assert.Equal(t, "bearer", scheme.Scheme)
	assert.Equal(t, "JWT", scheme.BearerFormat)
	assert.NotEmpty(t, scheme.Description)
}

// TestDeclareBearerAuth_Idempotent proves a second declaration is a no-op
// overwrite (still exactly one BearerAuth scheme).
func TestDeclareBearerAuth_Idempotent(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	api := New(app, app.Group("/"), testConfig())

	DeclareBearerAuth(api)
	DeclareBearerAuth(api)

	assert.Len(t, api.OpenAPI().Components.SecuritySchemes, 1)
	assert.NotNil(t, api.OpenAPI().Components.SecuritySchemes["BearerAuth"])
}

// TestDeclareBearerAuth_NilSafe proves nil api does not panic.
func TestDeclareBearerAuth_NilSafe(t *testing.T) {
	t.Parallel()

	assert.NotPanics(t, func() { DeclareBearerAuth(nil) })
}

// TestServeSpec_MountsThreeRoutes proves the three spec/docs routes mount under
// prefix and serve the expected content types.
func TestServeSpec_MountsThreeRoutes(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	api := New(app, app.Group("/"), testConfig())
	registerEcho(api)

	ServeSpec(app, api, &libLog.NopLogger{}, "/v1", "Test Docs")

	t.Run("openapi.json", func(t *testing.T) {
		t.Parallel()

		resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/v1/openapi.json", nil))
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Contains(t, resp.Header.Get(fiber.HeaderContentType), "application/json")

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)

		var spec map[string]any
		require.NoError(t, json.Unmarshal(body, &spec))
		assert.Contains(t, spec, "openapi")
	})

	t.Run("openapi.yaml", func(t *testing.T) {
		t.Parallel()

		resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/v1/openapi.yaml", nil))
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Contains(t, resp.Header.Get(fiber.HeaderContentType), "application/yaml")

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Contains(t, string(body), "openapi:")
	})

	t.Run("docs with relaxed CSP", func(t *testing.T) {
		t.Parallel()

		resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/v1/docs", nil))
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Contains(t, resp.Header.Get(fiber.HeaderContentType), "text/html")
		assert.Equal(t, scalarCSP, resp.Header.Get("Content-Security-Policy"))

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Contains(t, string(body), "Test Docs")
		assert.Contains(t, string(body), "/v1/openapi.json")
		assert.Contains(t, string(body), "@scalar/api-reference")
	})
}

// TestServeSpec_NilAPI proves nil api mounts nothing and does not panic.
func TestServeSpec_NilAPI(t *testing.T) {
	t.Parallel()

	app := fiber.New()

	assert.NotPanics(t, func() {
		ServeSpec(app, nil, &libLog.NopLogger{}, "/v1", "Test Docs")
	})

	status, _ := doReq(t, app, http.MethodGet, "/v1/openapi.json")
	assert.Equal(t, http.StatusNotFound, status, "no routes when api is nil")
}

// recordingLogger captures the last Log call so the skip-on-failure path can be
// asserted.
type recordingLogger struct {
	libLog.NopLogger
	called bool
	msg    string
}

func (l *recordingLogger) Log(_ context.Context, _ libLog.Level, msg string, _ ...libLog.Field) {
	l.called = true
	l.msg = msg
}

// TestServeSpec_RenderFailure_LogsAndSkips proves that when the spec cannot be
// rendered, ServeSpec logs and registers no routes (no panic, no partial mount).
//
// Note on coverage: huma's OpenAPI().YAML() internally calls json.Marshal(o)
// first, so any value that breaks JSON marshaling breaks YAML() first and we
// return on the YAML branch. The standalone specJSON-error branch in ServeSpec
// is therefore structurally unreachable defensive code (a verbatim port); it is
// retained as cheap safety, not exercised here.
func TestServeSpec_RenderFailure_LogsAndSkips(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	api := New(app, app.Group("/"), testConfig())

	// Inject an un-serializable extension into the spec so YAML() fails
	// deterministically (its internal json.Marshal chokes on the channel).
	api.OpenAPI().Extensions = map[string]any{"x-bad": make(chan int)}

	logger := &recordingLogger{}
	assert.NotPanics(t, func() {
		ServeSpec(app, api, logger, "/v1", "Test Docs")
	})

	assert.True(t, logger.called, "render failure must be logged")
	assert.Contains(t, logger.msg, "yaml", "the YAML render branch is the one hit")

	status, _ := doReq(t, app, http.MethodGet, "/v1/openapi.json")
	assert.Equal(t, http.StatusNotFound, status, "no routes registered on render failure")
}

// TestSchemaParity_ErrorSchemaCarriesCode is the org-wide shape lock: after
// problem.Install(), the generated OpenAPI error component schema must expose the
// optional `code` property (alongside the RFC 9457 status/title/detail). The
// test fails if Detail ever loses the field. It restores the global override so
// it does not leak into sibling tests.
func TestSchemaParity_ErrorSchemaCarriesCode(t *testing.T) {
	// NOT parallel: mutates the process-global huma.NewError.
	original := huma.NewError
	t.Cleanup(func() { huma.NewError = original })

	problem.Install()

	app := fiber.New()
	api := New(app, app.Group("/"), testConfig())

	// Register an operation that can produce a validation error so Huma wires the
	// error schema into the components.
	registerEcho(api)

	// Force the error schema into the registry via the override return type.
	errSchema := findErrorSchema(t, api)
	require.NotNil(t, errSchema, "an error component schema must be generated")

	_, hasCode := errSchema.Properties["code"]
	assert.True(t, hasCode, "error schema must carry the optional `code` property")

	// The RFC 9457 quartet must remain.
	for _, p := range []string{"status", "title", "detail"} {
		_, ok := errSchema.Properties[p]
		assert.Truef(t, ok, "error schema must carry %q", p)
	}

	// Belt-and-suspenders: the marshaled spec contains the example domain code.
	raw, err := json.Marshal(api.OpenAPI())
	require.NoError(t, err)
	assert.Contains(t, string(raw), "SPB-3002", "code property example should be present in the spec")
}

// findErrorSchema locates the registered component schema that carries the
// promoted `code` property (the *Detail shape). It is resilient to the schema's
// component name.
func findErrorSchema(t *testing.T, api huma.API) *huma.Schema {
	t.Helper()

	for name, s := range api.OpenAPI().Components.Schemas.Map() {
		if s == nil || s.Properties == nil {
			continue
		}

		if _, ok := s.Properties["code"]; !ok {
			continue
		}

		_, hasStatus := s.Properties["status"]
		_, hasDetail := s.Properties["detail"]
		if hasStatus && hasDetail {
			t.Logf("error schema component: %s", name)
			return s
		}
	}

	return nil
}
