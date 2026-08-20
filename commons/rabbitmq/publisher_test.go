//go:build unit

package rabbitmq

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	libLog "github.com/LerianStudio/lib-observability/v2/log"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

type mockConfirmableChannel struct {
	mu              sync.Mutex
	confirmErr      error
	publishErr      error
	confirms        chan amqp.Confirmation
	closeNotify     chan *amqp.Error
	lastPublishing  amqp.Publishing
	confirmCalled   bool
	publishCalled   bool
	closeCalled     bool
	deliveryCounter uint64
}

type panicPublisherLogger struct {
	used bool
}

func (logger *panicPublisherLogger) Log(context.Context, libLog.Level, string, ...libLog.Field) {
	logger.used = true
}

func (logger *panicPublisherLogger) With(...libLog.Field) libLog.Logger {
	return logger
}

func (logger *panicPublisherLogger) WithGroup(string) libLog.Logger {
	return logger
}

func (logger *panicPublisherLogger) Enabled(libLog.Level) bool {
	return true
}

func (logger *panicPublisherLogger) Sync(context.Context) error {
	return nil
}

func newMockChannel() *mockConfirmableChannel {
	return &mockConfirmableChannel{
		closeNotify: make(chan *amqp.Error, 1),
	}
}

func (m *mockConfirmableChannel) Confirm(_ bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.confirmCalled = true

	return m.confirmErr
}

func (m *mockConfirmableChannel) NotifyPublish(confirm chan amqp.Confirmation) chan amqp.Confirmation {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.confirms = confirm

	return confirm
}

func (m *mockConfirmableChannel) NotifyClose(_ chan *amqp.Error) chan *amqp.Error {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.closeNotify
}

func (m *mockConfirmableChannel) PublishWithContext(
	_ context.Context,
	_, _ string,
	_, _ bool,
	msg amqp.Publishing,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.publishCalled = true
	m.deliveryCounter++
	m.lastPublishing = msg

	return m.publishErr
}

// publishedMessage returns the message the publisher handed to the broker.
func (m *mockConfirmableChannel) publishedMessage() amqp.Publishing {
	m.mu.Lock()
	defer m.mu.Unlock()

	return m.lastPublishing
}

func (m *mockConfirmableChannel) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closeCalled {
		return nil
	}

	m.closeCalled = true
	if m.confirms != nil {
		close(m.confirms)
	}

	return nil
}

func (m *mockConfirmableChannel) sendConfirm(ack bool) {
	m.mu.Lock()
	tag := m.deliveryCounter
	confirms := m.confirms
	m.mu.Unlock()

	confirms <- amqp.Confirmation{DeliveryTag: tag, Ack: ack}
}

func (m *mockConfirmableChannel) waitForPublish(t *testing.T) {
	t.Helper()

	require.Eventually(t, func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()

		return m.deliveryCounter > 0
	}, time.Second, time.Millisecond)
}

func TestNewConfirmablePublisher_NilConnection(t *testing.T) {
	t.Parallel()

	publisher, err := NewConfirmablePublisher(nil)
	assert.Nil(t, publisher)
	assert.ErrorIs(t, err, ErrConnectionRequired)
}

func TestNewConfirmablePublisher_NilChannel(t *testing.T) {
	t.Parallel()

	conn := &RabbitMQConnection{Channel: nil}
	publisher, err := NewConfirmablePublisher(conn)
	assert.Nil(t, publisher)
	assert.ErrorIs(t, err, ErrChannelRequired)
}

func TestConfirmablePublisher_Publish_Success(t *testing.T) {
	t.Parallel()

	ch := newMockChannel()
	publisher, err := NewConfirmablePublisherFromChannel(ch)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := publisher.Close(); err != nil {
			t.Errorf("cleanup: publisher close: %v", err)
		}
	})

	go func() {
		ch.waitForPublish(t)
		ch.sendConfirm(true)
	}()

	err = publisher.Publish(context.Background(), "exchange", "route", false, false, amqp.Publishing{Body: []byte("ok")})
	require.NoError(t, err)
	assert.True(t, ch.publishCalled)
}

func TestConfirmablePublisher_PublishAndWaitConfirm_Success(t *testing.T) {
	t.Parallel()

	ch := newMockChannel()
	publisher, err := NewConfirmablePublisherFromChannel(ch)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := publisher.Close(); err != nil {
			t.Errorf("cleanup: publisher close: %v", err)
		}
	})

	go func() {
		ch.waitForPublish(t)
		ch.sendConfirm(true)
	}()

	err = publisher.PublishAndWaitConfirm(
		context.Background(),
		"exchange",
		"route",
		false,
		false,
		amqp.Publishing{Body: []byte("ok")},
	)
	require.NoError(t, err)
}

