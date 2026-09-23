package buildinfo

import "github.com/gofiber/fiber/v3"

const (
	schemaVersion  = "v1"
	manifestFormat = "go-buildinfo-v1"
	scopeLerian    = "lerian"
	scopeFull      = "full"
)

// response is the body served by GET /version and printed by --version.
type response struct {
	SchemaVersion string `json:"schemaVersion"`
	// Service is absent from the --version output and present on the
	// endpoint, empty string included.
	Service            *string  `json:"service,omitempty"`
	Version            string   `json:"version"`
	Revision           string   `json:"revision"`
	BuildTime          string   `json:"buildTime"`
	Modified           bool     `json:"modified"`
	GoVersion          string   `json:"goVersion"`
	DependencyManifest manifest `json:"dependencyManifest"`
}

// manifest lists the modules linked into the binary.
type manifest struct {
	Format  string   `json:"format"`
	Scope   string   `json:"scope"`
	Modules []Module `json:"modules"`
}

// Handler answers GET /version with the identity of the running process and
// the manifest of the Lerian modules linked into it. "?full=1" (or
// "?full=true") widens the manifest to every module. The route has no
// authentication of its own: serve it on the admin port.
func Handler(service string) fiber.Handler {
	return func(c fiber.Ctx) error {
		return c.JSON(newResponse(&service, fullScope(c.Query("full"))))
	}
}

func newResponse(service *string, full bool) response {
	info := Get()

	name := scopeLerian
	if full {
		name = scopeFull
	}

	return response{
		SchemaVersion: schemaVersion,
		Service:       service,
		Version:       info.Version,
		Revision:      info.Revision,
		BuildTime:     info.BuildTime,
		Modified:      info.Modified,
		GoVersion:     info.GoVersion,
		DependencyManifest: manifest{
			Format:  manifestFormat,
			Scope:   name,
			Modules: Modules(full),
		},
	}
}

func fullScope(query string) bool {
	return query == "1" || query == "true"
}
