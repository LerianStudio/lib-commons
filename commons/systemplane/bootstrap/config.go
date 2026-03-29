// Copyright 2025 Lerian Studio.

package bootstrap

import (
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"os"
	"strings"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/domain"
)

// Sentinel errors for bootstrap configuration validation.
var (
	ErrMissingBackend            = errors.New("systemplane: backend is required")
	ErrMissingPostgresConfig     = errors.New("systemplane: postgres config is required when backend is postgres")
	ErrMissingMongoConfig        = errors.New("systemplane: mongodb config is required when backend is mongodb")
	ErrMissingPostgresDSN        = errors.New("systemplane: postgres DSN is required")
	ErrMissingMongoURI           = errors.New("systemplane: mongodb URI is required")
	ErrInvalidPostgresIdentifier = errors.New("systemplane: invalid postgres identifier")
	ErrInvalidWatchMode          = errors.New("systemplane: mongodb watch mode must be change_stream or poll")
	ErrInvalidPollInterval       = errors.New("systemplane: mongodb poll interval must be greater than zero when watch mode is poll")
	ErrInvalidMongoIdentifier    = errors.New("systemplane: invalid mongodb identifier")
	ErrUnhandledBackend          = errors.New("systemplane: unhandled backend kind")
)

// BootstrapConfig holds the initial configuration needed to connect to the
// systemplane backend before any runtime configuration is loaded.
type BootstrapConfig struct {
	Backend        domain.BackendKind
	Postgres       *PostgresBootstrapConfig
	MongoDB        *MongoBootstrapConfig
	Secrets        *SecretStoreConfig
	ApplyBehaviors map[string]domain.ApplyBehavior
}

// SecretStoreConfig holds bootstrap-only encryption settings for secret values.
type SecretStoreConfig struct {
	MasterKey  string
	SecretKeys []string
}

// String implements fmt.Stringer to prevent accidental master key exposure in logs or spans.
func (s *SecretStoreConfig) String() string {
	if s == nil {
		return "<nil>"
	}

	return "SecretStoreConfig{MasterKey:REDACTED}"
}

// GoString implements fmt.GoStringer to prevent accidental master key exposure in %#v formatting.
func (s *SecretStoreConfig) GoString() string {
	return s.String()
}

// validate checks that the master key meets minimum size requirements.
// It accepts either exactly 32 raw bytes or a valid base64-encoded 32-byte value.
func (s *SecretStoreConfig) validate() error {
	if s == nil {
		return nil
	}

	const masterKeySizeBytes = 32

	if len([]byte(s.MasterKey)) == masterKeySizeBytes {
		return nil
	}

	decoded, err := base64.StdEncoding.DecodeString(s.MasterKey)
	if err == nil && len(decoded) == masterKeySizeBytes {
		return nil
	}

	return fmt.Errorf("secret master key must be exactly %d raw bytes or a valid base64-encoded %d-byte value", masterKeySizeBytes, masterKeySizeBytes)
}

// PostgresBootstrapConfig holds PostgreSQL-specific bootstrap settings.
type PostgresBootstrapConfig struct {
	DSN            string
	Schema         string
	EntriesTable   string
	HistoryTable   string
	RevisionTable  string
	NotifyChannel  string
	ApplyBehaviors map[string]domain.ApplyBehavior
}

// MongoBootstrapConfig holds MongoDB-specific bootstrap settings.
type MongoBootstrapConfig struct {
	URI               string
	Database          string
	EntriesCollection string
	HistoryCollection string
	WatchMode         string
	PollInterval      time.Duration
	ApplyBehaviors    map[string]domain.ApplyBehavior
}

