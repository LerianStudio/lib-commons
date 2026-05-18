//go:build integration

package rabbitmq

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/LerianStudio/lib-observability/log"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcrabbit "github.com/testcontainers/testcontainers-go/modules/rabbitmq"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	testRabbitMQImage   = "rabbitmq:3-management-alpine"
	testRabbitMQUser    = "guest"
	testRabbitMQPass    = "guest"
	testStartupTimeout  = 60 * time.Second
	testConsumeDeadline = 10 * time.Second
)

// setupRabbitMQContainer starts a RabbitMQ testcontainer with the management
// plugin and returns the AMQP URL, the management HTTP URL, and a cleanup
// function.
//
// On Docker Desktop the broker can report startup complete before Docker has
// published the mapped host ports. The additional port waits make endpoint
// discovery deterministic before the AMQP and management URLs are resolved.
func setupRabbitMQContainer(t *testing.T) (amqpURL string, mgmtURL string, cleanup func()) {
	t.Helper()

	ctx := context.Background()

	container, err := tcrabbit.Run(ctx,
		testRabbitMQImage,
		testcontainers.WithAdditionalWaitStrategy(
			wait.ForListeningPort(tcrabbit.DefaultAMQPPort),
			wait.ForListeningPort(tcrabbit.DefaultHTTPPort),
		),
	)
	require.NoError(t, err, "failed to start RabbitMQ container")
	var terminateOnce sync.Once
	var terminateErr error
	terminate := func() error {
		terminateOnce.Do(func() {
			termCtx, termCancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer termCancel()

			terminateErr = container.Terminate(termCtx)
		})

		return terminateErr
	}
	t.Cleanup(func() {
		require.NoError(t, terminate(), "failed to terminate RabbitMQ container")
	})

	var amqpEndpoint string
	var httpEndpoint string

	require.Eventually(t, func() bool {
		var amqpErr error
		amqpEndpoint, amqpErr = container.AmqpURL(ctx)
		if amqpErr != nil || amqpEndpoint == "" {
			return false
		}

		var httpErr error
		httpEndpoint, httpErr = container.HttpURL(ctx)
		return httpErr == nil && httpEndpoint != ""
	}, testStartupTimeout, 100*time.Millisecond, "failed to resolve RabbitMQ container endpoints")

	return amqpEndpoint, httpEndpoint, func() {
		require.NoError(t, terminate(), "failed to terminate RabbitMQ container")
	}
}

// newTestConnection creates a RabbitMQConnection configured for integration testing.
func newTestConnection(amqpURL, mgmtURL string) *RabbitMQConnection {
	return &RabbitMQConnection{
		ConnectionStringSource:   amqpURL,
		HealthCheckURL:           mgmtURL,
		User:                     testRabbitMQUser,
		Pass:                     testRabbitMQPass,
		AllowInsecureHealthCheck: true,
		Logger:                   log.NewNop(),
	}
}

func TestIntegration_RabbitMQ_ConnectAndClose(t *testing.T) {
	amqpURL, mgmtURL, cleanup := setupRabbitMQContainer(t)
	defer cleanup()

	ctx := context.Background()
	rc := newTestConnection(amqpURL, mgmtURL)

	// Connect to the real RabbitMQ instance.
	err := rc.ConnectContext(ctx)
	require.NoError(t, err, "ConnectContext should succeed against a live broker")

	assert.True(t, rc.Connected, "Connected flag should be true after successful connection")
	assert.NotNil(t, rc.Connection, "Connection should be non-nil after connect")
	assert.NotNil(t, rc.Channel, "Channel should be non-nil after connect")

	// Close the connection and verify state is reset.
	err = rc.CloseContext(ctx)
	require.NoError(t, err, "CloseContext should succeed")

	assert.False(t, rc.Connected, "Connected flag should be false after close")
	assert.Nil(t, rc.Connection, "Connection should be nil after close")
	assert.Nil(t, rc.Channel, "Channel should be nil after close")
}

func TestIntegration_RabbitMQ_HealthCheck(t *testing.T) {
	amqpURL, mgmtURL, cleanup := setupRabbitMQContainer(t)
	defer cleanup()

	ctx := context.Background()
	rc := newTestConnection(amqpURL, mgmtURL)

	// Connect first — health check needs a configured connection object.
	err := rc.ConnectContext(ctx)
	require.NoError(t, err, "ConnectContext should succeed")

	defer func() {
		_ = rc.CloseContext(ctx)
	}()

	// Run health check against the management API.
	healthy, err := rc.HealthCheckContext(ctx)
	require.NoError(t, err, "HealthCheckContext should not return an error for a healthy broker")
	assert.True(t, healthy, "HealthCheckContext should report true for a running broker")
}

