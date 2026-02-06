package rabbitmq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/LerianStudio/lib-commons/v2/commons/log"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.uber.org/zap"
)

// DefaultConnectionTimeout is the default timeout for establishing RabbitMQ connections
// when ConnectionTimeout field is not set.
const DefaultConnectionTimeout = 30 * time.Second

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
	VHost                  string
	Channel                *amqp.Channel
	Logger                 log.Logger
	Connected              bool
	ConnectionTimeout      time.Duration // timeout for establishing connection. Zero value uses default of 30s.
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
// For context-aware connection handling with timeout support, see EnsureChannelWithContext.
func (rc *RabbitMQConnection) EnsureChannel() error {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	newConnection := false

	if rc.Connection == nil || rc.Connection.IsClosed() {
		conn, err := amqp.Dial(rc.ConnectionStringSource)
		if err != nil {
			rc.Logger.Error("failed to connect to rabbitmq", zap.Error(err))

			return fmt.Errorf("failed to connect to rabbitmq: %w", err)
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

			// Reset stale state so GetNewConnect triggers reconnection
			rc.Connected = false
			rc.Channel = nil

			rc.Logger.Error("failed to open channel on rabbitmq", zap.Error(err))

			return fmt.Errorf("failed to open channel on rabbitmq: %w", err)
		}

		rc.Channel = ch
	}

	rc.Connected = true

	return nil
}

// EnsureChannelWithContext ensures that the channel is open and connected,
// respecting context cancellation and deadline. Unlike EnsureChannel, this method
// will return immediately if context is cancelled or deadline exceeded.
//
// The effective connection timeout is the minimum of:
//   - The remaining time until context deadline (if context has a deadline)
//   - ConnectionTimeout field value (defaults to 30s if zero)
//
// Usage:
//
//	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
//	defer cancel()
//	if err := conn.EnsureChannelWithContext(ctx); err != nil {
//	    // Handle error - could be context timeout or connection failure
//	}
func (rc *RabbitMQConnection) EnsureChannelWithContext(ctx context.Context) error {
	// Check context before acquiring lock to fail fast
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	rc.mu.Lock()
	defer rc.mu.Unlock()

	// Check context again after acquiring lock
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	newConnection := false

	if rc.Connection == nil || rc.Connection.IsClosed() {
		conn, err := rc.dialWithContext(ctx)
		if err != nil {
			rc.Logger.Error("failed to connect to rabbitmq", zap.Error(err))
			return fmt.Errorf("failed to connect to rabbitmq: %w", err)
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

			// Reset stale state so GetNewConnect triggers reconnection
			rc.Connected = false
			rc.Channel = nil

			rc.Logger.Error("failed to open channel on rabbitmq", zap.Error(err))

			return fmt.Errorf("failed to open channel on rabbitmq: %w", err)
		}

		rc.Channel = ch
	}

	rc.Connected = true

	return nil
}

// dialWithContext creates an AMQP connection with context awareness.
// It extracts the deadline from context and uses it as connection timeout.
// If context has no deadline, uses ConnectionTimeout field (default 30s).
func (rc *RabbitMQConnection) dialWithContext(ctx context.Context) (*amqp.Connection, error) {
	// Determine timeout from context deadline or default
	timeout := rc.ConnectionTimeout
	if timeout == 0 {
		timeout = DefaultConnectionTimeout
	}

	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, context.DeadlineExceeded
		}

		if remaining < timeout {
			timeout = remaining
		}
	}

	// Create config with custom dialer that respects timeout
	config := amqp.Config{
		Dial: func(network, addr string) (net.Conn, error) {
			// Check context before dialing
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			default:
			}

			dialer := &net.Dialer{
				Timeout: timeout,
			}

			conn, err := dialer.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}

			return conn, nil
		},
	}

	return amqp.DialConfig(rc.ConnectionStringSource, config)
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

// BuildRabbitMQConnectionString constructs an AMQP connection string.
// If vhost is empty, the default vhost "/" is used (no path in URL).
// Special characters in user, password, and vhost are URL-encoded automatically.
// Supports IPv6 hosts (e.g., "[::1]").
func BuildRabbitMQConnectionString(protocol, user, pass, host, port, vhost string) string {
	u := &url.URL{
		Scheme: protocol,
		User:   url.UserPassword(user, pass),
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
