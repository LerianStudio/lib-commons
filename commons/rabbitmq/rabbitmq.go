// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package rabbitmq

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
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
	Pass                   string //#nosec G117 -- Credential field required for RabbitMQ connection config
	VHost                  string
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
	healthURL := rc.HealthCheckURL + "/api/health/checks/alarms"

	req, err := http.NewRequest(http.MethodGet, healthURL, nil)
	if err != nil {
		rc.Logger.Errorf("failed to make GET request before client do: %v", err.Error())

		return false
	}

	req.SetBasicAuth(rc.User, rc.Pass)

	client := &http.Client{}

	resp, err := client.Do(req) //#nosec G704 -- HealthCheckURL is operator-configured, not user input
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

// BuildRabbitMQConnectionString constructs an AMQP connection string.
// If vhost is empty, the default vhost "/" is used (no path in URL).
// Special characters in user, password, and vhost are URL-encoded automatically.
// Supports IPv6 hosts (e.g., "[::1]").
func BuildRabbitMQConnectionString(protocol, user, pass, host, port, vhost string) string {
	u := &url.URL{Scheme: protocol}
	if user != "" || pass != "" {
		u.User = url.UserPassword(user, pass)
	}

	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	} else {
		// Bracket bare IPv6 addresses to avoid malformed URLs (e.g., amqp://user:pass@::1)
		if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
			u.Host = "[" + host + "]"
		} else {
			u.Host = host
		}
	}

	if vhost != "" {
		// Use QueryEscape instead of PathEscape because RabbitMQ vhost names may contain '/'
		// which must be percent-encoded as %2F. QueryEscape encodes '/' while PathEscape does not.
		// The subsequent ReplaceAll converts query-style space encoding (+) to path-style (%20).
		escapedVHost := url.QueryEscape(vhost)
		escapedVHost = strings.ReplaceAll(escapedVHost, "+", "%20")
		u.Path = "/" + vhost
		u.RawPath = "/" + escapedVHost
	}

	return u.String()
}
