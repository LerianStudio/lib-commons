// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

// Package redis provides a Redis client factory for the tenant event Pub/Sub system
// consumed by TenantEventListener.
package redis

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"net"

	"github.com/redis/go-redis/v9"
)

const defaultPort = "6379"

// TenantPubSubRedisConfig configures the Redis client used as the transport for
// tenant lifecycle Pub/Sub events. It intentionally exposes a narrow surface
// because this client only carries lifecycle signals — no application data is
// persisted on this Redis instance.
//
// CACertBase64 is OPTIONAL. When empty (the zero value), TLS validation falls
// back to the Go default system trust pool — preserving the historical
// behavior of this struct. Set CACertBase64 when targeting a managed cluster
// whose CA is not reliably present in the deployer's system trust (e.g., AWS
// ElastiCache/Valkey from a macOS development workstation, where Apple's
// Security Framework rejects valid Amazon-issued certs that Go's pure
// verifier with an explicit RootCAs pool accepts).
type TenantPubSubRedisConfig struct {
	Host     string
	Port     string
	Password string
	TLS      bool
	// CACertBase64 is OPTIONAL. Base64-encoded PEM bundle used to populate
	// tls.Config.RootCAs when TLS is enabled. When empty, RootCAs is left
	// nil and the Go runtime falls back to the system trust pool —
	// preserving existing behavior (backward-compatible). When non-empty,
	// the decoded PEM blocks become the only roots trusted for the TLS
	// handshake. Ignored when TLS is false.
	//
	// Downstream consumers typically wire this from the
	// MULTI_TENANT_REDIS_CA_CERT environment variable.
	CACertBase64 string
}

// BuildOptions translates the high-level configuration into go-redis Options.
// It validates required fields and applies sensible defaults (port 6379,
// TLS 1.2 minimum version).
//
// When TLS is enabled and CACertBase64 is empty, the resulting tls.Config has
// no explicit RootCAs — the Go runtime falls back to the system trust pool.
// This preserves backward compatibility with callers that pre-date the
// CACertBase64 field.
func BuildOptions(cfg TenantPubSubRedisConfig) (*redis.Options, error) {
	if cfg.Host == "" {
		return nil, errors.New("tenant pubsub redis: host is required")
	}

	port := cfg.Port
	if port == "" {
		port = defaultPort
	}

	opts := &redis.Options{
		Addr: net.JoinHostPort(cfg.Host, port),
	}

	if cfg.Password != "" {
		opts.Password = cfg.Password
	}

	if cfg.TLS {
		tlsCfg, err := buildTLSConfig(cfg)
		if err != nil {
			return nil, err
		}

		opts.TLSConfig = tlsCfg
	}

	return opts, nil
}

// buildTLSConfig constructs the tls.Config used by the tenant Pub/Sub Redis
// client. It is only called when cfg.TLS is true. When cfg.CACertBase64 is
// empty, RootCAs is intentionally left nil so the Go default system trust
// pool is used (backward-compatible behavior).
func buildTLSConfig(cfg TenantPubSubRedisConfig) (*tls.Config, error) {
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
	}

	if cfg.CACertBase64 == "" {
		return tlsCfg, nil
	}

	pem, err := base64.StdEncoding.DecodeString(cfg.CACertBase64)
	if err != nil {
		return nil, fmt.Errorf("tenant pubsub redis: decode CA cert base64: %w", err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("tenant pubsub redis: CA cert: no PEM blocks found")
	}

	tlsCfg.RootCAs = pool

	return tlsCfg, nil
}

// NewTenantPubSubRedisClient creates a go-redis client configured for tenant
// event Pub/Sub.  It dials Redis immediately to verify connectivity.
func NewTenantPubSubRedisClient(ctx context.Context, cfg TenantPubSubRedisConfig) (redis.UniversalClient, error) {
	opts, err := BuildOptions(cfg)
	if err != nil {
		return nil, err
	}

	client := redis.NewClient(opts)

	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("tenant pubsub redis: ping failed: %w", err)
	}

	return client, nil
}
