//go:build unit

package mongo

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
	"math/big"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func withDeps(deps clientDeps) Option {
	return func(current *clientDeps) {
		*current = deps
	}
}

func baseConfig() Config {
	return Config{
		URI:      "mongodb://localhost:27017",
		Database: "app",
	}
}

func successDeps() clientDeps {
	fakeClient := &mongo.Client{}

	return clientDeps{
		connect: func(context.Context, *options.ClientOptions) (*mongo.Client, error) {
			return fakeClient, nil
		},
		ping:       func(context.Context, *mongo.Client) error { return nil },
		disconnect: func(context.Context, *mongo.Client) error { return nil },
		createIndex: func(context.Context, *mongo.Client, string, string, mongo.IndexModel) error {
			return nil
		},
	}
}

func newTestClient(t *testing.T, overrides *clientDeps) *Client {
	t.Helper()

	deps := successDeps()
	if overrides != nil {
		if overrides.connect != nil {
			deps.connect = overrides.connect
		}

		if overrides.ping != nil {
			deps.ping = overrides.ping
		}

		if overrides.disconnect != nil {
			deps.disconnect = overrides.disconnect
		}

		if overrides.createIndex != nil {
			deps.createIndex = overrides.createIndex
		}
	}

	client, err := NewClient(context.Background(), baseConfig(), withDeps(deps))
	require.NoError(t, err)

	return client
}

// spyLogger implements log.Logger and records messages for verification.
type spyLogger struct {
	mu       sync.Mutex
	messages []string
	levels   []log.Level
}

func (s *spyLogger) Log(_ context.Context, level log.Level, msg string, _ ...log.Field) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.messages = append(s.messages, msg)
	s.levels = append(s.levels, level)
}

func (s *spyLogger) With(_ ...log.Field) log.Logger { return s }
func (s *spyLogger) WithGroup(_ string) log.Logger  { return s }
func (s *spyLogger) Enabled(_ log.Level) bool       { return true }
func (s *spyLogger) Sync(_ context.Context) error   { return nil }

func generateTestCertificatePEM(t *testing.T) []byte {
	t.Helper()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "mongo-test-ca"},
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

// ---------------------------------------------------------------------------
// NewClient tests
// ---------------------------------------------------------------------------

func TestNewClient_ValidatesInput(t *testing.T) {
	t.Parallel()

	t.Run("nil_context", func(t *testing.T) {
		t.Parallel()

		client, err := NewClient(nil, baseConfig())
		assert.Nil(t, client)
		assert.ErrorIs(t, err, ErrNilContext)
	})

	t.Run("empty_uri", func(t *testing.T) {
		t.Parallel()

		cfg := baseConfig()
		cfg.URI = ""

		client, err := NewClient(context.Background(), cfg)
		assert.Nil(t, client)
		assert.ErrorIs(t, err, ErrEmptyURI)
	})

	t.Run("empty_database", func(t *testing.T) {
		t.Parallel()

		cfg := baseConfig()
		cfg.Database = "  "

		client, err := NewClient(context.Background(), cfg)
		assert.Nil(t, client)
		assert.ErrorIs(t, err, ErrEmptyDatabaseName)
	})
}

