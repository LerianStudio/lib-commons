//go:build unit

package rabbitmq

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	libLog "github.com/LerianStudio/lib-commons/v4/commons/log"
	amqp "github.com/rabbitmq/amqp091-go"
)

type mockConfirmableChannel struct {
	mu              sync.Mutex
	confirmErr      error
	publishErr      error
	confirms        chan amqp.Confirmation
	closeNotify     chan *amqp.Error
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
	_ amqp.Publishing,
) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.publishCalled = true
	m.deliveryCounter++

	return m.publishErr
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
