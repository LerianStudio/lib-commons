//go:build unit

package openapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"

	"github.com/LerianStudio/lib-commons/v7/commons/net/http/problem"
	"github.com/danielgtaylor/huma/v2"
	"github.com/gofiber/fiber/v3"
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

	ServeSpec(app, api, obs.Nop(), "/v1", "Test Docs")

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

			ServeSpec(app, api, obs.Nop(), tc.prefix, "Test Docs")

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
		ServeSpec(app, nil, obs.Nop(), "/v1", "Test Docs")
	})

	status, _ := doReq(t, app, http.MethodGet, "/v1/openapi.json")
	assert.Equal(t, http.StatusNotFound, status, "no routes when api is nil")
}

// recordingLogger captures the last Log call so the skip-on-failure path can be
// asserted.
type recordingLogger struct {
	called bool
	msg    string
}

func (l *recordingLogger) Log(_ context.Context, _ int, msg string, _ ...any) {
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

	// The `upstream` extension member must be published too, or a client that
	// proxies a third-party rail has no contract to code against: it would receive
	// a member the spec never declared.
	_, hasUpstream := errSchema.Properties["upstream"]
	assert.True(t, hasUpstream, "error schema must carry the optional `upstream` extension member")

	// Belt-and-suspenders: the marshaled spec contains the example domain code.
	raw, err := json.Marshal(api.OpenAPI())
	require.NoError(t, err)
	assert.Contains(t, string(raw), "ERR-0001", "code property example should be present in the spec")
}

// TestErrorSeams_UpstreamReachesTheClient is the end-to-end lock for BOTH error
// seams over a real Fiber app: the provider's own code and message must arrive in
// the `upstream` extension member of the response body, on the status the rail
// mapped to, with everything of ours on a 5xx still scrubbed.
//
// The MapError case is the one that would silently regress: a handler error that
// already satisfies huma.StatusError is written verbatim by Huma, so the
// process-global huma.NewError override never runs on that path.
func TestErrorSeams_UpstreamReachesTheClient(t *testing.T) {
	// NOT parallel: mutates the process-global huma.NewError.
	original := huma.NewError
	t.Cleanup(func() { huma.NewError = original })

	problem.Install()

	railErr := fmt.Errorf("consult rail: %w", &problem.Upstream{
		Code:    "E4001",
		Message: "serviço indisponível",
	})

	cases := map[string]struct {
		handlerErr error
		wantStatus int
	}{
		"MapError seam": {
			handlerErr: problem.MapError(railErr,
				func(error) (string, string, bool) { return "GW-9001", "leaky raw cause", true },
				func(string) int { return http.StatusBadGateway },
				"GW-0000",
			),
			wantStatus: http.StatusBadGateway,
		},
		"huma.Error5xx seam": {
			handlerErr: huma.Error502BadGateway("rail refused", railErr),
			wantStatus: http.StatusBadGateway,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			app := fiber.New()
			api := New(app, app.Group("/"), testConfig())
			registerFailing(api, tc.handlerErr)

			status, body := doReq(t, app, http.MethodGet, "/fail")
			assert.Equal(t, tc.wantStatus, status)

			var got map[string]any
			require.NoError(t, json.Unmarshal([]byte(body), &got))

			assert.Equal(t, "internal error", got["detail"], "our own 5xx detail stays scrubbed")

			up, ok := got["upstream"].(map[string]any)
			require.True(t, ok, "upstream member must reach the client, got %s", body)
			assert.Equal(t, "E4001", up["code"])
			assert.Equal(t, "serviço indisponível", up["message"])
		})
	}
}

