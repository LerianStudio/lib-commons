//go:build unit

package idempotency

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	chttp "github.com/LerianStudio/lib-commons/v6/commons/constants"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestNewWithStore_MissingStore_FailsClosed(t *testing.T) {
	t.Parallel()

	var typedNil *MockStore
	tests := []struct {
		name       string
		store      Store
		opts       []Option
		wantStatus int
	}{
		{name: "nil interface", store: nil, wantStatus: http.StatusServiceUnavailable},
		{name: "typed nil", store: typedNil, wantStatus: http.StatusServiceUnavailable},
		{
			name:       "fail-open option cannot weaken injected store",
			store:      nil,
			opts:       []Option{WithFailClosed(false)},
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:  "custom unavailable handler",
			store: nil,
			opts: []Option{WithUnavailableHandler(func(c fiber.Ctx) error {
				return c.SendStatus(http.StatusTeapot)
			})},
			wantStatus: http.StatusTeapot,
		},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			var called atomic.Bool
			middleware := NewWithStore(testCase.store, testCase.opts...)
			response := doPost(t, spyApp(middleware.Check(), "tenant-store", &called), "missing-store")
			defer response.Body.Close()

			assert.Equal(t, testCase.wantStatus, response.StatusCode)
			assert.False(t, called.Load(), "handler must not run without an injected store")
		})
	}
}

func TestCheck_InjectedStore_AcquireErrorFailsClosed(t *testing.T) {
	t.Parallel()

	controller := gomock.NewController(t)
	store := NewMockStore(controller)
	store.EXPECT().Acquire(gomock.Any(), "idempotency:tenant-store:errored-store", gomock.Any(), gomock.Any()).
		Return(nil, false, errors.New("backend unavailable"))

	var called atomic.Bool
	middleware := NewWithStore(store)
	response := doPost(t, spyApp(middleware.Check(), "tenant-store", &called), "errored-store")
	defer response.Body.Close()

	assert.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
	assert.False(t, called.Load(), "handler must not run when the injected store errors")
}

func TestCheck_InjectedStore_StoredStates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		stored         []byte
		wantStatus     int
		wantBody       string
		wantReplayed   string
		wantHandlerRun bool
		wantHeaders    bool
	}{
		{
			name: "in progress returns conflict",
			stored: encodeStoreRecord(t, storeRecord{
				State: keyStateProcessing, Fingerprint: requestFingerprint(http.MethodPost, "/test", nil), Owner: "owner-a",
			}),
			wantStatus: http.StatusConflict, wantBody: "IDEMPOTENCY_CONFLICT", wantReplayed: "true",
		},
		{
			name: "key reuse returns unprocessable content",
			stored: encodeStoreRecord(t, storeRecord{
				State: keyStateComplete, Fingerprint: "different-fingerprint", Owner: "owner-a",
			}),
			wantStatus: http.StatusUnprocessableEntity, wantBody: "IDEMPOTENCY_KEY_REUSE",
		},
		{
			name: "completed response replays status body and headers",
			stored: encodeStoreRecord(t, storeRecord{
				State: keyStateComplete, Fingerprint: requestFingerprint(http.MethodPost, "/test", nil), Owner: "owner-a",
				Response: encodeCachedResponse(t, cachedResponse{
					StatusCode:  http.StatusAccepted,
					ContentType: "application/octet-stream",
					Body:        []byte{0xff, 0x00, 0x7f},
					Headers: map[string][]string{
						"Location":   {"/jobs/123"},
						"Set-Cookie": {"a=1", "b=2"},
					},
				}),
			}),
			wantStatus: http.StatusAccepted, wantBody: string([]byte{0xff, 0x00, 0x7f}),
			wantReplayed: "true", wantHeaders: true,
		},
		{
			name: "completed without cached response fails closed",
			stored: encodeStoreRecord(t, storeRecord{
				State: keyStateComplete, Fingerprint: requestFingerprint(http.MethodPost, "/test", nil), Owner: "owner-a",
			}),
			wantStatus: http.StatusServiceUnavailable, wantBody: "IDEMPOTENCY_UNAVAILABLE",
		},
		{
			name:       "invalid backend record fails closed",
			stored:     []byte("invalid"),
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "IDEMPOTENCY_UNAVAILABLE",
		},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			controller := gomock.NewController(t)
			store := NewMockStore(controller)
			store.EXPECT().Acquire(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
				Return(testCase.stored, false, nil)

			var called atomic.Bool
			middleware := NewWithStore(store)
			response := doPost(t, spyApp(middleware.Check(), "tenant-store", &called), "state-key")
			body := readBody(t, response)

			assert.Equal(t, testCase.wantStatus, response.StatusCode)
			assert.Equal(t, testCase.wantReplayed, response.Header.Get(chttp.IdempotencyReplayed))
			assert.Equal(t, testCase.wantHandlerRun, called.Load())
			assert.Contains(t, body, testCase.wantBody)
			if testCase.wantHeaders {
				assert.Equal(t, "/jobs/123", response.Header.Get("Location"))
				assert.ElementsMatch(t, []string{"a=1", "b=2"}, response.Header.Values("Set-Cookie"))
				assert.Equal(t, "application/octet-stream", response.Header.Get("Content-Type"))
			}
		})
	}
}

