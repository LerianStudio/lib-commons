package rabbitmq

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/LerianStudio/lib-commons/v2/commons/log"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"
)

// RabbitMQConnection is a hub which deal with rabbitmq connections.
type RabbitMQConnection struct {
	mu                     sync.Mutex // protects connection and channel operations
	ConnectionStringSource string
	Connection             *amqp.Connection
	Queue                  string
	HealthCheckURL         string
	Host                   string
	Port                   string
	User                   string
	Pass                   string
	Channel                *amqp.Channel
	Logger                 log.Logger
	Connected              bool
}

// Connect keeps a singleton connection with rabbitmq.
func (rc *RabbitMQConnection) Connect() error {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	rc.Logger.Info("Connecting on rabbitmq...")

	conn, err := amqp.Dial(rc.ConnectionStringSource)
	if err != nil {
		rc.Logger.Error("failed to connect on rabbitmq", zap.Error(err))
		return fmt.Errorf("failed to connect to rabbitmq: %w", err)
	}

	ch, err := conn.Channel()
	if err != nil {
		if closeErr := conn.Close(); closeErr != nil {
			rc.Logger.Warn("failed to close connection during cleanup", zap.Error(closeErr))
		}

		rc.Logger.Error("failed to open channel on rabbitmq", zap.Error(err))
		return fmt.Errorf("failed to open channel on rabbitmq: %w", err)
	}

	if ch == nil || !rc.HealthCheck() {
		if closeErr := conn.Close(); closeErr != nil {
			rc.Logger.Warn("failed to close connection during cleanup", zap.Error(closeErr))
		}

		rc.Connected = false
		err = errors.New("can't connect rabbitmq")
		rc.Logger.Error("RabbitMQ.HealthCheck failed", zap.Error(err))

		return fmt.Errorf("rabbitmq health check failed: %w", err)
	}

	rc.Logger.Info("Connected on rabbitmq ✅ \n")

	rc.Connected = true
	rc.Connection = conn

	rc.Channel = ch

	return nil
}

// EnsureChannel ensures that the channel is open and connected.
func (rc *RabbitMQConnection) EnsureChannel() error {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	newConnection := false

	if rc.Connection == nil || rc.Connection.IsClosed() {
		conn, err := amqp.Dial(rc.ConnectionStringSource)
		if err != nil {
			rc.Logger.Errorf("can't connect to rabbitmq: %v", err)

			return err
		}

		rc.Connection = conn
		newConnection = true
	}

	if rc.Channel == nil || rc.Channel.IsClosed() {
		ch, err := rc.Connection.Channel()
		if err != nil {
			// cleanup connection if we just created it and channel creation fails
			if newConnection {
				if closeErr := rc.Connection.Close(); closeErr != nil {
					rc.Logger.Warn("failed to close connection during cleanup", zap.Error(closeErr))
				}

				rc.Connection = nil
			}

			rc.Logger.Errorf("can't open channel on rabbitmq: %v", err)

			return err
		}

		rc.Channel = ch
	}

	rc.Connected = true

	return nil
}

// GetNewConnect returns a pointer to the rabbitmq connection, initializing it if necessary.
func (rc *RabbitMQConnection) GetNewConnect() (*amqp.Channel, error) {
	if !rc.Connected {
		err := rc.Connect()
		if err != nil {
			rc.Logger.Infof("ERRCONECT %s", err)

			return nil, err
		}
	}

	return rc.Channel, nil
}

// HealthCheck rabbitmq when the server is started
func (rc *RabbitMQConnection) HealthCheck() bool {
	url := rc.HealthCheckURL + "/api/health/checks/alarms"

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

	defer resp.Body.Close()

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