// registerFailing registers a GET operation whose handler always returns err, so
// a test can drive a real request through Huma's own error-writing path.
func registerFailing(api huma.API, err error) {
	huma.Register(api, huma.Operation{
		OperationID: "fail",
		Method:      http.MethodGet,
		Path:        "/fail",
	}, func(_ context.Context, _ *struct{}) (*echoOutput, error) {
		return nil, err
	})
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
		ServeSpec(nil, api, obs.Nop(), "/v1", "Test Docs")
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

func (*recordingLogger) Enabled(int) bool { return true }

func (*recordingLogger) Sync(context.Context) error { return nil }

// baselinePathInput backs the baseline-response operations that read a path
// parameter. echoInput (a body) and *struct{} (neither) cover the other two
// input shapes the 422 rule branches on.
type baselinePathInput struct {
	ID string `path:"id"`
}

// registerBaseline registers a no-op operation so a test can inspect what the
// wrapper wrote into the emitted document for it. It declares no Errors, which
// is the shape every Lerian service currently ships.
func registerBaseline[I any](api huma.API, id, method, path string) {
	huma.Register(api, huma.Operation{
		OperationID: id,
		Method:      method,
		Path:        path,
	}, func(context.Context, *I) (*echoOutput, error) {
		return &echoOutput{}, nil
	})
}

// responseKeys lists the statuses documented on op, so a test can assert the
// COMPLETE set and therefore that nothing unexpected was added.
func responseKeys(op *huma.Operation) []string {
	return slices.Collect(maps.Keys(op.Responses))
}

// TestBaselineErrors_AddedWhenServiceDeclaresNone proves the statuses a service
// gets without writing any error declaration of its own: 500 always, 422 for an
// operation that reads input, and the catch-all untouched beside them. It also
// locks C2 — every added status reuses the schema pointer Huma already
// registered for the catch-all, so components carries one error schema, not two.
func TestBaselineErrors_AddedWhenServiceDeclaresNone(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	api := New(app, app.Group("/"), testConfig())

	registerBaseline[echoInput](api, "baseline-body", http.MethodPost, "/baseline/body")
	registerBaseline[baselinePathInput](api, "baseline-path", http.MethodGet, "/baseline/path/{id}")
	registerBaseline[struct{}](api, "baseline-bare", http.MethodGet, "/baseline/bare")

	cases := map[string]struct {
		op   *huma.Operation
		want []string
	}{
		"reads a body":           {op: api.OpenAPI().Paths["/baseline/body"].Post, want: []string{"200", "422", "500", "default"}},
		"reads a path parameter": {op: api.OpenAPI().Paths["/baseline/path/{id}"].Get, want: []string{"200", "422", "500", "default"}},
		"reads neither":          {op: api.OpenAPI().Paths["/baseline/bare"].Get, want: []string{"200", "500", "default"}},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			require.NotNil(t, tc.op)
			assert.ElementsMatch(t, tc.want, responseKeys(tc.op), "exact documented status set")

			catchAll := tc.op.Responses["default"]
			require.NotNil(t, catchAll)
			require.NotEmpty(t, catchAll.Content)

			for _, status := range tc.want {
				if status == "200" || status == "default" {
					continue
				}

				added := tc.op.Responses[status]
				require.NotNil(t, added)
				assert.Equal(t, http.StatusText(mustAtoi(t, status)), added.Description)

				for mediaType, media := range catchAll.Content {
					require.Containsf(t, added.Content, mediaType, "%s must answer with the error media type", status)
					assert.Samef(t, media.Schema, added.Content[mediaType].Schema,
						"%s must reference the catch-all's schema, not a second one", status)
				}
			}
		})
	}
}

// mustAtoi converts a documented status key back to an int for a description
// comparison, failing the test rather than swallowing a malformed key.
func mustAtoi(t *testing.T, s string) int {
	t.Helper()

	n, err := strconv.Atoi(s)
	require.NoError(t, err)

	return n
}

// TestBaselineErrors_DeclaredErrorsLeftAlone proves an operation that enumerates
// its own errors is untouched. Huma writes no catch-all for it, so there is no
// registered error schema to clone from and the wrapper adds nothing: the whole
// documented set is Huma's own (the declared 404, plus the 422/500 Huma appends
// once Errors is non-empty).
func TestBaselineErrors_DeclaredErrorsLeftAlone(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	api := New(app, app.Group("/"), testConfig())

	huma.Register(api, huma.Operation{
		OperationID: "baseline-declared",
		Method:      http.MethodGet,
		Path:        "/baseline/declared/{id}",
		Errors:      []int{http.StatusNotFound},
	}, func(context.Context, *baselinePathInput) (*echoOutput, error) {
		return &echoOutput{}, nil
	})

	op := api.OpenAPI().Paths["/baseline/declared/{id}"].Get
	require.NotNil(t, op)

	assert.ElementsMatch(t, []string{"200", "404", "422", "500"}, responseKeys(op))
	assert.NotContains(t, op.Responses, "default", "Huma writes no catch-all once the service enumerates its errors")
	assert.Equal(t, http.StatusText(http.StatusNotFound), op.Responses["404"].Description)
}

