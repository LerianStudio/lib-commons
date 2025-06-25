package redis

import (
	iamcredentials "cloud.google.com/go/iam/credentials/apiv1"
	iamcredentialspb "cloud.google.com/go/iam/credentials/apiv1/credentialspb"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/LerianStudio/lib-commons/commons/log"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/durationpb"
	"sync"
	"time"
)

// Mode define the Redis connection mode supported
type Mode string

const (
	TTL                    int    = 300
	Scope                  string = "https://www.googleapis.com/auth/cloud-platform"
	PrefixServicesAccounts string = "projects/-/serviceAccounts/"
	ModeStandalone         Mode   = "standalone"
	ModeSentinel           Mode   = "sentinel"
	ModeCluster            Mode   = "cluster"
)

// RedisConnection represents a Redis connection hub
type RedisConnection struct {
	Mode               Mode
	Address            []string
	DB                 int
	MasterName         string
	Password           string
	Protocol           int
	UseTLS             bool
	Logger             log.Logger
	Connected          bool
	Client             redis.UniversalClient
	CACert             string
	UseGCPIAMAuth      bool
	ServiceAccountName string
	TokenLifeTime      time.Duration
	RefreshDuration    time.Duration
	token              string
	lastRefreshInstant time.Time
	errLastSeen        error
	mu                 sync.RWMutex
}

// Connect initializes a Redis connection
func (rc *RedisConnection) Connect(ctx context.Context) error {
	rc.Logger.Info("Connecting to Redis/Valkey...")

	var err error
	if rc.UseGCPIAMAuth {
		rc.token, err = rc.retrieveToken(ctx)
		if err != nil {
			rc.Logger.Infof("initial token retrieval failed: %v", zap.Error(err))
			return err
		}

		rc.lastRefreshInstant = time.Now()

		go rc.refreshTokenLoop(ctx)
	}

	opts := &redis.UniversalOptions{
		Addrs:      rc.Address,
		MasterName: rc.MasterName,
		DB:         rc.DB,
		Protocol:   rc.Protocol,
	}

	if rc.UseGCPIAMAuth {
		opts.Password = rc.token
		opts.Username = "default"
	} else {
		opts.Password = rc.Password
	}

	if rc.UseTLS {
		tlsConfig, err := rc.BuildTLSConfig()
		if err != nil {
			rc.Logger.Infof("BuildTLSConfig error: %v", zap.Error(err))

			return err
		}

		opts.TLSConfig = tlsConfig
	}

	rdb := redis.NewUniversalClient(opts)
	if _, err := rdb.Ping(ctx).Result(); err != nil {
		rc.Logger.Infof("Ping error: %v", zap.Error(err))
		return err
	}

	rc.Client = rdb
	rc.Connected = true

	switch rc.Client.(type) {
	case *redis.ClusterClient:
		rc.Logger.Info("Connected to Redis/Valkey in CLUSTER mode ✅ \n")
	case *redis.Client:
		rc.Logger.Info("Connected to Redis/Valkey in STANDALONE mode ✅ \n")
	case *redis.Ring:
		rc.Logger.Info("Connected to Redis/Valkey in SENTINEL mode ✅ \n")
	default:
		rc.Logger.Warn("Unknown Redis/Valkey mode ⚠️ \n")
	}

	return nil
}

// GetClient always returns a pointer to a Redis client
func (rc *RedisConnection) GetClient(ctx context.Context) (redis.UniversalClient, error) {
	if rc.Client == nil {
		if err := rc.Connect(ctx); err != nil {
			rc.Logger.Infof("Get client connect error %v", zap.Error(err))
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

// BuildTLSConfig generates a *tls.Config configuration using ca cert on base64
func (rc *RedisConnection) BuildTLSConfig() (*tls.Config, error) {
	caCert, err := base64.StdEncoding.DecodeString(rc.CACert)
	if err != nil {
		rc.Logger.Infof("Base64 caceret error to decode error: %v", zap.Error(err))

		return nil, err
	}

	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caCert) {
		return nil, errors.New("adding CA cert failed")
	}

	tlsCfg := &tls.Config{
		RootCAs:    caCertPool,
		MinVersion: tls.VersionTLS12,
	}

	return tlsCfg, nil
}

// retrieveToken generates a new GCP IAM token
func (rc *RedisConnection) retrieveToken(ctx context.Context) (string, error) {
	client, err := iamcredentials.NewIamCredentialsClient(ctx)
	if err != nil {
		return "", fmt.Errorf("creating IAM credentials client: %w", err)
	}
	defer client.Close()

	req := &iamcredentialspb.GenerateAccessTokenRequest{
		Name:     PrefixServicesAccounts + rc.ServiceAccountName,
		Scope:    []string{Scope},
		Lifetime: durationpb.New(rc.TokenLifeTime),
	}

	resp, err := client.GenerateAccessToken(ctx, req)
	if err != nil {
		return "", fmt.Errorf("problem to generate access token: %w", err)
	}

	return resp.AccessToken, nil
}

// refreshTokenLoop periodically refreshes the GCP IAM token
func (rc *RedisConnection) refreshTokenLoop(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			rc.mu.RLock()
			last := rc.lastRefreshInstant
			rc.mu.RUnlock()

			if time.Now().After(last.Add(rc.RefreshDuration)) {
				token, err := rc.retrieveToken(ctx)
				rc.mu.Lock()

				if err != nil {
					rc.errLastSeen = err
					rc.Logger.Infof("IAM token refresh failed: %v", zap.Error(err))
				} else {
					rc.token = token
					rc.lastRefreshInstant = time.Now()
					rc.Logger.Info("IAM token refreshed...")

					_ = rc.Close()

					_ = rc.Connect(ctx)
				}

				rc.mu.Unlock()
			}

		case <-ctx.Done():
			return
		}
	}
}
