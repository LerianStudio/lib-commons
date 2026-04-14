//go:build unit

package redis

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons"
	"github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func unsetEnvVar(t *testing.T, key string) {
	t.Helper()

	original, present := os.LookupEnv(key)
	require.NoError(t, os.Unsetenv(key))
	t.Cleanup(func() {
		if present {
			require.NoError(t, os.Setenv(key, original))
			return
		}

		require.NoError(t, os.Unsetenv(key))
	})
}

type recordingLogger struct {
	mu       sync.Mutex
	warnings []string
}

func (logger *recordingLogger) Log(_ context.Context, level log.Level, msg string, _ ...log.Field) {
	if level != log.LevelWarn {
		return
	}

	logger.mu.Lock()
	logger.warnings = append(logger.warnings, msg)
	logger.mu.Unlock()
}

func (logger *recordingLogger) With(...log.Field) log.Logger { return logger }

func (logger *recordingLogger) WithGroup(string) log.Logger { return logger }

func (logger *recordingLogger) Enabled(log.Level) bool { return true }

func (logger *recordingLogger) Sync(context.Context) error { return nil }

func (logger *recordingLogger) warningMessages() []string {
	logger.mu.Lock()
	defer logger.mu.Unlock()

	return append([]string(nil), logger.warnings...)
}

func newStandaloneConfig(addr string) Config {
	return Config{
		Topology: Topology{
			Standalone: &StandaloneTopology{Address: addr},
		},
		Logger: &log.NopLogger{},
	}
}

func TestClient_NewAndGetClient(t *testing.T) {
	mr := miniredis.RunT(t)

	client, err := New(context.Background(), newStandaloneConfig(mr.Addr()))
	require.NoError(t, err)
	t.Cleanup(func() {
		if closeErr := client.Close(); closeErr != nil {
			t.Errorf("cleanup: client close: %v", closeErr)
		}
	})

	redisClient, err := client.GetClient(context.Background())
	require.NoError(t, err)

	require.NoError(t, redisClient.Set(context.Background(), "test:key", "value", 0).Err())
	value, err := redisClient.Get(context.Background(), "test:key").Result()
	require.NoError(t, err)
	assert.Equal(t, "value", value)
	connected, err := client.IsConnected()
	require.NoError(t, err)
	assert.True(t, connected)
}

func TestClient_New_InvalidConfig(t *testing.T) {
	validCert := base64.StdEncoding.EncodeToString(generateTestCertificatePEM(t))

	tests := []struct {
		name    string
		cfg     Config
		errText string
	}{
		{
			name:    "missing topology",
			cfg:     Config{Logger: &log.NopLogger{}},
			errText: "exactly one topology",
		},
		{
			name: "multiple topologies",
			cfg: Config{
				Topology: Topology{
					Standalone: &StandaloneTopology{Address: "127.0.0.1:6379"},
					Cluster:    &ClusterTopology{Addresses: []string{"127.0.0.1:6379"}},
				},
				Logger: &log.NopLogger{},
			},
			errText: "exactly one topology",
		},
		{
			name: "gcp iam requires tls",
			cfg: Config{
				Topology: Topology{Standalone: &StandaloneTopology{Address: "127.0.0.1:6379"}},
				Auth: Auth{
					GCPIAM: &GCPIAMAuth{
						CredentialsBase64: "abc",
						ServiceAccount:    "svc@project.iam.gserviceaccount.com",
					},
				},
				Logger: &log.NopLogger{},
			},
			errText: "TLS must be configured",
		},
		{
			name: "gcp iam requires service account",
			cfg: Config{
				Topology: Topology{Standalone: &StandaloneTopology{Address: "127.0.0.1:6379"}},
				TLS:      &TLSConfig{CACertBase64: validCert},
				Auth: Auth{
					GCPIAM: &GCPIAMAuth{CredentialsBase64: "abc"},
				},
				Logger: &log.NopLogger{},
			},
			errText: "service account is required",
		},
		{
			name: "gcp iam service account cannot contain slash",
			cfg: Config{
				Topology: Topology{Standalone: &StandaloneTopology{Address: "127.0.0.1:6379"}},
				TLS:      &TLSConfig{CACertBase64: validCert},
				Auth: Auth{
					GCPIAM: &GCPIAMAuth{
						CredentialsBase64: "abc",
						ServiceAccount:    "projects/-/serviceAccounts/svc@project.iam.gserviceaccount.com",
					},
				},
				Logger: &log.NopLogger{},
			},
			errText: "cannot contain '/'",
		},
		{
			name: "gcp iam credentials required",
			cfg: Config{
				Topology: Topology{Standalone: &StandaloneTopology{Address: "127.0.0.1:6379"}},
				TLS:      &TLSConfig{CACertBase64: validCert},
				Auth: Auth{
					GCPIAM: &GCPIAMAuth{ServiceAccount: "svc@project.iam.gserviceaccount.com"},
				},
				Logger: &log.NopLogger{},
			},
			errText: "credentials are required",
		},
		{
			name: "tls requires ca cert",
			cfg: Config{
				Topology: Topology{Standalone: &StandaloneTopology{Address: "127.0.0.1:6379"}},
				TLS:      &TLSConfig{},
				Logger:   &log.NopLogger{},
			},
			errText: "TLS CA cert is required",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, err := New(context.Background(), test.cfg)
			require.Error(t, err)
			assert.Nil(t, client)
			assert.ErrorIs(t, err, ErrInvalidConfig)
			assert.Contains(t, err.Error(), test.errText)
		})
	}
}

