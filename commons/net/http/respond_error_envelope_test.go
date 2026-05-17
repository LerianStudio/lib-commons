//go:build unit

package http

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// RespondErrorEnvelope -- richer error envelope with taxonomy-coded contracts
// ---------------------------------------------------------------------------

func TestRespondErrorEnvelope_FullyPopulated(t *testing.T) {
	t.Parallel()

	env := ErrorEnvelope{
		Code:      "BTF-0429",
		Service:   "plugin-br-bank-transfer",
		Category:  "rate_limit",
		Message:   "Rate limit exceeded",
		RequestID: "6d3e2a68-1f2b-4c3d-9e4f-5a6b7c8d9e0f",
		Fields: map[string]any{
			"limit":         100,
			"tenantHash":    "8b6f3e2a91c4d7e0",
			"windowSeconds": 60,
		},
	}

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return RespondErrorEnvelope(c, fiber.StatusTooManyRequests, env)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	assert.Equal(t, fiber.StatusTooManyRequests, resp.StatusCode)
	assert.Contains(t, resp.Header.Get("Content-Type"), "application/json")

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var decoded ErrorPayload
	require.NoError(t, json.Unmarshal(body, &decoded))

	assert.Equal(t, "BTF-0429", decoded.Error.Code)
	assert.Equal(t, "plugin-br-bank-transfer", decoded.Error.Service)
	assert.Equal(t, "rate_limit", decoded.Error.Category)
	assert.Equal(t, "Rate limit exceeded", decoded.Error.Message)
	assert.Equal(t, "6d3e2a68-1f2b-4c3d-9e4f-5a6b7c8d9e0f", decoded.Error.RequestID)
	require.NotNil(t, decoded.Error.Fields)
	assert.InDelta(t, float64(100), decoded.Error.Fields["limit"], 0)
	assert.Equal(t, "8b6f3e2a91c4d7e0", decoded.Error.Fields["tenantHash"])
	assert.InDelta(t, float64(60), decoded.Error.Fields["windowSeconds"], 0)
}

func TestRespondErrorEnvelope_OmitemptyDropsAbsentFields(t *testing.T) {
	t.Parallel()

	// RequestID empty + Fields nil → both keys must be absent from the body.
	env := ErrorEnvelope{
		Code:     "BTF-0001",
		Service:  "plugin-br-bank-transfer",
		Category: "validation",
		Message:  "Invalid input",
	}

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return RespondErrorEnvelope(c, fiber.StatusBadRequest, env)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	assert.Equal(t, fiber.StatusBadRequest, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	// Parse as raw map to assert key absence (not just zero values).
	var raw map[string]map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))

	errMap, ok := raw["error"]
	require.True(t, ok, "response must have top-level 'error' key")

	// Always-emitted keys (no omitempty).
	assert.Contains(t, errMap, "code")
	assert.Contains(t, errMap, "service")
	assert.Contains(t, errMap, "category")
	assert.Contains(t, errMap, "message")

	// Omitempty keys must be absent.
	assert.NotContains(t, errMap, "requestId",
		"empty RequestID must be omitted via omitempty")
	assert.NotContains(t, errMap, "fields",
		"nil Fields must be omitted via omitempty")

	// Exactly four keys present (code, service, category, message).
	assert.Len(t, errMap, 4)
}

func TestRespondErrorEnvelope_AlwaysEmittedKeysSurviveEmptyValues(t *testing.T) {
	t.Parallel()

	// Empty Code / Service / Category / Message must still surface in the
	// body as empty strings (they have no omitempty). This protects strict
	// JSON-schema consumers that contract on the field set.
	env := ErrorEnvelope{}

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return RespondErrorEnvelope(c, fiber.StatusInternalServerError, env)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var raw map[string]map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))

	errMap, ok := raw["error"]
	require.True(t, ok)

	require.Contains(t, errMap, "code")
	require.Contains(t, errMap, "service")
	require.Contains(t, errMap, "category")
	require.Contains(t, errMap, "message")

	assert.Equal(t, "", errMap["code"])
	assert.Equal(t, "", errMap["service"])
	assert.Equal(t, "", errMap["category"])
	assert.Equal(t, "", errMap["message"])
	assert.NotContains(t, errMap, "requestId")
	assert.NotContains(t, errMap, "fields")
}

