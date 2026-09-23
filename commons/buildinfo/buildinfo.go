package buildinfo

import "github.com/LerianStudio/lib-commons/v7/commons/internal/buildid"

// Build carries the values injected at link time with -X main.<field>.
type Build struct {
	// Version is the release version, e.g. "4.0.3". Empty falls back to "dev":
	// the version never comes from VCS.
	Version string
	// Revision is the commit the binary was built from. Empty falls back to the
	// toolchain's vcs.revision stamp, then to "unknown".
	Revision string
	// BuildTime is the RFC 3339 build timestamp. Empty falls back to the
	// toolchain's vcs.time stamp, then to "unknown".
	BuildTime string
}

// Info is the compiled identity of the running process.
type Info struct {
	// Version is the injected release version, or "dev".
	Version string
	// Revision is the injected commit, the vcs.revision stamp, or "unknown".
	Revision string
	// BuildTime is the injected timestamp, the vcs.time stamp, or "unknown".
	BuildTime string
	// Modified reports the toolchain's vcs.modified stamp: the binary was built
	// from a dirty working tree.
	Modified bool
	// GoVersion is the Go toolchain the binary was built with.
	GoVersion string
}

// Module is one entry of the dependency manifest.
type Module struct {
	// Path is the module path, e.g. "github.com/LerianStudio/lib-commons/v7".
	Path string `json:"path"`
	// Version is the module version as resolved by the build.
	Version string `json:"version"`
	// Sum is the go.sum checksum of the module; empty when the build recorded
	// none, as for a module replaced by a local directory.
	Sum string `json:"sum,omitempty"`
	// Replace is the module this one was replaced with, nil unless replaced.
	// Only its Path and Version are set.
	Replace *Module `json:"replace,omitempty"`
}

// Set records the values injected at link time. Call it once, at the top of
// main, before anything reads the identity. Empty fields keep their fallback.
func Set(b Build) { buildid.Set(buildid.Build(b)) }

// Get returns the identity of the running process.
func Get() Info { return Info(buildid.Get()) }

// Modules returns the modules linked into the binary, sorted by path and
// excluding the main module. full=false keeps only github.com/LerianStudio/
// modules. The result is never nil.
func Modules(full bool) []Module { return fromCore(buildid.Modules(full)) }

// fromCore converts the core's manifest to the public type. Module refers to
// itself through Replace, so a plain type conversion is not legal.
func fromCore(in []buildid.Module) []Module {
	out := make([]Module, len(in))

	for i, m := range in {
		out[i] = Module{Path: m.Path, Version: m.Version, Sum: m.Sum}

		if m.Replace != nil {
			out[i].Replace = &Module{Path: m.Replace.Path, Version: m.Replace.Version, Sum: m.Replace.Sum}
		}
	}

	return out
}

// Scope returns the OpenTelemetry instrumentation scope for a package of this
// library: the module path of lib-commons as linked into the running binary,
// joined with pkg, and the version of that module ("(devel)" when unknown).
func Scope(pkg string) (name, version string) { return buildid.Scope(pkg) }
