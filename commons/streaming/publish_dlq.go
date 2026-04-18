package streaming

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
)

// DLQ header keys (TRD §C8). Every DLQ message carries all six; none are
// optional. Keeping them colocated here (not in cloudevents.go) because they
// are NOT CloudEvents context attributes — they are Lerian-specific
// operational metadata that sits alongside the ce-* headers on DLQ messages.
const (
	dlqHeaderSourceTopic    = "x-lerian-dlq-source-topic"
	dlqHeaderErrorClass     = "x-lerian-dlq-error-class"
	dlqHeaderErrorMessage   = "x-lerian-dlq-error-message"
	dlqHeaderRetryCount     = "x-lerian-dlq-retry-count"
	dlqHeaderFirstFailureAt = "x-lerian-dlq-first-failure-at"
	dlqHeaderProducerID     = "x-lerian-dlq-producer-id"
)

// dlqTopicSuffix is the literal suffix appended to the source topic to derive
// the per-topic DLQ name. Per-topic DLQ (TRD §C8) is preferred over a global
// DLQ because it lets operators scope replay tooling to a single topic and
// keeps failure-class cardinality proportional to topic count, not to the
// product of topic × error_class.
const dlqTopicSuffix = ".dlq"

// dlqTopic derives "{source}.dlq" from the source topic name. Split out as a
// named helper so the naming convention surfaces in exactly one place — if
// TRD §C8 ever renames the suffix, this is the only edit site.
func dlqTopic(sourceTopic string) string {
	return sourceTopic + dlqTopicSuffix
}

// isDLQRoutable reports whether a classified error should route to the per-
// topic DLQ. Per TRD §C9 retry + DLQ policy table, DLQ routing applies to
// every class EXCEPT caller-cancel (ClassContextCanceled — caller already
// gave up) and caller-validation (ClassValidation — caller sees the error
// and corrects its own input).
//
// The other six classes (serialization, auth, topic_not_found,
// broker_unavailable, network_timeout, broker_overloaded) ALL route to DLQ:
//
//   - Serialization / auth / topic_not_found route immediately (franz-go
//     does zero retries for these classes).
//   - Broker_unavailable / network_timeout / broker_overloaded route AFTER
//     franz-go exhausts its internal retries — by the time they surface,
//     the record is already past its retry budget.
//
// The pragmatic blanket rule (NOT in [Validation, ContextCanceled]) is both
// simpler to reason about and matches the TRD table row-for-row.
func isDLQRoutable(cls ErrorClass) bool {
	switch cls {
	case ClassValidation, ClassContextCanceled:
		return false
	default:
		return true
	}
}

// extractRetryCount reports the franz-go retry counter at the time of
// exhaustion. franz-go does NOT export a stable API for retrieving the
// retry counter off kgo.ErrRecordRetries — the internal metadata is
// package-private. Returning 0 is the conservative choice; ops tooling can
// infer retry count from repeated DLQ entries or (preferred) from the
// upstream producer's span attribute set in T6.
//
// TODO(v1.1): if franz-go exposes a public accessor in a future version,
// swap this stub for the real extraction. Tracked via the TRD §C8 header
// specification which notes retry_count is "best-effort today".
func extractRetryCount(_ error) int {
	return 0
}

