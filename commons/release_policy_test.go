//go:build unit

package commons

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReleasePolicy_BreakingChangesStayMinor keeps `breaking` anchored to
// `minor` between majors. A Go module's major lives in the import path, so a
// tag whose major disagrees with the path is unconsumable: left on `major`, the
// next breaking commit computes v8.0.0-beta.1 against this /v7 path and
// publishes a tag `go get` refuses.
//
// #633 flipped the rule to `major` for exactly one release to let CI publish
// v7.0.0-beta.1, and this restores it. Cutting v8 follows the same one-shot:
// rename the path, flip to `major` alongside a breaking commit on a
// non-ignored path, let CI publish, revert here immediately.
//
// Do NOT replace that with a hand-made tag. It was tried across #630..#633 and
// cannot work: semantic-release adopts a tag as `lastRelease` only when the tag
// carries its own channel record, so a hand-made tag is invisible for that
// while still feeding getNextVersion's highest() second term — it recomputes
// its own version and collides with itself on every run.
//
// The whitespace normalization below exists so the rule cannot be bypassed by
// reformatting it across lines or with tabs.
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
