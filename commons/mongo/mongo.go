package mongo

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	neturl "net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	commons "github.com/LerianStudio/lib-commons/v5/commons"
	"github.com/LerianStudio/lib-commons/v5/commons/backoff"
	constant "github.com/LerianStudio/lib-commons/v5/commons/constants"
	"github.com/LerianStudio/lib-commons/v5/commons/internal/nilcheck"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v5/commons/opentelemetry"
	"github.com/LerianStudio/lib-observability/assert"
	"github.com/LerianStudio/lib-observability/log"
	"github.com/LerianStudio/lib-observability/metrics"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

const (
	defaultServerSelectionTimeout = 5 * time.Second
	defaultHeartbeatInterval      = 10 * time.Second
	maxMaxPoolSize                = 1000
)

var (
	// ErrNilContext is returned when a required context is nil.
	ErrNilContext = errors.New("context cannot be nil")
	// ErrNilClient is returned when a *Client receiver is nil.
	ErrNilClient = errors.New("mongo client is nil")
	// ErrClientClosed is returned when the client is not connected.
	ErrClientClosed = errors.New("mongo client is closed")
	// ErrNilDependency is returned when an Option sets a required dependency to nil.
	ErrNilDependency = errors.New("mongo option set a required dependency to nil")
	// ErrInvalidConfig indicates the provided configuration is invalid.
	ErrInvalidConfig = errors.New("invalid mongo config")
	// ErrEmptyURI is returned when Mongo URI is empty.
	ErrEmptyURI = errors.New("mongo uri cannot be empty")
	// ErrEmptyDatabaseName is returned when database name is empty.
	ErrEmptyDatabaseName = errors.New("database name cannot be empty")
	// ErrEmptyCollectionName is returned when collection name is empty.
	ErrEmptyCollectionName = errors.New("collection name cannot be empty")
	// ErrEmptyIndexes is returned when no index model is provided.
	ErrEmptyIndexes = errors.New("at least one index must be provided")
	// ErrConnect wraps connection establishment failures.
	ErrConnect = errors.New("mongo connect failed")
	// ErrPing wraps connectivity probe failures.
	ErrPing = errors.New("mongo ping failed")
	// ErrDisconnect wraps disconnection failures.
	ErrDisconnect = errors.New("mongo disconnect failed")
	// ErrCreateIndex wraps index creation failures.
	ErrCreateIndex = errors.New("mongo create index failed")
	// ErrNilMongoClient is returned when mongo driver returns a nil client.
	ErrNilMongoClient = errors.New("mongo driver returned nil client")
)

// nilClientAssert fires a telemetry assertion for nil-receiver calls and returns ErrNilClient.
func nilClientAssert(operation string) error {
	asserter := assert.New(context.Background(), nil, "mongo", operation)
	_ = asserter.Never(context.Background(), "mongo client receiver is nil")

	return ErrNilClient
}

// TLSConfig configures TLS validation for MongoDB connections.
type TLSConfig struct {
	CACertBase64 string
	MinVersion   uint16
}

// Config defines MongoDB connection and pool behavior.
type Config struct {
	URI                    string
	Database               string
	MaxPoolSize            uint64
	ServerSelectionTimeout time.Duration
	HeartbeatInterval      time.Duration
	TLS                    *TLSConfig
	Logger                 log.Logger
	MetricsFactory         *metrics.MetricsFactory
}

func (cfg Config) validate() error {
	if strings.TrimSpace(cfg.URI) == "" {
		return ErrEmptyURI
	}

	if strings.TrimSpace(cfg.Database) == "" {
		return ErrEmptyDatabaseName
	}

	return nil
}

// Option customizes internal client dependencies (primarily for tests).
type Option func(*clientDeps)

// connectBackoffCap is the maximum delay between lazy-connect retries.
const connectBackoffCap = 30 * time.Second

// connectionFailuresMetric defines the counter for mongo connection failures.
var connectionFailuresMetric = metrics.Metric{
	Name:        "mongo_connection_failures_total",
	Unit:        "1",
	Description: "Total number of mongo connection failures",
}

