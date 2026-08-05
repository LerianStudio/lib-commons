//go:build unit

package idempotency

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	chttp "github.com/LerianStudio/lib-commons/v6/commons/constants"
	tmcore "github.com/LerianStudio/lib-commons/v6/commons/tenant-manager/core"
	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v3"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

const (
	bridgeDashedTenant   = "550e8400-e29b-41d4-a716-446655440000"
	bridgeDashlessTenant = "550e8400e29b41d4a716446655440000"
)

func tenantIdentityMiddleware(canonical, original string) fiber.Handler {
	return func(c fiber.Ctx) error {
		ctx := tmcore.ContextWithTenantID(c.Context(), canonical)
		if original != "" {
			ctx = tmcore.ContextWithOriginalTenantID(ctx, original)
		}
		c.SetContext(ctx)

		return c.Next()
	}
}

func readV62Record(t *testing.T, miniRedis *miniredis.Miniredis, key, fingerprint string) (int, cachedResponse) {
	t.Helper()

	marker, err := miniRedis.Get(key)
	require.NoError(t, err)
	state, storedFingerprint, found := strings.Cut(marker, stateSeparator)
	require.True(t, found)

	if storedFingerprint != fingerprint {
		return http.StatusUnprocessableEntity, cachedResponse{}
	}

	if state == keyStateProcessing {
		return http.StatusConflict, cachedResponse{}
	}

	require.Equal(t, keyStateComplete, state)
	encoded, err := miniRedis.Get(key + ":response")
	require.NoError(t, err)

	var response cachedResponse
	require.NoError(t, json.Unmarshal([]byte(encoded), &response))

	return response.StatusCode, response
}

