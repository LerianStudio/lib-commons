// Package openapi adapts an existing Fiber v3 application into a configured
// Huma v2 API that emits OpenAPI 3.1 metadata. It is a thin adapter only: it
// configures the document metadata, suppresses Huma's auto-mounted spec/docs
// routes, binds the API to a Fiber group via the Fiber v3 adapter, and serves
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
	"html"
	"net/http"
	"path"
	"reflect"
	"strconv"
	"strings"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humafiber"
	"github.com/gofiber/fiber/v3"
)

// scalarCSP relaxes the strict global CSP for the Scalar docs page so the
// Scalar bundle and its assets load from the jsdelivr CDN. It is applied
// per-route by scalarSecurityHeadersMiddleware; the global strict CSP is unaffected
// elsewhere.
const scalarCSP = "default-src 'self'; script-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net; style-src 'self' 'unsafe-inline' https://cdn.jsdelivr.net; img-src 'self' data: https://cdn.jsdelivr.net; font-src 'self' data: https://cdn.jsdelivr.net; connect-src 'self'; frame-ancestors 'none'"

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
	// BaselineErrors lists extra statuses this service's middleware stack can
	// return on ANY operation, documented on every operation alongside the 500
	// (and the 422 for an operation that reads input) the wrapper always adds.
	// 401 and 403 belong here only when authentication runs inside this Huma
	// API; a service that authenticates ahead of Huma answers them before an
	// operation is ever reached, so they are not this document's to promise.
	// Nil adds nothing.
	BaselineErrors []int
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

	// Reinstate the (now empty) hook slice with the baseline-response hook. This
	// is the ONLY seam that can still add a status: huma.Register materializes
	// op.Responses from op.Errors and only THEN calls AddOperation, which fires
	// this slice, so appending to op.Errors here would change nothing.
	humaConfig.OnAddOperation = []huma.AddOpFunc{baselineResponses(cfg.BaselineErrors)}

	// DefaultConfig leaves OpenAPIPath="/openapi" and DocsPath="/docs", which
	// makes humafiber.NewWithGroup auto-mount /openapi.json, /openapi.yaml,
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

	return humafiber.NewWithGroup(app, group, humaConfig)
}

// baselineResponses returns the OnAddOperation hook that documents the statuses
// every operation can answer with but that Huma leaves out. A Lerian service
// declares no huma.Operation.Errors, so Huma writes only the success status and
// a single "default" catch-all: the contract never names 500, and a caller
// generating a client from it gets no branch for the failures it will actually
// receive. The hook fills that in without the service writing a line.
//
// extra carries Config.BaselineErrors, the statuses this service's own
// middleware stack can return on any operation.
func baselineResponses(extra []int) huma.AddOpFunc {
	return func(oapi *huma.OpenAPI, op *huma.Operation) {
		if op.Responses == nil {
			return
		}

		// Prefer Huma's catch-all when it exists. An operation with a custom
		// non-default response does not get that catch-all, so derive the same
		// registered error content Huma uses rather than dropping the baseline
		// statuses for that valid operation shape.
		var errorContent map[string]*huma.MediaType

		if catchAll := op.Responses["default"]; catchAll != nil {
			errorContent = catchAll.Content
		} else {
			errorContent = registeredErrorContent(oapi)
		}

		statuses := make([]int, 0, len(extra)+2)
		statuses = append(statuses, http.StatusInternalServerError)

		// Mirrors huma.Register's own rule: an operation that reads a parameter or
		// a body with a validation schema can fail request validation with 422.
		if len(op.Parameters) > 0 || hasValidationBody(op.RequestBody) {
			statuses = append(statuses, http.StatusUnprocessableEntity)
		}

		statuses = append(statuses, extra...)

		for _, status := range statuses {
			key := strconv.Itoa(status)

			// A status already present is one the service said something specific
			// about, and its wording wins. This is also what makes a repeated entry
			// in extra harmless.
			if _, exists := op.Responses[key]; exists {
				continue
			}

			op.Responses[key] = &huma.Response{
				// http.StatusText is empty for a status outside the registered
				// range. That is deliberately not filtered: an empty description in
				// the emitted document is a visible defect in the caller's
				// BaselineErrors, where a dropped status would be a silent one.
				Description: http.StatusText(status),
				Content:     cloneErrorContent(errorContent),
			}
		}

		// op.Responses["default"] is never touched: a status nobody enumerated is
		// still documented by it.
	}
}