// Client wraps a MongoDB client with lifecycle and index helpers.
type Client struct {
	mu             sync.RWMutex
	client         *mongo.Client
	closed         bool // terminal flag; set by Close(), prevents reconnection
	databaseName   string
	cfg            Config
	metricsFactory *metrics.MetricsFactory
	uri            string // private copy for reconnection; cfg.URI cleared after connect
	deps           clientDeps

	// Lazy-connect rate-limiting: prevents thundering-herd reconnect storms
	// when the database is down by enforcing exponential backoff between attempts.
	lastConnectAttempt time.Time
	connectAttempts    int
}

type clientDeps struct {
	connect     func(context.Context, *options.ClientOptions) (*mongo.Client, error)
	ping        func(context.Context, *mongo.Client) error
	disconnect  func(context.Context, *mongo.Client) error
	createIndex func(context.Context, *mongo.Client, string, string, mongo.IndexModel) error
}

func defaultDeps() clientDeps {
	return clientDeps{
		connect: func(ctx context.Context, clientOptions *options.ClientOptions) (*mongo.Client, error) {
			return mongo.Connect(ctx, clientOptions)
		},
		ping: func(ctx context.Context, client *mongo.Client) error {
			return client.Ping(ctx, nil)
		},
		disconnect: func(ctx context.Context, client *mongo.Client) error {
			return client.Disconnect(ctx)
		},
		createIndex: func(ctx context.Context, client *mongo.Client, database, collection string, index mongo.IndexModel) error {
			_, err := client.Database(database).Collection(collection).Indexes().CreateOne(ctx, index)

			return err
		},
	}
}

// NewClient validates config, connects to MongoDB, and returns a ready client.
func NewClient(ctx context.Context, cfg Config, opts ...Option) (*Client, error) {
	if ctx == nil {
		return nil, ErrNilContext
	}

	cfg = normalizeConfig(cfg)

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	deps := defaultDeps()

	for _, opt := range opts {
		if opt == nil {
			asserter := assert.New(ctx, cfg.Logger, "mongo", "NewClient")
			_ = asserter.Never(ctx, "nil mongo option received; skipping")

			continue
		}

		opt(&deps)
	}

	if deps.connect == nil || deps.ping == nil || deps.disconnect == nil || deps.createIndex == nil {
		return nil, ErrNilDependency
	}

	client := &Client{
		databaseName:   cfg.Database,
		cfg:            cfg,
		metricsFactory: cfg.MetricsFactory,
		uri:            cfg.URI,
		deps:           deps,
	}

	if err := client.Connect(ctx); err != nil {
		return nil, err
	}

	return client, nil
}

// Connect establishes a MongoDB connection if one is not already open.
func (c *Client) Connect(ctx context.Context) error {
	if c == nil {
		return nilClientAssert("connect")
	}

	if ctx == nil {
		return ErrNilContext
	}

	tracer := otel.Tracer("mongo")

	ctx, span := tracer.Start(ctx, "mongo.connect")
	defer span.End()

	span.SetAttributes(attribute.String(constant.AttrDBSystem, constant.DBSystemMongoDB))

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return ErrClientClosed
	}

	if c.client != nil {
		return nil
	}

	if err := c.connectLocked(ctx); err != nil {
		c.recordConnectionFailure("connect")

		libOpentelemetry.HandleSpanError(span, "Failed to connect to mongo", err)

		return err
	}

	return nil
}