func TestConfirmablePublisher_Publish_Nack(t *testing.T) {
	t.Parallel()

	ch := newMockChannel()
	publisher, err := NewConfirmablePublisherFromChannel(ch)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := publisher.Close(); err != nil {
			t.Errorf("cleanup: publisher close: %v", err)
		}
	})

	go func() {
		ch.waitForPublish(t)
		ch.sendConfirm(false)
	}()

	err = publisher.Publish(context.Background(), "exchange", "route", false, false, amqp.Publishing{Body: []byte("x")})
	require.ErrorIs(t, err, ErrPublishNacked)
}

func TestConfirmablePublisher_Publish_Timeout(t *testing.T) {
	t.Parallel()

	ch := newMockChannel()
	publisher, err := NewConfirmablePublisherFromChannel(ch, WithConfirmTimeout(30*time.Millisecond))
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := publisher.Close(); err != nil {
			t.Errorf("cleanup: publisher close: %v", err)
		}
	})

	err = publisher.Publish(context.Background(), "exchange", "route", false, false, amqp.Publishing{Body: []byte("x")})
	require.ErrorIs(t, err, ErrConfirmTimeout)
}

func TestNewConfirmablePublisherFromChannel_ConfirmError(t *testing.T) {
	t.Parallel()

	ch := newMockChannel()
	ch.confirmErr = errors.New("confirm mode unavailable")

	publisher, err := NewConfirmablePublisherFromChannel(ch)
	require.Nil(t, publisher)
	require.ErrorIs(t, err, ErrConfirmModeUnavailable)
}

func TestConfirmablePublisher_ReconnectAfterCloseFails(t *testing.T) {
	t.Parallel()

	ch1 := newMockChannel()
	publisher, err := NewConfirmablePublisherFromChannel(ch1)
	require.NoError(t, err)

	require.NoError(t, publisher.Close())
	err = publisher.Reconnect(newMockChannel())
	require.ErrorIs(t, err, ErrReconnectAfterClose)
}

func TestConfirmablePublisher_ReconnectNilChannel(t *testing.T) {
	t.Parallel()

	ch := newMockChannel()
	publisher, err := NewConfirmablePublisherFromChannel(ch)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := publisher.Close(); err != nil {
			t.Errorf("cleanup: publisher close: %v", err)
		}
	})

	err = publisher.Reconnect(nil)
	require.ErrorIs(t, err, ErrChannelRequired)
}

func TestConfirmablePublisher_WithConfirmTimeoutZeroKeepsDefault(t *testing.T) {
	t.Parallel()

	ch := newMockChannel()
	publisher, err := NewConfirmablePublisherFromChannel(ch, WithConfirmTimeout(0))
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := publisher.Close(); err != nil {
			t.Errorf("cleanup: publisher close: %v", err)
		}
	})

	require.Equal(t, DefaultConfirmTimeout, publisher.confirmTimeout)
}

func TestConfirmablePublisher_WithConfirmTimeoutNegativeKeepsDefault(t *testing.T) {
	t.Parallel()

	ch := newMockChannel()
	publisher, err := NewConfirmablePublisherFromChannel(ch, WithConfirmTimeout(-time.Second))
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := publisher.Close(); err != nil {
			t.Errorf("cleanup: publisher close: %v", err)
		}
	})

	require.Equal(t, DefaultConfirmTimeout, publisher.confirmTimeout)
}

func TestConfirmablePublisher_WithRecoveryBackoffRejectsInitialGreaterThanMax(t *testing.T) {
	t.Parallel()

	ch := newMockChannel()
	publisher, err := NewConfirmablePublisherFromChannel(ch, WithRecoveryBackoff(5*time.Second, time.Second))
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := publisher.Close(); err != nil {
			t.Errorf("cleanup: publisher close: %v", err)
		}
	})

	require.Nil(t, publisher.recovery)
}

func TestConfirmablePublisher_ReconnectAfterRecoveryPreparation(t *testing.T) {
	t.Parallel()

	ch1 := newMockChannel()
	publisher, err := NewConfirmablePublisherFromChannel(ch1)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := publisher.Close(); err != nil {
			t.Errorf("cleanup: publisher close: %v", err)
		}
	})

	require.True(t, publisher.prepareForRecovery())
	recoveryDone := publisher.done

	ch2 := newMockChannel()
	require.NoError(t, publisher.Reconnect(ch2))
	require.Equal(t, recoveryDone, publisher.done)

	go func() {
		ch2.waitForPublish(t)
		ch2.sendConfirm(true)
	}()

	err = publisher.Publish(context.Background(), "exchange", "route", false, false, amqp.Publishing{Body: []byte("ok")})
	require.NoError(t, err)
}

