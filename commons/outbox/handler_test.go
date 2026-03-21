//go:build unit

package outbox

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestHandlerRegistry_RegisterAndHandle(t *testing.T) {
	t.Parallel()

	registry := NewHandlerRegistry()
	handled := false

	err := registry.Register("payment.created", func(_ context.Context, event *OutboxEvent) error {
		handled = true
		require.Equal(t, "payment.created", event.EventType)
		return nil
	})
	require.NoError(t, err)

	event := &OutboxEvent{ID: uuid.New(), EventType: "payment.created", Payload: []byte(`{"ok":true}`)}
	err = registry.Handle(context.Background(), event)
	require.NoError(t, err)
	require.True(t, handled)
}

func TestHandlerRegistry_RegisterDuplicate(t *testing.T) {
	t.Parallel()

	registry := NewHandlerRegistry()
	require.NoError(t, registry.Register("same", func(_ context.Context, _ *OutboxEvent) error { return nil }))

	err := registry.Register("same", func(_ context.Context, _ *OutboxEvent) error { return nil })
	require.ErrorIs(t, err, ErrHandlerAlreadyRegistered)
}

func TestHandlerRegistry_RegisterNormalizesEventType(t *testing.T) {
	t.Parallel()

	registry := NewHandlerRegistry()
	require.NoError(t, registry.Register("  payment.created  ", func(_ context.Context, _ *OutboxEvent) error { return nil }))

	err := registry.Handle(context.Background(), &OutboxEvent{ID: uuid.New(), EventType: "payment.created", Payload: []byte(`{"x":1}`)})
	require.NoError(t, err)
}

func TestHandlerRegistry_HandleMissing(t *testing.T) {
	t.Parallel()

	registry := NewHandlerRegistry()
	err := registry.Handle(context.Background(), &OutboxEvent{ID: uuid.New(), EventType: "missing", Payload: []byte(`{"x":1}`)})
	require.ErrorIs(t, err, ErrHandlerNotRegistered)
}

func TestHandlerRegistry_HandlePropagatesHandlerError(t *testing.T) {
	t.Parallel()

	registry := NewHandlerRegistry()
	handlerErr := errors.New("publish to broker failed")
	require.NoError(t, registry.Register("payment.created", func(_ context.Context, _ *OutboxEvent) error {
		return handlerErr
	}))

	event := &OutboxEvent{ID: uuid.New(), EventType: "payment.created", Payload: []byte(`{"ok":true}`)}
	err := registry.Handle(context.Background(), event)
	require.ErrorIs(t, err, handlerErr)
}

func TestRetryClassifierFunc_IsNonRetryable(t *testing.T) {
	t.Parallel()

	classifier := RetryClassifierFunc(func(err error) bool {
		return errors.Is(err, ErrHandlerNotRegistered)
	})

	require.True(t, classifier.IsNonRetryable(ErrHandlerNotRegistered))
	require.False(t, classifier.IsNonRetryable(errors.New("other")))
}

func TestHandlerRegistry_NilReceiver(t *testing.T) {
	t.Parallel()

	var registry *HandlerRegistry

	err := registry.Register("event", func(context.Context, *OutboxEvent) error { return nil })
	require.ErrorIs(t, err, ErrHandlerRegistryRequired)

	err = registry.Handle(context.Background(), &OutboxEvent{ID: uuid.New(), EventType: "event", Payload: []byte(`{"ok":true}`)})
	require.ErrorIs(t, err, ErrHandlerRegistryRequired)
}

func TestHandlerRegistry_RegisterValidation(t *testing.T) {
	t.Parallel()

	registry := NewHandlerRegistry()

	err := registry.Register("", func(context.Context, *OutboxEvent) error { return nil })
	require.ErrorIs(t, err, ErrEventTypeRequired)

	err = registry.Register("payment.created", nil)
	require.ErrorIs(t, err, ErrEventHandlerRequired)
}

func TestHandlerRegistry_HandleNilEvent(t *testing.T) {
	t.Parallel()

	registry := NewHandlerRegistry()
	err := registry.Handle(context.Background(), nil)
	require.ErrorIs(t, err, ErrOutboxEventRequired)
}

func TestHandlerRegistry_HandleRejectsBlankEventType(t *testing.T) {
	t.Parallel()

	registry := NewHandlerRegistry()
	err := registry.Handle(context.Background(), &OutboxEvent{ID: uuid.New(), EventType: "   ", Payload: []byte(`{"ok":true}`)})
	require.ErrorIs(t, err, ErrEventTypeRequired)
}
