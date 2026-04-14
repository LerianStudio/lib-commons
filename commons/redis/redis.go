package redis

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	iamcredentials "cloud.google.com/go/iam/credentials/apiv1"
	iamcredentialspb "cloud.google.com/go/iam/credentials/apiv1/credentialspb"
	commons "github.com/LerianStudio/lib-commons/v5/commons"
	"github.com/LerianStudio/lib-commons/v5/commons/assert"
	"github.com/LerianStudio/lib-commons/v5/commons/backoff"
	constant "github.com/LerianStudio/lib-commons/v5/commons/constants"
	"github.com/LerianStudio/lib-commons/v5/commons/log"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v5/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v5/commons/opentelemetry/metrics"
	"github.com/LerianStudio/lib-commons/v5/commons/runtime"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	"google.golang.org/protobuf/types/known/durationpb"
)

const (
	gcpScope                = "https://www.googleapis.com/auth/cloud-platform"
	gcpServiceAccountPrefix = "projects/-/serviceAccounts/"

	defaultTokenLifetime           = 1 * time.Hour
	defaultRefreshEvery            = 50 * time.Minute
	defaultRefreshCheckInterval    = 10 * time.Second
	defaultRefreshOperationTimeout = 15 * time.Second
)

var (
	// ErrNilClient is returned when a redis client receiver is nil.
	ErrNilClient = errors.New("redis client is nil")
	// ErrInvalidConfig indicates the provided redis configuration is invalid.
	ErrInvalidConfig = errors.New("invalid redis config")

	// pkgLogger holds the package-level logger for nil-receiver diagnostics.
	// Defaults to NopLogger; consumers can override via SetPackageLogger.
	pkgLogger atomic.Value // stores log.Logger
)

func init() {
	pkgLogger.Store(log.Logger(&log.NopLogger{}))
}

// SetPackageLogger configures a package-level logger used for nil-receiver
// assertion diagnostics and telemetry reporting. This is typically called
// once during application bootstrap. If l is nil, a NopLogger is used.
func SetPackageLogger(l log.Logger) {
	if l == nil {
		l = &log.NopLogger{}
	}

	pkgLogger.Store(l)
}

func resolvePackageLogger() log.Logger {
	if v := pkgLogger.Load(); v != nil {
		if l, ok := v.(log.Logger); ok {
			return l
		}
	}

	return &log.NopLogger{}
}

// nilClientAssert fires a nil-receiver assertion and returns ErrNilClient.
func nilClientAssert(ctx context.Context, operation string) error {
	a := assert.New(ctx, resolvePackageLogger(), "redis.Client", operation)
	_ = a.Never(ctx, "nil receiver on *redis.Client")

	return ErrNilClient
}

// Config defines Redis client topology, auth, TLS, and connection settings.
type Config struct {
	Topology       Topology
	TLS            *TLSConfig
	Auth           Auth
	Options        ConnectionOptions
	Logger         log.Logger
	MetricsFactory *metrics.MetricsFactory
}

// Topology selects exactly one Redis deployment mode.
type Topology struct {
	Standalone *StandaloneTopology
	Sentinel   *SentinelTopology
	Cluster    *ClusterTopology
}

// StandaloneTopology configures single-node Redis access.
type StandaloneTopology struct {
	Address string
}

// SentinelTopology configures Redis Sentinel access.
type SentinelTopology struct {
	Addresses  []string
	MasterName string
}

// ClusterTopology configures Redis cluster access.
type ClusterTopology struct {
	Addresses []string
}

// TLSConfig configures TLS validation for Redis connections.
type TLSConfig struct {
	CACertBase64          string
	MinVersion            uint16
	AllowLegacyMinVersion bool
}

// Auth selects one Redis authentication strategy.
type Auth struct {
	StaticPassword *StaticPasswordAuth
	GCPIAM         *GCPIAMAuth
}

// StaticPasswordAuth authenticates using a static password.
type StaticPasswordAuth struct {
	Password string // #nosec G117 -- field is redacted via String() and GoString() methods
}

// String returns a redacted representation to prevent accidental credential logging.
func (StaticPasswordAuth) String() string { return "StaticPasswordAuth{Password:REDACTED}" }

