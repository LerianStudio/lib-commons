//go:build unit

package idempotency

import (
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	chttp "github.com/LerianStudio/lib-commons/v6/commons/constants"
	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// countingApp routes POST /test through mw and counts how many times the
// protected handler actually runs.
//
// The COUNT is the assertion that matters here, not the status code. A stored
// record this middleware cannot decode used to fall through to c.Next(), which
// re-executes the mutation while returning a perfectly plausible 201 — so a
// status assertion alone passes over the defect. On a financial API that second
// execution is a duplicated money movement.
func countingApp(mw fiber.Handler, tenantID string, calls *atomic.Int64) *fiber.App {
	app := fiber.New()
	app.Use(tenantMiddleware(tenantID))
	app.Use(mw)
	app.Post("/test", func(c fiber.Ctx) error {
		calls.Add(1)

		return c.Status(fiber.StatusCreated).JSON(fiber.Map{"status": "created"})
	})

	return app
}

// legacyValue builds the plain-text record lib-commons v6.4.0 wrote under the
// primary key: "<state>:<fingerprint>". The key name is byte-identical across
// the format change, so these values are what a v6.6.0 process finds mid-deploy.
func legacyValue(state, fingerprint string) string {
	return state + ":" + fingerprint
}

// TestCheck_LegacyPlainTextRecord_NeverReExecutesHandler covers the rolling
// deploy that crosses v6.4.0 -> v6.5.0+: the primary key holds plain text rather
// than the atomic JSON record, and a legitimate client retry MUST be answered
// from that record instead of being executed a second time.
func TestCheck_LegacyPlainTextRecord_NeverReExecutesHandler(t *testing.T) {
	t.Parallel()

	const tenant = "tenant-legacy"

	current := requestFingerprint(http.MethodPost, "/test", nil)
	other := requestFingerprint(http.MethodPost, "/other", nil)

	tests := []struct {
		name           string
		stored         string
		wantStatus     int
		wantBody       string
		wantReplayed   string
		wantRetryAfter string
	}{
		{
			name:           "processing with matching fingerprint returns in-flight conflict",
			stored:         legacyValue(keyStateProcessing, current),
			wantStatus:     http.StatusConflict,
			wantBody:       "IDEMPOTENCY_CONFLICT",
			wantReplayed:   "true",
			wantRetryAfter: retryAfterSeconds,
		},
		{
			name:       "processing with differing fingerprint returns key reuse",
			stored:     legacyValue(keyStateProcessing, other),
			wantStatus: http.StatusUnprocessableEntity,
			wantBody:   "IDEMPOTENCY_KEY_REUSE",
		},
		{
			name:         "complete with matching fingerprint reports already processed",
			stored:       legacyValue(keyStateComplete, current),
			wantStatus:   http.StatusOK,
			wantBody:     "IDEMPOTENT",
			wantReplayed: "true",
		},
		{
			// The fingerprint gate runs BEFORE the state routing, exactly as it
			// does for the JSON record and as v6.4.0 itself did. Answering a
			// differing payload with "already processed" would report success for
			// an operation that never ran.
			name:       "complete with differing fingerprint returns key reuse",
			stored:     legacyValue(keyStateComplete, other),
			wantStatus: http.StatusUnprocessableEntity,
			wantBody:   "IDEMPOTENCY_KEY_REUSE",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			miniRedis := miniredis.RunT(t)
			conn := newRedisClient(t, miniRedis)
			middleware := New(conn)

			idempotencyKey := "legacy-" + testCase.name
			require.NoError(t, miniRedis.Set(
				fmt.Sprintf("idempotency:%s:%s", tenant, idempotencyKey), testCase.stored))

			var calls atomic.Int64

			response := doPost(t, countingApp(middleware.Check(), tenant, &calls), idempotencyKey)
			body := readBody(t, response)

			assert.Equal(t, int64(0), calls.Load(),
				"handler MUST NOT run for a legacy record: the mutation would execute a second time")
			assert.Equal(t, testCase.wantStatus, response.StatusCode)
			assert.Contains(t, body, testCase.wantBody)
			assert.Equal(t, testCase.wantReplayed, response.Header.Get(chttp.IdempotencyReplayed))
			assert.Equal(t, testCase.wantRetryAfter, response.Header.Get(fiber.HeaderRetryAfter))
		})
	}
}