func TestNewClient_ConnectAndPingFailures(t *testing.T) {
	t.Parallel()

	t.Run("connect_failure", func(t *testing.T) {
		t.Parallel()

		deps := clientDeps{
			connect: func(context.Context, *options.ClientOptions) (*mongo.Client, error) {
				return nil, errors.New("dial failed")
			},
			ping:       func(context.Context, *mongo.Client) error { return nil },
			disconnect: func(context.Context, *mongo.Client) error { return nil },
			createIndex: func(context.Context, *mongo.Client, string, string, mongo.IndexModel) error {
				return nil
			},
		}

		client, err := NewClient(context.Background(), baseConfig(), withDeps(deps))
		assert.Nil(t, client)
		assert.ErrorIs(t, err, ErrConnect)
	})

	t.Run("nil_client_returned", func(t *testing.T) {
		t.Parallel()

		deps := clientDeps{
			connect: func(context.Context, *options.ClientOptions) (*mongo.Client, error) {
				return nil, nil
			},
			ping:       func(context.Context, *mongo.Client) error { return nil },
			disconnect: func(context.Context, *mongo.Client) error { return nil },
			createIndex: func(context.Context, *mongo.Client, string, string, mongo.IndexModel) error {
				return nil
			},
		}

		client, err := NewClient(context.Background(), baseConfig(), withDeps(deps))
		assert.Nil(t, client)
		assert.ErrorIs(t, err, ErrNilMongoClient)
	})

	t.Run("ping_failure_disconnects", func(t *testing.T) {
		t.Parallel()

		fakeClient := &mongo.Client{}
		var disconnectCalls atomic.Int32

		deps := clientDeps{
			connect: func(context.Context, *options.ClientOptions) (*mongo.Client, error) {
				return fakeClient, nil
			},
			ping: func(context.Context, *mongo.Client) error {
				return errors.New("ping failed")
			},
			disconnect: func(context.Context, *mongo.Client) error {
				disconnectCalls.Add(1)
				return nil
			},
			createIndex: func(context.Context, *mongo.Client, string, string, mongo.IndexModel) error {
				return nil
			},
		}

		client, err := NewClient(context.Background(), baseConfig(), withDeps(deps))
		assert.Nil(t, client)
		assert.ErrorIs(t, err, ErrPing)
		assert.EqualValues(t, 1, disconnectCalls.Load())
	})
}

func TestNewClient_NilOptionIsSkipped(t *testing.T) {
	t.Parallel()

	deps := successDeps()
	client, err := NewClient(context.Background(), baseConfig(), nil, withDeps(deps))
	require.NoError(t, err)
	assert.NotNil(t, client)
}

func TestNewClient_NilDependencyRejected(t *testing.T) {
	t.Parallel()

	nilConnect := func(d *clientDeps) { d.connect = nil }
	_, err := NewClient(context.Background(), baseConfig(), nilConnect)
	assert.ErrorIs(t, err, ErrNilDependency)
}

func TestNewClient_ClearsURIAfterConnect(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, nil)
	assert.Empty(t, client.cfg.URI, "URI should be cleared from cfg after connect")
	assert.NotEmpty(t, client.uri, "private uri should be preserved")
}

// ---------------------------------------------------------------------------
// Connect tests
// ---------------------------------------------------------------------------

func TestClient_ConnectIsIdempotent(t *testing.T) {
	t.Parallel()

	fakeClient := &mongo.Client{}
	var connectCalls atomic.Int32

	deps := clientDeps{
		connect: func(context.Context, *options.ClientOptions) (*mongo.Client, error) {
			connectCalls.Add(1)
			return fakeClient, nil
		},
		ping:       func(context.Context, *mongo.Client) error { return nil },
		disconnect: func(context.Context, *mongo.Client) error { return nil },
		createIndex: func(context.Context, *mongo.Client, string, string, mongo.IndexModel) error {
			return nil
		},
	}

	client, err := NewClient(context.Background(), baseConfig(), withDeps(deps))
	require.NoError(t, err)

	assert.NoError(t, client.Connect(context.Background()))
	assert.EqualValues(t, 1, connectCalls.Load())
}

func TestClient_Connect_Guards(t *testing.T) {
	t.Parallel()

	t.Run("nil_receiver", func(t *testing.T) {
		t.Parallel()

		var c *Client
		assert.ErrorIs(t, c.Connect(context.Background()), ErrNilClient)
	})

	t.Run("nil_context_on_closed_client", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t, nil)
		require.NoError(t, client.Close(context.Background()))
		assert.ErrorIs(t, client.Connect(nil), ErrNilContext)
	})
}

func TestClient_Connect_ConfigPropagation(t *testing.T) {
	t.Parallel()

	fakeClient := &mongo.Client{}
	var capturedOpts *options.ClientOptions

	cfg := baseConfig()
	cfg.MaxPoolSize = 42
	cfg.ServerSelectionTimeout = 3 * time.Second
	cfg.HeartbeatInterval = 7 * time.Second

	deps := clientDeps{
		connect: func(_ context.Context, opts *options.ClientOptions) (*mongo.Client, error) {
			capturedOpts = opts
			return fakeClient, nil
		},
		ping:       func(context.Context, *mongo.Client) error { return nil },
		disconnect: func(context.Context, *mongo.Client) error { return nil },
		createIndex: func(context.Context, *mongo.Client, string, string, mongo.IndexModel) error {
			return nil
		},
	}

	_, err := NewClient(context.Background(), cfg, withDeps(deps))
	require.NoError(t, err)
	assert.NotNil(t, capturedOpts)
}