func TestClient_New_StrictTierBlocksPlaintextBeforeDial(t *testing.T) {
	t.Setenv("ENV_NAME", commons.Production.String())
	t.Setenv("ENV", "")
	t.Setenv("GO_ENV", "")
	t.Setenv(commons.EnvSecurityTier, "")
	t.Setenv(commons.EnvSecurityEnforcement, "true")
	unsetEnvVar(t, commons.EnvAllowInsecureTLS)

	client, err := New(context.Background(), Config{
		Topology: Topology{
			Standalone: &StandaloneTopology{Address: "127.0.0.1:1"},
		},
		Logger: log.NewNop(),
	})

	assert.Nil(t, client)
	require.Error(t, err)
	assert.ErrorIs(t, err, commons.ErrSecurityViolation)
}

func TestClient_New_StrictTierOverrideAllowsPlaintext(t *testing.T) {
	t.Setenv("ENV_NAME", commons.Production.String())
	t.Setenv("ENV", "")
	t.Setenv("GO_ENV", "")
	t.Setenv(commons.EnvSecurityTier, "")
	t.Setenv(commons.EnvSecurityEnforcement, "true")
	t.Setenv(commons.EnvAllowInsecureTLS, "private trusted network")

	mr := miniredis.RunT(t)
	client, err := New(context.Background(), newStandaloneConfig(mr.Addr()))
	require.NoError(t, err)
	require.NotNil(t, client)
	t.Cleanup(func() {
		require.NoError(t, client.Close())
	})
}

func TestBuildTLSConfig(t *testing.T) {
	_, err := buildTLSConfig(TLSConfig{CACertBase64: "not-base64"})
	assert.Error(t, err)

	_, err = buildTLSConfig(TLSConfig{CACertBase64: base64.StdEncoding.EncodeToString([]byte("not-a-pem"))})
	assert.Error(t, err)

	cfg, err := buildTLSConfig(TLSConfig{
		CACertBase64: base64.StdEncoding.EncodeToString(generateTestCertificatePEM(t)),
		MinVersion:   tls.VersionTLS12,
	})
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, uint16(tls.VersionTLS12), cfg.MinVersion)

	cfg, err = buildTLSConfig(TLSConfig{
		CACertBase64: base64.StdEncoding.EncodeToString(generateTestCertificatePEM(t)),
		MinVersion:   tls.VersionTLS13,
	})
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, uint16(tls.VersionTLS13), cfg.MinVersion)

	// buildTLSConfig enforces a TLS 1.2 floor. Passing a version below 1.2
	// is silently upgraded to TLS 1.2 to prevent insecure configurations.
	cfg, err = buildTLSConfig(TLSConfig{
		CACertBase64: base64.StdEncoding.EncodeToString(generateTestCertificatePEM(t)),
		MinVersion:   tls.VersionTLS10,
	})
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, uint16(tls.VersionTLS12), cfg.MinVersion)

	// Even when AllowLegacyMinVersion is true and normalizeTLSDefaults
	// preserves the lower version, buildTLSConfig enforces the TLS 1.2 floor
	// as a defense-in-depth measure.
	normalizedCfg := &TLSConfig{
		CACertBase64:          base64.StdEncoding.EncodeToString(generateTestCertificatePEM(t)),
		MinVersion:            tls.VersionTLS10,
		AllowLegacyMinVersion: true,
	}
	_, _ = normalizeTLSDefaults(normalizedCfg)
	cfg, err = buildTLSConfig(*normalizedCfg)
	require.NoError(t, err)
	require.NotNil(t, cfg)
	assert.Equal(t, uint16(tls.VersionTLS12), cfg.MinVersion)
}

func TestClient_NilReceiverGuards(t *testing.T) {
	var client *Client

	err := client.Connect(context.Background())
	assert.ErrorIs(t, err, ErrNilClient)

	rdb, err := client.GetClient(context.Background())
	assert.ErrorIs(t, err, ErrNilClient)
	assert.Nil(t, rdb)

	err = client.Close()
	assert.ErrorIs(t, err, ErrNilClient)

	connected, err := client.IsConnected()
	assert.ErrorIs(t, err, ErrNilClient)
	assert.False(t, connected)
	assert.ErrorIs(t, client.LastRefreshError(), ErrNilClient)
}

func TestClient_StatusLifecycle(t *testing.T) {
	mr := miniredis.RunT(t)

	client, err := New(context.Background(), newStandaloneConfig(mr.Addr()))
	require.NoError(t, err)

	status, err := client.Status()
	require.NoError(t, err)
	assert.True(t, status.Connected)
	assert.Nil(t, status.LastRefreshError)

	require.NoError(t, client.Close())
	connected, err := client.IsConnected()
	require.NoError(t, err)
	assert.False(t, connected)
}