// GoString returns a redacted representation for fmt %#v.
func (a StaticPasswordAuth) GoString() string { return a.String() }

// GCPIAMAuth authenticates with short-lived GCP IAM access tokens.
type GCPIAMAuth struct {
	CredentialsBase64       string
	ServiceAccount          string
	TokenLifetime           time.Duration
	RefreshEvery            time.Duration
	RefreshCheckInterval    time.Duration
	RefreshOperationTimeout time.Duration
}

// String returns a redacted representation to prevent accidental credential logging.
func (a GCPIAMAuth) String() string {
	return fmt.Sprintf("GCPIAMAuth{ServiceAccount:%s, CredentialsBase64:REDACTED}", a.ServiceAccount)
}

// GoString returns a redacted representation for fmt %#v.
func (a GCPIAMAuth) GoString() string { return a.String() }

// ConnectionOptions configures protocol, timeouts, pools, and retries.
type ConnectionOptions struct {
	DB              int
	Protocol        int
	PoolSize        int
	MinIdleConns    int
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	DialTimeout     time.Duration
	PoolTimeout     time.Duration
	MaxRetries      int
	MinRetryBackoff time.Duration
	MaxRetryBackoff time.Duration
}

// Status reports the last known client connectivity and IAM refresh loop health.
// Fields reflect cached state updated during connect/reconnect/refresh operations,
// not a live probe of the underlying connection. Use a Redis PING for liveness checks.
type Status struct {
	Connected          bool
	LastRefreshError   error
	LastRefreshAt      time.Time
	RefreshLoopRunning bool
}

// connectionFailuresMetric defines the counter for redis connection failures.
var connectionFailuresMetric = metrics.Metric{
	Name:        "redis_connection_failures_total",
	Unit:        "1",
	Description: "Total number of redis connection failures",
}

// reconnectionsMetric defines the counter for redis reconnection attempts.
var reconnectionsMetric = metrics.Metric{
	Name:        "redis_reconnections_total",
	Unit:        "1",
	Description: "Total number of redis reconnection attempts",
}

// Client wraps a redis.UniversalClient with reconnection and IAM token refresh logic.
type Client struct {
	mu             sync.RWMutex
	cfg            Config
	logger         log.Logger
	metricsFactory *metrics.MetricsFactory
	client         redis.UniversalClient
	connected      bool
	token          string
	lastRefresh    time.Time
	refreshErr     error

	refreshCancel      context.CancelFunc
	refreshLoopRunning bool
	refreshGeneration  uint64

	// Reconnect rate-limiting: prevents thundering-herd reconnect storms
	// when the server is down by enforcing exponential backoff between attempts.
	lastReconnectAttempt time.Time
	reconnectAttempts    int

	// test hooks
	tokenRetriever func(ctx context.Context) (string, error)
	reconnectFn    func(ctx context.Context) error
}

// New validates config, connects to Redis, and returns a ready client.
func New(ctx context.Context, cfg Config) (*Client, error) {
	normalized, err := normalizeConfig(cfg)
	if err != nil {
		return nil, err
	}

	c := &Client{
		cfg:            normalized,
		logger:         normalized.Logger,
		metricsFactory: normalized.MetricsFactory,
	}

	if err := c.Connect(ctx); err != nil {
		return nil, err
	}

	return c, nil
}

// Connect establishes a Redis connection using the current client configuration.
func (c *Client) Connect(ctx context.Context) error {
	if c == nil {
		return nilClientAssert(ctx, "Connect")
	}

	tracer := otel.Tracer("redis")

	ctx, span := tracer.Start(ctx, "redis.connect")
	defer span.End()

	span.SetAttributes(attribute.String(constant.AttrDBSystem, constant.DBSystemRedis))

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.logger == nil {
		c.logger = &log.NopLogger{}
	}

	if err := c.connectLocked(ctx); err != nil {
		c.recordConnectionFailure("connect")

		libOpentelemetry.HandleSpanError(span, "Failed to connect to redis", err)

		return err
	}

	return nil
}

// reconnectBackoffCap is the maximum delay between reconnect attempts.
const reconnectBackoffCap = 30 * time.Second