func TestConfirmablePublisher_ConcurrentReconnectSerialized(t *testing.T) {
	t.Parallel()

	publisher, err := NewConfirmablePublisherFromChannel(newMockChannel())
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := publisher.Close(); err != nil {
			t.Errorf("cleanup: publisher close: %v", err)
		}
	})

	require.True(t, publisher.prepareForRecovery())

	start := make(chan struct{})
	errs := make(chan error, 2)

	go func() {
		<-start
		errs <- publisher.Reconnect(newMockChannel())
	}()

	go func() {
		<-start
		errs <- publisher.Reconnect(newMockChannel())
	}()

	close(start)

	errA := <-errs
	errB := <-errs

	if errA == nil {
		require.ErrorIs(t, errB, ErrReconnectWhileOpen)

		return
	}

	require.Nil(t, errB)
	require.ErrorIs(t, errA, ErrReconnectWhileOpen)
}

func TestConfirmablePublisher_PublishDuringRecoveryState(t *testing.T) {
	t.Parallel()

	ch := newMockChannel()
	publisher, err := NewConfirmablePublisherFromChannel(ch)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := publisher.Close(); err != nil {
			t.Errorf("cleanup: publisher close: %v", err)
		}
	})

	require.True(t, publisher.prepareForRecovery())

	err = publisher.Publish(context.Background(), "exchange", "route", false, false, amqp.Publishing{Body: []byte("x")})
	require.ErrorIs(t, err, ErrPublisherClosed)
}

func TestConfirmablePublisher_ChannelAccessorAndChannelOrError(t *testing.T) {
	t.Parallel()

	ch := newMockChannel()
	publisher, err := NewConfirmablePublisherFromChannel(ch)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := publisher.Close(); err != nil {
			t.Errorf("cleanup: publisher close: %v", err)
		}
	})

	underlying := publisher.Channel()
	require.NotNil(t, underlying)

	readyChannel, err := publisher.ChannelOrError()
	require.NoError(t, err)
	require.Equal(t, underlying, readyChannel)

	require.NoError(t, publisher.Close())
	require.Nil(t, publisher.Channel())

	notReadyChannel, err := publisher.ChannelOrError()
	require.Nil(t, notReadyChannel)
	require.ErrorIs(t, err, ErrPublisherClosed)
}

func TestConfirmablePublisher_AutoRecovery(t *testing.T) {
	t.Parallel()

	ch1 := newMockChannel()
	ch2 := newMockChannel()

	recovered := make(chan struct{})
	publisher, err := NewConfirmablePublisherFromChannel(
		ch1,
		WithLogger(&libLog.NopLogger{}),
		WithAutoRecovery(func() (ConfirmableChannel, error) { return ch2, nil }),
		WithRecoveryBackoff(1*time.Millisecond, 5*time.Millisecond),
		WithMaxRecoveryAttempts(3),
		WithHealthCallback(func(state HealthState) {
			if state == HealthStateConnected {
				select {
				case <-recovered:
				default:
					close(recovered)
				}
			}
		}),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := publisher.Close(); err != nil {
			t.Errorf("cleanup: publisher close: %v", err)
		}
	})

	ch1.closeNotify <- amqp.ErrClosed

	select {
	case <-recovered:
	case <-time.After(2 * time.Second):
		t.Fatal("auto recovery did not complete")
	}

	go func() {
		ch2.waitForPublish(t)
		ch2.sendConfirm(true)
	}()

	err = publisher.Publish(context.Background(), "exchange", "route", false, false, amqp.Publishing{Body: []byte("ok")})
	require.NoError(t, err)
}

func TestConfirmablePublisher_PrepareForRecoveryWaitsForInFlightPublish(t *testing.T) {
	t.Parallel()

	ch := newMockChannel()
	publisher, err := NewConfirmablePublisherFromChannel(ch, WithConfirmTimeout(time.Second))
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := publisher.Close(); err != nil {
			t.Errorf("cleanup: publisher close: %v", err)
		}
	})

	publishDone := make(chan error, 1)
	go func() {
		publishDone <- publisher.Publish(
			context.Background(),
			"exchange",
			"route",
			false,
			false,
			amqp.Publishing{Body: []byte("ok")},
		)
	}()

	ch.waitForPublish(t)

	recoveryDone := make(chan bool, 1)
	go func() {
		recoveryDone <- publisher.prepareForRecovery()
	}()

	select {
	case <-recoveryDone:
		t.Fatal("prepareForRecovery must wait for in-flight publish")
	default:
	}

	ch.sendConfirm(true)

	select {
	case err = <-publishDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("publish did not complete")
	}

	select {
	case prepared := <-recoveryDone:
		require.True(t, prepared)
	case <-time.After(time.Second):
		t.Fatal("prepareForRecovery did not complete")
	}
}

func TestConfirmablePublisher_CloseWaitsForInFlightPublish(t *testing.T) {
	t.Parallel()

	ch := newMockChannel()
	publisher, err := NewConfirmablePublisherFromChannel(ch, WithConfirmTimeout(time.Second))
	require.NoError(t, err)

	publishDone := make(chan error, 1)
	go func() {
		publishDone <- publisher.Publish(
			context.Background(),
			"exchange",
			"route",
			false,
			false,
			amqp.Publishing{Body: []byte("ok")},
		)
	}()

	ch.waitForPublish(t)

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- publisher.Close()
	}()

	select {
	case err = <-closeDone:
		t.Fatalf("close returned early while publish in-flight: %v", err)
	default:
	}

	ch.sendConfirm(true)

	select {
	case err = <-publishDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("publish did not complete")
	}

	select {
	case err = <-closeDone:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("close did not complete")
	}

	ch.mu.Lock()
	closed := ch.closeCalled
	ch.mu.Unlock()
	require.True(t, closed)
}

