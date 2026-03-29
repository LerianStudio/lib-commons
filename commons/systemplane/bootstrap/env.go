// Copyright 2025 Lerian Studio.

package bootstrap

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/domain"
)

// Environment variable names for systemplane bootstrap configuration.
const (
	EnvBackend = "SYSTEMPLANE_BACKEND"

	EnvPostgresDSN           = "SYSTEMPLANE_POSTGRES_DSN"
	EnvPostgresSchema        = "SYSTEMPLANE_POSTGRES_SCHEMA"
	EnvPostgresEntriesTable  = "SYSTEMPLANE_POSTGRES_ENTRIES_TABLE"
	EnvPostgresHistoryTable  = "SYSTEMPLANE_POSTGRES_HISTORY_TABLE"
	EnvPostgresRevisionTable = "SYSTEMPLANE_POSTGRES_REVISION_TABLE"
	EnvPostgresNotifyChannel = "SYSTEMPLANE_POSTGRES_NOTIFY_CHANNEL"

	EnvMongoURI               = "SYSTEMPLANE_MONGODB_URI"
	EnvMongoDatabase          = "SYSTEMPLANE_MONGODB_DATABASE"
	EnvMongoEntriesCollection = "SYSTEMPLANE_MONGODB_ENTRIES_COLLECTION"
	EnvMongoHistoryCollection = "SYSTEMPLANE_MONGODB_HISTORY_COLLECTION"
	EnvMongoWatchMode         = "SYSTEMPLANE_MONGODB_WATCH_MODE"
	EnvMongoPollIntervalSec   = "SYSTEMPLANE_MONGODB_POLL_INTERVAL_SEC"
)

// EnvSecretMasterKey is the environment variable holding the AES-256 master key
// for encrypting/decrypting secret configuration values at rest.
const EnvSecretMasterKey = "SYSTEMPLANE_SECRET_MASTER_KEY"

// LoadFromEnvOrDefault reads SYSTEMPLANE_* environment variables when present,
// otherwise falls back to a minimal Postgres backend configuration using the
// provided DSN. This covers the common case where a product embeds systemplane
// into its existing Postgres database and does not set dedicated SYSTEMPLANE_*
// env vars.
//
// When SYSTEMPLANE_BACKEND is set, this delegates entirely to LoadFromEnv.
// When it is not set, a BootstrapConfig is constructed with
// Backend=BackendPostgres and the fallbackDSN as the Postgres DSN.
// ApplyDefaults and Validate are called in both paths.
func LoadFromEnvOrDefault(fallbackDSN string) (*BootstrapConfig, error) {
	if strings.TrimSpace(os.Getenv(EnvBackend)) != "" {
		return LoadFromEnv()
	}

	trimmedDSN := strings.TrimSpace(fallbackDSN)
	if trimmedDSN == "" {
		return nil, ErrMissingPostgresDSN
	}

	cfg := &BootstrapConfig{
		Backend: domain.BackendPostgres,
		Postgres: &PostgresBootstrapConfig{
			DSN: trimmedDSN,
		},
	}

	cfg.ApplyDefaults()

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// LoadFromEnv reads SYSTEMPLANE_* environment variables and returns a validated
// BootstrapConfig.
func LoadFromEnv() (*BootstrapConfig, error) {
	backendStr := os.Getenv(EnvBackend)
	if backendStr == "" {
		return nil, fmt.Errorf("%w: %s not set", ErrMissingBackend, EnvBackend)
	}

	backend, err := domain.ParseBackendKind(backendStr)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", EnvBackend, err)
	}

	cfg := &BootstrapConfig{Backend: backend}
	switch backend {
	case domain.BackendPostgres:
		cfg.Postgres = &PostgresBootstrapConfig{
			DSN:           strings.TrimSpace(os.Getenv(EnvPostgresDSN)),
			Schema:        strings.TrimSpace(os.Getenv(EnvPostgresSchema)),
			EntriesTable:  strings.TrimSpace(os.Getenv(EnvPostgresEntriesTable)),
			HistoryTable:  strings.TrimSpace(os.Getenv(EnvPostgresHistoryTable)),
			RevisionTable: strings.TrimSpace(os.Getenv(EnvPostgresRevisionTable)),
			NotifyChannel: strings.TrimSpace(os.Getenv(EnvPostgresNotifyChannel)),
		}
	case domain.BackendMongoDB:
		cfg.MongoDB = &MongoBootstrapConfig{
			URI:               strings.TrimSpace(os.Getenv(EnvMongoURI)),
			Database:          strings.TrimSpace(os.Getenv(EnvMongoDatabase)),
			EntriesCollection: strings.TrimSpace(os.Getenv(EnvMongoEntriesCollection)),
			HistoryCollection: strings.TrimSpace(os.Getenv(EnvMongoHistoryCollection)),
			WatchMode:         strings.TrimSpace(os.Getenv(EnvMongoWatchMode)),
		}

		if raw := strings.TrimSpace(os.Getenv(EnvMongoPollIntervalSec)); raw != "" {
			seconds, convErr := strconv.Atoi(raw)
			if convErr != nil {
				return nil, fmt.Errorf("parse %s: %w", EnvMongoPollIntervalSec, convErr)
			}

			if seconds <= 0 {
				return nil, fmt.Errorf("%w: %s", ErrInvalidPollInterval, EnvMongoPollIntervalSec)
			}

			cfg.MongoDB.PollInterval = time.Duration(seconds) * time.Second
		}
	}

	cfg.ApplyDefaults()

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}