// GetClient returns a connected redis client, reconnecting on demand if needed.
func (c *Client) GetClient(ctx context.Context) (redis.UniversalClient, error) {
	if c == nil {
		return nil, nilClientAssert(ctx, "GetClient")
	}

	c.mu.RLock()

	if c.client != nil {
		client := c.client
		c.mu.RUnlock()

		return client, nil
	}

	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.logger == nil {
		c.logger = &log.NopLogger{}
	}

	if c.client != nil {
		return c.client, nil
	}

	// Rate-limit reconnect attempts: if we've failed recently, enforce a
	// minimum delay before the next attempt to avoid hammering the server.
	if c.reconnectAttempts > 0 {
		delay := min(backoff.ExponentialWithJitter(500*time.Millisecond, c.reconnectAttempts), reconnectBackoffCap)

		if elapsed := time.Since(c.lastReconnectAttempt); elapsed < delay {
			return nil, fmt.Errorf("redis reconnect: rate-limited (next attempt in %s)", delay-elapsed)
		}
	}

	c.lastReconnectAttempt = time.Now()

	// Only trace when actually reconnecting.
	tracer := otel.Tracer("redis")

	ctx, span := tracer.Start(ctx, "redis.reconnect")
	defer span.End()

	span.SetAttributes(attribute.String(constant.AttrDBSystem, constant.DBSystemRedis))

	if err := c.connectLocked(ctx); err != nil {
		c.reconnectAttempts++
		c.recordConnectionFailure("reconnect")
		c.recordReconnection("failure")

		libOpentelemetry.HandleSpanError(span, "Failed to reconnect redis", err)

		return nil, err
	}

	c.reconnectAttempts = 0
	c.recordReconnection("success")

	return c.client, nil
}

// Close stops background refresh and closes the underlying Redis client.
func (c *Client) Close() error {
	if c == nil {
		return nilClientAssert(context.Background(), "Close")
	}

	tracer := otel.Tracer("redis")

	_, span := tracer.Start(context.Background(), "redis.close")
	defer span.End()

	span.SetAttributes(attribute.String(constant.AttrDBSystem, constant.DBSystemRedis))

	c.mu.Lock()
	defer c.mu.Unlock()

	c.stopRefreshLoopLocked()

	if err := c.closeClientLocked(); err != nil {
		libOpentelemetry.HandleSpanError(span, "Failed to close redis client", err)

		return err
	}

	return nil
}

