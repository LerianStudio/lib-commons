//go:build unit

package boundary_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestScanFile pins the shapes the boundary scan must catch and the shapes it
// must leave alone. Each case is a whole synthetic source file; a regression in
// scanFile flips exactly one of them.
func TestScanFile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		source   string
		wantHits int
	}{
		{
			name: "exported var with inferred lib-observability type",
			source: `package p
import liblog "github.com/LerianStudio/lib-observability/v4/log"
var Public = liblog.NewNop()
`,
			wantHits: 1,
		},
		{
			name: "dot import of lib-observability",
			source: `package p
import . "github.com/LerianStudio/lib-observability/v4/log"
func Public() Logger { return NewNop() }
`,
			wantHits: 1,
		},
		{
			name: "exported var with annotated lib-observability type",
			source: `package p
import liblog "github.com/LerianStudio/lib-observability/v4/log"
var Public liblog.Logger
`,
			wantHits: 1,
		},
		{
			name: "exported func result",
			source: `package p
import liblog "github.com/LerianStudio/lib-observability/v4/log"
func Public() liblog.Logger { return nil }
`,
			wantHits: 1,
		},
		{
			name: "exported struct field",
			source: `package p
import liblog "github.com/LerianStudio/lib-observability/v4/log"
type S struct{ Field liblog.Logger }
`,
			wantHits: 1,
		},
		{
			name: "reexported alias",
			source: `package p
import liblog "github.com/LerianStudio/lib-observability/v4/log"
type Logger = liblog.Logger
`,
			wantHits: 1,
		},
		{
			name: "unexported var with inferred lib-observability type",
			source: `package p
import liblog "github.com/LerianStudio/lib-observability/v4/log"
var private = liblog.NewNop()
`,
			wantHits: 0,
		},
		{
			name: "unexported struct field",
			source: `package p
import liblog "github.com/LerianStudio/lib-observability/v4/log"
type S struct{ field liblog.Logger }
`,
			wantHits: 0,
		},
		{
			name: "exported constant aliasing an untyped upstream constant",
			source: `package p
import obsconstants "github.com/LerianStudio/lib-observability/v4/constants"
const Traceparent = obsconstants.MetadataTraceparent
`,
			wantHits: 0,
		},
		{
			name: "lib-observability used only in an unexported function body",
			source: `package p
import liblog "github.com/LerianStudio/lib-observability/v4/log"
func private() { _ = liblog.NewNop() }
`,
			wantHits: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "source.go")
			require.NoError(t, os.WriteFile(path, []byte(tt.source), 0o600))

			found, err := scanFile(path, "synthetic/source.go")
			require.NoError(t, err)
			assert.Len(t, found, tt.wantHits, "violations: %v", found)
		})
	}
}
