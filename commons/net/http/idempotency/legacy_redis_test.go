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
)

const legacyReplayResponse = `{"status_code":202,"content_type":"application/vnd.legacy+json","body":"bGVnYWN5LWJvZHk=","headers":{"Location":["/legacy/42"],"Set-Cookie":["a=1","b=2"]}}`

func TestCheck_LegacyRedisRecord_ProtectsMutation(t *testing.T) {
	t.Parallel()

	requestBody := `{"amount":10}`
	matchingFingerprint := requestFingerprint(http.MethodPost, "/test", []byte(requestBody))
	differentFingerprint := requestFingerprint(http.MethodPost, "/test", []byte(`{"amount":11}`))

	tests := []struct {
		name            string
		marker          string
		legacyResponse  string
		responsePresent bool
		wantStatus      int
		wantBody        string
		wantReplayed    string
		wantRetryAfter  string
		wantContentType string
		wantLocation    string
		wantCookies     []string
	}{
		{
			name:           "processing with matching fingerprint returns in-flight conflict",
			marker:         keyStateProcessing + ":" + matchingFingerprint,
			wantStatus:     http.StatusConflict,
			wantBody:       "IDEMPOTENCY_CONFLICT",
			wantReplayed:   "true",
			wantRetryAfter: retryAfterSeconds,
		},
		{
			name:       "processing with different fingerprint returns key reuse",
			marker:     keyStateProcessing + ":" + differentFingerprint,
			wantStatus: http.StatusUnprocessableEntity,
			wantBody:   "IDEMPOTENCY_KEY_REUSE",
		},
		{
			name:            "complete with matching fingerprint replays exact response",
			marker:          keyStateComplete + ":" + matchingFingerprint,
			legacyResponse:  legacyReplayResponse,
			responsePresent: true,
			wantStatus:      http.StatusAccepted,
			wantBody:        "legacy-body",
			wantReplayed:    "true",
			wantContentType: "application/vnd.legacy+json",
			wantLocation:    "/legacy/42",
			wantCookies:     []string{"a=1", "b=2"},
		},
		{
			name:            "complete with different fingerprint returns key reuse",
			marker:          keyStateComplete + ":" + differentFingerprint,
			legacyResponse:  legacyReplayResponse,
			responsePresent: true,
			wantStatus:      http.StatusUnprocessableEntity,
			wantBody:        "IDEMPOTENCY_KEY_REUSE",
		},
		{
			name:       "complete with missing response fails closed",
			marker:     keyStateComplete + ":" + matchingFingerprint,
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "IDEMPOTENCY_UNAVAILABLE",
		},
		{
			name:            "complete with corrupt response fails closed",
			marker:          keyStateComplete + ":" + matchingFingerprint,
			legacyResponse:  `{"status_code":`,
			responsePresent: true,
			wantStatus:      http.StatusServiceUnavailable,
			wantBody:        "IDEMPOTENCY_UNAVAILABLE",
		},
		{
			name:       "legacy marker without fingerprint fails closed",
			marker:     keyStateProcessing,
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "IDEMPOTENCY_UNAVAILABLE",
		},
		{
			name:       "legacy marker with unknown state fails closed",
			marker:     "unknown:" + matchingFingerprint,
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "IDEMPOTENCY_UNAVAILABLE",
		},
		{
			name:       "undecodable current record fails closed",
			marker:     `{"state":`,
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "IDEMPOTENCY_UNAVAILABLE",
		},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			miniRedis := miniredis.RunT(t)
			client := newRedisClient(t, miniRedis)
			middleware := New(client)
			key := "idempotency:tenant-legacy:legacy-key"
			require.NoError(t, miniRedis.Set(key, testCase.marker))

			if testCase.responsePresent {
				require.NoError(t, miniRedis.Set(key+":response", testCase.legacyResponse))
			}

			var handlerCalls atomic.Int32
			app := newEchoApp(middleware.Check(), &handlerCalls, tenantMiddleware("tenant-legacy"))
			response := doSend(t, app, http.MethodPost, "/test", requestBody, "legacy-key")
			body := readBody(t, response)

			assert.Equal(t, testCase.wantStatus, response.StatusCode)
			assert.Contains(t, body, testCase.wantBody)
			assert.Equal(t, testCase.wantReplayed, response.Header.Get(chttp.IdempotencyReplayed))
			assert.Equal(t, testCase.wantRetryAfter, response.Header.Get(fiber.HeaderRetryAfter))

			if testCase.wantContentType != "" {
				assert.Equal(t, testCase.wantContentType, response.Header.Get(fiber.HeaderContentType))
				assert.Equal(t, testCase.wantLocation, response.Header.Get(fiber.HeaderLocation))
				assert.ElementsMatch(t, testCase.wantCookies, response.Header.Values(fiber.HeaderSetCookie))
			}

			assert.Zero(t, handlerCalls.Load(), "a stored or corrupt idempotency record must never execute the mutation")
		})
	}
}

