//go:build unit

// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package event

import (
	"context"
	"testing"

	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/internal/testutil"
	tmmongo "github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/mongo"
	tmpostgres "github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/postgres"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/tenantcache"
	"github.com/stretchr/testify/require"
)

type recordingPostgresLifecycleManager struct {
	module           string
	closedTenantIDs  []string
	appliedTenantIDs []string
}

func (manager *recordingPostgresLifecycleManager) Module() string {
	return manager.module
}

func (manager *recordingPostgresLifecycleManager) CloseConnection(_ context.Context, tenantID string) error {
	manager.closedTenantIDs = append(manager.closedTenantIDs, tenantID)

	return nil
}

func (manager *recordingPostgresLifecycleManager) ApplyConnectionSettings(
	tenantID string,
	_ *core.TenantConfig,
) {
	manager.appliedTenantIDs = append(manager.appliedTenantIDs, tenantID)
}

type recordingMongoLifecycleManager struct {
	closedTenantIDs []string
}

func (manager *recordingMongoLifecycleManager) CloseConnection(_ context.Context, tenantID string) error {
	manager.closedTenantIDs = append(manager.closedTenantIDs, tenantID)

	return nil
}

func TestDispatcherOptions_WithModuleManagers_DeduplicatesRegistrations(t *testing.T) {
	t.Parallel()

	postgresA := tmpostgres.NewManager(nil, testServiceName)
	postgresB := tmpostgres.NewManager(nil, testServiceName)
	mongoA := tmmongo.NewManager(nil, testServiceName)
	mongoB := tmmongo.NewManager(nil, testServiceName)
	dispatcher := NewEventDispatcher(
		tenantcache.NewTenantCache(),
		nil,
		testServiceName,
		WithPostgresManagers(postgresA, postgresB, postgresA, nil),
		WithMongoManagers(mongoA, mongoB, mongoA, nil),
	)

	require.Len(t, dispatcher.postgresManagers, 2)
	require.Len(t, dispatcher.mongoManagers, 2)
}

func TestDispatcherOptions_WithSingularManager_PreservesLastOptionWins(t *testing.T) {
	t.Parallel()

	postgresA := tmpostgres.NewManager(nil, testServiceName)
	postgresB := tmpostgres.NewManager(nil, testServiceName)
	mongoA := tmmongo.NewManager(nil, testServiceName)
	mongoB := tmmongo.NewManager(nil, testServiceName)
	dispatcher := NewEventDispatcher(
		tenantcache.NewTenantCache(),
		nil,
		testServiceName,
		WithPostgresManagers(postgresA, postgresB),
		WithMongoManagers(mongoA, mongoB),
		WithPostgres(postgresB),
		WithMongo(mongoB),
	)

	require.Equal(t, []postgresLifecycleManager{postgresB}, dispatcher.postgresManagers)
	require.Equal(t, []tenantConnectionCloser{mongoB}, dispatcher.mongoManagers)
}

func TestEventDispatcher_RemoveTenant_FansOutAcrossModuleManagers(t *testing.T) {
	t.Parallel()

	postgresA := &recordingPostgresLifecycleManager{}
	postgresB := &recordingPostgresLifecycleManager{}
	mongoA := &recordingMongoLifecycleManager{}
	mongoB := &recordingMongoLifecycleManager{}
	dispatcher := NewEventDispatcher(
		tenantcache.NewTenantCache(),
		nil,
		testServiceName,
		WithDispatcherLogger(testutil.NewMockLogger()),
	)
	dispatcher.postgresManagers = []postgresLifecycleManager{postgresA, postgresB}
	dispatcher.mongoManagers = []tenantConnectionCloser{mongoA, mongoB}

	dispatcher.RemoveTenant(context.Background(), "tenant-a")

	require.Equal(t, []string{"tenant-a"}, postgresA.closedTenantIDs)
	require.Equal(t, []string{"tenant-a"}, postgresB.closedTenantIDs)
	require.Equal(t, []string{"tenant-a"}, mongoA.closedTenantIDs)
	require.Equal(t, []string{"tenant-a"}, mongoB.closedTenantIDs)
}

func TestEventDispatcher_ConnectionsUpdated_FansOutAcrossPostgresModuleManagers(t *testing.T) {
	t.Parallel()

	genericPostgres := &recordingPostgresLifecycleManager{}
	consignadoPostgres := &recordingPostgresLifecycleManager{module: "consignado"}
	credentialsPostgres := &recordingPostgresLifecycleManager{module: "credentials"}
	dispatcher := NewEventDispatcher(
		tenantcache.NewTenantCache(),
		nil,
		testServiceName,
		WithDispatcherLogger(testutil.NewMockLogger()),
	)
	dispatcher.postgresManagers = []postgresLifecycleManager{
		genericPostgres,
		consignadoPostgres,
		credentialsPostgres,
	}
	event := TenantLifecycleEvent{
		EventID:   "event-a",
		EventType: EventTenantConnectionsUpdated,
		TenantID:  "tenant-a",
		Payload: mustMarshalPayload(t, ConnectionsUpdatedPayload{
			ServiceName:  testServiceName,
			Module:       "consignado",
			MaxOpenConns: 12,
			MaxIdleConns: 6,
		}),
	}

	err := dispatcher.HandleEvent(context.Background(), event)

	require.NoError(t, err)
	require.Empty(t, genericPostgres.appliedTenantIDs)
	require.Equal(t, []string{"tenant-a"}, consignadoPostgres.appliedTenantIDs)
	require.Empty(t, credentialsPostgres.appliedTenantIDs)
}
