package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/LerianStudio/lib-observability/v4/assert"
	"github.com/google/uuid"
)

const (
	OutboxStatusPending    = "PENDING"
	OutboxStatusProcessing = "PROCESSING"
	OutboxStatusPublished  = "PUBLISHED"
	OutboxStatusFailed     = "FAILED"
	OutboxStatusInvalid    = "INVALID"
	DefaultMaxPayloadBytes = 1 << 20
)

// OutboxEvent is an event stored in the outbox for reliable delivery.
type OutboxEvent struct {
	ID          uuid.UUID
	EventType   string
	AggregateID uuid.UUID
	Payload     []byte
	Status      string
	Attempts    int
	PublishedAt *time.Time
	LastError   string
	CreatedAt   time.Time
	UpdatedAt   time.Time

	// TraceContext is an optional W3C trace carrier (traceparent, and
	// tracestate when present) captured from the producer's context so the
	// dispatcher can publish the event inside the originating trace instead of
	// its own background trace. It is persisted only by repositories
	// configured with a trace context column; see WithTraceContext.
	TraceContext map[string]string
}

// EventOption customizes an outbox event at construction.
type EventOption func(*OutboxEvent)

// WithTraceContext captures the caller's W3C trace context onto the event, so
// the dispatcher later publishes it as a child of the producing request rather
// than starting a disconnected trace. Only traceparent and tracestate are
// captured; a context without a valid span leaves the carrier unset.
func WithTraceContext(ctx context.Context) EventOption {
	return func(event *OutboxEvent) {
		event.TraceContext = CaptureTraceContext(ctx)
	}
}

// WithTraceCarrier sets an already-extracted W3C trace carrier on the event,
// for callers that hold one outside a Go context (a consumed message header,
// for instance). The carrier is reduced to traceparent and tracestate.
func WithTraceCarrier(carrier map[string]string) EventOption {
	return func(event *OutboxEvent) {
		event.TraceContext = SanitizeTraceContext(carrier)
	}
}

// NewOutboxEvent creates a valid outbox event initialized as pending.
func NewOutboxEvent(
	ctx context.Context,
	eventType string,
	aggregateID uuid.UUID,
	payload []byte,
	opts ...EventOption,
) (*OutboxEvent, error) {
	return NewOutboxEventWithID(ctx, uuid.New(), eventType, aggregateID, payload, opts...)
}

// NewOutboxEventWithID creates a valid outbox event initialized as pending using a caller-provided ID.
func NewOutboxEventWithID(
	ctx context.Context,
	eventID uuid.UUID,
	eventType string,
	aggregateID uuid.UUID,
	payload []byte,
	opts ...EventOption,
) (*OutboxEvent, error) {
	asserter := assert.New(ctx, nil, "outbox", "outbox.new_event")

	if err := asserter.That(ctx, eventID != uuid.Nil, "event id is required"); err != nil {
		return nil, fmt.Errorf("outbox event id: %w", err)
	}

	eventType = strings.TrimSpace(eventType)

	if err := asserter.NotEmpty(ctx, eventType, "event type is required"); err != nil {
		return nil, fmt.Errorf("outbox event type: %w", err)
	}

	if err := asserter.That(ctx, aggregateID != uuid.Nil, "aggregate id is required"); err != nil {
		return nil, fmt.Errorf("outbox event aggregate id: %w", err)
	}

	if err := asserter.That(ctx, len(payload) > 0, "payload is required"); err != nil {
		return nil, fmt.Errorf("outbox event payload: %w", err)
	}

	if err := asserter.That(ctx, len(payload) <= DefaultMaxPayloadBytes, "payload exceeds max size"); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrOutboxEventPayloadTooLarge, err)
	}

	if err := asserter.That(ctx, json.Valid(payload), "payload must be valid JSON"); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrOutboxEventPayloadNotJSON, err)
	}

	now := time.Now().UTC()

	event := &OutboxEvent{
		ID:          eventID,
		EventType:   eventType,
		AggregateID: aggregateID,
		Payload:     payload,
		Status:      OutboxStatusPending,
		Attempts:    0,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(event)
		}
	}

	event.TraceContext = SanitizeTraceContext(event.TraceContext)

	return event, nil
}