func TestCheck_RedisLegacyBridgeWrite_V62ReaderUnderstandsProcessingAndComplete(t *testing.T) {
	t.Parallel()

	requestBody := `{"amount":10}`
	fingerprint := requestFingerprint(http.MethodPost, "/test", []byte(requestBody))

	t.Run("processing marker gives old reader native conflict", func(t *testing.T) {
		t.Parallel()

		miniRedis := miniredis.RunT(t)
		client := newRedisClient(t, miniRedis)
		middleware := New(client, WithRedisLegacyBridge())
		started := make(chan struct{})
		release := make(chan struct{})
		app := fiber.New()
		app.Use(tenantIdentityMiddleware(bridgeDashlessTenant, bridgeDashedTenant))
		app.Use(middleware.Check())
		app.Post("/test", func(c fiber.Ctx) error {
			close(started)
			<-release

			return c.Status(http.StatusCreated).SendString("created")
		})

		request := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(requestBody))
		request.Header.Set(chttp.IdempotencyKey, "processing-key")
		result := make(chan error, 1)
		go func() {
			response, err := app.Test(request, fiber.TestConfig{Timeout: 0})
			if err == nil {
				err = response.Body.Close()
			}
			result <- err
		}()

		<-started
		key := "idempotency:" + bridgeDashedTenant + ":processing-key"
		status, _ := readV62Record(t, miniRedis, key, fingerprint)
		assert.Equal(t, http.StatusConflict, status)
		canonicalBytes, err := miniRedis.Get("idempotency:" + bridgeDashlessTenant + ":processing-key")
		require.NoError(t, err)
		canonicalRecord, err := decodeCurrentStoreRecord([]byte(canonicalBytes))
		require.NoError(t, err)
		assert.Equal(t, keyStateProcessing, canonicalRecord.State)
		close(release)
		require.NoError(t, <-result)
	})

	t.Run("complete record replays through old reader with headers preserved", func(t *testing.T) {
		t.Parallel()

		miniRedis := miniredis.RunT(t)
		client := newRedisClient(t, miniRedis)
		middleware := New(client, WithRedisLegacyBridge(), WithMaxBodyCache(256))
		app := fiber.New()
		app.Use(tenantIdentityMiddleware(bridgeDashlessTenant, bridgeDashedTenant))
		app.Use(middleware.Check())
		app.Post("/test", func(c fiber.Ctx) error {
			c.Append(fiber.HeaderSetCookie, "a=long-value")
			c.Append(fiber.HeaderSetCookie, "b=another-long-value")
			c.Append("X-Bridge", "first")
			c.Append("X-Bridge", "second")

			return c.Status(http.StatusAccepted).SendString("tiny")
		})

		response := doSend(t, app, http.MethodPost, "/test", requestBody, "complete-key")
		require.Equal(t, http.StatusAccepted, response.StatusCode)
		readBody(t, response)

		key := "idempotency:" + bridgeDashedTenant + ":complete-key"
		status, replay := readV62Record(t, miniRedis, key, fingerprint)
		assert.Equal(t, http.StatusAccepted, status)
		assert.Equal(t, []byte("tiny"), replay.Body)
		setCookies := strings.Join(replay.Headers[fiber.HeaderSetCookie], ",")
		assert.Contains(t, setCookies, "a=long-value")
		assert.Contains(t, setCookies, "b=another-long-value")
		bridgeHeaders := strings.Join(replay.Headers["X-Bridge"], ",")
		assert.Contains(t, bridgeHeaders, "first")
		assert.Contains(t, bridgeHeaders, "second")
		assert.NotContains(t, miniRedis.Keys(), key+":bridge-owner")

		stored, err := miniRedis.Get(key)
		require.NoError(t, err)
		assert.Equal(t, keyStateComplete+stateSeparator+fingerprint, stored)
		assert.Error(t, json.Unmarshal([]byte(stored), &storeRecord{}),
			"bridge writes must stay on the exact v6.2 marker format")
		canonicalBytes, err := miniRedis.Get("idempotency:" + bridgeDashlessTenant + ":complete-key")
		require.NoError(t, err)
		canonicalRecord, err := decodeCurrentStoreRecord([]byte(canonicalBytes))
		require.NoError(t, err)
		assert.Equal(t, keyStateComplete, canonicalRecord.State)
		legacyResponse, err := miniRedis.Get(key + ":response")
		require.NoError(t, err)
		assert.Equal(t, []byte(legacyResponse), canonicalRecord.Response)
	})

	// A small body with large headers can push the plaintext envelope past the
	// replay bound. Storing it would poison the key: after the bridge is
	// disabled, replay of the canonical record returns 503 until the TTL
	// expires. The bridge must fail closed before completion instead.
	t.Run("oversized replay envelope fails closed before completion", func(t *testing.T) {
		t.Parallel()

		miniRedis := miniredis.RunT(t)
		client := newRedisClient(t, miniRedis)
		middleware := New(client, WithRedisLegacyBridge(), WithMaxBodyCache(4))
		app := fiber.New()
		app.Use(tenantIdentityMiddleware(bridgeDashlessTenant, bridgeDashedTenant))
		app.Use(middleware.Check())
		app.Post("/test", func(c fiber.Ctx) error {
			c.Set("X-Bridge-Padding", strings.Repeat("h", 64))

			return c.Status(http.StatusAccepted).SendString("tiny")
		})

		response := doSend(t, app, http.MethodPost, "/test", requestBody, "oversized-key")
		body := readBody(t, response)
		assert.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
		assert.Contains(t, body, "IDEMPOTENCY_UNAVAILABLE")

		key := "idempotency:" + bridgeDashedTenant + ":oversized-key"
		stored, err := miniRedis.Get(key)
		require.NoError(t, err)
		state, _, valid := parseLegacyRecord([]byte(stored))
		assert.True(t, valid)
		assert.Equal(t, keyStateProcessing, state,
			"an unreplayable response must never be persisted as complete")
		assert.NotContains(t, miniRedis.Keys(), key+":response")
	})
}

