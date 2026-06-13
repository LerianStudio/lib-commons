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

// installOnce serializes the override of the process-global huma.NewError. The
// override is deterministic, so installing exactly once per process is correct;
// the Once also makes concurrent installs (e.g. several API constructions in
// parallel tests) race-free, delivering on the idempotency this function's doc
// contract promises.
var installOnce sync.Once

// Install overrides the process-global huma.NewError so every error Huma
// constructs — domain errors routed through MapError as well as the framework's
// own validation/404/etc. errors — is a *Detail. This is what makes Huma's
// generated OpenAPI error schema carry the shared shape (including the optional
// `code` property) with zero per-operation registration.
//
// It is idempotent: each service's runtime bootstrap and spec-gen entrypoint may
// call it once, and a double call is safe because the override is itself
// deterministic.
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
//
// For framework errors Code stays empty (dropped by omitempty) and Type stays at
// the RFC default about:blank.
func Install() {
	installOnce.Do(func() {
		huma.NewError = newError
	})
}

// newError is the override body installed over huma.NewError: it builds a
// *Detail for every error Huma constructs, scrubbing >=500 centrally. It is a
// named function (not an inline closure) so it can be exercised directly in
// tests without mutating the process-global, and so installOnce.Do assigns a
// stable reference.
func newError(status int, msg string, errs ...error) huma.StatusError {
	if status >= http.StatusInternalServerError {
		return &Detail{
			ErrorModel: huma.ErrorModel{
				Status: status,
				Title:  http.StatusText(status),
				Detail: genericServerErrorDetail,
			},
		}
	}

	details := make([]*huma.ErrorDetail, 0, len(errs))

	for _, e := range errs {
		if e == nil {
			continue
		}

		if converted, ok := e.(huma.ErrorDetailer); ok {
			details = append(details, converted.ErrorDetail())
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
	}
}
