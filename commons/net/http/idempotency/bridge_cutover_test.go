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
	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type bridgeNonPlaintextCodec struct{}

type bridgeRaceResult struct {
	status int
	err    error
}

func (bridgeNonPlaintextCodec) Encode(_ context.Context, plaintext []byte) ([]byte, error) {
	return append([]byte("encrypted:"), plaintext...), nil
}

func (bridgeNonPlaintextCodec) Decode(_ context.Context, encoded []byte) ([]byte, error) {
	return append([]byte(nil), encoded...), nil
}

func TestCheck_RedisLegacyBridge_CutoverReplaysFromCanonicalRecordAfterDisable(t *testing.T) {
	t.Parallel()

	miniRedis := miniredis.RunT(t)
	client := newRedisClient(t, miniRedis)
	requestBody := `{"amount":10}`
	const requestKey = "cutover-key"
	var handlerCalls atomic.Int32

	bridgeApp := newEchoApp(New(client, WithRedisLegacyBridge()).Check(), &handlerCalls,
		tenantIdentityMiddleware(bridgeDashlessTenant, bridgeDashedTenant))
	first := doSend(t, bridgeApp, http.MethodPost, "/test", requestBody, requestKey)
	assert.Equal(t, http.StatusCreated, first.StatusCode)
	assert.Equal(t, requestBody, readBody(t, first))

	legacyKey := "idempotency:" + bridgeDashedTenant + ":" + requestKey
	canonicalKey := "idempotency:" + bridgeDashlessTenant + ":" + requestKey
	legacyStatus, legacyResponse := readV62Record(t, miniRedis, legacyKey,
		requestFingerprint(http.MethodPost, "/test", []byte(requestBody)))
	assert.Equal(t, http.StatusCreated, legacyStatus)
	assert.Equal(t, []byte(requestBody), legacyResponse.Body)

	canonicalBytes, err := miniRedis.Get(canonicalKey)
	require.NoError(t, err)
	canonicalRecord, err := decodeCurrentStoreRecord([]byte(canonicalBytes))
	require.NoError(t, err)
	assert.Equal(t, keyStateComplete, canonicalRecord.State)

	currentApp := newEchoApp(New(client).Check(), &handlerCalls,
		tenantIdentityMiddleware(bridgeDashlessTenant, bridgeDashedTenant))
	second := doSend(t, currentApp, http.MethodPost, "/test", requestBody, requestKey)
	assert.Equal(t, http.StatusCreated, second.StatusCode)
	assert.Equal(t, requestBody, readBody(t, second))
	assert.Equal(t, "true", second.Header.Get(chttp.IdempotencyReplayed))
	assert.Equal(t, int32(1), handlerCalls.Load())
}

