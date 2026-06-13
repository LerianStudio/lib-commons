//go:build unit

package problem

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/danielgtaylor/huma/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBaseURI(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "https://errors.lerian.studio/v1", BaseURI)
}

// TestDetail_SatisfiesStatusError proves *Detail implements huma.StatusError via
// method promotion from the embedded ErrorModel — the property that lets the
// override and MapError return a *Detail wherever Huma expects a StatusError.
func TestDetail_SatisfiesStatusError(t *testing.T) {
	t.Parallel()

	var _ huma.StatusError = (*Detail)(nil)

	d := &Detail{
		ErrorModel: huma.ErrorModel{Status: http.StatusTeapot, Title: "I'm a teapot", Detail: "nope"},
	}

	var se huma.StatusError = d
	assert.Equal(t, http.StatusTeapot, se.GetStatus())
	assert.NotEmpty(t, se.Error())
}

// TestDetail_JSON_OmitsEmptyCode asserts the omitempty contract: a code-less
// Detail must not emit a `code` key on the wire, so code-less rails keep a bare
// RFC 9457 body.
func TestDetail_JSON_OmitsEmptyCode(t *testing.T) {
	t.Parallel()

	d := &Detail{
		ErrorModel: huma.ErrorModel{
			Status: http.StatusBadRequest,
			Title:  http.StatusText(http.StatusBadRequest),
			Detail: "bad input",
		},
	}

	raw, err := json.Marshal(d)
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))

	_, hasCode := m["code"]
	assert.False(t, hasCode, "empty Code must be dropped by omitempty, got %s", raw)
	assert.Equal(t, "bad input", m["detail"])
	assert.EqualValues(t, http.StatusBadRequest, m["status"])
}

// TestDetail_JSON_IncludesCode asserts that once Code is set it serializes under
// the `code` key.
func TestDetail_JSON_IncludesCode(t *testing.T) {
	t.Parallel()

	d := &Detail{
		ErrorModel: huma.ErrorModel{
			Status: http.StatusUnprocessableEntity,
			Title:  http.StatusText(http.StatusUnprocessableEntity),
			Detail: "boom",
			Type:   BaseURI + "/SPB-3002",
		},
		Code: "SPB-3002",
	}

	raw, err := json.Marshal(d)
	require.NoError(t, err)

	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))

	assert.Equal(t, "SPB-3002", m["code"])
	assert.Equal(t, BaseURI+"/SPB-3002", m["type"])
}
