//go:build unit

package rabbitmq

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons"
	"github.com/LerianStudio/lib-commons/v5/commons/log"
	amqp "github.com/rabbitmq/amqp091-go"
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

func TestRabbitMQConnection_Connect(t *testing.T) {
	t.Parallel()

	t.Run("nil receiver", func(t *testing.T) {
		t.Parallel()

		var conn *RabbitMQConnection

		err := conn.ConnectContext(context.Background())
		assert.ErrorIs(t, err, ErrNilConnection)
	})

	t.Run("context canceled before connect", func(t *testing.T) {
		t.Parallel()

		dialerCalls := 0

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		conn := &RabbitMQConnection{
			ConnectionStringSource: "amqp://guest:guest@localhost:5672",
			Logger:                 &log.NopLogger{},
			dialerContext: func(context.Context, string) (*amqp.Connection, error) {
				dialerCalls++

				return &amqp.Connection{}, nil
			},
		}

		err := conn.ConnectContext(ctx)

		assert.ErrorIs(t, err, context.Canceled)
		assert.Equal(t, 0, dialerCalls)
	})

	t.Run("dial error", func(t *testing.T) {
		t.Parallel()

		dialerCalls := 0

		conn := &RabbitMQConnection{
			ConnectionStringSource: "amqp://guest:guest@localhost:5672",
			Logger:                 &log.NopLogger{},
			dialer: func(string) (*amqp.Connection, error) {
				dialerCalls++

				return nil, errors.New("dial failed")
			},
		}

		err := conn.Connect()

		assert.Error(t, err)
		assert.False(t, conn.Connected)
		assert.Nil(t, conn.Connection)
		assert.Nil(t, conn.Channel)
		assert.Equal(t, 1, dialerCalls)
		assert.ErrorContains(t, err, "dial failed")
	})

	t.Run("channel error closes connection", func(t *testing.T) {
		t.Parallel()

		dialerCalls := 0
		closeCalls := 0

		conn := &RabbitMQConnection{
			ConnectionStringSource: "amqp://guest:guest@localhost:5672",
			Logger:                 &log.NopLogger{},
			dialer: func(string) (*amqp.Connection, error) {
				dialerCalls++

				return &amqp.Connection{}, nil
			},
			channelFactory: func(*amqp.Connection) (*amqp.Channel, error) {
				return nil, errors.New("channel failed")
			},
			connectionCloser: func(*amqp.Connection) error {
				closeCalls++

				return nil
			},
		}

		err := conn.Connect()

		assert.Error(t, err)
		assert.False(t, conn.Connected)
		assert.Nil(t, conn.Connection)
		assert.Nil(t, conn.Channel)
		assert.Equal(t, 1, dialerCalls)
		assert.Equal(t, 1, closeCalls)
	})

	t.Run("health check failure resets connection", func(t *testing.T) {
		t.Parallel()

		dialerCalls := 0
		closeCalls := 0

		healthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, err := w.Write([]byte(`{"status":"error"}`))
			assert.NoError(t, err)
		}))
		defer healthServer.Close()

		conn := &RabbitMQConnection{
			ConnectionStringSource: "amqp://guest:guest@localhost:5672",
			HealthCheckURL:         healthServer.URL,
			Logger:                 &log.NopLogger{},
			dialer: func(string) (*amqp.Connection, error) {
				dialerCalls++

				return &amqp.Connection{}, nil
			},
			channelFactory: func(*amqp.Connection) (*amqp.Channel, error) {
				return &amqp.Channel{}, nil
			},
			connectionCloser: func(conn *amqp.Connection) error {
				closeCalls++

				return nil
			},
		}

		err := conn.Connect()

		assert.Error(t, err)
		assert.False(t, conn.Connected)
		assert.Nil(t, conn.Connection)
		assert.Nil(t, conn.Channel)
		assert.Equal(t, 1, dialerCalls)
		assert.Equal(t, 1, closeCalls)
	})

	t.Run("healthy server creates connection", func(t *testing.T) {
		t.Parallel()

		dialerCalls := 0
		healthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, err := w.Write([]byte(`{"status":"ok"}`))
			assert.NoError(t, err)
		}))
		defer healthServer.Close()

		conn := &RabbitMQConnection{
			ConnectionStringSource: "amqp://guest:guest@localhost:5672",
			HealthCheckURL:         healthServer.URL,
			Logger:                 &log.NopLogger{},
			dialer: func(string) (*amqp.Connection, error) {
				dialerCalls++

				return &amqp.Connection{}, nil
			},
			channelFactory: func(*amqp.Connection) (*amqp.Channel, error) {
				return &amqp.Channel{}, nil
			},
			connectionClosedFn: func(*amqp.Connection) bool { return false },
			channelClosedFn:    func(*amqp.Channel) bool { return false },
		}

		err := conn.Connect()

		assert.NoError(t, err)
		assert.True(t, conn.Connected)
		assert.NotNil(t, conn.Connection)
		assert.NotNil(t, conn.Channel)
		assert.Equal(t, 1, dialerCalls)
	})

	t.Run("does not hold lock while running health check", func(t *testing.T) {
		healthStarted := make(chan struct{})
		continueHealth := make(chan struct{})
		dialerCalls := int32(0)

		var once sync.Once
		healthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			once.Do(func() { close(healthStarted) })

			<-continueHealth

			w.WriteHeader(http.StatusOK)
			_, err := w.Write([]byte(`{"status":"ok"}`))
			assert.NoError(t, err)
		}))
		defer healthServer.Close()

		conn := &RabbitMQConnection{
			ConnectionStringSource: "amqp://guest:guest@localhost:5672",
			HealthCheckURL:         healthServer.URL,
			Logger:                 &log.NopLogger{},
			dialer: func(string) (*amqp.Connection, error) {
				atomic.AddInt32(&dialerCalls, 1)

				return &amqp.Connection{}, nil
			},
			connectionCloser: func(*amqp.Connection) error {
				return nil
			},
			channelFactory: func(*amqp.Connection) (*amqp.Channel, error) {
				return &amqp.Channel{}, nil
			},
			connectionClosedFn: func(*amqp.Connection) bool { return false },
			channelClosedFn:    func(*amqp.Channel) bool { return false },
		}

		connectDone := make(chan error, 1)
		go func() {
			connectDone <- conn.Connect()
		}()

		select {
		case <-healthStarted:
		case err := <-connectDone:
			t.Fatalf("connect completed before health check request started: %v", err)
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for health check request to start")
		}

		ensureDone := make(chan error, 1)
		go func() {
			ensureDone <- conn.EnsureChannel()
		}()

		assert.Eventually(t, func() bool {
			return atomic.LoadInt32(&dialerCalls) >= 2
		}, 200*time.Millisecond, 10*time.Millisecond)

		close(continueHealth)

		select {
		case err := <-connectDone:
			assert.NoError(t, err)
		case <-time.After(time.Second):
			t.Fatal("connect did not complete")
		}

		select {
		case err := <-ensureDone:
			assert.NoError(t, err)
		case <-time.After(time.Second):
			t.Fatal("ensure channel did not complete")
		}
	})

	t.Run("nil logger is safe", func(t *testing.T) {
		t.Parallel()

		conn := &RabbitMQConnection{
			ConnectionStringSource: "amqp://guest:guest@localhost:5672",
			dialer: func(string) (*amqp.Connection, error) {
				return nil, errors.New("dial failed")
			},
		}

		assert.NotPanics(t, func() {
			_ = conn.Connect()
		})
	})
}