func TestCheck_CurrentJSONRecord_StillReplays(t *testing.T) {
	t.Parallel()

	miniRedis := miniredis.RunT(t)
	client := newRedisClient(t, miniRedis)
	middleware := New(client)
	requestBody := `{"amount":10}`
	key := "idempotency:tenant-current:current-key"
	seedStoreRecord(t, miniRedis, key, storeRecord{
		State:       keyStateComplete,
		Fingerprint: requestFingerprint(http.MethodPost, "/test", []byte(requestBody)),
		Owner:       "current-owner",
		Response:    []byte(legacyReplayResponse),
	})

	var handlerCalls atomic.Int32
	app := newEchoApp(middleware.Check(), &handlerCalls, tenantMiddleware("tenant-current"))
	response := doSend(t, app, http.MethodPost, "/test", requestBody, "current-key")
	body := readBody(t, response)

	assert.Equal(t, http.StatusAccepted, response.StatusCode)
	assert.Equal(t, "legacy-body", body)
	assert.Equal(t, "true", response.Header.Get(chttp.IdempotencyReplayed))
	assert.Zero(t, handlerCalls.Load())
}

func TestCheck_CurrentJSONRecord_ValidatesStructureBeforeFingerprint(t *testing.T) {
	t.Parallel()

	requestBody := `{"amount":10}`
	matchingFingerprint := requestFingerprint(http.MethodPost, "/test", []byte(requestBody))
	differentFingerprint := requestFingerprint(http.MethodPost, "/test", []byte(`{"amount":11}`))

	tests := []struct {
		name   string
		record string
	}{
		{name: "null record", record: "null"},
		{name: "invalid fingerprint", record: `{"state":"processing","fingerprint":"short","owner":"owner"}`},
		{name: "processing missing owner before mismatch", record: fmt.Sprintf(
			`{"state":"processing","fingerprint":%q,"owner":""}`, differentFingerprint)},
		{name: "complete missing owner before mismatch", record: fmt.Sprintf(
			`{"state":"complete","fingerprint":%q,"response":"eA=="}`, differentFingerprint)},
		{name: "complete missing response before mismatch", record: fmt.Sprintf(
			`{"state":"complete","fingerprint":%q,"owner":"owner"}`, differentFingerprint)},
		{name: "unknown state before mismatch", record: fmt.Sprintf(
			`{"state":"unknown","fingerprint":%q,"owner":"owner","response":"eA=="}`, differentFingerprint)},
		{name: "processing response is inconsistent", record: fmt.Sprintf(
			`{"state":"processing","fingerprint":%q,"owner":"owner","response":"eA=="}`, matchingFingerprint)},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			miniRedis := miniredis.RunT(t)
			client := newRedisClient(t, miniRedis)
			key := "idempotency:tenant-current-invalid:current-key"
			require.NoError(t, miniRedis.Set(key, testCase.record))

			var handlerCalls atomic.Int32
			middleware := New(client)
			app := newEchoApp(middleware.Check(), &handlerCalls, tenantMiddleware("tenant-current-invalid"))
			response := doSend(t, app, http.MethodPost, "/test", requestBody, "current-key")
			body := readBody(t, response)

			assert.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
			assert.Contains(t, body, "IDEMPOTENCY_UNAVAILABLE")
			assert.Zero(t, handlerCalls.Load())
		})
	}
}

func TestCheck_LegacyReplay_LimitsRawBodyNotSerializedEnvelope(t *testing.T) {
	t.Parallel()

	miniRedis := miniredis.RunT(t)
	client := newRedisClient(t, miniRedis)
	requestBody := `{"amount":10}`
	fingerprint := requestFingerprint(http.MethodPost, "/test", []byte(requestBody))
	key := "idempotency:tenant-small-limit:legacy-key"
	replay := `{"status_code":202,"content_type":"application/octet-stream","body":"dGlueQ==","headers":{"Set-Cookie":["first=long-value","second=another-long-value"],"X-Migration":["v6.2","bridge"]}}`
	require.Greater(t, len(replay), 8)
	require.NoError(t, miniRedis.Set(key, keyStateComplete+stateSeparator+fingerprint))
	require.NoError(t, miniRedis.Set(key+":response", replay))

	var handlerCalls atomic.Int32
	middleware := New(client, WithMaxBodyCache(4))
	app := newEchoApp(middleware.Check(), &handlerCalls, tenantMiddleware("tenant-small-limit"))
	response := doSend(t, app, http.MethodPost, "/test", requestBody, "legacy-key")
	body := readBody(t, response)

	assert.Equal(t, http.StatusAccepted, response.StatusCode)
	assert.Equal(t, "tiny", body)
	assert.ElementsMatch(t, []string{"first=long-value", "second=another-long-value"},
		response.Header.Values(fiber.HeaderSetCookie))
	assert.ElementsMatch(t, []string{"v6.2", "bridge"}, response.Header.Values("X-Migration"))
	assert.Zero(t, handlerCalls.Load())
}
