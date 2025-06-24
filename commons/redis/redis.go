package redis

import (
	iamcredentials "cloud.google.com/go/iam/credentials/apiv1"
	iamcredentialspb "cloud.google.com/go/iam/credentials/apiv1/credentialspb"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"github.com/LerianStudio/lib-commons/commons"
	"github.com/LerianStudio/lib-commons/commons/log"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/durationpb"
	"os"
	"sync"
	"time"
)

// Mode define the Redis connection mode supported
type Mode string

const (
	TTL            int    = 300
	Scope          string = "https://www.googleapis.com/auth/cloud-platform"
	ModeStandalone Mode   = "standalone"
	ModeSentinel   Mode   = "sentinel"
	ModeCluster    Mode   = "cluster"
)

// RedisConnection represents a Redis connection hub
type RedisConnection struct {
	Mode           Mode
	Address        []string
	DB             int
	MasterName     string
	Password       string
	Protocol       int
	UseTLS         bool
	Logger         log.Logger
	Connected      bool
	Client         redis.UniversalClient
	CACertPath     *string
	ClientCertPath *string
	ClientKeyPath  *string

	UseIAMAuth         bool
	ServiceAccountName string
	TokenLifetime      time.Duration
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
	if rc.UseIAMAuth {
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

	if rc.UseIAMAuth {
		opts.Password = rc.token
		opts.Username = "default"
	} else {
		opts.Password = rc.Password
	}

	if rc.UseTLS {
		tlsConfig, err := rc.BuildTLSConfig(rc.CACertPath, rc.ClientCertPath, rc.ClientKeyPath)
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
	rc.Logger.Info("Connected to Redis/Valkey ✅")

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

// BuildTLSConfig generates a *tls.Config configuration
// #nosec G304
func (rc *RedisConnection) BuildTLSConfig(caCertPath, clientCertPath, clientKeyPath *string) (*tls.Config, error) {
	if commons.IsNilOrEmpty(caCertPath) {
		return nil, errors.New("CA cert path is required for TLS")
	}

	caCert, err := os.ReadFile(*caCertPath)
	if err != nil {
		return nil, errors.New("reading CA cert: " + err.Error())
	}

	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caCert) {
		return nil, errors.New("adding CA cert failed")
	}

	tlsCfg := &tls.Config{
		RootCAs:    caCertPool,
		MinVersion: tls.VersionTLS12,
	}

	if !commons.IsNilOrEmpty(clientCertPath) && !commons.IsNilOrEmpty(clientKeyPath) {
		clientCert, err := tls.LoadX509KeyPair(*clientCertPath, *clientKeyPath)
		if err != nil {
			return nil, fmt.Errorf("loading client cert: %w", err)
		}

		tlsCfg.Certificates = []tls.Certificate{clientCert}
	}

	return tlsCfg, nil
}

// retrieveToken generates a new IAM token
func (rc *RedisConnection) retrieveToken(ctx context.Context) (string, error) {
	client, err := iamcredentials.NewIamCredentialsClient(ctx)
	if err != nil {
		return "", fmt.Errorf("creating IAM credentials client: %w", err)
	}
	defer client.Close()

	req := &iamcredentialspb.GenerateAccessTokenRequest{
		Name:     rc.ServiceAccountName,
		Scope:    []string{Scope},
		Lifetime: durationpb.New(rc.TokenLifetime),
	}

	resp, err := client.GenerateAccessToken(ctx, req)
	if err != nil {
		return "", fmt.Errorf("generate access token: %w", err)
	}

	return resp.AccessToken, nil
}

// refreshTokenLoop periodically refreshes the IAM token
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
