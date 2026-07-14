//go:build unit

package outbox

import (
	"context"
	"errors"
	"sync"
	"testing"

	libLog "github.com/LerianStudio/lib-observability/v2/log"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"
)

type logEntry struct {
	level  libLog.Level
	msg    string
	fields []libLog.Field
}

// recordingLogger captures emitted log entries for assertions.
type recordingLogger struct {
	mu      sync.Mutex
	entries []logEntry
}

func (l *recordingLogger) Log(_ context.Context, level libLog.Level, msg string, fields ...libLog.Field) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, logEntry{level: level, msg: msg, fields: fields})
}

func (l *recordingLogger) With(...libLog.Field) libLog.Logger { return l }
func (l *recordingLogger) WithGroup(string) libLog.Logger     { return l }
func (l *recordingLogger) Enabled(libLog.Level) bool          { return true }
func (l *recordingLogger) Sync(context.Context) error         { return nil }

func (l *recordingLogger) hasLevel(level libLog.Level) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.entries {
		if e.level == level {
			return true
		}
	}

	return false
}

func TestDispatcher_HandlePublishError_NilCallbacksDoNotBreak(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{}
	eventID := uuid.New()
	repo.pending = []*OutboxEvent{{ID: eventID, EventType: "payment.created", Payload: []byte("ok")}}

	handlers := NewHandlerRegistry()
	require.NoError(t, handlers.Register("payment.created", func(context.Context, *OutboxEvent) error {
		return errors.New("temporary broker outage")
	}))

	dispatcher, err := NewDispatcher(repo, handlers, nil, noop.NewTracerProvider().Tracer("test"), WithPublishMaxAttempts(1))
	require.NoError(t, err)

	require.NotPanics(t, func() {
		_ = dispatcher.DispatchOnce(context.Background())
	})
	require.Equal(t, []uuid.UUID{eventID}, repo.markedFail)
}

func TestDispatcher_HandlePublishError_CallbackPanicIsSwallowed(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{}
	eventID := uuid.New()
	repo.pending = []*OutboxEvent{{ID: eventID, EventType: "payment.created", Payload: []byte("ok")}}

	handlers := NewHandlerRegistry()
	require.NoError(t, handlers.Register("payment.created", func(context.Context, *OutboxEvent) error {
		return errors.New("temporary broker outage")
	}))

	logger := &recordingLogger{}
	dispatcher, err := NewDispatcher(
		repo,
		handlers,
		logger,
		noop.NewTracerProvider().Tracer("test"),
		WithPublishMaxAttempts(1),
		WithMaxDispatchAttempts(10),
		WithOnFailed(func(context.Context, *OutboxEvent, error) {
			panic("boom")
		}),
	)
	require.NoError(t, err)

	require.NotPanics(t, func() {
		_ = dispatcher.DispatchOnce(context.Background())
	})
	require.True(t, logger.hasLevel(libLog.LevelWarn))
}

func TestDispatcher_HandlePublishError_LogsWarnOnFailure(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{}
	eventID := uuid.New()
	repo.pending = []*OutboxEvent{{ID: eventID, EventType: "payment.created", Payload: []byte("ok")}}

	handlers := NewHandlerRegistry()
	require.NoError(t, handlers.Register("payment.created", func(context.Context, *OutboxEvent) error {
		return errors.New("temporary broker outage")
	}))

	logger := &recordingLogger{}
	dispatcher, err := NewDispatcher(
		repo,
		handlers,
		logger,
		noop.NewTracerProvider().Tracer("test"),
		WithPublishMaxAttempts(1),
		WithMaxDispatchAttempts(10),
	)
	require.NoError(t, err)

	_ = dispatcher.DispatchOnce(context.Background())

	logger.mu.Lock()
	defer logger.mu.Unlock()

	var found bool
	for _, e := range logger.entries {
		if e.level != libLog.LevelWarn || e.msg != "outbox event publish failed" {
			continue
		}

		var sawEventID, sawEventType bool
		for _, f := range e.fields {
			if f.Key == "event_id" && f.Value == eventID.String() {
				sawEventID = true
			}
			if f.Key == "event_type" && f.Value == "payment.created" {
				sawEventType = true
			}
		}
		if sawEventID && sawEventType {
			found = true
		}
	}
	require.True(t, found, "expected WARN log with event_id and event_type")
}