// ---------------------------------------------------------------------------
// Client and Database tests
// ---------------------------------------------------------------------------

func TestClient_ClientAndDatabase(t *testing.T) {
	t.Parallel()

	fakeClient := &mongo.Client{}
	deps := clientDeps{
		connect: func(context.Context, *options.ClientOptions) (*mongo.Client, error) {
			return fakeClient, nil
		},
		ping:       func(context.Context, *mongo.Client) error { return nil },
		disconnect: func(context.Context, *mongo.Client) error { return nil },
		createIndex: func(context.Context, *mongo.Client, string, string, mongo.IndexModel) error {
			return nil
		},
	}

	client, err := NewClient(context.Background(), baseConfig(), withDeps(deps))
	require.NoError(t, err)

	t.Run("nil_context", func(t *testing.T) {
		t.Parallel()

		mongoClient, callErr := client.Client(nil)
		assert.Nil(t, mongoClient)
		assert.ErrorIs(t, callErr, ErrNilContext)
	})

	t.Run("database_name", func(t *testing.T) {
		t.Parallel()

		databaseName, err := client.DatabaseName()
		require.NoError(t, err)
		assert.Equal(t, "app", databaseName)
	})

	t.Run("database_returns_handle", func(t *testing.T) {
		t.Parallel()

		db, callErr := client.Database(context.Background())
		require.NoError(t, callErr)
		assert.Equal(t, "app", db.Name())
	})
}

// ---------------------------------------------------------------------------
// Ping tests
// ---------------------------------------------------------------------------

func TestClient_Ping(t *testing.T) {
	t.Parallel()

	t.Run("nil_receiver", func(t *testing.T) {
		t.Parallel()

		var c *Client
		assert.ErrorIs(t, c.Ping(context.Background()), ErrNilClient)
	})

	t.Run("nil_context", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t, nil)
		assert.ErrorIs(t, client.Ping(nil), ErrNilContext)
	})

	t.Run("success", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t, nil)
		assert.NoError(t, client.Ping(context.Background()))
	})

	t.Run("wraps_ping_error", func(t *testing.T) {
		t.Parallel()

		var pingCount atomic.Int32
		deps := successDeps()
		deps.ping = func(context.Context, *mongo.Client) error {
			if pingCount.Add(1) == 1 {
				return nil // first ping (from Connect) succeeds
			}

			return errors.New("network timeout")
		}

		client := newTestClient(t, &deps)

		err := client.Ping(context.Background())
		assert.ErrorIs(t, err, ErrPing)
	})

	t.Run("closed_client", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t, nil)
		require.NoError(t, client.Close(context.Background()))
		assert.ErrorIs(t, client.Ping(context.Background()), ErrClientClosed)
	})
}

// ---------------------------------------------------------------------------
// Close tests
// ---------------------------------------------------------------------------

func TestClient_Close(t *testing.T) {
	t.Parallel()

	t.Run("nil_receiver", func(t *testing.T) {
		t.Parallel()

		var client *Client
		assert.ErrorIs(t, client.Close(context.Background()), ErrNilClient)
	})

	t.Run("nil_context", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t, nil)
		assert.ErrorIs(t, client.Close(nil), ErrNilContext)
	})

	t.Run("disconnect_failure_clears_client", func(t *testing.T) {
		t.Parallel()

		deps := successDeps()
		deps.disconnect = func(context.Context, *mongo.Client) error {
			return errors.New("disconnect failed")
		}

		client := newTestClient(t, &deps)

		err := client.Close(context.Background())
		assert.ErrorIs(t, err, ErrDisconnect)

		mongoClient, callErr := client.Client(context.Background())
		assert.Nil(t, mongoClient)
		assert.ErrorIs(t, callErr, ErrClientClosed)
	})

	t.Run("close_is_idempotent", func(t *testing.T) {
		t.Parallel()

		var disconnectCalls atomic.Int32
		deps := successDeps()
		deps.disconnect = func(context.Context, *mongo.Client) error {
			disconnectCalls.Add(1)
			return nil
		}

		client := newTestClient(t, &deps)

		require.NoError(t, client.Close(context.Background()))
		require.NoError(t, client.Close(context.Background()))
		assert.EqualValues(t, 1, disconnectCalls.Load())
	})
}

