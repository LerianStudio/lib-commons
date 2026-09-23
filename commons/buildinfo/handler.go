package buildinfo

import "github.com/gofiber/fiber/v3"

const schemaVersion = "v1"

// identity is the part of the build identity shared by GET /version and
// --version. Embedded, its fields render flat in both bodies. It mirrors Info
// field for field, so a field added to Info fails to compile here until the
// contract decides where it goes.
type identity struct {
	Version   string `json:"version"`
	Revision  string `json:"revision"`
	BuildTime string `json:"buildTime"`
	Modified  bool   `json:"modified"`
	GoVersion string `json:"goVersion"`
}

// endpointBody is the body served by GET /version. It carries no dependency
// manifest: the route answers on the service's API port, so which modules a
// binary links stays inside the process and is read through --version.
type endpointBody struct {
	SchemaVersion string `json:"schemaVersion"`
	Service       string `json:"service"`
	identity
}

// Handler answers GET /version with the identity of the running process:
// schemaVersion, service, version, revision, buildTime, modified and
// goVersion. The dependency manifest is available only through --version; in
// a cluster, run "kubectl exec <pod> -- /service --version".
func Handler(service string) fiber.Handler {
	return func(c fiber.Ctx) error {
		return c.JSON(endpointBody{
			SchemaVersion: schemaVersion,
			Service:       service,
			identity:      identity(Get()),
		})
	}
}
