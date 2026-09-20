//go:build integration

package rabbitmq

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testReturnExchange   = "lib-commons.return.test"
	testReturnBoundKey   = "bound.key"
	testReturnUnboundKey = "nobody.listens"
)

// newReturnTestPublisher dials the broker, declares a topic exchange with a
// single queue bound to testReturnBoundKey, and returns a ConfirmablePublisher
// over a dedicated channel. testReturnUnboundKey deliberately has no binding.
func newReturnTestPublisher(t *testing.T, amqpURL string) (*ConfirmablePublisher, *amqp.Channel, string) {
	t.Helper()

	conn, err := amqp.Dial(amqpURL)
	require.NoError(t, err, "dial broker")

	channel, err := conn.Channel()
	require.NoError(t, err, "open channel")

	require.NoError(t, channel.ExchangeDeclare(testReturnExchange, "topic", true, false, false, false, nil))

	queue, err := channel.QueueDeclare("", true, false, true, false, nil)
	require.NoError(t, err, "declare queue")
	require.NoError(t, channel.QueueBind(queue.Name, testReturnBoundKey, testReturnExchange, false, nil))

	publisher, err := NewConfirmablePublisherFromChannel(channel)
	require.NoError(t, err, "construct confirmable publisher")

	t.Cleanup(func() {
		_ = publisher.Close()
		_ = conn.Close()
	})

	return publisher, channel, queue.Name
}

func testPublishing(body string) amqp.Publishing {
	return amqp.Publishing{
		ContentType:  "application/json",
		DeliveryMode: amqp.Persistent,
		Body:         []byte(body),
	}
}

// TestIntegration_ConfirmablePublisher_BoundKeyPublishes is the control: a
// routable message must still succeed and actually land on the queue.
func TestIntegration_ConfirmablePublisher_BoundKeyPublishes(t *testing.T) {
	amqpURL, _, cleanup := setupRabbitMQContainer(t)
	defer cleanup()

	publisher, channel, queueName := newReturnTestPublisher(t, amqpURL)

	err := publisher.PublishAndWaitConfirm(
		context.Background(), testReturnExchange, testReturnBoundKey, false, false, testPublishing(`{"ok":true}`))
	require.NoError(t, err, "publish to a bound routing key must succeed")

	delivery, ok, err := channel.Get(queueName, true)
	require.NoError(t, err, "read back the published message")
	require.True(t, ok, "a confirmed publish must actually be on the queue")
	assert.JSONEq(t, `{"ok":true}`, string(delivery.Body))
}

// TestIntegration_ConfirmablePublisher_UnboundKeyReturnsError is the defect.
// The broker ACKs an unroutable message, so confirms alone report success for
// a message that reached no queue. Only the return listener sees NO_ROUTE.
func TestIntegration_ConfirmablePublisher_UnboundKeyReturnsError(t *testing.T) {
	amqpURL, _, cleanup := setupRabbitMQContainer(t)
	defer cleanup()

	publisher, _, _ := newReturnTestPublisher(t, amqpURL)

	err := publisher.PublishAndWaitConfirm(
		context.Background(), testReturnExchange, testReturnUnboundKey, false, false, testPublishing(`{"lost":true}`))

	require.Error(t, err, "an unroutable message must surface as a publish error, never as success")
	require.ErrorIs(t, err, ErrPublishReturned)
	assert.Contains(t, err.Error(), "NO_ROUTE", "the error must name the broker's reason")
	assert.Contains(t, err.Error(), testReturnUnboundKey, "the error must name the routing key that reached nothing")
}

// TestIntegration_ConfirmablePublisher_UnroutableDoesNotPoisonNextPublish
// guards the correlation: a return must be attributed to the message that
// caused it and must not leak into the verdict of the next publish.
func TestIntegration_ConfirmablePublisher_UnroutableDoesNotPoisonNextPublish(t *testing.T) {
	amqpURL, _, cleanup := setupRabbitMQContainer(t)
	defer cleanup()

	publisher, _, _ := newReturnTestPublisher(t, amqpURL)

	require.ErrorIs(t, publisher.PublishAndWaitConfirm(
		context.Background(), testReturnExchange, testReturnUnboundKey, false, false, testPublishing(`{"lost":true}`)),
		ErrPublishReturned)

	for i := range 5 {
		require.NoError(t, publisher.PublishAndWaitConfirm(
			context.Background(), testReturnExchange, testReturnBoundKey, false, false,
			testPublishing(fmt.Sprintf(`{"seq":%d}`, i))),
			"a routable publish after an unroutable one must succeed")
	}
}

