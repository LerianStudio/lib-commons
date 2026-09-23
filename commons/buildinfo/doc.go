// Package buildinfo exposes the identity a binary was compiled with: version,
// git revision, build time, Go version and the manifest of the modules linked
// into it.
//
// The three mutable values come from the linker, never from the environment.
// Each binary declares them and hands them over once, at the top of main:
//
//	// Filled at build time with -ldflags "-X main.version=... -X main.revision=... -X main.buildTime=...".
//	var version, revision, buildTime string
//
//	func main() {
//		buildinfo.Set(buildinfo.Build{Version: version, Revision: revision, BuildTime: buildTime})
//		buildinfo.HandleFlag() // "--version" prints the identity as JSON and exits 0
//
//		// ... bootstrap
//	}
//
// Everything else is read from runtime/debug. Per field, an injected value
// wins; otherwise the VCS stamp the toolchain recorded is used; only then the
// "dev" and "unknown" fallbacks. A version is never taken from VCS.
package buildinfo
