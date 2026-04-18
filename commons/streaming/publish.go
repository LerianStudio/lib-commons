package streaming

import (
	"context"
	"encoding/json"

	"github.com/twmb/franz-go/pkg/kgo"
)

// maxPayloadBytes is the pre-flight payload-size ceiling. 1 MiB matches
// Redpanda's default broker.max.message.bytes and commons/outbox's
// DefaultMaxPayloadBytes, so the outbox-fallback path cannot accept a
// payload Redpanda would reject downstream.
const maxPayloadBytes = 1_048_576

// preFlight runs all caller-side validation on an Event before it reaches
// the circuit breaker. Defaults must already be applied by the caller —
// preFlight is pure validation, never mutation. Split from publishDirect in
// T3 so a storm of caller-fault emissions cannot trip the breaker.
//
// Order of checks is tuned so the cheapest / most-common caller mistake
// surfaces first:
//
//  1. Event toggle: resource.event key opts the event out at runtime
//  2. Tenant: non-system events require a tenant
//  3. Source: required CloudEvents ce-source
//  4. Payload size: reject before JSON scan
//  5. Payload JSON validity: prevents DLQ poisoning downstream
//
// Returns one of the caller sentinel errors (ErrEventDisabled,
// ErrMissingTenantID, ErrMissingSource, ErrPayloadTooLarge, ErrNotJSON).
// Never returns an *EmitError — caller faults have no Kafka-level class.
func (p *Producer) preFlight(event Event) error {
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

// publishDirect is the synchronous produce step. Caller-side validation is
// assumed complete (see preFlight) and defaults are assumed applied. This
// function assembles a kgo.Record from the Event and calls ProduceSync.
//
// The franz-go produce call is synchronous (ProduceSync + FirstErr). On
// non-nil err, we wrap in an *EmitError so the caller has the full diagnostic
// envelope (topic, resource, event, class, cause). The Cause stays on the
// error chain via Unwrap so errors.Is still works for sentinels.
//
// Errors returned from publishDirect DO feed the circuit breaker (via
// cb.Execute in Emit) — transport-level failures are exactly the signal the
// breaker needs to trip.
func (p *Producer) publishDirect(ctx context.Context, event Event) error {
	if p == nil {
		return ErrNilProducer
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
		Topic:   event.Topic(),
		Key:     []byte(partKey),
		Value:   event.Payload,
		Headers: buildCloudEventsHeaders(event),
	}

	// ProduceSync blocks until all records are fully produced OR an error
	// surfaces. With a single record, FirstErr() is the produce outcome.
	results := p.client.ProduceSync(ctx, record)
	if err := results.FirstErr(); err != nil {
		return &EmitError{
			ResourceType: event.ResourceType,
			EventType:    event.EventType,
			TenantID:     event.TenantID,
			Topic:        event.Topic(),
			Class:        classifyError(err),
			Cause:        err,
		}
	}

	return nil
}