func TestDispatcher_HandlePublishError_OnInvalidCalledOnMaxAttempts(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{}
	eventID := uuid.New()
	// Attempts already at max-1 so this attempt (Attempts+1) reaches MaxDispatchAttempts.
	repo.pending = []*OutboxEvent{{ID: eventID, EventType: "payment.created", Payload: []byte("ok"), Attempts: 9}}

	handlers := NewHandlerRegistry()
	require.NoError(t, handlers.Register("payment.created", func(context.Context, *OutboxEvent) error {
		return errors.New("temporary broker outage")
	}))

	var (
		mu             sync.Mutex
		onInvalidCalls []uuid.UUID
		onFailedCalls  []uuid.UUID
	)

	dispatcher, err := NewDispatcher(
		repo,
		handlers,
		nil,
		noop.NewTracerProvider().Tracer("test"),
		WithPublishMaxAttempts(1),
		WithMaxDispatchAttempts(10),
		WithOnInvalid(func(_ context.Context, event *OutboxEvent, _ error) {
			mu.Lock()
			onInvalidCalls = append(onInvalidCalls, event.ID)
			mu.Unlock()
		}),
		WithOnFailed(func(_ context.Context, event *OutboxEvent, _ error) {
			mu.Lock()
			onFailedCalls = append(onFailedCalls, event.ID)
			mu.Unlock()
		}),
	)
	require.NoError(t, err)

	_ = dispatcher.DispatchOnce(context.Background())

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []uuid.UUID{eventID}, onInvalidCalls)
	require.Empty(t, onFailedCalls)
}

func TestDispatcher_HandlePublishError_OnInvalidCalledOnNonRetryable(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{}
	eventID := uuid.New()
	repo.pending = []*OutboxEvent{{ID: eventID, EventType: "payment.created", Payload: []byte("ok")}}

	nonRetryable := errors.New("validation failed")
	handlers := NewHandlerRegistry()
	require.NoError(t, handlers.Register("payment.created", func(context.Context, *OutboxEvent) error {
		return nonRetryable
	}))

	var (
		mu             sync.Mutex
		onInvalidCalls []uuid.UUID
		onFailedCalls  []uuid.UUID
	)

	dispatcher, err := NewDispatcher(
		repo,
		handlers,
		nil,
		noop.NewTracerProvider().Tracer("test"),
		WithPublishMaxAttempts(1),
		WithRetryClassifier(RetryClassifierFunc(func(err error) bool {
			return errors.Is(err, nonRetryable)
		})),
		WithOnInvalid(func(_ context.Context, event *OutboxEvent, _ error) {
			mu.Lock()
			onInvalidCalls = append(onInvalidCalls, event.ID)
			mu.Unlock()
		}),
		WithOnFailed(func(_ context.Context, event *OutboxEvent, _ error) {
			mu.Lock()
			onFailedCalls = append(onFailedCalls, event.ID)
			mu.Unlock()
		}),
	)
	require.NoError(t, err)

	_ = dispatcher.DispatchOnce(context.Background())

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []uuid.UUID{eventID}, onInvalidCalls)
	require.Empty(t, onFailedCalls)
}

func TestDispatcher_HandlePublishError_OnFailedCalledOnIntermediateFailure(t *testing.T) {
	t.Parallel()

	repo := &fakeRepo{}
	eventID := uuid.New()
	repo.pending = []*OutboxEvent{{ID: eventID, EventType: "payment.created", Payload: []byte("ok"), Attempts: 0}}

	handlers := NewHandlerRegistry()
	require.NoError(t, handlers.Register("payment.created", func(context.Context, *OutboxEvent) error {
		return errors.New("temporary broker outage")
	}))

	var (
		mu             sync.Mutex
		onInvalidCalls []uuid.UUID
		onFailedCalls  []uuid.UUID
	)

	dispatcher, err := NewDispatcher(
		repo,
		handlers,
		nil,
		noop.NewTracerProvider().Tracer("test"),
		WithPublishMaxAttempts(1),
		WithMaxDispatchAttempts(10),
		WithOnInvalid(func(_ context.Context, event *OutboxEvent, _ error) {
			mu.Lock()
			onInvalidCalls = append(onInvalidCalls, event.ID)
			mu.Unlock()
		}),
		WithOnFailed(func(_ context.Context, event *OutboxEvent, _ error) {
			mu.Lock()
			onFailedCalls = append(onFailedCalls, event.ID)
			mu.Unlock()
		}),
	)
	require.NoError(t, err)

	_ = dispatcher.DispatchOnce(context.Background())

	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []uuid.UUID{eventID}, onFailedCalls)
	require.Empty(t, onInvalidCalls)
}
