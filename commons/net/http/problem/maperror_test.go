//go:build unit

package problem

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mapErrDetail unwraps the error returned by MapError into a *Detail.
func mapErrDetail(t *testing.T, err error) *Detail {
	t.Helper()

	require.Error(t, err)

	d, ok := err.(*Detail)
	require.True(t, ok, "expected *Detail, got %T", err)

	return d
}

// neverCodeOf is a codeOf that always reports !ok.
func neverCodeOf(error) (string, string, bool) { return "", "", false }

// staticStatusOf maps any code to 422 (a <500 status) for the happy paths.
func staticStatusOf(string) int { return http.StatusUnprocessableEntity }

func TestMapError_NilError_SanitizedFallback500(t *testing.T) {
	t.Parallel()

	d := mapErrDetail(t, MapError(nil, neverCodeOf, staticStatusOf, "SPB-0000"))

	assert.Equal(t, http.StatusInternalServerError, d.Status)
	assert.Equal(t, genericServerErrorDetail, d.Detail)
	assert.Equal(t, "SPB-0000", d.Code)
	assert.Equal(t, BaseURI+"/SPB-0000", d.Type)
}

func TestMapError_Unrecognized_SanitizedFallback500(t *testing.T) {
	t.Parallel()

	d := mapErrDetail(t, MapError(errors.New("some infra error"), neverCodeOf, staticStatusOf, "SPB-0000"))

	assert.Equal(t, http.StatusInternalServerError, d.Status)
	assert.Equal(t, genericServerErrorDetail, d.Detail)
	assert.Equal(t, "SPB-0000", d.Code)
	assert.Equal(t, BaseURI+"/SPB-0000", d.Type)
}

// TestMapError_Unrecognized_EmptyFallback proves the SPI path: empty fallbackCode
// yields a bare sanitized 500 with no Code/Type.
func TestMapError_Unrecognized_EmptyFallback(t *testing.T) {
	t.Parallel()

	d := mapErrDetail(t, MapError(errors.New("infra"), neverCodeOf, staticStatusOf, ""))

	assert.Equal(t, http.StatusInternalServerError, d.Status)
	assert.Equal(t, genericServerErrorDetail, d.Detail)
	assert.Empty(t, d.Code)
	assert.Empty(t, d.Type)
}

// TestMapError_Coded_SetsCodeAndType proves the coded <500 path: status from
// statusOf, msg passes through, Code set, Type = BaseURI + "/" + code.
func TestMapError_Coded_SetsCodeAndType(t *testing.T) {
	t.Parallel()

	codeOf := func(error) (string, string, bool) { return "SPB-3002", "amount exceeds limit", true }

	d := mapErrDetail(t, MapError(errors.New("domain"), codeOf, staticStatusOf, "SPB-0000"))

	assert.Equal(t, http.StatusUnprocessableEntity, d.Status)
	assert.Equal(t, "amount exceeds limit", d.Detail, "<500 msg passes through")
	assert.Equal(t, "SPB-3002", d.Code)
	assert.Equal(t, BaseURI+"/SPB-3002", d.Type)
}

// TestMapError_Coded_5xxScrubbedKeepsCode proves a coded 5xx scrubs the detail to
// "internal error" while still carrying Code/Type so clients can branch.
func TestMapError_Coded_5xxScrubbedKeepsCode(t *testing.T) {
	t.Parallel()

	codeOf := func(error) (string, string, bool) { return "SPB-9001", "raw db cause", true }
	statusOf := func(string) int { return http.StatusServiceUnavailable }

	d := mapErrDetail(t, MapError(errors.New("domain"), codeOf, statusOf, "SPB-0000"))

	assert.Equal(t, http.StatusServiceUnavailable, d.Status)
	assert.Equal(t, genericServerErrorDetail, d.Detail, "5xx detail sanitized")
	assert.Equal(t, "SPB-9001", d.Code, "Code still carried on sanitized 5xx")
	assert.Equal(t, BaseURI+"/SPB-9001", d.Type)
}