func TestCheck_RedisLegacyBridgeRead_V62RecordsAcrossTenantSpellings(t *testing.T) {
	t.Parallel()

	requestBody := `{"amount":10}`
	fingerprint := requestFingerprint(http.MethodPost, "/test", []byte(requestBody))

	tests := []struct {
		name          string
		canonical     string
		original      string
		state         string
		wantStatus    int
		wantBody      string
		writeResponse bool
	}{
		{name: "dashed processing", canonical: bridgeDashlessTenant, original: bridgeDashedTenant,
			state: keyStateProcessing, wantStatus: http.StatusConflict, wantBody: "IDEMPOTENCY_CONFLICT"},
		{name: "dashed complete", canonical: bridgeDashlessTenant, original: bridgeDashedTenant,
			state: keyStateComplete, wantStatus: http.StatusAccepted, wantBody: "legacy-body", writeResponse: true},
		{name: "dashless processing", canonical: bridgeDashlessTenant, original: bridgeDashlessTenant,
			state: keyStateProcessing, wantStatus: http.StatusConflict, wantBody: "IDEMPOTENCY_CONFLICT"},
		{name: "dashless complete", canonical: bridgeDashlessTenant, original: bridgeDashlessTenant,
			state: keyStateComplete, wantStatus: http.StatusAccepted, wantBody: "legacy-body", writeResponse: true},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			miniRedis := miniredis.RunT(t)
			client := newRedisClient(t, miniRedis)
			key := "idempotency:" + testCase.original + ":legacy-key"
			require.NoError(t, miniRedis.Set(key, testCase.state+stateSeparator+fingerprint))
			if testCase.writeResponse {
				require.NoError(t, miniRedis.Set(key+":response", legacyReplayResponse))
			}

			var handlerCalls atomic.Int32
			middleware := New(client, WithRedisLegacyBridge())
			app := newEchoApp(middleware.Check(), &handlerCalls,
				tenantIdentityMiddleware(testCase.canonical, testCase.original))
			response := doSend(t, app, http.MethodPost, "/test", requestBody, "legacy-key")
			body := readBody(t, response)

			assert.Equal(t, testCase.wantStatus, response.StatusCode)
			assert.Contains(t, body, testCase.wantBody)
			assert.Zero(t, handlerCalls.Load())
		})
	}
}

func TestCheck_RedisLegacyBridge_TenantKeyUsesOriginalOrDeterministicFallback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		canonical string
		original  string
		wantKey   string
	}{
		{
			name:      "HTTP identity uses original dashed spelling",
			canonical: bridgeDashlessTenant,
			original:  bridgeDashedTenant,
			wantKey:   "idempotency:" + bridgeDashedTenant + ":bridge-key",
		},
		{
			name:      "direct dashless context falls back to its exact spelling",
			canonical: bridgeDashlessTenant,
			wantKey:   "idempotency:" + bridgeDashlessTenant + ":bridge-key",
		},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			miniRedis := miniredis.RunT(t)
			client := newRedisClient(t, miniRedis)
			middleware := New(client, WithRedisLegacyBridge())
			app := newPostApp(middleware.Check(), tenantIdentityMiddleware(testCase.canonical, testCase.original))
			response := doPost(t, app, "bridge-key")
			readBody(t, response)

			assert.Contains(t, miniRedis.Keys(), testCase.wantKey)
		})
	}
}

func TestCheck_RedisLegacyBridge_ServerFailureReleasesOwnedClaim(t *testing.T) {
	t.Parallel()

	miniRedis := miniredis.RunT(t)
	client := newRedisClient(t, miniRedis)
	middleware := New(client, WithRedisLegacyBridge())
	var handlerCalls atomic.Int32
	app := fiber.New()
	app.Use(tenantIdentityMiddleware(bridgeDashlessTenant, bridgeDashedTenant))
	app.Use(middleware.Check())
	app.Post("/test", func(c fiber.Ctx) error {
		if handlerCalls.Add(1) == 1 {
			return c.Status(http.StatusServiceUnavailable).SendString("retry")
		}

		return c.Status(http.StatusCreated).SendString("created")
	})

	first := doPost(t, app, "release-key")
	readBody(t, first)
	assert.Equal(t, http.StatusServiceUnavailable, first.StatusCode)
	key := "idempotency:" + bridgeDashedTenant + ":release-key"
	canonicalKey := "idempotency:" + bridgeDashlessTenant + ":release-key"
	assert.NotContains(t, miniRedis.Keys(), key)
	assert.NotContains(t, miniRedis.Keys(), canonicalKey)
	assert.NotContains(t, miniRedis.Keys(), key+":response")
	assert.NotContains(t, miniRedis.Keys(), key+":bridge-owner")

	second := doPost(t, app, "release-key")
	readBody(t, second)
	assert.Equal(t, http.StatusCreated, second.StatusCode)
	assert.Equal(t, int32(2), handlerCalls.Load())
}

