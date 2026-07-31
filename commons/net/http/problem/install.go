package problem

import (
	"net/http"
	"sync"

	"github.com/danielgtaylor/huma/v2"
)

// genericServerErrorDetail is the static, leak-free public detail served for
// every status>=500 error built through the installed override. It carries no
// operation name and no underlying cause, so a careless call site (including a
// direct huma.Error500(rawErr.Error())) cannot interpolate an internal error
// into a client-visible 5xx body.
const genericServerErrorDetail = "internal error"

// installMu serializes writes to the process-global huma.NewError so concurrent
// installs (e.g. several API constructions in parallel tests) cannot race on the
// package var.
//
// Deliberately a mutex and not a sync.Once: a Once would make every install
// after the first a NO-OP, which is silent and wrong the moment anything else in
// the process has put the stock huma.NewError back — a test that saves and
// restores the global, most commonly. The next Install() would quietly do
// nothing and every scrub assertion after it would be measuring stock Huma while
// looking like it measured ours. Republishing unconditionally is the same
// deterministic end state and has no such failure mode.
var installMu sync.Mutex

// Install overrides the process-global huma.NewError so every error Huma
// constructs — domain errors routed through MapError as well as the framework's
// own validation/404/etc. errors — is a *Detail. This is what makes Huma's
// generated OpenAPI error schema carry the shared shape (including the optional
// `code` property) with zero per-operation registration.
//
// It is idempotent AND re-entrant: each service's runtime bootstrap and spec-gen
// entrypoint may call it, a double call is safe because the override is itself
// deterministic, and every call actually republishes — so a caller can never end
// up believing the override is installed when it is not.
//
// huma.NewError is a package var, so this MUST run before any operation is
// registered on the runtime API or the spec-gen API, or the generated schema and
// the runtime bodies will diverge.
//
// MERGE SEMANTICS (this is the crux of the promotion):
//   - status >= 500: the body is scrubbed to the static genericServerErrorDetail
//     and NO errs are folded. This is underwriter's central safety — it closes
//     the direct-huma.Error5xx(rawErr) info-leak that br-sfn's old override left
//     open by passing the raw msg/errs straight through.
//   - status  < 500: msg is passed through and errs are folded into Errors[] in
//     order (skip nil, honor huma.ErrorDetailer) — exactly like the stock
//     huma.NewError, so native 422 validation errors keep their per-field
//     errors[] list.
//   - at ANY status, an *Upstream found in errs (see takeUpstream) is lifted into
//     the upstream extension member instead of being folded. It is the single
//     exception to the >=500 scrub, and the exception is carried by the TYPE, not
//     by a flag: only a value a call site deliberately built as an *Upstream can
//     land there, and its shape cannot hold a provider's raw body. Everything
//     else about a 5xx — detail, errors[], our own code — stays scrubbed.
//
// For framework errors Code stays empty (dropped by omitempty) and Type stays at
// the RFC default about:blank.
func Install() {
	installMu.Lock()
	defer installMu.Unlock()

	huma.NewError = newError
}

// newError is the override body installed over huma.NewError: it builds a
// *Detail for every error Huma constructs, scrubbing >=500 centrally. It is a
// named function (not an inline closure) so it can be exercised directly in
// tests without mutating the process-global, and so every Install() republishes
// the same stable reference.
func newError(status int, msg string, errs ...error) huma.StatusError {
	upstream, errs := takeUpstream(errs)

	if status >= http.StatusInternalServerError {
		return &Detail{
			ErrorModel: huma.ErrorModel{
				Status: status,
				Title:  http.StatusText(status),
				Detail: genericServerErrorDetail,
			},
			Upstream: upstream,
		}
	}

	details := make([]*huma.ErrorDetail, 0, len(errs))

	for _, e := range errs {
		if e == nil {
			continue
		}

		if converted, ok := e.(huma.ErrorDetailer); ok {
			// ErrorDetail() may return a nil *huma.ErrorDetail; appending it
			// would serialize a null entry into errors[]. Skip the nil one.
			if d := converted.ErrorDetail(); d != nil {
				details = append(details, d)
			}

			continue
		}

		details = append(details, &huma.ErrorDetail{Message: e.Error()})
	}

	var folded []*huma.ErrorDetail
	if len(details) > 0 {
		folded = details
	}

	return &Detail{
		ErrorModel: huma.ErrorModel{
			Status: status,
			Title:  http.StatusText(status),
			Detail: msg,
			Errors: folded,
		},
		Upstream: upstream,
	}
}

// takeUpstream pulls the upstream extension member out of errs (by the shared
// upstreamFrom rule) and returns the remaining errors to fold. An err the type
// is found in is never folded, so upstream data lives in exactly one place on
// the wire; the first non-empty match wins, and an empty or typed-nil *Upstream
// is dropped as if it had not been passed.
func takeUpstream(errs []error) (*Upstream, []error) {
	var (
		found *Upstream
		rest  = make([]error, 0, len(errs))
	)

	for _, e := range errs {
		up, matched := upstreamFrom(e)
		if !matched {
			rest = append(rest, e)

			continue
		}

		if found == nil {
			found = up
		}
	}

	return found, rest
}
