package redis

import (
	"context"
	"crypto/tls"
	"github.com/LerianStudio/lib-commons/commons/log"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// TTL default redis ttl to cache
const TTL = 300

// Mode define the Redis connection mode supported
type Mode string

const (
	ModeStandalone Mode = "standalone"
	ModeSentinel   Mode = "sentinel"
	ModeCluster    Mode = "cluster"
)

// RedisConnection this struct represent a Redis connection hub
type RedisConnection struct {
	Mode Mode

	Address string
	DB      int

	SentinelAddress []string
	MasterName      string

	ClusterAddress []string

	Password  string
	Protocol  int
	UseTLS    bool
	TLSConfig *tls.Config

	Logger    log.Logger
	Connected bool
	Client    redis.UniversalClient
}

// Connect initializes a Redis connection
func (rc *RedisConnection) Connect(ctx context.Context) error {
	rc.Logger.Info("Connecting to Redis...")

	opts := &redis.UniversalOptions{
		Password:   rc.Password,
		DB:         rc.DB,
		MasterName: "",
		Protocol:   rc.Protocol,
	}

	if rc.UseTLS {
		if rc.TLSConfig != nil {
			opts.TLSConfig = rc.TLSConfig
		} else {
			opts.TLSConfig = &tls.Config{
				MinVersion: tls.VersionTLS12,
			}
		}
	}

	switch rc.Mode {
	case ModeSentinel:
		opts.Addrs = rc.SentinelAddress
		opts.MasterName = rc.MasterName
	case ModeCluster:
		opts.Addrs = rc.ClusterAddress
	default: //Standalone
		opts.Addrs = []string{rc.Address}
		opts.DB = 0
	}

	rdb := redis.NewUniversalClient(opts)

	if _, err := rdb.Ping(ctx).Result(); err != nil {
		rc.Logger.Infof("RedisConnection.Ping %v", zap.Error(err))
		return err
	}

	rc.Client = rdb
	rc.Connected = true

	rc.Logger.Info("Connected to Redis ✅")

	return nil
}

// GetClient always returns a pointer to a Redis client
func (rc *RedisConnection) GetClient(ctx context.Context) (redis.UniversalClient, error) {
	if rc.Client == nil {
		if err := rc.Connect(ctx); err != nil {
			rc.Logger.Infof("RedisConnection.Connect error %v", zap.Error(err))
			return nil, err
		}
	}

	return rc.Client, nil
}

// Close closes the Redis connection
func (rc *RedisConnection) Close() error {
	if rc.Client != nil {
		return rc.Client.Close()
	}

	return nil
}
