//go:build unit

package constant

import (
	"testing"

	obsconstants "github.com/LerianStudio/lib-observability/v2/constants"
	"github.com/stretchr/testify/require"
)

func TestTraceConstantsMatchLibObservability(t *testing.T) {
	t.Parallel()

	require.Equal(t, obsconstants.HeaderTraceparentPascal, HeaderTraceparent)
	require.Equal(t, obsconstants.HeaderTraceparentPascal, HeaderTraceparentPascal)
	require.Equal(t, obsconstants.HeaderTracestatePascal, HeaderTracestatePascal)
	require.Equal(t, obsconstants.MetadataTraceparent, MetadataTraceparent)
	require.Equal(t, obsconstants.MetadataTracestate, MetadataTracestate)
}

func TestObfuscatedValueMatchesLibObservability(t *testing.T) {
	t.Parallel()

	require.Equal(t, "********", ObfuscatedValue)
	require.Equal(t, obsconstants.ObfuscatedValue, ObfuscatedValue)
}
