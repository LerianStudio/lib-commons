//go:build unit

package problem

import (
	"errors"
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
