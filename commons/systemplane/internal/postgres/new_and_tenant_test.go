//go:build unit

package postgres

import (
	"context"
	"database/sql"
	"testing"

	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/store"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// New — validation-only paths (no real DB required)
// ---------------------------------------------------------------------------

func TestNew_NilDB(t *testing.T) {
	t.Parallel()

	_, err := New(Config{DB: nil, ListenDSN: "dsn"})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrNilBackend)
}

func TestNew_EmptyListenDSN(t *testing.T) {
	t.Parallel()

	// Need a non-nil *sql.DB — use sql.Open with a noop driver.
	// We just need a non-nil handle; the schema call will fail later.
	db, err := sql.Open("postgres", "")
	if err != nil {
		// Skip if driver not registered; this is OK since we test the DSN guard.
		t.Skip("postgres driver not available")
	}
	t.Cleanup(func() { _ = db.Close() })

	_, err = New(Config{DB: db, ListenDSN: ""})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ListenDSN")
}

func TestNew_UnsafeChannelName(t *testing.T) {
	t.Parallel()

	db := &sql.DB{} // non-nil, no real connection needed — guard fires before schema
	_, err := New(Config{
		DB:        db,
		ListenDSN: "postgres://localhost/db",
		Channel:   "bad-channel-name!",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsafe channel name")
}

func TestNew_UnsafeTableName(t *testing.T) {
	t.Parallel()

	db := &sql.DB{}
	_, err := New(Config{
		DB:        db,
		ListenDSN: "postgres://localhost/db",
		Channel:   "valid_channel",
		Table:     "123-invalid-table",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsafe table name")
}

// ---------------------------------------------------------------------------
// startSpan — with nil Telemetry (fast path)
// ---------------------------------------------------------------------------

func TestStartSpan_NilTelemetry(t *testing.T) {
	t.Parallel()

	s := &Store{cfg: Config{Telemetry: nil}}
	ctx, span, finish := s.startSpan(context.Background(), "test.op")
	defer finish()

	assert.NotNil(t, ctx)
	assert.NotNil(t, span)
	// finish() must not panic
}

// ---------------------------------------------------------------------------
// ListTenantValues — nil and closed guards
// ---------------------------------------------------------------------------

func TestListTenantValues_NilReceiver(t *testing.T) {
	t.Parallel()

	var s *Store
	_, err := s.ListTenantValues(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

func TestListTenantValues_ClosedStore(t *testing.T) {
	t.Parallel()

	s := &Store{closed: true}
	_, err := s.ListTenantValues(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

// ---------------------------------------------------------------------------
// ListTenantOverrides — nil and closed guards
// ---------------------------------------------------------------------------

func TestListTenantOverrides_NilReceiver(t *testing.T) {
	t.Parallel()

	var s *Store
	_, err := s.ListTenantOverrides(context.Background(), "", "", "", 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

func TestListTenantOverrides_ClosedStore(t *testing.T) {
	t.Parallel()

	s := &Store{closed: true}
	_, err := s.ListTenantOverrides(context.Background(), "", "", "", 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

// ---------------------------------------------------------------------------
// ListTenantsForKey — nil and closed guards
// ---------------------------------------------------------------------------

func TestListTenantsForKey_NilReceiver(t *testing.T) {
	t.Parallel()

	var s *Store
	_, err := s.ListTenantsForKey(context.Background(), "global", "log.level")
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}

func TestListTenantsForKey_ClosedStore(t *testing.T) {
	t.Parallel()

	s := &Store{closed: true}
	_, err := s.ListTenantsForKey(context.Background(), "global", "log.level")
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrClosed)
}
