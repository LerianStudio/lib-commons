//go:build unit

package outbox

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestNewOutboxEvent(t *testing.T) {
	t.Parallel()

	aggregateID := uuid.New()
	payload := []byte(`{"key":"value"}`)

	event, err := NewOutboxEvent(context.Background(), "event.type", aggregateID, payload)
	require.NoError(t, err)
	require.NotNil(t, event)
	require.Equal(t, "event.type", event.EventType)
	require.Equal(t, aggregateID, event.AggregateID)
	require.Equal(t, payload, event.Payload)
	require.Equal(t, OutboxStatusPending, event.Status)
	require.Equal(t, 0, event.Attempts)
	require.NotEqual(t, uuid.Nil, event.ID)
	require.False(t, event.CreatedAt.IsZero())
	require.False(t, event.UpdatedAt.IsZero())
	require.Equal(t, event.CreatedAt, event.UpdatedAt)
}

func TestNewOutboxEventValidation(t *testing.T) {
	t.Parallel()

	event, err := NewOutboxEvent(context.Background(), "", uuid.New(), []byte(`{"k":"v"}`))
	require.Error(t, err)
	require.Nil(t, event)
	require.Contains(t, err.Error(), "event type")

	event, err = NewOutboxEvent(context.Background(), "type", uuid.Nil, []byte(`{"k":"v"}`))
	require.Error(t, err)
	require.Nil(t, event)
	require.Contains(t, err.Error(), "aggregate id")

	event, err = NewOutboxEvent(context.Background(), "type", uuid.New(), nil)
	require.Error(t, err)
	require.Nil(t, event)
	require.Contains(t, err.Error(), "payload")

	oversizedPayload := make([]byte, DefaultMaxPayloadBytes+1)
	event, err = NewOutboxEvent(context.Background(), "type", uuid.New(), oversizedPayload)
	require.Error(t, err)
	require.Nil(t, event)
	require.ErrorIs(t, err, ErrOutboxEventPayloadTooLarge)

	event, err = NewOutboxEvent(context.Background(), "type", uuid.New(), []byte("not-json"))
	require.Error(t, err)
	require.Nil(t, event)
	require.ErrorIs(t, err, ErrOutboxEventPayloadNotJSON)

	event, err = NewOutboxEvent(context.Background(), "   ", uuid.New(), []byte(`{"k":"v"}`))
	require.Error(t, err)
	require.Nil(t, event)
	require.Contains(t, err.Error(), "event type")
}

func TestNewOutboxEventWithID(t *testing.T) {
	t.Parallel()

	eventID := uuid.New()
	aggregateID := uuid.New()

	event, err := NewOutboxEventWithID(context.Background(), eventID, "event.type", aggregateID, []byte(`{"key":"value"}`))
	require.NoError(t, err)
	require.NotNil(t, event)
	require.Equal(t, eventID, event.ID)
	require.Equal(t, OutboxStatusPending, event.Status)
}

func TestNewOutboxEventWithIDValidation(t *testing.T) {
	t.Parallel()

	event, err := NewOutboxEventWithID(context.Background(), uuid.Nil, "event.type", uuid.New(), []byte(`{"key":"value"}`))
	require.Error(t, err)
	require.Nil(t, event)
	require.Contains(t, err.Error(), "event id")
}