// TestBaselineErrors_DoesNotOverwriteADeclaredStatus proves C3 and C4 together:
// a status the service described itself keeps its own description, the catch-all
// the service wrote survives, and the missing baseline status is still added
// beside them with the catch-all's schema.
func TestBaselineErrors_DoesNotOverwriteADeclaredStatus(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	api := New(app, app.Group("/"), testConfig())

	// The service supplies both its own catch-all and its own 500. Supplying any
	// response at all stops Huma from writing a catch-all, so the catch-all here
	// has to come from the service for the wrapper to have a schema to clone.
	serviceSchema := &huma.Schema{Type: "object"}
	huma.Register(api, huma.Operation{
		OperationID: "baseline-preset",
		Method:      http.MethodPost,
		Path:        "/baseline/preset",
		Responses: map[string]*huma.Response{
			"default": {
				Description: "Service catch-all",
				Content:     map[string]*huma.MediaType{"application/problem+json": {Schema: serviceSchema}},
			},
			"500": {Description: "Service-specific server error"},
		},
	}, func(context.Context, *echoInput) (*echoOutput, error) {
		return &echoOutput{}, nil
	})

	op := api.OpenAPI().Paths["/baseline/preset"].Post
	require.NotNil(t, op)

	assert.Equal(t, "Service-specific server error", op.Responses["500"].Description, "a declared status is never rewritten")
	assert.Equal(t, "Service catch-all", op.Responses["default"].Description, "the catch-all is never rewritten")

	added := op.Responses["422"]
	require.NotNil(t, added, "the missing baseline status is still added")
	assert.Same(t, serviceSchema, added.Content["application/problem+json"].Schema)
}

// TestBaselineErrors_ConfigAddsExtraStatuses proves the per-service additions:
// a status listed in Config.BaselineErrors is documented on every operation,
// a repeat of it is harmless, and nothing else arrives uninvited.
func TestBaselineErrors_ConfigAddsExtraStatuses(t *testing.T) {
	t.Parallel()

	cfg := testConfig()
	cfg.BaselineErrors = []int{http.StatusUnauthorized, http.StatusUnauthorized}

	app := fiber.New()
	api := New(app, app.Group("/"), cfg)

	registerBaseline[struct{}](api, "baseline-configured", http.MethodGet, "/baseline/configured")

	op := api.OpenAPI().Paths["/baseline/configured"].Get
	require.NotNil(t, op)

	assert.ElementsMatch(t, []string{"200", "401", "500", "default"}, responseKeys(op))
	assert.Equal(t, http.StatusText(http.StatusUnauthorized), op.Responses["401"].Description)
}

// TestBaselineErrors_EmittedDocumentForAServiceShapedAPI is the end-to-end lock:
// an API built the way a service builds one — openapi.New plus problem.Install()
// for the RFC 9457 envelope — emits the baseline statuses in the marshalled
// document, and every one of them points at the SAME error schema as the
// catch-all, which is what C2 exists to guarantee.
func TestBaselineErrors_EmittedDocumentForAServiceShapedAPI(t *testing.T) {
	// NOT parallel: problem.Install mutates the process-global huma.NewError.
	original := huma.NewError
	t.Cleanup(func() { huma.NewError = original })

	problem.Install()

	app := fiber.New()
	api := New(app, app.Group("/"), testConfig())

	registerBaseline[baselinePathInput](api, "doc-get-one", http.MethodGet, "/doc/items/{id}")
	registerBaseline[echoInput](api, "doc-create", http.MethodPost, "/doc/items")
	registerBaseline[struct{}](api, "doc-list", http.MethodGet, "/doc/items")

	raw, err := json.Marshal(api.OpenAPI())
	require.NoError(t, err)

	// Decoded from the marshalled bytes rather than read off the in-memory
	// objects: the emitted document is the artefact a caller reads, and only it
	// shows whether the added statuses resolved to one $ref or to several.
	var doc struct {
		Paths map[string]map[string]struct {
			Responses map[string]struct {
				Content map[string]struct {
					Schema struct {
						Ref string `json:"$ref"`
					} `json:"schema"`
				} `json:"content"`
			} `json:"responses"`
		} `json:"paths"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc))

	// Each entry is "<media type> -> <$ref>". One distinct entry across every
	// error response of every operation means one shared error schema.
	shapes := map[string]struct{}{}

	cases := []struct {
		name    string
		path    string
		method  string
		want422 bool
	}{
		{name: "path parameter", path: "/doc/items/{id}", method: "get", want422: true},
		{name: "request body", path: "/doc/items", method: "post", want422: true},
		{name: "neither", path: "/doc/items", method: "get", want422: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			responses := doc.Paths[tc.path][tc.method].Responses
			require.NotEmpty(t, responses)

			require.Contains(t, responses, "500")
			require.Contains(t, responses, "default", "the catch-all still covers a status nobody enumerated")

			statuses := []string{"500", "default"}

			if tc.want422 {
				require.Contains(t, responses, "422")

				statuses = append(statuses, "422")
			} else {
				assert.NotContains(t, responses, "422", "an operation that reads no input cannot fail request validation")
			}

			for _, status := range statuses {
				for mediaType, media := range responses[status].Content {
					assert.NotEmptyf(t, media.Schema.Ref, "%s must reference a schema component", status)
					shapes[mediaType+" -> "+media.Schema.Ref] = struct{}{}
				}
			}
		})
	}

	assert.Len(t, shapes, 1, "every documented error must share one media type and one schema reference, got %v", shapes)
}
