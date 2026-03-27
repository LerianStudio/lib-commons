// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package consumer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/logcompat"
	"github.com/LerianStudio/lib-commons/v4/commons/tenant-manager/internal/testutil"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeAcknowledger struct {
	ackCalls  int
	nackCalls int
	requeue   bool
}

func (f *fakeAcknowledger) Ack(uint64, bool) error {
	f.ackCalls++
	return nil
}

func (f *fakeAcknowledger) Nack(uint64, bool, bool) error {
	f.nackCalls++
	f.requeue = true
	return nil
}

func (f *fakeAcknowledger) Reject(uint64, bool) error { return nil }

func TestMultiTenantConsumer_HandleMessage_AcksSuccessfulMessages(t *testing.T) {
	t.Parallel()

	consumer := &MultiTenantConsumer{}
	ack := &fakeAcknowledger{}
	logger := logcompat.New(testutil.NewMockLogger())

	var seenTenantID string
	msg := amqp.Delivery{Acknowledger: ack, DeliveryTag: 1, Headers: amqp.Table{}}

	consumer.handleMessage(context.Background(), "tenant-ack", "queue-a", func(ctx context.Context, delivery amqp.Delivery) error {
		seenTenantID = core.GetTenantIDContext(ctx)
		return nil
	}, msg, logger)

	assert.Equal(t, "tenant-ack", seenTenantID)
	assert.Equal(t, 1, ack.ackCalls)
	assert.Equal(t, 0, ack.nackCalls)
}

func TestMultiTenantConsumer_HandleMessage_NacksFailedMessages(t *testing.T) {
	t.Parallel()

	consumer := &MultiTenantConsumer{}
	ack := &fakeAcknowledger{}
	logger := logcompat.New(testutil.NewMockLogger())

	msg := amqp.Delivery{Acknowledger: ack, DeliveryTag: 2, Headers: amqp.Table{}}

	consumer.handleMessage(context.Background(), "tenant-nack", "queue-b", func(context.Context, amqp.Delivery) error {
		return errors.New("boom")
	}, msg, logger)

	assert.Equal(t, 0, ack.ackCalls)
	assert.Equal(t, 1, ack.nackCalls)
	assert.True(t, ack.requeue)
}

func TestMultiTenantConsumer_ProcessMessages_ReturnsOnChannelClose(t *testing.T) {
	t.Parallel()

	consumer := &MultiTenantConsumer{}
	logger := logcompat.New(testutil.NewMockLogger())
	msgs := make(chan amqp.Delivery)
	notifyClose := make(chan *amqp.Error, 1)
	done := make(chan struct{})

	go func() {
		consumer.processMessages(context.Background(), "tenant-close", "queue-c", func(context.Context, amqp.Delivery) error {
			return nil
		}, msgs, notifyClose, logger)
		close(done)
	}()

	notifyClose <- &amqp.Error{Reason: "channel closed"}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("processMessages did not return after channel close notification")
	}
}

