package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/assert"
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
}

// NewOutboxEvent creates a valid outbox event initialized as pending.
func NewOutboxEvent(
	ctx context.Context,
	eventType string,
	aggregateID uuid.UUID,
	payload []byte,
) (*OutboxEvent, error) {
	return NewOutboxEventWithID(ctx, uuid.New(), eventType, aggregateID, payload)
}

// NewOutboxEventWithID creates a valid outbox event initialized as pending using a caller-provided ID.
func NewOutboxEventWithID(
	ctx context.Context,
	eventID uuid.UUID,
	eventType string,
	aggregateID uuid.UUID,
	payload []byte,
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

	return &OutboxEvent{
		ID:          eventID,
		EventType:   eventType,
		AggregateID: aggregateID,
		Payload:     payload,
		Status:      OutboxStatusPending,
		Attempts:    0,
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}
