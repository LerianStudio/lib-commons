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

	"github.com/LerianStudio/lib-commons/v4/commons/events"
	"github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/testutil"
)

// setEnv sets ENVIRONMENT_NAME for the duration of the test and explicitly
// clears ENV_NAME so the fallback path does not pollute the test. t.Setenv
// restores both at test end and prevents the test from running in parallel
// (env vars are process-global state).
func setEnv(t *testing.T, value string) {
	t.Helper()
	t.Setenv("ENVIRONMENT_NAME", value)
	t.Setenv("ENV_NAME", "")
}

// clearEnv removes both ENVIRONMENT_NAME and ENV_NAME for the duration of the
// test. Used to verify that Start() fails fast when no env is configured.
func clearEnv(t *testing.T) {
	t.Helper()
	t.Setenv("ENVIRONMENT_NAME", "")
	t.Setenv("ENV_NAME", "")
}

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

// TestTenantEventListener_Start_MissingEnvFailsFast verifies that Start()
// returns an error when neither ENVIRONMENT_NAME nor ENV_NAME is set.
// Multi-tenant consumers MUST configure one of the two on the pod.
func TestTenantEventListener_Start_MissingEnvFailsFast(t *testing.T) {
	clearEnv(t)

	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	t.Cleanup(func() { client.Close() })

	handler := func(_ context.Context, _ TenantLifecycleEvent) error { return nil }

	listener, err := NewTenantEventListener(client, handler)
	require.NoError(t, err)

	err = listener.Start(context.Background())
	require.Error(t, err)
	// The wrapped error from commons.CurrentEnv() must mention both env var
	// names so operators know either is accepted.
	assert.Contains(t, err.Error(), "ENVIRONMENT_NAME")
	assert.Contains(t, err.Error(), "ENV_NAME")
	assert.Contains(t, err.Error(), "tenant event listener cannot start")
}

