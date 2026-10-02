//go:build integration

// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package consumer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcrabbit "github.com/testcontainers/testcontainers-go/modules/rabbitmq"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/LerianStudio/lib-commons/v7/commons/rabbitmq"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/internal/logcompat"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/internal/testutil"
	tmrabbitmq "github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/rabbitmq"
)

const (
	integrationRabbitMQImage = "rabbitmq:3.13-management-alpine"
	integrationTenant        = "tenant-a"
	integrationDeadline      = 15 * time.Second
	integrationPoll          = 20 * time.Millisecond
)

// startBroker runs a RabbitMQ container and returns a connection to it.
func startBroker(t *testing.T) *amqp.Connection {
	t.Helper()

	ctx := context.Background()

	container, err := tcrabbit.Run(ctx, integrationRabbitMQImage,
		testcontainers.WithAdditionalWaitStrategy(wait.ForListeningPort(tcrabbit.DefaultAMQPPort)))
	require.NoError(t, err, "start RabbitMQ container")

	t.Cleanup(func() {
		termCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		assert.NoError(t, container.Terminate(termCtx))
	})

	var conn *amqp.Connection

	require.Eventually(t, func() bool {
		url, urlErr := container.AmqpURL(ctx)
		if urlErr != nil || url == "" {
			return false
		}

		conn, err = amqp.Dial(url)

		return err == nil
	}, 60*time.Second, 200*time.Millisecond, "dial RabbitMQ")

	t.Cleanup(func() { _ = conn.Close() })

	return conn
}

// brokerConsumer is a consumer whose channels come from conn, opened the way
// the RabbitMQ manager opens them: one consume channel and one confirm-mode
// publisher channel per connect.
func brokerConsumer(conn *amqp.Connection) *MultiTenantConsumer {
	c := &MultiTenantConsumer{
		rabbitmq: tmrabbitmq.NewManager(nil, "integration"),
		config:   MultiTenantConfig{PrefetchCount: 1},
		logger:   logcompat.New(testutil.NewMockLogger()),
	}

	c.openConsumeChannelFn = func(context.Context, string) (consumeChannel, error) {
		return conn.Channel()
	}

	c.openPublisherFn = func(context.Context, string) (dispositionPublisher, error) {
		ch, err := conn.Channel()
		if err != nil {
			return nil, err
		}

		publisher, err := rabbitmq.NewConfirmablePublisherFromChannel(ch, rabbitmq.WithConfirmTimeout(5*time.Second))
		if err != nil {
			_ = ch.Close()
			return nil, err
		}

		return publisher, nil
	}

	return c
}

// runQueue consumes queue until the returned stop function is called.
func runQueue(t *testing.T, c *MultiTenantConsumer, queue string, handler HandlerFunc, policy QueuePolicy) (stop func()) {
	t.Helper()

	normalized, err := normalizeQueuePolicy(policy)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		defer close(done)
		c.consumeTenantQueue(ctx, integrationTenant, queue, handler, &normalized, logcompat.New(testutil.NewMockLogger()))
	}()

	var once sync.Once

	stop = func() {
		once.Do(func() {
			cancel()

			select {
			case <-done:
			case <-time.After(integrationDeadline):
				t.Error("consumer did not stop")
			}
		})
	}

	t.Cleanup(stop)

	return stop
}

// fastRetry keeps the disposition delays in milliseconds.
func fastRetry(maxAttempts int) RetryPolicy {
	return RetryPolicy{MaxAttempts: maxAttempts, InitialDelay: 10 * time.Millisecond, MaxDelay: 50 * time.Millisecond}
}

