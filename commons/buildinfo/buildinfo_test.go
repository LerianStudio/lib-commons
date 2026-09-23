//go:build unit

package buildinfo

import (
	"testing"

	"github.com/LerianStudio/lib-commons/v7/commons/internal/buildid"
	"github.com/stretchr/testify/assert"
)

func TestScope(t *testing.T) {
	t.Parallel()

	name, version := Scope("commons/postgres")
	assert.Equal(t, "github.com/LerianStudio/lib-commons/v7/commons/postgres", name)

	// lib-commons is the main module of its own test binary and the toolchain
	// stamps no version on it, so (devel) is the only reachable value here.
	assert.Equal(t, "(devel)", version)
}

func TestFromCoreKeepsEveryField(t *testing.T) {
	t.Parallel()

	got := fromCore([]buildid.Module{
		{Path: "github.com/LerianStudio/lib-auth/v3", Version: "v3.1.0", Sum: "h1:abc="},
		{Path: "github.com/LerianStudio/lib-observability/v2", Version: "v2.0.0",
			Replace: &buildid.Module{Path: "../lib-observability", Version: "(devel)"}},
	})

	assert.Equal(t, []Module{
		{Path: "github.com/LerianStudio/lib-auth/v3", Version: "v3.1.0", Sum: "h1:abc="},
		{Path: "github.com/LerianStudio/lib-observability/v2", Version: "v2.0.0",
			Replace: &Module{Path: "../lib-observability", Version: "(devel)"}},
	}, got)
}

func TestFromCoreNeverReturnsNil(t *testing.T) {
	t.Parallel()

	assert.NotNil(t, fromCore(nil), "an empty manifest encodes as [], never null")
}
