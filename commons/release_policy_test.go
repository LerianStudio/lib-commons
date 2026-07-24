//go:build unit

package commons

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReleasePolicy_BreakingChangesStayMinor guards the /v6 release policy set
// by #558: Go module majors live in the import path and are cut by hand, so CI
// must never auto-major. Breaking commits map to `minor` on this line, and the
// rule must not be re-flipped back to `major` (see the header comment in
// .releaserc.yml). This test enforces both directions.
func TestReleasePolicy_BreakingChangesStayMinor(t *testing.T) {
	t.Parallel()

	content, err := os.ReadFile("../.releaserc.yml")
	require.NoError(t, err)

	policy := string(content)
	assert.Contains(t, policy, `{ breaking: true, release: "minor" }`)
	assert.NotContains(t, strings.ReplaceAll(policy, " ", ""), `breaking:true,release:"major"`)
}
