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

	t.Run("docs with security headers", func(t *testing.T) {
		t.Parallel()

		resp, err := app.Test(httptest.NewRequest(http.MethodGet, "/v1/docs", nil))
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()

		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Contains(t, resp.Header.Get(fiber.HeaderContentType), "text/html")
		assert.Equal(t, scalarCSP, resp.Header.Get("Content-Security-Policy"))
		assert.Contains(t, resp.Header.Get("Content-Security-Policy"), "frame-ancestors 'none'")
		assert.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))

		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		assert.Contains(t, string(body), "Test Docs")
		assert.Contains(t, string(body), "/v1/openapi.json")
		assert.Contains(t, string(body), "@scalar/api-reference")
	})
}

// TestServeSpec_NormalizesPrefix proves ServeSpec normalizes the prefix once
// (leading slash, no trailing slash, no double slash) before using it for both
// the served spec URL and the route group: an un-normalized prefix still mounts
// at the clean absolute path and the docs HTML carries the absolute spec URL.
func TestServeSpec_NormalizesPrefix(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		prefix   string
		specPath string // expected normalized openapi.json route + served spec URL
		docsPath string // expected normalized docs route
	}{
		{name: "no leading slash", prefix: "v1", specPath: "/v1/openapi.json", docsPath: "/v1/docs"},
		{name: "trailing slash", prefix: "/v1/", specPath: "/v1/openapi.json", docsPath: "/v1/docs"},
		{name: "leading slash already", prefix: "/v1", specPath: "/v1/openapi.json", docsPath: "/v1/docs"},
		{name: "nested", prefix: "api/v1/spi", specPath: "/api/v1/spi/openapi.json", docsPath: "/api/v1/spi/docs"},
		{name: "root slash", prefix: "/", specPath: "/openapi.json", docsPath: "/docs"},
		{name: "empty", prefix: "", specPath: "/openapi.json", docsPath: "/docs"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app := fiber.New()
			api := New(app, app.Group("/"), testConfig())
			registerEcho(api)

			ServeSpec(app, api, &libLog.NopLogger{}, tc.prefix, "Test Docs")

			status, _ := doReq(t, app, http.MethodGet, tc.specPath)
			assert.Equalf(t, http.StatusOK, status, "spec must be reachable at normalized path %s", tc.specPath)

			resp, err := app.Test(httptest.NewRequest(http.MethodGet, tc.docsPath, nil))
			require.NoError(t, err)
			defer func() { _ = resp.Body.Close() }()

			require.Equalf(t, http.StatusOK, resp.StatusCode, "docs must be reachable at %s", tc.docsPath)

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			docsHTML := string(body)
			assert.Containsf(t, docsHTML, tc.specPath, "docs HTML must carry the normalized spec URL %s", tc.specPath)
			assert.NotContains(t, docsHTML, "//openapi.json", "no double slash in spec URL")
			assert.NotContains(t, docsHTML, `data-url="openapi.json"`, "spec URL must be absolute, not relative")
		})
	}
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
	assert.Contains(t, string(raw), "ERR-0001", "code property example should be present in the spec")
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

// TestDocsHTML_EscapesInterpolatedValues proves the docs page HTML-escapes the
// title and spec URL, so a value carrying HTML/attribute-breaking characters
// cannot inject markup or script into /docs.
func TestDocsHTML_EscapesInterpolatedValues(t *testing.T) {
	t.Parallel()

	out := string(docsHTML(`</title><script>alert(1)</script>`, `x"><img src=y onerror=alert(1)>`))

	assert.NotContains(t, out, "<script>alert(1)</script>", "raw script must not survive into the docs HTML")
	assert.NotContains(t, out, `x"><img`, "attribute-breaking spec URL must be escaped")
	assert.Contains(t, out, "&lt;script&gt;", "title must be HTML-escaped")
	assert.Contains(t, out, "&#34;&gt;&lt;img", "spec URL must be HTML-escaped")
}

// TestServeSpec_NilApp proves a nil app is a no-op and does not panic, even with
// a valid api.
func TestServeSpec_NilApp(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	api := New(app, app.Group("/"), testConfig())
	registerEcho(api)

	assert.NotPanics(t, func() {
		ServeSpec(nil, api, &libLog.NopLogger{}, "/v1", "Test Docs")
	})
}

// TestServeSpec_NilLogger_RenderFailure_NoPanic proves a nil logger falls back to
// a no-op logger: even on a render failure (which logs), ServeSpec must not panic
// and registers no routes.
func TestServeSpec_NilLogger_RenderFailure_NoPanic(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	api := New(app, app.Group("/"), testConfig())
	api.OpenAPI().Extensions = map[string]any{"x-bad": make(chan int)}

	assert.NotPanics(t, func() {
		ServeSpec(app, api, nil, "/v1", "Test Docs")
	})

	status, _ := doReq(t, app, http.MethodGet, "/v1/openapi.json")
	assert.Equal(t, http.StatusNotFound, status, "no routes registered on render failure")
}