func TestCheck_RedisLegacyBridge_RaceWithBridgeDisabledCurrentExecutesOneHandler(t *testing.T) {
	t.Parallel()

	miniRedis := miniredis.RunT(t)
	client := newRedisClient(t, miniRedis)
	var handlerCalls atomic.Int32
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	handler := func(c fiber.Ctx) error {
		handlerCalls.Add(1)
		started <- struct{}{}
		<-release

		return c.Status(http.StatusCreated).SendString("created")
	}
	newApp := func(middleware *Middleware) *fiber.App {
		app := fiber.New()
		app.Use(tenantIdentityMiddleware(bridgeDashlessTenant, bridgeDashedTenant))
		app.Use(middleware.Check())
		app.Post("/test", handler)

		return app
	}
	bridgeApp := newApp(New(client, WithRedisLegacyBridge()))
	currentApp := newApp(New(client))
	results := make(chan bridgeRaceResult, 2)
	send := func(app *fiber.App) {
		request := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{"amount":10}`))
		request.Header.Set(chttp.IdempotencyKey, "race-key")
		response, err := app.Test(request, fiber.TestConfig{Timeout: 0})
		if err != nil {
			results <- bridgeRaceResult{err: err}

			return
		}

		results <- bridgeRaceResult{status: response.StatusCode, err: response.Body.Close()}
	}

	go send(bridgeApp)
	go send(currentApp)
	select {
	case <-started:
	case <-time.After(time.Second):
		require.FailNow(t, "neither contender reached the handler")
	}

	// The winner is parked on release, so the loser's conflict response is the
	// only result that can arrive here. Draining it before releasing proves the
	// loser already resolved without a handler execution; no sleep is needed to
	// order the contenders.
	var loserResult bridgeRaceResult
	select {
	case loserResult = <-results:
	case <-time.After(time.Second):
		require.FailNow(t, "the losing contender never resolved while the winner held the claim")
	}

	close(release)
	winnerResult := <-results

	require.NoError(t, loserResult.err)
	require.NoError(t, winnerResult.err)
	assert.Equal(t, int32(1), handlerCalls.Load())
	assert.Equal(t, http.StatusConflict, loserResult.status)
	assert.Equal(t, http.StatusCreated, winnerResult.status)
}

func TestCheck_RedisLegacyBridge_RaceWithV62ClaimExecutesOneHandler(t *testing.T) {
	t.Parallel()

	miniRedis := miniredis.RunT(t)
	client := newRedisClient(t, miniRedis)
	redisClient, err := client.GetClient(context.Background())
	require.NoError(t, err)
	var handlerCalls atomic.Int32
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	handler := func(c fiber.Ctx) error {
		handlerCalls.Add(1)
		started <- struct{}{}
		<-release

		return c.Status(http.StatusCreated).SendString("created")
	}
	bridgeApp := fiber.New()
	bridgeApp.Use(tenantIdentityMiddleware(bridgeDashlessTenant, bridgeDashedTenant))
	bridgeApp.Use(New(client, WithRedisLegacyBridge()).Check())
	bridgeApp.Post("/test", handler)

	v62App := fiber.New()
	v62App.Use(func(c fiber.Ctx) error {
		key := "idempotency:" + bridgeDashedTenant + ":" + c.Get(chttp.IdempotencyKey)
		fingerprint := requestFingerprint(c.Method(), c.Path(), c.Body())
		acquired, acquireErr := redisClient.SetNX(
			c.Context(), key, keyStateProcessing+stateSeparator+fingerprint, time.Hour,
		).Result()
		if acquireErr != nil {
			return acquireErr
		}
		if !acquired {
			return c.SendStatus(http.StatusConflict)
		}

		return c.Next()
	})
	v62App.Post("/test", handler)

	results := make(chan bridgeRaceResult, 2)
	send := func(app *fiber.App) {
		request := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{"amount":10}`))
		request.Header.Set(chttp.IdempotencyKey, "v62-race-key")
		response, requestErr := app.Test(request, fiber.TestConfig{Timeout: 0})
		if requestErr != nil {
			results <- bridgeRaceResult{err: requestErr}

			return
		}

		results <- bridgeRaceResult{status: response.StatusCode, err: response.Body.Close()}
	}

	go send(bridgeApp)
	go send(v62App)
	select {
	case <-started:
	case <-time.After(time.Second):
		require.FailNow(t, "neither contender reached the handler")
	}

	// The winner is parked on release, so the loser's conflict response is the
	// only result that can arrive here. Draining it before releasing proves the
	// loser already resolved without a handler execution; no sleep is needed to
	// order the contenders.
	var loserResult bridgeRaceResult
	select {
	case loserResult = <-results:
	case <-time.After(time.Second):
		require.FailNow(t, "the losing contender never resolved while the winner held the claim")
	}

	close(release)
	winnerResult := <-results

	require.NoError(t, loserResult.err)
	require.NoError(t, winnerResult.err)
	assert.Equal(t, int32(1), handlerCalls.Load())
	assert.Equal(t, http.StatusConflict, loserResult.status)
	assert.Equal(t, http.StatusCreated, winnerResult.status)
}