func TestHealthState_String(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "connected", HealthStateConnected.String())
	assert.Equal(t, "reconnecting", HealthStateReconnecting.String())
	assert.Equal(t, "degraded", HealthStateDegraded.String())
	assert.Equal(t, "disconnected", HealthStateDisconnected.String())
	assert.Equal(t, "unknown", HealthState(99).String())
}

func TestConfirmablePublisher_HealthStateSnapshot(t *testing.T) {
	t.Parallel()

	ch := newMockChannel()
	publisher, err := NewConfirmablePublisherFromChannel(ch)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := publisher.Close(); err != nil {
			t.Errorf("cleanup: publisher close: %v", err)
		}
	})

	require.Equal(t, HealthStateConnected, publisher.HealthState())

	publisher.emitHealthState(HealthStateReconnecting)
	require.Equal(t, HealthStateReconnecting, publisher.HealthState())
}

func TestWithAutoRecoveryNilProvider(t *testing.T) {
	t.Parallel()

	ch := newMockChannel()
	publisher, err := NewConfirmablePublisherFromChannel(ch, WithAutoRecovery(nil))
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := publisher.Close(); err != nil {
			t.Errorf("cleanup: publisher close: %v", err)
		}
	})

	assert.Nil(t, publisher.recovery)
}

func TestConfirmablePublisher_PublishError(t *testing.T) {
	t.Parallel()

	ch := newMockChannel()
	publishErr := errors.New("publish failed")
	ch.publishErr = publishErr
	publisher, err := NewConfirmablePublisherFromChannel(ch)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := publisher.Close(); err != nil {
			t.Errorf("cleanup: publisher close: %v", err)
		}
	})

	err = publisher.Publish(context.Background(), "exchange", "route", false, false, amqp.Publishing{Body: []byte("x")})
	require.ErrorIs(t, err, publishErr)
}

func TestConfirmablePublisher_PublishOnClosedPublisher(t *testing.T) {
	t.Parallel()

	ch := newMockChannel()
	publisher, err := NewConfirmablePublisherFromChannel(ch)
	require.NoError(t, err)

	require.NoError(t, publisher.Close())
	err = publisher.Publish(context.Background(), "exchange", "route", false, false, amqp.Publishing{Body: []byte("x")})
	require.ErrorIs(t, err, ErrPublisherClosed)
}

func TestConfirmablePublisher_ReconnectWhileOpen(t *testing.T) {
	t.Parallel()

	ch := newMockChannel()
	publisher, err := NewConfirmablePublisherFromChannel(ch)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := publisher.Close(); err != nil {
			t.Errorf("cleanup: publisher close: %v", err)
		}
	})

	err = publisher.Reconnect(newMockChannel())
	require.ErrorIs(t, err, ErrReconnectWhileOpen)
}

func TestConfirmablePublisher_PublishContextCancelled(t *testing.T) {
	t.Parallel()

	ch := newMockChannel()
	publisher, err := NewConfirmablePublisherFromChannel(ch, WithConfirmTimeout(time.Second))
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := publisher.Close(); err != nil {
			t.Errorf("cleanup: publisher close: %v", err)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = publisher.Publish(ctx, "exchange", "route", false, false, amqp.Publishing{Body: []byte("x")})
	require.Error(t, err)
	require.Contains(t, err.Error(), "context cancelled")
}

func TestConfirmablePublisher_CloseDuringRecoveryClosesRecoveryDone(t *testing.T) {
	t.Parallel()

	ch := newMockChannel()
	publisher, err := NewConfirmablePublisherFromChannel(ch, WithAutoRecovery(func() (ConfirmableChannel, error) {
		return newMockChannel(), nil
	}))
	require.NoError(t, err)

	require.True(t, publisher.prepareForRecovery())
	recoveryDone := publisher.done

	require.NoError(t, publisher.Close())

	select {
	case <-recoveryDone:
	case <-time.After(time.Second):
		t.Fatal("recovery done channel was not closed by Close")
	}

	require.True(t, publisher.shutdown)
}

func TestConfirmablePublisher_AutoRecoveryExhausted(t *testing.T) {
	t.Parallel()

	ch := newMockChannel()
	disconnected := make(chan struct{})

	publisher, err := NewConfirmablePublisherFromChannel(
		ch,
		WithAutoRecovery(func() (ConfirmableChannel, error) {
			return nil, errors.New("provider failed")
		}),
		WithRecoveryBackoff(time.Millisecond, 2*time.Millisecond),
		WithMaxRecoveryAttempts(2),
		WithHealthCallback(func(state HealthState) {
			if state == HealthStateDisconnected {
				select {
				case <-disconnected:
				default:
					close(disconnected)
				}
			}
		}),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := publisher.Close(); err != nil {
			t.Errorf("cleanup: publisher close: %v", err)
		}
	})

	ch.closeNotify <- amqp.ErrClosed

	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("auto recovery did not report disconnection after exhaustion")
	}

	err = publisher.Publish(context.Background(), "exchange", "route", false, false, amqp.Publishing{Body: []byte("x")})
	require.ErrorIs(t, err, ErrPublisherClosed)
	require.ErrorIs(t, err, ErrRecoveryExhausted)
}