// declareQueue is a TopologyFunc that declares queue with args and binds it to
// the origin exchange under the origin key.
func declareQueue(args amqp.Table, before func(rabbitmq.AMQPChannel) error) TopologyFunc {
	return func(_ context.Context, _, queueName string, ch rabbitmq.AMQPChannel) error {
		if before != nil {
			if err := before(ch); err != nil {
				return err
			}
		}

		if err := ch.ExchangeDeclare(originExchange(queueName), "topic", false, true, false, false, nil); err != nil {
			return err
		}

		if _, err := ch.QueueDeclare(queueName, false, false, false, false, args); err != nil {
			return err
		}

		return ch.QueueBind(queueName, originKey, originExchange(queueName), false, nil)
	}
}

const originKey = "orders.created"

func originExchange(queue string) string { return queue + ".in" }

// publishOrigin publishes one message through the queue's origin exchange once
// the topology hook has declared it.
func publishOrigin(t *testing.T, conn *amqp.Connection, queue, body string) {
	t.Helper()

	require.Eventually(t, func() bool {
		ch, err := conn.Channel()
		if err != nil {
			return false
		}
		defer ch.Close()

		_, err = ch.QueueDeclarePassive(queue, false, false, false, false, nil)

		return err == nil
	}, integrationDeadline, integrationPoll, "the topology hook declares the queue before consuming")

	ch, err := conn.Channel()
	require.NoError(t, err)

	defer ch.Close()

	require.NoError(t, ch.PublishWithContext(context.Background(), originExchange(queue), originKey, true, false,
		amqp.Publishing{ContentType: "application/json", Body: []byte(body), MessageId: body}))
}

// queueDepth reports the ready messages on queue.
func queueDepth(t *testing.T, conn *amqp.Connection, queue string) int {
	t.Helper()

	ch, err := conn.Channel()
	require.NoError(t, err)

	defer ch.Close()

	q, err := ch.QueueDeclarePassive(queue, false, false, false, false, nil)
	require.NoError(t, err)

	return q.Messages
}

// getOne reads one message from queue, waiting for it to arrive.
func getOne(t *testing.T, conn *amqp.Connection, queue string) amqp.Delivery {
	t.Helper()

	ch, err := conn.Channel()
	require.NoError(t, err)

	defer ch.Close()

	var delivery amqp.Delivery

	require.Eventually(t, func() bool {
		d, ok, getErr := ch.Get(queue, true)
		if getErr != nil || !ok {
			return false
		}

		delivery = d

		return true
	}, integrationDeadline, integrationPoll, "a message on %s", queue)

	return delivery
}

type seenDelivery struct {
	attempt    int
	exchange   string
	routingKey string
	redeliver  bool
}

type deliveryLog struct {
	mu   sync.Mutex
	seen []seenDelivery
}

func (l *deliveryLog) record(d amqp.Delivery) int {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.seen = append(l.seen, seenDelivery{attempt: DeliveryAttempt(d), exchange: d.Exchange, routingKey: d.RoutingKey, redeliver: d.Redelivered})

	return len(l.seen)
}

func (l *deliveryLog) snapshot() []seenDelivery {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]seenDelivery(nil), l.seen...)
}

