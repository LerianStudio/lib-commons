//go:build unit

package commons

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReleasePolicy_BreakingChangesMapToMajorForTheV7Cut pins the release rule
// during the /v7 bootstrap, and is the inverse of the guard it replaces.
//
// The rule it used to enforce — breaking commits map to `minor`, majors cut by
// hand — was written for the /v6 line by #558. Its premise was that a hand-made
// vN.0.0 tag is a viable substitute for letting CI compute the major. It is not:
// semantic-release only adopts a tag as `lastRelease` when the tag carries its
// own channel record, so a hand-made tag stays invisible for that purpose while
// still feeding getNextVersion's highest() second term. The result is a version
// that collides with the very tag that produced it — `fatal: tag
// 'v7.0.0-beta.1' already exists`, on every run (observed on run 33907401438,
// twice). Removing the manual tag then let CI publish v6.9.0-beta.5 against a
// /v7 module path, which `go get` rejects outright.
//
// So the rule is flipped to `major` for exactly one release, the same one-shot
// #547 used for v5→v6 and #558 reverted afterwards.
//
// THIS TEST MUST BE RESTORED TO ITS `minor` FORM once v7.0.0-beta.1 is
// published. Left on `major`, the next breaking commit computes v8.0.0-beta.1
// against a /v7 path and publishes another tag nobody can resolve. The guard
// exists so that reverting is a deliberate act rather than something forgotten.
func TestReleasePolicy_BreakingChangesMapToMajorForTheV7Cut(t *testing.T) {
	t.Parallel()

	content, err := os.ReadFile("../.releaserc.yml")
	require.NoError(t, err)

	// Collapse all whitespace (spaces, tabs, newlines) so the guard cannot be
	// bypassed by reformatting the rule across lines or with tabs.
	normalized := strings.Join(strings.Fields(string(content)), "")
	assert.Contains(t, normalized, `{breaking:true,release:"major"}`)
	assert.NotContains(t, normalized, `{breaking:true,release:"minor"}`)
}
