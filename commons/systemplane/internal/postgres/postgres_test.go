//go:build unit

package postgres

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/store"
)

// TestParseNotifyPayload_WithTenantID asserts that a post-Task-3 payload
// (one carrying the tenant_id field) round-trips through the parser and
// surfaces TenantID on the resulting store.Event. This is the core
// contract the Client's changefeed router depends on to distinguish
// global vs tenant-scoped writes.
func TestParseNotifyPayload_WithTenantID(t *testing.T) {
	t.Parallel()

	payload := `{"namespace":"global","key":"fees.fail_closed_default","tenant_id":"tenant-A"}`

	evt, err := parseNotifyPayload(payload)
	require.NoError(t, err)

	assert.Equal(t, store.Event{
		Namespace: "global",
		Key:       "fees.fail_closed_default",
		TenantID:  "tenant-A",
	}, evt)
}

// TestParseNotifyPayload_GlobalSentinel asserts the same roundtrip works
// when the tenant_id carries the '_global' sentinel — the shape the
// trigger function emits for legacy Set() writes after the schema
// migration.
func TestParseNotifyPayload_GlobalSentinel(t *testing.T) {
	t.Parallel()

	payload := `{"namespace":"global","key":"log.level","tenant_id":"_global"}`

	evt, err := parseNotifyPayload(payload)
	require.NoError(t, err)

	assert.Equal(t, "_global", evt.TenantID)
	assert.Equal(t, store.SentinelGlobal, evt.TenantID, "parser should surface the sentinel unchanged")
}

// TestParseNotifyPayload_BackwardCompatNoTenantID guards against a
// rollout race: during the window when the tenant column has been added
// but the trigger has not yet been recreated with the new payload body,
// the Postgres server may still emit the pre-Task-3 shape
// ({"namespace":"...", "key":"..."} with no tenant_id field). The parser
// must decode such payloads without error and coerce the missing tenant_id
// to the '_global' sentinel so the Client's refresh router takes the
// legacy global path. Leaving TenantID empty would cause the router to
// treat the event as a tenant event and emit a spurious warn for every
// legacy payload during a rolling upgrade.
func TestParseNotifyPayload_BackwardCompatNoTenantID(t *testing.T) {
	t.Parallel()

	payload := `{"namespace":"global","key":"log.level"}`

	evt, err := parseNotifyPayload(payload)
	require.NoError(t, err)

	assert.Equal(t, "global", evt.Namespace)
	assert.Equal(t, "log.level", evt.Key)
	assert.Equal(t, store.SentinelGlobal, evt.TenantID, "missing tenant_id must coerce to the _global sentinel for router safety")
}

// TestParseNotifyPayload_InvalidJSONReturnsError is a regression pin so
// future changes to the notifyPayload shape do not accidentally swallow
// a malformed payload as a valid zero-value event.
func TestParseNotifyPayload_InvalidJSONReturnsError(t *testing.T) {
	t.Parallel()

	_, err := parseNotifyPayload(`{not-json`)
	require.Error(t, err)

	assert.Contains(t, err.Error(), "unmarshal")
}

// TestSetTenantValue_Phase1ReturnsErrTenantSchemaNotEnabled pins the H8
// rolling-deploy guard: when the Store is running in phase-1 compat mode
// (the default, TenantSchemaEnabled=false), SetTenantValue MUST return
// store.ErrTenantSchemaNotEnabled BEFORE issuing any I/O. The check runs
// at the very top of the method, so a nil *sql.DB on Config is sufficient
// — the guard short-circuits the DB call entirely.
func TestSetTenantValue_Phase1ReturnsErrTenantSchemaNotEnabled(t *testing.T) {
	t.Parallel()

	// Construct a Store manually (bypassing New, which would require a real
	// DB handle for ensureSchema). The phase guard runs before any DB access,
	// so the nil DB is never dereferenced.
	s := &Store{cfg: Config{TenantSchemaEnabled: false, Table: "test"}}

	err := s.SetTenantValue(context.Background(), "tenant-A", store.Entry{
		Namespace: "global", Key: "fee.rate", Value: []byte(`0.01`),
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrTenantSchemaNotEnabled,
		"phase-1 SetTenantValue must reject with ErrTenantSchemaNotEnabled")
}

// TestDeleteTenantValue_Phase1ReturnsErrTenantSchemaNotEnabled mirrors the
// SetTenantValue guard for the delete path.
func TestDeleteTenantValue_Phase1ReturnsErrTenantSchemaNotEnabled(t *testing.T) {
	t.Parallel()

	s := &Store{cfg: Config{TenantSchemaEnabled: false, Table: "test"}}

	err := s.DeleteTenantValue(context.Background(), "tenant-A", "global", "fee.rate", "admin")
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrTenantSchemaNotEnabled,
		"phase-1 DeleteTenantValue must reject with ErrTenantSchemaNotEnabled")
}