func TestCheck_InjectedStore_AcquiredCompletesWithCapturedResponse(t *testing.T) {
	t.Parallel()

	controller := gomock.NewController(t)
	store := NewMockStore(controller)

	var acquired []byte
	store.EXPECT().Acquire(gomock.Any(), "idempotency:tenant-store:complete-key", gomock.Any(), 2*time.Hour).
		DoAndReturn(func(_ context.Context, _ string, processing []byte, _ time.Duration) ([]byte, bool, error) {
			acquired = append([]byte(nil), processing...)

			return nil, true, nil
		})
	store.EXPECT().Complete(gomock.Any(), "idempotency:tenant-store:complete-key", gomock.Any(), gomock.Any(), 2*time.Hour).
		DoAndReturn(func(_ context.Context, _ string, expected, completed []byte, _ time.Duration) (bool, error) {
			assert.Equal(t, acquired, expected)

			var record storeRecord
			require.NoError(t, json.Unmarshal(completed, &record))
			require.NotEmpty(t, record.Response)

			var response cachedResponse
			require.NoError(t, json.Unmarshal(record.Response, &response))
			assert.Equal(t, http.StatusCreated, response.StatusCode)
			assert.Equal(t, "application/json; charset=utf-8", response.ContentType)
			assert.JSONEq(t, `{"status":"created"}`, string(response.Body))

			return true, nil
		})

	middleware := NewWithStore(store, WithKeyTTL(2*time.Hour))
	response := doPost(t, newPostApp(middleware.Check(), tenantMiddleware("tenant-store")), "complete-key")
	defer response.Body.Close()

	assert.Equal(t, http.StatusCreated, response.StatusCode)
}

func TestCheck_InjectedStore_HandlerFailureReleasesOwnedAcquisition(t *testing.T) {
	t.Parallel()

	controller := gomock.NewController(t)
	store := NewMockStore(controller)

	var acquired []byte
	store.EXPECT().Acquire(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, processing []byte, _ time.Duration) ([]byte, bool, error) {
			acquired = append([]byte(nil), processing...)

			return nil, true, nil
		})
	store.EXPECT().Release(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ string, expected []byte) (bool, error) {
			assert.Equal(t, acquired, expected)

			return true, nil
		})

	middleware := NewWithStore(store)
	app := fiber.New()
	app.Use(tenantMiddleware("tenant-store"))
	app.Use(middleware.Check())
	app.Post("/test", func(c fiber.Ctx) error {
		return c.Status(http.StatusServiceUnavailable).JSON(fiber.Map{"error": "retry"})
	})

	response := doPost(t, app, "release-key")
	defer response.Body.Close()

	assert.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
}

func TestCheck_InjectedStore_TransitionFailuresPreserveHandlerResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
		applied    bool
		storeErr   error
	}{
		{name: "completion backend error", statusCode: http.StatusCreated, storeErr: errors.New("completion failed")},
		{name: "completion stale value", statusCode: http.StatusCreated},
		{name: "release backend error", statusCode: http.StatusServiceUnavailable, storeErr: errors.New("release failed")},
		{name: "release stale value", statusCode: http.StatusServiceUnavailable},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			controller := gomock.NewController(t)
			store := NewMockStore(controller)
			store.EXPECT().Acquire(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
				Return(nil, true, nil)
			if testCase.statusCode < http.StatusInternalServerError {
				store.EXPECT().Complete(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
					Return(testCase.applied, testCase.storeErr)
			} else {
				store.EXPECT().Release(gomock.Any(), gomock.Any(), gomock.Any()).
					Return(testCase.applied, testCase.storeErr)
			}

			middleware := NewWithStore(store)
			app := fiber.New()
			app.Use(tenantMiddleware("tenant-store"))
			app.Use(middleware.Check())
			app.Post("/test", func(c fiber.Ctx) error {
				return c.Status(testCase.statusCode).JSON(fiber.Map{"status": testCase.statusCode})
			})

			response := doPost(t, app, "transition-failure")
			defer response.Body.Close()

			wantStatus := testCase.statusCode
			if testCase.statusCode < http.StatusInternalServerError {
				wantStatus = http.StatusServiceUnavailable
			}

			assert.Equal(t, wantStatus, response.StatusCode)
		})
	}
}

func encodeStoreRecord(t *testing.T, record storeRecord) []byte {
	t.Helper()

	data, err := json.Marshal(record)
	require.NoError(t, err)

	return data
}

func encodeCachedResponse(t *testing.T, response cachedResponse) []byte {
	t.Helper()

	data, err := json.Marshal(response)
	require.NoError(t, err)

	return data
}