// connectLocked performs the actual connection logic.
// The caller MUST hold c.mu (write lock) before calling this method.
func (c *Client) connectLocked(ctx context.Context) error {
	clientOptions := options.Client().ApplyURI(c.uri)

	serverSelectionTimeout := c.cfg.ServerSelectionTimeout
	if serverSelectionTimeout <= 0 {
		serverSelectionTimeout = defaultServerSelectionTimeout
	}

	heartbeatInterval := c.cfg.HeartbeatInterval
	if heartbeatInterval <= 0 {
		heartbeatInterval = defaultHeartbeatInterval
	}

	clientOptions.SetServerSelectionTimeout(serverSelectionTimeout)
	clientOptions.SetHeartbeatInterval(heartbeatInterval)

	if c.cfg.MaxPoolSize > 0 {
		clientOptions.SetMaxPoolSize(c.cfg.MaxPoolSize)
	}

	if c.cfg.TLS != nil {
		tlsCfg, err := buildTLSConfig(*c.cfg.TLS)
		if err != nil {
			return fmt.Errorf("%w: TLS configuration: %w", ErrInvalidConfig, err)
		}

		clientOptions.SetTLSConfig(tlsCfg)
	}

	// Security policy: TLS enforcement in strict tier (production).
	// Check BEFORE connecting to avoid sending credentials over plaintext.
	tlsDisabled := c.cfg.TLS == nil && !isTLSImplied(c.uri)
	if commons.CurrentTier() == commons.TierStrict {
		result := commons.CheckSecurityRule(commons.RuleTLSRequired, tlsDisabled)
		if err := commons.EnforceSecurityRule(ctx, c.cfg.Logger, "mongo", result); err != nil {
			return fmt.Errorf("mongo connect: %w", err)
		}
	}

	mongoClient, err := c.deps.connect(ctx, clientOptions)
	if err != nil {
		sanitized := sanitizeDriverError(err)
		c.log(ctx, "mongo connect failed", log.Err(sanitized))

		return fmt.Errorf("%w: %w", ErrConnect, sanitized)
	}

	if mongoClient == nil {
		return ErrNilMongoClient
	}

	if err := c.deps.ping(ctx, mongoClient); err != nil {
		if disconnectErr := c.deps.disconnect(ctx, mongoClient); disconnectErr != nil {
			c.log(ctx, "failed to disconnect after ping failure", log.Err(sanitizeDriverError(disconnectErr)))
		}

		sanitized := sanitizeDriverError(err)
		c.log(ctx, "mongo ping failed", log.Err(sanitized))

		return fmt.Errorf("%w: %w", ErrPing, sanitized)
	}

	c.client = mongoClient

	if tlsDisabled {
		c.logAtLevel(ctx, log.LevelWarn, "mongo connection established without TLS; "+
			"consider configuring TLS for production use")
	}

	c.cfg.URI = ""

	return nil
}

// Client returns the underlying mongo client if connected.
//
// Note: the returned *mongo.Client may become stale if Close is called
// concurrently from another goroutine. Callers that need atomicity
// across multiple operations should coordinate externally.
func (c *Client) Client(ctx context.Context) (*mongo.Client, error) {
	if c == nil {
		return nil, nilClientAssert("client")
	}

	if ctx == nil {
		return nil, ErrNilContext
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	if c.client == nil {
		return nil, ErrClientClosed
	}

	return c.client, nil
}

// ResolveClient returns a connected mongo client, reconnecting lazily if needed.
// Unlike Client(), this method attempts to re-establish a dropped connection using
// double-checked locking with backoff rate-limiting to prevent reconnect storms.
func (c *Client) ResolveClient(ctx context.Context) (*mongo.Client, error) {
	if c == nil {
		return nil, nilClientAssert("resolve_client")
	}

	if ctx == nil {
		return nil, ErrNilContext
	}

	// Fast path: already connected (read-lock only).
	c.mu.RLock()
	closed := c.closed
	client := c.client
	c.mu.RUnlock()

	if closed {
		return nil, ErrClientClosed
	}

	if client != nil {
		return client, nil
	}

	// Slow path: acquire write lock and double-check before connecting.
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil, ErrClientClosed
	}

	if c.client != nil {
		return c.client, nil
	}

	// Rate-limit lazy-connect retries: if previous attempts failed recently,
	// enforce a minimum delay before the next attempt to prevent reconnect storms.
	if c.connectAttempts > 0 {
		delay := min(backoff.ExponentialWithJitter(1*time.Second, c.connectAttempts), connectBackoffCap)

		if elapsed := time.Since(c.lastConnectAttempt); elapsed < delay {
			return nil, fmt.Errorf("mongo resolve_client: rate-limited (next attempt in %s)", delay-elapsed)
		}
	}

	c.lastConnectAttempt = time.Now()

	tracer := otel.Tracer("mongo")

	ctx, span := tracer.Start(ctx, "mongo.resolve")
	defer span.End()

	span.SetAttributes(attribute.String(constant.AttrDBSystem, constant.DBSystemMongoDB))

	if err := c.connectLocked(ctx); err != nil {
		c.connectAttempts++
		c.recordConnectionFailure("resolve")

		libOpentelemetry.HandleSpanError(span, "Failed to resolve mongo connection", err)

		return nil, err
	}

	c.connectAttempts = 0

	if c.client == nil {
		err := ErrClientClosed
		libOpentelemetry.HandleSpanError(span, "Mongo client not connected after resolve", err)

		return nil, err
	}

	return c.client, nil
}