func TestCheck_RedisLegacyBridge_RejectsUntrustedOriginalTenantBeforeRedis(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		original string
	}{
		{name: "different tenant", original: "650e8400-e29b-41d4-a716-446655440000"},
		{name: "delimiter injection", original: bridgeDashedTenant + ":other"},
		{name: "invalid identity", original: "../tenant"},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			miniRedis := miniredis.RunT(t)
			client := newRedisClient(t, miniRedis)
			var handlerCalled atomic.Bool
			middleware := New(client, WithRedisLegacyBridge())
			app := fiber.New()
			app.Use(tenantIdentityMiddleware(bridgeDashlessTenant, testCase.original))
			app.Use(middleware.Check())
			app.Post("/test", func(c fiber.Ctx) error {
				handlerCalled.Store(true)

				return c.SendStatus(http.StatusCreated)
			})

			response := doPost(t, app, "tenant-key")
			body := readBody(t, response)
			assert.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
			assert.Contains(t, body, "IDEMPOTENCY_UNAVAILABLE")
			assert.False(t, handlerCalled.Load())
			assert.Empty(t, miniRedis.Keys())
		})
	}
}

func TestCheck_RedisLegacyBridge_RejectsNonPlaintextCodecInAnyOptionOrder(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		options []Option
	}{
		{name: "bridge then codec", options: []Option{WithRedisLegacyBridge(), WithResponseCodec(bridgeNonPlaintextCodec{})}},
		{name: "codec then bridge", options: []Option{WithResponseCodec(bridgeNonPlaintextCodec{}), WithRedisLegacyBridge()}},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			miniRedis := miniredis.RunT(t)
			client := newRedisClient(t, miniRedis)
			var handlerCalled atomic.Bool
			middleware := New(client, testCase.options...)
			response := doPost(t, spyApp(middleware.Check(), bridgeDashlessTenant, &handlerCalled), "codec-key")
			body := readBody(t, response)

			assert.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
			assert.Contains(t, body, "plaintext response codec")
			assert.False(t, handlerCalled.Load())
			assert.Empty(t, miniRedis.Keys())
		})
	}
}

func TestCheck_RedisLegacyBridge_LuaFailuresNeverRunOrReopenHandler(t *testing.T) {
	t.Parallel()

	t.Run("acquire wrong type fails closed", func(t *testing.T) {
		t.Parallel()

		miniRedis := miniredis.RunT(t)
		client := newRedisClient(t, miniRedis)
		canonicalKey := "idempotency:" + bridgeDashlessTenant + ":wrong-type-key"
		_, err := miniRedis.Lpush(canonicalKey, "wrong-type")
		require.NoError(t, err)
		var handlerCalled atomic.Bool
		middleware := New(client, WithRedisLegacyBridge())
		app := fiber.New()
		app.Use(tenantIdentityMiddleware(bridgeDashlessTenant, bridgeDashedTenant))
		app.Use(middleware.Check())
		app.Post("/test", func(c fiber.Ctx) error {
			handlerCalled.Store(true)

			return c.SendStatus(http.StatusCreated)
		})

		response := doPost(t, app, "wrong-type-key")
		readBody(t, response)
		assert.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
		assert.False(t, handlerCalled.Load())
	})

	tests := []struct {
		name       string
		statusCode int
	}{
		{name: "complete failure", statusCode: http.StatusCreated},
		{name: "release failure", statusCode: http.StatusInternalServerError},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			miniRedis := miniredis.RunT(t)
			client := newRedisClient(t, miniRedis)
			legacyKey := "idempotency:" + bridgeDashedTenant + ":transition-key"
			canonicalKey := "idempotency:" + bridgeDashlessTenant + ":transition-key"
			middleware := New(client, WithRedisLegacyBridge())
			app := fiber.New()
			app.Use(tenantIdentityMiddleware(bridgeDashlessTenant, bridgeDashedTenant))
			app.Use(middleware.Check())
			// The handler runs on a goroutine app.Test owns, where require's
			// FailNow cannot stop the test; the seed error is asserted on the
			// test goroutine after the response arrives.
			seedErr := make(chan error, 1)
			app.Post("/test", func(c fiber.Ctx) error {
				miniRedis.Del(bridgeOwnerKey(legacyKey))
				_, err := miniRedis.Lpush(bridgeOwnerKey(legacyKey), "wrong-type")
				seedErr <- err

				return c.Status(testCase.statusCode).SendString("handled")
			})

			response := doPost(t, app, "transition-key")
			body := readBody(t, response)
			select {
			case err := <-seedErr:
				require.NoError(t, err)
			case <-time.After(time.Second):
				require.FailNow(t, "the handler never seeded the owner-key corruption")
			}
			assert.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
			assert.Contains(t, body, "IDEMPOTENCY_UNAVAILABLE")
			legacy, err := miniRedis.Get(legacyKey)
			require.NoError(t, err)
			state, _, valid := parseLegacyRecord([]byte(legacy))
			assert.True(t, valid)
			assert.Equal(t, keyStateProcessing, state)
			canonical, err := miniRedis.Get(canonicalKey)
			require.NoError(t, err)
			record, err := decodeCurrentStoreRecord([]byte(canonical))
			require.NoError(t, err)
			assert.Equal(t, keyStateProcessing, record.State)
			assert.NotContains(t, miniRedis.Keys(), legacyResponseKey(legacyKey))
		})
	}
}