func TestMultiTenantConsumer_StopConsumer(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		setupTenantID   string // tenant to set up in maps (empty = no setup)
		stopTenantID    string // tenant to stop
		expectCancelled bool   // whether the context should be cancelled
	}{
		{
			name:            "stops_running_consumer_and_cleans_up_all_state",
			setupTenantID:   "tenant-stop-1",
			stopTenantID:    "tenant-stop-1",
			expectCancelled: true,
		},
		{
			name:            "noop_for_unknown_tenant",
			setupTenantID:   "",
			stopTenantID:    "tenant-unknown",
			expectCancelled: false,
		},
		{
			name:            "removes_known_tenant_without_active_consumer",
			setupTenantID:   "tenant-known-only",
			stopTenantID:    "tenant-known-only",
			expectCancelled: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			server := setupTenantManagerAPIServer(t, nil)
			consumer := mustNewConsumer(t, nil, newTestConfig(server.URL), testutil.NewMockLogger())

			var tenantCtx context.Context

			// Set up tenant state based on test case
			if tt.setupTenantID != "" {
				if tt.name == "removes_known_tenant_without_active_consumer" {
					// Only add to knownTenants, no cancel function
					consumer.mu.Lock()
					consumer.knownTenants[tt.setupTenantID] = true
					consumer.mu.Unlock()
				} else {
					// Add both cancel function and known tenant flag
					var cancel context.CancelFunc
					tenantCtx, cancel = context.WithCancel(context.Background())

					consumer.mu.Lock()
					consumer.tenants[tt.setupTenantID] = cancel
					consumer.knownTenants[tt.setupTenantID] = true
					consumer.mu.Unlock()
				}

				// Add retry state and consumer lock
				consumer.retryState.Store(tt.setupTenantID, &retryStateEntry{retryCount: 3, degraded: true})
				consumer.consumerLocks.Store(tt.setupTenantID, &sync.Mutex{})
			}

			// Act
			consumer.StopConsumer(tt.stopTenantID)

			// Assert: tenant removed from tenants map
			consumer.mu.RLock()
			_, hasCancel := consumer.tenants[tt.stopTenantID]
			_, isKnown := consumer.knownTenants[tt.stopTenantID]
			consumer.mu.RUnlock()

			assert.False(t, hasCancel, "tenant should be removed from tenants map")
			assert.False(t, isKnown, "tenant should be removed from knownTenants map")

			// Assert: retry state cleaned up
			_, hasRetryState := consumer.retryState.Load(tt.stopTenantID)
			assert.False(t, hasRetryState, "retry state should be cleaned up")

			// Assert: consumer lock cleaned up
			_, hasLock := consumer.consumerLocks.Load(tt.stopTenantID)
			assert.False(t, hasLock, "consumer lock should be cleaned up")

			// Assert: context was cancelled if there was a running consumer
			if tt.expectCancelled {
				require.NotNil(t, tenantCtx, "tenantCtx should have been set up")
				select {
				case <-tenantCtx.Done():
					// expected
				default:
					t.Fatal("tenant context should be cancelled after StopConsumer")
				}
			}
		})
	}
}

func TestMultiTenantConsumer_StopConsumer_ConcurrentSafety(t *testing.T) {
	t.Parallel()

	server := setupTenantManagerAPIServer(t, nil)
	consumer := mustNewConsumer(t, nil, newTestConfig(server.URL), testutil.NewMockLogger())

	const tenantID = "tenant-concurrent"

	_, cancel := context.WithCancel(context.Background())

	consumer.mu.Lock()
	consumer.tenants[tenantID] = cancel
	consumer.knownTenants[tenantID] = true
	consumer.mu.Unlock()

	consumer.retryState.Store(tenantID, &retryStateEntry{retryCount: 1})
	consumer.consumerLocks.Store(tenantID, &sync.Mutex{})

	// Call StopConsumer concurrently to verify no race conditions
	var wg sync.WaitGroup

	const goroutines = 10

	wg.Add(goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()
			consumer.StopConsumer(tenantID)
		}()
	}

	wg.Wait()

	// Verify tenant is fully cleaned up
	consumer.mu.RLock()
	_, hasCancel := consumer.tenants[tenantID]
	_, isKnown := consumer.knownTenants[tenantID]
	consumer.mu.RUnlock()

	assert.False(t, hasCancel, "tenant should not be in tenants map after concurrent stops")
	assert.False(t, isKnown, "tenant should not be in knownTenants map after concurrent stops")

	_, hasRetryState := consumer.retryState.Load(tenantID)
	assert.False(t, hasRetryState, "retry state should be cleaned up after concurrent stops")

	_, hasLock := consumer.consumerLocks.Load(tenantID)
	assert.False(t, hasLock, "consumer lock should be cleaned up after concurrent stops")
}