// ApplyKeyDefs propagates ApplyBehavior from the given KeyDefs into the
// bootstrap configuration and auto-configures secret encryption when secret
// keys are detected and the SYSTEMPLANE_SECRET_MASTER_KEY env var is set.
//
// Reading from the environment here is intentional and consistent with the
// rest of the bootstrap layer (LoadFromEnv, LoadFromEnvOrDefault). Bootstrap
// is the one place where direct os.Getenv calls are expected: the entire
// purpose of this layer is to translate process-environment state into typed
// Go configuration before any backend is created.
//
// This is typically called once during service startup, after
// LoadFromEnvOrDefault and before creating the backend.
func (cfg *BootstrapConfig) ApplyKeyDefs(defs []domain.KeyDef) {
	if cfg == nil {
		return
	}

	applyBehaviors := make(map[string]domain.ApplyBehavior, len(defs))
	secretKeys := make([]string, 0)

	for _, def := range defs {
		applyBehaviors[def.Key] = def.ApplyBehavior

		if def.Secret {
			secretKeys = append(secretKeys, def.Key)
		}
	}

	cfg.ApplyBehaviors = applyBehaviors

	if cfg.Postgres != nil {
		pgBehaviors := make(map[string]domain.ApplyBehavior, len(applyBehaviors))
		maps.Copy(pgBehaviors, applyBehaviors)

		cfg.Postgres.ApplyBehaviors = pgBehaviors
	}

	if cfg.MongoDB != nil {
		mgBehaviors := make(map[string]domain.ApplyBehavior, len(applyBehaviors))
		maps.Copy(mgBehaviors, applyBehaviors)

		cfg.MongoDB.ApplyBehaviors = mgBehaviors
	}

	masterKey := strings.TrimSpace(os.Getenv(EnvSecretMasterKey))
	if len(secretKeys) == 0 || masterKey == "" {
		return
	}

	cfg.Secrets = &SecretStoreConfig{
		MasterKey:  masterKey,
		SecretKeys: secretKeys,
	}
}

// Validate checks that the bootstrap configuration is well-formed.
func (cfg *BootstrapConfig) Validate() error {
	if cfg == nil || !cfg.Backend.IsValid() {
		return fmt.Errorf("%w: %q", ErrMissingBackend, backendString(cfg))
	}

	if cfg.Secrets != nil {
		if err := cfg.Secrets.validate(); err != nil {
			return err
		}
	}

	switch cfg.Backend {
	case domain.BackendPostgres:
		return validatePostgresBootstrap(cfg.Postgres)
	case domain.BackendMongoDB:
		return validateMongoBootstrap(cfg.MongoDB)
	default:
		return fmt.Errorf("validate: %w: %q", ErrUnhandledBackend, cfg.Backend)
	}
}

// ApplyDefaults fills in zero-value fields with sensible defaults.
func (cfg *BootstrapConfig) ApplyDefaults() {
	if cfg == nil {
		return
	}

	if cfg.Postgres != nil {
		applyPostgresDefaults(cfg.Postgres)
	}

	if cfg.MongoDB != nil {
		applyMongoDefaults(cfg.MongoDB)
	}
}

func validatePostgresBootstrap(postgresConfig *PostgresBootstrapConfig) error {
	if postgresConfig == nil {
		return ErrMissingPostgresConfig
	}

	if strings.TrimSpace(postgresConfig.DSN) == "" {
		return ErrMissingPostgresDSN
	}

	if err := ValidatePostgresObjectNames(
		defaultString(postgresConfig.Schema, DefaultPostgresSchema),
		defaultString(postgresConfig.EntriesTable, DefaultPostgresEntriesTable),
		defaultString(postgresConfig.HistoryTable, DefaultPostgresHistoryTable),
		defaultString(postgresConfig.RevisionTable, DefaultPostgresRevisionTable),
		defaultString(postgresConfig.NotifyChannel, DefaultPostgresNotifyChannel),
	); err != nil {
		return err
	}

	return nil
}

func validateMongoBootstrap(mongoConfig *MongoBootstrapConfig) error {
	if mongoConfig == nil {
		return ErrMissingMongoConfig
	}

	if strings.TrimSpace(mongoConfig.URI) == "" {
		return ErrMissingMongoURI
	}

	if err := validateMongoIdentifier("database", defaultString(mongoConfig.Database, DefaultMongoDatabase)); err != nil {
		return err
	}

	if err := validateMongoIdentifier("entries collection", defaultString(mongoConfig.EntriesCollection, DefaultMongoEntriesCollection)); err != nil {
		return err
	}

	if err := validateMongoIdentifier("history collection", defaultString(mongoConfig.HistoryCollection, DefaultMongoHistoryCollection)); err != nil {
		return err
	}

	if mongoConfig.WatchMode != "" && mongoConfig.WatchMode != "change_stream" && mongoConfig.WatchMode != "poll" {
		return ErrInvalidWatchMode
	}

	if mongoConfig.WatchMode == "poll" && mongoConfig.PollInterval <= 0 {
		return ErrInvalidPollInterval
	}

	return nil
}