func TestRabbitMQConnection_ConnectContext_StrictTierBlocksPlaintextBeforeDial(t *testing.T) {
	t.Setenv("ENV_NAME", commons.Production.String())
	t.Setenv("ENV", "")
	t.Setenv("GO_ENV", "")
	t.Setenv(commons.EnvSecurityTier, "")
	t.Setenv(commons.EnvSecurityEnforcement, "true")
	unsetEnvVar(t, commons.EnvAllowInsecureTLS)

	var dialerCalls atomic.Int32
	conn := &RabbitMQConnection{
		ConnectionStringSource: "amqp://guest:guest@localhost:5672",
		Logger:                 log.NewNop(),
		dialer: func(string) (*amqp.Connection, error) {
			dialerCalls.Add(1)
			return nil, errors.New("dial should not be called")
		},
	}

	err := conn.ConnectContext(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, commons.ErrSecurityViolation)
	assert.EqualValues(t, 0, dialerCalls.Load())
}

func TestRabbitMQConnection_EnsureChannelContext_StrictTierBlocksPlaintextBeforeDial(t *testing.T) {
	t.Setenv("ENV_NAME", commons.Production.String())
	t.Setenv("ENV", "")
	t.Setenv("GO_ENV", "")
	t.Setenv(commons.EnvSecurityTier, "")
	t.Setenv(commons.EnvSecurityEnforcement, "true")
	unsetEnvVar(t, commons.EnvAllowInsecureTLS)

	var dialerCalls atomic.Int32
	conn := &RabbitMQConnection{
		ConnectionStringSource: "amqp://guest:guest@localhost:5672",
		Logger:                 log.NewNop(),
		dialerContext: func(context.Context, string) (*amqp.Connection, error) {
			dialerCalls.Add(1)
			return nil, errors.New("dial should not be called")
		},
	}

	err := conn.EnsureChannelContext(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, commons.ErrSecurityViolation)
	assert.EqualValues(t, 0, dialerCalls.Load())
}

func TestRabbitMQConnection_EnsureChannelContext_StrictTierOverrideAllowsDial(t *testing.T) {
	t.Setenv("ENV_NAME", commons.Production.String())
	t.Setenv("ENV", "")
	t.Setenv("GO_ENV", "")
	t.Setenv(commons.EnvSecurityTier, "")
	t.Setenv(commons.EnvSecurityEnforcement, "true")
	t.Setenv(commons.EnvAllowInsecureTLS, "edge proxy terminates TLS")

	var dialerCalls atomic.Int32
	conn := &RabbitMQConnection{
		ConnectionStringSource: "amqp://guest:guest@localhost:5672",
		Logger:                 log.NewNop(),
		dialerContext: func(context.Context, string) (*amqp.Connection, error) {
			dialerCalls.Add(1)
			return &amqp.Connection{}, nil
		},
		channelFactoryContext: func(context.Context, *amqp.Connection) (*amqp.Channel, error) {
			return &amqp.Channel{}, nil
		},
	}

	err := conn.EnsureChannelContext(context.Background())
	require.NoError(t, err)
	assert.EqualValues(t, 1, dialerCalls.Load())
}

func TestRabbitMQConnection_EnsureChannel(t *testing.T) {
	t.Parallel()

	t.Run("nil receiver", func(t *testing.T) {
		t.Parallel()

		var conn *RabbitMQConnection

		err := conn.EnsureChannelContext(context.Background())
		assert.ErrorIs(t, err, ErrNilConnection)
	})

	t.Run("creates connection and channel when missing", func(t *testing.T) {
		t.Parallel()

		dialerCalls := 0
		channelCalls := 0

		conn := &RabbitMQConnection{
			Logger: &log.NopLogger{},
			dialer: func(string) (*amqp.Connection, error) {
				dialerCalls++

				return &amqp.Connection{}, nil
			},
			channelFactory: func(*amqp.Connection) (*amqp.Channel, error) {
				channelCalls++

				return &amqp.Channel{}, nil
			},
			connectionClosedFn: func(connection *amqp.Connection) bool { return connection == nil },
			channelClosedFn:    func(ch *amqp.Channel) bool { return ch == nil },
		}

		err := conn.EnsureChannel()

		assert.NoError(t, err)
		assert.True(t, conn.Connected)
		assert.NotNil(t, conn.Connection)
		assert.NotNil(t, conn.Channel)
		assert.Equal(t, 1, dialerCalls)
		assert.Equal(t, 1, channelCalls)
	})

	t.Run("reuses open connection and channel", func(t *testing.T) {
		t.Parallel()

		dialerCalls := 0
		channelCalls := 0

		conn := &RabbitMQConnection{
			Connection: &amqp.Connection{},
			Channel:    &amqp.Channel{},
			Connected:  true,
			Logger:     &log.NopLogger{},
			dialer: func(string) (*amqp.Connection, error) {
				dialerCalls++

				return nil, errors.New("should not be called")
			},
			channelFactory: func(*amqp.Connection) (*amqp.Channel, error) {
				channelCalls++

				return &amqp.Channel{}, nil
			},
			connectionClosedFn: func(*amqp.Connection) bool { return false },
			channelClosedFn:    func(*amqp.Channel) bool { return false },
		}

		err := conn.EnsureChannel()

		assert.NoError(t, err)
		assert.True(t, conn.Connected)
		assert.Equal(t, 0, dialerCalls)
		assert.Equal(t, 0, channelCalls)
	})

	t.Run("reopens channel when closed", func(t *testing.T) {
		t.Parallel()

		channelCalls := 0

		conn := &RabbitMQConnection{
			Connection: &amqp.Connection{},
			Channel:    &amqp.Channel{},
			Logger:     &log.NopLogger{},
			dialer: func(string) (*amqp.Connection, error) {
				return nil, nil
			},
			channelFactory: func(*amqp.Connection) (*amqp.Channel, error) {
				channelCalls++

				return &amqp.Channel{}, nil
			},
			connectionClosedFn: func(*amqp.Connection) bool { return false },
			channelClosedFn:    func(ch *amqp.Channel) bool { return ch != nil },
		}

		err := conn.EnsureChannel()

		assert.NoError(t, err)
		assert.True(t, conn.Connected)
		assert.Equal(t, 1, channelCalls)
		assert.NotNil(t, conn.Channel)
	})

	t.Run("context canceled before ensure channel", func(t *testing.T) {
		t.Parallel()

		dialerCalls := 0

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		conn := &RabbitMQConnection{
			Logger: &log.NopLogger{},
			dialer: func(string) (*amqp.Connection, error) {
				dialerCalls++

				return &amqp.Connection{}, nil
			},
		}

		err := conn.EnsureChannelContext(ctx)

		assert.ErrorIs(t, err, context.Canceled)
		assert.Equal(t, 0, dialerCalls)
	})

	t.Run("nil context defaults to background", func(t *testing.T) {
		t.Parallel()

		conn := &RabbitMQConnection{
			Connection:         &amqp.Connection{},
			Channel:            &amqp.Channel{},
			Connected:          true,
			Logger:             &log.NopLogger{},
			connectionClosedFn: func(*amqp.Connection) bool { return false },
			channelClosedFn:    func(*amqp.Channel) bool { return false },
		}

		assert.NotPanics(t, func() {
			//nolint:staticcheck // intentionally passing nil context
			err := conn.EnsureChannelContext(nil)
			assert.NoError(t, err)
		})
	})

	t.Run("resets stale connection on channel failure", func(t *testing.T) {
		t.Parallel()

		dialerCalls := 0
		closeCalls := 0

		connection := &amqp.Connection{}
		conn := &RabbitMQConnection{
			Logger: &log.NopLogger{},
			dialer: func(string) (*amqp.Connection, error) {
				dialerCalls++

				return connection, nil
			},
			channelFactory: func(*amqp.Connection) (*amqp.Channel, error) {
				return nil, errors.New("failed to open")
			},
			connectionCloser: func(*amqp.Connection) error {
				closeCalls++

				return nil
			},
			connectionClosedFn: func(*amqp.Connection) bool { return true },
			channelClosedFn:    func(*amqp.Channel) bool { return true },
		}

		err := conn.EnsureChannel()

		assert.Error(t, err)
		assert.False(t, conn.Connected)
		assert.Nil(t, conn.Connection)
		assert.Nil(t, conn.Channel)
		assert.Equal(t, 1, dialerCalls)
		assert.Equal(t, 1, closeCalls)
	})
}

