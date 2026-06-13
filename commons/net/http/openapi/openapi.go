// Package openapi adapts an existing Fiber v2 application into a configured
// Huma v2 API that emits OpenAPI 3.1 metadata. It is a thin adapter only: it
// configures the document metadata, suppresses Huma's auto-mounted spec/docs
// routes, binds the API to a Fiber group via the Fiber v2 adapter, and serves
// the spec + Scalar docs on explicit, caller-gated routes.
//
// It applies NO error policy. Error policy is the org-wide RFC 9457 model in
// commons/net/http/problem, installed by the consumer's bootstrap via
// problem.Install(). This package deliberately does NOT import problem: the
// binding layer must not depend on the error model. Until a consumer calls
// problem.Install(), Huma emits its native RFC 9457 application/problem+json
// error model, which handlers select via the package-level huma.ErrorNNN
// constructors.
//
// This package is platform glue shared by every Lerian service; it must not
// import any bounded-context package.
package openapi

import (
	"context"
	"encoding/json"
	"fmt"

	libLog "github.com/LerianStudio/lib-observability/log"
	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humafiber"
	"github.com/gofiber/fiber/v2"
)

// scalarCSP relaxes the strict global CSP for the Scalar docs page so the
// Scalar bundle and its assets load from the jsdelivr CDN. It is applied
// per-route by scalarCSPMiddleware; the global strict CSP is unaffected
// elsewhere.
const scalarCSP = "default-src 'self'; script-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net; style-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net; img-src 'self' data: https://cdn.jsdelivr.net; font-src 'self' data: https://cdn.jsdelivr.net; connect-src 'self'"

// docsHTMLTemplate is a minimal, dependency-free docs page that renders a Huma
// spec via Scalar loaded from a CDN <script>. The title and the spec URL are
// substituted per service (%[1]s = title, %[2]s = spec URL). No Go dependency is
// added for the docs UI.
const docsHTMLTemplate = `<!doctype html>
<html>
  <head>
    <title>%[1]s</title>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
  </head>
  <body>
    <script id="api-reference" data-url="%[2]s"></script>
    <script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference"></script>
  </body>
</html>`

// Config carries the OpenAPI metadata for the generated API. It is intentionally
// transport-agnostic so callers in bootstrap supply values from service config.
type Config struct {
	// Title is the API title surfaced in the OpenAPI Info object.
	Title string
	// Version is the API document version surfaced in the OpenAPI Info object.
	Version string
	// Description is an optional long-form API description.
	Description string
	// Servers lists the server URLs advertised in the spec.
	Servers []string
}

// New wraps an existing Fiber app/group with a Huma v2 API that emits OpenAPI
// 3.1. It starts from huma.DefaultConfig, configures the Info metadata and
// servers, then clears auto-mount + transformers and applies no error policy
// (native RFC 9457 until the consumer calls problem.Install). Serving the
// spec/docs is the caller's concern (see ServeSpec). Operations are registered
// by callers, not here.
func New(app *fiber.App, group fiber.Router, cfg Config) huma.API {
	humaConfig := huma.DefaultConfig(cfg.Title, cfg.Version)
	humaConfig.Info.Description = cfg.Description

	// DefaultConfig installs a SchemaLinkTransformer that, by reflection,
	// rebuilds every response body into a new struct carrying a `$schema` field
	// plus copies of the original's exported fields. That bypasses custom
	// json.Marshaler implementations and leaks an internal schema URL into every
	// response body. Strip it so bodies serialize exactly as written.
	humaConfig.Transformers = nil
	humaConfig.OnAddOperation = nil
	humaConfig.CreateHooks = nil
	humaConfig.SchemasPath = ""

	// DefaultConfig leaves OpenAPIPath="/openapi" and DocsPath="/docs", which
	// makes humafiber.NewV2WithGroup auto-mount /openapi.json, /openapi.yaml,
	// and /docs on the supplied group at construction time — un-gated and, since
	// an API commonly binds to the app root, reachable in production. Clearing
	// both paths disables that auto-mount: the wrapper registers NO HTTP routes,
	// so it never silently exposes the spec. Serving spec/docs becomes an
	// explicit, gated bootstrap concern, keeping this shared wrapper free of
	// exposure policy. Clearing the paths suppresses route registration only;
	// api.OpenAPI() stays fully populated.
	humaConfig.OpenAPIPath = ""
	humaConfig.DocsPath = ""

	if len(cfg.Servers) > 0 {
		servers := make([]*huma.Server, 0, len(cfg.Servers))
		for _, url := range cfg.Servers {
			servers = append(servers, &huma.Server{URL: url})
		}

		humaConfig.Servers = servers
	}

	return humafiber.NewV2WithGroup(app, group, humaConfig)
}

