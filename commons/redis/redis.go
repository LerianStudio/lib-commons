package redis

import (
	"context"
	"crypto/tls"
	"github.com/LerianStudio/lib-commons/commons/log"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// Mode define the Redis connection mode supported
type Mode string

const (
	TTL            int  = 300
	ModeStandalone Mode = "standalone"
	ModeSentinel   Mode = "sentinel"
	ModeCluster    Mode = "cluster"
)

// RedisConnection this struct represent a Redis connection hub
type RedisConnection struct {
	Mode       Mode
	Address    []string
	DB         int
	MasterName string
	Password   string
	Protocol   int
	UseTLS     bool
	TLSConfig  *tls.Config
	Logger     log.Logger
	Connected  bool
	Client     redis.UniversalClient
}

// Connect initializes a Redis connection
func (rc *RedisConnection) Connect(ctx context.Context) error {
	rc.Logger.Info("Connecting to Redis...")

	opts := &redis.UniversalOptions{
		Addrs:      rc.Address,
		Password:   rc.Password,
		MasterName: rc.MasterName,
		DB:         rc.DB,
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