func TestConfirmablePublisher_ChannelCloseWithoutRecoveryTransitionsToDisconnected(t *testing.T) {
	t.Parallel()

	ch := newMockChannel()
	publisher, err := NewConfirmablePublisherFromChannel(ch)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := publisher.Close(); err != nil {
			t.Errorf("cleanup: publisher close: %v", err)
		}
	})

	ch.closeNotify <- amqp.ErrClosed

	require.Eventually(t, func() bool {
		return publisher.HealthState() == HealthStateDisconnected
	}, time.Second, time.Millisecond)

	err = publisher.Publish(context.Background(), "exchange", "route", false, false, amqp.Publishing{Body: []byte("x")})
	require.ErrorIs(t, err, ErrPublisherClosed)
}

func TestConfirmablePublisher_WithTypedNilLoggerDoesNotPanic(t *testing.T) {
	t.Parallel()

	var logger *panicPublisherLogger

	ch := newMockChannel()
	require.NotPanics(t, func() {
		publisher, err := NewConfirmablePublisherFromChannel(ch, WithLogger(logger))
		require.NoError(t, err)
		require.NoError(t, publisher.Close())
	})
}

func TestConfirmablePublisher_CloseZeroValueIsSafe(t *testing.T) {
	t.Parallel()

	pub := &ConfirmablePublisher{}
	require.NotPanics(t, func() {
		require.NoError(t, pub.Close())
	})

	require.NoError(t, pub.Close())
}

func TestConfirmablePublisher_NilReceiverGuards(t *testing.T) {
	t.Parallel()

	var publisher *ConfirmablePublisher

	err := publisher.Publish(context.Background(), "exchange", "route", false, false, amqp.Publishing{Body: []byte("x")})
	require.ErrorIs(t, err, ErrPublisherRequired)

	err = publisher.PublishAndWaitConfirm(context.Background(), "exchange", "route", false, false, amqp.Publishing{Body: []byte("x")})
	require.ErrorIs(t, err, ErrPublisherRequired)

	err = publisher.Close()
	require.ErrorIs(t, err, ErrPublisherRequired)

	err = publisher.Reconnect(newMockChannel())
	require.ErrorIs(t, err, ErrPublisherRequired)

	ch, err := publisher.ChannelOrError()
	require.Nil(t, ch)
	require.ErrorIs(t, err, ErrPublisherRequired)

	require.Nil(t, publisher.Channel())
	require.Equal(t, HealthStateDisconnected, publisher.HealthState())
}

// --- producer instrumentation ------------------------------------------------

const (
	// telemetryLibraryName is the instrumentation scope of the test telemetry.
	telemetryLibraryName = "lib-commons-rabbitmq-test"

	// produceDurationMetric is the metric messagingobs emits for a produce
	// operation. Asserted by name so the test fails loudly if the upstream
	// metrics contract ever drifts.
	produceDurationMetric = "messaging.client.operation.duration"

	// telemetryExchange and telemetryRoutingKey model the cardinality guardrail:
	// the exchange is bounded by the service topology and may become a label,
	// the routing key carries ids and must never become one.
	telemetryExchange   = "transactions"
	telemetryRoutingKey = "tenant-42.account.7f3a9c.created"
)

// telemetryForTest builds OpenTelemetry providers whose metrics and spans are
// collectable in-process, returning the option that binds them to a publisher.
//
// WARNING: it installs the global text-map propagator (header injection funnels
// through it, and OTel leaves it a no-op until a service installs one at
// bootstrap), so tests using it must NOT call t.Parallel().
func telemetryForTest(t *testing.T) (ConfirmablePublisherOption, *sdkmetric.ManualReader, *tracetest.InMemoryExporter) {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	spans := tracetest.NewInMemoryExporter()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(spans))

	previousPropagator := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})

	t.Cleanup(func() {
		otel.SetTextMapPropagator(previousPropagator)
		_ = meterProvider.Shutdown(context.Background())
		_ = tracerProvider.Shutdown(context.Background())
	})

	return WithTelemetryProviders(meterProvider, tracerProvider), reader, spans
}

