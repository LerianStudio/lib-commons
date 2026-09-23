//go:build unit

package buildid

import (
	"runtime"
	"runtime/debug"
	"testing"
)

func newBuildInfo(main debug.Module, settings []debug.BuildSetting, deps ...*debug.Module) *debug.BuildInfo {
	return &debug.BuildInfo{
		GoVersion: "go1.26.3",
		Main:      main,
		Settings:  settings,
		Deps:      deps,
	}
}

func vcsSettings(revision, buildTime, modified string) []debug.BuildSetting {
	return []debug.BuildSetting{
		{Key: "-trimpath", Value: "true"},
		{Key: "vcs.revision", Value: revision},
		{Key: "vcs.time", Value: buildTime},
		{Key: "vcs.modified", Value: modified},
	}
}

func serviceMain() debug.Module {
	return debug.Module{Path: "github.com/LerianStudio/midaz", Version: "v4.0.3"}
}

func TestComputePrecedence(t *testing.T) {
	t.Parallel()

	vcs := vcsSettings("1111111111111111111111111111111111111111", "2026-09-01T00:00:00Z", "true")

	tests := []struct {
		name  string
		info  *debug.BuildInfo
		build Build
		want  Info
	}{
		{
			name:  "injected values win over the vcs stamp",
			info:  newBuildInfo(serviceMain(), vcs),
			build: Build{Version: "4.0.3", Revision: "abcdef0123456789", BuildTime: "2026-09-22T19:00:00Z"},
			want: Info{
				Version:   "4.0.3",
				Revision:  "abcdef0123456789",
				BuildTime: "2026-09-22T19:00:00Z",
				Modified:  true,
				GoVersion: "go1.26.3",
			},
		},
		{
			name:  "vcs stamp fills what was not injected and version never comes from it",
			info:  newBuildInfo(serviceMain(), vcsSettings("2222222222222222222222222222222222222222", "2026-09-02T10:00:00Z", "false")),
			build: Build{},
			want: Info{
				Version:   "dev",
				Revision:  "2222222222222222222222222222222222222222",
				BuildTime: "2026-09-02T10:00:00Z",
				Modified:  false,
				GoVersion: "go1.26.3",
			},
		},
		{
			name:  "no build info falls back to dev and unknown",
			info:  nil,
			build: Build{},
			want: Info{
				Version:   "dev",
				Revision:  "unknown",
				BuildTime: "unknown",
				Modified:  false,
				GoVersion: runtime.Version(),
			},
		},
		{
			name:  "no build info still honours injected values",
			info:  nil,
			build: Build{Version: "1.2.0-beta.1", Revision: "deadbeef", BuildTime: "2026-09-22T19:00:00Z"},
			want: Info{
				Version:   "1.2.0-beta.1",
				Revision:  "deadbeef",
				BuildTime: "2026-09-22T19:00:00Z",
				Modified:  false,
				GoVersion: runtime.Version(),
			},
		},
		{
			name:  "go version comes from the build info",
			info:  newBuildInfo(serviceMain(), nil),
			build: Build{},
			want: Info{
				Version:   "dev",
				Revision:  "unknown",
				BuildTime: "unknown",
				Modified:  false,
				GoVersion: "go1.26.3",
			},
		},
		{
			name:  "an empty build-info go version falls back to the runtime",
			info:  &debug.BuildInfo{Main: serviceMain()},
			build: Build{},
			want: Info{
				Version:   "dev",
				Revision:  "unknown",
				BuildTime: "unknown",
				Modified:  false,
				GoVersion: runtime.Version(),
			},
		},
		{
			name:  "empty vcs values do not overwrite the fallbacks",
			info:  newBuildInfo(serviceMain(), vcsSettings("", "", "")),
			build: Build{},
			want: Info{
				Version:   "dev",
				Revision:  "unknown",
				BuildTime: "unknown",
				Modified:  false,
				GoVersion: "go1.26.3",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := compute(tc.info, tc.build)
			if got != tc.want {
				t.Fatalf("compute() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestModulesFilterAndOrder(t *testing.T) {
	t.Parallel()

	info := newBuildInfo(
		serviceMain(),
		nil,
		&debug.Module{Path: "github.com/jackc/pgx/v5", Version: "v5.7.1", Sum: "h1:pgx"},
		&debug.Module{Path: "github.com/LerianStudio/lib-commons/v7", Version: "v7.4.0", Sum: "h1:commons"},
		&debug.Module{Path: "github.com/LerianStudio/lib-auth/v3", Version: "v3.1.0", Sum: "h1:auth"},
	)

	lerian := modules(info, false)
	if len(lerian) != 2 {
		t.Fatalf("lerian scope returned %d modules, want 2: %+v", len(lerian), lerian)
	}

	if lerian[0].Path != "github.com/LerianStudio/lib-auth/v3" || lerian[1].Path != "github.com/LerianStudio/lib-commons/v7" {
		t.Fatalf("lerian scope is not sorted by path: %+v", lerian)
	}

	if lerian[1].Version != "v7.4.0" || lerian[1].Sum != "h1:commons" {
		t.Fatalf("module fields not mapped: %+v", lerian[1])
	}

	full := modules(info, true)
	if len(full) != 3 || full[2].Path != "github.com/jackc/pgx/v5" {
		t.Fatalf("full scope = %+v, want 3 modules sorted by path", full)
	}
}

func TestModulesReplace(t *testing.T) {
	t.Parallel()

	info := newBuildInfo(
		serviceMain(),
		nil,
		&debug.Module{
			Path:    "github.com/LerianStudio/lib-commons/v7",
			Version: "v7.4.0",
			Sum:     "h1:commons",
			Replace: &debug.Module{Path: "../lib-commons", Version: "(devel)"},
		},
	)

	got := modules(info, false)
	if len(got) != 1 {
		t.Fatalf("got %d modules, want 1", len(got))
	}

	if got[0].Replace == nil {
		t.Fatal("Replace is nil, want the replacement module")
	}

	if got[0].Replace.Path != "../lib-commons" || got[0].Replace.Version != "(devel)" {
		t.Fatalf("Replace = %+v, want ../lib-commons (devel)", got[0].Replace)
	}
}

func TestModulesExcludesMainAndNeverReturnsNil(t *testing.T) {
	t.Parallel()

	info := newBuildInfo(debug.Module{Path: "github.com/LerianStudio/midaz", Version: "v4.0.3"}, nil)

	got := modules(info, true)
	if got == nil {
		t.Fatal("modules() returned nil, want an empty slice")
	}

	if len(got) != 0 {
		t.Fatalf("modules() = %+v, want empty (main module is excluded)", got)
	}

	if nilInfo := modules(nil, true); nilInfo == nil || len(nilInfo) != 0 {
		t.Fatalf("modules(nil) = %+v, want an empty non-nil slice", nilInfo)
	}
}

func TestModulesExportedIsNeverNil(t *testing.T) {
	t.Parallel()

	if got := Modules(false); got == nil {
		t.Fatal("Modules(false) returned nil, want an empty slice")
	}
}

func TestScopeFromDependency(t *testing.T) {
	t.Parallel()

	info := newBuildInfo(
		serviceMain(),
		nil,
		&debug.Module{Path: "github.com/LerianStudio/lib-commons/v7", Version: "v7.4.0"},
	)

	name, version := scope(info, "commons/postgres")
	if name != "github.com/LerianStudio/lib-commons/v7/commons/postgres" {
		t.Fatalf("name = %q", name)
	}

	if version != "v7.4.0" {
		t.Fatalf("version = %q, want v7.4.0", version)
	}
}

// TestScopeIgnoresOtherMajors guards the shape every Lerian service has during
// a major migration: sibling libs still pin an older lib-commons, so two or
// three majors are linked into the same binary. The scope must name the major
// that actually emitted the span, not the first one in the path-sorted list.
func TestScopeIgnoresOtherMajors(t *testing.T) {
	t.Parallel()

	info := newBuildInfo(
		serviceMain(),
		nil,
		&debug.Module{Path: "github.com/LerianStudio/lib-commons/v2", Version: "v2.0.0"},
		&debug.Module{Path: "github.com/LerianStudio/lib-commons/v5", Version: "v5.8.0"},
		&debug.Module{Path: "github.com/LerianStudio/lib-commons/v7", Version: "v7.4.0"},
	)

	name, version := scope(info, "commons/postgres")
	if name != "github.com/LerianStudio/lib-commons/v7/commons/postgres" {
		t.Fatalf("name = %q, want the v7 scope", name)
	}

	if version != "v7.4.0" {
		t.Fatalf("version = %q, want v7.4.0", version)
	}
}

func TestScopeUsesReplaceVersion(t *testing.T) {
	t.Parallel()

	info := newBuildInfo(
		serviceMain(),
		nil,
		&debug.Module{
			Path:    "github.com/LerianStudio/lib-commons/v7",
			Version: "v7.4.0",
			Replace: &debug.Module{Path: "../lib-commons", Version: "(devel)"},
		},
	)

	_, version := scope(info, "commons/redis")
	if version != "(devel)" {
		t.Fatalf("version = %q, want (devel)", version)
	}
}

func TestScopeWhenLibIsMainModule(t *testing.T) {
	t.Parallel()

	info := newBuildInfo(debug.Module{Path: "github.com/LerianStudio/lib-commons/v7", Version: ""}, nil)

	name, version := scope(info, "commons/redis")
	if name != "github.com/LerianStudio/lib-commons/v7/commons/redis" {
		t.Fatalf("name = %q", name)
	}

	if version != "(devel)" {
		t.Fatalf("version = %q, want (devel) fallback", version)
	}
}

func TestScopeWithoutBuildInfo(t *testing.T) {
	t.Parallel()

	name, version := scope(nil, "commons/postgres")
	if name != "github.com/LerianStudio/lib-commons/v7/commons/postgres" {
		t.Fatalf("name = %q", name)
	}

	if version != "(devel)" {
		t.Fatalf("version = %q, want (devel)", version)
	}
}

func TestScopeEmptyPackage(t *testing.T) {
	t.Parallel()

	name, _ := scope(nil, "")
	if name != "github.com/LerianStudio/lib-commons/v7" {
		t.Fatalf("name = %q, want the bare module path", name)
	}
}

func TestScopeFromRunningBinary(t *testing.T) {
	t.Parallel()

	name, version := Scope("commons/buildinfo")
	if name != "github.com/LerianStudio/lib-commons/v7/commons/buildinfo" {
		t.Fatalf("name = %q", name)
	}

	// lib-commons is the main module of its own test binary and the toolchain
	// stamps no version on it, so (devel) is the only reachable value here. The
	// dependency and replace cases are covered by the pure scope() table above.
	if version != "(devel)" {
		t.Fatalf("version = %q, want (devel)", version)
	}
}

// TestModulePathIsTheMainModule guards the derivation of modulePath: in its own
// tests lib-commons is the main module, so the two must agree.
func TestModulePathIsTheMainModule(t *testing.T) {
	t.Parallel()

	bi, ok := debug.ReadBuildInfo()
	if !ok {
		t.Skip("test binary carries no build information")
	}

	if modulePath != bi.Main.Path {
		t.Fatalf("modulePath = %q, main module = %q", modulePath, bi.Main.Path)
	}
}

// TestGetAppliesSet mutates the package-level injected Build, a process-global:
// no t.Parallel(), and Cleanup puts the zero value back.
func TestGetAppliesSet(t *testing.T) {
	t.Cleanup(func() { Set(Build{}) })

	Set(Build{Version: "9.9.9", Revision: "cafebabe", BuildTime: "2026-01-01T00:00:00Z"})

	got := Get()
	if got.Version != "9.9.9" || got.Revision != "cafebabe" || got.BuildTime != "2026-01-01T00:00:00Z" {
		t.Fatalf("Get() = %+v, want the values passed to Set", got)
	}

	Set(Build{})

	if after := Get(); after.Version != "dev" {
		t.Fatalf("Get().Version = %q after clearing Set, want dev", after.Version)
	}

	if got.GoVersion == "" {
		t.Fatal("GoVersion is empty")
	}
}
