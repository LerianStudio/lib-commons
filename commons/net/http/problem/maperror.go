package problem

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// MapError translates err into the shared RFC 9457 Detail. It applies the
// generic policy; each service supplies its own code extraction (codeOf) and
// code->status table (statusOf):
//
//   - nil error, a nil codeOf/statusOf callback, or codeOf reporting !ok -> 500
//     "internal error" carrying the fallbackCode (when non-empty) in the
//     machine-readable Code + Type fields. A nil error at an error mapper is a
//     handler bug, not success; returning a nil here would render a 200 and hide
//     the bug, so the safe default is the canonical sanitized 500. A miswired
//     caller passing a nil callback gets the same sanitized 500 instead of a
//     panic.
//   - codeOf -> (code, msg, true) -> a *Detail with Status=statusOf(code). When
//     code is non-empty, Code holds the bare domain code and Type is the flat
//     URI (BaseURI + "/" + code); the code is NOT appended to detail. 5xx details
//     are sanitized to "internal error" so a raw cause never leaks, while
//     Code/Type still let clients branch on a sanitized 500. An empty code yields
//     a bare body (no Code, default Type) for rails without a code taxonomy.
//
// codeOf extracts a (code, msg, ok) triple from err: ok=false signals the error
// is not a recognized domain error (fall back to the canonical 500). statusOf
// maps a code to its HTTP status. fallbackCode is the code carried in the body
// when the error is nil or unrecognized.
//
// An *Upstream reachable from err (wrapped or not) is lifted into the upstream
// extension member on whatever body comes out, at every status. This seam has to
// do that lifting itself: it returns a concrete *Detail straight to Huma, and a
// handler error that already satisfies huma.StatusError is written verbatim
// without huma.NewError ever being called — so the Install override never sees
// it, and a rail whose only error path is MapError would otherwise have no way
// to publish the member at all. As in the override, the exception to the >=500
// sanitization is carried by the TYPE, not by a flag: only a value a call site
// deliberately built as an *Upstream can land there.
//
// It returns a concrete *Detail directly rather than round-tripping through
// huma.NewError, so the result is independent of whether Install ran.
func MapError(
	err error,
	codeOf func(error) (code, msg string, ok bool),
	statusOf func(code string) int,
	fallbackCode string,
) error {
	pd := mapProblem(err, codeOf, statusOf, fallbackCode)

	if up, _ := upstreamFrom(err); up != nil {
		pd.Upstream = up
	}

	return pd
}

// mapProblem is MapError's status/code/detail policy, split out so the upstream
// member can be attached to every body it can return from one place.
func mapProblem(
	err error,
	codeOf func(error) (code, msg string, ok bool),
	statusOf func(code string) int,
	fallbackCode string,
) *Detail {
	if err == nil || codeOf == nil || statusOf == nil {
		return newProblem(http.StatusInternalServerError, genericServerErrorDetail, fallbackCode)
	}

	code, msg, ok := codeOf(err)
	if !ok {
		return newProblem(http.StatusInternalServerError, genericServerErrorDetail, fallbackCode)
	}

	status := statusOf(code)
	// A recognized domain error must map to a 4xx/5xx. A status below 400 (0,
	// 2xx, 3xx) means the rail's code->status table is misconfigured — a server
	// bug, not a client success — so clamp it to 500 rather than emit a
	// malformed or success-looking problem. The >=500 sanitization below then
	// applies to the clamped status.
	if status < http.StatusBadRequest {
		status = http.StatusInternalServerError
	}

	detail := msg
	if status >= http.StatusInternalServerError {
		detail = genericServerErrorDetail
	}

	return newProblem(status, detail, code)
}

// newProblem assembles a *Detail with the title defaulted from the status text
// and the type/code wiring: when code is non-empty, Code is set and Type is
// BaseURI + "/" + code; otherwise the body stays bare (no Code, default Type).
func newProblem(status int, detail, code string) *Detail {
	pd := &Detail{
		ErrorModel: huma.ErrorModel{
			Status: status,
			Title:  http.StatusText(status),
			Detail: detail,
		},
	}

	if code != "" {
		pd.Code = code
		pd.Type = BaseURI + "/" + code
	}

	return pd
}
