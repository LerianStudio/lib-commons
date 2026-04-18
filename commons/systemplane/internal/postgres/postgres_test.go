//go:build unit

package postgres

import (
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
	assert.Equal(t, sentinelGlobal, evt.TenantID, "parser should surface the sentinel unchanged")
}

// TestParseNotifyPayload_BackwardCompatNoTenantID guards against a
// rollout race: during the window when the tenant column has been added
// but the trigger has not yet been recreated with the new payload body,
// the Postgres server may still emit the pre-Task-3 shape
// ({"namespace":"...", "key":"..."} with no tenant_id field). The parser
// must decode such payloads without error and leave TenantID empty so
// the Client can gracefully route the event through the global path.
// Without this tolerance a single legacy notification would silently
// wedge the subscriber loop.
func TestParseNotifyPayload_BackwardCompatNoTenantID(t *testing.T) {
	t.Parallel()

	payload := `{"namespace":"global","key":"log.level"}`

	evt, err := parseNotifyPayload(payload)
	require.NoError(t, err)

	assert.Equal(t, "global", evt.Namespace)
	assert.Equal(t, "log.level", evt.Key)
	assert.Equal(t, "", evt.TenantID, "missing tenant_id must decode as empty, not error")
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