func TestIntegration_Disposition(t *testing.T) {
	conn := startBroker(t)

	t.Run("topology declares the queue and a retry is delivered again", func(t *testing.T) {
		const queue = "it.retry"

		c := brokerConsumer(conn)
		deliveries := &deliveryLog{}

		runQueue(t, c, queue, func(_ context.Context, d amqp.Delivery) error {
			if deliveries.record(d) == 1 {
				return RetryLater(errors.New("database unavailable"))
			}

			return nil
		}, QueuePolicy{
			Retry:      fastRetry(3),
			DeadLetter: DeadLetterRoute{RoutingKey: "unused"},
			Topology:   declareQueue(nil, nil),
		})

		publishOrigin(t, conn, queue, "retry-me")

		require.Eventually(t, func() bool { return len(deliveries.snapshot()) == 2 }, integrationDeadline, integrationPoll)

		seen := deliveries.snapshot()
		assert.Equal(t, 0, seen[0].attempt)
		assert.Equal(t, 1, seen[1].attempt, "the retry hop carries x-lc-retry-attempt=1")
		assert.Equal(t, originExchange(queue), seen[1].exchange, "the handler sees the origin exchange after the hop")
		assert.Equal(t, originKey, seen[1].routingKey, "the handler sees the origin routing key after the hop")

		require.Eventually(t, func() bool { return queueDepth(t, conn, queue) == 0 }, integrationDeadline, integrationPoll)
	})

	t.Run("an unroutable dead-letter publish leaves the message on the queue", func(t *testing.T) {
		const queue = "it.unroutable"

		c := brokerConsumer(conn)
		deliveries := &deliveryLog{}

		stop := runQueue(t, c, queue, func(_ context.Context, d amqp.Delivery) error {
			deliveries.record(d)
			return DeadLetter(errors.New("forged signature"))
		}, QueuePolicy{
			Retry:      fastRetry(3),
			DeadLetter: DeadLetterRoute{Exchange: queue + ".dlx", RoutingKey: "dead"},
			// The dead-letter exchange exists but nothing is bound to it.
			Topology: declareQueue(nil, func(ch rabbitmq.AMQPChannel) error {
				return ch.ExchangeDeclare(queue+".dlx", "direct", false, true, false, false, nil)
			}),
		})

		publishOrigin(t, conn, queue, "poison")

		require.Eventually(t, func() bool { return len(deliveries.snapshot()) >= 2 }, integrationDeadline, integrationPoll)
		stop()

		seen := deliveries.snapshot()
		assert.True(t, seen[1].redeliver, "the broker returned the unroutable dead-letter and the message was requeued")
		assert.Equal(t, 1, queueDepth(t, conn, queue), "the message is still on its queue, not acked away")
	})

	t.Run("a dead-letter exchange missing from the vhost backs off and keeps the message", func(t *testing.T) {
		const queue = "it.missing-dlx"

		c := brokerConsumer(conn)
		rec := &struct {
			mu       sync.Mutex
			delays   []time.Duration
			degraded []bool
		}{}

		c.reconnectWaitFn = func(ctx context.Context, d time.Duration) error {
			rec.mu.Lock()
			rec.delays = append(rec.delays, d)
			rec.degraded = append(rec.degraded, c.IsDegraded(integrationTenant))
			rec.mu.Unlock()

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(10 * time.Millisecond):
				return nil
			}
		}

		stop := runQueue(t, c, queue, func(context.Context, amqp.Delivery) error {
			return DeadLetter(errors.New("forged signature"))
		}, QueuePolicy{
			Retry:      fastRetry(3),
			DeadLetter: DeadLetterRoute{Exchange: queue + ".never-declared", RoutingKey: "dead"},
			Topology:   declareQueue(nil, nil),
		})

		publishOrigin(t, conn, queue, "poison")

		require.Eventually(t, func() bool {
			rec.mu.Lock()
			defer rec.mu.Unlock()

			return len(rec.delays) >= 4
		}, integrationDeadline, integrationPoll, "the broker's 404 closes the publish channel and the consumer backs off")
		stop()

		rec.mu.Lock()
		delays := append([]time.Duration(nil), rec.delays[:4]...)
		degraded := append([]bool(nil), rec.degraded[:4]...)
		rec.mu.Unlock()

		for i := 1; i < len(delays); i++ {
			assert.Greater(t, delays[i], delays[i-1], "the backoff grows across reconnects: %v", delays)
		}

		assert.Equal(t, []bool{false, false, true, true}, degraded, "the tenant is marked degraded")
		assert.True(t, c.IsDegraded(integrationTenant))
		assert.Equal(t, 1, queueDepth(t, conn, queue), "the message is still on its queue")
	})

	t.Run("ViaQueueDLX dead-letters under the queue name after the retry hop", func(t *testing.T) {
		const (
			queue = "it.via-dlx"
			dlx   = queue + ".dlx"
			dlq   = queue + ".dlq"
		)

		c := brokerConsumer(conn)
		deliveries := &deliveryLog{}

		runQueue(t, c, queue, func(_ context.Context, d amqp.Delivery) error {
			deliveries.record(d)
			return errors.New("database unavailable")
		}, QueuePolicy{
			Retry:      fastRetry(2),
			DeadLetter: DeadLetterRoute{ViaQueueDLX: true},
			Topology: declareQueue(rabbitmq.GetDLXArgs(dlx), func(ch rabbitmq.AMQPChannel) error {
				return rabbitmq.DeclareDLQTopology(ch, rabbitmq.WithDLXExchangeName(dlx), rabbitmq.WithDLQName(dlq))
			}),
		})

		publishOrigin(t, conn, queue, "exhaust-me")

		dead := getOne(t, conn, dlq)

		assert.Equal(t, "exhaust-me", string(dead.Body))
		assert.Equal(t, dlx, dead.Exchange)
		assert.Equal(t, queue, dead.RoutingKey,
			"after the retry hop the broker dead-letters under the queue name, not the origin key")
		assert.Equal(t, 1, DeliveryAttempt(dead), "the broker forwards x-lc-retry-attempt unchanged")
		assert.Len(t, deliveries.snapshot(), 2)
		require.Eventually(t, func() bool { return queueDepth(t, conn, queue) == 0 }, integrationDeadline, integrationPoll)
	})

	t.Run("ViaQueueDLX with a narrow binding needs x-dead-letter-routing-key", func(t *testing.T) {
		const (
			queue      = "it.via-dlx-fixed-key"
			dlx        = queue + ".dlx"
			dlq        = queue + ".dlq"
			bindingKey = "dead"
		)

		args := rabbitmq.GetDLXArgs(dlx)
		args["x-dead-letter-routing-key"] = bindingKey

		c := brokerConsumer(conn)

		runQueue(t, c, queue, func(context.Context, amqp.Delivery) error {
			return errors.New("database unavailable")
		}, QueuePolicy{
			Retry:      fastRetry(2),
			DeadLetter: DeadLetterRoute{ViaQueueDLX: true},
			Topology: declareQueue(args, func(ch rabbitmq.AMQPChannel) error {
				return rabbitmq.DeclareDLQTopology(ch, rabbitmq.WithDLXExchangeName(dlx), rabbitmq.WithDLQName(dlq),
					rabbitmq.WithDLQBindingKey(bindingKey))
			}),
		})

		publishOrigin(t, conn, queue, "exhaust-me")

		dead := getOne(t, conn, dlq)
		assert.Equal(t, bindingKey, dead.RoutingKey)
	})

	t.Run("an explicit dead-letter lands without the retry counter", func(t *testing.T) {
		const (
			queue = "it.explicit"
			dlq   = queue + ".dlq"
		)

		c := brokerConsumer(conn)

		runQueue(t, c, queue, func(context.Context, amqp.Delivery) error {
			return errors.New("database unavailable")
		}, QueuePolicy{
			Retry:      fastRetry(2),
			DeadLetter: DeadLetterRoute{RoutingKey: dlq},
			Topology: declareQueue(nil, func(ch rabbitmq.AMQPChannel) error {
				_, err := ch.QueueDeclare(dlq, false, false, false, false, nil)
				return err
			}),
		})

		publishOrigin(t, conn, queue, "exhaust-me")

		dead := getOne(t, conn, dlq)
		assert.Equal(t, DeadLetterClassRetryExhausted, dead.Headers[HeaderDeadLetterClass])
		assert.Equal(t, int32(2), dead.Headers[HeaderDeadLetterAttempts])
		assert.NotContains(t, dead.Headers, HeaderRetryAttempt)
		require.Eventually(t, func() bool { return queueDepth(t, conn, queue) == 0 }, integrationDeadline, integrationPoll)
	})
}