func TestTenantEventListener_StartStop(t *testing.T) {
	setEnv(t, "staging")

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
	setEnv(t, "staging")

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
// Channel-scoping tests — listener must subscribe ONLY to its env channel.
// --------------------------------------------------------------------------

// TestTenantEventListener_SubscribesToStagingChannel verifies that when
// ENVIRONMENT_NAME=staging, Start() subscribes to tenant-events:staging:
// and the listener receives events published on that channel.
func TestTenantEventListener_SubscribesToStagingChannel(t *testing.T) {
	setEnv(t, "staging")

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	t.Cleanup(func() { rdb.Close() })

	var received atomic.Int32

	handler := func(_ context.Context, _ TenantLifecycleEvent) error {
		received.Add(1)
		return nil
	}

	listener, err := NewTenantEventListener(rdb, handler)
	require.NoError(t, err)

	err = listener.Start(context.Background())
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	channel := events.TenantEventsChannel("staging")
	assert.Equal(t, "tenant-events:staging:", channel)

	evt := TenantLifecycleEvent{
		EventID:     "evt-staging-001",
		EventType:   EventTenantCreated,
		Timestamp:   time.Now().UTC().Truncate(time.Second),
		TenantID:    "t-stg",
		TenantSlug:  "acme",
		Environment: "staging",
	}

	data, marshalErr := json.Marshal(evt)
	require.NoError(t, marshalErr)

	publishCount := mr.Publish(channel, string(data))
	require.Greater(t, publishCount, 0, "listener should be subscribed to tenant-events:staging:")

	assert.Eventually(t, func() bool {
		return received.Load() >= 1
	}, 2*time.Second, 20*time.Millisecond, "handler should receive events on staging channel")

	require.NoError(t, listener.Stop())
}

// TestTenantEventListener_SubscribesToProductionChannel mirrors the staging
// case for ENVIRONMENT_NAME=production.
func TestTenantEventListener_SubscribesToProductionChannel(t *testing.T) {
	setEnv(t, "production")

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	t.Cleanup(func() { rdb.Close() })

	var received atomic.Int32

	handler := func(_ context.Context, _ TenantLifecycleEvent) error {
		received.Add(1)
		return nil
	}

	listener, err := NewTenantEventListener(rdb, handler)
	require.NoError(t, err)

	err = listener.Start(context.Background())
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	channel := events.TenantEventsChannel("production")
	assert.Equal(t, "tenant-events:production:", channel)

	evt := TenantLifecycleEvent{
		EventID:     "evt-prod-001",
		EventType:   EventTenantActivated,
		Timestamp:   time.Now().UTC().Truncate(time.Second),
		TenantID:    "t-prod",
		TenantSlug:  "corp",
		Environment: "production",
	}

	data, marshalErr := json.Marshal(evt)
	require.NoError(t, marshalErr)

	publishCount := mr.Publish(channel, string(data))
	require.Greater(t, publishCount, 0, "listener should be subscribed to tenant-events:production:")

	assert.Eventually(t, func() bool {
		return received.Load() >= 1
	}, 2*time.Second, 20*time.Millisecond, "handler should receive events on production channel")

	require.NoError(t, listener.Stop())
}

// TestTenantEventListener_IgnoresLegacyPerTenantChannel verifies that the
// listener does NOT receive events published on the legacy
// tenant-events:{tenantID} channel — that channel format is still emitted by
// tenant-manager during the transition but multi-tenant consumers must only
// see events for their own environment.
func TestTenantEventListener_IgnoresLegacyPerTenantChannel(t *testing.T) {
	setEnv(t, "staging")

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	t.Cleanup(func() { rdb.Close() })

	var received atomic.Int32

	handler := func(_ context.Context, _ TenantLifecycleEvent) error {
		received.Add(1)
		return nil
	}

	listener, err := NewTenantEventListener(rdb, handler)
	require.NoError(t, err)

	err = listener.Start(context.Background())
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	// Publish to the LEGACY per-tenant channel "tenant-events:{tenantID}".
	legacyChannel := ChannelForTenant("t-legacy")

	evt := TenantLifecycleEvent{
		EventID:     "evt-legacy-001",
		EventType:   EventTenantCreated,
		Timestamp:   time.Now().UTC().Truncate(time.Second),
		TenantID:    "t-legacy",
		TenantSlug:  "legacy",
		Environment: "staging",
	}

	data, marshalErr := json.Marshal(evt)
	require.NoError(t, marshalErr)

	// miniredis.Publish returns subscriber count; with env-scoped subscribe
	// the count for the legacy channel MUST be 0.
	publishCount := mr.Publish(legacyChannel, string(data))
	assert.Equal(t, 0, publishCount,
		"listener must NOT be subscribed to legacy per-tenant channel %q", legacyChannel)

	// Wait long enough that a stray delivery would have happened.
	time.Sleep(200 * time.Millisecond)

	assert.Equal(t, int32(0), received.Load(),
		"handler must not be called for events on the legacy per-tenant channel")

	require.NoError(t, listener.Stop())
}

// TestTenantEventListener_IgnoresOtherEnvChannel verifies that a listener
// running with ENVIRONMENT_NAME=staging does not receive events published on
// tenant-events:production: — i.e., cross-environment isolation holds.
func TestTenantEventListener_IgnoresOtherEnvChannel(t *testing.T) {
	setEnv(t, "staging")

	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	t.Cleanup(func() { rdb.Close() })

	var received atomic.Int32

	handler := func(_ context.Context, _ TenantLifecycleEvent) error {
		received.Add(1)
		return nil
	}

	listener, err := NewTenantEventListener(rdb, handler)
	require.NoError(t, err)

	err = listener.Start(context.Background())
	require.NoError(t, err)

	time.Sleep(100 * time.Millisecond)

	otherChannel := events.TenantEventsChannel("production")

	evt := TenantLifecycleEvent{
		EventID:     "evt-other-001",
		EventType:   EventTenantCreated,
		Timestamp:   time.Now().UTC().Truncate(time.Second),
		TenantID:    "t-other",
		TenantSlug:  "other",
		Environment: "production",
	}

	data, marshalErr := json.Marshal(evt)
	require.NoError(t, marshalErr)

	publishCount := mr.Publish(otherChannel, string(data))
	assert.Equal(t, 0, publishCount,
		"staging listener must NOT be subscribed to %q", otherChannel)

	time.Sleep(200 * time.Millisecond)

	assert.Equal(t, int32(0), received.Load(),
		"staging listener must not receive production events")

	require.NoError(t, listener.Stop())
}

// --------------------------------------------------------------------------
// Message dispatch tests — publish on the env-scoped channel.
// --------------------------------------------------------------------------

func TestTenantEventListener_ReceivesEvents(t *testing.T) {
	setEnv(t, "staging")

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

	// Publish on the env-scoped channel that the listener actually subscribes to.
	channel := events.TenantEventsChannel("staging")

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
	setEnv(t, "staging")

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

	// Publish invalid JSON on the env-scoped channel — should not crash, should be skipped
	channel := events.TenantEventsChannel("staging")

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
	setEnv(t, "production")

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

	// Publish a valid event on the env-scoped channel
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

	channel := events.TenantEventsChannel("production")

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
