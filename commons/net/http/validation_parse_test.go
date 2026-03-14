//go:build unit

package http

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseBodyAndValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		body        string
		contentType string
		payload     any
		wantErr     bool
		errContains string
		errIs       error
	}{
		{
			name:        "valid JSON payload",
			body:        `{"name":"test","email":"test@example.com","priority":1}`,
			contentType: "application/json",
			payload:     &testPayload{},
			wantErr:     false,
		},
		{
			name:        "invalid JSON",
			body:        `{"name": invalid}`,
			contentType: "application/json",
			payload:     &testPayload{},
			wantErr:     true,
			errContains: "failed to parse request body",
			errIs:       ErrBodyParseFailed,
		},
		{
			name:        "valid JSON but validation fails",
			body:        `{"name":"","email":"test@example.com","priority":1}`,
			contentType: "application/json",
			payload:     &testPayload{},
			wantErr:     true,
			errContains: "field is required: 'name'",
			errIs:       ErrFieldRequired,
		},
		{
			name:        "empty body",
			body:        "",
			contentType: "application/json",
			payload:     &testPayload{},
			wantErr:     true,
			errContains: "failed to parse request body",
			errIs:       ErrBodyParseFailed,
		},
		{
			name:        "application/json with charset is accepted",
			body:        `{"name":"test","email":"test@example.com","priority":1}`,
			contentType: "application/json; charset=utf-8",
			payload:     &testPayload{},
			wantErr:     false,
		},
		{
			name:        "empty Content-Type falls through to body parser",
			body:        `{"name":"test","email":"test@example.com","priority":1}`,
			contentType: "",
			payload:     &testPayload{},
			wantErr:     true,
			errContains: "failed to parse request body",
			errIs:       ErrBodyParseFailed,
		},
		{
			name:        "JSON Content-Type with surrounding whitespace is accepted",
			body:        `{"name":"test","email":"test@example.com","priority":1}`,
			contentType: " application/json ; charset=utf-8 ",
			payload:     &testPayload{},
			wantErr:     false,
		},
		{
			name:        "JSON Content-Type is case-insensitive",
			body:        `{"name":"test","email":"test@example.com","priority":1}`,
			contentType: "Application/JSON",
			payload:     &testPayload{},
			wantErr:     false,
		},
		{
			name:        "text/plain Content-Type is rejected",
			body:        `{"name":"test","email":"test@example.com","priority":1}`,
			contentType: "text/plain",
			payload:     &testPayload{},
			wantErr:     true,
			errContains: "Content-Type must be application/json",
			errIs:       ErrUnsupportedContentType,
		},
		{
			name:        "text/xml Content-Type is rejected",
			body:        `<data/>`,
			contentType: "text/xml",
			payload:     &testPayload{},
			wantErr:     true,
			errContains: "Content-Type must be application/json",
			errIs:       ErrUnsupportedContentType,
		},
		{
			name:        "multipart/form-data Content-Type is rejected",
			body:        `{"name":"test"}`,
			contentType: "multipart/form-data",
			payload:     &testPayload{},
			wantErr:     true,
			errContains: "Content-Type must be application/json",
			errIs:       ErrUnsupportedContentType,
		},
		{
			name:        "application/jsonx is rejected",
			body:        `{"name":"test","email":"test@example.com","priority":1}`,
			contentType: "application/jsonx",
			payload:     &testPayload{},
			wantErr:     true,
			errContains: "Content-Type must be application/json",
			errIs:       ErrUnsupportedContentType,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			app := fiber.New()
			var gotErr error
			app.Post("/test", func(c *fiber.Ctx) error {
				gotErr = ParseBodyAndValidate(c, tc.payload)
				if gotErr != nil {
					return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": gotErr.Error()})
				}

				return c.SendStatus(fiber.StatusOK)
			})

			req := httptest.NewRequest(http.MethodPost, "/test", bytes.NewBufferString(tc.body))
			if tc.contentType != "" {
				req.Header.Set("Content-Type", tc.contentType)
			}

			resp, err := app.Test(req)
			require.NoError(t, err)

			defer func() {
				require.NoError(t, resp.Body.Close())
			}()

			if tc.wantErr {
				require.Error(t, gotErr)
				assert.Contains(t, gotErr.Error(), tc.errContains)
				if tc.errIs != nil {
					assert.ErrorIs(t, gotErr, tc.errIs)
				}
				assert.Equal(t, fiber.StatusBadRequest, resp.StatusCode)
			} else {
				require.NoError(t, gotErr)
				assert.Equal(t, fiber.StatusOK, resp.StatusCode)
			}
		})
	}
}

func TestParseBodyAndValidate_NilContext(t *testing.T) {
	t.Parallel()

	payload := &testPayload{}
	err := ParseBodyAndValidate(nil, payload)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrContextNotFound)
}
