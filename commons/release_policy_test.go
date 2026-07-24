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

	// Collapse all whitespace (spaces, tabs, newlines) so the guard cannot be
	// bypassed by reformatting the rule across lines or with tabs.
	normalized := strings.Join(strings.Fields(string(content)), "")
	assert.Contains(t, normalized, `{breaking:true,release:"minor"}`)
	assert.NotContains(t, normalized, `{breaking:true,release:"major"}`)
}
