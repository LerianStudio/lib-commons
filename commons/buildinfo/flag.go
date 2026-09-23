package buildinfo

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// versionFlag is the single spelling of the identity flag. FC-5 of the runtime
// version identity plan verifies every CI image with "docker run <img>
// --version", so there is no short form and no "version" subcommand.
const versionFlag = "--version"

// HandleFlag prints the compiled identity as JSON and exits with 0 when the
// binary was invoked as "<binary> --version". Any other argument list returns
// and lets the process boot normally. Call it at the top of main, right after
// Set, so the identity is answerable before configuration is loaded or any
// connection is opened.
func HandleFlag() {
	if len(os.Args) < 2 || os.Args[1] != versionFlag {
		return
	}

	if err := writeVersion(os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	os.Exit(0)
}

const (
	manifestFormat = "go-buildinfo-v1"
	scopeLerian    = "lerian"
)

// flagBody is the line printed by --version: the identity plus the manifest of
// the Lerian modules linked into the binary, and no service name.
type flagBody struct {
	SchemaVersion string `json:"schemaVersion"`
	identity
	DependencyManifest manifest `json:"dependencyManifest"`
}

// manifest lists the modules linked into the binary.
type manifest struct {
	Format  string   `json:"format"`
	Scope   string   `json:"scope"`
	Modules []Module `json:"modules"`
}

// writeVersion encodes the identity of the running process, Lerian module
// scope, without the service name: a binary knows what it was built from, not
// what it was configured to be called.
func writeVersion(w io.Writer) error {
	return json.NewEncoder(w).Encode(flagBody{
		SchemaVersion: schemaVersion,
		identity:      identity(Get()),
		DependencyManifest: manifest{
			Format:  manifestFormat,
			Scope:   scopeLerian,
			Modules: Modules(false),
		},
	})
}
