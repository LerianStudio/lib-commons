//go:build unit

// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package mongo

import (
	"context"
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/core"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	tenantDashless = "11111111111111111111111111111111"
	tenantDashed   = "11111111-1111-1111-1111-111111111111"
)

// TestManager_OneTenantTwoSpellingsShareOneClient covers a client opened for
// the Tenant Manager's dashless ID and asked for under the dashed UUID: the
// same client and database, and closing under either spelling closes it.
func TestManager_OneTenantTwoSpellingsShareOneClient(t *testing.T) {
	for _, requested := range []string{tenantDashed, tenantDashless} {
		t.Run(requested, func(t *testing.T) {
			fakeDB, cleanup := startFakeMongoServer(t)
			defer cleanup()

			manager := NewManager(mustNewTestClient(t, "http://localhost:8080"), "ledger",
				WithLogger(testutil.NewMockLogger()))

			manager.connections[tenantDashless] = &MongoConnection{DB: fakeDB}
			manager.databaseNames[tenantDashless] = "tenant_db"
			manager.lastAccessed[tenantDashless] = time.Now()
			manager.lastConnectionsCheck[tenantDashless] = time.Now().Add(time.Hour)

			conn, err := manager.GetConnection(context.Background(), requested)
			require.NoError(t, err)
			assert.Same(t, fakeDB, conn, "one tenant, one client")

			db, err := manager.GetDatabaseForTenant(context.Background(), requested)
			require.NoError(t, err)
			assert.Equal(t, "tenant_db", db.Name())
			assert.Len(t, manager.connections, 1)

			// Leave the fake server's client to its cleanup; closing a nil-DB
			// entry is a no-op.
			manager.connections[tenantDashless] = &MongoConnection{}

			require.NoError(t, manager.CloseConnection(context.Background(), requested))
			assert.Empty(t, manager.connections, "closing under either spelling closes the client")
			assert.Empty(t, manager.databaseNames)
		})
	}
}

// TestManager_RejectsAnInvalidTenantID covers an ID with no canonical form:
// refused before any Tenant Manager call.
func TestManager_RejectsAnInvalidTenantID(t *testing.T) {
	manager := NewManager(mustNewTestClient(t, "http://localhost:8080"), "ledger")

	for _, id := range []string{"bad id", "../etc"} {
		conn, err := manager.GetConnection(context.Background(), id)
		require.ErrorIs(t, err, core.ErrInvalidTenantIDFormat, "tenant %q", id)
		assert.Nil(t, conn)

		db, err := manager.GetDatabaseForTenant(context.Background(), id)
		require.ErrorIs(t, err, core.ErrInvalidTenantIDFormat, "tenant %q", id)
		assert.Nil(t, db)

		require.ErrorIs(t, manager.CloseConnection(context.Background(), id), core.ErrInvalidTenantIDFormat)
	}
}
