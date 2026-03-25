// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

//go:build unit

package event

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/testutil"
)

// --------------------------------------------------------------------------
// Constructor validation tests
// --------------------------------------------------------------------------

func TestNewTenantEventListener_NilRedisClient(t *testing.T) {
	t.Parallel()

	handler := func(_ context.Context, _ TenantLifecycleEvent) error { return nil }

	listener, err := NewTenantEventListener(nil, handler)

	require.Error(t, err)
	assert.Nil(t, listener)
	assert.Contains(t, err.Error(), "redisClient must not be nil")
}

func TestNewTenantEventListener_NilHandler(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	t.Cleanup(func() { client.Close() })

	listener, err := NewTenantEventListener(client, nil)

	require.Error(t, err)
	assert.Nil(t, listener)
	assert.Contains(t, err.Error(), "handler must not be nil")
}

func TestNewTenantEventListener_Valid(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	t.Cleanup(func() { client.Close() })

	handler := func(_ context.Context, _ TenantLifecycleEvent) error { return nil }

	listener, err := NewTenantEventListener(client, handler)

	require.NoError(t, err)
	require.NotNil(t, listener)
}

func TestNewTenantEventListener_WithLogger(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	t.Cleanup(func() { client.Close() })

	handler := func(_ context.Context, _ TenantLifecycleEvent) error { return nil }
	logger := testutil.NewMockLogger()

	listener, err := NewTenantEventListener(client, handler, WithListenerLogger(logger))

	require.NoError(t, err)
	require.NotNil(t, listener)
}

// --------------------------------------------------------------------------
// Start / Stop lifecycle tests
// --------------------------------------------------------------------------

func TestTenantEventListener_StartStop(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	t.Cleanup(func() { client.Close() })

	handler := func(_ context.Context, _ TenantLifecycleEvent) error { return nil }

	listener, err := NewTenantEventListener(client, handler)
	require.NoError(t, err)

	ctx := context.Background()

	err = listener.Start(ctx)
	require.NoError(t, err)

	// Give the goroutine time to subscribe
	time.Sleep(50 * time.Millisecond)

	err = listener.Stop()
	require.NoError(t, err)
}

func TestTenantEventListener_ContextCancellation(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	t.Cleanup(func() { client.Close() })

	handler := func(_ context.Context, _ TenantLifecycleEvent) error { return nil }

	listener, err := NewTenantEventListener(client, handler)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())

	err = listener.Start(ctx)
	require.NoError(t, err)

	// Give the goroutine time to subscribe
	time.Sleep(50 * time.Millisecond)

	// Cancel the context — the goroutine should exit cleanly
	cancel()

	// Stop should still work cleanly after context cancellation
	err = listener.Stop()
	require.NoError(t, err)
}

// --------------------------------------------------------------------------
// Message dispatch tests
// --------------------------------------------------------------------------

func TestTenantEventListener_ReceivesEvents(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	t.Cleanup(func() { rdb.Close() })

	var received atomic.Int32

	var mu sync.Mutex

	var receivedEvent TenantLifecycleEvent

	handler := func(_ context.Context, evt TenantLifecycleEvent) error {
		received.Add(1)

		mu.Lock()
		receivedEvent = evt
		mu.Unlock()

		return nil
	}

	listener, err := NewTenantEventListener(rdb, handler)
	require.NoError(t, err)

	ctx := context.Background()

	err = listener.Start(ctx)
	require.NoError(t, err)

	// Give the goroutine time to subscribe
	time.Sleep(100 * time.Millisecond)

	// Build and publish a valid event
	evt := TenantLifecycleEvent{
		EventID:     "evt-001",
		EventType:   EventTenantCreated,
		Timestamp:   time.Now().UTC().Truncate(time.Second),
		TenantID:    "t-123",
		TenantSlug:  "acme",
		Environment: "staging",
	}

	data, marshalErr := json.Marshal(evt)
	require.NoError(t, marshalErr)

	// Publish to the tenant's channel
	channel := ChannelForTenant(evt.TenantID)

	publishCount := mr.Publish(channel, string(data))
	require.Greater(t, publishCount, 0, "expected at least one subscriber to receive the message")

	// Wait for handler to be called
	assert.Eventually(t, func() bool {
		return received.Load() >= 1
	}, 2*time.Second, 20*time.Millisecond, "handler should have been called")

	mu.Lock()
	assert.Equal(t, "evt-001", receivedEvent.EventID)
	assert.Equal(t, EventTenantCreated, receivedEvent.EventType)
	assert.Equal(t, "t-123", receivedEvent.TenantID)
	mu.Unlock()

	err = listener.Stop()
	require.NoError(t, err)
}