// TestMapError_Coded_EmptyCode_BareBody proves the SPI taxonomy-less path: a
// recognized error reporting an empty code yields a bare body (no Code/Type).
func TestMapError_Coded_EmptyCode_BareBody(t *testing.T) {
	t.Parallel()

	codeOf := func(error) (string, string, bool) { return "", "bad request", true }

	d := mapErrDetail(t, MapError(errors.New("domain"), codeOf, staticStatusOf, ""))

	assert.Equal(t, http.StatusUnprocessableEntity, d.Status)
	assert.Equal(t, "bad request", d.Detail)
	assert.Empty(t, d.Code)
	assert.Empty(t, d.Type)
}

// TestMapError_InvalidStatusClamped500 proves a misconfigured statusOf returning
// a non-error status (0, 2xx, 3xx) for a recognized code is clamped up to a
// sanitized 500 instead of emitting a malformed or success-looking problem; the
// domain Code/Type are still carried so clients can branch.
func TestMapError_InvalidStatusClamped500(t *testing.T) {
	t.Parallel()

	for _, badStatus := range []int{0, http.StatusOK, http.StatusFound} {
		t.Run(http.StatusText(badStatus), func(t *testing.T) {
			t.Parallel()

			codeOf := func(error) (string, string, bool) { return "SPB-3002", "leaky raw cause", true }
			statusOf := func(string) int { return badStatus }

			d := mapErrDetail(t, MapError(errors.New("domain"), codeOf, statusOf, "SPB-0000"))

			assert.Equal(t, http.StatusInternalServerError, d.Status, "invalid status must clamp to 500")
			assert.Equal(t, genericServerErrorDetail, d.Detail, "clamped 500 detail must be sanitized")
			assert.Equal(t, "SPB-3002", d.Code, "domain code still carried on clamped 500")
			assert.Equal(t, BaseURI+"/SPB-3002", d.Type)
		})
	}
}

// TestMapError_NilCallbacks_SanitizedFallback500 proves a miswired caller passing
// a nil codeOf or statusOf gets the canonical sanitized 500 (carrying the
// fallbackCode) instead of a panic.
func TestMapError_NilCallbacks_SanitizedFallback500(t *testing.T) {
	t.Parallel()

	t.Run("nil codeOf", func(t *testing.T) {
		t.Parallel()

		d := mapErrDetail(t, MapError(errors.New("x"), nil, staticStatusOf, "SPB-0000"))

		assert.Equal(t, http.StatusInternalServerError, d.Status)
		assert.Equal(t, genericServerErrorDetail, d.Detail)
		assert.Equal(t, "SPB-0000", d.Code)
		assert.Equal(t, BaseURI+"/SPB-0000", d.Type)
	})

	t.Run("nil statusOf", func(t *testing.T) {
		t.Parallel()

		d := mapErrDetail(t, MapError(errors.New("x"), neverCodeOf, nil, "SPB-0000"))

		assert.Equal(t, http.StatusInternalServerError, d.Status)
		assert.Equal(t, genericServerErrorDetail, d.Detail)
		assert.Equal(t, "SPB-0000", d.Code)
	})

	t.Run("both nil with empty fallback yields a bare body", func(t *testing.T) {
		t.Parallel()

		d := mapErrDetail(t, MapError(errors.New("x"), nil, nil, ""))

		assert.Equal(t, http.StatusInternalServerError, d.Status)
		assert.Empty(t, d.Code)
		assert.Empty(t, d.Type)
	})
}

// TestMapError_5xx_CarriesUpstream is the gateway's real path: the rail error is
// wrapped by a domain error, mapped through the per-rail seam, and the provider's
// own code/message must reach the client even though everything of OURS on a 5xx
// is scrubbed. MapError returns a *Detail straight to Huma, so huma.NewError —
// and therefore the Install override that lifts the member on the other seam —
// is never called on this path; if MapError did not lift it itself, a rail whose
// only error path is this seam could not publish the member at all.
func TestMapError_5xx_CarriesUpstream(t *testing.T) {
	t.Parallel()

	railErr := fmt.Errorf("consult rail: %w", &Upstream{Code: "E4001", Message: "serviço indisponível"})
	codeOf := func(error) (string, string, bool) { return "GW-9001", "raw db cause", true }
	statusOf := func(string) int { return http.StatusBadGateway }

	d := mapErrDetail(t, MapError(railErr, codeOf, statusOf, "GW-0000"))

	assert.Equal(t, http.StatusBadGateway, d.Status)
	assert.Equal(t, genericServerErrorDetail, d.Detail, "our own 5xx detail stays scrubbed")
	assert.Equal(t, "GW-9001", d.Code)
	require.NotNil(t, d.Upstream, "the provider's error must survive the 5xx sanitization")
	assert.Equal(t, "E4001", d.Upstream.Code)
	assert.Equal(t, "serviço indisponível", d.Upstream.Message)
}

