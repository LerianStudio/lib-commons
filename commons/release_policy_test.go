//go:build unit

package commons

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReleasePolicy_BreakingChangesTriggerMajor(t *testing.T) {
	t.Parallel()

	content, err := os.ReadFile("../.releaserc.yml")
	require.NoError(t, err)

	policy := string(content)
	assert.Contains(t, policy, `{ breaking: true, release: "major" }`)
	assert.NotContains(t, strings.ReplaceAll(policy, " ", ""), `breaking:true,release:"minor"`)
}
