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

// publishDirect is the happy-path publish step: it validates the caller's
// Event, assembles a kgo.Record, and synchronously produces it to the broker.
//
// Pre-flight validation runs BEFORE any I/O so caller faults surface
// synchronously without touching the broker. Order of checks is tuned so the
// cheapest / most-common caller mistake surfaces first:
//
//  1. Producer lifecycle: closed producer → ErrEmitterClosed
//  2. Event toggle: resource.event key opts the event out at runtime
//  3. Tenant: non-system events require a tenant
//  4. Source: required CloudEvents ce-source
//  5. Payload size: reject before serialization
//  6. Payload JSON validity: prevents DLQ poisoning downstream
//
// After validation, we apply defaults (EventID, Timestamp, SchemaVersion,
// DataContentType) on a local copy of the Event so the caller's struct is
// not mutated — this keeps Emit concurrency-safe and makes tests that assert
// on the original Event struct stable.
//
// The franz-go produce call is synchronous (ProduceSync + FirstErr). On
// non-nil err, we wrap in an *EmitError so the caller has the full diagnostic
// envelope (topic, resource, event, class, cause). The Cause stays on the
// error chain via Unwrap so errors.Is still works for sentinels.
func (p *Producer) publishDirect(ctx context.Context, event Event) error {
	if p == nil {
		return ErrNilProducer
	}

	if p.closed.Load() {
		return ErrEmitterClosed
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

	// Fill zero-valued optional fields on a LOCAL COPY so we don't mutate
	// the caller's struct. This is a concurrency-safety boundary: if the
	// caller re-uses the same Event across goroutines, each call gets its
	// own defaults without races.
	(&event).ApplyDefaults()

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
