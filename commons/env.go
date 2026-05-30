package commons

import (
	"fmt"
	"os"
	"strings"
)

// Environment identifiers accepted by CurrentEnv. Keep this list aligned with
// downstream services' deploy manifests.
const (
	EnvStaging    = "staging"
	EnvProduction = "production"
)

// CurrentEnv reads the runtime environment from ENVIRONMENT_NAME (preferred)
// or ENV_NAME (legacy fallback). The value is normalised to lowercase and
// trimmed. Returns an error if the variable is unset, empty, or not one of
// the accepted values (staging, production).
//
// Callers MUST handle the error — env mis-configuration should fail-fast at
// boot, never silently default.
func CurrentEnv() (string, error) {
	raw := os.Getenv("ENVIRONMENT_NAME")
	if raw == "" {
		raw = os.Getenv("ENV_NAME")
	}

	env := strings.ToLower(strings.TrimSpace(raw))
	switch env {
	case EnvStaging, EnvProduction:
		return env, nil
	case "":
		return "", fmt.Errorf("ENVIRONMENT_NAME (or ENV_NAME) is required: must be %q or %q", EnvStaging, EnvProduction)
	default:
		return "", fmt.Errorf("invalid environment %q: must be %q or %q", env, EnvStaging, EnvProduction)
	}
}

// MustCurrentEnv is CurrentEnv that panics on error. Intended for bootstrap
// where a misconfigured env is unrecoverable. Prefer CurrentEnv in library
// code; reserve MustCurrentEnv for application main packages.
func MustCurrentEnv() string {
	env, err := CurrentEnv()
	if err != nil {
		panic(err)
	}

	return env
}