func TestTenantEventListener_ParseError(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	t.Cleanup(func() { rdb.Close() })

	var received atomic.Int32

	handler := func(_ context.Context, _ TenantLifecycleEvent) error {
		received.Add(1)
		return nil
	}

	capLogger := testutil.NewCapturingLogger()

	listener, err := NewTenantEventListener(rdb, handler, WithListenerLogger(capLogger))
	require.NoError(t, err)

	ctx := context.Background()

	err = listener.Start(ctx)
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	// Publish invalid JSON — should not crash, should be skipped
	channel := ChannelForTenant("t-bad")

	publishCount := mr.Publish(channel, "{invalid json")
	require.Greater(t, publishCount, 0, "expected at least one subscriber")

	// Give time for message processing
	time.Sleep(200 * time.Millisecond)

	// Handler should NOT have been called
	assert.Equal(t, int32(0), received.Load(), "handler should not be called for invalid JSON")

	// Logger should have captured the parse error
	assert.True(t, capLogger.ContainsSubstring("failed to parse"),
		"expected log message about parse failure, got: %v", capLogger.GetMessages())

	err = listener.Stop()
	require.NoError(t, err)
}

func TestTenantEventListener_HandlerError(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	t.Cleanup(func() { rdb.Close() })

	var received atomic.Int32

	errBoom := errors.New("handler exploded")

	handler := func(_ context.Context, _ TenantLifecycleEvent) error {
		received.Add(1)
		return errBoom
	}

	capLogger := testutil.NewCapturingLogger()

	listener, err := NewTenantEventListener(rdb, handler, WithListenerLogger(capLogger))
	require.NoError(t, err)

	ctx := context.Background()

	err = listener.Start(ctx)
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	// Publish a valid event
	evt := TenantLifecycleEvent{
		EventID:     "evt-002",
		EventType:   EventTenantActivated,
		Timestamp:   time.Now().UTC().Truncate(time.Second),
		TenantID:    "t-456",
		TenantSlug:  "corp",
		Environment: "production",
	}

	data, marshalErr := json.Marshal(evt)
	require.NoError(t, marshalErr)

	channel := ChannelForTenant(evt.TenantID)

	publishCount := mr.Publish(channel, string(data))
	require.Greater(t, publishCount, 0, "expected at least one subscriber")

	// Wait for handler to be called
	assert.Eventually(t, func() bool {
		return received.Load() >= 1
	}, 2*time.Second, 20*time.Millisecond, "handler should have been called even though it returns error")

	// Logger should have captured the handler error (non-fatal)
	assert.Eventually(t, func() bool {
		return capLogger.ContainsSubstring("handler exploded") || capLogger.ContainsSubstring("handler error")
	}, time.Second, 20*time.Millisecond,
		"expected log message about handler error, got: %v", capLogger.GetMessages())

	err = listener.Stop()
	require.NoError(t, err)
}

// --------------------------------------------------------------------------
// EventHandler type verification
// --------------------------------------------------------------------------

func TestEventHandler_TypeSatisfied(t *testing.T) {
	t.Parallel()

	// Verify EventHandler type signature matches the contract
	var h EventHandler = func(_ context.Context, _ TenantLifecycleEvent) error {
		return nil
	}

	assert.NotNil(t, h)
}

// --------------------------------------------------------------------------
// ListenerOption verification
// --------------------------------------------------------------------------

func TestWithListenerLogger_NilLogger(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	t.Cleanup(func() { client.Close() })

	handler := func(_ context.Context, _ TenantLifecycleEvent) error { return nil }

	// Nil logger should not panic — should fall back to nop
	listener, err := NewTenantEventListener(client, handler, WithListenerLogger(nil))

	require.NoError(t, err)
	require.NotNil(t, listener)
}

// --------------------------------------------------------------------------
// Ensure the listener ignores the correct log.Logger import
// --------------------------------------------------------------------------

func TestNewTenantEventListener_UsesLogInterface(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	t.Cleanup(func() { rdb.Close() })

	// Verify the listener works with log.NewNop()
	nop := log.NewNop()
	handler := func(_ context.Context, _ TenantLifecycleEvent) error { return nil }

	listener, err := NewTenantEventListener(rdb, handler, WithListenerLogger(nop))

	require.NoError(t, err)
	require.NotNil(t, listener)
}

// --------------------------------------------------------------------------
// WithService option
// --------------------------------------------------------------------------

func TestWithService_SetsServiceName(t *testing.T) {
	t.Parallel()

	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	t.Cleanup(func() { client.Close() })

	handler := func(_ context.Context, _ TenantLifecycleEvent) error { return nil }

	listener, err := NewTenantEventListener(client, handler, WithService("my-service"))

	require.NoError(t, err)
	require.NotNil(t, listener)
	assert.Equal(t, "my-service", listener.service, "service name should be set via WithService")
}