// DatabaseName returns the configured database name.
func (c *Client) DatabaseName() (string, error) {
	if c == nil {
		return "", nilClientAssert("database_name")
	}

	c.mu.RLock()
	defer c.mu.RUnlock()

	return c.databaseName, nil
}

// Database returns the configured mongo database handle.
//
// Note: the returned *mongo.Database may become stale if Close is called
// concurrently from another goroutine. Callers that need atomicity
// across multiple operations should coordinate externally.
func (c *Client) Database(ctx context.Context) (*mongo.Database, error) {
	client, err := c.Client(ctx)
	if err != nil {
		return nil, err
	}

	databaseName, err := c.DatabaseName()
	if err != nil {
		return nil, err
	}

	return client.Database(databaseName), nil
}

// Ping checks MongoDB availability using the active connection.
func (c *Client) Ping(ctx context.Context) error {
	if c == nil {
		return nilClientAssert("ping")
	}

	if ctx == nil {
		return ErrNilContext
	}

	tracer := otel.Tracer("mongo")

	ctx, span := tracer.Start(ctx, "mongo.ping")
	defer span.End()

	span.SetAttributes(attribute.String(constant.AttrDBSystem, constant.DBSystemMongoDB))

	client, err := c.Client(ctx)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "Failed to get mongo client for ping", err)

		return err
	}

	if err := c.deps.ping(ctx, client); err != nil {
		sanitized := sanitizeDriverError(err)
		pingErr := fmt.Errorf("%w: %w", ErrPing, sanitized)
		libOpentelemetry.HandleSpanError(span, "Mongo ping failed", pingErr)

		return pingErr
	}

	return nil
}

// Close releases the MongoDB connection.
// The client is marked as closed regardless of whether disconnect succeeds or fails.
// This prevents callers from retrying operations on a potentially half-closed client.
func (c *Client) Close(ctx context.Context) error {
	if c == nil {
		return nilClientAssert("close")
	}

	if ctx == nil {
		return ErrNilContext
	}

	tracer := otel.Tracer("mongo")

	ctx, span := tracer.Start(ctx, "mongo.close")
	defer span.End()

	span.SetAttributes(attribute.String(constant.AttrDBSystem, constant.DBSystemMongoDB))

	c.mu.Lock()
	defer c.mu.Unlock()

	c.closed = true

	if c.client == nil {
		return nil
	}

	err := c.deps.disconnect(ctx, c.client)
	c.client = nil

	if err != nil {
		sanitized := sanitizeDriverError(err)
		c.log(ctx, "mongo disconnect failed", log.Err(sanitized))

		disconnectErr := fmt.Errorf("%w: %w", ErrDisconnect, sanitized)
		libOpentelemetry.HandleSpanError(span, "Failed to disconnect from mongo", disconnectErr)

		return disconnectErr
	}

	return nil
}