// publishDLQ writes the original record body to {source}.dlq, preserving
// every ce-* header verbatim and adding the six x-lerian-dlq-* headers
// defined in TRD §C8.
//
// Contract:
//   - Body (record.Value) is event.Payload exactly — no JSON re-marshaling,
//     no byte copies, no encoding change. Byte-equal preservation is
//     load-bearing so replay tooling can republish the same bytes to the
//     source topic.
//   - Every ce-* header present on the original emit is copied onto the
//     DLQ message, in the same order (buildCloudEventsHeaders is
//     deterministic).
//   - The six x-lerian-dlq-* headers are appended after the ce-* headers
//     so consumers that skim headers top-down see the protocol envelope
//     first, then the DLQ metadata.
//   - The error message carried in x-lerian-dlq-error-message is run
//     through sanitizeBrokerURL so embedded SASL credentials in the cause
//     never leak onto the wire.
//   - DLQ publish does NOT fall back to outbox (TRD §C8). A DLQ write
//     failure is logged at ERROR, the stub for streaming_dlq_publish_failed_total
//     is in place (T6 wires the metric), and the wrapped original error
//     is returned to the caller of publishDirect — never silently dropped.
//
// retryCount and firstFailureAt are caller-supplied: publishDLQ itself has
// no view into when the original attempt started (it's called at the
// moment of failure, not at the moment of origination), so publishDirect
// passes them through.
//
// Nil-receiver safe: returns ErrNilProducer / ErrEmitterClosed before any
// I/O when called in a degraded state.
func (p *Producer) publishDLQ(
	ctx context.Context,
	event Event,
	cause error,
	retryCount int,
	firstFailureAt time.Time,
) error {
	if p == nil {
		return ErrNilProducer
	}

	if p.client == nil || p.closed.Load() {
		return ErrEmitterClosed
	}

	sourceTopic := event.Topic()
	cls := classifyError(cause)

	// Sanitize the cause before it lands on a header that downstream
	// consumers may log verbatim. sanitizeBrokerURL is nil-safe on empty
	// strings so we do not need to guard the cause.Error() call beyond a
	// cause != nil check.
	causeMessage := ""
	if cause != nil {
		causeMessage = sanitizeBrokerURL(cause.Error())
	}

	// Build CloudEvents headers from the ORIGINAL event — buildCloudEventsHeaders
	// is pure, so calling it again here produces the same 8-13 headers the
	// original publish assembled. Keeping this verbatim is the contract
	// that makes replay tooling trivial (strip x-lerian-dlq-*, republish).
	headers := buildCloudEventsHeaders(event)

	// Append the six DLQ-specific headers. Per TRD §C8, all six are
	// REQUIRED on every DLQ message — no optional branches here.
	headers = append(headers,
		kgo.RecordHeader{Key: dlqHeaderSourceTopic, Value: []byte(sourceTopic)},
		kgo.RecordHeader{Key: dlqHeaderErrorClass, Value: []byte(cls)},
		kgo.RecordHeader{Key: dlqHeaderErrorMessage, Value: []byte(causeMessage)},
		kgo.RecordHeader{Key: dlqHeaderRetryCount, Value: []byte(strconv.Itoa(retryCount))},
		kgo.RecordHeader{Key: dlqHeaderFirstFailureAt, Value: []byte(firstFailureAt.UTC().Format(time.RFC3339Nano))},
		kgo.RecordHeader{Key: dlqHeaderProducerID, Value: []byte(p.producerID)},
	)

	// Preserve the partition key so within-tenant ordering is maintained
	// across the DLQ partitions. Using the same partFn resolution as
	// publishDirect keeps the two paths byte-compatible.
	partKey := event.PartitionKey()
	if p.partFn != nil {
		partKey = p.partFn(event)
	}

	record := &kgo.Record{
		Topic:   dlqTopic(sourceTopic),
		Key:     []byte(partKey),
		Value:   event.Payload, // UNCHANGED: replay strips x-lerian-dlq-* and republishes
		Headers: headers,
	}

	if err := p.client.ProduceSync(ctx, record).FirstErr(); err != nil {
		// DLQ write itself failed. Log at ERROR so alerting picks this up
		// (a failing DLQ is a leading indicator of correlated broker
		// failure — streaming_dlq_publish_failed_total is the metric
		// operators alert on). The caller sees a wrapped error containing
		// both the original cause and the DLQ failure, so no context is
		// lost.
		//
		// Log fields follow TRD §7.3 — producer_id / topic / resource_type /
		// event_type / tenant_id / event_id / error_class / outcome. Kept
		// in the same order everywhere so log-stream shapes stay stable.
		p.logger.Log(ctx, log.LevelError, "streaming: DLQ publish failed",
			log.String("producer_id", p.producerID),
			log.String("topic", sourceTopic),
			log.String("resource_type", event.ResourceType),
			log.String("event_type", event.EventType),
			log.String("tenant_id", event.TenantID),
			log.String("event_id", event.EventID),
			log.String("outcome", "dlq_publish_failed"),
			log.String("error_class", string(cls)),
			log.String("dlq_topic", dlqTopic(sourceTopic)),
			log.Err(err),
		)

		// streaming_dlq_publish_failed_total counter. Guarded by metrics
		// nil-safety so a misconfigured Producer (no metrics factory)
		// still logs the error and returns the wrapped chain.
		p.metrics.recordDLQFailed(ctx, sourceTopic)

		return fmt.Errorf("streaming: DLQ publish to %s failed: %w", dlqTopic(sourceTopic), err)
	}

	return nil
}