func TestRabbitMQConnection_GetNewConnect(t *testing.T) {
	t.Parallel()

	t.Run("nil receiver", func(t *testing.T) {
		t.Parallel()

		var conn *RabbitMQConnection

		ch, err := conn.GetNewConnectContext(context.Background())
		assert.ErrorIs(t, err, ErrNilConnection)
		assert.Nil(t, ch)
	})

	t.Run("context canceled before connect", func(t *testing.T) {
		t.Parallel()

		conn := &RabbitMQConnection{}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		got, err := conn.GetNewConnectContext(ctx)

		assert.ErrorIs(t, err, context.Canceled)
		assert.Nil(t, got)
	})

	t.Run("creates channel when not connected", func(t *testing.T) {
		t.Parallel()

		dialerCalls := int32(0)

		conn := &RabbitMQConnection{
			Logger: &log.NopLogger{},
			dialer: func(string) (*amqp.Connection, error) {
				atomic.AddInt32(&dialerCalls, 1)

				return &amqp.Connection{}, nil
			},
			channelFactory: func(*amqp.Connection) (*amqp.Channel, error) {
				return &amqp.Channel{}, nil
			},
			connectionClosedFn: func(connection *amqp.Connection) bool { return connection == nil },
			channelClosedFn:    func(ch *amqp.Channel) bool { return ch == nil },
		}

		channel, err := conn.GetNewConnect()

		assert.NoError(t, err)
		assert.NotNil(t, channel)
		assert.Equal(t, int32(1), atomic.LoadInt32(&dialerCalls))
	})

	t.Run("reuses existing connected channel", func(t *testing.T) {
		t.Parallel()

		dialerCalls := 0
		channelCalls := 0

		existing := &amqp.Channel{}
		conn := &RabbitMQConnection{
			Connection: &amqp.Connection{},
			Channel:    existing,
			Connected:  true,
			Logger:     &log.NopLogger{},
			dialer: func(string) (*amqp.Connection, error) {
				dialerCalls++

				return nil, errors.New("should not be called")
			},
			channelFactory: func(*amqp.Connection) (*amqp.Channel, error) {
				channelCalls++

				return &amqp.Channel{}, nil
			},
			connectionClosedFn: func(*amqp.Connection) bool { return false },
			channelClosedFn:    func(*amqp.Channel) bool { return false },
		}

		got, err := conn.GetNewConnect()

		assert.NoError(t, err)
		assert.Same(t, existing, got)
		assert.Equal(t, 0, dialerCalls)
		assert.Equal(t, 0, channelCalls)
	})

	t.Run("stale channel state returns error", func(t *testing.T) {
		t.Parallel()

		connection := &amqp.Connection{}
		closeCalls := 0
		conn := &RabbitMQConnection{
			Connection: connection,
			Channel:    nil,
			Connected:  true,
			Logger:     &log.NopLogger{},
			dialer: func(string) (*amqp.Connection, error) {
				return connection, nil
			},
			channelFactory: func(*amqp.Connection) (*amqp.Channel, error) {
				return nil, nil
			},
			connectionClosedFn: func(*amqp.Connection) bool { return true },
			channelClosedFn:    func(*amqp.Channel) bool { return true },
			connectionCloser: func(*amqp.Connection) error {
				closeCalls++

				return nil
			},
		}

		got, err := conn.GetNewConnect()

		assert.Error(t, err)
		assert.Nil(t, got)
		assert.False(t, conn.Connected)
		assert.Nil(t, conn.Connection)
		assert.Nil(t, conn.Channel)
		assert.Equal(t, 1, closeCalls)
	})

	t.Run("concurrent callers all succeed", func(t *testing.T) {
		dialerCalls := int32(0)

		conn := &RabbitMQConnection{
			Logger: &log.NopLogger{},
			dialer: func(string) (*amqp.Connection, error) {
				atomic.AddInt32(&dialerCalls, 1)

				return &amqp.Connection{}, nil
			},
			channelFactory: func(*amqp.Connection) (*amqp.Channel, error) {
				return &amqp.Channel{}, nil
			},
			connectionClosedFn: func(connection *amqp.Connection) bool { return connection == nil },
			channelClosedFn:    func(ch *amqp.Channel) bool { return ch == nil },
			// Stub closer: real amqp.Connection{} zero-value cannot be closed.
			connectionCloser: func(_ *amqp.Connection) error { return nil },
		}

		const total = 10
		results := make(chan error, total)

		var wg sync.WaitGroup
		wg.Add(total)
		for i := 0; i < total; i++ {
			go func() {
				defer wg.Done()

				_, err := conn.GetNewConnect()
				results <- err
			}()
		}

		wg.Wait()
		close(results)

		for err := range results {
			assert.NoError(t, err)
		}

		// EnsureChannelContext releases the lock before dialing (to avoid holding it
		// during I/O). Under contention, a small number of goroutines may race to dial
		// before the first one finishes and updates the shared connection state. This is
		// the expected trade-off — rare duplicate dials vs. convoy effect.
		dials := atomic.LoadInt32(&dialerCalls)
		assert.GreaterOrEqual(t, dials, int32(1))
		assert.LessOrEqual(t, dials, int32(total))
		assert.True(t, conn.Connected)
		assert.NotNil(t, conn.Channel)
	})

	t.Run("concurrent reconnect closes leaked connections", func(t *testing.T) {
		// Simulates a chaos scenario: connection is killed while under load.
		// Multiple goroutines detect the dead connection and race to reconnect.
		// Verifies that duplicate connections are properly closed (no leaks).

		var mu sync.Mutex
		created := make(map[*amqp.Connection]bool) // tracks all created connections
		closed := make(map[*amqp.Connection]bool)  // tracks all closed connections
		dialStarted := make(chan struct{}, 1)      // signals that dialing has begun (buffered so the first dialer never drops the signal)
		dialBarrier := make(chan struct{})         // holds all dialers until released
		var dialersReady atomic.Int32

		// The "dead" connection that was killed externally.
		deadConn := &amqp.Connection{}

		conn := &RabbitMQConnection{
			Logger:    &log.NopLogger{},
			Connected: true,
			// Simulate an existing dead connection (force-closed by RabbitMQ).
			Connection: deadConn,
			Channel:    &amqp.Channel{},
			connectionClosedFn: func(c *amqp.Connection) bool {
				// deadConn is always considered closed; new connections are alive.
				return c == deadConn
			},
			channelClosedFn: func(ch *amqp.Channel) bool {
				return ch == nil
			},
			dialerContext: func(_ context.Context, _ string) (*amqp.Connection, error) {
				// Signal that we started dialing, then wait for barrier.
				// This ensures all goroutines enter the dial phase concurrently.
				dialersReady.Add(1)
				select {
				case dialStarted <- struct{}{}:
				default:
				}
				<-dialBarrier

				newConn := &amqp.Connection{}
				mu.Lock()
				created[newConn] = true
				mu.Unlock()

				return newConn, nil
			},
			channelFactoryContext: func(_ context.Context, _ *amqp.Connection) (*amqp.Channel, error) {
				return &amqp.Channel{}, nil
			},
			connectionCloser: func(c *amqp.Connection) error {
				mu.Lock()
				closed[c] = true
				mu.Unlock()

				return nil
			},
		}

		const goroutines = 5
		var wg sync.WaitGroup
		errs := make(chan error, goroutines)
		wg.Add(goroutines)

		for i := 0; i < goroutines; i++ {
			go func() {
				defer wg.Done()
				errs <- conn.EnsureChannelContext(context.Background())
			}()
		}

		// Wait for at least one goroutine to enter the dial phase,
		// then wait for multiple dialers to maximize race coverage.
		select {
		case <-dialStarted:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for a dialer to enter the dial phase")
		}
		require.Eventually(t, func() bool {
			return dialersReady.Load() >= 2
		}, time.Second, 10*time.Millisecond, "test did not force concurrent reconnect attempts")
		close(dialBarrier)

		wg.Wait()
		close(errs)

		for err := range errs {
			assert.NoError(t, err)
		}

		mu.Lock()
		numCreated := len(created)
		mu.Unlock()

		// Multiple connections were created (proves the race path was exercised).
		assert.Greater(t, numCreated, 1, "test must create multiple concurrent reconnects to validate leak cleanup")
		// The final state has exactly 1 surviving connection.
		assert.True(t, conn.Connected, "should be connected after reconnect")
		assert.NotNil(t, conn.Connection, "should have a live connection")
		// Use pointer identity (not DeepEqual) since all amqp.Connection{} zero-values are equal.
		assert.False(t, conn.Connection == deadConn, "should not be the dead connection (pointer identity)")
		// All excess connections (created - 1 survivor) plus the dead connection are closed.
		// The dead connection may or may not be closed (it's already dead), so we check
		// that at minimum the duplicate new connections were closed.
		survivingNew := 0
		mu.Lock()
		for c := range created {
			if !closed[c] {
				survivingNew++
			}
		}
		mu.Unlock()

		assert.Equal(t, 1, survivingNew, "exactly 1 new connection should survive; all others must be closed to prevent connection leak")
	})
}

