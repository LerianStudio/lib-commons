//go:build unit

// Copyright (c) 2026 Lerian Studio. All rights reserved.
// Use of this source code is governed by the Elastic License 2.0
// that can be found in the LICENSE file.

package middleware

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/LerianStudio/lib-commons/v7/commons"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/client"
	"github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/core"
	tmpostgres "github.com/LerianStudio/lib-commons/v7/commons/tenant-manager/postgres"
	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refusalCase is one way WithTenantDB refuses a request, with the body it writes.
type refusalCase struct {
	name    string
	token   func(t *testing.T) string // empty: no Authorization header
	pg      func(t *testing.T) *tmpostgres.Manager
	status  int
	code    string
	title   string
	message string
	cause   func(err error) bool // nil: the cause has no sentinel to match
}

func refusalCases() []refusalCase {
	tenantToken := func(t *testing.T) string { return makeTenantJWT(t, "tenant-refused") }
	unusedPG := func(t *testing.T) *tmpostgres.Manager { pg, _ := newTestManagers(t); return pg }
	is := func(target error) func(error) bool { return func(err error) bool { return errors.Is(err, target) } }

	return []refusalCase{
		{
			name: "missing token", token: func(*testing.T) string { return "" }, pg: unusedPG,
			status: http.StatusUnauthorized, code: "MISSING_TOKEN", title: "Unauthorized",
			message: "Authorization token is required", cause: is(core.ErrAuthorizationTokenRequired),
		},
		{
			name: "unparseable token", token: func(*testing.T) string { return "not-a-jwt" }, pg: unusedPG,
			status: http.StatusUnauthorized, code: "INVALID_TOKEN", title: "Unauthorized",
			message: "Failed to parse authorization token", cause: is(core.ErrInvalidAuthorizationToken),
		},
		{
			name: "no tenantId claim", token: makeNoTenantJWT, pg: unusedPG,
			status: http.StatusUnauthorized, code: "MISSING_TENANT", title: "Unauthorized",
			message: "tenantId is required in JWT token", cause: is(core.ErrMissingTenantIDClaim),
		},
		{
			name:  "malformed tenantId claim",
			token: func(t *testing.T) string { return makeTenantJWT(t, "tenant/../../etc") }, pg: unusedPG,
			status: http.StatusUnauthorized, code: "INVALID_TENANT", title: "Unauthorized",
			message: "tenantId has invalid format", cause: is(core.ErrInvalidTenantIDFormat),
		},
		{
			name: "tenant not found", token: tenantToken, pg: tenantManagerAnswering(http.StatusNotFound, ""),
			status: http.StatusNotFound, code: "TENANT_NOT_FOUND", title: "Tenant Not Found",
			message: "tenant not found: tenant-refused", cause: is(core.ErrTenantNotFound),
		},
		{
			name: "tenant suspended", token: tenantToken,
			pg:     tenantManagerAnswering(http.StatusForbidden, `{"status":"suspended"}`),
			status: http.StatusForbidden, code: "0131", title: "Service Suspended",
			message: "tenant service is suspended",
			cause: func(err error) bool {
				var suspended *core.TenantSuspendedError
				return errors.As(err, &suspended) && suspended.Status == "suspended"
			},
		},
		{
			name: "tenant access denied", token: tenantToken, pg: tenantManagerAnswering(http.StatusForbidden, ""),
			status: http.StatusForbidden, code: "0131", title: "Access Denied",
			message: "tenant service access denied", cause: is(core.ErrTenantServiceAccessDenied),
		},
		{
			name: "tenant manager closed", token: tenantToken, pg: closedTenantManager,
			status: http.StatusServiceUnavailable, code: "SERVICE_UNAVAILABLE", title: "Service Unavailable",
			message: "Service temporarily unavailable", cause: is(core.ErrManagerClosed),
		},
		{
			name: "tenant manager error", token: tenantToken, pg: tenantManagerAnswering(http.StatusInternalServerError, ""),
			status: http.StatusInternalServerError, code: "TENANT_DB_ERROR", title: "Failed to resolve tenant database",
			message: "Internal server error",
		},
	}
}