// TestSetTenantValue_RejectsGlobalSentinel pins M1: a caller that attempts
// to pass "_global" as the tenantID in phase 2 gets rejected explicitly,
// mirroring the MongoDB backend's long-standing behavior. The phase-2
// prerequisite keeps this check on the write-time fast path.
func TestSetTenantValue_RejectsGlobalSentinel(t *testing.T) {
	t.Parallel()

	s := &Store{cfg: Config{TenantSchemaEnabled: true, Table: "test"}}

	err := s.SetTenantValue(context.Background(), store.SentinelGlobal, store.Entry{
		Namespace: "global", Key: "fee.rate", Value: []byte(`0.01`),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "_global", "error must name the forbidden sentinel")
}

// TestDeleteTenantValue_RejectsGlobalSentinel mirrors the Set guard for the
// delete path under phase 2.
func TestDeleteTenantValue_RejectsGlobalSentinel(t *testing.T) {
	t.Parallel()

	s := &Store{cfg: Config{TenantSchemaEnabled: true, Table: "test"}}

	err := s.DeleteTenantValue(context.Background(), store.SentinelGlobal, "global", "fee.rate", "admin")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "_global", "error must name the forbidden sentinel")
}

// TestGetTenantValue_RejectsGlobalSentinel pins M-S3-7: the read path must
// refuse the '_global' sentinel as a tenant ID. Reading via the
// tenant-scoped path with the sentinel would silently return the shared
// global row, defeating the row-separation invariant (TRD §3 / PRD AC13).
// The Postgres backend has no phase-1/phase-2 gate on Get, so the check
// runs regardless of TenantSchemaEnabled.
func TestGetTenantValue_RejectsGlobalSentinel(t *testing.T) {
	t.Parallel()

	s := &Store{cfg: Config{TenantSchemaEnabled: true, Table: "test"}}

	_, _, err := s.GetTenantValue(context.Background(), store.SentinelGlobal, "global", "fee.rate")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "_global", "error must name the forbidden sentinel")
}

// TestGetTenantValue_RejectsEmptyTenantID pins the empty-tenant guard on
// the read path — without it, a caller passing "" would return whichever
// row happens to have an empty tenant_id (the pre-migration legacy shape).
func TestGetTenantValue_RejectsEmptyTenantID(t *testing.T) {
	t.Parallel()

	s := &Store{cfg: Config{TenantSchemaEnabled: true, Table: "test"}}

	_, _, err := s.GetTenantValue(context.Background(), "", "global", "fee.rate")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tenantID must not be empty")
}