func TestRabbitMQConnection_HealthCheck(t *testing.T) {
	t.Parallel()

	t.Run("nil receiver", func(t *testing.T) {
		t.Parallel()

		var conn *RabbitMQConnection
		healthy, err := conn.HealthCheckContext(context.Background())
		assert.ErrorIs(t, err, ErrNilConnection)
		assert.False(t, healthy)
	})

	t.Run("healthy response", func(t *testing.T) {
		t.Parallel()

		healthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, err := w.Write([]byte(`{"status":"ok"}`))
			assert.NoError(t, err)
		}))
		defer healthServer.Close()

		conn := &RabbitMQConnection{
			HealthCheckURL: healthServer.URL,
			Logger:         &log.NopLogger{},
		}

		healthy, err := conn.HealthCheck()
		assert.NoError(t, err)
		assert.True(t, healthy)
	})

	t.Run("returns defaults validation error", func(t *testing.T) {
		t.Parallel()

		conn := &RabbitMQConnection{
			HealthCheckURL: "https://localhost:15672",
			Logger:         &log.NopLogger{},
			healthHTTPClient: &http.Client{
				Transport: &http.Transport{
					TLSClientConfig: &tls.Config{
						InsecureSkipVerify: true, //nolint:gosec // intentional for validation test
					},
				},
			},
		}

		healthy, err := conn.HealthCheckContext(context.Background())
		assert.ErrorIs(t, err, ErrInsecureTLS)
		assert.False(t, healthy)
	})

	t.Run("server returns error status", func(t *testing.T) {
		t.Parallel()

		healthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, err := w.Write([]byte("err"))
			assert.NoError(t, err)
		}))
		defer healthServer.Close()

		conn := &RabbitMQConnection{HealthCheckURL: healthServer.URL, Logger: &log.NopLogger{}}

		healthy, err := conn.HealthCheck()
		assert.Error(t, err)
		assert.False(t, healthy)
	})

	t.Run("unhealthy response body", func(t *testing.T) {
		t.Parallel()

		healthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, err := w.Write([]byte(`{"status":"error"}`))
			assert.NoError(t, err)
		}))
		defer healthServer.Close()

		conn := &RabbitMQConnection{HealthCheckURL: healthServer.URL, Logger: &log.NopLogger{}}

		healthy, err := conn.HealthCheck()
		assert.Error(t, err)
		assert.False(t, healthy)
	})

	t.Run("malformed response", func(t *testing.T) {
		t.Parallel()

		healthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, err := w.Write([]byte(`{"status":`))
			assert.NoError(t, err)
		}))
		defer healthServer.Close()

		conn := &RabbitMQConnection{HealthCheckURL: healthServer.URL, Logger: &log.NopLogger{}}

		healthy, err := conn.HealthCheck()
		assert.Error(t, err)
		assert.False(t, healthy)
	})

	t.Run("null response", func(t *testing.T) {
		t.Parallel()

		healthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, err := w.Write([]byte("null"))
			assert.NoError(t, err)
		}))
		defer healthServer.Close()

		conn := &RabbitMQConnection{HealthCheckURL: healthServer.URL, Logger: &log.NopLogger{}}

		healthy, err := conn.HealthCheck()
		assert.Error(t, err)
		assert.False(t, healthy)
	})

	t.Run("invalid URL returns false", func(t *testing.T) {
		t.Parallel()

		conn := &RabbitMQConnection{HealthCheckURL: "http://[::1", Logger: &log.NopLogger{}}

		healthy, err := conn.HealthCheck()
		assert.Error(t, err)
		assert.False(t, healthy)
	})

	t.Run("strict allowlist mode requires configured hosts", func(t *testing.T) {
		t.Parallel()

		conn := &RabbitMQConnection{
			HealthCheckURL:                 "http://localhost:15672",
			Logger:                         &log.NopLogger{},
			RequireHealthCheckAllowedHosts: true,
		}

		healthy, err := conn.HealthCheck()
		assert.ErrorIs(t, err, ErrHealthCheckAllowedHostsRequired)
		assert.False(t, healthy)
	})

	t.Run("invalid URL scheme is rejected", func(t *testing.T) {
		t.Parallel()

		conn := &RabbitMQConnection{HealthCheckURL: "ftp://localhost:15672", Logger: &log.NopLogger{}}

		healthy, err := conn.HealthCheck()
		assert.Error(t, err)
		assert.False(t, healthy)
	})

	t.Run("context canceled before health check request", func(t *testing.T) {
		t.Parallel()

		healthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, err := w.Write([]byte(`{"status":"ok"}`))
			assert.NoError(t, err)
		}))
		defer healthServer.Close()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		conn := &RabbitMQConnection{
			HealthCheckURL: healthServer.URL,
			Logger:         &log.NopLogger{},
		}

		healthy, err := conn.HealthCheckContext(ctx)
		assert.Error(t, err)
		assert.False(t, healthy)
	})

	t.Run("authentication", func(t *testing.T) {
		t.Parallel()

		healthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			username, password, ok := r.BasicAuth()
			if !ok || username != "correct" || password != "correct" {
				w.WriteHeader(http.StatusUnauthorized)

				return
			}

			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`{"status":"ok"}`))
			assert.NoError(t, err)
		}))
		defer healthServer.Close()

		badAuth := &RabbitMQConnection{
			HealthCheckURL:           healthServer.URL,
			User:                     "wrong",
			Pass:                     "wrong",
			Logger:                   &log.NopLogger{},
			AllowInsecureHealthCheck: true,
		}

		goodAuth := &RabbitMQConnection{
			HealthCheckURL:           healthServer.URL,
			User:                     "correct",
			Pass:                     "correct",
			Logger:                   &log.NopLogger{},
			AllowInsecureHealthCheck: true,
		}

		badHealthy, badErr := badAuth.HealthCheck()
		assert.Error(t, badErr)
		assert.False(t, badHealthy)

		goodHealthy, goodErr := goodAuth.HealthCheck()
		assert.NoError(t, goodErr)
		assert.True(t, goodHealthy)
	})

	t.Run("https basic auth without explicit allowlist derives host from connection string", func(t *testing.T) {
		t.Parallel()

		healthServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			username, password, ok := r.BasicAuth()
			if !ok || username != "correct" || password != "correct" {
				w.WriteHeader(http.StatusUnauthorized)

				return
			}

			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`{"status":"ok"}`))
			assert.NoError(t, err)
		}))
		defer healthServer.Close()

		parsedURL, err := url.Parse(healthServer.URL)
		assert.NoError(t, err)

		conn := &RabbitMQConnection{
			ConnectionStringSource: "amqp://guest:guest@" + parsedURL.Host,
			HealthCheckURL:         healthServer.URL,
			User:                   "correct",
			Pass:                   "correct",
			Logger:                 &log.NopLogger{},
			healthHTTPClient:       healthServer.Client(),
			AllowInsecureTLS:       true,
		}

		healthy, healthErr := conn.HealthCheck()
		assert.NoError(t, healthErr)
		assert.True(t, healthy)
	})

	t.Run("healthCheck uses provided policy snapshot", func(t *testing.T) {
		t.Parallel()

		healthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, err := w.Write([]byte(`{"status":"ok"}`))
			assert.NoError(t, err)
		}))
		defer healthServer.Close()

		parsed, err := url.Parse(healthServer.URL)
		assert.NoError(t, err)

		conn := &RabbitMQConnection{
			AllowInsecureHealthCheck: false,
			HealthCheckAllowedHosts:  []string{"blocked.example:15672"},
			Logger:                   &log.NopLogger{},
		}

		err = conn.healthCheck(
			context.Background(),
			healthServer.URL,
			"user",
			"pass",
			healthServer.Client(),
			healthCheckURLConfig{
				allowInsecure: true,
				hasBasicAuth:  true,
				allowedHosts:  []string{parsed.Host},
			},
			&log.NopLogger{},
		)

		assert.NoError(t, err)
	})
}