// TestIntegration_ConfirmablePublisher_ConcurrentIsolatesUnroutable proves the
// correlation under concurrency: with many goroutines publishing at once and
// exactly one of them using an unbound key, exactly one publish fails.
func TestIntegration_ConfirmablePublisher_ConcurrentIsolatesUnroutable(t *testing.T) {
	amqpURL, _, cleanup := setupRabbitMQContainer(t)
	defer cleanup()

	publisher, _, _ := newReturnTestPublisher(t, amqpURL)

	const routable = 24

	var (
		wg           sync.WaitGroup
		mu           sync.Mutex
		routableErrs []error
		unroutable   error
	)

	wg.Add(routable + 1)

	for i := range routable {
		go func(seq int) {
			defer wg.Done()

			err := publisher.PublishAndWaitConfirm(
				context.Background(), testReturnExchange, testReturnBoundKey, false, false,
				testPublishing(fmt.Sprintf(`{"seq":%d}`, seq)))

			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				routableErrs = append(routableErrs, err)
			}
		}(i)
	}

	go func() {
		defer wg.Done()

		err := publisher.PublishAndWaitConfirm(
			context.Background(), testReturnExchange, testReturnUnboundKey, false, false, testPublishing(`{"lost":true}`))

		mu.Lock()
		defer mu.Unlock()
		unroutable = err
	}()

	wg.Wait()

	assert.Empty(t, routableErrs, "routable publishes must not inherit another message's return")
	require.Error(t, unroutable, "the unroutable publish must fail")
	assert.ErrorIs(t, unroutable, ErrPublishReturned)
}

// TestIntegration_ConfirmablePublisher_BrokerDownErrors covers the severed
// broker: the publish must fail rather than report a delivery nobody received.
func TestIntegration_ConfirmablePublisher_BrokerDownErrors(t *testing.T) {
	amqpURL, _, cleanup := setupRabbitMQContainer(t)

	publisher, _, _ := newReturnTestPublisher(t, amqpURL)

	require.NoError(t, publisher.PublishAndWaitConfirm(
		context.Background(), testReturnExchange, testReturnBoundKey, false, false, testPublishing(`{"before":true}`)))

	cleanup()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var lastErr error

	require.Eventually(t, func() bool {
		lastErr = publisher.PublishAndWaitConfirm(
			ctx, testReturnExchange, testReturnBoundKey, false, false, testPublishing(`{"after":true}`))

		return lastErr != nil
	}, 20*time.Second, 250*time.Millisecond, "publishing to a dead broker must surface an error")

	assert.False(t, errors.Is(lastErr, ErrPublishReturned), "a dead broker is not an unroutable message")
}

// TestIntegration_ConfirmablePublisher_BatchLatencyReport measures the cost of
// the added round trip for the outbox dispatcher's batch pattern: N publishes
// drained in one pass. It reports fire-and-forget (the old lossy behaviour),
// confirm-only (the previous ConfirmablePublisher) and confirm+return (this
// change) against the same broker.
func TestIntegration_ConfirmablePublisher_BatchLatencyReport(t *testing.T) {
	amqpURL, _, cleanup := setupRabbitMQContainer(t)
	defer cleanup()

	const batch = 100

	publisher, _, _ := newReturnTestPublisher(t, amqpURL)

	rawConn, err := amqp.Dial(amqpURL)
	require.NoError(t, err)

	defer func() { _ = rawConn.Close() }()

	rawChannel, err := rawConn.Channel()
	require.NoError(t, err)

	// Fire-and-forget: no confirm mode, mandatory=false. This is the shape
	// that loses events silently.
	start := time.Now()

	for i := range batch {
		require.NoError(t, rawChannel.PublishWithContext(
			context.Background(), testReturnExchange, testReturnBoundKey, false, false,
			testPublishing(fmt.Sprintf(`{"seq":%d}`, i))))
	}

	fireAndForget := time.Since(start)

	// Confirm-only: what ConfirmablePublisher did before this change. Same
	// round trip, no mandatory flag and no return listener, so an unroutable
	// message is ACKed and reported as delivered.
	confirmOnlyChannel, err := rawConn.Channel()
	require.NoError(t, err)
	require.NoError(t, confirmOnlyChannel.Confirm(false))

	confirmOnly := confirmOnlyChannel.NotifyPublish(make(chan amqp.Confirmation, batch))

	start = time.Now()

	for i := range batch {
		require.NoError(t, confirmOnlyChannel.PublishWithContext(
			context.Background(), testReturnExchange, testReturnBoundKey, false, false,
			testPublishing(fmt.Sprintf(`{"seq":%d}`, i))))
		require.True(t, (<-confirmOnly).Ack)
	}

	confirmOnlyElapsed := time.Since(start)

	// Confirm + return: the behaviour this change ships.
	start = time.Now()

	for i := range batch {
		require.NoError(t, publisher.PublishAndWaitConfirm(
			context.Background(), testReturnExchange, testReturnBoundKey, false, false,
			testPublishing(fmt.Sprintf(`{"seq":%d}`, i))))
	}

	confirmed := time.Since(start)

	perMsg := func(d time.Duration) float64 {
		return float64(d.Microseconds()) / float64(batch) / 1000
	}

	t.Logf("batch=%d | fire-and-forget %v (%.3f ms/msg) | confirm-only %v (%.3f ms/msg) | confirm+return %v (%.3f ms/msg) | cost of THIS change %v",
		batch,
		fireAndForget, perMsg(fireAndForget),
		confirmOnlyElapsed, perMsg(confirmOnlyElapsed),
		confirmed, perMsg(confirmed),
		confirmed-confirmOnlyElapsed)
}
