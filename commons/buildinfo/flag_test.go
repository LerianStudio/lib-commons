//go:build unit

package buildinfo

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// flagEnv re-executes the test binary as a process whose os.Args[1] is the
// value of the variable, so HandleFlag can be observed exiting.
const flagEnv = "LIB_COMMONS_BUILDINFO_FLAG_ARG"

// noFlagExit is the status the re-executed process leaves when HandleFlag
// returns instead of exiting.
const noFlagExit = 7

// setEnv carries an injected identity into the re-executed process as
// "version|revision|buildTime". FC-5 gates every CI image on
// "docker run <image> --version", so the linker-injected values have to be
// observable through the real process, not only through writeVersion.
const setEnv = "LIB_COMMONS_BUILDINFO_SET"

const (
	injectedVersion   = "4.0.3"
	injectedRevision  = "cafebabe00000000000000000000000000000000"
	injectedBuildTime = "2026-09-22T19:00:00Z"
)

func injectedBuild() Build {
	return Build{Version: injectedVersion, Revision: injectedRevision, BuildTime: injectedBuildTime}
}

func TestMain(m *testing.M) {
	arg, reexec := os.LookupEnv(flagEnv)
	if !reexec {
		os.Exit(m.Run())
	}

	if _, inject := os.LookupEnv(setEnv); inject {
		Set(injectedBuild())
	}

	os.Args = []string{"service"}
	if arg != "" {
		os.Args = append(os.Args, arg)
	}

	HandleFlag()
	os.Exit(noFlagExit)
}

type failingWriter struct{}

var errWrite = errors.New("stdout is gone")

func (failingWriter) Write([]byte) (int, error) { return 0, errWrite }

// TestWriteVersion mutates the package-level injected Build, a process-global:
// no t.Parallel(), and Cleanup puts the zero value back.
func TestWriteVersion(t *testing.T) {
	t.Cleanup(func() { Set(Build{}) })

	Set(injectedBuild())

	var out strings.Builder

	require.NoError(t, writeVersion(&out))

	body := out.String()
	assert.True(t, strings.HasSuffix(body, "\n"), "output must end with one newline")
	assert.Equal(t, 1, strings.Count(body, "\n"), "output must be a single line")

	var got map[string]any
	require.NoError(t, json.Unmarshal([]byte(body), &got))

	assert.NotContains(t, got, "service", "--version output omits service")
	assert.Equal(t, "v1", got["schemaVersion"])
	assert.Equal(t, injectedVersion, got["version"])
	assert.Equal(t, injectedRevision, got["revision"])
	assert.Equal(t, injectedBuildTime, got["buildTime"])
	assert.Equal(t, false, got["modified"], "the test binary carries no dirty vcs stamp")
	assert.Equal(t, runtime.Version(), got["goVersion"])

	manifest, ok := got["dependencyManifest"].(map[string]any)
	require.True(t, ok, "dependencyManifest must be an object")
	assert.Equal(t, "go-buildinfo-v1", manifest["format"])
	assert.Equal(t, "lerian", manifest["scope"])
	assert.NotNil(t, manifest["modules"], "modules must serialize as a list")
}

func TestWriteVersionWriteFailure(t *testing.T) {
	t.Parallel()

	assert.ErrorIs(t, writeVersion(failingWriter{}), errWrite)
}

func TestHandleFlag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		arg  string
		code int
		json bool
	}{
		{name: "version flag prints and exits zero", arg: "--version", code: 0, json: true},
		{name: "other argument is ignored", arg: "--help", code: noFlagExit},
		{name: "no argument is ignored", arg: "", code: noFlagExit},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cmd := exec.Command(os.Args[0])
			cmd.Env = append(os.Environ(), flagEnv+"="+tt.arg, setEnv+"=1")

			out, err := cmd.Output()

			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				err = nil
			}

			require.NoError(t, err)
			assert.Equal(t, tt.code, cmd.ProcessState.ExitCode())

			if !tt.json {
				assert.Empty(t, out)
				return
			}

			var got map[string]any
			require.NoError(t, json.Unmarshal(out, &got))
			assert.NotContains(t, got, "service")
			assert.Equal(t, "v1", got["schemaVersion"])
			assert.Equal(t, injectedVersion, got["version"], "FC-5 compares this against the release tag")
			assert.Equal(t, injectedRevision, got["revision"], "FC-5 compares this against the build revision")
			assert.Equal(t, injectedBuildTime, got["buildTime"])
		})
	}
}