func TestApplyDefaults_InsecureTLS(t *testing.T) {
	t.Parallel()

	t.Run("returns error when injected client disables TLS verification", func(t *testing.T) {
		t.Parallel()

		conn := &RabbitMQConnection{
			Logger: &log.NopLogger{},
			healthHTTPClient: &http.Client{
				Transport: &http.Transport{
					TLSClientConfig: &tls.Config{
						InsecureSkipVerify: true, //nolint:gosec // intentional for test
					},
				},
			},
		}

		conn.mu.Lock()
		err := conn.applyDefaults()
		conn.mu.Unlock()

		assert.ErrorIs(t, err, ErrInsecureTLS)
	})

	t.Run("AllowInsecureTLS bypasses the check", func(t *testing.T) {
		t.Parallel()

		conn := &RabbitMQConnection{
			Logger: &log.NopLogger{},
			healthHTTPClient: &http.Client{
				Transport: &http.Transport{
					TLSClientConfig: &tls.Config{
						InsecureSkipVerify: true, //nolint:gosec // intentional for test
					},
				},
			},
			AllowInsecureTLS: true,
		}

		conn.mu.Lock()
		err := conn.applyDefaults()
		conn.mu.Unlock()

		assert.NoError(t, err)
	})

	t.Run("no error for default client", func(t *testing.T) {
		t.Parallel()

		conn := &RabbitMQConnection{
			Logger: &log.NopLogger{},
		}

		conn.mu.Lock()
		err := conn.applyDefaults()
		conn.mu.Unlock()

		assert.NoError(t, err)
	})

	t.Run("no error for secure custom client", func(t *testing.T) {
		t.Parallel()

		conn := &RabbitMQConnection{
			Logger: &log.NopLogger{},
			healthHTTPClient: &http.Client{
				Transport: &http.Transport{
					TLSClientConfig: &tls.Config{
						MinVersion: tls.VersionTLS12,
					},
				},
			},
		}

		conn.mu.Lock()
		err := conn.applyDefaults()
		conn.mu.Unlock()

		assert.NoError(t, err)
	})
}

