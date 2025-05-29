// Package rabbitmq provides RabbitMQ connection and messaging functionality.
// It includes connection management, health checks, and message publishing/consuming.
package rabbitmq

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/LerianStudio/lib-commons/commons/log"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"
)

// RabbitMQConnection is a hub which deal with rabbitmq connections.
// The type name intentionally matches the package name for clarity in external usage.
//
//nolint:revive // Intentional stuttering for external package clarity
type RabbitMQConnection struct {
	ConnectionStringSource string
	Queue                  string
	Host                   string
	Port                   string
	User                   string
	Pass                   string
	Channel                *amqp.Channel
	Connection             *amqp.Connection
	Logger                 log.Logger
	Connected              bool

	// Connection recovery
	mu                   sync.RWMutex
	reconnectInterval    time.Duration
	maxReconnectAttempts int
	publisherConfirms    bool
	confirmChannel       chan amqp.Confirmation

	// Health monitoring
	lastHealthCheck     time.Time
	healthCheckInterval time.Duration
}

// Connect keeps a singleton connection with rabbitmq.
func (rc *RabbitMQConnection) Connect() error {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	// Set default values if not configured
	if rc.reconnectInterval == 0 {
		rc.reconnectInterval = 5 * time.Second
	}

	if rc.maxReconnectAttempts == 0 {
		rc.maxReconnectAttempts = 5
	}

	if rc.healthCheckInterval == 0 {
		rc.healthCheckInterval = 30 * time.Second
	}

	rc.Logger.Info("Connecting to rabbitmq...")

	conn, err := amqp.Dial(rc.ConnectionStringSource)
	if err != nil {
		rc.Logger.Error("failed to connect to rabbitmq", zap.Error(err))
		return fmt.Errorf("failed to connect to rabbitmq: %w", err)
	}

	ch, err := conn.Channel()
	if err != nil {
		_ = conn.Close() // Clean up connection if channel creation fails, ignore close error

		rc.Logger.Error("failed to open channel on rabbitmq", zap.Error(err))

		return fmt.Errorf("failed to open rabbitmq channel: %w", err)
	}

	// Enable publisher confirms if requested
	if rc.publisherConfirms {
		if err := ch.Confirm(false); err != nil {
			_ = ch.Close() // Ignore close errors during cleanup
			_ = conn.Close()

			rc.Logger.Error("failed to enable publisher confirms", zap.Error(err))

			return fmt.Errorf("failed to enable publisher confirms: %w", err)
		}

		// Set up confirmation channel
		rc.confirmChannel = ch.NotifyPublish(make(chan amqp.Confirmation, 100))
	}

	// Test connection with AMQP-level health check instead of HTTP
	if err := rc.testAMQPConnection(ch); err != nil {
		_ = ch.Close() // Ignore close errors during cleanup
		_ = conn.Close()

		rc.Logger.Error("AMQP connection health check failed", zap.Error(err))

		return fmt.Errorf("rabbitmq health check failed: %w", err)
	}

	rc.Logger.Info("Connected to rabbitmq ✅")

	rc.Connected = true
	rc.Channel = ch
	rc.Connection = conn
	rc.lastHealthCheck = time.Now()

	return nil
}

// testAMQPConnection performs AMQP-level health check instead of HTTP API
func (rc *RabbitMQConnection) testAMQPConnection(ch *amqp.Channel) error {
	// Try to declare a temporary queue to test the connection
	tempQueue := fmt.Sprintf("health-check-%d", time.Now().UnixNano())

	_, err := ch.QueueDeclare(
		tempQueue, // name
		false,     // durable
		true,      // delete when unused
		true,      // exclusive
		false,     // no-wait
		nil,       // arguments
	)
	if err != nil {
		return fmt.Errorf("failed to declare test queue: %w", err)
	}

	// Clean up the test queue
	_, err = ch.QueueDelete(tempQueue, false, false, false)
	if err != nil {
		rc.Logger.Warn("failed to delete test queue", zap.Error(err))
		// Don't fail the health check for cleanup failure
	}

	return nil
}

// GetNewConnect returns a pointer to the rabbitmq connection, initializing it if necessary.
func (rc *RabbitMQConnection) GetNewConnect() (*amqp.Channel, error) {
	if !rc.Connected || rc.Channel == nil || rc.Connection == nil {
		if err := rc.Connect(); err != nil {
			rc.Logger.Error("failed to establish rabbitmq connection", zap.Error(err))
			return nil, fmt.Errorf("rabbitmq connection failed: %w", err)
		}
	}

	// Check if connection is still alive
	if rc.Connection.IsClosed() {
		rc.Logger.Info("detected closed connection, attempting to reconnect...")

		rc.Connected = false
		if err := rc.Connect(); err != nil {
			return nil, fmt.Errorf("rabbitmq reconnection failed: %w", err)
		}
	}

	return rc.Channel, nil
}