// EnsureIndexes creates indexes for a collection if they do not already exist.
func (c *Client) EnsureIndexes(ctx context.Context, collection string, indexes ...mongo.IndexModel) error {
	if c == nil {
		return nilClientAssert("ensure_indexes")
	}

	if ctx == nil {
		return ErrNilContext
	}

	if strings.TrimSpace(collection) == "" {
		return ErrEmptyCollectionName
	}

	if len(indexes) == 0 {
		return ErrEmptyIndexes
	}

	tracer := otel.Tracer("mongo")

	ctx, span := tracer.Start(ctx, "mongo.ensure_indexes")
	defer span.End()

	span.SetAttributes(
		attribute.String(constant.AttrDBSystem, constant.DBSystemMongoDB),
		attribute.String(constant.AttrDBMongoDBCollection, collection),
	)

	client, err := c.Client(ctx)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "Failed to get mongo client for ensure indexes", err)

		return err
	}

	databaseName, err := c.DatabaseName()
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "Failed to get database name for ensure indexes", err)

		return err
	}

	var indexErrors []error

	for _, index := range indexes {
		if err := ctx.Err(); err != nil {
			indexErrors = append(indexErrors, fmt.Errorf("%w: context cancelled: %w", ErrCreateIndex, err))

			break
		}

		fields := indexKeysString(index.Keys)

		if fields == "<unknown>" {
			c.logAtLevel(ctx, log.LevelWarn, "unrecognized index key type; expected bson.D or bson.M",
				log.String("collection", collection))
		}

		c.log(ctx, "ensuring mongo index", log.String("collection", collection), log.String("fields", fields))

		if err := c.deps.createIndex(ctx, client, databaseName, collection, index); err != nil {
			c.logAtLevel(ctx, log.LevelWarn, "failed to create mongo index",
				log.String("collection", collection),
				log.String("fields", fields),
				log.Err(err),
			)

			indexErrors = append(indexErrors, fmt.Errorf("%w: collection=%s fields=%s: %w", ErrCreateIndex, collection, fields, err))
		}
	}

	if len(indexErrors) > 0 {
		joinedErr := errors.Join(indexErrors...)
		libOpentelemetry.HandleSpanError(span, "Failed to ensure some mongo indexes", joinedErr)

		return joinedErr
	}

	return nil
}

func (c *Client) log(ctx context.Context, message string, fields ...log.Field) {
	c.logAtLevel(ctx, log.LevelDebug, message, fields...)
}

func (c *Client) logAtLevel(ctx context.Context, level log.Level, message string, fields ...log.Field) {
	if c == nil || c.cfg.Logger == nil {
		return
	}

	if !c.cfg.Logger.Enabled(level) {
		return
	}

	c.cfg.Logger.Log(ctx, level, message, fields...)
}

// normalizeConfig applies safe defaults, trims whitespace, and clamps to a Config.
func normalizeConfig(cfg Config) Config {
	cfg.URI = strings.TrimSpace(cfg.URI)
	cfg.Database = strings.TrimSpace(cfg.Database)

	if nilcheck.Interface(cfg.Logger) {
		cfg.Logger = log.NewNop()
	}

	if cfg.MaxPoolSize > maxMaxPoolSize {
		cfg.MaxPoolSize = maxMaxPoolSize
	}

	if cfg.TLS != nil {
		tlsCopy := *cfg.TLS
		tlsCopy.CACertBase64 = strings.TrimSpace(tlsCopy.CACertBase64)
		cfg.TLS = &tlsCopy
	}

	normalizeTLSDefaults(cfg.TLS)

	return cfg
}

// normalizeTLSDefaults sets MinVersion to TLS 1.2 when unspecified (zero).
// Explicit versions are preserved so downstream validation in buildTLSConfig
// can reject disallowed values rather than silently overwriting them.
func normalizeTLSDefaults(tlsCfg *TLSConfig) {
	if tlsCfg == nil {
		return
	}

	if tlsCfg.MinVersion == 0 {
		tlsCfg.MinVersion = tls.VersionTLS12
	}
}

// buildTLSConfig creates a *tls.Config from a TLSConfig.
// When CACertBase64 is provided, it is decoded and used as the root CA pool.
// When CACertBase64 is empty, the system root CA pool is used (RootCAs = nil).
// MinVersion defaults to TLS 1.2. If cfg.MinVersion is set, it must be
// tls.VersionTLS12 or tls.VersionTLS13; any other value returns ErrInvalidConfig.
func buildTLSConfig(cfg TLSConfig) (*tls.Config, error) {
	if cfg.MinVersion != 0 && cfg.MinVersion != tls.VersionTLS12 && cfg.MinVersion != tls.VersionTLS13 {
		return nil, fmt.Errorf("%w: unsupported TLS MinVersion %#x (must be tls.VersionTLS12 or tls.VersionTLS13)", ErrInvalidConfig, cfg.MinVersion)
	}

	tlsConfig := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	if cfg.MinVersion == tls.VersionTLS13 {
		tlsConfig.MinVersion = tls.VersionTLS13
	}

	// When CACertBase64 is provided, build a custom root CA pool.
	// When empty, RootCAs remains nil and Go uses the system root CA pool.
	if strings.TrimSpace(cfg.CACertBase64) != "" {
		caCert, err := base64.StdEncoding.DecodeString(cfg.CACertBase64)
		if err != nil {
			return nil, configError(fmt.Sprintf("decoding CA cert: %v", err))
		}

		caCertPool := x509.NewCertPool()
		if !caCertPool.AppendCertsFromPEM(caCert) {
			return nil, fmt.Errorf("adding CA cert to pool failed: %w", ErrInvalidConfig)
		}

		tlsConfig.RootCAs = caCertPool
	}

	return tlsConfig, nil
}

