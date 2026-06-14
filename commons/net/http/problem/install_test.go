//go:build unit

package problem

import (
	"errors"
	"net/http"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// detailErr is a test error implementing huma.ErrorDetailer to prove newError
// honors the interface (folding the carried *huma.ErrorDetail rather than the
// Error() string) on the <500 path.
type detailErr struct {
	d *huma.ErrorDetail
}

func (e *detailErr) Error() string                  { return e.d.Message }
func (e *detailErr) ErrorDetail() *huma.ErrorDetail { return e.d }

// asDetail asserts the StatusError is a *Detail and returns it.
func asDetail(t *testing.T, se huma.StatusError) *Detail {
	t.Helper()

	d, ok := se.(*Detail)
	require.True(t, ok, "expected *Detail, got %T", se)

	return d
}

// TestNewError_ServerError_Scrubbed is the central safety assertion: every
// status>=500 is scrubbed to the static detail and folds NO errs, even when a
// raw cause is passed (the leak underwriter's scrubber closes).
func TestNewError_ServerError_Scrubbed(t *testing.T) {
	t.Parallel()

	for _, status := range []int{
		http.StatusInternalServerError,
		http.StatusBadGateway,
		http.StatusServiceUnavailable,
	} {
		d := asDetail(t, newError(status, "db password = hunter2", errors.New("leaky raw cause")))

		assert.Equal(t, status, d.Status)
		assert.Equal(t, http.StatusText(status), d.Title)
		assert.Equal(t, genericServerErrorDetail, d.Detail, "5xx detail must be scrubbed")
		assert.Nil(t, d.Errors, "5xx must fold NO errs")
		assert.Empty(t, d.Code)
		assert.Empty(t, d.Type, "Type stays RFC default about:blank (empty)")
	}
}

// TestNewError_ClientError_PassthroughAndFold proves the <500 path: msg passes
// through and errs fold into Errors[] in order — preserving Huma's native 422
// field-error behavior.
func TestNewError_ClientError_PassthroughAndFold(t *testing.T) {
	t.Parallel()

	d := asDetail(t, newError(
		http.StatusUnprocessableEntity,
		"validation failed",
		errors.New("field a invalid"),
		errors.New("field b invalid"),
	))

	assert.Equal(t, http.StatusUnprocessableEntity, d.Status)
	assert.Equal(t, "validation failed", d.Detail)
	require.Len(t, d.Errors, 2)
	assert.Equal(t, "field a invalid", d.Errors[0].Message)
	assert.Equal(t, "field b invalid", d.Errors[1].Message, "order preserved")
}

// TestNewError_ClientError_SkipsNilErrs proves nil entries are skipped and an
// all-nil / empty slice yields a nil Errors (not an empty []).
func TestNewError_ClientError_SkipsNilErrs(t *testing.T) {
	t.Parallel()

	t.Run("nil interspersed", func(t *testing.T) {
		t.Parallel()

		d := asDetail(t, newError(http.StatusBadRequest, "msg", nil, errors.New("real"), nil))

		require.Len(t, d.Errors, 1)
		assert.Equal(t, "real", d.Errors[0].Message)
	})

	t.Run("no errs at all", func(t *testing.T) {
		t.Parallel()

		d := asDetail(t, newError(http.StatusBadRequest, "msg"))
		assert.Nil(t, d.Errors, "empty slice must become nil, not []")
	})

	t.Run("all nil", func(t *testing.T) {
		t.Parallel()

		d := asDetail(t, newError(http.StatusNotFound, "missing", nil, nil))
		assert.Nil(t, d.Errors)
	})
}

// TestNewError_HonorsErrorDetailer proves a huma.ErrorDetailer's carried detail
// is folded (not its Error() string) on the <500 path.
func TestNewError_HonorsErrorDetailer(t *testing.T) {
	t.Parallel()

	carried := &huma.ErrorDetail{Message: "carried", Location: "body.name", Value: "x"}
	d := asDetail(t, newError(http.StatusBadRequest, "msg", &detailErr{d: carried}))

	require.Len(t, d.Errors, 1)
	assert.Same(t, carried, d.Errors[0], "ErrorDetailer's *ErrorDetail folded verbatim")
	assert.Equal(t, "body.name", d.Errors[0].Location)
}

// nilDetailErr is a test error implementing huma.ErrorDetailer whose
// ErrorDetail() returns a nil *huma.ErrorDetail, exercising the skip-nil guard
// on the <500 fold path.
type nilDetailErr struct{}

func (e *nilDetailErr) Error() string                  { return "nil detail" }
func (e *nilDetailErr) ErrorDetail() *huma.ErrorDetail { return nil }

// TestNewError_SkipsNilErrorDetail proves an ErrorDetailer returning a nil
// *huma.ErrorDetail is skipped (no null entry in Errors[]) while a valid sibling
// is still folded.
func TestNewError_SkipsNilErrorDetail(t *testing.T) {
	t.Parallel()

	t.Run("nil detail interspersed with a valid sibling", func(t *testing.T) {
		t.Parallel()

		carried := &huma.ErrorDetail{Message: "kept"}
		d := asDetail(t, newError(
			http.StatusBadRequest,
			"msg",
			&nilDetailErr{},
			&detailErr{d: carried},
		))

		require.Len(t, d.Errors, 1, "nil ErrorDetail must be skipped, valid sibling kept")
		for _, e := range d.Errors {
			assert.NotNil(t, e, "no null entry must be folded into Errors[]")
		}
		assert.Same(t, carried, d.Errors[0])
	})

	t.Run("only a nil-detail error yields nil Errors", func(t *testing.T) {
		t.Parallel()

		d := asDetail(t, newError(http.StatusBadRequest, "msg", &nilDetailErr{}))
		assert.Nil(t, d.Errors, "a sole nil ErrorDetail leaves Errors nil, not [null]")
	})
}

// TestInstall_OverridesGlobal_Idempotent proves Install swaps the process-global
// huma.NewError to our *Detail-producing override and that a second call is a
// safe no-op. It restores the global afterward to avoid leaking across packages.
func TestInstall_OverridesGlobal_Idempotent(t *testing.T) {
	// NOT parallel: mutates the process-global huma.NewError.
	original := huma.NewError
	t.Cleanup(func() { huma.NewError = original })

	Install()
	first := asDetail(t, huma.NewError(http.StatusBadRequest, "after install"))
	assert.Equal(t, "after install", first.Detail)

	// A second Install must not re-wrap or otherwise change behavior.
	Install()
	scrubbed := asDetail(t, huma.NewError(http.StatusInternalServerError, "raw cause"))
	assert.Equal(t, genericServerErrorDetail, scrubbed.Detail)
}