func TestClient_RefreshLoop_DoesNotDuplicateGoroutines(t *testing.T) {
	validCert := base64.StdEncoding.EncodeToString(generateTestCertificatePEM(t))
	normalized, err := normalizeConfig(Config{
		Topology: Topology{Standalone: &StandaloneTopology{Address: "127.0.0.1:6379"}},
		TLS:      &TLSConfig{CACertBase64: validCert},
		Auth: Auth{GCPIAM: &GCPIAMAuth{
			CredentialsBase64:       base64.StdEncoding.EncodeToString([]byte("{}")),
			ServiceAccount:          "svc@project.iam.gserviceaccount.com",
			RefreshEvery:            time.Millisecond,
			RefreshCheckInterval:    time.Millisecond,
			RefreshOperationTimeout: time.Second,
		}},
		Logger: &log.NopLogger{},
	})
	require.NoError(t, err)

	var calls int32
	client := &Client{
		cfg:    normalized,
		logger: normalized.Logger,
		tokenRetriever: func(ctx context.Context) (string, error) {
			atomic.AddInt32(&calls, 1)
			<-ctx.Done()

			return "", ctx.Err()
		},
		reconnectFn: func(context.Context) error { return nil },
	}

	client.mu.Lock()
	client.lastRefresh = time.Now().Add(-time.Hour)
	client.startRefreshLoopLocked()
	client.startRefreshLoopLocked()
	client.mu.Unlock()

	require.Eventually(t, func() bool {
		return atomic.LoadInt32(&calls) >= 1
	}, 200*time.Millisecond, 10*time.Millisecond)

	require.NoError(t, client.Close())
	assert.Equal(t, int32(1), atomic.LoadInt32(&calls))
}

func TestClient_RefreshStatusErrorAndRecovery(t *testing.T) {
	validCert := base64.StdEncoding.EncodeToString(generateTestCertificatePEM(t))
	normalized, err := normalizeConfig(Config{
		Topology: Topology{Standalone: &StandaloneTopology{Address: "127.0.0.1:6379"}},
		TLS:      &TLSConfig{CACertBase64: validCert},
		Auth: Auth{GCPIAM: &GCPIAMAuth{
			CredentialsBase64:       base64.StdEncoding.EncodeToString([]byte("{}")),
			ServiceAccount:          "svc@project.iam.gserviceaccount.com",
			RefreshEvery:            time.Millisecond,
			RefreshCheckInterval:    time.Millisecond,
			RefreshOperationTimeout: time.Second,
		}},
		Logger: &log.NopLogger{},
	})
	require.NoError(t, err)

	firstErr := errors.New("token refresh failed")
	var shouldFail atomic.Bool
	shouldFail.Store(true)

	client := &Client{
		cfg:    normalized,
		logger: normalized.Logger,
		tokenRetriever: func(context.Context) (string, error) {
			if shouldFail.Load() {
				return "", firstErr
			}

			return "token", nil
		},
		reconnectFn: func(context.Context) error { return nil },
	}

	client.mu.Lock()
	client.lastRefresh = time.Now().Add(-time.Hour)
	client.startRefreshLoopLocked()
	client.mu.Unlock()

	require.Eventually(t, func() bool {
		return errors.Is(client.LastRefreshError(), firstErr)
	}, 500*time.Millisecond, 10*time.Millisecond)

	shouldFail.Store(false)

	require.Eventually(t, func() bool {
		return client.LastRefreshError() == nil
	}, 500*time.Millisecond, 10*time.Millisecond)

	require.NoError(t, client.Close())
}

func TestClient_RefreshTick_ReconnectFailureReturnsFalse(t *testing.T) {
	validCert := base64.StdEncoding.EncodeToString(generateTestCertificatePEM(t))
	normalized, err := normalizeConfig(Config{
		Topology: Topology{Standalone: &StandaloneTopology{Address: "127.0.0.1:6379"}},
		TLS:      &TLSConfig{CACertBase64: validCert},
		Auth: Auth{GCPIAM: &GCPIAMAuth{
			CredentialsBase64:       base64.StdEncoding.EncodeToString([]byte("{}")),
			ServiceAccount:          "svc@project.iam.gserviceaccount.com",
			RefreshEvery:            time.Millisecond,
			RefreshCheckInterval:    time.Millisecond,
			RefreshOperationTimeout: time.Second,
		}},
		Logger: &log.NopLogger{},
	})
	require.NoError(t, err)

	reconnectErr := errors.New("simulated reconnect failure")
	initialRefresh := time.Now().Add(-time.Hour)

	client := &Client{
		cfg:    normalized,
		logger: normalized.Logger,
		token:  "old-token",
		tokenRetriever: func(context.Context) (string, error) {
			return "new-token", nil
		},
		reconnectFn: func(context.Context) error {
			return reconnectErr
		},
		lastRefresh: initialRefresh,
	}

	ok := client.refreshTick(context.Background(), normalized.Auth.GCPIAM)
	assert.False(t, ok)
	assert.ErrorIs(t, client.LastRefreshError(), reconnectErr)

	client.mu.RLock()
	defer client.mu.RUnlock()

	assert.Equal(t, "old-token", client.token)
	assert.Equal(t, initialRefresh, client.lastRefresh)
}

func TestClient_Connect_ReconnectClosesPreviousClient(t *testing.T) {
	mr := miniredis.RunT(t)

	client, err := New(context.Background(), newStandaloneConfig(mr.Addr()))
	require.NoError(t, err)
	t.Cleanup(func() {
		if closeErr := client.Close(); closeErr != nil {
			t.Errorf("cleanup: client close: %v", closeErr)
		}
	})

	firstClient, err := client.GetClient(context.Background())
	require.NoError(t, err)

	require.NoError(t, client.Connect(context.Background()))

	secondClient, err := client.GetClient(context.Background())
	require.NoError(t, err)
	assert.NotSame(t, firstClient, secondClient)

	_, err = firstClient.Ping(context.Background()).Result()
	assert.Error(t, err)
}