func tenantManagerAnswering(status int, body string) func(t *testing.T) *tmpostgres.Manager {
	return func(t *testing.T) *tmpostgres.Manager {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
		}))
		t.Cleanup(server.Close)

		pg, _ := newTestManagersWithServer(t, server.URL)

		return pg
	}
}

func closedTenantManager(t *testing.T) *tmpostgres.Manager {
	c, err := client.NewClient("http://localhost:8080", nil, client.WithAllowInsecureHTTP(), client.WithServiceAPIKey("test-key"))
	require.NoError(t, err)

	pg := tmpostgres.NewManager(c, "ledger")
	require.NoError(t, pg.Close(t.Context()))

	return pg
}

// serveRefusal drives one request through WithTenantDB on an app whose
// ErrorHandler records what it receives, and reports whether the chain went on.
func serveRefusal(t *testing.T, tc refusalCase, mw *TenantMiddleware, errorHandler fiber.ErrorHandler) (*http.Response, bool) {
	t.Helper()

	nextRan := false
	app := fiber.New(fiber.Config{ErrorHandler: errorHandler})
	app.Get("/test", mw.WithTenantDB, func(c fiber.Ctx) error {
		nextRan = true

		return c.SendStatus(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	if token := tc.token(t); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := app.Test(req, fiber.TestConfig{Timeout: 0})
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	return resp, nextRan
}

func TestWithTenantDB_WithoutRefusalOption_WritesTheSameResponseAsBefore(t *testing.T) {
	t.Parallel()

	for _, tc := range refusalCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			handlerRan := false
			resp, nextRan := serveRefusal(t, tc, NewTenantMiddleware(WithPG(tc.pg(t))), func(c fiber.Ctx, err error) error {
				handlerRan = true

				return fiber.DefaultErrorHandler(c, err)
			})

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			wantBody := fmt.Sprintf(`{"code":%q,"message":%q,"title":%q}`, tc.code, tc.message, tc.title)
			resp.Header.Del("Date")

			assert.Equal(t, tc.status, resp.StatusCode)
			assert.Equal(t, wantBody, string(body))
			assert.Equal(t, http.Header{
				"Content-Type":   {"application/json; charset=utf-8"},
				"Content-Length": {strconv.Itoa(len(wantBody))},
			}, resp.Header)
			assert.False(t, handlerRan, "the middleware writes its own refusal and returns no error")
			assert.False(t, nextRan, "a refusal stops the chain")
		})
	}
}

func TestWithTenantDB_WithRefusalOption_HandsEveryRefusalToTheErrorHandler(t *testing.T) {
	t.Parallel()

	for _, tc := range refusalCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var (
				handled                  error
				writtenStatus            int
				writtenBody, writtenType string
			)

			mw := NewTenantMiddleware(WithPG(tc.pg(t)), WithRefusalsToErrorHandler())
			resp, nextRan := serveRefusal(t, tc, mw, func(c fiber.Ctx, err error) error {
				handled = err
				writtenStatus = c.Response().StatusCode()
				writtenBody = string(c.Response().Body())
				writtenType = c.GetRespHeader(fiber.HeaderContentType)

				return c.Status(http.StatusTeapot).SendString("rendered by the app")
			})

			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)

			assert.Equal(t, http.StatusTeapot, resp.StatusCode, "the app's ErrorHandler owns the response")
			assert.Equal(t, "rendered by the app", string(body))
			assert.False(t, nextRan, "a refusal stops the chain")

			require.Error(t, handled, "the refusal reaches the ErrorHandler")
			assert.Equal(t, http.StatusOK, writtenStatus, "the middleware set no status")
			assert.Empty(t, writtenBody, "the middleware wrote no body")
			assert.NotContains(t, writtenType, fiber.MIMEApplicationJSON, "the middleware set no JSON content type")

			var fiberErr *fiber.Error
			require.ErrorAs(t, handled, &fiberErr)
			assert.Equal(t, tc.status, fiberErr.Code)
			assert.Equal(t, tc.message, fiberErr.Message)

			var response commons.Response
			require.ErrorAs(t, handled, &response)
			assert.Equal(t, commons.Response{Code: tc.code, Title: tc.title, Message: tc.message}, response)

			if tc.cause != nil {
				assert.True(t, tc.cause(handled), "the domain error stays reachable: %v", handled)
			}
		})
	}
}