func TestValidateHealthCheckURL(t *testing.T) {
	t.Parallel()

	t.Run("trims spaces and appends health path", func(t *testing.T) {
		t.Parallel()

		conn := &RabbitMQConnection{
			HealthCheckURL: "  http://localhost:15672  ",
			Logger:         &log.NopLogger{},
		}

		normalized, err := validateHealthCheckURLWithConfig(conn.HealthCheckURL, healthCheckURLConfig{})

		assert.NoError(t, err)
		assert.Equal(t, "http://localhost:15672/api/health/checks/alarms", normalized)
	})

	t.Run("preserves nested path and appends health endpoint", func(t *testing.T) {
		t.Parallel()

		normalized, err := validateHealthCheckURLWithConfig("http://localhost:15672/custom/alerts", healthCheckURLConfig{})

		assert.NoError(t, err)
		assert.Equal(t, "http://localhost:15672/custom/alerts/api/health/checks/alarms", normalized)
	})

	t.Run("normalizes path with trailing slash", func(t *testing.T) {
		t.Parallel()

		normalized, err := validateHealthCheckURLWithConfig("http://localhost:15672/custom/alerts/", healthCheckURLConfig{})

		assert.NoError(t, err)
		assert.Equal(t, "http://localhost:15672/custom/alerts/api/health/checks/alarms", normalized)
	})

	t.Run("requires host", func(t *testing.T) {
		t.Parallel()

		normalized, err := validateHealthCheckURLWithConfig("http:///api/health", healthCheckURLConfig{})

		assert.Error(t, err)
		assert.Empty(t, normalized)
	})

	t.Run("rejects unsupported scheme", func(t *testing.T) {
		t.Parallel()

		normalized, err := validateHealthCheckURLWithConfig("ftp://localhost:15672", healthCheckURLConfig{})

		assert.Error(t, err)
		assert.Empty(t, normalized)
	})

	t.Run("rejects user credentials", func(t *testing.T) {
		t.Parallel()

		normalized, err := validateHealthCheckURLWithConfig("http://user:pass@localhost:15672", healthCheckURLConfig{})

		assert.Error(t, err)
		assert.Empty(t, normalized)
	})

	t.Run("rejects http with basic auth", func(t *testing.T) {
		t.Parallel()

		_, err := validateHealthCheckURLWithConfig("http://localhost:15672", healthCheckURLConfig{
			hasBasicAuth: true,
		})
		assert.ErrorIs(t, err, ErrInsecureHealthCheck)
	})

	t.Run("allows http with basic auth when opted in", func(t *testing.T) {
		t.Parallel()

		normalized, err := validateHealthCheckURLWithConfig("http://localhost:15672", healthCheckURLConfig{
			hasBasicAuth:  true,
			allowInsecure: true,
		})
		assert.NoError(t, err)
		assert.Contains(t, normalized, "/api/health/checks/alarms")
	})

	t.Run("requires allowlist for https basic auth", func(t *testing.T) {
		t.Parallel()

		_, err := validateHealthCheckURLWithConfig("https://rabbitmq:15671", healthCheckURLConfig{
			hasBasicAuth: true,
		})
		assert.ErrorIs(t, err, ErrHealthCheckAllowedHostsRequired)
	})

	t.Run("allows https basic auth when host is derived from AMQP connection host", func(t *testing.T) {
		t.Parallel()

		normalized, err := validateHealthCheckURLWithConfig("https://rabbitmq:15671", healthCheckURLConfig{
			hasBasicAuth: true,
			allowedHosts: deriveAllowedHostsFromConnectionString("amqp://guest:guest@rabbitmq:5672"),
		})
		assert.NoError(t, err)
		assert.Contains(t, normalized, "/api/health/checks/alarms")
	})

	t.Run("strict allowlist mode still requires explicit configured list", func(t *testing.T) {
		t.Parallel()

		_, err := validateHealthCheckURLWithConfig("https://rabbitmq:15671", healthCheckURLConfig{
			hasBasicAuth:        true,
			derivedAllowedHosts: deriveAllowedHostsFromConnectionString("amqp://guest:guest@rabbitmq:5672"),
			requireAllowedHosts: true,
		})
		assert.ErrorIs(t, err, ErrHealthCheckAllowedHostsRequired)
	})

	t.Run("does not enforce derived hosts when basic auth is not used", func(t *testing.T) {
		t.Parallel()

		normalized, err := validateHealthCheckURLWithConfig("https://management.rabbitmq:15671", healthCheckURLConfig{
			derivedAllowedHosts: deriveAllowedHostsFromConnectionString("amqp://guest:guest@rabbitmq:5672"),
		})
		assert.NoError(t, err)
		assert.Contains(t, normalized, "/api/health/checks/alarms")
	})

	t.Run("allows https basic auth without allowlist when explicitly insecure", func(t *testing.T) {
		t.Parallel()

		normalized, err := validateHealthCheckURLWithConfig("https://rabbitmq:15671", healthCheckURLConfig{
			hasBasicAuth:  true,
			allowInsecure: true,
		})
		assert.NoError(t, err)
		assert.Contains(t, normalized, "/api/health/checks/alarms")
	})

	t.Run("rejects host not in allowlist", func(t *testing.T) {
		t.Parallel()

		_, err := validateHealthCheckURLWithConfig("http://evil.example.com:15672", healthCheckURLConfig{
			allowedHosts: []string{"localhost:15672", "rabbitmq:15672"},
		})
		assert.ErrorIs(t, err, ErrHealthCheckHostNotAllowed)
	})

	t.Run("requires allowlist when strict mode enabled", func(t *testing.T) {
		t.Parallel()

		_, err := validateHealthCheckURLWithConfig("http://localhost:15672", healthCheckURLConfig{
			requireAllowedHosts: true,
		})
		assert.ErrorIs(t, err, ErrHealthCheckAllowedHostsRequired)
	})

	t.Run("allows host in allowlist", func(t *testing.T) {
		t.Parallel()

		normalized, err := validateHealthCheckURLWithConfig("http://rabbitmq:15672", healthCheckURLConfig{
			allowedHosts: []string{"localhost:15672", "rabbitmq:15672"},
		})
		assert.NoError(t, err)
		assert.Contains(t, normalized, "/api/health/checks/alarms")
	})

	t.Run("allows host-only allowlist entries", func(t *testing.T) {
		t.Parallel()

		normalized, err := validateHealthCheckURLWithConfig("http://rabbitmq:15672", healthCheckURLConfig{
			allowedHosts: []string{"rabbitmq"},
		})
		assert.NoError(t, err)
		assert.Contains(t, normalized, "/api/health/checks/alarms")
	})

	t.Run("enforces port when allowlist entry includes port", func(t *testing.T) {
		t.Parallel()

		_, err := validateHealthCheckURLWithConfig("http://rabbitmq:5672", healthCheckURLConfig{
			allowedHosts: []string{"rabbitmq:15672"},
		})
		assert.ErrorIs(t, err, ErrHealthCheckHostNotAllowed)
	})
}

func TestRabbitMQConnection_HealthCheck_UsesConfiguredPath(t *testing.T) {
	t.Parallel()

	gotPath := make(chan string, 1)

	healthServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath <- r.URL.Path

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, err := w.Write([]byte(`{"status":"ok"}`))
		assert.NoError(t, err)
	}))
	defer healthServer.Close()

	conn := &RabbitMQConnection{
		HealthCheckURL: healthServer.URL + "/custom/alerts",
		Logger:         &log.NopLogger{},
	}

	healthy, err := conn.HealthCheck()
	assert.NoError(t, err)
	assert.True(t, healthy)

	select {
	case p := <-gotPath:
		assert.Equal(t, "/custom/alerts/api/health/checks/alarms", p)
	case <-time.After(1 * time.Second):
		t.Fatal("health check did not reach test server")
	}
}