// globalTelemetryForTest installs the collectable providers as the OTel globals,
// exactly as a service does at bootstrap with Telemetry.ApplyGlobals, and
// restores the previous globals afterwards. Tests using it must NOT call
// t.Parallel().
func globalTelemetryForTest(t *testing.T) (*sdkmetric.ManualReader, *tracetest.InMemoryExporter) {
	t.Helper()

	reader := sdkmetric.NewManualReader()
	meterProvider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	spans := tracetest.NewInMemoryExporter()
	tracerProvider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(spans))

	previousPropagator := otel.GetTextMapPropagator()
	previousMeterProvider := otel.GetMeterProvider()
	previousTracerProvider := otel.GetTracerProvider()

	otel.SetTextMapPropagator(propagation.TraceContext{})
	otel.SetMeterProvider(meterProvider)
	otel.SetTracerProvider(tracerProvider)

	t.Cleanup(func() {
		otel.SetTextMapPropagator(previousPropagator)
		otel.SetMeterProvider(previousMeterProvider)
		otel.SetTracerProvider(previousTracerProvider)
		_ = meterProvider.Shutdown(context.Background())
		_ = tracerProvider.Shutdown(context.Background())
	})

	return reader, spans
}

// produceDataPoints returns every data point currently collected for the
// producer duration histogram.
func produceDataPoints(t *testing.T, reader *sdkmetric.ManualReader) []metricdata.HistogramDataPoint[float64] {
	t.Helper()

	rm := &metricdata.ResourceMetrics{}
	require.NoError(t, reader.Collect(context.Background(), rm))

	var points []metricdata.HistogramDataPoint[float64]

	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != produceDurationMetric {
				continue
			}

			hist, ok := m.Data.(metricdata.Histogram[float64])
			require.True(t, ok, "expected float64 histogram for %s, got %T", m.Name, m.Data)

			points = append(points, hist.DataPoints...)
		}
	}

	return points
}

// requiredAttr returns a data point attribute, failing when it is absent.
func requiredAttr(t *testing.T, dp metricdata.HistogramDataPoint[float64], key string) string {
	t.Helper()

	value, found := dp.Attributes.Value(attribute.Key(key))
	require.True(t, found, "missing attribute %s", key)

	return value.AsString()
}

// headerKeyPresent reports whether the table carries key, case-insensitively:
// the W3C propagator writes through an http.Header carrier, which canonicalizes
// "traceparent" into "Traceparent".
func headerKeyPresent(headers amqp.Table, key string) bool {
	for name := range headers {
		if strings.EqualFold(name, key) {
			return true
		}
	}

	return false
}

// confirmNextPublish acks the next message the publisher hands to the broker.
func confirmNextPublish(t *testing.T, ch *mockConfirmableChannel) {
	t.Helper()

	go func() {
		ch.waitForPublish(t)
		ch.sendConfirm(true)
	}()
}

// TestWithTelemetryProviders_NilProvidersKeepTheGlobalDefault verifies the
// option never downgrades the publisher: passing nothing usable leaves the
// global-backed instrument that the constructor already installed.
func TestWithTelemetryProviders_NilProvidersKeepTheGlobalDefault(t *testing.T) {
	t.Parallel()

	publisher, err := NewConfirmablePublisherFromChannel(newMockChannel(), WithTelemetryProviders(nil, nil))
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := publisher.Close(); err != nil {
			t.Errorf("cleanup: publisher close: %v", err)
		}
	})

	assert.NotNil(t, publisher.producer)
}

// TestConfirmablePublisher_PublishWithoutTelemetryDoesNotTouchTheMessage pins
// the degradation contract: with no providers installed anywhere, the publisher
// is still instrumented but every OTel global is a no-op, so it publishes
// exactly what it did before the instrumentation existed.
//
// Not parallel: it asserts on the state of the global propagator.
func TestConfirmablePublisher_PublishWithoutTelemetryDoesNotTouchTheMessage(t *testing.T) {
	previousPropagator := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())

	t.Cleanup(func() { otel.SetTextMapPropagator(previousPropagator) })

	ch := newMockChannel()
	publisher, err := NewConfirmablePublisherFromChannel(ch)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := publisher.Close(); err != nil {
			t.Errorf("cleanup: publisher close: %v", err)
		}
	})

	require.NotNil(t, publisher.producer, "the publisher is always instrumented")

	callerHeaders := amqp.Table{"x-caller": "value"}

	confirmNextPublish(t, ch)

	err = publisher.Publish(context.Background(), telemetryExchange, telemetryRoutingKey, false, false,
		amqp.Publishing{Headers: callerHeaders, Body: []byte("ok")})
	require.NoError(t, err)

	assert.Equal(t, amqp.Table{"x-caller": "value"}, ch.publishedMessage().Headers,
		"an uninstrumented publish must not add or drop headers")
}

