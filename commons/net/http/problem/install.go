package problem

import (
	"net/http"
	"sync"
	"sync/atomic"

	"github.com/danielgtaylor/huma/v2"
)

// genericServerErrorDetail is the static, leak-free public detail served for
// every status>=500 error built through the installed override that carries no
// PublicDetail. It carries no operation name and no underlying cause, so a
// careless call site (including a direct huma.Error500(rawErr.Error())) cannot
// interpolate an internal error into a client-visible 5xx body.
const genericServerErrorDetail = "internal error"

// installMu serializes reads and writes of the process-global huma.NewError so
// concurrent installs (e.g. several API constructions in parallel tests) cannot
// race with EACH OTHER on the package var.
//
// The mutex covers install-vs-install only. Huma READS huma.NewError on every
// request that constructs an error, and that read is unsynchronized, so Install
// MUST run during bootstrap, before any request can be served. An Install that
// still needs to write while the server is serving is a data race on a plain
// function variable.
//
// Deliberately a mutex and not a sync.Once, and the reason is a real failure
// mode: a Once makes every install after the first a NO-OP, which is silent and
// wrong the moment anything else in the process has put the stock huma.NewError
// back — a test that saves and restores the global, most commonly. The next
// Install() would quietly do nothing and every scrub assertion after it would be
// measuring stock Huma while looking like it measured ours.
//
// But republishing UNCONDITIONALLY trades that fault for a worse one, and the
// worse one is a PII leak. Callers are documented to be able to decorate the
// installed model — br-sfn/scr wraps it to scrub the borrower's CPF/CNPJ out of
// Huma's request-body validation echo — and such a wrapper installs itself once,
// after Install(). A second Install() in the same process would overwrite the
// wrapper and NOT restore it, because the wrapper's own guard has already fired.
// Silently, with no signal anywhere: the next validation error echoes the
// document again.
//
// So the decision is BEHAVIOURAL rather than a call count, see installed().
var installMu sync.Mutex

// Install overrides the process-global huma.NewError so every error Huma
// constructs — domain errors routed through MapError as well as the framework's
// own validation/404/etc. errors — is a *Detail. This is what makes Huma's
// generated OpenAPI error schema carry the shared shape (including the optional
// `code` property) with zero per-operation registration.
//
// It is idempotent AND re-entrant: each service's runtime bootstrap and spec-gen
// entrypoint may call it, and a double call is safe. A call installs when the
// model is not in place and leaves it alone when it is — including when a caller
// has DECORATED it — so a caller can neither end up believing the override is
// installed when it is not, nor lose a decoration by calling again.
//
// DECORATING THE INSTALLED MODEL is a supported pattern: read huma.NewError,
// wrap it, assign it back. A wrapper that still returns a *Detail is recognised
// as installed and survives every later Install(). A wrapper that returns some
// OTHER type is not, and will be replaced — deliberately, because Huma builds the
// generated error schema by reflecting the type this constructor returns, so a
// different type silently breaks the spec's error shape.
//
// huma.NewError is a package var, so this MUST run before any operation is
// registered on the runtime API or the spec-gen API, or the generated schema and
// the runtime bodies will diverge. It equally MUST run before the server starts
// serving: Huma reads the var unsynchronized on every error-constructing request,
// and installMu only serializes installs against each other, not against those
// reads.
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
//   - at ANY status, an *Upstream, an Extensions or a PublicDetail found in errs
//     is lifted onto the body instead of being folded (see curated). They are
//     the only exceptions to the >=500 scrub, and each is carried by its TYPE,
//     not by a flag: only a value a call site deliberately built lands there.
//     Everything else about a 5xx — the raw msg, errors[] — stays scrubbed.
//
// For framework errors Code stays empty (dropped by omitempty) and Type stays at
// the RFC default about:blank.
//
// Folded errors[] entries keep the value Huma echoes from the rejected input;
// [InstallWithoutValueEcho] drops it.
func Install() {
	installMu.Lock()
	defer installMu.Unlock()

	if installed(huma.NewError) {
		return
	}

	huma.NewError = newError
}

// dropValueEcho is set by InstallWithoutValueEcho and never cleared by the
// package. It is read by fold on every error built, from request goroutines, so
// it is atomic rather than guarded by installMu.
var dropValueEcho atomic.Bool

