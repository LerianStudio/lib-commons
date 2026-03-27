// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

// Package redis provides a Redis client factory for the tenant event Pub/Sub system
// consumed by TenantEventListener.
package redis

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"

	"github.com/redis/go-redis/v9"
)

const defaultPort = "6379"

type TenantPubSubRedisConfig struct {
	Host     string
	Port     string
	Password string
	TLS      bool
}

// BuildOptions translates the high-level configuration into go-redis Options.
// It validates required fields and applies sensible defaults (port 6379,
// TLS 1.2 minimum version).
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
		opts.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
		}
	}

	return opts, nil
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