// TestMapError_4xx_CarriesUpstream proves the member is not a 5xx-only affordance:
// a rail refusal mapped to a client-fixable status carries the provider's code
// alongside our own passed-through detail.
func TestMapError_4xx_CarriesUpstream(t *testing.T) {
	t.Parallel()

	railErr := fmt.Errorf("register proposal: %w", &Upstream{Code: "E22", Message: "prazo inválido"})
	codeOf := func(error) (string, string, bool) { return "GW-3002", "rail refused the proposal", true }

	d := mapErrDetail(t, MapError(railErr, codeOf, staticStatusOf, "GW-0000"))

	assert.Equal(t, http.StatusUnprocessableEntity, d.Status)
	assert.Equal(t, "rail refused the proposal", d.Detail)
	require.NotNil(t, d.Upstream)
	assert.Equal(t, "E22", d.Upstream.Code)
}

// TestMapError_Unrecognized_CarriesUpstream covers the case the member exists
// for: the rail failed with something our own taxonomy does not recognize, so the
// body is the canonical sanitized 500 and the provider's code is the ONLY
// diagnosable information the client has.
func TestMapError_Unrecognized_CarriesUpstream(t *testing.T) {
	t.Parallel()

	railErr := fmt.Errorf("call rail: %w", &Upstream{Code: "E500", Message: "gateway timeout"})

	d := mapErrDetail(t, MapError(railErr, neverCodeOf, staticStatusOf, "GW-0000"))

	assert.Equal(t, http.StatusInternalServerError, d.Status)
	assert.Equal(t, genericServerErrorDetail, d.Detail)
	assert.Equal(t, "GW-0000", d.Code)
	require.NotNil(t, d.Upstream)
	assert.Equal(t, "E500", d.Upstream.Code)
}

// TestMapError_NoUpstream_OmitsMember is the additive guarantee for every rail
// that never proxies a third party (br-slc, the scaffolds): nothing about their
// bodies changes, and an unrelated cause must never be promoted into the member.
func TestMapError_NoUpstream_OmitsMember(t *testing.T) {
	t.Parallel()

	cases := map[string]error{
		"plain error":      errors.New("some infra error"),
		"nil error":        nil,
		"empty member":     &Upstream{},
		"wrapped empty":    fmt.Errorf("call rail: %w", &Upstream{}),
		"typed-nil member": fmt.Errorf("call rail: %w", (*Upstream)(nil)),
	}

	for name, err := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			d := mapErrDetail(t, MapError(err, neverCodeOf, staticStatusOf, "GW-0000"))
			assert.Nil(t, d.Upstream, "no curated upstream means no member on the wire")
		})
	}
}

// TestMapError_Upstream_JSON is the wire-level proof for this seam: the body a
// client receives has `upstream` as a top-level RFC 9457 extension member.
func TestMapError_Upstream_JSON(t *testing.T) {
	t.Parallel()

	railErr := fmt.Errorf("consult rail: %w", &Upstream{Code: "E4001", Message: "serviço indisponível"})
	codeOf := func(error) (string, string, bool) { return "GW-9001", "leaky raw cause", true }
	statusOf := func(string) int { return http.StatusServiceUnavailable }

	raw, err := json.Marshal(MapError(railErr, codeOf, statusOf, "GW-0000"))
	require.NoError(t, err)

	var body map[string]any
	require.NoError(t, json.Unmarshal(raw, &body))

	assert.Equal(t, genericServerErrorDetail, body["detail"])

	up, ok := body["upstream"].(map[string]any)
	require.True(t, ok, "upstream must reach the wire through MapError, got %s", raw)
	assert.Equal(t, "E4001", up["code"])
	assert.Equal(t, "serviço indisponível", up["message"])
}