// dlqRouteResult describes the outcome of attempting to route a failed
// direct-publish through the DLQ. It lets publishDirect return a single
// *EmitError with the appropriate Cause chain — either the original cause
// when DLQ succeeded, or a joined "<original>; DLQ also failed: <dlq>"
// chain when DLQ itself failed.
type dlqRouteResult struct {
	// routed is true when the DLQ write succeeded. When false, dlqErr
	// carries the DLQ-side failure; the caller joins it onto the original
	// cause for a single error return.
	routed bool

	// dlqErr is the DLQ publishSync failure, if any. Populated only when
	// routed == false AND the class was DLQ-routable.
	dlqErr error
}

// routeToDLQIfApplicable classifies origErr, checks the DLQ routing rule,
// and (when applicable) publishes the event onto the per-topic DLQ. Returns
// a dlqRouteResult describing what happened so publishDirect can assemble
// its return error without branching at every step.
//
// Ordering rationale: classify FIRST (cheap, pure), then decide routing
// (also cheap), then publish to DLQ (I/O). This keeps the hot path short
// when the class is not DLQ-routable.
func (p *Producer) routeToDLQIfApplicable(
	ctx context.Context,
	event Event,
	origErr error,
	firstAttempt time.Time,
) (ErrorClass, dlqRouteResult) {
	cls := classifyError(origErr)

	if !isDLQRoutable(cls) {
		// Caller-cancel or caller-validation: do not route. Caller sees
		// the *EmitError with the original cause and decides.
		return cls, dlqRouteResult{}
	}

	retryCount := extractRetryCount(origErr)

	if dlqErr := p.publishDLQ(ctx, event, origErr, retryCount, firstAttempt); dlqErr != nil {
		return cls, dlqRouteResult{routed: false, dlqErr: dlqErr}
	}

	return cls, dlqRouteResult{routed: true}
}

// buildEmitErrorWithDLQ assembles the *EmitError returned from publishDirect
// when a direct publish has failed. When the DLQ succeeded, the Cause is
// the original error alone (callers can errors.Is against kerr sentinels).
// When the DLQ also failed, we join the two via errors.Join so both chains
// remain walkable via errors.Is, and format a human-readable wrapper for
// Error().
//
// This is a pure helper with no I/O — it lives here rather than inline so
// publishDirect stays legible.
func buildEmitErrorWithDLQ(event Event, origErr error, cls ErrorClass, dlq dlqRouteResult) *EmitError {
	cause := origErr
	if !dlq.routed && dlq.dlqErr != nil {
		// errors.Join preserves errors.Is on BOTH legs; the fmt wrapper is
		// for human-readable log output (EmitError.Error() runs the full
		// cause through sanitizeBrokerURL so neither leg leaks credentials).
		cause = fmt.Errorf("%w; DLQ also failed: %w", origErr, dlq.dlqErr)
	}

	return &EmitError{
		ResourceType: event.ResourceType,
		EventType:    event.EventType,
		TenantID:     event.TenantID,
		Topic:        event.Topic(),
		Class:        cls,
		Cause:        cause,
	}
}