func TestRespondErrorEnvelope_NestedFieldsSerializeCorrectly(t *testing.T) {
	t.Parallel()

	env := ErrorEnvelope{
		Code:     "BTF-0001",
		Service:  "plugin-br-bank-transfer",
		Category: "validation",
		Message:  "Multiple validation failures",
		Fields: map[string]any{
			"violations": []any{
				map[string]any{
					"field":   "amount",
					"problem": "negative",
				},
				map[string]any{
					"field":   "currency",
					"problem": "unsupported",
				},
			},
			"retryable": false,
			"retryAfter": map[string]any{
				"seconds": 30,
				"jitter":  true,
			},
			"counts": []any{1, 2, 3},
		},
	}

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return RespondErrorEnvelope(c, fiber.StatusUnprocessableEntity, env)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	assert.Equal(t, fiber.StatusUnprocessableEntity, resp.StatusCode)

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(body, &raw))

	errObj, ok := raw["error"].(map[string]any)
	require.True(t, ok, "error must be a JSON object")

	fields, ok := errObj["fields"].(map[string]any)
	require.True(t, ok, "fields must be a JSON object")

	// Nested array of objects.
	violations, ok := fields["violations"].([]any)
	require.True(t, ok)
	require.Len(t, violations, 2)

	v0, ok := violations[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "amount", v0["field"])
	assert.Equal(t, "negative", v0["problem"])

	// Boolean primitive.
	assert.Equal(t, false, fields["retryable"])

	// Nested object.
	retryAfter, ok := fields["retryAfter"].(map[string]any)
	require.True(t, ok)
	assert.InDelta(t, float64(30), retryAfter["seconds"], 0)
	assert.Equal(t, true, retryAfter["jitter"])

	// Array of primitives.
	counts, ok := fields["counts"].([]any)
	require.True(t, ok)
	require.Len(t, counts, 3)
	assert.InDelta(t, float64(1), counts[0], 0)
	assert.InDelta(t, float64(2), counts[1], 0)
	assert.InDelta(t, float64(3), counts[2], 0)
}

// TestRespondErrorEnvelope_GoldenWireFormat locks the exact byte sequence of
// the JSON wire format for a fully-populated ErrorEnvelope. This is the
// load-bearing contract for downstream taxonomy consumers (BTF-style plugins)
// that match the response byte-for-byte against golden fixtures.
//
// encoding/json (Fiber v2's default JSON encoder) emits struct fields in
// declaration order and emits map[string]T keys in lexicographic order. Both
// are deterministic, so the golden bytes are stable across runs and platforms.
func TestRespondErrorEnvelope_GoldenWireFormat(t *testing.T) {
	t.Parallel()

	env := ErrorEnvelope{
		Code:      "BTF-0429",
		Service:   "plugin-br-bank-transfer",
		Category:  "rate_limit",
		Message:   "Rate limit exceeded",
		RequestID: "6d3e2a68-1f2b-4c3d-9e4f-5a6b7c8d9e0f",
		Fields: map[string]any{
			"limit":         100,
			"tenantHash":    "8b6f3e2a91c4d7e0",
			"windowSeconds": 60,
		},
	}

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return RespondErrorEnvelope(c, fiber.StatusTooManyRequests, env)
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	// Struct fields in declaration order: code, service, category, message,
	// requestId, fields. Map keys (in Fields) in lexicographic order: limit,
	// tenantHash, windowSeconds.
	expected := `{"error":{"code":"BTF-0429","service":"plugin-br-bank-transfer","category":"rate_limit","message":"Rate limit exceeded","requestId":"6d3e2a68-1f2b-4c3d-9e4f-5a6b7c8d9e0f","fields":{"limit":100,"tenantHash":"8b6f3e2a91c4d7e0","windowSeconds":60}}}`
	assert.Equal(t, expected, string(body),
		"wire format must be byte-exact — downstream BTF taxonomy consumers match goldens")
}

// ---------------------------------------------------------------------------
// Nil guard
// ---------------------------------------------------------------------------

func TestRespondErrorEnvelope_NilContext(t *testing.T) {
	t.Parallel()

	err := RespondErrorEnvelope(nil, fiber.StatusBadRequest, ErrorEnvelope{
		Code:    "BTF-0001",
		Service: "plugin-br-bank-transfer",
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrContextNotFound)
}

func TestRespondErrorEnvelope_InvalidStatus_NormalizesToInternalServerError(t *testing.T) {
	t.Parallel()

	app := fiber.New()
	app.Get("/test", func(c *fiber.Ctx) error {
		return RespondErrorEnvelope(c, 99, ErrorEnvelope{Code: "BTF-0001", Service: "plugin"})
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	assert.Equal(t, fiber.StatusInternalServerError, resp.StatusCode)
}

func TestRespondErrorEnvelope_JSONSerializationError_Propagates(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("json encoder failed")
	var captured error

	app := fiber.New(fiber.Config{
		JSONEncoder: func(_ any) ([]byte, error) {
			return nil, sentinel
		},
		ErrorHandler: func(c *fiber.Ctx, err error) error {
			captured = err
			return c.SendStatus(fiber.StatusTeapot)
		},
	})
	app.Get("/test", func(c *fiber.Ctx) error {
		return RespondErrorEnvelope(c, fiber.StatusBadRequest, ErrorEnvelope{Code: "BTF-0001", Service: "plugin"})
	})

	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer func() { require.NoError(t, resp.Body.Close()) }()

	assert.Equal(t, fiber.StatusTeapot, resp.StatusCode)
	assert.ErrorIs(t, captured, sentinel)
}
