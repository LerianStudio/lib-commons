package streaming

import (
	"context"
	"errors"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Emit publishes a single Event. It performs caller-side validation
// synchronously (tenant/source/size/JSON), applies defaults on a local copy
// so the caller's struct is untouched, then dispatches the produce through
// the circuit-breaker wrapper so infrastructure faults feed the breaker but
// caller faults do NOT.
//
// Circuit-open behavior:
//   - With WithOutboxRepository wired: Emit writes the event to the outbox
//     and returns nil. The outbox Dispatcher will drain it via the handler
//     registered by RegisterOutboxHandler (bypasses the breaker on replay).
//   - Without an outbox wired: Emit returns ErrCircuitOpen so the caller
//     can fail fast or implement its own fallback.
//
// In either case the circuit breaker itself stays untouched during the
// circuit-open branch — it is already OPEN; there is nothing to "feed".
//
// Observability (T6):
//   - One `streaming.emit` span per Emit that passes preflight. Preflight
//     rejections never create a span but DO record the caller_error metric;
//     this keeps span cardinality bounded to actual emission attempts and
//     aligns with TRD §7.2.
//   - Every terminal branch records a single outcome in both the emitted
//     counter (outcome label) and the duration histogram, AND sets the
//     event.outcome span attribute to the same value. Keeping the three
//     signals aligned is the whole point of the single-span design.
//
// Nil-receiver safe: returns ErrNilProducer rather than panicking.
func (p *Producer) Emit(ctx context.Context, event Event) error {
	if p == nil {
		return ErrNilProducer
	}

	if p.closed.Load() {
		return ErrEmitterClosed
	}

	// Apply defaults on a local copy first so the caller's struct is not
	// mutated. Safe before validation because defaults (EventID, Timestamp,
	// SchemaVersion, DataContentType) do not influence any validation check.
	(&event).ApplyDefaults()

	// Pre-flight validation runs OUTSIDE the circuit-breaker wrapper so a
	// barrage of caller mistakes cannot trip the breaker — breaker counts
	// only reflect infrastructure health, not caller hygiene.
	//
	// Metric-only instrumentation here: preflight rejections are caller
	// errors, worth counting but not worth a span (TRD §7.2 implies spans
	// cover emission attempts, not input-validation rejections). Recording
	// duration on a non-attempt would skew the histogram.
	if err := p.preFlight(event); err != nil {
		p.metrics.recordEmitted(ctx, event.Topic(), "send", outcomeCallerError)

		return err
	}

	// Start the streaming.emit span AFTER preflight so we only count actual
	// emission attempts. The span lives across the whole CB-wrapped branch
	// tree so all three terminal branches (produced/outboxed/circuit_open/dlq)
	// share one trace context — exactly one span per Emit (DX-D01).
	ctx, span := p.tracer.Start(ctx, emitSpanName, trace.WithSpanKind(trace.SpanKindProducer))
	defer span.End()

	p.setEmitSpanAttributes(span, event)

	// outcome tracks the terminal branch so the deferred metric recorders
	// and the span attribute write a consistent value. Defaulting to
	// caller_error is defensive — every successful branch overwrites it
	// before return, so an unset outcome indicates a logic bug and will
	// surface in the caller_error bucket rather than silently skew metrics.
	outcome := outcomeCallerError
	start := time.Now()

	// Defer the metric + span-attr writes so they fire regardless of how
	// the function exits (early returns, errors, panics via runtime). This
	// is intentionally the ONLY place metric.recordEmitDuration is called
	// for post-preflight emits — keeping the call site single simplifies
	// auditing the histogram against the counter.
	defer func() {
		span.SetAttributes(attribute.String("event.outcome", outcome))
		p.metrics.recordEmitted(ctx, event.Topic(), "send", outcome)
		p.metrics.recordEmitDuration(ctx, event.Topic(), outcome, durationMilliseconds(time.Since(start)))
	}()

	// cb.Execute takes a closure with signature func() (any, error). We close
	// over the outer ctx and defaulted-event. A typed-nil result is fine;
	// the caller discards it. Errors are propagated verbatim — Execute wraps
	// ErrOpenState / ErrTooManyRequests into its own diagnostic so callers
	// can still errors.Is for those sentinels.
	_, err := p.cb.Execute(func() (any, error) {
		return p.emitAttempt(ctx, span, event, &outcome)
	})
	if err != nil {
		// Record error on the span so trace viewers surface it in the span
		// status/events. Does not override the outcome attribute — the
		// deferred SetAttributes fires after this return path and carries
		// the final outcome label.
		span.RecordError(err)
	}

	return err
}

// emitAttempt is the inner function passed to cb.Execute. Extracted from
// the Emit body so the closure that feeds the circuit breaker has a named,
// testable entry point and so producer.go stays under the file-size cap.
//
// outcome is a pointer because the deferred recorders in Emit close over
// the same variable; every branch writes to *outcome before returning so
// the deferred SetAttributes + recordEmitted picks up the final value.
func (p *Producer) emitAttempt(ctx context.Context, span trace.Span, event Event, outcome *string) (any, error) {
	// Circuit-open fallback. When the breaker is OPEN we observe the
	// mirrored flag and either (a) route to the outbox if one is
	// configured — caller sees nil after a durable write — or (b) fail
	// fast with ErrCircuitOpen. Either way we return (nil, nil) on the
	// outbox-success path so gobreaker leaves the breaker untouched;
	// returning an error here would count against the already-open
	// breaker for no useful reason.
	if p.cbStateFlag.Load() == flagCBOpen {
		return p.emitCircuitOpenBranch(ctx, span, event, outcome)
	}

	if err := p.publishDirect(ctx, event); err != nil {
		// Classify via EmitError (populated by publishDirect). Outcome
		// mapping is DRIVEN BY DLQ ROUTING, not by IsCallerError:
		//
		//   isDLQRoutable(class) == true  → outcome dlq
		//   isDLQRoutable(class) == false → outcome caller_error
		//
		// This is important because ClassSerialization is BOTH a
		// caller-correctable class AND routes to DLQ — its TRD outcome
		// is "dlq" (the payload went to the per-topic DLQ and operators
		// need a DLQ counter increment). See TRD §7.1 outcome table and
		// §C9 retry-and-DLQ policy.
		var emitErr *EmitError
		if errors.As(err, &emitErr) && emitErr != nil {
			span.SetAttributes(attribute.String("error.type", string(emitErr.Class)))

			if isDLQRoutable(emitErr.Class) {
				*outcome = outcomeDLQ

				p.metrics.recordDLQ(ctx, event.Topic(), string(emitErr.Class))
			} else {
				*outcome = outcomeCallerError
			}
		} else {
			// Non-EmitError errors from publishDirect are unexpected
			// (publishDirect's contract is to always wrap). Treat as
			// caller_error to keep the label bounded.
			*outcome = outcomeCallerError
		}

		return nil, err
	}

	// Success: the result value is unused by Emit and the CB manager
	// only inspects the error. A nil, nil return is the idiomatic
	// success signal for this Execute signature.
	*outcome = outcomeProduced

	return nil, nil //nolint:nilnil // CB Execute signature mandates (any, error); nil result is discarded
}

// emitCircuitOpenBranch handles the CB=OPEN leg. With an outbox wired it
// routes the event and returns (nil, nil) so the breaker stays untouched.
// Without an outbox it returns ErrCircuitOpen for the caller.
//
// Extracted from emitAttempt to keep single-responsibility per function;
// the CB-open decision tree is simple but verbose enough to benefit from
// its own docstring and test seams.
func (p *Producer) emitCircuitOpenBranch(ctx context.Context, span trace.Span, event Event, outcome *string) (any, error) {
	if p.outbox != nil {
		if obxErr := p.publishToOutbox(ctx, event); obxErr != nil {
			// Outbox write itself failed: surface the error so the caller
			// knows the event wasn't durably captured. Silent drop here
			// would lose the event entirely. Classified as caller_error
			// at the metric layer because the caller needs to know the
			// event is not durably captured — there is no dedicated
			// outbox-fail outcome in the TRD enum and adding one would
			// be scope creep.
			*outcome = outcomeCallerError

			return nil, obxErr
		}

		// Outbox wrote successfully. Caller sees nil; Dispatcher will
		// drain the row back through publishDirect once the broker
		// recovers.
		*outcome = outcomeOutboxed

		span.AddEvent("outbox.routed", trace.WithAttributes(
			attribute.String("reason", "circuit_open"),
		))
		p.metrics.recordOutboxRouted(ctx, event.Topic(), "circuit_open")

		return nil, nil //nolint:nilnil // CB Execute signature mandates (any, error); nil result is discarded
	}

	// No outbox configured — T3 behavior preserved. Caller gets an
	// explicit sentinel they can errors.Is against.
	*outcome = outcomeCircuitOpen

	return nil, ErrCircuitOpen
}
