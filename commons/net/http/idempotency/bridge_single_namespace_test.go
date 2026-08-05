//go:build unit

package idempotency

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	chttp "github.com/LerianStudio/lib-commons/v6/commons/constants"
	"github.com/alicebob/miniredis/v2"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheck_RedisLegacyBridge_SingleNamespaceReadsCurrentRecords(t *testing.T) {
	t.Parallel()

	identities := []struct {
		name     string
		tenantID string
	}{
		{name: "slug", tenantID: "tenant-bridge"},
		{name: "canonical UUID", tenantID: bridgeDashlessTenant},
	}

	for _, identity := range identities {
		identity := identity
		t.Run(identity.name, func(t *testing.T) {
			t.Parallel()

			t.Run("processing current record returns conflict", func(t *testing.T) {
				t.Parallel()

				miniRedis := miniredis.RunT(t)
				client := newRedisClient(t, miniRedis)
				started := make(chan struct{})
				release := make(chan struct{})
				currentApp := fiber.New()
				currentApp.Use(tenantIdentityMiddleware(identity.tenantID, identity.tenantID))
				currentApp.Use(New(client).Check())
				currentApp.Post("/test", func(c fiber.Ctx) error {
					close(started)
					<-release

					return c.Status(http.StatusAccepted).SendString("current-body")
				})

				currentResult := make(chan bridgeRaceResult, 1)
				go func() {
					request := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(`{"amount":10}`))
					request.Header.Set(chttp.IdempotencyKey, "processing-key")
					response, err := currentApp.Test(request, fiber.TestConfig{Timeout: 0})
					if err != nil {
						currentResult <- bridgeRaceResult{err: err}

						return
					}

					currentResult <- bridgeRaceResult{status: response.StatusCode, err: response.Body.Close()}
				}()
				<-started

				var bridgeHandlerCalled atomic.Bool
				bridge := New(client, WithRedisLegacyBridge())
				bridgeApp := spyApp(bridge.Check(), identity.tenantID, &bridgeHandlerCalled)
				response := doSend(t, bridgeApp, http.MethodPost, "/test", `{"amount":10}`, "processing-key")
				body := readBody(t, response)
				assert.Equal(t, http.StatusConflict, response.StatusCode)
				assert.Contains(t, body, "IDEMPOTENCY_CONFLICT")
				assert.False(t, bridgeHandlerCalled.Load())

				close(release)
				result := <-currentResult
				require.NoError(t, result.err)
				assert.Equal(t, http.StatusAccepted, result.status)
			})

			t.Run("complete current record replays exactly", func(t *testing.T) {
				t.Parallel()

				miniRedis := miniredis.RunT(t)
				client := newRedisClient(t, miniRedis)
				var currentCalls atomic.Int32
				currentApp := fiber.New()
				currentApp.Use(tenantIdentityMiddleware(identity.tenantID, identity.tenantID))
				currentApp.Use(New(client).Check())
				currentApp.Post("/test", func(c fiber.Ctx) error {
					currentCalls.Add(1)
					c.Append(fiber.HeaderSetCookie, "a=1")
					c.Set("X-Current", "preserved")
					c.Set(fiber.HeaderContentType, "application/octet-stream")

					return c.Status(http.StatusAccepted).Send([]byte{0xff, 0x00, 0x7f})
				})
				first := doSend(t, currentApp, http.MethodPost, "/test", `{"amount":10}`, "complete-key")
				assert.Equal(t, http.StatusAccepted, first.StatusCode)
				assert.Equal(t, string([]byte{0xff, 0x00, 0x7f}), readBody(t, first))

				var bridgeHandlerCalled atomic.Bool
				bridgeApp := spyApp(New(client, WithRedisLegacyBridge()).Check(),
					identity.tenantID, &bridgeHandlerCalled)
				second := doSend(t, bridgeApp, http.MethodPost, "/test", `{"amount":10}`, "complete-key")
				body := readBody(t, second)

				assert.Equal(t, http.StatusAccepted, second.StatusCode)
				assert.Equal(t, string([]byte{0xff, 0x00, 0x7f}), body)
				assert.Equal(t, "application/octet-stream", second.Header.Get(fiber.HeaderContentType))
				assert.Equal(t, "preserved", second.Header.Get("X-Current"))
				assert.Equal(t, []string{"a=1"}, second.Header.Values(fiber.HeaderSetCookie))
				assert.Equal(t, "true", second.Header.Get(chttp.IdempotencyReplayed))
				assert.Equal(t, int32(1), currentCalls.Load())
				assert.False(t, bridgeHandlerCalled.Load())
			})

			t.Run("different fingerprint returns unprocessable content", func(t *testing.T) {
				t.Parallel()

				miniRedis := miniredis.RunT(t)
				client := newRedisClient(t, miniRedis)
				var currentCalls atomic.Int32
				currentApp := newEchoApp(New(client).Check(), &currentCalls,
					tenantIdentityMiddleware(identity.tenantID, identity.tenantID))
				first := doSend(t, currentApp, http.MethodPost, "/test", `{"amount":10}`, "reuse-key")
				readBody(t, first)

				var bridgeHandlerCalled atomic.Bool
				bridgeApp := spyApp(New(client, WithRedisLegacyBridge()).Check(),
					identity.tenantID, &bridgeHandlerCalled)
				second := doSend(t, bridgeApp, http.MethodPost, "/test", `{"amount":11}`, "reuse-key")
				body := readBody(t, second)

				assert.Equal(t, http.StatusUnprocessableEntity, second.StatusCode)
				assert.Contains(t, body, "IDEMPOTENCY_KEY_REUSE")
				assert.Equal(t, int32(1), currentCalls.Load())
				assert.False(t, bridgeHandlerCalled.Load())
			})

			t.Run("malformed current JSON fails closed", func(t *testing.T) {
				t.Parallel()

				miniRedis := miniredis.RunT(t)
				client := newRedisClient(t, miniRedis)
				key := "idempotency:" + identity.tenantID + ":malformed-key"
				require.NoError(t, miniRedis.Set(key, `{"state":`))
				var bridgeHandlerCalled atomic.Bool
				bridgeApp := spyApp(New(client, WithRedisLegacyBridge()).Check(),
					identity.tenantID, &bridgeHandlerCalled)
				response := doPost(t, bridgeApp, "malformed-key")
				body := readBody(t, response)

				assert.Equal(t, http.StatusServiceUnavailable, response.StatusCode)
				assert.Contains(t, body, "IDEMPOTENCY_UNAVAILABLE")
				assert.False(t, bridgeHandlerCalled.Load())
			})
		})
	}
}