// DeclareBearerAuth registers the BearerAuth HTTP bearer/JWT security scheme in
// the API's components so per-operation Security:[{"BearerAuth":{}}] references
// resolve in the generated spec instead of dangling. It carries zero service
// content, so it lives in this shared adapter; services declare the same scheme.
// Declared once on the shared API; idempotent (re-declaring the same scheme is a
// no-op overwrite). Nil-safe.
func DeclareBearerAuth(api huma.API) {
	if api == nil {
		return
	}

	components := api.OpenAPI().Components
	if components.SecuritySchemes == nil {
		components.SecuritySchemes = map[string]*huma.SecurityScheme{}
	}

	components.SecuritySchemes["BearerAuth"] = &huma.SecurityScheme{
		Type:         "http",
		Scheme:       "bearer",
		BearerFormat: "JWT",
		Description:  "JWT bearer token issued by the identity provider.",
	}
}

// ServeSpec mounts the Huma OpenAPI spec + Scalar docs under prefix:
// {prefix}/openapi.json, {prefix}/openapi.yaml, and {prefix}/docs. The Huma
// spec is immutable after operation registration, so the JSON/YAML bytes are
// snapshotted once here rather than marshaled per request. These routes are
// deliberately OFF the auth/tenant chain (public-within-the-gate). Callers MUST
// gate this on their Swagger.Enabled flag; it is never registered when the flag
// is false. If the spec fails to render the routes are skipped and the failure
// is logged. Nil-safe on api.
//
// title is the docs page <title>; the Scalar data-url points at the
// prefix-scoped /openapi.json route. Services share this helper; the prefix and
// title diverge per service.
func ServeSpec(app *fiber.App, api huma.API, logger libLog.Logger, prefix, title string) {
	if api == nil {
		return
	}

	specYAML, err := api.OpenAPI().YAML()
	if err != nil {
		logger.Log(context.Background(), libLog.LevelError, "failed to render Huma spec yaml", libLog.Err(err))
		return
	}

	specJSON, err := json.Marshal(api.OpenAPI())
	if err != nil {
		logger.Log(context.Background(), libLog.LevelError, "failed to marshal Huma spec json", libLog.Err(err))
		return
	}

	specURL := prefix + "/openapi.json"
	docs := docsHTML(title, specURL)

	group := app.Group(prefix)
	group.Get("/openapi.json", func(c *fiber.Ctx) error {
		c.Type("json")
		return c.Send(specJSON)
	})
	group.Get("/openapi.yaml", func(c *fiber.Ctx) error {
		c.Set(fiber.HeaderContentType, "application/yaml; charset=utf-8")
		return c.Send(specYAML)
	})
	group.Get("/docs", scalarCSPMiddleware(), func(c *fiber.Ctx) error {
		c.Type("html")
		return c.Send(docs)
	})
}

// scalarCSPMiddleware overrides the global strict CSP for the Scalar docs page
// so the Scalar bundle can load from the jsdelivr CDN. Applied only to that
// route; the global strict CSP is unaffected elsewhere.
func scalarCSPMiddleware() fiber.Handler {
	return func(c *fiber.Ctx) error {
		c.Set("Content-Security-Policy", scalarCSP)
		return c.Next()
	}
}

// docsHTML renders the Scalar docs page bytes for the given title and spec URL.
func docsHTML(title, specURL string) []byte {
	return fmt.Appendf(nil, docsHTMLTemplate, title, specURL)
}