func TestClient_ReconnectFailure_PreservesOldClient(t *testing.T) {
	mr := miniredis.RunT(t)
	addr := mr.Addr() // capture address before closing

	// Connect a working standalone client (no IAM -- we test reconnect directly).
	client, err := New(context.Background(), newStandaloneConfig(addr))
	require.NoError(t, err)
	t.Cleanup(func() {
		if closeErr := client.Close(); closeErr != nil {
			t.Errorf("cleanup: client close: %v", closeErr)
		}
	})

	// Verify initial connectivity.
	rdb, err := client.GetClient(context.Background())
	require.NoError(t, err)
	require.NoError(t, rdb.Set(context.Background(), "preserve:key", "before", 0).Err())

	// Shut down miniredis so the new client Ping fails during reconnect.
	mr.Close()

	// Simulate a reconnect failure.
	client.mu.Lock()
	err = client.reconnectLocked(context.Background())
	client.mu.Unlock()

	// reconnectLocked must return an error (Ping against closed server fails).
	require.Error(t, err, "reconnectLocked should fail when new client cannot Ping")

	// The old client must still be set and marked connected.
	connected, err := client.IsConnected()
	require.NoError(t, err)
	assert.True(t, connected, "client must remain connected after failed reconnect")

	// Restart miniredis on the same address so the OLD preserved client can work again.
	mr2 := miniredis.NewMiniRedis()
	require.NoError(t, mr2.StartAddr(addr))
	t.Cleanup(mr2.Close)

	// The preserved old client must still be usable.
	rdb2, err := client.GetClient(context.Background())
	require.NoError(t, err)
	require.NoError(t, rdb2.Set(context.Background(), "preserve:key", "still-works", 0).Err())

	val, err := rdb2.Get(context.Background(), "preserve:key").Result()
	require.NoError(t, err)
	assert.Equal(t, "still-works", val)
}

func TestClient_ReconnectFailure_IAMRefreshLoopPreservesClient(t *testing.T) {
	validCert := base64.StdEncoding.EncodeToString(generateTestCertificatePEM(t))
	normalized, err := normalizeConfig(Config{
		Topology: Topology{Standalone: &StandaloneTopology{Address: "127.0.0.1:6379"}},
		TLS:      &TLSConfig{CACertBase64: validCert},
		Auth: Auth{GCPIAM: &GCPIAMAuth{
			CredentialsBase64:       base64.StdEncoding.EncodeToString([]byte("{}")),
			ServiceAccount:          "svc@project.iam.gserviceaccount.com",
			RefreshEvery:            time.Millisecond,
			RefreshCheckInterval:    time.Millisecond,
			RefreshOperationTimeout: time.Second,
		}},
		Logger: &log.NopLogger{},
	})
	require.NoError(t, err)

	reconnectErr := errors.New("simulated reconnect failure")
	var reconnectShouldFail atomic.Bool
	reconnectShouldFail.Store(true)

	var reconnectCalls atomic.Int32
	var tokenAtReconnect atomic.Value

	client := &Client{
		cfg:       normalized,
		logger:    normalized.Logger,
		connected: true,
		token:     "original-working-token",
		tokenRetriever: func(context.Context) (string, error) {
			return "new-refreshed-token", nil
		},
		reconnectFn: func(ctx context.Context) error {
			reconnectCalls.Add(1)

			// Capture the token at the time of reconnect attempt for verification.
			tokenAtReconnect.Store("called")

			if reconnectShouldFail.Load() {
				return reconnectErr
			}

			return nil
		},
	}

	client.mu.Lock()
	client.lastRefresh = time.Now().Add(-time.Hour)
	client.startRefreshLoopLocked()
	client.mu.Unlock()

	// Wait for at least one failed reconnect attempt.
	require.Eventually(t, func() bool {
		return reconnectCalls.Load() >= 1
	}, 500*time.Millisecond, 5*time.Millisecond)

	// Verify: the refresh error is recorded.
	require.Eventually(t, func() bool {
		return client.LastRefreshError() != nil
	}, 500*time.Millisecond, 5*time.Millisecond)
	assert.ErrorIs(t, client.LastRefreshError(), reconnectErr)

	// Verify: the token is rolled back to the original after failed reconnect.
	client.mu.RLock()
	currentToken := client.token
	client.mu.RUnlock()
	assert.Equal(t, "original-working-token", currentToken,
		"token must be rolled back to original after failed reconnect")

	// Now allow reconnect to succeed.
	reconnectShouldFail.Store(false)

	// Wait for recovery.
	require.Eventually(t, func() bool {
		return client.LastRefreshError() == nil
	}, 500*time.Millisecond, 5*time.Millisecond)

	// After successful reconnect, the new token should be in place.
	client.mu.RLock()
	recoveredToken := client.token
	client.mu.RUnlock()
	assert.Equal(t, "new-refreshed-token", recoveredToken,
		"token must be updated after successful reconnect")

	require.NoError(t, client.Close())
}

