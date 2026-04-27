package streaming

import (
	"context"
	"encoding/json"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// maxPayloadBytes is the pre-flight payload-size ceiling. 1 MiB matches
// Redpanda's default broker.max.message.bytes and commons/outbox's
// DefaultMaxPayloadBytes, so the outbox-fallback path cannot accept a
// payload Redpanda would reject downstream.
const maxPayloadBytes = 1_048_576

// Header-length ceilings for CloudEvents context attributes. Values
// balance two constraints: (a) Kafka's default max.header.bytes per record
// is large but not unbounded; (b) downstream log parsers and OTEL label
// pipelines choke on arbitrary-length attribute values. The ceilings are
// intentionally conservative — longer values almost always indicate a bug
// (e.g. a UUID concatenated in a loop) rather than a legitimate use case.
const (
	maxTenantIDBytes        = 256
	maxResourceTypeBytes    = 128
	maxEventTypeBytes       = 128
	maxSourceBytes          = 2048
	maxSubjectBytes         = 1024
	maxEventIDBytes         = 256
	maxSchemaVersionBytes   = 64
	maxDataContentTypeBytes = 256
	maxDataSchemaBytes      = 2048
)

// hasControlChar reports whether s contains any ASCII control character
// (bytes < 0x20 including tab 0x09, or the DEL byte 0x7F). Control chars
// would corrupt log streams (CRLF injection, ANSI escape), break OTEL
// label pipelines, and confuse downstream header parsers. Tab is included
// in the deny set — legitimate CloudEvents attribute values never contain
// tabs, and allowing them would be a discipline gap.
func hasControlChar(s string) bool {
	for i := range len(s) {
		b := s[i]
		if b < 0x20 || b == 0x7F {
			return true
		}
	}

	return false
}

// preFlight runs all caller-side validation on an Event before it reaches
// the circuit breaker. Defaults must already be applied by the caller —
// preFlight is pure validation, never mutation. Split from publishDirect in
// T3 so a storm of caller-fault emissions cannot trip the breaker.
//
// Order of checks is tuned so the cheapest / most-common caller mistake
// surfaces first:
//
//  1. SystemEvent capability gate: reject before any other work if the
//     Producer did not opt into system events.
//  2. Event toggle: resource.event key opts the event out at runtime.
//  3. Tenant: non-system events require a tenant.
//  4. Source: required CloudEvents ce-source.
//  5. Header sanitization: TenantID / ResourceType / EventType / Source /
//     Subject must be header-safe (no control chars, bounded length).
//  6. Payload size: reject before JSON scan.
//  7. Payload JSON validity: prevents DLQ poisoning downstream.
//
// Returns one of the caller sentinel errors (ErrSystemEventsNotAllowed,
// ErrEventDisabled, ErrMissingTenantID, ErrMissingSource, ErrInvalid*,
// ErrPayloadTooLarge, ErrNotJSON). Never returns an *EmitError — caller
// faults have no Kafka-level class.
func (p *Producer) preFlight(event Event) error {
	// SystemEvent capability gate — runs FIRST so an opt-in violation is
	// rejected before any other validation can mask it. A service that
	// sets SystemEvent=true without WithAllowSystemEvents would otherwise
	// hijack the "system:*" partition space, so the rejection must be
	// unambiguous and fire even if other fields are also malformed.
	if event.SystemEvent && !p.allowSystemEvents {
		return ErrSystemEventsNotAllowed
	}

	// Topic-forming fields MUST be populated. An empty ResourceType or
	// EventType would produce a degenerate Kafka topic ("lerian.streaming..")
	// and a malformed ce-type header ("studio.lerian.."). No downstream
	// consumer routes those, so the event would be silently lost at the
	// worst possible layer. Checked before the toggle lookup so a caller
	// that forgot to set these fields can't accidentally match the "."
	// toggle key.
	if event.ResourceType == "" {
		return ErrMissingResourceType
	}

	if event.EventType == "" {
		return ErrMissingEventType
	}

	// Event-type toggle. Built from STREAMING_EVENT_TOGGLES at LoadConfig;
	// empty map (nil) means every event is enabled by default.
	if p.toggles != nil {
		key := event.ResourceType + "." + event.EventType
		if enabled, present := p.toggles[key]; present && !enabled {
			return ErrEventDisabled
		}
	}

	// Tenant discipline. System events (`ce-systemevent: true`) opt out of
	// the requirement — they're ops-level fan-out that carries no per-tenant
	// payload.
	if !event.SystemEvent && event.TenantID == "" {
		return ErrMissingTenantID
	}

	// ce-source is a required CloudEvents attribute. Empty source is a
	// caller config bug (usually: forgot to set Config.CloudEventsSource).
	if event.Source == "" {
		return ErrMissingSource
	}

	// Header sanitization. Every field that travels as a CloudEvents
	// context attribute (header) must be free of control characters and
	// within a bounded length. Bypassing these checks would let malicious
	// or buggy callers corrupt downstream log streams (CRLF injection
	// into structured logs) or break OTEL label pipelines.
	if err := p.validateHeaderSafeFields(event); err != nil {
		return err
	}

	// Pre-flight size cap. 1 MiB is the Redpanda default; we check BEFORE
	// JSON validity so the dominant caller mistake (huge payload) short-
	// circuits the slightly more expensive json.Valid scan.
	if len(event.Payload) > maxPayloadBytes {
		return ErrPayloadTooLarge
	}

	// Payload must parse as JSON. This is the line of defense that keeps
	// malformed bytes out of consumers and prevents DLQ replay from
	// repeatedly re-poisoning the same topic. An empty payload is
	// permitted ONLY when it's valid JSON (e.g. `null`, `{}`); a genuinely
	// empty byte slice fails json.Valid and surfaces ErrNotJSON.
	if !json.Valid(event.Payload) {
		return ErrNotJSON
	}

	return nil
}

// headerFieldCheck pairs a field value with its size ceiling and sentinel.
type headerFieldCheck struct {
	value    string
	maxBytes int
	sentinel error
}

// validateHeaderSafeFields checks every CloudEvents attribute that travels
// as a Kafka header for control characters and length overruns. Returns
// the first offending sentinel. Empty values are NOT checked here —
// required-vs-optional semantics live in preFlight (tenant, source).
func (*Producer) validateHeaderSafeFields(event Event) error {
	checks := [...]headerFieldCheck{
		{event.TenantID, maxTenantIDBytes, ErrInvalidTenantID},
		{event.ResourceType, maxResourceTypeBytes, ErrInvalidResourceType},
		{event.EventType, maxEventTypeBytes, ErrInvalidEventType},
		{event.Source, maxSourceBytes, ErrInvalidSource},
		{event.Subject, maxSubjectBytes, ErrInvalidSubject},
		{event.EventID, maxEventIDBytes, ErrInvalidEventID},
		{event.SchemaVersion, maxSchemaVersionBytes, ErrInvalidSchemaVersion},
		{event.DataContentType, maxDataContentTypeBytes, ErrInvalidDataContentType},
		{event.DataSchema, maxDataSchemaBytes, ErrInvalidDataSchema},
	}

	for _, c := range checks {
		if len(c.value) > c.maxBytes || hasControlChar(c.value) {
			return c.sentinel
		}
	}

	return nil
}

// publishDirect is the synchronous produce step. Caller-side validation is
// assumed complete (see preFlight) and defaults are assumed applied. This
// function assembles a kgo.Record from the Event and calls ProduceSync.
//
// The franz-go produce call is synchronous (ProduceSync + FirstErr). On
// non-nil err, we:
//
//  1. Classify the error per TRD §C9.
//  2. When the class is DLQ-routable (every class except ClassValidation
//     and ClassContextCanceled), publish the original payload to the
//     per-topic DLQ (`{topic}.dlq`) via publishDLQ. DLQ writes do NOT fall
//     back to outbox (TRD §C8) — a DLQ failure is a correlated-infra
//     signal that would amplify into an outbox backlog if we looped it.
//  3. Wrap in an *EmitError so the caller has the full diagnostic envelope
//     (topic, resource, event, class, cause). The Cause stays on the error
//     chain via Unwrap so errors.Is still works for sentinels.
//
// Errors returned from publishDirect DO feed the circuit breaker (via
// cb.Execute in Emit) regardless of whether the DLQ absorbed the failure
// — transport-level failures on the SOURCE topic are exactly the signal
// the breaker needs to trip, and whether DLQ succeeded has no bearing on
// source-topic health.
//
// topic is passed through from Emit (already computed once and threaded to
// every downstream call site) so we avoid recomputing event.Topic() on the
// hot path.
func (p *Producer) publishDirect(ctx context.Context, event Event, topic string) error {
	if p == nil {
		return ErrNilProducer
	}

	// Nil-ctx belt-and-suspenders. Emit already guards its caller ctx and
	// handleOutboxRow receives the Dispatcher's ctx (non-nil by contract),
	// but a direct tester or future internal caller must not reach franz-go
	// with a nil ctx — ProduceSync reads ctx.Err()/ctx.Done() internally.
	if ctx == nil {
		ctx = context.Background()
	}

	// Defense in depth: publishDirect can be called by the outbox handler
	// bridge in T4, so re-checking the closed flag keeps the invariant
	// "closed producer never touches the broker" true even off the main
	// Emit path.
	if p.closed.Load() {
		return ErrEmitterClosed
	}

	// Partition key: operator override (WithPartitionKey) if configured,
	// else the Event's struct-level default (tenant-id / system-eventtype).
	partKey := event.PartitionKey()
	if p.partFn != nil {
		partKey = p.partFn(event)
	}

	record := &kgo.Record{
		Topic:   topic,
		Key:     []byte(partKey),
		Value:   event.Payload,
		Headers: buildCloudEventsHeaders(event),
	}

	// firstAttempt is captured before the produce call so
	// x-lerian-dlq-first-failure-at on the DLQ message reflects when this
	// attempt started (not when it failed). For a replay-oriented tool,
	// the start timestamp is the more useful anchor.
	firstAttempt := time.Now().UTC()

	err := p.produceWithContext(ctx, record)
	if err == nil {
		return nil
	}

	// Classify and (conditionally) route to the per-topic DLQ. See
	// isDLQRoutable for the two-class deny-list. DLQ publish is
	// best-effort — failures are logged and metricked inside publishDLQ
	// and do not propagate here.
	cls := p.routeToDLQIfApplicable(ctx, event, err, topic, firstAttempt)

	return buildEmitError(event, err, topic, cls)
}

// produceWithContext sends a single record to the broker with reliable
// context cancellation. It replaces direct ProduceSync calls throughout the
// produce path.
//
// franz-go's ProduceSync blocks on a sync.WaitGroup that does NOT honor
// context cancellation: when the broker is completely unreachable and
// metadata fetches stall, RecordDeliveryTimeout only starts counting once
// the record is assigned to a partition — which never happens if the
// metadata loop can't discover any broker. The result is an indefinite
// hang that ignores ctx.Done().
//
// By using async Produce + select{} we guarantee the call returns within
// the caller's context deadline. The buffered channel (cap 1) ensures the
// callback goroutine never leaks even if we exit via the ctx.Done() branch
// — the callback writes to the buffer and returns.
func (p *Producer) produceWithContext(ctx context.Context, record *kgo.Record) error {
	errCh := make(chan error, 1)

	p.client.Produce(ctx, record, func(_ *kgo.Record, err error) {
		errCh <- err
	})

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