// ---------------------------------------------------------------------------
// EnsureIndexes tests
// ---------------------------------------------------------------------------

func TestClient_EnsureIndexes(t *testing.T) {
	t.Parallel()

	t.Run("nil_receiver", func(t *testing.T) {
		t.Parallel()

		var c *Client
		err := c.EnsureIndexes(context.Background(), "users", mongo.IndexModel{Keys: bson.D{{Key: "a", Value: 1}}})
		assert.ErrorIs(t, err, ErrNilClient)
	})

	t.Run("nil_context", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t, nil)
		err := client.EnsureIndexes(nil, "users", mongo.IndexModel{Keys: bson.D{{Key: "tenant_id", Value: 1}}})
		assert.ErrorIs(t, err, ErrNilContext)
	})

	t.Run("empty_collection", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t, nil)
		err := client.EnsureIndexes(context.Background(), " ", mongo.IndexModel{Keys: bson.D{{Key: "tenant_id", Value: 1}}})
		assert.ErrorIs(t, err, ErrEmptyCollectionName)
	})

	t.Run("empty_indexes", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t, nil)
		err := client.EnsureIndexes(context.Background(), "users")
		assert.ErrorIs(t, err, ErrEmptyIndexes)
	})

	t.Run("creates_all_indexes", func(t *testing.T) {
		t.Parallel()

		fakeClient := &mongo.Client{}
		var createCalls atomic.Int32

		deps := clientDeps{
			connect: func(context.Context, *options.ClientOptions) (*mongo.Client, error) {
				return fakeClient, nil
			},
			ping:       func(context.Context, *mongo.Client) error { return nil },
			disconnect: func(context.Context, *mongo.Client) error { return nil },
			createIndex: func(_ context.Context, client *mongo.Client, database, collection string, index mongo.IndexModel) error {
				createCalls.Add(1)
				assert.Same(t, fakeClient, client)
				assert.Equal(t, "app", database)
				assert.Equal(t, "users", collection)
				assert.NotNil(t, index.Keys)

				return nil
			},
		}

		client, err := NewClient(context.Background(), baseConfig(), withDeps(deps))
		require.NoError(t, err)

		err = client.EnsureIndexes(
			context.Background(),
			"users",
			mongo.IndexModel{Keys: bson.D{{Key: "tenant_id", Value: 1}}},
			mongo.IndexModel{Keys: bson.D{{Key: "created_at", Value: -1}}},
		)
		require.NoError(t, err)
		assert.EqualValues(t, 2, createCalls.Load())
	})

	t.Run("wraps_create_index_error", func(t *testing.T) {
		t.Parallel()

		deps := successDeps()
		deps.createIndex = func(context.Context, *mongo.Client, string, string, mongo.IndexModel) error {
			return errors.New("duplicate options")
		}

		client := newTestClient(t, &deps)

		err := client.EnsureIndexes(context.Background(), "users", mongo.IndexModel{Keys: bson.D{{Key: "tenant_id", Value: 1}}})
		assert.ErrorIs(t, err, ErrCreateIndex)
	})

	t.Run("batches_multiple_errors", func(t *testing.T) {
		t.Parallel()

		var createCalls atomic.Int32
		deps := successDeps()
		deps.createIndex = func(context.Context, *mongo.Client, string, string, mongo.IndexModel) error {
			createCalls.Add(1)
			return errors.New("failed")
		}

		client := newTestClient(t, &deps)

		err := client.EnsureIndexes(context.Background(), "users",
			mongo.IndexModel{Keys: bson.D{{Key: "a", Value: 1}}},
			mongo.IndexModel{Keys: bson.D{{Key: "b", Value: 1}}},
			mongo.IndexModel{Keys: bson.D{{Key: "c", Value: 1}}},
		)
		assert.Error(t, err)
		assert.EqualValues(t, 3, createCalls.Load()) // all 3 attempted, not short-circuited
		assert.ErrorIs(t, err, ErrCreateIndex)
	})

	t.Run("partial_failure_continues", func(t *testing.T) {
		t.Parallel()

		var successCalls, failCalls atomic.Int32
		deps := successDeps()
		deps.createIndex = func(_ context.Context, _ *mongo.Client, _, _ string, idx mongo.IndexModel) error {
			keys := idx.Keys.(bson.D)
			if keys[0].Key == "b" {
				failCalls.Add(1)
				return errors.New("duplicate")
			}

			successCalls.Add(1)

			return nil
		}

		client := newTestClient(t, &deps)

		err := client.EnsureIndexes(context.Background(), "users",
			mongo.IndexModel{Keys: bson.D{{Key: "a", Value: 1}}},
			mongo.IndexModel{Keys: bson.D{{Key: "b", Value: 1}}},
			mongo.IndexModel{Keys: bson.D{{Key: "c", Value: 1}}},
		)
		assert.Error(t, err)
		assert.EqualValues(t, 2, successCalls.Load())
		assert.EqualValues(t, 1, failCalls.Load())
	})

	t.Run("context_cancellation_stops_loop", func(t *testing.T) {
		t.Parallel()

		var calls atomic.Int32
		deps := successDeps()
		deps.createIndex = func(context.Context, *mongo.Client, string, string, mongo.IndexModel) error {
			calls.Add(1)
			return nil
		}

		client := newTestClient(t, &deps)

		ctx, cancel := context.WithCancel(context.Background())
		cancel() // cancel immediately

		err := client.EnsureIndexes(ctx, "users",
			mongo.IndexModel{Keys: bson.D{{Key: "a", Value: 1}}},
			mongo.IndexModel{Keys: bson.D{{Key: "b", Value: 1}}},
		)
		assert.Error(t, err)
		assert.ErrorIs(t, err, ErrCreateIndex)
		assert.EqualValues(t, 0, calls.Load()) // no indexes attempted
	})

	t.Run("closed_client", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t, nil)
		require.NoError(t, client.Close(context.Background()))

		err := client.EnsureIndexes(context.Background(), "users", mongo.IndexModel{Keys: bson.D{{Key: "a", Value: 1}}})
		assert.ErrorIs(t, err, ErrClientClosed)
	})
}