func TestIntegration_RabbitMQ_EnsureChannel(t *testing.T) {
	amqpURL, mgmtURL, cleanup := setupRabbitMQContainer(t)
	defer cleanup()

	ctx := context.Background()
	rc := newTestConnection(amqpURL, mgmtURL)

	err := rc.ConnectContext(ctx)
	require.NoError(t, err, "ConnectContext should succeed")

	defer func() {
		_ = rc.CloseContext(ctx)
	}()

	// Close the channel explicitly to simulate a lost channel.
	require.NotNil(t, rc.Channel, "Channel should exist after connect")

	err = rc.Channel.Close()
	require.NoError(t, err, "explicit channel close should succeed")

	// EnsureChannelContext should detect the closed channel and recover it.
	err = rc.EnsureChannelContext(ctx)
	require.NoError(t, err, "EnsureChannelContext should recover a closed channel")

	assert.True(t, rc.Connected, "Connected flag should be true after channel recovery")
	assert.NotNil(t, rc.Channel, "Channel should be non-nil after recovery")
	assert.False(t, rc.Channel.IsClosed(), "Recovered channel should not be closed")
}

func TestIntegration_RabbitMQ_GetNewConnect(t *testing.T) {
	amqpURL, mgmtURL, cleanup := setupRabbitMQContainer(t)
	defer cleanup()

	ctx := context.Background()
	rc := newTestConnection(amqpURL, mgmtURL)

	err := rc.ConnectContext(ctx)
	require.NoError(t, err, "ConnectContext should succeed")

	defer func() {
		_ = rc.CloseContext(ctx)
	}()

	// GetNewConnectContext returns the active channel.
	ch, err := rc.GetNewConnectContext(ctx)
	require.NoError(t, err, "GetNewConnectContext should succeed on a connected instance")
	assert.NotNil(t, ch, "returned channel should not be nil")
	assert.False(t, ch.IsClosed(), "returned channel should be open")
}

func TestIntegration_RabbitMQ_PublishAndConsume(t *testing.T) {
	amqpURL, mgmtURL, cleanup := setupRabbitMQContainer(t)
	defer cleanup()

	ctx := context.Background()
	rc := newTestConnection(amqpURL, mgmtURL)

	err := rc.ConnectContext(ctx)
	require.NoError(t, err, "ConnectContext should succeed")

	defer func() {
		_ = rc.CloseContext(ctx)
	}()

	ch, err := rc.GetNewConnectContext(ctx)
	require.NoError(t, err, "GetNewConnectContext should succeed")

	// Declare a test queue.
	queueName := fmt.Sprintf("integration-test-queue-%d", time.Now().UnixNano())

	q, err := ch.QueueDeclare(
		queueName,
		false, // durable
		true,  // autoDelete
		false, // exclusive
		false, // noWait
		nil,   // args
	)
	require.NoError(t, err, "QueueDeclare should succeed")

	// Publish a message.
	messageBody := []byte("hello from integration test")

	publishCtx, publishCancel := context.WithTimeout(ctx, 5*time.Second)
	defer publishCancel()

	err = ch.PublishWithContext(
		publishCtx,
		"",     // exchange (default)
		q.Name, // routing key = queue name
		false,  // mandatory
		false,  // immediate
		amqp.Publishing{
			ContentType: "text/plain",
			Body:        messageBody,
		},
	)
	require.NoError(t, err, "PublishWithContext should succeed")

	// Consume the message.
	msgs, err := ch.Consume(
		q.Name, // queue
		"",     // consumer tag (auto-generated)
		true,   // autoAck
		false,  // exclusive
		false,  // noLocal
		false,  // noWait
		nil,    // args
	)
	require.NoError(t, err, "Consume should succeed")

	// Wait for the message with a deadline to avoid hanging forever.
	consumeCtx, consumeCancel := context.WithTimeout(ctx, testConsumeDeadline)
	defer consumeCancel()

	select {
	case msg, ok := <-msgs:
		require.True(t, ok, "message channel should deliver a message")
		assert.Equal(t, messageBody, msg.Body, "consumed message body should match published body")
		assert.Equal(t, "text/plain", msg.ContentType, "content type should match")
	case <-consumeCtx.Done():
		t.Fatal("timed out waiting for message from RabbitMQ")
	}
}
