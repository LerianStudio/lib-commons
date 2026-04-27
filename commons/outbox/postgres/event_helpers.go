package postgres

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/outbox"
	"github.com/google/uuid"
)

type createValues struct {
	id          uuid.UUID
	eventType   string
	aggregateID uuid.UUID
	payload     []byte
	status      string
	attempts    int
	publishedAt *time.Time
	lastError   string
	createdAt   time.Time
	updatedAt   time.Time
}

func normalizedCreateValues(event *outbox.OutboxEvent, now time.Time) createValues {
	createdAt := event.CreatedAt
	if createdAt.IsZero() {
		createdAt = now
	}

	updatedAt := event.UpdatedAt
	if updatedAt.IsZero() || updatedAt.Before(createdAt) {
		updatedAt = createdAt
	}

	return createValues{
		id:          event.ID,
		eventType:   strings.TrimSpace(event.EventType),
		aggregateID: event.AggregateID,
		payload:     event.Payload,
		status:      outbox.OutboxStatusPending,
		attempts:    0,
		publishedAt: nil,
		lastError:   "",
		createdAt:   createdAt,
		updatedAt:   updatedAt,
	}
}

func validateCreateEvent(event *outbox.OutboxEvent) error {
	if event == nil {
		return outbox.ErrOutboxEventRequired
	}

	if event.ID == uuid.Nil {
		return ErrIDRequired
	}

	if strings.TrimSpace(event.EventType) == "" {
		return ErrEventTypeRequired
	}

	if event.AggregateID == uuid.Nil {
		return ErrAggregateIDRequired
	}

	if len(event.Payload) == 0 {
		return outbox.ErrOutboxEventPayloadRequired
	}

	if len(event.Payload) > outbox.DefaultMaxPayloadBytes {
		return outbox.ErrOutboxEventPayloadTooLarge
	}

	if !json.Valid(event.Payload) {
		return outbox.ErrOutboxEventPayloadNotJSON
	}

	return nil
}
