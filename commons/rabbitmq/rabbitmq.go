package rabbitmq

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	commons "github.com/LerianStudio/lib-commons/v5/commons"
	"github.com/LerianStudio/lib-commons/v5/commons/assert"
	"github.com/LerianStudio/lib-commons/v5/commons/backoff"
	constant "github.com/LerianStudio/lib-commons/v5/commons/constants"
	"github.com/LerianStudio/lib-commons/v5/commons/internal/nilcheck"
	"github.com/LerianStudio/lib-commons/v5/commons/log"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v5/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry/metrics"
	"github.com/LerianStudio/lib-commons/v5/commons/security/sanitize"
	amqp "github.com/rabbitmq/amqp091-go"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// connectionFailuresMetric defines the counter for rabbitmq connection failures.
var connectionFailuresMetric = metrics.Metric{
	Name:        "rabbitmq_connection_failures_total",
	Unit:        "1",
	Description: "Total number of rabbitmq connection failures",
}

// RabbitMQConnection is a hub which deal with rabbitmq connections.
type RabbitMQConnection struct {
	mu                     sync.RWMutex // protects connection and channel operations
	ConnectionStringSource string       `json:"-"`
	Connection             *amqp.Connection
	Queue                  string
	HealthCheckURL         string
	Host                   string
	Port                   string
	User                   string `json:"-"`
	Pass                   string `json:"-"`
	VHost                  string
	Channel                *amqp.Channel
	Logger                 log.Logger
	MetricsFactory         *metrics.MetricsFactory
	Connected              bool

	dialer                  func(string) (*amqp.Connection, error)
	dialerContext           func(context.Context, string) (*amqp.Connection, error)
	channelFactory          func(*amqp.Connection) (*amqp.Channel, error)
	channelFactoryContext   func(context.Context, *amqp.Connection) (*amqp.Channel, error)
	connectionCloser        func(*amqp.Connection) error
	connectionCloserContext func(context.Context, *amqp.Connection) error
	connectionClosedFn      func(*amqp.Connection) bool
	channelClosedFn         func(*amqp.Channel) bool
	channelCloser           func(*amqp.Channel) error
	channelCloserContext    func(context.Context, *amqp.Channel) error
	healthHTTPClient        *http.Client

	// AllowInsecureTLS must be set to true to explicitly acknowledge that
	// the health check HTTP client has TLS certificate verification disabled.
	// Without this flag, applyDefaults returns ErrInsecureTLS.
	AllowInsecureTLS bool

	// AllowInsecureHealthCheck must be set to true to explicitly acknowledge
	// that basic auth credentials are sent over plain HTTP (not HTTPS).
	// Without this flag, health check validation returns ErrInsecureHealthCheck.
	AllowInsecureHealthCheck bool

	// HealthCheckAllowedHosts restricts which hosts the health check URL may
	// target. When non-empty, the health check URL's host (optionally host:port)
	// must match one of the entries. This protects against SSRF via
	// configuration injection.
	// When empty, compatibility mode allows any host unless
	// RequireHealthCheckAllowedHosts is true. For basic-auth health checks,
	// derived hosts from AMQP settings are used as fallback enforcement.
	HealthCheckAllowedHosts []string

	// RequireHealthCheckAllowedHosts enforces a non-empty HealthCheckAllowedHosts
	// list for every health check. Keep this false during compatibility rollout,
	// then enable it to hard-fail unsafe configurations.
	RequireHealthCheckAllowedHosts bool

	warnMissingAllowlistOnce sync.Once

	// Reconnect rate-limiting: prevents thundering-herd reconnect storms
	// when the broker is down by enforcing exponential backoff between attempts.
	lastReconnectAttempt time.Time
	reconnectAttempts    int
}

const defaultRabbitMQHealthCheckTimeout = 5 * time.Second

// reconnectBackoffCap is the maximum delay between reconnect attempts.
const reconnectBackoffCap = 30 * time.Second

// ErrInsecureTLS is returned when the health check HTTP client has TLS verification disabled
// without explicitly acknowledging the risk via AllowInsecureTLS.
var ErrInsecureTLS = errors.New("rabbitmq health check HTTP client has TLS verification disabled — set AllowInsecureTLS to acknowledge this risk")

// ErrInsecureHealthCheck is returned when the health check URL uses HTTP with basic auth
// credentials without explicitly opting in via AllowInsecureHealthCheck.
var ErrInsecureHealthCheck = errors.New("rabbitmq health check uses HTTP with basic auth credentials — set AllowInsecureHealthCheck to acknowledge this risk")

// ErrHealthCheckHostNotAllowed is returned when the health check URL targets a host
// not present in the HealthCheckAllowedHosts allowlist.
var ErrHealthCheckHostNotAllowed = errors.New("rabbitmq health check host not in allowed list")

// ErrHealthCheckAllowedHostsRequired is returned when strict allowlist mode is enabled
// but no allowed hosts were configured.
var ErrHealthCheckAllowedHostsRequired = errors.New("rabbitmq health check allowed hosts list is required")

// ErrNilConnection is returned when a method is called on a nil RabbitMQConnection.
var ErrNilConnection = errors.New("rabbitmq connection is nil")

// redactedURLPassword is the canonical redaction marker for credentials in
// RabbitMQ URLs. Delegates to commons/security/sanitize.SecretRedactionMarker
// so log-stream shapes stay uniform across lib-commons.
const redactedURLPassword = sanitize.SecretRedactionMarker