func TestRedisStore_LegacyBridgeOwnerFenceRejectsStaleNewWorker(t *testing.T) {
	t.Parallel()

	miniRedis := miniredis.RunT(t)
	store := newRedisStore(newRedisClient(t, miniRedis))
	ctx := context.Background()
	keys := bridgeKeyPair{
		legacy:    "idempotency:" + bridgeDashedTenant + ":stale-bridge",
		canonical: "idempotency:" + bridgeDashlessTenant + ":stale-bridge",
	}
	fingerprint := requestFingerprint(http.MethodPost, "/test", nil)
	processingRecord := storeRecord{State: keyStateProcessing, Fingerprint: fingerprint, Owner: "owner-old"}
	processingCanonical, err := json.Marshal(processingRecord)
	require.NoError(t, err)
	processing := bridgeRecordPair{
		legacy:    []byte(keyStateProcessing + stateSeparator + fingerprint),
		canonical: processingCanonical,
	}

	_, acquired, err := store.AcquireBridge(ctx, keys, processing, "owner-old", time.Minute)
	require.NoError(t, err)
	require.True(t, acquired)
	miniRedis.FastForward(time.Minute + time.Second)

	replacementRecord := processingRecord
	replacementRecord.Owner = "owner-replacement"
	replacementCanonical, err := json.Marshal(replacementRecord)
	require.NoError(t, err)
	replacement := bridgeRecordPair{legacy: processing.legacy, canonical: replacementCanonical}
	_, acquired, err = store.AcquireBridge(ctx, keys, replacement, "owner-replacement", time.Hour)
	require.NoError(t, err)
	require.True(t, acquired)

	completedRecord := processingRecord
	completedRecord.State = keyStateComplete
	completedRecord.Response = []byte(legacyReplayResponse)
	completedCanonical, err := json.Marshal(completedRecord)
	require.NoError(t, err)
	completed := bridgeRecordPair{
		legacy:    []byte(keyStateComplete + stateSeparator + fingerprint),
		canonical: completedCanonical,
	}
	applied, err := store.CompleteBridge(
		ctx, keys, processing, completed, []byte(legacyReplayResponse), "owner-old", time.Hour,
	)
	require.NoError(t, err)
	assert.False(t, applied)

	applied, err = store.ReleaseBridge(ctx, keys, processing, "owner-old")
	require.NoError(t, err)
	assert.False(t, applied)

	stored, err := miniRedis.Get(keys.legacy)
	require.NoError(t, err)
	assert.Equal(t, string(replacement.legacy), stored)
	stored, err = miniRedis.Get(keys.canonical)
	require.NoError(t, err)
	assert.Equal(t, string(replacement.canonical), stored)

	completedRecord.Owner = "owner-replacement"
	completedCanonical, err = json.Marshal(completedRecord)
	require.NoError(t, err)
	completed.canonical = completedCanonical
	applied, err = store.CompleteBridge(ctx, keys, replacement, completed, []byte(legacyReplayResponse),
		"owner-replacement", time.Hour)
	require.NoError(t, err)
	assert.True(t, applied)
}

func TestNewWithStore_RedisLegacyBridgeOptionRejectsSafely(t *testing.T) {
	t.Parallel()

	controller := gomock.NewController(t)
	store := NewMockStore(controller)
	middleware := NewWithStore(store, WithRedisLegacyBridge())
	var handlerCalled atomic.Bool
	response := doPost(t, spyApp(middleware.Check(), "tenant-custom", &handlerCalled), "bridge-key")
	body := readBody(t, response)

	assert.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
	assert.Contains(t, body, "IDEMPOTENCY_UNAVAILABLE")
	assert.False(t, handlerCalled.Load())
}

func TestLegacyBridge_ClusterClientIsRejectedBeforeScriptsRun(t *testing.T) {
	t.Parallel()

	client := redis.NewClusterClient(&redis.ClusterOptions{Addrs: []string{"127.0.0.1:6379"}})
	t.Cleanup(func() {
		require.NoError(t, client.Close())
	})

	assert.ErrorIs(t, validateLegacyBridgeClient(client), errLegacyBridgeClusterUnsupported)
}