// ---------------------------------------------------------------------------
// Concurrency tests
// ---------------------------------------------------------------------------

func TestClient_ConcurrentClientReads(t *testing.T) {
	t.Parallel()

	fakeClient := &mongo.Client{}
	deps := clientDeps{
		connect: func(context.Context, *options.ClientOptions) (*mongo.Client, error) {
			return fakeClient, nil
		},
		ping:       func(context.Context, *mongo.Client) error { return nil },
		disconnect: func(context.Context, *mongo.Client) error { return nil },
		createIndex: func(context.Context, *mongo.Client, string, string, mongo.IndexModel) error {
			return nil
		},
	}

	client, err := NewClient(context.Background(), baseConfig(), withDeps(deps))
	require.NoError(t, err)

	const workers = 50
	results := make([]*mongo.Client, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup

	for i := 0; i < workers; i++ {
		wg.Add(1)

		go func(idx int) {
			defer wg.Done()
			results[idx], errs[idx] = client.Client(context.Background())
		}(i)
	}

	wg.Wait()

	for i := 0; i < workers; i++ {
		assert.NoError(t, errs[i])
		assert.Same(t, fakeClient, results[i])
	}
}

// ---------------------------------------------------------------------------
// Logging tests
// ---------------------------------------------------------------------------

func TestClient_LogsOnConnectFailure(t *testing.T) {
	t.Parallel()

	spy := &spyLogger{}
	cfg := baseConfig()
	cfg.Logger = spy

	deps := clientDeps{
		connect: func(context.Context, *options.ClientOptions) (*mongo.Client, error) {
			return nil, errors.New("dial failed")
		},
		ping:       func(context.Context, *mongo.Client) error { return nil },
		disconnect: func(context.Context, *mongo.Client) error { return nil },
		createIndex: func(context.Context, *mongo.Client, string, string, mongo.IndexModel) error {
			return nil
		},
	}

	_, _ = NewClient(context.Background(), cfg, withDeps(deps))

	spy.mu.Lock()
	defer spy.mu.Unlock()

	require.NotEmpty(t, spy.messages)
	assert.Equal(t, "mongo connect failed", spy.messages[0])
}

func TestClient_LogsNonTLSWarning(t *testing.T) {
	t.Parallel()

	spy := &spyLogger{}
	cfg := baseConfig()
	cfg.Logger = spy

	client := newTestClientWithLogger(t, nil, spy)
	_ = client // verify no panic, warning was logged during construction

	spy.mu.Lock()
	defer spy.mu.Unlock()

	found := false

	for _, msg := range spy.messages {
		if msg == "mongo connection established without TLS; consider configuring TLS for production use" {
			found = true
			break
		}
	}

	assert.True(t, found, "expected non-TLS warning in log messages, got: %v", spy.messages)
}

func newTestClientWithLogger(t *testing.T, overrides *clientDeps, logger log.Logger) *Client {
	t.Helper()

	deps := successDeps()
	if overrides != nil {
		if overrides.connect != nil {
			deps.connect = overrides.connect
		}

		if overrides.ping != nil {
			deps.ping = overrides.ping
		}

		if overrides.disconnect != nil {
			deps.disconnect = overrides.disconnect
		}

		if overrides.createIndex != nil {
			deps.createIndex = overrides.createIndex
		}
	}

	cfg := baseConfig()
	cfg.Logger = logger

	client, err := NewClient(context.Background(), cfg, withDeps(deps))
	require.NoError(t, err)

	return client
}

// ---------------------------------------------------------------------------
// indexKeysString tests
// ---------------------------------------------------------------------------

func TestIndexKeysString(t *testing.T) {
	t.Parallel()

	t.Run("bson_d_preserves_order", func(t *testing.T) {
		t.Parallel()

		keys := bson.D{{Key: "tenant_id", Value: 1}, {Key: "created_at", Value: -1}}
		assert.Equal(t, "tenant_id,created_at", indexKeysString(keys))
	})

	t.Run("bson_m_is_sorted", func(t *testing.T) {
		t.Parallel()

		keys := bson.M{"zeta": 1, "alpha": 1, "middle": -1}
		assert.Equal(t, "alpha,middle,zeta", indexKeysString(keys))
	})

	t.Run("unknown_type", func(t *testing.T) {
		t.Parallel()

		assert.Equal(t, "<unknown>", indexKeysString(42))
	})

	t.Run("nil_keys", func(t *testing.T) {
		t.Parallel()

		assert.Equal(t, "<unknown>", indexKeysString(nil))
	})

	t.Run("empty_bson_d", func(t *testing.T) {
		t.Parallel()

		assert.Equal(t, "", indexKeysString(bson.D{}))
	})

	t.Run("empty_bson_m", func(t *testing.T) {
		t.Parallel()

		assert.Equal(t, "", indexKeysString(bson.M{}))
	})
}

// ---------------------------------------------------------------------------
// Normalization tests
// ---------------------------------------------------------------------------

func TestNormalizeConfig(t *testing.T) {
	t.Parallel()

	t.Run("clamps_pool_size", func(t *testing.T) {
		t.Parallel()

		cfg := normalizeConfig(Config{MaxPoolSize: 9999})
		assert.EqualValues(t, maxMaxPoolSize, cfg.MaxPoolSize)
	})

	t.Run("preserves_valid_pool_size", func(t *testing.T) {
		t.Parallel()

		cfg := normalizeConfig(Config{MaxPoolSize: 50})
		assert.EqualValues(t, 50, cfg.MaxPoolSize)
	})

	t.Run("pool_size_at_cap", func(t *testing.T) {
		t.Parallel()

		cfg := normalizeConfig(Config{MaxPoolSize: maxMaxPoolSize})
		assert.EqualValues(t, maxMaxPoolSize, cfg.MaxPoolSize)
	})
}

func TestNormalizeTLSDefaults(t *testing.T) {
	t.Parallel()

	t.Run("nil_config", func(t *testing.T) {
		t.Parallel()

		normalizeTLSDefaults(nil) // should not panic
	})

	t.Run("sets_default_min_version", func(t *testing.T) {
		t.Parallel()

		cfg := &TLSConfig{}
		normalizeTLSDefaults(cfg)
		assert.Equal(t, uint16(tls.VersionTLS12), cfg.MinVersion)
	})

	t.Run("preserves_tls13", func(t *testing.T) {
		t.Parallel()

		cfg := &TLSConfig{MinVersion: tls.VersionTLS13}
		normalizeTLSDefaults(cfg)
		assert.Equal(t, uint16(tls.VersionTLS13), cfg.MinVersion)
	})

	t.Run("preserves_explicit_insecure_version", func(t *testing.T) {
		t.Parallel()

		// normalizeTLSDefaults only sets defaults for unspecified (zero) values.
		// Explicit versions are preserved for downstream validation in buildTLSConfig.
		cfg := &TLSConfig{MinVersion: tls.VersionTLS10}
		normalizeTLSDefaults(cfg)
		assert.Equal(t, uint16(tls.VersionTLS10), cfg.MinVersion)
	})
}

// ---------------------------------------------------------------------------
// TLS tests
// ---------------------------------------------------------------------------

func TestBuildTLSConfig(t *testing.T) {
	t.Parallel()

	t.Run("invalid_base64", func(t *testing.T) {
		t.Parallel()

		_, err := buildTLSConfig(TLSConfig{CACertBase64: "not-valid-base64!!!"})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "decoding CA cert")
	})

	t.Run("valid_base64_invalid_pem", func(t *testing.T) {
		t.Parallel()

		invalidPEM := base64.StdEncoding.EncodeToString([]byte("not a PEM certificate"))
		_, err := buildTLSConfig(TLSConfig{CACertBase64: invalidPEM})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "adding CA cert to pool failed")
	})

	t.Run("valid_cert_tls12", func(t *testing.T) {
		t.Parallel()

		certPEM := generateTestCertificatePEM(t)
		encoded := base64.StdEncoding.EncodeToString(certPEM)

		cfg, err := buildTLSConfig(TLSConfig{
			CACertBase64: encoded,
			MinVersion:   tls.VersionTLS12,
		})
		require.NoError(t, err)
		assert.Equal(t, uint16(tls.VersionTLS12), cfg.MinVersion)
		assert.NotNil(t, cfg.RootCAs)
	})

	t.Run("valid_cert_tls13", func(t *testing.T) {
		t.Parallel()

		certPEM := generateTestCertificatePEM(t)
		encoded := base64.StdEncoding.EncodeToString(certPEM)

		cfg, err := buildTLSConfig(TLSConfig{
			CACertBase64: encoded,
			MinVersion:   tls.VersionTLS13,
		})
		require.NoError(t, err)
		assert.Equal(t, uint16(tls.VersionTLS13), cfg.MinVersion)
	})

	t.Run("unsupported_version_returns_error", func(t *testing.T) {
		t.Parallel()

		certPEM := generateTestCertificatePEM(t)
		encoded := base64.StdEncoding.EncodeToString(certPEM)

		_, err := buildTLSConfig(TLSConfig{
			CACertBase64: encoded,
			MinVersion:   tls.VersionTLS10,
		})
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidConfig)
	})

	t.Run("zero_version_defaults_to_tls12", func(t *testing.T) {
		t.Parallel()

		certPEM := generateTestCertificatePEM(t)
		encoded := base64.StdEncoding.EncodeToString(certPEM)

		cfg, err := buildTLSConfig(TLSConfig{
			CACertBase64: encoded,
			MinVersion:   0,
		})
		require.NoError(t, err)
		assert.Equal(t, uint16(tls.VersionTLS12), cfg.MinVersion)
	})
}

func TestConfig_Validate_TLS(t *testing.T) {
	t.Parallel()

	t.Run("tls_requires_ca_cert", func(t *testing.T) {
		t.Parallel()

		cfg := Config{URI: "mongodb://localhost", Database: "db", TLS: &TLSConfig{}}
		err := cfg.validate()
		assert.ErrorIs(t, err, ErrInvalidConfig)
	})

	t.Run("tls_with_valid_cert_passes", func(t *testing.T) {
		t.Parallel()

		certPEM := generateTestCertificatePEM(t)
		encoded := base64.StdEncoding.EncodeToString(certPEM)

		cfg := Config{URI: "mongodb://localhost", Database: "db", TLS: &TLSConfig{CACertBase64: encoded}}
		err := cfg.validate()
		assert.NoError(t, err)
	})
}

func TestIsTLSImplied(t *testing.T) {
	t.Parallel()

	assert.True(t, isTLSImplied("mongodb+srv://cluster.mongodb.net"))
	assert.True(t, isTLSImplied("mongodb://host:27017/?tls=true"))
	assert.True(t, isTLSImplied("mongodb://host:27017/?ssl=true"))
	assert.False(t, isTLSImplied("mongodb://localhost:27017"))
}
