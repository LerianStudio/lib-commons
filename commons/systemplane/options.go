// Package-level option constructors for Client and key registration.
package systemplane

import (
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/LerianStudio/lib-commons/v4/commons/opentelemetry"
)

// clientConfig holds the merged configuration applied by Option functions.
type clientConfig struct {
	logger        log.Logger
	telemetry     *opentelemetry.Telemetry
	listenChannel string        // Postgres LISTEN channel name
	pollInterval  time.Duration // MongoDB polling interval (zero = change streams)
	debounce      time.Duration
	collection    string // MongoDB collection name
	table         string // Postgres table name
}

// defaultClientConfig returns sensible defaults.
func defaultClientConfig() clientConfig {
	return clientConfig{
		listenChannel: "systemplane_changes",
		debounce:      100 * time.Millisecond,
		collection:    "systemplane_entries",
		table:         "systemplane_entries",
	}
}

// Option configures a Client at construction time.
type Option func(*clientConfig)

// WithLogger sets the structured logger used by the Client and its backend.
func WithLogger(l log.Logger) Option {
	return func(cfg *clientConfig) {
		if l != nil {
			cfg.logger = l
		}
	}
}

// WithTelemetry sets the OpenTelemetry provider for spans and metrics.
func WithTelemetry(t *opentelemetry.Telemetry) Option {
	return func(cfg *clientConfig) {
		if t != nil {
			cfg.telemetry = t
		}
	}
}

// WithListenChannel overrides the Postgres LISTEN/NOTIFY channel name.
// Default: "systemplane_changes". Ignored by MongoDB backends.
func WithListenChannel(name string) Option {
	return func(cfg *clientConfig) {
		if name != "" {
			cfg.listenChannel = name
		}
	}
}

// WithPollInterval enables polling mode for MongoDB instead of change streams.
// A zero or negative value keeps the default change-stream mode.
// Ignored by Postgres backends.
func WithPollInterval(d time.Duration) Option {
	return func(cfg *clientConfig) {
		if d > 0 {
			cfg.pollInterval = d
		}
	}
}

// WithDebounce sets the trailing-edge debounce window for change notifications.
// Default: 100ms. A zero or negative value disables debouncing.
func WithDebounce(d time.Duration) Option {
	return func(cfg *clientConfig) {
		cfg.debounce = d
	}
}

// WithCollection overrides the MongoDB collection name.
// Default: "systemplane_entries". Ignored by Postgres backends.
func WithCollection(name string) Option {
	return func(cfg *clientConfig) {
		if name != "" {
			cfg.collection = name
		}
	}
}

// WithTable overrides the Postgres table name.
// Default: "systemplane_entries". Ignored by MongoDB backends.
func WithTable(name string) Option {
	return func(cfg *clientConfig) {
		if name != "" {
			cfg.table = name
		}
	}
}

// KeyOption configures a single key at registration time.
type KeyOption func(*keyDef)

// WithDescription sets a human-readable description for the key,
// surfaced in admin endpoints.
func WithDescription(s string) KeyOption {
	return func(k *keyDef) {
		k.description = s
	}
}

// WithValidator sets a validation function invoked on every Set.
// The function receives the proposed value and should return a non-nil
// error to reject it.
func WithValidator(fn func(any) error) KeyOption {
	return func(k *keyDef) {
		if fn != nil {
			k.validator = fn
		}
	}
}

// WithRedaction sets the redaction policy for admin and log output.
// Default: RedactNone.
func WithRedaction(policy RedactPolicy) KeyOption {
	return func(k *keyDef) {
		k.redaction = policy
	}
}