func TestRedisStore_LegacyBridgeDualNamespaceLuaFailuresAreAtomic(t *testing.T) {
	t.Parallel()

	newPair := func(t *testing.T, owner string) (bridgeKeyPair, bridgeRecordPair, bridgeRecordPair) {
		t.Helper()

		fingerprint := requestFingerprint(http.MethodPost, "/test", nil)
		processingRecord := storeRecord{State: keyStateProcessing, Fingerprint: fingerprint, Owner: owner}
		processingJSON, err := json.Marshal(processingRecord)
		require.NoError(t, err)
		completedRecord := processingRecord
		completedRecord.State = keyStateComplete
		completedRecord.Response = []byte(legacyReplayResponse)
		completedJSON, err := json.Marshal(completedRecord)
		require.NoError(t, err)

		return bridgeKeyPair{
				legacy:    "idempotency:" + bridgeDashedTenant + ":lua-failure",
				canonical: "idempotency:" + bridgeDashlessTenant + ":lua-failure",
			}, bridgeRecordPair{
				legacy:    []byte(keyStateProcessing + stateSeparator + fingerprint),
				canonical: processingJSON,
			}, bridgeRecordPair{
				legacy:    []byte(keyStateComplete + stateSeparator + fingerprint),
				canonical: completedJSON,
			}
	}

	t.Run("acquire wrong type creates no legacy half", func(t *testing.T) {
		t.Parallel()

		miniRedis := miniredis.RunT(t)
		store := newRedisStore(newRedisClient(t, miniRedis))
		keys, processing, _ := newPair(t, "owner-a")
		_, err := miniRedis.Lpush(keys.canonical, "wrong-type")
		require.NoError(t, err)

		_, acquired, err := store.AcquireBridge(context.Background(), keys, processing, "owner-a", time.Hour)
		assert.Error(t, err)
		assert.False(t, acquired)
		assert.NotContains(t, miniRedis.Keys(), keys.legacy)
		assert.NotContains(t, miniRedis.Keys(), bridgeOwnerKey(keys.legacy))
	})

	t.Run("complete script error preserves both processing claims", func(t *testing.T) {
		t.Parallel()

		miniRedis := miniredis.RunT(t)
		store := newRedisStore(newRedisClient(t, miniRedis))
		keys, processing, completed := newPair(t, "owner-a")
		_, acquired, err := store.AcquireBridge(context.Background(), keys, processing, "owner-a", time.Hour)
		require.NoError(t, err)
		require.True(t, acquired)
		miniRedis.Del(bridgeOwnerKey(keys.legacy))
		_, err = miniRedis.Lpush(bridgeOwnerKey(keys.legacy), "wrong-type")
		require.NoError(t, err)

		applied, err := store.CompleteBridge(context.Background(), keys, processing, completed,
			[]byte(legacyReplayResponse), "owner-a", time.Hour)
		assert.Error(t, err)
		assert.False(t, applied)
		legacy, legacyErr := miniRedis.Get(keys.legacy)
		require.NoError(t, legacyErr)
		canonical, canonicalErr := miniRedis.Get(keys.canonical)
		require.NoError(t, canonicalErr)
		assert.Equal(t, string(processing.legacy), legacy)
		assert.Equal(t, string(processing.canonical), canonical)
		assert.NotContains(t, miniRedis.Keys(), legacyResponseKey(keys.legacy))
	})

	t.Run("release script error preserves both processing claims", func(t *testing.T) {
		t.Parallel()

		miniRedis := miniredis.RunT(t)
		store := newRedisStore(newRedisClient(t, miniRedis))
		keys, processing, _ := newPair(t, "owner-a")
		_, acquired, err := store.AcquireBridge(context.Background(), keys, processing, "owner-a", time.Hour)
		require.NoError(t, err)
		require.True(t, acquired)
		miniRedis.Del(bridgeOwnerKey(keys.legacy))
		_, err = miniRedis.Lpush(bridgeOwnerKey(keys.legacy), "wrong-type")
		require.NoError(t, err)

		applied, err := store.ReleaseBridge(context.Background(), keys, processing, "owner-a")
		assert.Error(t, err)
		assert.False(t, applied)
		assert.Contains(t, miniRedis.Keys(), keys.legacy)
		assert.Contains(t, miniRedis.Keys(), keys.canonical)
	})

	t.Run("invalid acquire result is never treated as connectivity", func(t *testing.T) {
		t.Parallel()

		_, acquired, err := parseRedisBridgeAcquireResult([]any{int64(0), "", ""})
		assert.ErrorIs(t, err, errInvalidStoreResult)
		assert.False(t, acquired)
		assert.Equal(t, storeFailureUnsafe, classifyStoreFailure(err, false))
	})
}
