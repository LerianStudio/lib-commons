package rabbitmq

import (
	"encoding/json"
	"errors"
	"github.com/LerianStudio/lib-commons/commons/log"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"
	"io"
	"net/http"
	"time"
)

// RabbitMQConnection is a hub which deal with rabbitmq connections.
type RabbitMQConnection struct {
	ConnectionStringSource string
	Queue                  string
	HealthCheckURL         string
	Host                   string
	Port                   string
	User                   string
	Pass                   string
	Channel                *amqp.Channel
	Logger                 log.Logger
	Connected              bool
	conn                   *amqp.Connection
	HeartBeat              int
}

// Connect keeps a singleton connection with rabbitmq.
func (rc *RabbitMQConnection) Connect() error {
	rc.Logger.Info("Connecting to RabbitMQ...")

	conn, err := amqp.DialConfig(rc.ConnectionStringSource, amqp.Config{
		Heartbeat: time.Duration(rc.HeartBeat) * time.Second,
	})

	if err != nil {
		rc.Connected = false
		rc.Logger.Fatal("failed to connect to RabbitMQ", zap.Error(err))

		return err
	}

	ch, err := conn.Channel()
	if err != nil {
		rc.Connected = false
		rc.Logger.Fatal("failed to open channel", zap.Error(err))

		return err
	}

	rc.conn = conn
	rc.Channel = ch
	rc.Connected = true

	rc.Logger.Info("Connected to RabbitMQ ✅ \n")

	if conn.IsClosed() {
		err := errors.New("connection is closed after connect")
		rc.Logger.Error("RabbitMQ: connection unexpectedly closed", zap.Error(err))

		rc.Connected = false

		return err
	}

	return nil
}

// GetNewConnect returns a pointer to the rabbitmq connection, initializing it if necessary.
func (rc *RabbitMQConnection) GetNewConnect() (*amqp.Channel, error) {
	if rc.conn == nil || rc.conn.IsClosed() || rc.Channel == nil || rc.Channel.IsClosed() {
		rc.Logger.Warn("RabbitMQ connection or channel is closed. Reconnecting...")

		if err := rc.Connect(); err != nil {
			rc.Logger.Error("Failed to reconnect to RabbitMQ", zap.Error(err))
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