func TestCheck_RedisLegacyBridge_DualNamespaceStoredStates(t *testing.T) {
	t.Parallel()

	requestBody := `{"amount":10}`
	fingerprint := requestFingerprint(http.MethodPost, "/test", []byte(requestBody))
	tests := []struct {
		name              string
		legacyState       string
		canonicalState    string
		canonicalResponse []byte
		legacyResponse    string
		wantStatus        int
		wantBody          string
	}{
		{
			name:        "matching processing conflicts",
			legacyState: keyStateProcessing, canonicalState: keyStateProcessing,
			wantStatus: http.StatusConflict, wantBody: "IDEMPOTENCY_CONFLICT",
		},
		{
			name:        "matching complete replays",
			legacyState: keyStateComplete, canonicalState: keyStateComplete,
			canonicalResponse: []byte(legacyReplayResponse), legacyResponse: legacyReplayResponse,
			wantStatus: http.StatusAccepted, wantBody: "legacy-body",
		},
		{
			name:        "state disagreement fails closed",
			legacyState: keyStateProcessing, canonicalState: keyStateComplete,
			canonicalResponse: []byte(legacyReplayResponse), legacyResponse: legacyReplayResponse,
			wantStatus: http.StatusServiceUnavailable, wantBody: "IDEMPOTENCY_UNAVAILABLE",
		},
		{
			name:        "response disagreement fails closed",
			legacyState: keyStateComplete, canonicalState: keyStateComplete,
			canonicalResponse: []byte(legacyReplayResponse), legacyResponse: `{"status_code":202,"body":"other"}`,
			wantStatus: http.StatusServiceUnavailable, wantBody: "IDEMPOTENCY_UNAVAILABLE",
		},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			miniRedis := miniredis.RunT(t)
			client := newRedisClient(t, miniRedis)
			legacyKey := "idempotency:" + bridgeDashedTenant + ":stored-state"
			canonicalKey := "idempotency:" + bridgeDashlessTenant + ":stored-state"
			require.NoError(t, miniRedis.Set(legacyKey,
				testCase.legacyState+stateSeparator+fingerprint))
			canonicalRecord := storeRecord{
				State: testCase.canonicalState, Fingerprint: fingerprint, Owner: "owner-a",
				Response: testCase.canonicalResponse,
			}
			canonicalJSON, err := json.Marshal(canonicalRecord)
			require.NoError(t, err)
			require.NoError(t, miniRedis.Set(canonicalKey, string(canonicalJSON)))
			if testCase.legacyResponse != "" {
				require.NoError(t, miniRedis.Set(legacyResponseKey(legacyKey), testCase.legacyResponse))
			}

			var handlerCalls atomic.Int32
			middleware := New(client, WithRedisLegacyBridge())
			app := newEchoApp(middleware.Check(), &handlerCalls,
				tenantIdentityMiddleware(bridgeDashlessTenant, bridgeDashedTenant))
			response := doSend(t, app, http.MethodPost, "/test", requestBody, "stored-state")
			body := readBody(t, response)

			assert.Equal(t, testCase.wantStatus, response.StatusCode)
			assert.Contains(t, body, testCase.wantBody)
			assert.Zero(t, handlerCalls.Load())
		})
	}
}