// validateMongoIdentifier checks that a MongoDB database or collection name
// does not contain forbidden characters (null bytes, $, empty).
func validateMongoIdentifier(kind, value string) error {
	trimmedValue := strings.TrimSpace(value)
	if trimmedValue == "" {
		return fmt.Errorf("%w %s: must not be empty", ErrInvalidMongoIdentifier, kind)
	}

	if strings.ContainsRune(trimmedValue, '$') {
		return fmt.Errorf("%w %s %q: must not contain '$'", ErrInvalidMongoIdentifier, kind, value)
	}

	if strings.ContainsRune(trimmedValue, 0) {
		return fmt.Errorf("%w %s: must not contain null bytes", ErrInvalidMongoIdentifier, kind)
	}

	return nil
}

func applyPostgresDefaults(postgresConfig *PostgresBootstrapConfig) {
	postgresConfig.DSN = strings.TrimSpace(postgresConfig.DSN)
	postgresConfig.Schema = strings.TrimSpace(postgresConfig.Schema)
	postgresConfig.EntriesTable = strings.TrimSpace(postgresConfig.EntriesTable)
	postgresConfig.HistoryTable = strings.TrimSpace(postgresConfig.HistoryTable)
	postgresConfig.RevisionTable = strings.TrimSpace(postgresConfig.RevisionTable)
	postgresConfig.NotifyChannel = strings.TrimSpace(postgresConfig.NotifyChannel)

	if postgresConfig.Schema == "" {
		postgresConfig.Schema = DefaultPostgresSchema
	}

	if postgresConfig.EntriesTable == "" {
		postgresConfig.EntriesTable = DefaultPostgresEntriesTable
	}

	if postgresConfig.HistoryTable == "" {
		postgresConfig.HistoryTable = DefaultPostgresHistoryTable
	}

	if postgresConfig.RevisionTable == "" {
		postgresConfig.RevisionTable = DefaultPostgresRevisionTable
	}

	if postgresConfig.NotifyChannel == "" {
		postgresConfig.NotifyChannel = DefaultPostgresNotifyChannel
	}
}

func applyMongoDefaults(mongoConfig *MongoBootstrapConfig) {
	mongoConfig.URI = strings.TrimSpace(mongoConfig.URI)
	mongoConfig.Database = strings.TrimSpace(mongoConfig.Database)
	mongoConfig.EntriesCollection = strings.TrimSpace(mongoConfig.EntriesCollection)
	mongoConfig.HistoryCollection = strings.TrimSpace(mongoConfig.HistoryCollection)
	mongoConfig.WatchMode = strings.TrimSpace(mongoConfig.WatchMode)

	if mongoConfig.Database == "" {
		mongoConfig.Database = DefaultMongoDatabase
	}

	if mongoConfig.EntriesCollection == "" {
		mongoConfig.EntriesCollection = DefaultMongoEntriesCollection
	}

	if mongoConfig.HistoryCollection == "" {
		mongoConfig.HistoryCollection = DefaultMongoHistoryCollection
	}

	if mongoConfig.WatchMode == "" {
		mongoConfig.WatchMode = DefaultMongoWatchMode
	}

	if mongoConfig.PollInterval == 0 {
		mongoConfig.PollInterval = DefaultMongoPollInterval
	}
}

func backendString(c *BootstrapConfig) string {
	if c == nil {
		return ""
	}

	return string(c.Backend)
}

func defaultString(value, fallback string) string {
	trimmedValue := strings.TrimSpace(value)
	if trimmedValue == "" {
		return fallback
	}

	return trimmedValue
}
