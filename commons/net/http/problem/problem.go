// Package problem is the rail-agnostic, org-wide RFC 9457 error model for
// Huma-served APIs. It exposes the shared Detail body (huma.ErrorModel plus a
// flat, machine-readable Code), an Install() override of the process-global
// huma.NewError that makes EVERY Huma-constructed error a *Detail while
// centrally scrubbing every >=500 body, and a generic MapError mapper that
// translates a domain-layer error into the shared Detail.
//
// The package imports github.com/danielgtaylor/huma/v2 only — no Fiber, no
// transport adapter — so it is the light, transport-free half of the wrapper.
// The heavier Fiber binding lives in commons/net/http/openapi and deliberately
// does NOT import this package: error policy is the consumer bootstrap's
// concern (it calls Install), the binding's is metadata + mounting.
//
// This package is platform glue shared by every Lerian service; it must not
// import any bounded-context package.
package problem

import "github.com/danielgtaylor/huma/v2"

// BaseURI is the single source of truth for the RFC 9457 `type` URI shape. The
// full `type` for a coded error is BaseURI + "/" + code (flat + versioned),
// e.g. https://errors.lerian.studio/v1/ERR-0001. The /v1 segment versions the
// published error catalog so a `type` URI stays a stable, dereferenceable
// identifier even if the catalog's meaning model later evolves. Never hardcode
// the literal a second time; reference this constant.
const BaseURI = "https://errors.lerian.studio/v1"

// Detail is the single RFC 9457 error body for every Lerian rail. It embeds
// Huma's ErrorModel (type/title/status/detail/instance/errors) and adds the
// flat machine-readable domain code.
//
// *Detail satisfies huma.StatusError via method promotion from the embedded
// ErrorModel (Error/GetStatus/ContentType/Add). Installing it as the
// huma.NewError override (see Install) makes Huma's generated OpenAPI error
// schema reflect this type, including the optional `code` property; the field
// is dropped by omitempty for code-less rails.
type Detail struct {
	huma.ErrorModel
	Code string `json:"code,omitempty" doc:"Stable, machine-readable domain error code scoped to the emitting service (format: <SERVICE>-NNNN)." example:"ERR-0001"`
}