func TestBuildRabbitMQConnectionString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		protocol string
		user     string
		pass     string
		host     string
		port     string
		vhost    string
		expected string
	}{
		{
			name:     "empty vhost",
			protocol: "amqp",
			user:     "guest",
			pass:     "guest",
			host:     "localhost",
			port:     "5672",
			expected: "amqp://guest:guest@localhost:5672",
		},
		{
			name:     "custom vhost",
			protocol: "amqp",
			user:     "admin",
			pass:     "secret",
			host:     "rabbitmq.example.com",
			port:     "5672",
			vhost:    "production",
			expected: "amqp://admin:secret@rabbitmq.example.com:5672/production",
		},
		{
			name:     "root vhost",
			protocol: "amqp",
			user:     "guest",
			pass:     "guest",
			host:     "localhost",
			port:     "5672",
			vhost:    "/",
			expected: "amqp://guest:guest@localhost:5672/%2F",
		},
		{
			name:     "vhost with spaces",
			protocol: "amqp",
			user:     "guest",
			pass:     "guest",
			host:     "localhost",
			port:     "5672",
			vhost:    "my vhost",
			expected: "amqp://guest:guest@localhost:5672/my%20vhost",
		},
		{
			name:     "vhost with slash",
			protocol: "amqp",
			user:     "guest",
			pass:     "guest",
			host:     "localhost",
			port:     "5672",
			vhost:    "env/prod/region1",
			expected: "amqp://guest:guest@localhost:5672/env%2Fprod%2Fregion1",
		},
		{
			name:     "vhost with hash and ampersand",
			protocol: "amqp",
			user:     "guest",
			pass:     "guest",
			host:     "localhost",
			port:     "5672",
			vhost:    "test#1&2",
			expected: "amqp://guest:guest@localhost:5672/test%231%262",
		},
		{
			name:     "password with special chars",
			protocol: "amqp",
			user:     "admin",
			pass:     "p@ss:word/123",
			host:     "localhost",
			port:     "5672",
			vhost:    "production",
			expected: "amqp://admin:p%40ss%3Aword%2F123@localhost:5672/production",
		},
		{
			name:     "username with special chars",
			protocol: "amqp",
			user:     "admin@domain:user",
			pass:     "secret",
			host:     "localhost",
			port:     "5672",
			vhost:    "production",
			expected: "amqp://admin%40domain%3Auser:secret@localhost:5672/production",
		},
		{
			name:     "ipv6 with port",
			protocol: "amqp",
			user:     "guest",
			pass:     "guest",
			host:     "::1",
			port:     "5672",
			expected: "amqp://guest:guest@[::1]:5672",
		},
		{
			name:     "ipv6 without port",
			protocol: "amqp",
			user:     "guest",
			pass:     "guest",
			host:     "::1",
			expected: "amqp://guest:guest@[::1]",
		},
		{
			name:     "hostname without port",
			protocol: "amqp",
			user:     "guest",
			pass:     "guest",
			host:     "rabbitmq.local",
			expected: "amqp://guest:guest@rabbitmq.local",
		},
		{
			name:     "empty credentials",
			protocol: "amqp",
			host:     "localhost",
			port:     "5672",
			expected: "amqp://localhost:5672",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := BuildRabbitMQConnectionString(tt.protocol, tt.user, tt.pass, tt.host, tt.port, tt.vhost)

			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestRabbitMQConnection_ChannelSnapshot(t *testing.T) {
	t.Parallel()

	t.Run("nil receiver returns nil", func(t *testing.T) {
		t.Parallel()

		var conn *RabbitMQConnection

		assert.Nil(t, conn.ChannelSnapshot())
	})

	t.Run("nil channel returns nil", func(t *testing.T) {
		t.Parallel()

		conn := &RabbitMQConnection{}

		assert.Nil(t, conn.ChannelSnapshot())
	})

	t.Run("returns current channel", func(t *testing.T) {
		t.Parallel()

		expected := &amqp.Channel{}
		conn := &RabbitMQConnection{Channel: expected}

		assert.Same(t, expected, conn.ChannelSnapshot())
	})

	t.Run("snapshot read is mutex protected", func(t *testing.T) {
		t.Parallel()

		conn := &RabbitMQConnection{Channel: &amqp.Channel{}}
		conn.mu.Lock()

		started := make(chan struct{}, 1)
		readDone := make(chan struct{}, 1)

		go func() {
			started <- struct{}{}
			_ = conn.ChannelSnapshot()
			readDone <- struct{}{}
		}()

		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("ChannelSnapshot goroutine did not start")
		}

		select {
		case <-readDone:
			t.Fatal("ChannelSnapshot should block while the connection lock is held")
		case <-time.After(250 * time.Millisecond):
		}

		conn.mu.Unlock()

		select {
		case <-readDone:
		case <-time.After(time.Second):
			t.Fatal("ChannelSnapshot did not resume after lock release")
		}
	})
}

func TestIsHostAllowed(t *testing.T) {
	t.Parallel()

	t.Run("allows CIDR ranges", func(t *testing.T) {
		t.Parallel()

		assert.True(t, isHostAllowed("10.10.1.7:15672", []string{"10.10.0.0/16"}))
		assert.False(t, isHostAllowed("10.11.1.7:15672", []string{"10.10.0.0/16"}))
	})

	t.Run("normalizes ipv4 mapped ipv6", func(t *testing.T) {
		t.Parallel()

		assert.True(t, isHostAllowed("127.0.0.1:15672", []string{"::ffff:127.0.0.1"}))
	})
}

func TestDeriveAllowedHostsFromConnectionString(t *testing.T) {
	t.Parallel()

	t.Run("derives host and host:port", func(t *testing.T) {
		t.Parallel()

		hosts := deriveAllowedHostsFromConnectionString("amqp://guest:guest@rabbitmq.internal:5672")
		assert.Contains(t, hosts, "rabbitmq.internal:5672")
		assert.Contains(t, hosts, "rabbitmq.internal")
	})

	t.Run("invalid connection string returns no hosts", func(t *testing.T) {
		t.Parallel()

		hosts := deriveAllowedHostsFromConnectionString("not-a-url")
		assert.Empty(t, hosts)
	})
}

func TestRedactURLCredentials(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name            string
		message         string
		expected        string
		expectedContain []string
		notContain      []string
	}{
		{
			name:            "amqps scheme is redacted",
			message:         "dial amqps://admin:s3cret@broker:5671/vhost failed",
			expectedContain: []string{"amqps://admin:****@broker:5671/vhost"},
			notContain:      []string{"s3cret"},
		},
		{
			name:     "user-only URL remains unchanged",
			message:  "dial amqp://guest@localhost:5672 failed",
			expected: "dial amqp://guest@localhost:5672 failed",
		},
		{
			name:            "url-encoded password is redacted",
			message:         "dial amqp://admin:p%40ss%3Aword%2F123@broker:5672 failed",
			expectedContain: []string{"amqp://admin:****@broker:5672"},
			notContain:      []string{"p%40ss%3Aword%2F123"},
		},
		{
			name:            "password with slash is redacted",
			message:         "dial amqp://admin:pa/ss@broker:5672 failed",
			expectedContain: []string{"amqp://admin:****@broker:5672"},
			notContain:      []string{"pa/ss"},
		},
		{
			name:            "password with literal at is redacted",
			message:         "dial amqp://admin:p@ss@broker:5672 failed",
			expectedContain: []string{"amqp://admin:****@broker:5672"},
			notContain:      []string{"p@ss"},
		},
		{
			name:            "multiple URLs are redacted",
			message:         "upstream amqp://u1:p1@host1:5672 then amqps://u2:p2@host2:5671",
			expectedContain: []string{"amqp://u1:****@host1:5672", "amqps://u2:****@host2:5671"},
			notContain:      []string{"u1:p1", "u2:p2"},
		},
		{
			name:            "ipv6 host is redacted",
			message:         "dial amqp://guest:guest@[::1]:5672 failed",
			expectedContain: []string{"amqp://guest:****@[::1]:5672"},
			notContain:      []string{"guest:guest@[::1]"},
		},
		{
			name:            "empty password is normalized to redacted placeholder",
			message:         "dial amqp://user:@localhost:5672 failed",
			expectedContain: []string{"amqp://user:****@localhost:5672"},
			notContain:      []string{"user:@localhost"},
		},
		{
			name:            "surrounding text and punctuation are preserved",
			message:         "error details (amqp://user:secret@localhost:5672), retry later",
			expectedContain: []string{"error details (amqp://user:****@localhost:5672), retry later"},
			notContain:      []string{"user:secret@"},
		},
		{
			name:            "multiple colons in userinfo are fully redacted",
			message:         "dial amqp://user:name:secret@localhost:5672 failed",
			expectedContain: []string{"amqp://user:****@localhost:5672"},
			notContain:      []string{"secret", "user:name:secret"},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			got := redactURLCredentials(testCase.message)

			if testCase.expected != "" {
				assert.Equal(t, testCase.expected, got)
			}

			for _, expected := range testCase.expectedContain {
				assert.Contains(t, got, expected)
			}

			for _, unwanted := range testCase.notContain {
				assert.NotContains(t, got, unwanted)
			}
		})
	}
}

