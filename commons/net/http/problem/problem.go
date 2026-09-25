// Package problem is the rail-agnostic, org-wide RFC 9457 error model for
// Huma-served APIs. It exposes the shared Detail body (huma.ErrorModel plus a
// flat, machine-readable Code), an Install() override of the process-global
// huma.NewError that makes EVERY Huma-constructed error a *Detail while
// centrally scrubbing every >=500 body, and a generic MapError mapper that
// translates a domain-layer error into the shared Detail.
//
// The package imports github.com/danielgtaylor/huma/v2 and lib-observability's
// tracing accessor only — no Fiber, no transport adapter — so it stays the
// light, transport-free half of the wrapper. The heavier Fiber binding lives in
// commons/net/http/openapi, which imports this package for exactly one thing:
// registering InstanceTransformer on the API it builds, so every service carries
// the RFC 9457 `instance` member without writing a line. Choosing the error
// MODEL remains the consumer bootstrap's concern — it calls Install — and the
// binding still applies none of that policy.
//
// This package is platform glue shared by every Lerian service; it must not
// import any bounded-context package.
package problem

import (
	"bytes"
	"encoding/json"
	"errors"
	"slices"

	"github.com/danielgtaylor/huma/v2"
)

// BaseURI is the single source of truth for the RFC 9457 `type` URI shape. The
// full `type` for a coded error is BaseURI + "/" + code (flat + versioned),
// e.g. https://errors.lerian.studio/v1/<SERVICE>-NNNN. The /v1 segment versions the
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
// flat machine-readable domain code, the optional upstream extension member and
// any extension members the service curated (see Extensions).
//
// The embedded `instance` member is populated by InstanceTransformer with the
// request's trace id; it is `omitempty`, so it is absent rather than empty when
// the request carried no trace.
//
// *Detail satisfies huma.StatusError via method promotion from the embedded
// ErrorModel (Error/GetStatus/ContentType/Add). Installing it as the
// huma.NewError override (see Install) makes Huma's generated OpenAPI error
// schema reflect this type, including the optional `code` property; the field
// is dropped by omitempty for code-less rails, and `upstream` is likewise absent
// for every service that does not proxy a third party.
type Detail struct {
	huma.ErrorModel
	// No example: tag on Code, deliberately. This one struct is the error model
	// of every Huma-served Lerian API, so any literal here is published verbatim
	// into every one of their specs — and no literal can be right for more than
	// one of them, because the prefix is per-service by definition. A previous
	// example leaked one rail's namespace (SPB-3002) into every other rail's
	// spec; replacing it with a neutral-looking ERR-0001 only made the same value
	// wrong everywhere at once, since no service allocates that prefix. A reader
	// building a fixture from it built a code no response can ever carry. The
	// format below is the contract; the concrete codes belong to each service's
	// own error catalog.
	Code     string    `json:"code,omitempty" doc:"Stable, machine-readable domain error code scoped to the emitting service (format: <SERVICE>-NNNN)."`
	Upstream *Upstream `json:"upstream,omitempty" doc:"RFC 9457 extension member: the error a proxied third-party provider reported. Absent unless the emitting service explicitly surfaced one."`
	// Extensions are rendered as top-level members by MarshalJSON, never as a
	// member of their own.
	Extensions Extensions `json:"-"`

	// A body may carry extension members, so the published schema must allow them.
	_ struct{} `json:"-" additionalProperties:"true"`
}

// MarshalJSON renders the standard members, then each extension member in key
// order, so one body always renders the same bytes.
func (d Detail) MarshalJSON() ([]byte, error) {
	type wire Detail // sheds this method, so the call below does not recurse

	body, err := marshalUnescaped(wire(d))
	if err != nil {
		return nil, err
	}

	keys := make([]string, 0, len(d.Extensions))

	for key := range d.Extensions {
		if _, reserved := reservedMembers[key]; !reserved {
			keys = append(keys, key)
		}
	}

	if len(keys) == 0 {
		return body, nil
	}

	slices.Sort(keys)

	out := body[:len(body)-1] // reopen the object: body always ends in '}'

	for _, key := range keys {
		name, err := marshalUnescaped(key)
		if err != nil {
			return nil, err
		}

		value, err := marshalUnescaped(d.Extensions[key])
		if err != nil {
			return nil, err
		}

		if len(out) > 1 {
			out = append(out, ',')
		}

		out = append(append(append(out, name...), ':'), value...)
	}

	return append(out, '}'), nil
}

// marshalUnescaped leaves HTML characters as they are: the encoder writing the
// response applies its own escaping policy to these bytes, exactly as it did
// before Detail had a MarshalJSON of its own.
func marshalUnescaped(v any) ([]byte, error) {
	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(v); err != nil {
		return nil, err
	}

	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// reservedMembers are the document's own members. An extension never overrides
// one, so no call site can rewrite a status, a code or a scrubbed detail.
var reservedMembers = map[string]struct{}{
	"type": {}, "title": {}, "status": {}, "detail": {}, "instance": {}, "errors": {}, "code": {}, "upstream": {},
}

// Extensions carries RFC 9457 extension members: data a client needs to act on
// the problem, such as the id of the resource to poll. It travels in an error
// chain like *Upstream and, like it, survives the >=500 scrub.
type Extensions map[string]any

// Error lets an Extensions value travel in an error chain. It names no cause.
func (Extensions) Error() string { return "problem extensions" }

// PublicDetail is a detail its service has judged safe for a client to read at
// any status, the remedy a 5xx must not lose included ("do not resend"). It
// replaces the detail the status policy chose; nothing else about a 5xx changes.
type PublicDetail string

// Error lets a PublicDetail travel in an error chain.
func (d PublicDetail) Error() string { return string(d) }

// curated holds the values a call site deliberately built for the wire. They are
// the only content of a 5xx the scrub lets through, each carried by its TYPE.
type curated struct {
	upstream   *Upstream
	extensions Extensions
	detail     PublicDetail
}

// collect records what err carries, wrapped or not; the first non-empty value of
// each kind wins. matched reports that any of the types was present, even empty,
// so the caller never folds err into errors[].
func (c *curated) collect(err error) (matched bool) {
	if up, ok := upstreamFrom(err); ok {
		matched = true

		if c.upstream == nil {
			c.upstream = up
		}
	}

	var extensions Extensions
	if errors.As(err, &extensions) {
		matched = true

		if c.extensions == nil && len(extensions) > 0 {
			c.extensions = extensions
		}
	}

	var detail PublicDetail
	if errors.As(err, &detail) {
		matched = true

		if c.detail == "" {
			c.detail = detail
		}
	}

	return matched
}

// apply writes the curated values onto a body.
func (c *curated) apply(pd *Detail) {
	pd.Upstream = c.upstream
	pd.Extensions = c.extensions

	if c.detail != "" {
		pd.Detail = string(c.detail)
	}
}
