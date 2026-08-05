//go:build unit

package idempotency

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	chttp "github.com/LerianStudio/lib-commons/v6/commons/constants"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

type xorResponseCodec struct {
	key byte
}

func (codec xorResponseCodec) Encode(_ context.Context, plaintext []byte) ([]byte, error) {
	return codec.transform(plaintext), nil
}

func (codec xorResponseCodec) Decode(_ context.Context, encoded []byte) ([]byte, error) {
	return codec.transform(encoded), nil
}

func (codec xorResponseCodec) transform(input []byte) []byte {
	output := make([]byte, len(input))
	for index, value := range input {
		output[index] = value ^ codec.key
	}

	return output
}

func TestCheck_ResponseCodec_ProtectsStoredBodyAndReplaysExactly(t *testing.T) {
	t.Parallel()

	controller := gomock.NewController(t)
	store := NewMockStore(controller)
	codec := xorResponseCodec{key: 0x5a}
	const sensitiveBody = `{"account":"sensitive-value"}`

	var completed []byte
	store.EXPECT().Acquire(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, true, nil)
	store.EXPECT().Complete(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, _, value []byte, _ time.Duration) (bool, error) {
			completed = append([]byte(nil), value...)
			assert.NotContains(t, string(completed), "sensitive-value")

			return true, nil
		})
	store.EXPECT().Acquire(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, _ []byte, _ time.Duration) ([]byte, bool, error) {
			return completed, false, nil
		})

	middleware := NewWithStore(store, WithResponseCodec(codec))
	app := fiber.New()
	app.Use(tenantMiddleware("tenant-codec"))
	app.Use(middleware.Check())
	app.Post("/test", func(c fiber.Ctx) error {
		c.Set("Location", "/payments/42")

		return c.Status(http.StatusCreated).Type("json").SendString(sensitiveBody)
	})

	first := doPost(t, app, "codec-key")
	assert.Equal(t, http.StatusCreated, first.StatusCode)
	assert.Equal(t, sensitiveBody, readBody(t, first))

	replay := doPost(t, app, "codec-key")
	assert.Equal(t, http.StatusCreated, replay.StatusCode)
	assert.Equal(t, sensitiveBody, readBody(t, replay))
	assert.Equal(t, "/payments/42", replay.Header.Get("Location"))
	assert.Equal(t, "true", replay.Header.Get(chttp.IdempotencyReplayed))
}

func TestCheck_TTLProvider_ResolvesPerRequest(t *testing.T) {
	t.Parallel()

	controller := gomock.NewController(t)
	store := NewMockStore(controller)
	const ttlHeader = "X-Test-TTL"

	store.EXPECT().Acquire(gomock.Any(), gomock.Any(), gomock.Any(), 90*time.Second).
		Return(nil, true, nil)
	store.EXPECT().Complete(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), 90*time.Second).
		Return(true, nil)

	middleware := NewWithStore(store, WithTTLProvider(func(c fiber.Ctx) (time.Duration, error) {
		if c.Get(ttlHeader) == "short" {
			return 90 * time.Second, nil
		}

		return 2 * time.Hour, nil
	}))
	app := newPostApp(middleware.Check(), tenantMiddleware("tenant-ttl"))
	req := httptest.NewRequest(http.MethodPost, "/test", nil)
	req.Header.Set(chttp.IdempotencyKey, "ttl-key")
	req.Header.Set(ttlHeader, "short")

	response, err := app.Test(req, fiber.TestConfig{Timeout: 0})
	require.NoError(t, err)
	defer response.Body.Close()

	assert.Equal(t, http.StatusCreated, response.StatusCode)
}

func TestCheck_ClientErrorPolicyRelease_CleansOwnedRecord(t *testing.T) {
	t.Parallel()

	controller := gomock.NewController(t)
	store := NewMockStore(controller)

	var acquired []byte
	store.EXPECT().Acquire(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, candidate []byte, _ time.Duration) ([]byte, bool, error) {
			acquired = append([]byte(nil), candidate...)

			return nil, true, nil
		})
	store.EXPECT().Release(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, expected []byte) (bool, error) {
			assert.Equal(t, acquired, expected)

			return true, nil
		})

	middleware := NewWithStore(store, WithClientErrorPolicy(ClientErrorPolicyRelease))
	app := fiber.New()
	app.Use(tenantMiddleware("tenant-client-error"))
	app.Use(middleware.Check())
	app.Post("/test", func(c fiber.Ctx) error {
		return c.Status(http.StatusUnprocessableEntity).JSON(fiber.Map{"code": "INVALID_PAYMENT"})
	})

	response := doPost(t, app, "client-error-key")
	defer response.Body.Close()

	assert.Equal(t, http.StatusUnprocessableEntity, response.StatusCode)
}

func TestCheck_OversizedResponse_FailsClosedWithoutCompletionMarker(t *testing.T) {
	t.Parallel()

	controller := gomock.NewController(t)
	store := NewMockStore(controller)
	store.EXPECT().Acquire(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		Return(nil, true, nil)

	middleware := NewWithStore(store, WithMaxBodyCache(8))
	app := fiber.New()
	app.Use(tenantMiddleware("tenant-size"))
	app.Use(middleware.Check())
	app.Post("/test", func(c fiber.Ctx) error {
		return c.Status(http.StatusCreated).Send(bytes.Repeat([]byte("x"), 9))
	})

	response := doPost(t, app, "oversized-key")
	body := readBody(t, response)

	assert.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
	assert.Contains(t, body, "IDEMPOTENCY_UNAVAILABLE")
}

func TestCheck_RetryAfter_IsExclusiveToInFlightConflict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		record         storeRecord
		wantStatus     int
		wantRetryAfter string
	}{
		{
			name: "in-flight conflict advertises retry",
			record: storeRecord{
				State: keyStateProcessing, Fingerprint: requestFingerprint(http.MethodPost, "/test", nil), Owner: "owner-a",
			},
			wantStatus: http.StatusConflict, wantRetryAfter: "1",
		},
		{
			name: "key reuse never advertises retry",
			record: storeRecord{
				State: keyStateComplete, Fingerprint: "different", Owner: "owner-a", Response: []byte("encoded"),
			},
			wantStatus: http.StatusUnprocessableEntity,
		},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			stored, err := json.Marshal(testCase.record)
			require.NoError(t, err)

			controller := gomock.NewController(t)
			store := NewMockStore(controller)
			store.EXPECT().Acquire(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
				Return(stored, false, nil)

			middleware := NewWithStore(store)
			response := doPost(t, newPostApp(middleware.Check(), tenantMiddleware("tenant-retry")), "retry-key")
			defer response.Body.Close()

			assert.Equal(t, testCase.wantStatus, response.StatusCode)
			assert.Equal(t, testCase.wantRetryAfter, response.Header.Get("Retry-After"))
		})
	}
}
