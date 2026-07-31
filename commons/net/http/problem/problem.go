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

import (
	"encoding/json"
	"errors"

	"github.com/danielgtaylor/huma/v2"
)

// BaseURI is the single source of truth for the RFC 9457 `type` URI shape. The
// full `type` for a coded error is BaseURI + "/" + code (flat + versioned),
// e.g. https://errors.lerian.studio/v1/ERR-0001. The /v1 segment versions the
// published error catalog so a `type` URI stays a stable, dereferenceable
// identifier even if the catalog's meaning model later evolves. Never hardcode
// the literal a second time; reference this constant.
const BaseURI = "https://errors.lerian.studio/v1"

// Upstream is the RFC 9457 extension member carrying the error a THIRD PARTY
// reported, for services that proxy an external rail. It is a top-level member
// of the problem document (see Detail.Upstream), so a client reads it without
// unpacking anything, and it is deliberately just two strings: the provider's
// own code and its own message.
//
// It is NOT a place for the provider's response body. There is no field that
// can hold one, and both fields are bounded on the wire (see MarshalJSON), so a
// call site that pastes a whole body into Message still cannot turn the member
// into a body dump. What belongs here is a code and a message the call site
// explicitly chose to surface, because the client automates against them.
//
// *Upstream is an error, which is how it reaches a problem document: pass it as
// an errs argument to huma.NewError / huma.Error4xx / huma.Error5xx (wrapped or
// not) and the installed override (see Install) lifts it into the member. That
// is also what makes it the only thing that survives the >=500 scrub — being
// this type IS the curation signal, so there is no flag to forget.
type Upstream struct {
	Code    string `json:"code,omitempty" doc:"The upstream provider's own error code, verbatim." example:"E4001"`
	Message string `json:"message,omitempty" doc:"The upstream provider's own error message, verbatim (bounded, never its raw response body)." example:"account not found at provider"`
}

// Bounds on each member field, enforced at encoding time so they hold whatever
// path built the value. A provider code is an identifier and a provider message
// is a sentence; anything longer is a body leaking in, not information.
const (
	maxUpstreamCodeLen    = 64
	maxUpstreamMessageLen = 512
	truncationMark        = "…"
)

// Error makes *Upstream an error so it can be passed to huma.NewError. A nil
// receiver yields an empty string rather than panicking: this value crosses a
// package boundary and a careless call site must not take a service down.
func (u *Upstream) Error() string {
	if u == nil {
		return ""
	}

	switch {
	case u.Code == "":
		return u.Message
	case u.Message == "":
		return "upstream " + u.Code
	default:
		return "upstream " + u.Code + ": " + u.Message
	}
}

// MarshalJSON bounds each field before it reaches the client. Truncation is
// rune-based, so multi-byte provider text is never cut mid-character and the
// body stays valid UTF-8.
func (u *Upstream) MarshalJSON() ([]byte, error) {
	// encoding/json emits null for a nil pointer without calling this method, so
	// this guard only covers a direct call: a library must not panic on one.
	if u == nil {
		return []byte("null"), nil
	}

	// wire sheds the MarshalJSON method, so json.Marshal below does not recurse.
	type wire Upstream

	return json.Marshal(wire{
		Code:    bound(u.Code, maxUpstreamCodeLen),
		Message: bound(u.Message, maxUpstreamMessageLen),
	})
}

// isEmpty reports whether the member carries nothing worth publishing. An empty
// member is treated as absent so a document never shows an `upstream` object
// with nothing in it.
func (u *Upstream) isEmpty() bool {
	return u == nil || (u.Code == "" && u.Message == "")
}

// upstreamFrom is the SINGLE detection rule for the extension member, shared by
// both seams that can produce a problem document (the Install override and
// MapError) so the two can never drift on what counts as an upstream error.
//
// It unwraps (errors.As), because a real call site wraps the rail error with
// local context before returning it. matched reports that the TYPE was present
// even when the value carries nothing worth publishing: an empty or typed-nil
// *Upstream must be dropped everywhere rather than folded into errors[] as a
// blank entry.
func upstreamFrom(err error) (up *Upstream, matched bool) {
	var candidate *Upstream
	if !errors.As(err, &candidate) {
		return nil, false
	}

	if candidate.isEmpty() {
		return nil, true
	}

	return candidate, true
}

// bound truncates s to at most maxRunes runes, marking the cut so a reader can
// tell the value was shortened.
func bound(s string, maxRunes int) string {
	if len(s) <= maxRunes {
		// Byte length is an upper bound on rune count, so this is the fast path
		// for every realistic provider code and message.
		return s
	}

	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}

	return string(r[:maxRunes]) + truncationMark
}

// Detail is the single RFC 9457 error body for every Lerian rail. It embeds
// Huma's ErrorModel (type/title/status/detail/instance/errors) and adds the
// flat machine-readable domain code plus the optional upstream extension member.
//
// *Detail satisfies huma.StatusError via method promotion from the embedded
// ErrorModel (Error/GetStatus/ContentType/Add). Installing it as the
// huma.NewError override (see Install) makes Huma's generated OpenAPI error
// schema reflect this type, including the optional `code` property; the field
// is dropped by omitempty for code-less rails, and `upstream` is likewise absent
// for every service that does not proxy a third party.
type Detail struct {
	huma.ErrorModel
	Code     string    `json:"code,omitempty" doc:"Stable, machine-readable domain error code scoped to the emitting service (format: <SERVICE>-NNNN)." example:"ERR-0001"`
	Upstream *Upstream `json:"upstream,omitempty" doc:"RFC 9457 extension member: the error a proxied third-party provider reported. Absent unless the emitting service explicitly surfaced one."`
}