// Best-effort URL matcher used for redaction on arbitrary error messages.
// This intentionally differs from outbox's storage sanitizer because this path
// optimizes for preserving operational error context while redacting credentials.
var urlPattern = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s]+`)

// ChannelSnapshot returns the current channel reference under connection lock.
func (rc *RabbitMQConnection) ChannelSnapshot() *amqp.Channel {
	if rc == nil {
		return nil
	}

	rc.mu.RLock()
	defer rc.mu.RUnlock()

	return rc.Channel
}

// nilConnectionAssert fires a telemetry assertion for nil-receiver calls and returns ErrNilConnection.
// The logger is intentionally nil here because this function is called on a nil *RabbitMQConnection
// receiver, so there is no struct instance from which to extract a logger. The assert package
// handles nil loggers gracefully by falling back to stderr.
func nilConnectionAssert(operation string) error {
	asserter := assert.New(context.Background(), nil, "rabbitmq", operation)
	_ = asserter.Never(context.Background(), "rabbitmq connection receiver is nil")

	return ErrNilConnection
}

// Connect keeps a singleton connection with rabbitmq.
func (rc *RabbitMQConnection) Connect() error {
	return rc.ConnectContext(context.Background())
}

// isFullyConnected reports whether the connection and channel are both open.
// The caller MUST hold rc.mu.
func (rc *RabbitMQConnection) isFullyConnected() bool {
	return rc.Connected &&
		rc.Connection != nil && !rc.connectionClosedFn(rc.Connection) &&
		rc.Channel != nil && !rc.channelClosedFn(rc.Channel)
}

// connectSnapshot captures the configuration state needed for dialing and health
// checking under the lock. The caller MUST hold rc.mu.
type connectSnapshot struct {
	connStr            string
	healthCheckURL     string
	healthUser         string
	healthPass         string
	healthPolicy       healthCheckURLConfig
	healthClient       *http.Client
	dialer             func(context.Context, string) (*amqp.Connection, error)
	channelFactory     func(context.Context, *amqp.Connection) (*amqp.Channel, error)
	connectionClosedFn func(*amqp.Connection) bool
	connCloser         func(*amqp.Connection) error
	logger             log.Logger
}

// snapshotConnectState captures connect-time state under the lock.
// The caller MUST hold rc.mu.
func (rc *RabbitMQConnection) snapshotConnectState() connectSnapshot {
	connStr := rc.ConnectionStringSource
	healthCheckURL := rc.HealthCheckURL
	configuredHosts := append([]string(nil), rc.HealthCheckAllowedHosts...)
	derivedHosts := mergeAllowedHosts(
		deriveAllowedHostsFromConnectionString(connStr),
		append(
			deriveAllowedHostsFromHostPort(rc.Host, rc.Port),
			deriveAllowedHostsFromHealthCheckURL(healthCheckURL)...,
		)...,
	)

	return connectSnapshot{
		connStr:        connStr,
		healthCheckURL: healthCheckURL,
		healthUser:     rc.User,
		healthPass:     rc.Pass,
		healthPolicy: healthCheckURLConfig{
			allowInsecure:       rc.AllowInsecureHealthCheck,
			hasBasicAuth:        rc.User != "" || rc.Pass != "",
			allowedHosts:        configuredHosts,
			derivedAllowedHosts: derivedHosts,
			allowlistConfigured: len(configuredHosts) > 0,
			requireAllowedHosts: rc.RequireHealthCheckAllowedHosts,
		},
		healthClient:       rc.healthHTTPClient,
		dialer:             rc.dialerContext,
		channelFactory:     rc.channelFactoryContext,
		connectionClosedFn: rc.connectionClosedFn,
		connCloser:         rc.connectionCloser,
		logger:             rc.logger(),
	}
}

func (rc *RabbitMQConnection) currentConnectState() (connectSnapshot, bool, error) {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	if err := rc.applyDefaults(); err != nil {
		return connectSnapshot{}, false, err
	}

	return rc.snapshotConnectState(), rc.isFullyConnected(), nil
}

func (rc *RabbitMQConnection) enforceTLSBeforeDial(ctx context.Context, logger log.Logger, connStr string) error {
	if commons.CurrentTier() != commons.TierStrict {
		return nil
	}

	isPlaintext := !strings.HasPrefix(strings.ToLower(connStr), "amqps://")
	result := commons.CheckSecurityRule(commons.RuleTLSRequired, isPlaintext)

	return commons.EnforceSecurityRule(ctx, logger, "rabbitmq", result)
}

// ConnectContext keeps a singleton connection with rabbitmq.
func (rc *RabbitMQConnection) ConnectContext(ctx context.Context) error {
	if rc == nil {
		return nilConnectionAssert("connect_context")
	}

	if ctx == nil {
		ctx = context.Background()
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("rabbitmq connect: %w", err)
	}

	tracer := otel.Tracer("rabbitmq")

	ctx, span := tracer.Start(ctx, "rabbitmq.connect")
	defer span.End()

	span.SetAttributes(attribute.String(constant.AttrDBSystem, constant.DBSystemRabbitMQ))

	snap, fullyConnected, err := rc.currentConnectState()
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "Failed to prepare connect state", err)

		return fmt.Errorf("rabbitmq connect: %w", err)
	}

	if err := rc.enforceTLSBeforeDial(ctx, snap.logger, snap.connStr); err != nil {
		return fmt.Errorf("rabbitmq connect: %w", err)
	}

	// Fast-path: if already connected with an open connection and channel,
	// return immediately without creating a new connection.
	if fullyConnected {
		return nil
	}

	snap.logger.Log(ctx, log.LevelInfo, "connecting to rabbitmq")

	conn, ch, err := rc.dialAndOpenChannel(ctx, span, snap)
	if err != nil {
		return err
	}

	if healthErr := rc.healthCheck(ctx, snap.healthCheckURL, snap.healthUser, snap.healthPass, snap.healthClient, snap.healthPolicy, snap.logger); healthErr != nil {
		rc.closeConnectionWith(conn, snap.connCloser)
		rc.clearConnectionState()

		snap.logger.Log(ctx, log.LevelError, "rabbitmq health check failed")

		return fmt.Errorf("rabbitmq health check failed: %w", healthErr)
	}

	snap.logger.Log(ctx, log.LevelInfo, "connected to rabbitmq")

	rc.mu.Lock()
	if rc.Connection != nil && rc.Connection != conn && !snap.connectionClosedFn(rc.Connection) {
		rc.mu.Unlock()

		rc.closeConnectionWith(conn, snap.connCloser)

		return nil
	}

	rc.Connected = true
	rc.Connection = conn
	rc.Channel = ch
	rc.mu.Unlock()

	return nil
}

// dialAndOpenChannel dials the broker and opens a channel. On any failure it
// clears connection state and records the error on the span before returning.
func (rc *RabbitMQConnection) dialAndOpenChannel(ctx context.Context, span trace.Span, snap connectSnapshot) (*amqp.Connection, *amqp.Channel, error) {
	conn, err := snap.dialer(ctx, snap.connStr)
	if err != nil {
		snap.logger.Log(ctx, log.LevelError, "failed to connect to rabbitmq", log.String("error_detail", sanitizeAMQPErr(err, snap.connStr)))
		rc.recordConnectionFailure("connect")
		rc.clearConnectionState()

		sanitizedErr := newSanitizedError(err, snap.connStr, "failed to connect to rabbitmq")
		libOpentelemetry.HandleSpanError(span, "Failed to connect to rabbitmq", sanitizedErr)

		return nil, nil, sanitizedErr
	}

	ch, err := snap.channelFactory(ctx, conn)
	if err != nil {
		rc.closeConnectionWith(conn, snap.connCloser)
		rc.clearConnectionState()

		snap.logger.Log(ctx, log.LevelError, "failed to open channel on rabbitmq", log.Err(err))

		libOpentelemetry.HandleSpanError(span, "Failed to open channel on rabbitmq", err)

		return nil, nil, fmt.Errorf("failed to open channel on rabbitmq: %w", err)
	}

	if ch == nil {
		rc.closeConnectionWith(conn, snap.connCloser)
		rc.clearConnectionState()

		err = errors.New("can't connect rabbitmq")

		snap.logger.Log(ctx, log.LevelError, "rabbitmq health check failed")

		libOpentelemetry.HandleSpanError(span, "RabbitMQ health check failed", err)

		return nil, nil, fmt.Errorf("rabbitmq health check failed: %w", err)
	}

	return conn, ch, nil
}

// EnsureChannel ensures that the channel is open and connected.
func (rc *RabbitMQConnection) EnsureChannel() error {
	return rc.EnsureChannelContext(context.Background())
}

// ensureChannelSnapshot captures state needed by EnsureChannelContext under the lock.
type ensureChannelSnapshot struct {
	connStr            string
	logger             log.Logger
	dialer             func(context.Context, string) (*amqp.Connection, error)
	channelFactory     func(context.Context, *amqp.Connection) (*amqp.Channel, error)
	connCloser         func(*amqp.Connection) error
	connectionClosedFn func(*amqp.Connection) bool
	needConnection     bool
	needChannel        bool
	existingConn       *amqp.Connection
}

// snapshotEnsureChannelState captures and returns a snapshot of state needed for channel
// ensuring, applying defaults and rate-limiting under the lock. Returns an error if
// defaults fail or the request is rate-limited.
func (rc *RabbitMQConnection) snapshotEnsureChannelState() (ensureChannelSnapshot, error) {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	if err := rc.applyDefaults(); err != nil {
		return ensureChannelSnapshot{}, fmt.Errorf("rabbitmq ensure channel: %w", err)
	}

	connectionClosedFn := rc.connectionClosedFn
	channelClosedFn := rc.channelClosedFn
	needConnection := rc.Connection == nil || connectionClosedFn(rc.Connection)
	needChannel := needConnection || rc.Channel == nil || channelClosedFn(rc.Channel)

	// Rate-limit reconnect attempts: if we've failed recently, enforce a
	// minimum delay before the next attempt to prevent reconnect storms.
	if needConnection && rc.reconnectAttempts > 0 {
		delay := min(backoff.ExponentialWithJitter(500*time.Millisecond, rc.reconnectAttempts), reconnectBackoffCap)

		if elapsed := time.Since(rc.lastReconnectAttempt); elapsed < delay {
			return ensureChannelSnapshot{}, fmt.Errorf("rabbitmq ensure channel: rate-limited (next attempt in %s)", delay-elapsed)
		}
	}

	return ensureChannelSnapshot{
		connStr:            rc.ConnectionStringSource,
		logger:             rc.logger(),
		dialer:             rc.dialerContext,
		channelFactory:     rc.channelFactoryContext,
		connCloser:         rc.connectionCloser,
		connectionClosedFn: connectionClosedFn,
		needConnection:     needConnection,
		needChannel:        needChannel,
		existingConn:       rc.Connection,
	}, nil
}

// EnsureChannelContext ensures that the channel is open and connected.
func (rc *RabbitMQConnection) EnsureChannelContext(ctx context.Context) error {
	if rc == nil {
		return nilConnectionAssert("ensure_channel_context")
	}

	if ctx == nil {
		ctx = context.Background()
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("rabbitmq ensure channel: %w", err)
	}

	tracer := otel.Tracer("rabbitmq")

	ctx, span := tracer.Start(ctx, "rabbitmq.ensure_channel")
	defer span.End()

	span.SetAttributes(attribute.String(constant.AttrDBSystem, constant.DBSystemRabbitMQ))

	snap, err := rc.snapshotEnsureChannelState()
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "Failed to prepare ensure channel state", err)
		return err
	}

	if !snap.needChannel {
		return nil
	}

	if snap.needConnection {
		if err := rc.enforceTLSBeforeDial(ctx, snap.logger, snap.connStr); err != nil {
			return fmt.Errorf("rabbitmq ensure channel: %w", err)
		}
	}

	var conn *amqp.Connection

	newConnection := false

	if snap.needConnection {
		rc.mu.Lock()
		rc.lastReconnectAttempt = time.Now()
		rc.mu.Unlock()

		conn, err = snap.dialer(ctx, snap.connStr)
		if err != nil {
			snap.logger.Log(ctx, log.LevelError, "failed to connect to rabbitmq", log.String("error_detail", sanitizeAMQPErr(err, snap.connStr)))
			rc.recordConnectionFailure("ensure_channel_connect")

			rc.mu.Lock()
			rc.Connected = false
			rc.reconnectAttempts++
			rc.mu.Unlock()

			sanitizedErr := newSanitizedError(err, snap.connStr, "can't connect to rabbitmq")
			libOpentelemetry.HandleSpanError(span, "Failed to connect to rabbitmq", sanitizedErr)

			return sanitizedErr
		}

		newConnection = true
	} else {
		conn = snap.existingConn
	}

	ch, err := snap.channelFactory(ctx, conn)
	if err == nil && ch == nil {
		err = errors.New("channel factory returned nil channel")
	}

	if err != nil {
		rc.handleChannelFailure(conn, snap.existingConn, newConnection, snap.connCloser)
		rc.recordConnectionFailure("ensure_channel")

		snap.logger.Log(ctx, log.LevelError, "failed to open channel on rabbitmq", log.Err(err))

		libOpentelemetry.HandleSpanError(span, "Failed to open channel on rabbitmq", err)

		return fmt.Errorf("rabbitmq ensure channel: %w", err)
	}

	rc.commitChannelState(conn, ch, newConnection, snap)

	return nil
}

// commitChannelState publishes a successful dial/channel-open result into
// the connection state. For a freshly dialed connection it detects races
// against concurrent reconnects, closing whichever connection lost the race
// to prevent connection leaks. For an existing connection it just records
// the new channel. The lock is always released before any connection close.
func (rc *RabbitMQConnection) commitChannelState(conn *amqp.Connection, ch *amqp.Channel, newConnection bool, snap ensureChannelSnapshot) {
	rc.mu.Lock()

	if !newConnection {
		// Another goroutine may have swapped rc.Connection between the snapshot
		// and the lock re-acquisition. If so, ch belongs to snap.existingConn
		// but rc.Connection now points elsewhere — publishing on ch would hit a
		// dead socket. Close the orphaned channel and bail without mutating state.
		if rc.Connection != snap.existingConn || snap.connectionClosedFn(rc.Connection) {
			chCloser := rc.channelCloser
			rc.mu.Unlock()

			if chCloser != nil {
				if err := chCloser(ch); err != nil {
					rc.logger().Log(context.Background(), log.LevelWarn, "failed to close orphaned rabbitmq channel", log.Err(err))
				}
			}

			return
		}

		rc.Channel = ch
		rc.Connected = true
		rc.mu.Unlock()

		return
	}

	// Race detection: if another goroutine already established a healthy
	// connection while we were dialing, close ours and reuse theirs to
	// prevent connection leaks under concurrent reconnection pressure.
	if rc.Connection != nil && rc.Connection != conn && !snap.connectionClosedFn(rc.Connection) {
		rc.mu.Unlock()

		// Close the connection we just created — another goroutine won the race.
		rc.closeConnectionWith(conn, snap.connCloser)

		return
	}

	// Capture old connection to close outside the lock.
	oldConn := rc.Connection
	rc.Connection = conn
	rc.reconnectAttempts = 0
	rc.Channel = ch
	rc.Connected = true
	rc.mu.Unlock()

	// Best-effort cleanup of the replaced connection (usually already dead,
	// but explicit close ensures no half-open TCP socket is leaked).
	if oldConn != nil && oldConn != conn {
		rc.closeConnectionWith(oldConn, snap.connCloser)
	}
}

// GetNewConnect returns a pointer to the rabbitmq connection, initializing it if necessary.
func (rc *RabbitMQConnection) GetNewConnect() (*amqp.Channel, error) {
	return rc.GetNewConnectContext(context.Background())
}

// GetNewConnectContext returns a pointer to the rabbitmq connection, initializing it if necessary.
func (rc *RabbitMQConnection) GetNewConnectContext(ctx context.Context) (*amqp.Channel, error) {
	if rc == nil {
		return nil, nilConnectionAssert("get_new_connect_context")
	}

	if ctx == nil {
		ctx = context.Background()
	}

	if err := ctx.Err(); err != nil {
		return nil, err
	}

	rc.mu.Lock()

	if err := rc.applyDefaults(); err != nil {
		rc.mu.Unlock()

		return nil, err
	}

	if rc.Connected && rc.Channel != nil && !rc.channelClosedFn(rc.Channel) {
		ch := rc.Channel
		rc.mu.Unlock()

		return ch, nil
	}
	rc.mu.Unlock()

	if err := rc.EnsureChannelContext(ctx); err != nil {
		rc.logger().Log(ctx, log.LevelError, "failed to ensure channel", log.Err(err))

		return nil, err
	}

	rc.mu.Lock()
	defer rc.mu.Unlock()

	if rc.Channel == nil {
		rc.Connected = false

		return nil, errors.New("rabbitmq channel not available")
	}

	return rc.Channel, nil
}

// HealthCheck rabbitmq when the server is started.
func (rc *RabbitMQConnection) HealthCheck() (bool, error) {
	return rc.HealthCheckContext(context.Background())
}

// HealthCheckContext rabbitmq when the server is started.
// It captures config fields under lock to avoid reading them during concurrent mutation.
func (rc *RabbitMQConnection) HealthCheckContext(ctx context.Context) (bool, error) {
	if rc == nil {
		return false, nilConnectionAssert("health_check_context")
	}

	if ctx == nil {
		ctx = context.Background()
	}

	tracer := otel.Tracer("rabbitmq")

	ctx, span := tracer.Start(ctx, "rabbitmq.health_check")
	defer span.End()

	span.SetAttributes(attribute.String(constant.AttrDBSystem, constant.DBSystemRabbitMQ))

	rc.mu.Lock()
	if err := rc.applyDefaults(); err != nil {
		rc.mu.Unlock()

		return false, err
	}

	healthURL := rc.HealthCheckURL
	user := rc.User
	pass := rc.Pass
	configuredHosts := append([]string(nil), rc.HealthCheckAllowedHosts...)
	derivedHosts := mergeAllowedHosts(
		deriveAllowedHostsFromConnectionString(rc.ConnectionStringSource),
		append(
			deriveAllowedHostsFromHostPort(rc.Host, rc.Port),
			deriveAllowedHostsFromHealthCheckURL(healthURL)...,
		)...,
	)
	healthPolicy := healthCheckURLConfig{
		allowInsecure:       rc.AllowInsecureHealthCheck,
		hasBasicAuth:        rc.User != "" || rc.Pass != "",
		allowedHosts:        configuredHosts,
		derivedAllowedHosts: derivedHosts,
		allowlistConfigured: len(configuredHosts) > 0,
		requireAllowedHosts: rc.RequireHealthCheckAllowedHosts,
	}
	client := rc.healthHTTPClient
	logger := rc.logger()
	rc.mu.Unlock()

	if err := rc.healthCheck(ctx, healthURL, user, pass, client, healthPolicy, logger); err != nil {
		return false, err
	}

	return true, nil
}

// healthCheck is the internal implementation that operates on pre-captured config values,
// safe to call without holding the mutex.
func (rc *RabbitMQConnection) healthCheck(
	ctx context.Context,
	rawHealthURL, user, pass string,
	client *http.Client,
	policy healthCheckURLConfig,
	logger log.Logger,
) error {
	if ctx == nil {
		ctx = context.Background()
	}

	if err := ctx.Err(); err != nil {
		logger.Log(ctx, log.LevelError, "context canceled during rabbitmq health check", log.Err(err))

		return fmt.Errorf("rabbitmq health check context: %w", err)
	}

	if policy.hasBasicAuth != (user != "" || pass != "") {
		policy.hasBasicAuth = user != "" || pass != ""
	}

	if !policy.allowlistConfigured && !policy.requireAllowedHosts {
		rc.warnMissingAllowlistOnce.Do(func() {
			logger.Log(
				ctx,
				log.LevelWarn,
				"rabbitmq health check explicit host allowlist is empty; compatibility mode may skip host validation. Configure HealthCheckAllowedHosts and set RequireHealthCheckAllowedHosts=true to enforce strict SSRF hardening",
			)
		})
	}

	healthURL, err := validateHealthCheckURLWithConfig(rawHealthURL, policy)
	if err != nil {
		logger.Log(ctx, log.LevelError, "invalid rabbitmq health check URL", log.Err(err))

		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, healthURL, nil)
	if err != nil {
		logger.Log(ctx, log.LevelError, "failed to create rabbitmq health check request", log.Err(err))

		return fmt.Errorf("building rabbitmq health check request: %w", err)
	}

	req.SetBasicAuth(user, pass)

	if client == nil {
		client = &http.Client{Timeout: defaultRabbitMQHealthCheckTimeout}
	}

	// #nosec G704 -- URL is validated via validateHealthCheckURLWithConfig before request; host allowlist and IP safety checks prevent SSRF
	resp, err := client.Do(req)
	if err != nil {
		logger.Log(ctx, log.LevelError, "failed to execute rabbitmq health check request", log.Err(err))

		return fmt.Errorf("executing rabbitmq health check request: %w", err)
	}
	defer resp.Body.Close()

	return parseHealthCheckResponse(ctx, resp, logger)
}

func parseHealthCheckResponse(ctx context.Context, resp *http.Response, logger log.Logger) error {
	if resp == nil {
		return errors.New("rabbitmq health check response is empty")
	}

	if resp.StatusCode != http.StatusOK {
		logger.Log(ctx, log.LevelError, "rabbitmq health check failed", log.String("status", resp.Status))

		return fmt.Errorf("rabbitmq health check status %q", resp.Status)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		logger.Log(ctx, log.LevelError, "failed to read rabbitmq health check response", log.Err(err))

		return fmt.Errorf("reading rabbitmq health check response: %w", err)
	}

	var result map[string]any

	err = json.Unmarshal(body, &result)
	if err != nil {
		logger.Log(ctx, log.LevelError, "failed to parse rabbitmq health check response", log.Err(err))

		return fmt.Errorf("parsing rabbitmq health check response: %w", err)
	}

	if result == nil {
		logger.Log(ctx, log.LevelError, "rabbitmq health check response is empty or null")

		return errors.New("rabbitmq health check response is empty")
	}

	if status, ok := result["status"].(string); ok && status == "ok" {
		return nil
	}

	logger.Log(ctx, log.LevelError, "rabbitmq is not healthy")

	return errors.New("rabbitmq is not healthy")
}

func (rc *RabbitMQConnection) applyDefaults() error {
	rc.applyConnectionDefaults()
	rc.applyChannelDefaults()

	return rc.applyHealthDefaults()
}

func (rc *RabbitMQConnection) applyConnectionDefaults() {
	if rc.dialer == nil {
		rc.dialer = amqp.Dial
	}

	if rc.dialerContext == nil {
		rc.dialerContext = func(_ context.Context, connectionString string) (*amqp.Connection, error) {
			return rc.dialer(connectionString)
		}
	}

	if rc.connectionCloser == nil {
		rc.connectionCloser = func(connection *amqp.Connection) error {
			if connection == nil {
				return nil
			}

			return connection.Close()
		}
	}

	if rc.connectionCloserContext == nil {
		rc.connectionCloserContext = func(_ context.Context, connection *amqp.Connection) error {
			return rc.connectionCloser(connection)
		}
	}

	if rc.connectionClosedFn == nil {
		rc.connectionClosedFn = func(connection *amqp.Connection) bool {
			if connection == nil {
				return true
			}

			return connection.IsClosed()
		}
	}
}

func (rc *RabbitMQConnection) applyChannelDefaults() {
	if rc.channelFactory == nil {
		rc.channelFactory = func(connection *amqp.Connection) (*amqp.Channel, error) {
			if connection == nil {
				return nil, errors.New("cannot create channel: connection is nil")
			}

			return connection.Channel()
		}
	}

	if rc.channelFactoryContext == nil {
		rc.channelFactoryContext = func(_ context.Context, connection *amqp.Connection) (*amqp.Channel, error) {
			return rc.channelFactory(connection)
		}
	}

	if rc.channelClosedFn == nil {
		rc.channelClosedFn = func(ch *amqp.Channel) bool {
			if ch == nil {
				return true
			}

			return ch.IsClosed()
		}
	}

	if rc.channelCloser == nil {
		rc.channelCloser = func(ch *amqp.Channel) error {
			if ch == nil {
				return nil
			}

			return ch.Close()
		}
	}

	if rc.channelCloserContext == nil {
		rc.channelCloserContext = func(_ context.Context, ch *amqp.Channel) error {
			return rc.channelCloser(ch)
		}
	}
}

func (rc *RabbitMQConnection) applyHealthDefaults() error {
	if rc.healthHTTPClient == nil {
		rc.healthHTTPClient = &http.Client{Timeout: defaultRabbitMQHealthCheckTimeout}

		return nil
	}

	transport, ok := rc.healthHTTPClient.Transport.(*http.Transport)
	if !ok || transport.TLSClientConfig == nil {
		return nil
	}

	if transport.TLSClientConfig.InsecureSkipVerify && !rc.AllowInsecureTLS {
		return ErrInsecureTLS
	}

	return nil
}

func (rc *RabbitMQConnection) closeConnectionWith(connection *amqp.Connection, closer func(*amqp.Connection) error) {
	if closer == nil {
		return
	}

	if err := closer(connection); err != nil {
		rc.logger().Log(context.Background(), log.LevelWarn, "failed to close rabbitmq connection during cleanup", log.Err(err))
	}
}

// clearConnectionState resets the connection state under lock after a failed
// connect/reconnect attempt, ensuring no stale Connected/Connection/Channel
// references remain.
func (rc *RabbitMQConnection) clearConnectionState() {
	rc.mu.Lock()
	rc.Connected = false
	rc.Connection = nil
	rc.Channel = nil
	rc.mu.Unlock()
}

// handleChannelFailure cleans up after a failed channel creation in EnsureChannelContext.
// It conditionally closes the connection and resets the channel/connected state.
func (rc *RabbitMQConnection) handleChannelFailure(conn, existingConn *amqp.Connection, newConnection bool, connCloser func(*amqp.Connection) error) {
	if newConnection {
		rc.closeConnectionWith(conn, connCloser)
	}

	rc.mu.Lock()
	if newConnection && rc.Connection == existingConn {
		rc.Connection = nil
	}

	rc.Channel = nil
	rc.Connected = false
	rc.mu.Unlock()
}

// Close closes the rabbitmq channel and connection.
func (rc *RabbitMQConnection) Close() error {
	return rc.CloseContext(context.Background())
}

// CloseContext closes the rabbitmq channel and connection.
func (rc *RabbitMQConnection) CloseContext(ctx context.Context) error {
	if rc == nil {
		return nilConnectionAssert("close_context")
	}

	if ctx == nil {
		ctx = context.Background()
	}

	if err := ctx.Err(); err != nil {
		return fmt.Errorf("rabbitmq close: %w", err)
	}

	tracer := otel.Tracer("rabbitmq")

	ctx, span := tracer.Start(ctx, "rabbitmq.close")
	defer span.End()

	span.SetAttributes(attribute.String(constant.AttrDBSystem, constant.DBSystemRabbitMQ))

	rc.mu.Lock()
	_ = rc.applyDefaults() // Close must not fail due to TLS config — resources still need cleanup.
	channel := rc.Channel
	connection := rc.Connection
	chCloser := rc.channelCloserContext
	connCloser := rc.connectionCloserContext
	rc.Connection = nil
	rc.Channel = nil
	rc.Connected = false
	logger := rc.logger()
	rc.mu.Unlock()

	var closeErr error

	if channel != nil {
		if err := chCloser(ctx, channel); err != nil {
			closeErr = fmt.Errorf("failed to close rabbitmq channel: %w", err)
			logger.Log(ctx, log.LevelWarn, "failed to close rabbitmq channel", log.Err(err))
		}
	}

	if connection != nil {
		if err := connCloser(ctx, connection); err != nil {
			if closeErr == nil {
				closeErr = fmt.Errorf("failed to close rabbitmq connection: %w", err)
			} else {
				closeErr = errors.Join(closeErr, fmt.Errorf("failed to close rabbitmq connection: %w", err))
			}

			logger.Log(ctx, log.LevelWarn, "failed to close rabbitmq connection", log.Err(err))
		}
	}

	if closeErr != nil {
		libOpentelemetry.HandleSpanError(span, "Failed to close rabbitmq", closeErr)
	}

	return closeErr
}

func (rc *RabbitMQConnection) logger() log.Logger {
	if rc == nil {
		return &log.NopLogger{}
	}

	// Use reflect-based typed-nil detection: an interface can be non-nil at the
	// interface level while holding a nil concrete pointer (typed-nil). Calling
	// methods on a typed-nil logger will panic. The nilcheck package handles this.
	if nilcheck.Interface(rc.Logger) {
		return &log.NopLogger{}
	}

	return rc.Logger
}

// healthCheckURLConfig holds validation parameters for health check URL checking.
type healthCheckURLConfig struct {
	allowInsecure       bool
	hasBasicAuth        bool
	allowedHosts        []string
	derivedAllowedHosts []string
	allowlistConfigured bool
	requireAllowedHosts bool
}

// validateHealthCheckURLWithConfig validates the health check URL and appends the RabbitMQ health endpoint path
// if not already present. The HealthCheckURL should be the RabbitMQ management API base URL
// (e.g., "http://host:15672" or "https://host:15672"), NOT the full health endpoint.
// If the URL already ends with "/api/health/checks/alarms", it is returned as-is.
func validateHealthCheckURLWithConfig(rawURL string, cfg healthCheckURLConfig) (string, error) {
	cfg = normalizeHealthCheckURLConfig(cfg)

	parsedURL, err := parseAndValidateHealthCheckBaseURL(rawURL, cfg)
	if err != nil {
		return "", err
	}

	enforceHosts, hostsToEnforce, err := resolveHealthCheckAllowedHosts(cfg)
	if err != nil {
		return "", err
	}

	if err := validateHealthCheckHostAllowlist(parsedURL.Host, enforceHosts, hostsToEnforce); err != nil {
		return "", err
	}

	return normalizeHealthCheckEndpointPath(parsedURL), nil
}

func normalizeHealthCheckURLConfig(cfg healthCheckURLConfig) healthCheckURLConfig {
	if !cfg.allowlistConfigured && len(cfg.allowedHosts) > 0 {
		cfg.allowlistConfigured = true
	}

	return cfg
}

func parseAndValidateHealthCheckBaseURL(rawURL string, cfg healthCheckURLConfig) (*url.URL, error) {
	healthURL := strings.TrimSpace(rawURL)
	if healthURL == "" {
		return nil, errors.New("rabbitmq health check URL is empty")
	}

	parsedURL, err := url.Parse(healthURL)
	if err != nil {
		return nil, err
	}

	if parsedURL.Scheme != "http" && parsedURL.Scheme != "https" {
		return nil, errors.New("rabbitmq health check URL must use http or https")
	}

	if parsedURL.Host == "" {
		return nil, errors.New("rabbitmq health check URL must include a host")
	}

	if parsedURL.User != nil {
		return nil, errors.New("rabbitmq health check URL must not include user credentials")
	}

	if parsedURL.Scheme == "http" && cfg.hasBasicAuth && !cfg.allowInsecure {
		return nil, ErrInsecureHealthCheck
	}

	return parsedURL, nil
}

func resolveHealthCheckAllowedHosts(cfg healthCheckURLConfig) (bool, []string, error) {
	if cfg.requireAllowedHosts && (!cfg.allowlistConfigured || len(cfg.allowedHosts) == 0) {
		return false, nil, ErrHealthCheckAllowedHostsRequired
	}

	enforceHosts := cfg.allowlistConfigured
	hostsToEnforce := cfg.allowedHosts

	if cfg.hasBasicAuth && !cfg.allowInsecure {
		switch {
		case len(cfg.allowedHosts) > 0:
			return true, cfg.allowedHosts, nil
		case len(cfg.derivedAllowedHosts) > 0:
			return true, cfg.derivedAllowedHosts, nil
		default:
			return false, nil, ErrHealthCheckAllowedHostsRequired
		}
	}

	return enforceHosts, hostsToEnforce, nil
}

func validateHealthCheckHostAllowlist(host string, enforceHosts bool, allowedHosts []string) error {
	if !enforceHosts {
		return nil
	}

	if !isHostAllowed(host, allowedHosts) {
		return fmt.Errorf("%w: %s", ErrHealthCheckHostNotAllowed, host)
	}

	return nil
}

func normalizeHealthCheckEndpointPath(parsedURL *url.URL) string {
	const healthPath = "/api/health/checks/alarms"

	normalized := strings.TrimSuffix(parsedURL.String(), "/")
	if strings.HasSuffix(normalized, healthPath) {
		return normalized
	}

	return normalized + healthPath
}

func isHostAllowed(host string, allowedHosts []string) bool {
	hostName, hostPort := splitHostPortOrHost(host)
	targetAddr, targetIsIP := parseNormalizedAddr(hostName)

	for _, allowed := range allowedHosts {
		allowed = strings.TrimSpace(allowed)
		if allowed == "" {
			continue
		}

		allowedName, allowedPort := splitHostPortOrHost(allowed)
		if !isAllowedHostMatch(hostName, targetAddr, targetIsIP, allowedName) {
			continue
		}

		if allowedPort == "" || strings.EqualFold(hostPort, allowedPort) {
			return true
		}
	}

	return false
}

func isAllowedHostMatch(hostName string, hostAddr netip.Addr, hostIsIP bool, allowedName string) bool {
	if prefix, ok := parseNormalizedPrefix(allowedName); ok {
		return hostIsIP && prefix.Contains(hostAddr)
	}

	allowedAddr, allowedIsIP := parseNormalizedAddr(allowedName)

	if hostIsIP && allowedIsIP {
		return hostAddr == allowedAddr
	}

	if !hostIsIP && !allowedIsIP {
		return strings.EqualFold(hostName, allowedName)
	}

	return false
}

func parseNormalizedAddr(value string) (netip.Addr, bool) {
	trimmed := strings.Trim(strings.TrimSpace(value), "[]")
	if trimmed == "" {
		return netip.Addr{}, false
	}

	addr, err := netip.ParseAddr(trimmed)
	if err != nil {
		return netip.Addr{}, false
	}

	return addr.Unmap(), true
}

func parseNormalizedPrefix(value string) (netip.Prefix, bool) {
	trimmed := strings.Trim(strings.TrimSpace(value), "[]")
	if trimmed == "" {
		return netip.Prefix{}, false
	}

	prefix, err := netip.ParsePrefix(trimmed)
	if err != nil {
		return netip.Prefix{}, false
	}

	return prefix.Masked(), true
}

func splitHostPortOrHost(value string) (string, string) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", ""
	}

	host, port, err := net.SplitHostPort(trimmed)
	if err == nil {
		return strings.Trim(host, "[]"), port
	}

	return strings.Trim(trimmed, "[]"), ""
}

// deriveAllowedHostsFromHealthCheckURL extracts the host (and host:port) from
// a health check URL to add to the derived allowlist. This ensures that when
// basic-auth credentials are configured, the health check is at minimum
// restricted to its own configured URL host, preventing SSRF even without an
// explicit allowlist.
func deriveAllowedHostsFromHealthCheckURL(healthCheckURL string) []string {
	trimmed := strings.TrimSpace(healthCheckURL)
	if trimmed == "" {
		return nil
	}

	parsedURL, err := url.Parse(trimmed)
	if err != nil || parsedURL == nil || parsedURL.Host == "" {
		return nil
	}

	hostName, _ := splitHostPortOrHost(parsedURL.Host)

	return mergeAllowedHosts(nil, parsedURL.Host, hostName)
}

func deriveAllowedHostsFromConnectionString(connectionString string) []string {
	trimmed := strings.TrimSpace(connectionString)
	if trimmed == "" {
		return nil
	}

	parsedURL, err := url.Parse(trimmed)
	if err != nil || parsedURL == nil || parsedURL.Host == "" {
		return nil
	}

	hostName, _ := splitHostPortOrHost(parsedURL.Host)

	return mergeAllowedHosts(nil, parsedURL.Host, hostName)
}

func deriveAllowedHostsFromHostPort(host, port string) []string {
	host = strings.TrimSpace(host)
	if host == "" {
		return nil
	}

	if strings.TrimSpace(port) == "" {
		return mergeAllowedHosts(nil, host)
	}

	return mergeAllowedHosts(nil, net.JoinHostPort(host, port), host)
}

func mergeAllowedHosts(base []string, additional ...string) []string {
	if len(base) == 0 && len(additional) == 0 {
		return nil
	}

	merged := make([]string, 0, len(base)+len(additional))
	seen := make(map[string]struct{}, len(base)+len(additional))

	for _, host := range append(append([]string(nil), base...), additional...) {
		trimmed := strings.TrimSpace(host)
		if trimmed == "" {
			continue
		}

		key := strings.ToLower(trimmed)
		if _, exists := seen[key]; exists {
			continue
		}

		seen[key] = struct{}{}

		merged = append(merged, trimmed)
	}

	if len(merged) == 0 {
		return nil
	}

	return merged
}

// sanitizedError wraps an original error with a redacted message.
// Error() returns the sanitized message; Unwrap() returns the original
// so that errors.Is / errors.As still work for programmatic inspection.
type sanitizedError struct {
	original error
	message  string
}

// Error returns the sanitized message.
func (e *sanitizedError) Error() string { return e.message }

// Unwrap returns the original wrapped error.
func (e *sanitizedError) Unwrap() error { return e.original }

// newSanitizedError wraps err with a human-readable prefix and redacted connection string.
func newSanitizedError(err error, connectionString, prefix string) error {
	return fmt.Errorf("%s: %w", prefix, &sanitizedError{
		original: err,
		message:  sanitizeAMQPErr(err, connectionString),
	})
}

func sanitizeAMQPErr(err error, connectionString string) string {
	if err == nil {
		return ""
	}

	errMsg := err.Error()

	if connectionString == "" {
		return redactURLCredentials(errMsg)
	}

	referenceURL, parseErr := url.Parse(connectionString)
	if parseErr != nil {
		return redactURLCredentials(errMsg)
	}

	// Build the redacted form via the fallback splitter. stdlib's
	// (*url.URL).Redacted() hardcodes "xxxxx"; we want the sanitize package's
	// canonical marker for cross-package consistency.
	redactedURL := redactURLCredentialsFallback(connectionString)

	if strings.Contains(errMsg, connectionString) {
		errMsg = strings.ReplaceAll(errMsg, connectionString, redactedURL)
	}

	if strings.Contains(errMsg, referenceURL.String()) {
		errMsg = strings.ReplaceAll(errMsg, referenceURL.String(), redactedURL)
	}

	// Redact decoded password individually — covers cases where the error message
	// contains the password in decoded form (e.g., URL-encoded special characters).
	if referenceURL.User != nil {
		if pass, ok := referenceURL.User.Password(); ok && pass != "" {
			errMsg = strings.ReplaceAll(errMsg, pass, redactedURLPassword)
		}
	}

	return redactURLCredentials(errMsg)
}

func redactURLCredentials(message string) string {
	if message == "" {
		return ""
	}

	return urlPattern.ReplaceAllStringFunc(message, redactURLCredentialsCandidate)
}

func redactURLCredentialsCandidate(candidate string) string {
	core, suffix := splitTrailingURLPunctuation(candidate)

	return redactURLCredentialToken(core) + suffix
}

func splitTrailingURLPunctuation(candidate string) (string, string) {
	end := len(candidate)

	for end > 0 {
		switch candidate[end-1] {
		case '.', ',', ';', ')', ']', '}', '"', '\'':
			end--
		default:
			return candidate[:end], candidate[end:]
		}
	}

	return "", candidate
}

func redactURLCredentialToken(token string) string {
	if token == "" {
		return ""
	}

	parsedURL, err := url.Parse(token)
	if err == nil && parsedURL != nil && parsedURL.User != nil {
		if _, hasPassword := parsedURL.User.Password(); hasPassword {
			// Use the fallback string-splitter rather than (*url.URL).String().
			// The url package percent-encodes userinfo at serialize time, which
			// would turn our marker ("****") into "%2A%2A%2A%2A" — unreadable
			// in logs. Parse is still the authoritative detector for a
			// userinfo-carrying URL; the replacement itself must stay literal.
			return redactURLCredentialsFallback(token)
		}

		return token
	}

	return redactURLCredentialsFallback(token)
}

func redactURLCredentialsFallback(token string) string {
	schemeSeparator := strings.Index(token, "://")
	if schemeSeparator == -1 {
		return token
	}

	rest := token[schemeSeparator+3:]
	authorityEnd := len(rest)

	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case '/', '?', '#':
			authorityEnd = i
			i = len(rest)
		}
	}

	atIndex := strings.LastIndex(rest[:authorityEnd], "@")
	if atIndex == -1 && authorityEnd < len(rest) {
		candidate := rest[:authorityEnd]
		if separator := strings.LastIndex(candidate, ":"); separator > 0 {
			tail := candidate[separator+1:]
			if tail != "" && !allDigits(tail) {
				atIndex = strings.LastIndex(rest, "@")
			}
		}
	}

	if atIndex == -1 {
		return token
	}

	userinfo := rest[:atIndex]
	hostAndSuffix := rest[atIndex+1:]

	if hostAndSuffix == "" {
		return token
	}

	username, _, found := strings.Cut(userinfo, ":")
	if !found {
		return token
	}

	return token[:schemeSeparator+3] + username + ":" + redactedURLPassword + "@" + hostAndSuffix
}

func allDigits(value string) bool {
	if value == "" {
		return false
	}

	for i := range len(value) {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}

	return true
}

// recordConnectionFailure increments the rabbitmq connection failure counter.
// No-op when MetricsFactory is nil.
func (rc *RabbitMQConnection) recordConnectionFailure(operation string) {
	if rc == nil || rc.MetricsFactory == nil {
		return
	}

	counter, err := rc.MetricsFactory.Counter(connectionFailuresMetric)
	if err != nil {
		rc.logger().Log(context.Background(), log.LevelWarn, "failed to create rabbitmq metric counter", log.Err(err))
		return
	}

	err = counter.
		WithLabels(map[string]string{
			"operation": constant.SanitizeMetricLabel(operation),
		}).
		AddOne(context.Background())
	if err != nil {
		rc.logger().Log(context.Background(), log.LevelWarn, "failed to record rabbitmq metric", log.Err(err))
	}
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
