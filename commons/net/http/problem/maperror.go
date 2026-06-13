package problem

import (
	"net/http"

	"github.com/danielgtaylor/huma/v2"
)

// MapError translates err into the shared RFC 9457 Detail. It applies the
// generic policy; each service supplies its own code extraction (codeOf) and
// code->status table (statusOf):
//
//   - nil error, or codeOf reporting !ok -> 500 "internal error" carrying the
//     fallbackCode (when non-empty) in the machine-readable Code + Type fields.
//     A nil error at an error mapper is a handler bug, not success; returning a
//     nil here would render a 200 and hide the bug, so the safe default is the
//     canonical sanitized 500.
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
// It returns a concrete *Detail directly rather than round-tripping through
// huma.NewError, so the result is independent of whether Install ran.
func MapError(
	err error,
	codeOf func(error) (code, msg string, ok bool),
	statusOf func(code string) int,
	fallbackCode string,
) error {
	if err == nil {
		return newProblem(http.StatusInternalServerError, genericServerErrorDetail, fallbackCode)
	}

	code, msg, ok := codeOf(err)
	if !ok {
		return newProblem(http.StatusInternalServerError, genericServerErrorDetail, fallbackCode)
	}

	status := statusOf(code)

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
