//go:build unit

// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package event

import (
	"context"
	"testing"

	"github.com/LerianStudio/lib-commons/v7/commons/obs"

	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/internal/testutil"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/tenantcache"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHandleEvent_CacheInvalidateServiceMismatchWarns pins the diagnostic for the
// production symptom: an operator publishes tenant.cache.invalidate, the service
// skips it because the payload's service_name does not match, and at debug level
// the operator sees nothing at all.
func TestHandleEvent_CacheInvalidateServiceMismatchWarns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		payload  string
		received string
	}{
		{
			name:     "mismatched service name",
			payload:  `{"service_name":"transaction"}`,
			received: "transaction",
		},
		{
			name:     "empty service name",
			payload:  `{"service_name":""}`,
			received: "",
		},
		{
			name:     "absent payload",
			payload:  "",
			received: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			logger := testutil.NewLevelCapturingLogger()
			dispatcher := NewEventDispatcher(tenantcache.NewTenantCache(), nil, "ledger",
				WithDispatcherLogger(logger))

			evt := TenantLifecycleEvent{
				EventID:   "evt-1",
				EventType: EventTenantCacheInvalidate,
				TenantID:  "tenant-warn",
			}

			if tt.payload != "" {
				evt.Payload = []byte(tt.payload)
			}

			require.NoError(t, dispatcher.HandleEvent(context.Background(), evt))

			assert.True(t,
				logger.ContainsAtLevel(obs.LevelWarn,
					EventTenantCacheInvalidate, "expected_service_name=ledger",
					"received_service_name="+tt.received),
				"skipped cache invalidation must warn with expected and received service names; got: %v",
				logger.Entries())
		})
	}
}

// TestHandleEvent_ServiceScopedMismatchStaysDebug keeps the routine fan-out
// quiet: every service receives every other service's lifecycle events, so those
// skips must not warn.
func TestHandleEvent_ServiceScopedMismatchStaysDebug(t *testing.T) {
	t.Parallel()

	logger := testutil.NewLevelCapturingLogger()
	dispatcher := NewEventDispatcher(tenantcache.NewTenantCache(), nil, "ledger",
		WithDispatcherLogger(logger))

	require.NoError(t, dispatcher.HandleEvent(context.Background(), TenantLifecycleEvent{
		EventID:   "evt-2",
		EventType: EventTenantServiceSuspended,
		TenantID:  "tenant-warn",
		Payload:   []byte(`{"service_name":"transaction"}`),
	}))

	assert.False(t, logger.ContainsAtLevel(obs.LevelWarn, "service mismatch"),
		"routine service-scoped fan-out must not warn; got: %v", logger.Entries())
	assert.True(t, logger.ContainsAtLevel(obs.LevelDebug, "service mismatch"),
		"the skip must still be traceable at debug level; got: %v", logger.Entries())
}

// TestHandleEvent_UnknownEventTypeWarns pins the unknown-type diagnostic at warn
// level with the offending type included.
func TestHandleEvent_UnknownEventTypeWarns(t *testing.T) {
	t.Parallel()

	logger := testutil.NewLevelCapturingLogger()
	dispatcher := NewEventDispatcher(tenantcache.NewTenantCache(), nil, "ledger",
		WithDispatcherLogger(logger))

	require.NoError(t, dispatcher.HandleEvent(context.Background(), TenantLifecycleEvent{
		EventID:   "evt-3",
		EventType: "tenant.invented.by.nobody",
		TenantID:  "tenant-warn",
	}))

	assert.True(t,
		logger.ContainsAtLevel(obs.LevelWarn, "unknown event type", "tenant.invented.by.nobody"),
		"unknown event types must warn with the type; got: %v", logger.Entries())
}
