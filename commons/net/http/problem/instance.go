package problem

import (
	"github.com/LerianStudio/lib-observability/v4/tracing"
	"github.com/danielgtaylor/huma/v2"
)

// InstanceTransformer populates the RFC 9457 `instance` member — the reference
// identifying THIS occurrence of the problem — with the request's trace id, so a
// customer reading a failure has something to quote to support and support can
// find the request.
//
// The value is the OpenTelemetry trace id of the span already on the request
// context, rendered as 32 lowercase hex characters. It is deliberately the same
// string lib-observability writes as the `trace_id` field on every log line for
// that request and the id the trace is stored under, so a quoted value is greppable
// in logs and openable in traces with no translation. Nothing new is minted here.
//
// WHY A TRANSFORMER, and not the huma.NewError override Install() already owns:
// huma.NewError takes no context, so it cannot see the trace id, and it is not on
// every path anyway — a handler returning a value that already satisfies
// huma.StatusError (which is exactly what MapError returns) is written verbatim
// without any error constructor running. Huma funnels EVERY response it writes,
// error or not, through transformAndWrite -> api.Transform, and that seam receives
// the huma.Context. It is therefore the only point that sees both the request and
// the body, on every status the library can emit.
//
// It is safe on any value: anything that is not a *Detail is returned untouched,
// so registering it on an API whose consumer never called Install() changes
// nothing. An instance a caller set itself is never overwritten.
//
// When there is no valid span on the context — tracing not configured, a
// non-traced code path, a request rejected before the tracing middleware ran —
// the member is left EMPTY and `omitempty` on huma.ErrorModel.Instance drops it
// from the body entirely. An empty `instance` would look like an answer; an
// absent one correctly says there is none.
//
// The stamped body is a COPY. The *Detail reaching a transformer belongs to the
// caller, and a shared library on the error path of every Lerian service must not
// write through a pointer whose ownership it cannot see.
func InstanceTransformer(ctx huma.Context, _ string, v any) (any, error) {
	pd, ok := v.(*Detail)
	if !ok || pd == nil || pd.Instance != "" || ctx == nil {
		return v, nil
	}

	traceID := tracing.GetTraceIDFromContext(ctx.Context())
	if traceID == "" {
		return v, nil
	}

	stamped := *pd
	stamped.Instance = traceID

	return &stamped, nil
}