func registeredErrorContent(oapi *huma.OpenAPI) map[string]*huma.MediaType {
	if oapi == nil || oapi.Components.Schemas == nil {
		return nil
	}

	example := huma.NewError(0, "")
	contentType := "application/json"

	if filter, ok := example.(huma.ContentTypeFilter); ok {
		contentType = filter.ContentType(contentType)
	}

	t := reflect.TypeOf(example)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	return map[string]*huma.MediaType{
		contentType: {Schema: oapi.Components.Schemas.Schema(t, true, "Error")},
	}
}

func hasValidationBody(body *huma.RequestBody) bool {
	if body == nil {
		return false
	}

	if media := body.Content["application/json"]; media != nil && media.Schema != nil {
		return true
	}

	for _, media := range body.Content {
		if media == nil || media.Schema == nil {
			continue
		}

		if media.Schema.Type != "string" && media.Schema.Format != "binary" {
			return true
		}
	}

	return false
}

// cloneErrorContent copies the catch-all's content map so each added status owns
// its own map rather than aliasing one. The *huma.Schema pointer is shared on
// purpose: it is the reference Huma already registered, so every added status
// resolves to the one error component instead of duplicating it.
func cloneErrorContent(src map[string]*huma.MediaType) map[string]*huma.MediaType {
	if len(src) == 0 {
		return nil
	}

	out := make(map[string]*huma.MediaType, len(src))
	for mediaType, media := range src {
		if media == nil {
			continue
		}

		out[mediaType] = &huma.MediaType{Schema: media.Schema}
	}

	return out
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
// is logged. Nil-safe: a nil app or api is a no-op, and a nil logger falls back
// to a no-op logger so a render failure never panics.
//
// title is the docs page <title>; the Scalar data-url points at the
// prefix-scoped /openapi.json route. Services share this helper; the prefix and
// title diverge per service.
func ServeSpec(app *fiber.App, api huma.API, logger obs.Logger, prefix, title string) {
	if app == nil || api == nil {
		return
	}

	if logger == nil {
		logger = obs.Nop()
	}

	specYAML, err := api.OpenAPI().YAML()
	if err != nil {
		logger.Log(context.Background(), obs.LevelError, "failed to render Huma spec yaml", "error", err)
		return
	}

	specJSON, err := json.Marshal(api.OpenAPI())
	if err != nil {
		logger.Log(context.Background(), obs.LevelError, "failed to marshal Huma spec json", "error", err)
		return
	}

	// Normalize prefix to a leading slash and no trailing slash so a caller
	// passing "v1" (relative spec URL, broken Scalar link) or "/v1/" (double
	// slash "/v1//openapi.json") still yields a clean absolute path. path.Join
	// collapses the join cleanly and yields "/openapi.json" when prefix is "/".
	prefix = "/" + strings.Trim(prefix, "/")
	specURL := path.Join(prefix, "openapi.json")
	docs := docsHTML(title, specURL)

	group := app.Group(prefix)
	group.Get("/openapi.json", func(c fiber.Ctx) error {
		c.Type("json")
		return c.Send(specJSON)
	})
	group.Get("/openapi.yaml", func(c fiber.Ctx) error {
		c.Set(fiber.HeaderContentType, "application/yaml; charset=utf-8")
		return c.Send(specYAML)
	})
	group.Get("/docs", scalarSecurityHeadersMiddleware(), func(c fiber.Ctx) error {
		c.Type("html")
		return c.Send(docs)
	})
}

// scalarSecurityHeadersMiddleware overrides the global strict CSP for the
// Scalar docs page and prevents MIME sniffing. Applied only to that route; the
// global strict CSP is unaffected elsewhere.
func scalarSecurityHeadersMiddleware() fiber.Handler {
	return func(c fiber.Ctx) error {
		c.Set("Content-Security-Policy", scalarCSP)
		c.Set("X-Content-Type-Options", "nosniff")

		return c.Next()
	}
}

// docsHTML renders the Scalar docs page bytes for the given title and spec URL.
// Both values are HTML-escaped before interpolation so a title or prefix
// carrying HTML/attribute-breaking characters cannot inject markup or script
// into the /docs page.
func docsHTML(title, specURL string) []byte {
	return fmt.Appendf(nil, docsHTMLTemplate, html.EscapeString(title), html.EscapeString(specURL))
}