// TestSet_RejectsEmptyNamespace pins the empty-namespace guard on the
// global Set path. Empty namespaces would collide with the table's
// DEFAULT handling and could create a row unreachable by namespace-scoped
// Get. The method short-circuits before any DB access (M-S2-7).
func TestSet_RejectsEmptyNamespace(t *testing.T) {
	t.Parallel()

	s := &Store{cfg: Config{TenantSchemaEnabled: true, Table: "test"}}

	err := s.Set(context.Background(), store.Entry{
		Namespace: "", Key: "fee.rate", Value: []byte(`0.01`),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "namespace must not be empty")
}

// TestSet_RejectsEmptyKey pins the empty-key guard on the global Set path.
func TestSet_RejectsEmptyKey(t *testing.T) {
	t.Parallel()

	s := &Store{cfg: Config{TenantSchemaEnabled: true, Table: "test"}}

	err := s.Set(context.Background(), store.Entry{
		Namespace: "global", Key: "", Value: []byte(`0.01`),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "key must not be empty")
}

// TestSetTenantValue_RejectsEmptyTenantID pins the empty-tenant guard on
// the tenant write path.
func TestSetTenantValue_RejectsEmptyTenantID(t *testing.T) {
	t.Parallel()

	s := &Store{cfg: Config{TenantSchemaEnabled: true, Table: "test"}}

	err := s.SetTenantValue(context.Background(), "", store.Entry{
		Namespace: "global", Key: "fee.rate", Value: []byte(`0.01`),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tenantID must not be empty")
}

// TestSetTenantValue_RejectsEmptyNamespace pins the empty-namespace guard
// on the tenant write path.
func TestSetTenantValue_RejectsEmptyNamespace(t *testing.T) {
	t.Parallel()

	s := &Store{cfg: Config{TenantSchemaEnabled: true, Table: "test"}}

	err := s.SetTenantValue(context.Background(), "tenant-A", store.Entry{
		Namespace: "", Key: "fee.rate", Value: []byte(`0.01`),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "namespace must not be empty")
}

// TestSetTenantValue_RejectsEmptyKey pins the empty-key guard on the
// tenant write path.
func TestSetTenantValue_RejectsEmptyKey(t *testing.T) {
	t.Parallel()

	s := &Store{cfg: Config{TenantSchemaEnabled: true, Table: "test"}}

	err := s.SetTenantValue(context.Background(), "tenant-A", store.Entry{
		Namespace: "global", Key: "", Value: []byte(`0.01`),
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "key must not be empty")
}

// TestDeleteTenantValue_RejectsEmptyTenantID pins the empty-tenant guard
// on the tenant delete path.
func TestDeleteTenantValue_RejectsEmptyTenantID(t *testing.T) {
	t.Parallel()

	s := &Store{cfg: Config{TenantSchemaEnabled: true, Table: "test"}}

	err := s.DeleteTenantValue(context.Background(), "", "global", "fee.rate", "admin")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tenantID must not be empty")
}

// TestDeleteTenantValue_RejectsEmptyNamespace pins the empty-namespace
// guard on the tenant delete path.
func TestDeleteTenantValue_RejectsEmptyNamespace(t *testing.T) {
	t.Parallel()

	s := &Store{cfg: Config{TenantSchemaEnabled: true, Table: "test"}}

	err := s.DeleteTenantValue(context.Background(), "tenant-A", "", "fee.rate", "admin")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "namespace must not be empty")
}

// TestDeleteTenantValue_RejectsEmptyKey pins the empty-key guard on the
// tenant delete path.
func TestDeleteTenantValue_RejectsEmptyKey(t *testing.T) {
	t.Parallel()

	s := &Store{cfg: Config{TenantSchemaEnabled: true, Table: "test"}}

	err := s.DeleteTenantValue(context.Background(), "tenant-A", "global", "", "admin")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "key must not be empty")
}

// TestTruncateString pins the helper used to cap untrusted NOTIFY payload
// lengths before logging. The three cases mirror boundary behavior: below
// the cap, exactly at the cap, and strictly above.
func TestTruncateString(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{
			name:   "shorter than max returns input unchanged",
			input:  "abc",
			maxLen: 10,
			want:   "abc",
		},
		{
			name:   "exactly max length returns input unchanged",
			input:  "abcdefghij",
			maxLen: 10,
			want:   "abcdefghij",
		},
		{
			name:   "longer than max truncates and appends ellipsis",
			input:  "abcdefghijXYZ",
			maxLen: 10,
			want:   "abcdefghij...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := truncateString(tt.input, tt.maxLen)
			assert.Equal(t, tt.want, got)
		})
	}
}
