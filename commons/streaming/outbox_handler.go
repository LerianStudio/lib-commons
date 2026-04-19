package streaming

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/outbox"
)

// streamingOutboxPrefix is the topic-name prefix that every streaming
// outbox row carries as its EventType. Used both as a defensive filter
// inside handleOutboxRow and as a reasonable hint to operators scanning
// the outbox table.
const streamingOutboxPrefix = "lerian.streaming."

// RegisterOutboxHandler registers the streaming relay handler with the
// supplied *outbox.HandlerRegistry for each of the given event types.
//
// The outbox HandlerRegistry routes by exact EventType match (not prefix),
// so the caller must enumerate every concrete topic name it wants relayed
// — typically the full set of streaming event types the service emits
// (e.g. "lerian.streaming.transaction.created",
// "lerian.streaming.account.updated"). Registering zero event types is
// permitted but a no-op, which typically indicates a wiring mistake.
//
// When the Dispatcher reads a PENDING outbox row whose EventType matches
// one of the registered keys, the handler unmarshals the Payload back
// into a streaming.Event and calls p.publishDirect — NOT p.Emit. This
// deliberately bypasses the circuit-breaker wrapper and the outbox
// fallback so handler retries never themselves feed the outbox (the
// loop-prevention invariant from TRD §C7).
//
// Returns:
//   - ErrNilProducer when called on a nil *Producer.
//   - ErrNilOutboxRegistry when registry is nil.
//   - The first error from registry.Register, which could be
//     ErrEventTypeRequired, ErrEventHandlerRequired, or
//     ErrHandlerAlreadyRegistered. All other registrations roll back
//     are NOT attempted — the caller gets the first failure.
//
// Nil-receiver safe. Idempotent under error: if one event type fails to
// register, retrying with a disjoint set still works.
func (p *Producer) RegisterOutboxHandler(registry *outbox.HandlerRegistry, eventTypes ...string) error {
	if p == nil {
		return ErrNilProducer
	}

	if registry == nil {
		return ErrNilOutboxRegistry
	}

	for _, eventType := range eventTypes {
		if err := registry.Register(eventType, p.handleOutboxRow); err != nil {
			return fmt.Errorf("streaming: register outbox handler for %q: %w", eventType, err)
		}
	}

	return nil
}

// handleOutboxRow is the outbox-Dispatcher-facing handler. It receives a
// previously-persisted OutboxEvent, reconstructs the original
// streaming.Event, and relays it through p.publishDirect.
//
// Loop prevention (TRD §C7): this intentionally calls publishDirect, NOT
// Emit. Emit's circuit-open branch would itself write to the outbox — so
// if we called Emit here and the broker happened to still be down, the
// row we're trying to drain would re-enqueue a clone of itself, producing
// a monotonically-growing outbox backlog under sustained broker outage.
// publishDirect has no such fallback; it returns the error and the
// Dispatcher honors its normal retry/backoff policy.
//
// The EventType prefix check is defensive: the HandlerRegistry is exact-
// match, so handleOutboxRow should only ever be invoked for event types
// the caller registered. But if a bootstrap misconfiguration registers it
// for a non-streaming event, surfacing a silent publish of garbage
// through publishDirect would be worse than a no-op with a warning log
// would have been. The prefix check short-circuits that.
func (p *Producer) handleOutboxRow(ctx context.Context, row *outbox.OutboxEvent) error {
	if p == nil {
		return ErrNilProducer
	}

	if row == nil {
		return outbox.ErrOutboxEventRequired
	}

	if !strings.HasPrefix(row.EventType, streamingOutboxPrefix) {
		// Not a streaming row. The Dispatcher shouldn't be calling us —
		// this means someone registered the handler for an unrelated
		// event type. Returning nil (not an error) is the conservative
		// choice: an error would cause the Dispatcher to mark the row
		// FAILED, which is destructive for a row that wasn't ours in
		// the first place.
		//
		// Log at WARN so the operator sees the misconfiguration instead
		// of silently dropping every mis-registered row. A quiet no-op
		// here previously masked registration bugs in production.
		p.logger.Log(ctx, log.LevelWarn, "streaming: outbox row routed to streaming handler but EventType lacks expected prefix",
			log.String("row_id", row.ID.String()),
			log.String("event_type", row.EventType),
			log.String("expected_prefix", streamingOutboxPrefix),
		)

		return nil
	}

	var event Event
	// musttag nolint: Event ships in T1 without explicit `json:` tags — the
	// round-trip is symmetric with publishToOutbox's json.Marshal. Adding
	// tags would be a T1 scope change.
	if err := json.Unmarshal(row.Payload, &event); err != nil { //nolint:musttag // see above; T1 scope
		// Invalid payload. Do NOT retry — the row is structurally
		// broken. Returning an error lets the Dispatcher mark it FAILED
		// and stop retrying; the operator can inspect the row and
		// either fix it by hand or MarkInvalid it. This is strictly
		// better than panicking.
		return fmt.Errorf("streaming: unmarshal outbox row %s: %w", row.ID, err)
	}

	// Re-run preFlight on the deserialized Event. An outbox row is just
	// persisted bytes — it may have been corrupted in transit, tampered
	// with at rest, or authored before we tightened header-sanitization
	// rules. Re-validation here guarantees publishDirect never sees a
	// structurally-invalid Event, and surfaces the violation as a FAILED
	// row (not a silent publish of garbage).
	//
	// Same safety property as unmarshal failure: returning an error marks
	// the row FAILED so the Dispatcher stops retrying a hopeless row.
	if err := p.preFlight(event); err != nil {
		return fmt.Errorf("streaming: outbox replay preflight rejected row %s: %w", row.ID, err)
	}

	// publishDirect (NOT Emit) — the whole point of this handler.
	//
	// Use the persisted topic (row.EventType) rather than recomputing
	// event.Topic(). The outbox row captured the original destination;
	// recomputing would break replays if Topic() derivation changes
	// (e.g., a schema-major suffix or resource rename).
	if err := p.publishDirect(ctx, event, row.EventType); err != nil {
		return fmt.Errorf("streaming: replay outbox row %s: %w", row.ID, err)
	}

	return nil
}
