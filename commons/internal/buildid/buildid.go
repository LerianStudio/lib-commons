// Package buildid is the dependency-free core of the compiled identity: the
// precedence rules, the module manifest and the instrumentation scope. It
// exists so packages that only need the scope do not pull in the HTTP handler
// of commons/buildinfo, and with it the whole Fiber tree.
package buildid

import (
	"reflect"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"sync"
)

// modulePath is the import path of this module. It is the instrumentation
// scope prefix used when the running binary carries no build information.
//
// Derived from this package's own import path rather than written out, so the
// next major bump cannot leave every scope pointing at the previous major.
var modulePath = strings.TrimSuffix(reflect.TypeFor[Build]().PkgPath(), "/commons/internal/buildid")

const (
	// lerianPrefix selects the Lerian modules of the dependency manifest.
	lerianPrefix = "github.com/LerianStudio/"

	devVersion     = "dev"
	unknownValue   = "unknown"
	develVersion   = "(devel)"
	settingRev     = "vcs.revision"
	settingTime    = "vcs.time"
	settingChanged = "vcs.modified"
)

// Build carries the values injected at link time with -X main.<field>.
type Build struct {
	Version   string
	Revision  string
	BuildTime string
}

// Info is the compiled identity of the running process.
type Info struct {
	Version   string
	Revision  string
	BuildTime string
	Modified  bool
	GoVersion string
}

// Module is one entry of the dependency manifest.
type Module struct {
	Path    string  `json:"path"`
	Version string  `json:"version"`
	Sum     string  `json:"sum,omitempty"`
	Replace *Module `json:"replace,omitempty"` // nil unless the module was replaced
}

var (
	mu       sync.RWMutex
	injected Build

	readOnce  sync.Once
	buildInfo *debug.BuildInfo
)

// Set records the values injected at link time. Call it once, at the top of
// main, before anything reads the identity. Empty fields keep their fallback.
func Set(b Build) {
	mu.Lock()
	defer mu.Unlock()

	injected = b
}

// Get returns the identity of the running process.
func Get() Info {
	return compute(readBuildInfo(), current())
}

// Modules returns the modules linked into the binary, sorted by path and
// excluding the main module. full=false keeps only github.com/LerianStudio/
// modules. The result is never nil.
func Modules(full bool) []Module {
	return modules(readBuildInfo(), full)
}

// Scope returns the OpenTelemetry instrumentation scope for a package of this
// library: the module path of lib-commons as linked into the running binary,
// joined with pkg, and the version of that module ("(devel)" when unknown).
func Scope(pkg string) (name, version string) {
	return scope(readBuildInfo(), pkg)
}

func current() Build {
	mu.RLock()
	defer mu.RUnlock()

	return injected
}

func readBuildInfo() *debug.BuildInfo {
	readOnce.Do(func() {
		if bi, ok := debug.ReadBuildInfo(); ok {
			buildInfo = bi
		}
	})

	return buildInfo
}

// compute applies the precedence rules to a build info table and the injected
// values. It is pure so the rules can be tested against synthetic tables.
func compute(bi *debug.BuildInfo, b Build) Info {
	info := Info{
		Version:   devVersion,
		Revision:  unknownValue,
		BuildTime: unknownValue,
		GoVersion: runtime.Version(),
	}

	if bi != nil {
		if bi.GoVersion != "" {
			info.GoVersion = bi.GoVersion
		}

		for _, setting := range bi.Settings {
			switch setting.Key {
			case settingRev:
				if setting.Value != "" {
					info.Revision = setting.Value
				}
			case settingTime:
				if setting.Value != "" {
					info.BuildTime = setting.Value
				}
			case settingChanged:
				info.Modified = setting.Value == "true"
			}
		}
	}

	if b.Version != "" {
		info.Version = b.Version
	}

	if b.Revision != "" {
		info.Revision = b.Revision
	}

	if b.BuildTime != "" {
		info.BuildTime = b.BuildTime
	}

	return info
}

func modules(bi *debug.BuildInfo, full bool) []Module {
	if bi == nil {
		return []Module{}
	}

	out := make([]Module, 0, len(bi.Deps))

	for _, dep := range bi.Deps {
		if dep == nil || (!full && !strings.HasPrefix(dep.Path, lerianPrefix)) {
			continue
		}

		m := Module{Path: dep.Path, Version: dep.Version, Sum: dep.Sum}

		if dep.Replace != nil {
			m.Replace = &Module{Path: dep.Replace.Path, Version: dep.Replace.Version}
		}

		out = append(out, m)
	}

	slices.SortFunc(out, func(a, b Module) int {
		return strings.Compare(a.Path, b.Path)
	})

	return out
}

func scope(bi *debug.BuildInfo, pkg string) (name, version string) {
	path, version := modulePath, develVersion

	if bi != nil {
		if bi.Main.Path == modulePath {
			path, version = bi.Main.Path, bi.Main.Version
		}

		for _, dep := range bi.Deps {
			if dep == nil || dep.Path != modulePath {
				continue
			}

			path, version = dep.Path, dep.Version

			if dep.Replace != nil && dep.Replace.Version != "" {
				version = dep.Replace.Version
			}

			break
		}
	}

	if version == "" {
		version = develVersion
	}

	if pkg == "" {
		return path, version
	}

	return path + "/" + pkg, version
}