func TestRedactURLCredentialsFallback(t *testing.T) {
	t.Parallel()

	t.Run("preserves at-sign in path while redacting userinfo", func(t *testing.T) {
		t.Parallel()

		token := "amqp://user:secret@host:5672/path@segment?key=value"

		got := redactURLCredentialsFallback(token)

		assert.Equal(t, "amqp://user:****@host:5672/path@segment?key=value", got)
	})

	t.Run("does not redact when at-sign appears only in path", func(t *testing.T) {
		t.Parallel()

		token := "amqp://host:5672/path@segment"

		got := redactURLCredentialsFallback(token)

		assert.Equal(t, token, got)
	})
}

func TestSanitizeAMQPErr(t *testing.T) {
	t.Parallel()

	t.Run("redacts credentials from connection string in error", func(t *testing.T) {
		t.Parallel()

		err := errors.New("dial tcp: lookup amqp://admin:s3cretP@ss@broker:5672")
		connectionString := "amqp://admin:s3cretP@ss@broker:5672"

		got := sanitizeAMQPErr(err, connectionString)

		assert.NotContains(t, got, "s3cretP@ss")
		assert.Contains(t, got, "****")
	})

	t.Run("nil error returns empty string", func(t *testing.T) {
		t.Parallel()

		got := sanitizeAMQPErr(nil, "amqp://guest:guest@localhost:5672")

		assert.Equal(t, "", got)
	})

	t.Run("unparseable connection string uses fallback redaction pass", func(t *testing.T) {
		t.Parallel()

		err := errors.New("something went wrong")

		got := sanitizeAMQPErr(err, "://not-a-url")

		assert.Equal(t, "something went wrong", got)
	})

	t.Run("error without connection string returns original message", func(t *testing.T) {
		t.Parallel()

		err := errors.New("timeout connecting to broker")

		got := sanitizeAMQPErr(err, "amqp://admin:secret@broker:5672")

		assert.Equal(t, "timeout connecting to broker", got)
		assert.NotContains(t, got, "secret")
	})

	t.Run("redacts decoded password when embedded standalone in error", func(t *testing.T) {
		t.Parallel()

		err := errors.New("authentication failed: password=s3cr3t")
		connectionString := "amqp://admin:s3cr3t@broker:5672"

		got := sanitizeAMQPErr(err, connectionString)

		assert.NotContains(t, got, "s3cr3t")
		assert.Contains(t, got, "****")
	})

	t.Run("redacts URL-encoded password in decoded form", func(t *testing.T) {
		t.Parallel()

		// Password with special chars: p@ss:word/123 → encoded as p%40ss%3Aword%2F123
		err := errors.New("auth error for p@ss:word/123")
		connectionString := "amqp://admin:p%40ss%3Aword%2F123@broker:5672"

		got := sanitizeAMQPErr(err, connectionString)

		assert.NotContains(t, got, "p@ss:word/123")
		assert.Contains(t, got, "****")
	})

	t.Run("empty connection string without URL credentials returns unmodified error", func(t *testing.T) {
		t.Parallel()

		err := errors.New("something failed")

		got := sanitizeAMQPErr(err, "")

		assert.Equal(t, "something failed", got)
	})

	t.Run("empty connection string still redacts URL credentials from error", func(t *testing.T) {
		t.Parallel()

		err := errors.New("dial failed for amqp://guest:guest@localhost:5672")

		got := sanitizeAMQPErr(err, "")

		assert.NotContains(t, got, "guest:guest")
		assert.Contains(t, got, "****")
	})

	t.Run("fallback redaction fully redacts multi-colon userinfo passwords", func(t *testing.T) {
		t.Parallel()

		err := errors.New("dial failed for amqp://user:name:secret@localhost:5672")

		got := sanitizeAMQPErr(err, "")

		assert.NotContains(t, got, "secret")
		assert.Contains(t, got, "amqp://user:****@localhost:5672")
	})
}

func TestRabbitMQConnection_Close(t *testing.T) {
	t.Parallel()

	t.Run("close releases resources", func(t *testing.T) {
		t.Parallel()

		channelCloseCalls := int32(0)
		connectionCloseCalls := int32(0)

		conn := &RabbitMQConnection{
			Connection: &amqp.Connection{},
			Channel:    &amqp.Channel{},
			Connected:  true,
			channelCloser: func(*amqp.Channel) error {
				atomic.AddInt32(&channelCloseCalls, 1)

				return nil
			},
			connectionCloser: func(*amqp.Connection) error {
				atomic.AddInt32(&connectionCloseCalls, 1)

				return nil
			},
			Logger: &log.NopLogger{},
		}

		err := conn.Close()

		assert.NoError(t, err)
		assert.Equal(t, int32(1), atomic.LoadInt32(&channelCloseCalls))
		assert.Equal(t, int32(1), atomic.LoadInt32(&connectionCloseCalls))
		assert.False(t, conn.Connected)
		assert.Nil(t, conn.Channel)
		assert.Nil(t, conn.Connection)
	})

	t.Run("close aggregates channel and connection errors", func(t *testing.T) {
		t.Parallel()

		conn := &RabbitMQConnection{
			Connection: &amqp.Connection{},
			Channel:    &amqp.Channel{},
			Connected:  true,
			channelCloser: func(*amqp.Channel) error {
				return errors.New("channel close failed")
			},
			connectionCloser: func(*amqp.Connection) error {
				return errors.New("connection close failed")
			},
			Logger: &log.NopLogger{},
		}

		err := conn.Close()

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "channel close failed")
		assert.Contains(t, err.Error(), "connection close failed")
		assert.False(t, conn.Connected)
		assert.Nil(t, conn.Channel)
		assert.Nil(t, conn.Connection)
	})

	t.Run("close only connection error", func(t *testing.T) {
		t.Parallel()

		conn := &RabbitMQConnection{
			Connection: &amqp.Connection{},
			Channel:    &amqp.Channel{},
			Connected:  true,
			channelCloser: func(*amqp.Channel) error {
				return nil
			},
			connectionCloser: func(*amqp.Connection) error {
				return errors.New("connection close failed")
			},
			Logger: &log.NopLogger{},
		}

		err := conn.Close()

		assert.Error(t, err)
		assert.Contains(t, err.Error(), "connection close failed")
	})

	t.Run("close on nil receiver is safe", func(t *testing.T) {
		t.Parallel()

		var rc *RabbitMQConnection

		assert.NotPanics(t, func() {
			err := rc.CloseContext(context.Background())
			assert.ErrorIs(t, err, ErrNilConnection)
		})
	})

	t.Run("close context canceled", func(t *testing.T) {
		t.Parallel()

		conn := &RabbitMQConnection{}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := conn.CloseContext(ctx)

		assert.ErrorIs(t, err, context.Canceled)
	})
}