// TestCheck_LegacyPlainTextRecord_FailClosedStoreAlsoRefuses proves the legacy
// branch answers from the record under NewWithStore too, rather than degrading
// to the fail-closed 503 that a store error produces.
func TestCheck_LegacyPlainTextRecord_FailClosedStoreAlsoRefuses(t *testing.T) {
	t.Parallel()

	current := requestFingerprint(http.MethodPost, "/test", nil)

	tests := []struct {
		name       string
		stored     string
		wantStatus int
		wantBody   string
	}{
		{
			name:       "processing",
			stored:     legacyValue(keyStateProcessing, current),
			wantStatus: http.StatusConflict,
			wantBody:   "IDEMPOTENCY_CONFLICT",
		},
		{
			name:       "complete",
			stored:     legacyValue(keyStateComplete, current),
			wantStatus: http.StatusOK,
			wantBody:   "IDEMPOTENT",
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			controller := gomock.NewController(t)
			store := NewMockStore(controller)
			store.EXPECT().Acquire(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
				Return([]byte(testCase.stored), false, nil)

			var calls atomic.Int64

			middleware := NewWithStore(store, WithFailClosed(true))
			response := doPost(t, countingApp(middleware.Check(), "tenant-legacy-fc", &calls), "legacy-fc")
			body := readBody(t, response)

			assert.Equal(t, int64(0), calls.Load(), "handler MUST NOT run for a legacy record")
			assert.Equal(t, testCase.wantStatus, response.StatusCode)
			assert.Contains(t, body, testCase.wantBody)
		})
	}
}

// TestCheck_UndecodableNonLegacyRecord_KeepsStoreErrorPath pins the detector
// closed. Only "processing:" or "complete:" prefixed plain text is legacy;
// every other undecodable value keeps today's store-error path in BOTH
// postures. A permissive detector would be a second version of the same bug:
// unknown bytes granting permission to act on the money path.
func TestCheck_UndecodableNonLegacyRecord_KeepsStoreErrorPath(t *testing.T) {
	t.Parallel()

	values := map[string]string{
		"plain garbage":            "garbage",
		"unknown state with colon": "weird:state",
		"truncated json":           "{truncated",
		"empty value":              "",
		"separator only":           ":",
		"bare complete state":      keyStateComplete,
		"bare processing state":    keyStateProcessing,
		"state prefix not exact":   "processingx:abc",
		"state suffix not exact":   "xprocessing:abc",
	}

	for name, stored := range values {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			t.Run("fail_open_runs_handler_once", func(t *testing.T) {
				t.Parallel()

				calls, response, body := runWithSeededValue(t, "tenant-nonlegacy-fo", stored, false)

				assert.Equal(t, int64(1), calls, "fail-open behaviour must be unchanged")
				assert.Equal(t, http.StatusCreated, response.StatusCode)
				assert.Contains(t, body, `"status":"created"`)
			})

			t.Run("fail_closed_rejects_with_503", func(t *testing.T) {
				t.Parallel()

				calls, response, body := runWithSeededValue(t, "tenant-nonlegacy-fc", stored, true)

				assert.Equal(t, int64(0), calls, "fail-closed behaviour must be unchanged")
				assert.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
				assert.Contains(t, body, "IDEMPOTENCY_UNAVAILABLE")
			})
		})
	}
}

// runWithSeededValue seeds one raw value under the tenant-scoped key and drives
// a single POST through the middleware, returning the handler call count.
func runWithSeededValue(t *testing.T, tenant, stored string, failClosed bool) (int64, *http.Response, string) {
	t.Helper()

	miniRedis := miniredis.RunT(t)
	conn := newRedisClient(t, miniRedis)
	middleware := New(conn, WithFailClosed(failClosed))

	const idempotencyKey = "nonlegacy-key"

	require.NoError(t, miniRedis.Set(
		fmt.Sprintf("idempotency:%s:%s", tenant, idempotencyKey), stored))

	var calls atomic.Int64

	response := doPost(t, countingApp(middleware.Check(), tenant, &calls), idempotencyKey)

	return calls.Load(), response, readBody(t, response)
}