func TestClient_ReconnectSuccess_SwapsClient(t *testing.T) {
	mr := miniredis.RunT(t)

	client, err := New(context.Background(), newStandaloneConfig(mr.Addr()))
	require.NoError(t, err)
	t.Cleanup(func() {
		if closeErr := client.Close(); closeErr != nil {
			t.Errorf("cleanup: client close: %v", closeErr)
		}
	})

	// Grab reference to the original underlying client.
	rdb1, err := client.GetClient(context.Background())
	require.NoError(t, err)

	// Successful reconnect should swap the client.
	client.mu.Lock()
	err = client.reconnectLocked(context.Background())
	client.mu.Unlock()
	require.NoError(t, err)

	rdb2, err := client.GetClient(context.Background())
	require.NoError(t, err)

	// The client reference must have changed.
	assert.NotSame(t, rdb1, rdb2, "successful reconnect must swap to new client")

	// Old client must be closed.
	_, err = rdb1.Ping(context.Background()).Result()
	assert.Error(t, err, "old client must be closed after successful reconnect")

	// New client must work.
	require.NoError(t, rdb2.Set(context.Background(), "swap:key", "works", 0).Err())
	val, err := rdb2.Get(context.Background(), "swap:key").Result()
	require.NoError(t, err)
	assert.Equal(t, "works", val)

	connected, err := client.IsConnected()
	require.NoError(t, err)
	assert.True(t, connected)
}

func TestValidateTopology_Sentinel(t *testing.T) {
	tests := []struct {
		name    string
		topo    Topology
		errText string
	}{
		{
			name: "sentinel valid",
			topo: Topology{Sentinel: &SentinelTopology{
				Addresses:  []string{"127.0.0.1:26379"},
				MasterName: "mymaster",
			}},
		},
		{
			name: "sentinel missing addresses",
			topo: Topology{Sentinel: &SentinelTopology{
				MasterName: "mymaster",
			}},
			errText: "sentinel addresses are required",
		},
		{
			name: "sentinel missing master name",
			topo: Topology{Sentinel: &SentinelTopology{
				Addresses: []string{"127.0.0.1:26379"},
			}},
			errText: "sentinel master name is required",
		},
		{
			name: "sentinel empty address in list",
			topo: Topology{Sentinel: &SentinelTopology{
				Addresses:  []string{"127.0.0.1:26379", "  "},
				MasterName: "mymaster",
			}},
			errText: "sentinel addresses cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTopology(tt.topo)
			if tt.errText == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errText)
			}
		})
	}
}

func TestValidateTopology_Cluster(t *testing.T) {
	tests := []struct {
		name    string
		topo    Topology
		errText string
	}{
		{
			name: "cluster valid",
			topo: Topology{Cluster: &ClusterTopology{
				Addresses: []string{"127.0.0.1:7000", "127.0.0.1:7001"},
			}},
		},
		{
			name:    "cluster missing addresses",
			topo:    Topology{Cluster: &ClusterTopology{}},
			errText: "cluster addresses are required",
		},
		{
			name: "cluster empty address in list",
			topo: Topology{Cluster: &ClusterTopology{
				Addresses: []string{"127.0.0.1:7000", "   "},
			}},
			errText: "cluster addresses cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateTopology(tt.topo)
			if tt.errText == "" {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errText)
			}
		})
	}
}