// isTLSImplied returns true if the URI scheme or query parameters indicate TLS.
// Uses proper URI parsing to avoid false positives from substring matching
// (e.g. credentials or unrelated params containing "tls=true").
func isTLSImplied(uri string) bool {
	if strings.HasPrefix(strings.ToLower(uri), "mongodb+srv://") {
		return true
	}

	parsed, err := neturl.Parse(uri)
	if err != nil {
		return false
	}

	for key, values := range parsed.Query() {
		if strings.EqualFold(key, "tls") || strings.EqualFold(key, "ssl") {
			for _, value := range values {
				if strings.EqualFold(value, "true") {
					return true
				}
			}
		}
	}

	return false
}

// SanitizedError wraps a driver error with a credential-free message.
// Error() returns only the sanitized text. This prevents URI/auth details
// from leaking through error messages into logs or upstream callers.
// Unwrap() preserves the original error chain so callers can still use
// errors.Is/As to match context.Canceled, context.DeadlineExceeded, or
// driver sentinels.
type SanitizedError struct {
	// Message is the credential-free error description.
	Message string
	// cause is the original unwrapped error for errors.Is/As compatibility.
	cause error
}

func (e *SanitizedError) Error() string { return e.Message }

// Unwrap returns the original error, preserving the error chain for
// errors.Is and errors.As matching.
func (e *SanitizedError) Unwrap() error { return e.cause }

// sanitizeDriverError wraps a raw MongoDB driver error in a SanitizedError
// that strips potential URI and authentication details from the message.
func sanitizeDriverError(err error) error {
	if err == nil {
		return nil
	}

	msg := err.Error()
	msg = uriCredentialsPattern.ReplaceAllString(msg, "://***@")
	msg = uriPasswordParamPattern.ReplaceAllString(msg, "${1}***")

	return &SanitizedError{Message: msg, cause: err}
}

// uriCredentialsPattern matches "://user:pass@" in connection strings.
var uriCredentialsPattern = regexp.MustCompile(`://[^@\s]+@`)

// uriPasswordParamPattern matches "password=value" query parameters.
var uriPasswordParamPattern = regexp.MustCompile(`(?i)(password=)(\S+)`)

// configError wraps a configuration validation message with ErrInvalidConfig.
func configError(msg string) error {
	return fmt.Errorf("%w: %s", ErrInvalidConfig, msg)
}

// recordConnectionFailure increments the mongo connection failure counter.
// No-op when metricsFactory is nil.
func (c *Client) recordConnectionFailure(operation string) {
	if c == nil || c.metricsFactory == nil {
		return
	}

	counter, err := c.metricsFactory.Counter(connectionFailuresMetric)
	if err != nil {
		c.logAtLevel(context.Background(), log.LevelWarn, "failed to create mongo metric counter", log.Err(err))
		return
	}

	err = counter.
		WithLabels(map[string]string{
			"operation": constant.SanitizeMetricLabel(operation),
		}).
		AddOne(context.Background())
	if err != nil {
		c.logAtLevel(context.Background(), log.LevelWarn, "failed to record mongo metric", log.Err(err))
	}
}

// indexKeysString returns a string representation of the index keys.
// It's used to log the index keys in a human-readable format.
func indexKeysString(keys any) string {
	switch k := keys.(type) {
	case bson.D:
		parts := make([]string, 0, len(k))
		for _, e := range k {
			parts = append(parts, e.Key)
		}

		return strings.Join(parts, ",")
	case bson.M:
		parts := make([]string, 0, len(k))
		for key := range k {
			parts = append(parts, key)
		}

		sort.Strings(parts)

		return strings.Join(parts, ",")
	default:
		return "<unknown>"
	}
}
