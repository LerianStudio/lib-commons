// Package rabbitmq provides RabbitMQ connection and messaging functionality.
// It includes connection management, health checks, and message publishing/consuming.
package rabbitmq

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/LerianStudio/lib-commons/commons/log"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"
)

// RabbitMQConnection is a hub which deal with rabbitmq connections.
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
}

// Connect keeps a singleton connection with rabbitmq.
func (rc *RabbitMQConnection) Connect() error {
	rc.Logger.Info("Connecting to rabbitmq...")

	conn, err := amqp.Dial(rc.ConnectionStringSource)
	if err != nil {
		rc.Logger.Error("failed to connect to rabbitmq", zap.Error(err))
		return fmt.Errorf("failed to connect to rabbitmq: %w", err)
	}

	ch, err := conn.Channel()
	if err != nil {
		conn.Close() // Clean up connection if channel creation fails
		rc.Logger.Error("failed to open channel on rabbitmq", zap.Error(err))
		return fmt.Errorf("failed to open rabbitmq channel: %w", err)
	}

	// Test connection with AMQP-level health check instead of HTTP
	if err := rc.testAMQPConnection(ch); err != nil {
		ch.Close()
		conn.Close()
		rc.Logger.Error("AMQP connection health check failed", zap.Error(err))
		return fmt.Errorf("rabbitmq health check failed: %w", err)
	}

	rc.Logger.Info("Connected to rabbitmq ✅")

	rc.Connected = true
	rc.Channel = ch
	rc.Connection = conn

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