// TestConfirmablePublisher_PublishWithTelemetryMergesTraceHeaders verifies the
// merge is additive: the trace context reaches the broker without evicting what
// the caller set, and without mutating the caller's own map.
func TestConfirmablePublisher_PublishWithTelemetryMergesTraceHeaders(t *testing.T) {
	telemetryOption, _, _ := telemetryForTest(t)

	ch := newMockChannel()
	publisher, err := NewConfirmablePublisherFromChannel(ch, telemetryOption)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := publisher.Close(); err != nil {
			t.Errorf("cleanup: publisher close: %v", err)
		}
	})

	require.NotNil(t, publisher.producer)

	callerHeaders := amqp.Table{"x-caller": "value"}

	confirmNextPublish(t, ch)

	err = publisher.Publish(context.Background(), telemetryExchange, telemetryRoutingKey, false, false,
		amqp.Publishing{Headers: callerHeaders, Body: []byte("ok")})
	require.NoError(t, err)

	published := ch.publishedMessage()

	assert.Equal(t, "value", published.Headers["x-caller"], "caller headers must survive the merge")
	assert.True(t, headerKeyPresent(published.Headers, "traceparent"),
		"trace context must be injected, got %v", published.Headers)
	assert.Equal(t, amqp.Table{"x-caller": "value"}, callerHeaders,
		"the caller's own header map must not be mutated")
}

// TestConfirmablePublisher_PublishWithTelemetryEmitsProducerSpan verifies the
// span kind and name, and that the routing key never reaches a span attribute.
func TestConfirmablePublisher_PublishWithTelemetryEmitsProducerSpan(t *testing.T) {
	telemetryOption, _, spans := telemetryForTest(t)

	ch := newMockChannel()
	publisher, err := NewConfirmablePublisherFromChannel(ch, telemetryOption)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := publisher.Close(); err != nil {
			t.Errorf("cleanup: publisher close: %v", err)
		}
	})

	confirmNextPublish(t, ch)

	err = publisher.Publish(context.Background(), telemetryExchange, telemetryRoutingKey, false, false,
		amqp.Publishing{Body: []byte("ok")})
	require.NoError(t, err)

	recorded := spans.GetSpans()
	require.Len(t, recorded, 1, "Publish delegates to PublishAndWaitConfirm: exactly one span, never two")

	assert.Equal(t, trace.SpanKindProducer, recorded[0].SpanKind)
	assert.Equal(t, "publish "+telemetryExchange, recorded[0].Name)

	for _, attr := range recorded[0].Attributes {
		assert.NotContains(t, attr.Value.AsString(), telemetryRoutingKey,
			"the routing key must never reach a span attribute")
	}
}

// TestConfirmablePublisher_PublishWithTelemetryRecordsDuration verifies the
// success path records one observation with the bounded label set.
func TestConfirmablePublisher_PublishWithTelemetryRecordsDuration(t *testing.T) {
	telemetryOption, reader, _ := telemetryForTest(t)

	ch := newMockChannel()
	publisher, err := NewConfirmablePublisherFromChannel(ch, telemetryOption)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := publisher.Close(); err != nil {
			t.Errorf("cleanup: publisher close: %v", err)
		}
	})

	confirmNextPublish(t, ch)

	err = publisher.Publish(context.Background(), telemetryExchange, telemetryRoutingKey, false, false,
		amqp.Publishing{Body: []byte("ok")})
	require.NoError(t, err)

	points := produceDataPoints(t, reader)
	require.Len(t, points, 1, "one publish must produce exactly one observation")

	dp := points[0]

	assert.Equal(t, uint64(1), dp.Count)
	assert.Equal(t, "rabbitmq", requiredAttr(t, dp, "messaging.system"))
	assert.Equal(t, produceOperationName, requiredAttr(t, dp, "messaging.operation.name"))
	assert.Equal(t, telemetryExchange, requiredAttr(t, dp, "messaging.destination.template"),
		"the destination template must be the exchange, which is bounded")

	_, hasErrorType := dp.Attributes.Value(attribute.Key("error.type"))
	assert.False(t, hasErrorType, "a successful publish carries no error.type")

	for _, attr := range dp.Attributes.ToSlice() {
		assert.NotContains(t, attr.Value.AsString(), telemetryRoutingKey,
			"the routing key must never become a metric label")
	}
}

// TestConfirmablePublisher_PublishWithTelemetryRecordsBrokerFailure verifies
// finish also runs when the broker call itself fails.
func TestConfirmablePublisher_PublishWithTelemetryRecordsBrokerFailure(t *testing.T) {
	telemetryOption, reader, _ := telemetryForTest(t)

	ch := newMockChannel()
	ch.publishErr = errors.New("publish failed")

	publisher, err := NewConfirmablePublisherFromChannel(ch, telemetryOption)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := publisher.Close(); err != nil {
			t.Errorf("cleanup: publisher close: %v", err)
		}
	})

	err = publisher.Publish(context.Background(), telemetryExchange, telemetryRoutingKey, false, false,
		amqp.Publishing{Body: []byte("x")})
	require.Error(t, err)

	points := produceDataPoints(t, reader)
	require.Len(t, points, 1)

	assert.Equal(t, uint64(1), points[0].Count)

	errorType, found := points[0].Attributes.Value(attribute.Key("error.type"))
	require.True(t, found, "a failed publish must be recorded with error.type")
	assert.NotEmpty(t, errorType.AsString())
}