// HealthCheck rabbitmq when the server is started
func (rc *RabbitMQConnection) HealthCheck() bool {
	url := fmt.Sprintf("http://%s:%s/api/health/checks/alarms", rc.Host, rc.Port)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		rc.Logger.Errorf("failed to make GET request before client do: %v", err.Error())

		return false
	}

	req.SetBasicAuth(rc.User, rc.Pass)

	client := &http.Client{}

	resp, err := client.Do(req)
	if err != nil {
		rc.Logger.Errorf("failed to make GET request after client do: %v", err.Error())

		return false
	}

	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			rc.Logger.Errorf("failed to close response body: %v", closeErr.Error())
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		rc.Logger.Errorf("failed to read response body: %v", err.Error())

		return false
	}

	var result map[string]any

	err = json.Unmarshal(body, &result)
	if err != nil {
		rc.Logger.Errorf("failed to unmarshal response: %v", err.Error())

		return false
	}

	if status, ok := result["status"].(string); ok && status == "ok" {
		return true
	}

	rc.Logger.Error("rabbitmq unhealthy...")

	return false
}

// Close properly closes the RabbitMQ connection and channel
func (rc *RabbitMQConnection) Close() error {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	var errs []error

	if rc.Channel != nil {
		if err := rc.Channel.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close channel: %w", err))
		}

		rc.Channel = nil
	}

	if rc.Connection != nil {
		if err := rc.Connection.Close(); err != nil {
			errs = append(errs, fmt.Errorf("failed to close connection: %w", err))
		}

		rc.Connection = nil
	}

	rc.Connected = false

	if len(errs) > 0 {
		return fmt.Errorf("errors during close: %v", errs)
	}

	return nil
}

// EnablePublisherConfirms enables publisher confirms for reliable message delivery
func (rc *RabbitMQConnection) EnablePublisherConfirms(enabled bool) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.publisherConfirms = enabled
}

// PublishWithConfirm publishes a message and waits for confirmation
func (rc *RabbitMQConnection) PublishWithConfirm(ctx context.Context, exchange, routingKey string, mandatory, immediate bool, msg amqp.Publishing) error {
	ch, err := rc.GetNewConnect()
	if err != nil {
		return fmt.Errorf("failed to get connection: %w", err)
	}

	// Publish the message
	if err := ch.Publish(exchange, routingKey, mandatory, immediate, msg); err != nil {
		return fmt.Errorf("failed to publish message: %w", err)
	}

	// If publisher confirms are enabled, wait for confirmation
	if rc.publisherConfirms && rc.confirmChannel != nil {
		select {
		case confirm := <-rc.confirmChannel:
			if !confirm.Ack {
				return fmt.Errorf("message was nacked by broker")
			}

			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
			return fmt.Errorf("timeout waiting for publisher confirm")
		}
	}

	return nil
}

// ConnectWithRetry attempts to connect with automatic retry logic
func (rc *RabbitMQConnection) ConnectWithRetry(ctx context.Context) error {
	var lastErr error

	for attempt := 1; attempt <= rc.maxReconnectAttempts; attempt++ {
		err := rc.Connect()
		if err == nil {
			return nil // Success
		}

		lastErr = err
		rc.Logger.Warn("connection attempt failed",
			zap.Int("attempt", attempt),
			zap.Int("max_attempts", rc.maxReconnectAttempts),
			zap.Error(err))

		// Don't sleep after last attempt
		if attempt == rc.maxReconnectAttempts {
			break
		}

		// Wait before retry
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(rc.reconnectInterval):
			// Continue to next attempt
		}
	}

	return fmt.Errorf("failed to connect after %d attempts: %w", rc.maxReconnectAttempts, lastErr)
}

// SetReconnectionConfig configures connection recovery settings
func (rc *RabbitMQConnection) SetReconnectionConfig(interval time.Duration, maxAttempts int) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.reconnectInterval = interval
	rc.maxReconnectAttempts = maxAttempts
}

// IsHealthy checks if the connection is healthy
func (rc *RabbitMQConnection) IsHealthy() bool {
	rc.mu.RLock()
	defer rc.mu.RUnlock()

	if !rc.Connected || rc.Connection == nil || rc.Connection.IsClosed() {
		return false
	}

	// Check if it's time for a health check
	if time.Since(rc.lastHealthCheck) > rc.healthCheckInterval {
		go rc.performHealthCheck()
	}

	return true
}

// performHealthCheck performs an async health check
func (rc *RabbitMQConnection) performHealthCheck() {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	if rc.Channel != nil {
		if err := rc.testAMQPConnection(rc.Channel); err != nil {
			rc.Logger.Warn("health check failed, marking connection unhealthy", zap.Error(err))
			rc.Connected = false
		} else {
			rc.lastHealthCheck = time.Now()
		}
	}
}