// Status returns a snapshot of the last known connectivity and token refresh state.
// The Connected field is updated during connect/reconnect/close operations and does
// not probe the server. For a live liveness check, issue a Redis PING via GetClient.
func (c *Client) Status() (Status, error) {
	if c == nil {
		return Status{}, nilClientAssert(context.Background(), "Status")
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	return Status{
		Connected:          c.connected,
		LastRefreshError:   c.refreshErr,
		LastRefreshAt:      c.lastRefresh,
		RefreshLoopRunning: c.refreshLoopRunning,
	}, nil
}

// IsConnected reports the last known connection state. It does not probe
// the server — the value is updated during connect/reconnect/close operations.
// For a live liveness check, issue a Redis PING via GetClient.
func (c *Client) IsConnected() (bool, error) {
	status, err := c.Status()
	if err != nil {
		return false, err
	}

	return status.Connected, nil
}

// LastRefreshError returns the latest IAM refresh/reconnect error.
func (c *Client) LastRefreshError() error {
	if c == nil {
		return nilClientAssert(context.Background(), "LastRefreshError")
	}

	status, err := c.Status()
	if err != nil {
		return err
	}

	return status.LastRefreshError
}

func (c *Client) connectLocked(ctx context.Context) error {
	// Config validation is performed by New/normalizeConfig at construction time.
	// Direct Connect() callers should only use properly-constructed Clients.
	c.logger.Log(ctx, log.LevelInfo, "connecting to Redis/Valkey")

	if c.usesGCPIAM() && c.token == "" {
		token, err := c.retrieveToken(ctx)
		if err != nil {
			c.logger.Log(ctx, log.LevelError, "initial token retrieval failed", log.Err(err))

			return fmt.Errorf("redis connect: token retrieval: %w", err)
		}

		c.token = token
	}

	// Create and verify the new client BEFORE touching the old one.
	// This follows the same create-ping-swap pattern used by reconnectLocked,
	// preventing a window where a healthy client is closed before its replacement
	// is confirmed working.
	if err := c.connectClientLocked(ctx); err != nil {
		return err
	}

	if c.usesGCPIAM() {
		c.lastRefresh = time.Now()
		c.startRefreshLoopLocked()
	}

	return nil
}

func (c *Client) connectClientLocked(ctx context.Context) error {
	opts, err := c.buildUniversalOptionsLocked()
	if err != nil {
		return fmt.Errorf("redis connect: build options: %w", err)
	}

	// Security policy: TLS enforcement in strict tier (production).
	tlsDisabled := c.cfg.TLS == nil
	if commons.CurrentTier() == commons.TierStrict {
		result := commons.CheckSecurityRule(commons.RuleTLSRequired, tlsDisabled)
		if err := commons.EnforceSecurityRule(ctx, c.logger, "redis", result); err != nil {
			return fmt.Errorf("redis connect: %w", err)
		}
	}

	rdb := redis.NewUniversalClient(opts)
	if _, err := rdb.Ping(ctx).Result(); err != nil {
		_ = rdb.Close()

		c.logger.Log(ctx, log.LevelError, "redis ping failed", log.Err(err))
		c.connected = false

		return fmt.Errorf("redis connect: ping: %w", err)
	}

	// New client verified. Close old client (if any) AFTER new one is confirmed healthy.
	oldClient := c.client

	c.client = rdb
	c.connected = true
	c.refreshErr = nil

	if oldClient != nil {
		if err := oldClient.Close(); err != nil {
			c.logger.Log(ctx, log.LevelWarn, "failed to close previous client after successful connect", log.Err(err))
		}
	}

	switch rdb.(type) {
	case *redis.ClusterClient:
		c.logger.Log(ctx, log.LevelInfo, "connected to Redis/Valkey in cluster mode")
	case *redis.Client:
		c.logger.Log(ctx, log.LevelInfo, "connected to Redis/Valkey in standalone mode")
	case *redis.Ring:
		c.logger.Log(ctx, log.LevelInfo, "connected to Redis/Valkey in ring mode")
	default:
		c.logger.Log(ctx, log.LevelWarn, "connected to Redis/Valkey in unknown mode")
	}

	if tlsDisabled {
		c.logger.Log(ctx, log.LevelWarn, "redis connection established without TLS; consider configuring TLS for production use")
	}

	return nil
}

func (c *Client) closeClientLocked() error {
	if c.client == nil {
		return nil
	}

	err := c.client.Close()
	c.client = nil
	c.connected = false

	return err
}

func (c *Client) buildUniversalOptionsLocked() (*redis.UniversalOptions, error) {
	o := c.cfg.Options
	opts := &redis.UniversalOptions{
		DB:              o.DB,
		Protocol:        o.Protocol,
		PoolSize:        o.PoolSize,
		MinIdleConns:    o.MinIdleConns,
		ReadTimeout:     o.ReadTimeout,
		WriteTimeout:    o.WriteTimeout,
		DialTimeout:     o.DialTimeout,
		PoolTimeout:     o.PoolTimeout,
		MaxRetries:      o.MaxRetries,
		MinRetryBackoff: o.MinRetryBackoff,
		MaxRetryBackoff: o.MaxRetryBackoff,
	}

	if c.cfg.Topology.Standalone != nil {
		opts.Addrs = []string{c.cfg.Topology.Standalone.Address}
	}

	if c.cfg.Topology.Sentinel != nil {
		opts.Addrs = c.cfg.Topology.Sentinel.Addresses
		opts.MasterName = c.cfg.Topology.Sentinel.MasterName
	}

	if c.cfg.Topology.Cluster != nil {
		opts.Addrs = c.cfg.Topology.Cluster.Addresses
	}

	// Guard against zero-value Config producing Addrs: nil, which causes
	// go-redis to silently default to localhost:6379. This can happen when
	// GetClient triggers a reconnect on a Client not created via New().
	if len(opts.Addrs) == 0 {
		return nil, configError("no topology configured: at least one address is required")
	}

	if c.cfg.Auth.StaticPassword != nil {
		opts.Password = c.cfg.Auth.StaticPassword.Password
	}

	if c.usesGCPIAM() {
		opts.Username = "default"
		opts.Password = c.token
	}

	if c.cfg.TLS != nil {
		tlsCfg, err := buildTLSConfig(*c.cfg.TLS)
		if err != nil {
			return nil, fmt.Errorf("redis: TLS config: %w", err)
		}

		opts.TLSConfig = tlsCfg
	}

	return opts, nil
}

func (c *Client) retrieveToken(ctx context.Context) (string, error) {
	if c == nil {
		return "", nilClientAssert(ctx, "retrieveToken")
	}

	if c.tokenRetriever != nil {
		return c.tokenRetriever(ctx)
	}

	auth := c.cfg.Auth.GCPIAM
	if auth == nil {
		return "", errors.New("GCP IAM auth is not configured")
	}

	credentialsJSON, err := base64.StdEncoding.DecodeString(auth.CredentialsBase64)
	if err != nil {
		c.logger.Log(ctx, log.LevelError, "failed to decode base64 credentials", log.Err(err))

		return "", fmt.Errorf("redis: generate IAM token: %w", err)
	}

	// Defense-in-depth: zero decoded credentials when done to reduce memory exposure window.
	defer func() {
		for i := range credentialsJSON {
			credentialsJSON[i] = 0
		}
	}()

	creds, err := google.CredentialsFromJSONWithType(ctx, credentialsJSON, google.ServiceAccount)
	if err != nil {
		// Wrap error to prevent potential credential fragments in the original error message
		// from leaking into logs or upstream callers.
		return "", fmt.Errorf("parsing credentials JSON failed (content redacted): %w",
			errors.New("invalid service account credentials format"))
	}

	client, err := iamcredentials.NewIamCredentialsClient(ctx, option.WithCredentials(creds))
	if err != nil {
		return "", fmt.Errorf("creating IAM credentials client: %w", err)
	}
	defer client.Close()

	resp, err := client.GenerateAccessToken(ctx, &iamcredentialspb.GenerateAccessTokenRequest{
		Name:     gcpServiceAccountPrefix + auth.ServiceAccount,
		Scope:    []string{gcpScope},
		Lifetime: durationpb.New(auth.TokenLifetime),
	})
	if err != nil {
		return "", fmt.Errorf("problem generating access token: %w", err)
	}

	if resp == nil {
		return "", errors.New("generate access token returned nil response")
	}

	return resp.AccessToken, nil
}

func (c *Client) refreshTokenLoop(ctx context.Context) {
	if c == nil {
		return
	}

	auth := c.cfg.Auth.GCPIAM
	if auth == nil {
		// Should never happen in production (startRefreshLoopLocked checks usesGCPIAM()),
		// but guard defensively against direct invocations.
		return
	}

	ticker := time.NewTicker(auth.RefreshCheckInterval)
	defer ticker.Stop()

	var consecutiveFailures int

	for {
		select {
		case <-ticker.C:
			if c.refreshTick(ctx, auth) {
				consecutiveFailures = 0

				continue
			}

			// On failure, apply exponential backoff before the next attempt.
			// The ticker continues to fire, but we wait an additional delay
			// proportional to the number of consecutive failures. The base
			// derives from the configured check interval so that test configs
			// with sub-millisecond intervals produce proportionally small delays.
			consecutiveFailures++

			delay := min(backoff.ExponentialWithJitter(auth.RefreshCheckInterval, consecutiveFailures), reconnectBackoffCap)

			if err := backoff.WaitContext(ctx, delay); err != nil {
				return
			}

		case <-ctx.Done():
			return
		}
	}
}

// refreshTick handles a single tick of the IAM token refresh cycle.
// Returns true if the tick completed successfully (including when no refresh
// was needed), false if a token retrieval or reconnect failed.
func (c *Client) refreshTick(ctx context.Context, auth *GCPIAMAuth) bool {
	c.mu.RLock()
	lastRefresh := c.lastRefresh
	c.mu.RUnlock()

	if !time.Now().After(lastRefresh.Add(auth.RefreshEvery)) {
		return true
	}

	tracer := otel.Tracer("redis")

	refreshCtx, cancel := context.WithTimeout(ctx, auth.RefreshOperationTimeout)
	defer cancel()

	refreshCtx, span := tracer.Start(refreshCtx, "redis.iam_refresh")
	defer span.End()

	span.SetAttributes(attribute.String(constant.AttrDBSystem, constant.DBSystemRedis))

	token, err := c.retrieveToken(refreshCtx)
	if err != nil {
		c.mu.Lock()
		c.refreshErr = err
		c.logger.Log(refreshCtx, log.LevelWarn, "IAM token refresh failed", log.Err(err))
		c.mu.Unlock()

		libOpentelemetry.HandleSpanError(span, "IAM token refresh failed", err)

		return false
	}

	return c.applyTokenAndReconnect(refreshCtx, token)
}

// applyTokenAndReconnect sets the new token and reconnects the client.
// On reconnect failure, the old token is restored to keep the existing client usable.
func (c *Client) applyTokenAndReconnect(ctx context.Context, token string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	oldToken := c.token
	c.token = token

	reconnectFn := c.reconnectFn
	if reconnectFn == nil {
		reconnectFn = c.reconnectLocked
	}

	if err := reconnectFn(ctx); err != nil {
		c.refreshErr = err
		// Restore old token: reconnect failed, so the new token is useless
		// and the old client (if any) is still using the previous token.
		c.token = oldToken
		c.logger.Log(ctx, log.LevelError, "failed to reconnect after IAM token refresh, keeping existing client", log.Err(err))

		return false
	}

	c.lastRefresh = time.Now()
	c.refreshErr = nil
	c.logger.Log(ctx, log.LevelInfo, "IAM token refreshed")

	return true
}

func (c *Client) reconnectLocked(ctx context.Context) error {
	// Build new client options with the refreshed token.
	opts, err := c.buildUniversalOptionsLocked()
	if err != nil {
		c.logger.Log(ctx, log.LevelError, "failed to build options for reconnect", log.Err(err))

		return err
	}

	// Create and verify the new client BEFORE touching the old one.
	newClient := redis.NewUniversalClient(opts)

	if _, err := newClient.Ping(ctx).Result(); err != nil {
		_ = newClient.Close()

		c.logger.Log(ctx, log.LevelError, "new client ping failed during reconnect, keeping existing client", log.Err(err))

		return err
	}

	// New client is verified. Swap atomically: close old, assign new.
	oldClient := c.client

	c.client = newClient
	c.connected = true
	c.refreshErr = nil

	if oldClient != nil {
		if err := oldClient.Close(); err != nil {
			c.logger.Log(ctx, log.LevelWarn, "failed to close previous client after successful reconnect", log.Err(err))
		}
	}

	return nil
}

func (c *Client) startRefreshLoopLocked() {
	if !c.usesGCPIAM() || c.refreshLoopRunning {
		return
	}

	refreshCtx, cancel := context.WithCancel(context.Background())
	c.refreshGeneration++
	generation := c.refreshGeneration
	c.refreshCancel = cancel
	c.refreshLoopRunning = true

	runtime.SafeGoWithContextAndComponent(
		refreshCtx,
		c.logger,
		"redis",
		"iam_refresh_loop",
		runtime.KeepRunning,
		func(_ context.Context) {
			c.refreshTokenLoop(refreshCtx)

			c.mu.Lock()
			defer c.mu.Unlock()

			if c.refreshGeneration == generation {
				c.refreshCancel = nil
				c.refreshLoopRunning = false
			}
		},
	)
}

func (c *Client) stopRefreshLoopLocked() {
	if c.refreshCancel != nil {
		c.refreshCancel()
		c.refreshCancel = nil
	}

	c.refreshLoopRunning = false
}

func (c *Client) usesGCPIAM() bool {
	return c.cfg.Auth.GCPIAM != nil
}

func normalizeConfig(cfg Config) (Config, error) {
	normalizeLoggerDefault(&cfg)
	normalizeConnectionOptionsDefaults(&cfg.Options)

	originalTLSMinVersion := uint16(0)
	if cfg.TLS != nil {
		originalTLSMinVersion = cfg.TLS.MinVersion
	}

	tlsMinVersionUpgraded, legacyTLSAllowed := normalizeTLSDefaults(cfg.TLS)
	normalizeGCPIAMDefaults(cfg.Auth.GCPIAM)

	if tlsMinVersionUpgraded {
		if originalTLSMinVersion == 0 {
			cfg.Logger.Log(
				context.Background(),
				log.LevelInfo,
				"redis TLS MinVersion was not set and has been defaulted to tls.VersionTLS12",
			)
		} else {
			cfg.Logger.Log(
				context.Background(),
				log.LevelWarn,
				"redis TLS MinVersion was below TLS1.2 and has been upgraded to tls.VersionTLS12",
			)
		}
	}

	if legacyTLSAllowed {
		cfg.Logger.Log(
			context.Background(),
			log.LevelWarn,
			"redis TLS MinVersion below TLS1.2 retained because AllowLegacyMinVersion=true; this is insecure and should be temporary",
		)
	}

	if err := validateConfig(cfg); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func normalizeLoggerDefault(cfg *Config) {
	if cfg.Logger == nil {
		cfg.Logger = &log.NopLogger{}
	}
}

const (
	maxPoolSize = 1000
)

func normalizeConnectionOptionsDefaults(options *ConnectionOptions) {
	if options.PoolSize == 0 {
		options.PoolSize = 10
	}

	if options.PoolSize > maxPoolSize {
		options.PoolSize = maxPoolSize
	}

	if options.ReadTimeout == 0 {
		options.ReadTimeout = 3 * time.Second
	}

	if options.WriteTimeout == 0 {
		options.WriteTimeout = 3 * time.Second
	}

	if options.DialTimeout == 0 {
		options.DialTimeout = 5 * time.Second
	}

	if options.PoolTimeout == 0 {
		options.PoolTimeout = 2 * time.Second
	}

	if options.MaxRetries == 0 {
		options.MaxRetries = 3
	}

	if options.MinRetryBackoff == 0 {
		options.MinRetryBackoff = 8 * time.Millisecond
	}

	if options.MaxRetryBackoff == 0 {
		options.MaxRetryBackoff = 1 * time.Second
	}
}

// normalizeTLSDefaults enforces a TLS 1.2 minimum floor. Versions below TLS 1.2
// (including TLS 1.0 and 1.1) have known vulnerabilities and are rejected by
// most compliance frameworks. If MinVersion is unset, it is upgraded. If
// MinVersion is below tls.VersionTLS12, it is upgraded unless
// AllowLegacyMinVersion is set explicitly.
//
// Returns (upgraded, legacyAllowed).
func normalizeTLSDefaults(tlsCfg *TLSConfig) (bool, bool) {
	if tlsCfg == nil {
		return false, false
	}

	if tlsCfg.MinVersion == 0 {
		tlsCfg.MinVersion = tls.VersionTLS12

		return true, false
	}

	if tlsCfg.MinVersion < tls.VersionTLS12 {
		if tlsCfg.AllowLegacyMinVersion {
			return false, true
		}

		tlsCfg.MinVersion = tls.VersionTLS12

		return true, false
	}

	return false, false
}

func normalizeGCPIAMDefaults(auth *GCPIAMAuth) {
	if auth == nil {
		return
	}

	if auth.TokenLifetime == 0 {
		auth.TokenLifetime = defaultTokenLifetime
	}

	if auth.RefreshEvery == 0 {
		auth.RefreshEvery = defaultRefreshEvery
	}

	if auth.RefreshCheckInterval == 0 {
		auth.RefreshCheckInterval = defaultRefreshCheckInterval
	}

	if auth.RefreshOperationTimeout == 0 {
		auth.RefreshOperationTimeout = defaultRefreshOperationTimeout
	}
}

func validateConfig(cfg Config) error {
	if err := validateTopology(cfg.Topology); err != nil {
		return err
	}

	if cfg.Auth.StaticPassword != nil && cfg.Auth.GCPIAM != nil {
		return configError("only one auth strategy can be configured")
	}

	if cfg.TLS != nil && strings.TrimSpace(cfg.TLS.CACertBase64) == "" {
		return configError("TLS CA cert is required when TLS is configured")
	}

	if cfg.Auth.GCPIAM == nil {
		return nil
	}

	if cfg.TLS == nil {
		return configError("TLS must be configured when GCP IAM auth is enabled")
	}

	if strings.TrimSpace(cfg.Auth.GCPIAM.ServiceAccount) == "" {
		return configError("service account is required for GCP IAM auth")
	}

	if strings.Contains(cfg.Auth.GCPIAM.ServiceAccount, "/") {
		return configError("service account cannot contain '/' characters")
	}

	if strings.TrimSpace(cfg.Auth.GCPIAM.CredentialsBase64) == "" {
		return configError("credentials are required for GCP IAM auth")
	}

	if cfg.Auth.GCPIAM.RefreshEvery >= cfg.Auth.GCPIAM.TokenLifetime {
		return configError("RefreshEvery must be less than TokenLifetime to prevent token expiry before refresh")
	}

	return nil
}

func validateTopology(topology Topology) error {
	count := 0

	if topology.Standalone != nil {
		count++

		if strings.TrimSpace(topology.Standalone.Address) == "" {
			return configError("standalone address is required")
		}
	}

	if topology.Sentinel != nil {
		count++

		if len(topology.Sentinel.Addresses) == 0 {
			return configError("sentinel addresses are required")
		}

		if strings.TrimSpace(topology.Sentinel.MasterName) == "" {
			return configError("sentinel master name is required")
		}

		for _, address := range topology.Sentinel.Addresses {
			if strings.TrimSpace(address) == "" {
				return configError("sentinel addresses cannot be empty")
			}
		}
	}

	if topology.Cluster != nil {
		count++

		if len(topology.Cluster.Addresses) == 0 {
			return configError("cluster addresses are required")
		}

		for _, address := range topology.Cluster.Addresses {
			if strings.TrimSpace(address) == "" {
				return configError("cluster addresses cannot be empty")
			}
		}
	}

	if count != 1 {
		return configError("exactly one topology must be configured")
	}

	return nil
}

func buildTLSConfig(cfg TLSConfig) (*tls.Config, error) {
	caCert, err := base64.StdEncoding.DecodeString(cfg.CACertBase64)
	if err != nil {
		return nil, err
	}

	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caCert) {
		return nil, errors.New("adding CA cert failed")
	}

	// Enforce a TLS 1.2 floor. normalizeTLSDefaults already applies this
	// floor in normal flows, but a caller using AllowLegacyMinVersion=true
	// could still set a lower value. The literal tls.VersionTLS12 default
	// satisfies gosec G402 static analysis; we override only when the caller
	// requests a *higher* version.
	minVersion := max(uint16(tls.VersionTLS12), cfg.MinVersion)

	tlsConfig := &tls.Config{ // #nosec G402 -- minVersion is floored to tls.VersionTLS12 above; gosec cannot trace through local variables
		RootCAs:    caCertPool,
		MinVersion: minVersion,
	}

	return tlsConfig, nil
}

// recordConnectionFailure increments the redis connection failure counter.
// No-op when metricsFactory is nil.
func (c *Client) recordConnectionFailure(operation string) {
	if c.metricsFactory == nil {
		return
	}

	counter, err := c.metricsFactory.Counter(connectionFailuresMetric)
	if err != nil {
		c.logger.Log(context.Background(), log.LevelWarn, "failed to create redis metric counter", log.Err(err))
		return
	}

	err = counter.
		WithLabels(map[string]string{
			"operation": constant.SanitizeMetricLabel(operation),
		}).
		AddOne(context.Background())
	if err != nil {
		c.logger.Log(context.Background(), log.LevelWarn, "failed to record redis metric", log.Err(err))
	}
}

// recordReconnection increments the redis reconnection counter.
// No-op when metricsFactory is nil.
func (c *Client) recordReconnection(result string) {
	if c.metricsFactory == nil {
		return
	}

	counter, err := c.metricsFactory.Counter(reconnectionsMetric)
	if err != nil {
		c.logger.Log(context.Background(), log.LevelWarn, "failed to create redis reconnection metric counter", log.Err(err))
		return
	}

	err = counter.
		WithLabels(map[string]string{
			"result": result,
		}).
		AddOne(context.Background())
	if err != nil {
		c.logger.Log(context.Background(), log.LevelWarn, "failed to record redis reconnection metric", log.Err(err))
	}
}

func configError(msg string) error {
	return fmt.Errorf("%w: %s", ErrInvalidConfig, msg)
}