func TestValidateTopology_StandaloneEmptyAddress(t *testing.T) {
	err := validateTopology(Topology{Standalone: &StandaloneTopology{Address: "   "}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "standalone address is required")
}

func TestValidateConfig_DualAuth(t *testing.T) {
	validCert := base64.StdEncoding.EncodeToString(generateTestCertificatePEM(t))
	_, err := normalizeConfig(Config{
		Topology: Topology{Standalone: &StandaloneTopology{Address: "127.0.0.1:6379"}},
		TLS:      &TLSConfig{CACertBase64: validCert},
		Auth: Auth{
			StaticPassword: &StaticPasswordAuth{Password: "pass"},
			GCPIAM: &GCPIAMAuth{
				CredentialsBase64: "abc",
				ServiceAccount:    "svc@project.iam.gserviceaccount.com",
			},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "only one auth strategy")
}

func TestNormalizeLoggerDefault_NilLogger(t *testing.T) {
	cfg := Config{}
	normalizeLoggerDefault(&cfg)
	require.NotNil(t, cfg.Logger)
}

func TestBuildUniversalOptionsLocked_Topologies(t *testing.T) {
	mr := miniredis.RunT(t)

	t.Run("sentinel topology", func(t *testing.T) {
		cfg, err := normalizeConfig(Config{
			Topology: Topology{Sentinel: &SentinelTopology{
				Addresses:  []string{mr.Addr()},
				MasterName: "mymaster",
			}},
		})
		require.NoError(t, err)

		c := &Client{cfg: cfg, logger: cfg.Logger}
		opts, err := c.buildUniversalOptionsLocked()
		require.NoError(t, err)
		assert.Equal(t, []string{mr.Addr()}, opts.Addrs)
		assert.Equal(t, "mymaster", opts.MasterName)
	})

	t.Run("cluster topology", func(t *testing.T) {
		cfg, err := normalizeConfig(Config{
			Topology: Topology{Cluster: &ClusterTopology{
				Addresses: []string{mr.Addr(), "127.0.0.1:7001"},
			}},
		})
		require.NoError(t, err)

		c := &Client{cfg: cfg, logger: cfg.Logger}
		opts, err := c.buildUniversalOptionsLocked()
		require.NoError(t, err)
		assert.Equal(t, []string{mr.Addr(), "127.0.0.1:7001"}, opts.Addrs)
	})

	t.Run("static password auth", func(t *testing.T) {
		cfg, err := normalizeConfig(Config{
			Topology: Topology{Standalone: &StandaloneTopology{Address: mr.Addr()}},
			Auth:     Auth{StaticPassword: &StaticPasswordAuth{Password: "secret"}},
		})
		require.NoError(t, err)

		c := &Client{cfg: cfg, logger: cfg.Logger}
		opts, err := c.buildUniversalOptionsLocked()
		require.NoError(t, err)
		assert.Equal(t, "secret", opts.Password)
	})

	t.Run("gcp iam auth sets username and token", func(t *testing.T) {
		validCert := base64.StdEncoding.EncodeToString(generateTestCertificatePEM(t))
		cfg, err := normalizeConfig(Config{
			Topology: Topology{Standalone: &StandaloneTopology{Address: mr.Addr()}},
			TLS:      &TLSConfig{CACertBase64: validCert},
			Auth: Auth{GCPIAM: &GCPIAMAuth{
				CredentialsBase64: base64.StdEncoding.EncodeToString([]byte("{}")),
				ServiceAccount:    "svc@project.iam.gserviceaccount.com",
			}},
		})
		require.NoError(t, err)

		c := &Client{cfg: cfg, logger: cfg.Logger, token: "test-token"}
		opts, err := c.buildUniversalOptionsLocked()
		require.NoError(t, err)
		assert.Equal(t, "default", opts.Username)
		assert.Equal(t, "test-token", opts.Password)
		assert.NotNil(t, opts.TLSConfig)
	})
}

func TestBuildUniversalOptionsLocked_NoTopology(t *testing.T) {
	c := &Client{logger: &log.NopLogger{}}
	_, err := c.buildUniversalOptionsLocked()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidConfig)
	assert.Contains(t, err.Error(), "no topology configured")
}

func TestClient_GetClient_NoTopology_ReturnsError(t *testing.T) {
	// A bare Client{} with no Config (e.g., constructed outside of New()) must
	// return an error from GetClient rather than silently connecting to localhost:6379.
	c := &Client{}
	_, err := c.GetClient(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidConfig)
}

func TestClient_GetClient_ReconnectsWhenNil(t *testing.T) {
	mr := miniredis.RunT(t)

	client, err := New(context.Background(), newStandaloneConfig(mr.Addr()))
	require.NoError(t, err)
	t.Cleanup(func() {
		if closeErr := client.Close(); closeErr != nil {
			t.Errorf("cleanup: client close: %v", closeErr)
		}
	})

	// Simulate a nil internal client to exercise the reconnect-on-demand path.
	client.mu.Lock()
	old := client.client
	client.client = nil
	client.mu.Unlock()

	// Close the old client manually.
	require.NotNil(t, old)
	require.NoError(t, old.Close())

	// GetClient should reconnect.
	rdb, err := client.GetClient(context.Background())
	require.NoError(t, err)
	require.NotNil(t, rdb)

	require.NoError(t, rdb.Set(context.Background(), "reconnect:key", "ok", 0).Err())
}

func TestClient_RetrieveToken_NilClient(t *testing.T) {
	var c *Client
	_, err := c.retrieveToken(context.Background())
	assert.ErrorIs(t, err, ErrNilClient)
}

func TestClient_RetrieveToken_NoGCPIAM(t *testing.T) {
	c := &Client{
		cfg:    Config{},
		logger: &log.NopLogger{},
	}
	_, err := c.retrieveToken(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GCP IAM auth is not configured")
}

func TestClient_RefreshTokenLoop_NilClient(t *testing.T) {
	var c *Client
	// Should return immediately without panic.
	c.refreshTokenLoop(context.Background())
}

func TestNormalizeConnectionOptionsDefaults(t *testing.T) {
	opts := ConnectionOptions{}
	normalizeConnectionOptionsDefaults(&opts)
	assert.Equal(t, 10, opts.PoolSize)
	assert.Equal(t, 3*time.Second, opts.ReadTimeout)
	assert.Equal(t, 3*time.Second, opts.WriteTimeout)
	assert.Equal(t, 5*time.Second, opts.DialTimeout)
	assert.Equal(t, 2*time.Second, opts.PoolTimeout)
	assert.Equal(t, 3, opts.MaxRetries)
	assert.Equal(t, 8*time.Millisecond, opts.MinRetryBackoff)
	assert.Equal(t, 1*time.Second, opts.MaxRetryBackoff)
}

func TestNormalizeConnectionOptionsDefaults_PreservesExisting(t *testing.T) {
	opts := ConnectionOptions{
		PoolSize:        20,
		ReadTimeout:     10 * time.Second,
		WriteTimeout:    10 * time.Second,
		DialTimeout:     10 * time.Second,
		PoolTimeout:     10 * time.Second,
		MaxRetries:      5,
		MinRetryBackoff: 100 * time.Millisecond,
		MaxRetryBackoff: 5 * time.Second,
	}
	normalizeConnectionOptionsDefaults(&opts)
	assert.Equal(t, 20, opts.PoolSize)
	assert.Equal(t, 10*time.Second, opts.ReadTimeout)
	assert.Equal(t, 5, opts.MaxRetries)
}

func TestNormalizeTLSDefaults(t *testing.T) {
	t.Run("nil config", func(t *testing.T) {
		upgraded, legacyAllowed := normalizeTLSDefaults(nil)
		assert.False(t, upgraded)
		assert.False(t, legacyAllowed)
	})

	t.Run("sets default min version", func(t *testing.T) {
		cfg := &TLSConfig{}
		upgraded, legacyAllowed := normalizeTLSDefaults(cfg)
		assert.Equal(t, uint16(tls.VersionTLS12), cfg.MinVersion)
		assert.True(t, upgraded)
		assert.False(t, legacyAllowed)
	})

	t.Run("preserves existing min version", func(t *testing.T) {
		cfg := &TLSConfig{MinVersion: tls.VersionTLS13}
		upgraded, legacyAllowed := normalizeTLSDefaults(cfg)
		assert.Equal(t, uint16(tls.VersionTLS13), cfg.MinVersion)
		assert.False(t, upgraded)
		assert.False(t, legacyAllowed)
	})

	t.Run("enforces tls1.2 minimum floor", func(t *testing.T) {
		cfg := &TLSConfig{MinVersion: tls.VersionTLS10}
		upgraded, legacyAllowed := normalizeTLSDefaults(cfg)
		assert.Equal(t, uint16(tls.VersionTLS12), cfg.MinVersion)
		assert.True(t, upgraded)
		assert.False(t, legacyAllowed)
	})

	t.Run("allows explicit legacy min version opt in", func(t *testing.T) {
		cfg := &TLSConfig{MinVersion: tls.VersionTLS10, AllowLegacyMinVersion: true}
		upgraded, legacyAllowed := normalizeTLSDefaults(cfg)
		assert.Equal(t, uint16(tls.VersionTLS10), cfg.MinVersion)
		assert.False(t, upgraded)
		assert.True(t, legacyAllowed)
	})
}

func TestNormalizeConfig_TLSUpgradeLogsWarning(t *testing.T) {
	validCert := base64.StdEncoding.EncodeToString(generateTestCertificatePEM(t))
	logger := &recordingLogger{}

	cfg, err := normalizeConfig(Config{
		Topology: Topology{Standalone: &StandaloneTopology{Address: "127.0.0.1:6379"}},
		TLS: &TLSConfig{
			CACertBase64: validCert,
			MinVersion:   tls.VersionTLS10,
		},
		Logger: logger,
	})
	require.NoError(t, err)
	require.NotNil(t, cfg.TLS)
	assert.Equal(t, uint16(tls.VersionTLS12), cfg.TLS.MinVersion)

	warnings := logger.warningMessages()
	require.NotEmpty(t, warnings)
	assert.Contains(t, warnings[0], "upgraded")
}

func TestNormalizeConfig_DefaultTLSMinVersionDoesNotLogWarning(t *testing.T) {
	validCert := base64.StdEncoding.EncodeToString(generateTestCertificatePEM(t))
	logger := &recordingLogger{}

	cfg, err := normalizeConfig(Config{
		Topology: Topology{Standalone: &StandaloneTopology{Address: "127.0.0.1:6379"}},
		TLS: &TLSConfig{
			CACertBase64: validCert,
		},
		Logger: logger,
	})
	require.NoError(t, err)
	require.NotNil(t, cfg.TLS)
	assert.Equal(t, uint16(tls.VersionTLS12), cfg.TLS.MinVersion)
	assert.Empty(t, logger.warningMessages())
}

func TestNormalizeConfig_LegacyTLSOptInLogsWarning(t *testing.T) {
	validCert := base64.StdEncoding.EncodeToString(generateTestCertificatePEM(t))
	logger := &recordingLogger{}

	cfg, err := normalizeConfig(Config{
		Topology: Topology{Standalone: &StandaloneTopology{Address: "127.0.0.1:6379"}},
		TLS: &TLSConfig{
			CACertBase64:          validCert,
			MinVersion:            tls.VersionTLS10,
			AllowLegacyMinVersion: true,
		},
		Logger: logger,
	})
	require.NoError(t, err)
	require.NotNil(t, cfg.TLS)
	assert.Equal(t, uint16(tls.VersionTLS10), cfg.TLS.MinVersion)

	warnings := logger.warningMessages()
	require.NotEmpty(t, warnings)
	assert.Contains(t, warnings[0], "retained")
}

func TestNormalizeGCPIAMDefaults(t *testing.T) {
	t.Run("nil auth", func(t *testing.T) {
		normalizeGCPIAMDefaults(nil) // should not panic
	})

	t.Run("sets defaults", func(t *testing.T) {
		auth := &GCPIAMAuth{}
		normalizeGCPIAMDefaults(auth)
		assert.Equal(t, defaultTokenLifetime, auth.TokenLifetime)
		assert.Equal(t, defaultRefreshEvery, auth.RefreshEvery)
		assert.Equal(t, defaultRefreshCheckInterval, auth.RefreshCheckInterval)
		assert.Equal(t, defaultRefreshOperationTimeout, auth.RefreshOperationTimeout)
	})

	t.Run("preserves existing", func(t *testing.T) {
		auth := &GCPIAMAuth{
			TokenLifetime:           2 * time.Hour,
			RefreshEvery:            30 * time.Minute,
			RefreshCheckInterval:    5 * time.Second,
			RefreshOperationTimeout: 10 * time.Second,
		}
		normalizeGCPIAMDefaults(auth)
		assert.Equal(t, 2*time.Hour, auth.TokenLifetime)
		assert.Equal(t, 30*time.Minute, auth.RefreshEvery)
	})
}

func TestNormalizeConnectionOptionsDefaults_PoolSizeCap(t *testing.T) {
	opts := ConnectionOptions{PoolSize: 5000}
	normalizeConnectionOptionsDefaults(&opts)
	assert.Equal(t, maxPoolSize, opts.PoolSize)
}

func TestNormalizeConnectionOptionsDefaults_PoolSizeAtCap(t *testing.T) {
	opts := ConnectionOptions{PoolSize: 1000}
	normalizeConnectionOptionsDefaults(&opts)
	assert.Equal(t, 1000, opts.PoolSize)
}

func TestValidateConfig_RefreshEveryExceedsTokenLifetime(t *testing.T) {
	validCert := base64.StdEncoding.EncodeToString(generateTestCertificatePEM(t))
	_, err := normalizeConfig(Config{
		Topology: Topology{Standalone: &StandaloneTopology{Address: "127.0.0.1:6379"}},
		TLS:      &TLSConfig{CACertBase64: validCert},
		Auth: Auth{GCPIAM: &GCPIAMAuth{
			CredentialsBase64: base64.StdEncoding.EncodeToString([]byte("{}")),
			ServiceAccount:    "svc@project.iam.gserviceaccount.com",
			TokenLifetime:     30 * time.Minute,
			RefreshEvery:      50 * time.Minute,
		}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RefreshEvery must be less than TokenLifetime")
}

func TestValidateConfig_RefreshEveryEqualsTokenLifetime(t *testing.T) {
	validCert := base64.StdEncoding.EncodeToString(generateTestCertificatePEM(t))
	_, err := normalizeConfig(Config{
		Topology: Topology{Standalone: &StandaloneTopology{Address: "127.0.0.1:6379"}},
		TLS:      &TLSConfig{CACertBase64: validCert},
		Auth: Auth{GCPIAM: &GCPIAMAuth{
			CredentialsBase64: base64.StdEncoding.EncodeToString([]byte("{}")),
			ServiceAccount:    "svc@project.iam.gserviceaccount.com",
			TokenLifetime:     1 * time.Hour,
			RefreshEvery:      1 * time.Hour,
		}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RefreshEvery must be less than TokenLifetime")
}

func TestStaticPasswordAuth_StringRedactsPassword(t *testing.T) {
	auth := StaticPasswordAuth{Password: "super-secret-password"}
	s := auth.String()
	assert.Contains(t, s, "REDACTED")
	assert.NotContains(t, s, "super-secret-password")

	gs := auth.GoString()
	assert.Contains(t, gs, "REDACTED")
	assert.NotContains(t, gs, "super-secret-password")
}

func TestGCPIAMAuth_StringRedactsCredentials(t *testing.T) {
	auth := GCPIAMAuth{
		CredentialsBase64: "c2VjcmV0LWtleS1tYXRlcmlhbA==",
		ServiceAccount:    "svc@project.iam.gserviceaccount.com",
	}
	s := auth.String()
	assert.Contains(t, s, "svc@project.iam.gserviceaccount.com")
	assert.Contains(t, s, "REDACTED")
	assert.NotContains(t, s, "c2VjcmV0LWtleS1tYXRlcmlhbA==")

	gs := auth.GoString()
	assert.Contains(t, gs, "REDACTED")
	assert.NotContains(t, gs, "c2VjcmV0LWtleS1tYXRlcmlhbA==")
}

func TestStaticPasswordAuth_FmtRedacts(t *testing.T) {
	auth := StaticPasswordAuth{Password: "my-password-123"}
	// fmt.Sprintf uses String()/GoString() methods
	assert.NotContains(t, fmt.Sprintf("%v", auth), "my-password-123")
	assert.NotContains(t, fmt.Sprintf("%s", auth), "my-password-123")
	assert.NotContains(t, fmt.Sprintf("%#v", auth), "my-password-123")
}

func TestGCPIAMAuth_FmtRedacts(t *testing.T) {
	auth := GCPIAMAuth{
		CredentialsBase64: "secret-base64-content",
		ServiceAccount:    "svc@project.iam.gserviceaccount.com",
	}
	assert.NotContains(t, fmt.Sprintf("%v", auth), "secret-base64-content")
	assert.NotContains(t, fmt.Sprintf("%s", auth), "secret-base64-content")
	assert.NotContains(t, fmt.Sprintf("%#v", auth), "secret-base64-content")
}

func TestSetPackageLogger_NilDefaultsToNop(t *testing.T) {
	// Should not panic with nil
	SetPackageLogger(nil)
	logger := resolvePackageLogger()
	require.NotNil(t, logger)

	// Reset to NopLogger
	SetPackageLogger(&log.NopLogger{})
}

func TestSetPackageLogger_CustomLogger(t *testing.T) {
	SetPackageLogger(&log.NopLogger{})
	logger := resolvePackageLogger()
	require.NotNil(t, logger)
}

func TestClient_RefreshTokenLoop_NilGCPIAM(t *testing.T) {
	// refreshTokenLoop with non-nil client but nil GCPIAM should return immediately.
	c := &Client{
		cfg:    Config{},
		logger: &log.NopLogger{},
	}
	// Should return immediately without panic.
	c.refreshTokenLoop(context.Background())
}

func generateTestCertificatePEM(t *testing.T) []byte {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "redis-test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &privateKey.PublicKey, privateKey)
	require.NoError(t, err)

	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
}
