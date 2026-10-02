//go:build unit

package problem

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/humatest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cpf is a synthetic Brazilian taxpayer document, the kind of value a 4xx body
// must never echo back.
const cpf = "123.456.789-09"

// dropValueEchoForTest turns the drop on for one test and puts both the flag and
// the process-global constructor back afterwards. NOT for parallel tests: both
// are process-global.
func dropValueEchoForTest(t *testing.T) {
	t.Helper()

	original := huma.NewError

	t.Cleanup(func() {
		huma.NewError = original

		dropValueEcho.Store(false)
	})

	InstallWithoutValueEcho()
}

// errorsJSON marshals d and returns its errors[] entries as generic maps.
func errorsJSON(t *testing.T, se huma.StatusError) (string, []map[string]any) {
	t.Helper()

	raw, err := json.Marshal(se)
	require.NoError(t, err)

	var body struct {
		Errors []map[string]any `json:"errors"`
	}

	require.NoError(t, json.Unmarshal(raw, &body))

	return string(raw), body.Errors
}

func TestInstallWithoutValueEcho_DropsValueFromValidationDetail(t *testing.T) {
	dropValueEchoForTest(t)

	carried := &huma.ErrorDetail{Message: "expected string", Location: "body.document", Value: cpf}

	d := asDetail(t, newError(http.StatusUnprocessableEntity, "validation failed", &detailErr{d: carried}))

	require.Len(t, d.Errors, 1)
	assert.Equal(t, "expected string", d.Errors[0].Message)
	assert.Equal(t, "body.document", d.Errors[0].Location)
	assert.Nil(t, d.Errors[0].Value)

	raw, entries := errorsJSON(t, d)
	require.Len(t, entries, 1)
	assert.NotContains(t, entries[0], "value", "the wire entry carries no value key")
	assert.NotContains(t, raw, cpf)

	assert.Equal(t, cpf, carried.Value, "the caller's detail is copied, never modified")
}

func TestInstallWithoutValueEcho_DropsRawBodyFromMalformedBodyDetail(t *testing.T) {
	dropValueEchoForTest(t)

	rawBody := `{"document": "` + cpf + `"`
	carried := &huma.ErrorDetail{Message: "unexpected end of JSON input", Location: "body", Value: rawBody}

	d := asDetail(t, newError(http.StatusBadRequest, "request body could not be parsed", carried))

	require.Len(t, d.Errors, 1)
	assert.Equal(t, "body", d.Errors[0].Location)
	assert.Nil(t, d.Errors[0].Value)

	raw, _ := errorsJSON(t, d)
	assert.NotContains(t, raw, cpf)
	assert.Equal(t, rawBody, carried.Value, "the caller's detail is copied, never modified")
}

func TestInstall_KeepsValueEchoByDefault(t *testing.T) {
	// NOT parallel: reads the process-global flag other tests in this file set.
	installForTest(t)

	carried := &huma.ErrorDetail{Message: "expected string", Location: "body.document", Value: cpf}

	d := asDetail(t, huma.NewError(http.StatusUnprocessableEntity, "validation failed", carried))

	require.Len(t, d.Errors, 1)
	assert.Same(t, carried, d.Errors[0], "without the opt-in, details fold verbatim")
	assert.Equal(t, cpf, d.Errors[0].Value)
}

func TestInstallWithoutValueEcho_IsStickyAcrossLaterInstall(t *testing.T) {
	dropValueEchoForTest(t)

	Install()

	carried := &huma.ErrorDetail{Message: "expected string", Location: "body.document", Value: cpf}

	d := asDetail(t, huma.NewError(http.StatusUnprocessableEntity, "validation failed", carried))

	require.Len(t, d.Errors, 1)
	assert.Nil(t, d.Errors[0].Value, "a plain Install after the opt-in never turns the drop back off")
}

func TestInstallWithoutValueEcho_ReachesADecoratorOfTheInstalledModel(t *testing.T) {
	dropValueEchoForTest(t)

	installedModel := huma.NewError
	huma.NewError = func(status int, msg string, errs ...error) huma.StatusError {
		return installedModel(status, msg, errs...)
	}

	carried := &huma.ErrorDetail{Message: "expected string", Location: "query.document", Value: cpf}

	d := asDetail(t, huma.NewError(http.StatusUnprocessableEntity, "validation failed", carried))

	require.Len(t, d.Errors, 1)
	assert.Nil(t, d.Errors[0].Value)
}

type consultationInput struct {
	Body struct {
		Document string `json:"document"`
	}
}

type lookupInput struct {
	Limit int `query:"limit"`
}

// TestInstallWithoutValueEcho_EndToEnd drives real Huma request handling: Huma
// builds every one of these errors itself, so this is the path a service takes.
func TestInstallWithoutValueEcho_EndToEnd(t *testing.T) {
	dropValueEchoForTest(t)

	_, api := humatest.New(t)

	huma.Post(api, "/consultations", func(context.Context, *consultationInput) (*struct{}, error) {
		return nil, nil
	})

	huma.Get(api, "/lookups", func(context.Context, *lookupInput) (*struct{}, error) {
		return nil, nil
	})

	const cpfDigits = "12345678909"

	cases := []struct {
		name     string
		do       func() *http.Response
		status   int
		location string
		secret   string
	}{
		{
			name: "schema type mismatch",
			do: func() *http.Response {
				return api.Post("/consultations", strings.NewReader(`{"document": `+cpfDigits+`}`)).Result()
			},
			status:   http.StatusUnprocessableEntity,
			location: "body.document",
			secret:   cpfDigits,
		},
		{
			name: "malformed json body",
			do: func() *http.Response {
				return api.Post("/consultations", strings.NewReader(`{"document": "`+cpf+`"`)).Result()
			},
			status:   http.StatusBadRequest,
			location: "body",
			secret:   cpf,
		},
		{
			name: "query parse failure",
			do: func() *http.Response {
				return api.Get("/lookups?limit=" + cpfDigits + "x").Result()
			},
			status:   http.StatusUnprocessableEntity,
			location: "query.limit",
			secret:   cpfDigits,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := tc.do()
			t.Cleanup(func() { _ = resp.Body.Close() })

			require.Equal(t, tc.status, resp.StatusCode)

			var body struct {
				Errors []map[string]any `json:"errors"`
			}

			raw, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(raw, &body))

			assert.NotContains(t, string(raw), tc.secret, "the error body never echoes the input")
			require.NotEmpty(t, body.Errors)

			for _, entry := range body.Errors {
				assert.NotContains(t, entry, "value")
			}

			assert.Equal(t, tc.location, body.Errors[0]["location"], "the location still names the field")
		})
	}
}