// TestConfirmablePublisher_PublishWithTelemetryRecordsEarlyReturn covers the
// return path that never reaches the broker at all. A publisher closed by a
// broker outage must still show up as errors on the series; recording only the
// paths that reach PublishWithContext would turn an outage into a silent hole.
func TestConfirmablePublisher_PublishWithTelemetryRecordsEarlyReturn(t *testing.T) {
	telemetryOption, reader, _ := telemetryForTest(t)

	ch := newMockChannel()
	publisher, err := NewConfirmablePublisherFromChannel(ch, telemetryOption)
	require.NoError(t, err)
	require.NoError(t, publisher.Close())

	err = publisher.Publish(context.Background(), telemetryExchange, telemetryRoutingKey, false, false,
		amqp.Publishing{Body: []byte("x")})
	require.ErrorIs(t, err, ErrPublisherClosed)

	assert.False(t, ch.publishCalled, "the message never reached the broker")

	points := produceDataPoints(t, reader)
	require.Len(t, points, 1)

	errorType, found := points[0].Attributes.Value(attribute.Key("error.type"))
	require.True(t, found, "an early-return failure must still be recorded")
	assert.NotEmpty(t, errorType.AsString())
}

// TestConfirmablePublisher_PublishWithNoOptionsUsesGlobalProviders is the
// contract this instrumentation exists for: a service that installed its
// providers at bootstrap gets the producer span, the trace headers and the
// duration metric by bumping lib-commons alone — the publisher is built with NO
// telemetry option at all.
//
// Not parallel: it installs the OTel globals.
func TestConfirmablePublisher_PublishWithNoOptionsUsesGlobalProviders(t *testing.T) {
	reader, spans := globalTelemetryForTest(t)

	ch := newMockChannel()

	publisher, err := NewConfirmablePublisherFromChannel(ch)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := publisher.Close(); err != nil {
			t.Errorf("cleanup: publisher close: %v", err)
		}
	})

	confirmNextPublish(t, ch)

	err = publisher.Publish(context.Background(), telemetryExchange, telemetryRoutingKey, false, false,
		amqp.Publishing{Body: []byte("ok")})
	require.NoError(t, err)

	assert.True(t, headerKeyPresent(ch.publishedMessage().Headers, "traceparent"),
		"trace context must be injected without any option")

	points := produceDataPoints(t, reader)
	require.Len(t, points, 1, "the globally installed MeterProvider must receive the duration")
	assert.Equal(t, telemetryExchange, requiredAttr(t, points[0], "messaging.destination.template"))

	recorded := spans.GetSpans()
	require.Len(t, recorded, 1, "the globally installed TracerProvider must receive the PRODUCER span")
	assert.Equal(t, producerLibraryName, recorded[0].InstrumentationScope.Name)
}

// TestConfirmablePublisher_ZeroValuePublishIsSafe covers a ConfirmablePublisher
// built as a zero value rather than through a constructor, so its producer is
// nil: instrumentation opens before the readiness check, and it must degrade
// instead of panicking on the way to ErrPublisherNotReady.
func TestConfirmablePublisher_ZeroValuePublishIsSafe(t *testing.T) {
	t.Parallel()

	publisher := &ConfirmablePublisher{}

	var err error

	require.NotPanics(t, func() {
		err = publisher.PublishAndWaitConfirm(context.Background(),
			telemetryExchange, telemetryRoutingKey, false, false, amqp.Publishing{Body: []byte("ok")})
	})

	assert.ErrorIs(t, err, ErrPublisherNotReady)
}

// TestMergeTraceHeaders_CaseInsensitiveCollision pins the propagation contract:
// a caller (or a hop that copied a Delivery's headers) carrying a lowercase
// "traceparent" must not survive alongside the canonicalized one the propagator
// injects, or the consumer could join the stale trace.
func TestMergeTraceHeaders_CaseInsensitiveCollision(t *testing.T) {
	t.Parallel()

	existing := amqp.Table{
		"traceparent": "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01",
		"x-caller":    "value",
	}

	merged := mergeTraceHeaders(existing, map[string]any{
		"Traceparent": "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
	})

	var found []string

	for name, value := range merged {
		if strings.EqualFold(name, "traceparent") {
			found = append(found, name)
			assert.Equal(t, "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", value,
				"the injected trace context must win")
		}
	}

	assert.Len(t, found, 1, "exactly one traceparent may reach the broker, got %v", found)
	assert.Equal(t, "value", merged["x-caller"], "unrelated caller headers survive")
	assert.Equal(t, "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01", existing["traceparent"],
		"the caller's own map must not be mutated")
}