// InstallWithoutValueEcho does everything [Install] does and also stops
// errors[] entries from echoing the rejected input back to the client: every
// errors[] entry of a <500 problem document keeps its message and location and
// loses its value.
//
// Huma fills that value with the offending input — the field value on a 422
// validation failure, the raw path, query or header parameter on a parse
// failure, and the ENTIRE raw request body when the body is not valid JSON
// (400) or has an unsupported content type (415). For an API that receives
// personal data (a taxpayer document, an account number) that echo returns
// the data in an error body, where it lands in client logs, proxies and
// support tickets. The drop applies to every location: body, path, query and
// header.
//
// It is opt-in because it changes the 4xx bodies a client already sees.
// Installing it is sticky for the life of the process: a later plain Install()
// (a second bootstrap, a spec-gen entrypoint) never turns the drop back off.
// It is read when each error is built, so a decorator of the installed model
// that delegates to it keeps the drop.
//
// It removes only the value member. Some Huma messages quote the input inside
// the message text itself — a time that fails to parse
// (`invalid value: parsing time "..."`), or a JSON syntax error near the
// offending token — and those messages are passed through unchanged.
//
// Like Install, it MUST run during bootstrap, before the server serves.
func InstallWithoutValueEcho() {
	dropValueEcho.Store(true)
	Install()
}

// installed reports whether a constructor already yields the shared model, which
// is the question Install actually needs answered — "is our shape in place?" —
// rather than "have I been called before?".
//
// It probes by construction instead of comparing function pointers, and that is
// the whole point: a caller's decorator is a DIFFERENT function that still
// returns a *Detail, so a pointer comparison would fail to recognise it and
// Install would overwrite the decoration. Probing the result recognises it.
//
// Calling the constructor here is safe and is not a novel operation: Huma itself
// does exactly this — NewError(0, "") — when it derives the generated error
// schema, so any conforming constructor must tolerate it. Nothing observable
// happens; the returned value is discarded.
func installed(constructor func(status int, msg string, errs ...error) huma.StatusError) bool {
	if constructor == nil {
		return false
	}

	_, ok := constructor(0, "").(*Detail)

	return ok
}

// newError is the override body installed over huma.NewError: it builds a
// *Detail for every error Huma constructs, scrubbing >=500 centrally. It is a
// named function (not an inline closure) so it can be exercised directly in
// tests without mutating the process-global, and so every Install() republishes
// the same stable reference.
func newError(status int, msg string, errs ...error) huma.StatusError {
	var members curated

	rest := make([]error, 0, len(errs))

	for _, e := range errs {
		if !members.collect(e) {
			rest = append(rest, e)
		}
	}

	pd := &Detail{
		ErrorModel: huma.ErrorModel{
			Status: status,
			Title:  http.StatusText(status),
			Detail: genericServerErrorDetail,
		},
	}

	if status < http.StatusInternalServerError {
		pd.Detail = msg
		pd.Errors = fold(rest)
	}

	members.apply(pd)

	return pd
}

// fold renders errs as errors[] exactly like the stock huma.NewError: nil errs
// are skipped and a huma.ErrorDetailer contributes its own detail. Under
// InstallWithoutValueEcho a detail carrying a value is folded as a copy with no
// value; the caller's (or Huma's) detail is never modified.
func fold(errs []error) []*huma.ErrorDetail {
	dropValue := dropValueEcho.Load()
	details := make([]*huma.ErrorDetail, 0, len(errs))

	for _, e := range errs {
		if e == nil {
			continue
		}

		if converted, ok := e.(huma.ErrorDetailer); ok {
			// ErrorDetail() may return a nil *huma.ErrorDetail; appending it
			// would serialize a null entry into errors[]. Skip the nil one.
			if d := converted.ErrorDetail(); d != nil {
				details = append(details, withoutValue(d, dropValue))
			}

			continue
		}

		details = append(details, &huma.ErrorDetail{Message: e.Error()})
	}

	if len(details) == 0 {
		return nil
	}

	return details
}

// withoutValue returns d unchanged unless drop is set and d carries a value, in
// which case it returns a copy of d with no value.
func withoutValue(d *huma.ErrorDetail, drop bool) *huma.ErrorDetail {
	if !drop || d.Value == nil {
		return d
	}

	stripped := *d
	stripped.Value = nil

	return &stripped
}
