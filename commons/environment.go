package commons

import (
	"errors"
	"os"
	"strings"
	"sync"
)

// Environment represents a deployment environment with an associated security tier.
// Five named environments map to three security postures.
type Environment string

const (
	// Production enables the strictest security defaults.
	Production Environment = "production"
	// Staging enables moderate security defaults.
	Staging Environment = "staging"
	// UAT enables moderate security defaults (same posture as Staging).
	UAT Environment = "uat"
	// Development enables permissive security defaults.
	Development Environment = "development"
	// Local enables permissive security defaults (same posture as Development).
	Local Environment = "local"
)

// SecurityTier represents the security posture associated with an environment.
// Higher values mean stricter enforcement.
type SecurityTier int

const (
	// TierPermissive allows all operations without enforcement.
	// Applies to: Local, Development.
	TierPermissive SecurityTier = iota

	// TierModerate enables security validation with warnings.
	// Overrides are logged at WARN level.
	// Applies to: Staging, UAT.
	TierModerate

	// TierStrict enables full security enforcement.
	// Violations return errors from constructors unless overridden.
	// Overrides are logged at ERROR level with OTEL metrics.
	// Applies to: Production.
	TierStrict
)

// String returns the human-readable name of the security tier.
func (t SecurityTier) String() string {
	switch t {
	case TierPermissive:
		return "permissive"
	case TierModerate:
		return "moderate"
	case TierStrict:
		return "strict"
	default:
		return "unknown"
	}
}

// SecurityTier returns the security posture for this environment.
// Unknown environment values are treated as TierStrict to fail safe.
func (e Environment) SecurityTier() SecurityTier {
	switch e {
	case Local, Development:
		return TierPermissive
	case Staging, UAT:
		return TierModerate
	case Production:
		return TierStrict
	default:
		// Unknown environments default to strict — fail safe, not fail open.
		return TierStrict
	}
}

// IsValid reports whether e is one of the five recognized environments.
func (e Environment) IsValid() bool {
	switch e {
	case Production, Staging, UAT, Development, Local:
		return true
	default:
		return false
	}
}

// String returns the environment name as a plain string.
func (e Environment) String() string {
	return string(e)
}

// ErrEnvironmentAlreadySet is returned by Set when the environment has already been configured.
var ErrEnvironmentAlreadySet = errors.New("environment: already set; call Set() only once during startup")

// ErrInvalidEnvironment is returned by Set when the environment value is not recognized.
var ErrInvalidEnvironment = errors.New("environment: invalid value; must be one of: production, staging, uat, development, local")

var (
	currentEnv Environment
	envMu      sync.RWMutex
	envSet     bool
)

// DetectEnvironment reads ENV_NAME, then ENV, then GO_ENV (in priority order)
// and returns the corresponding Environment. If no environment variable is set,
// it returns Local.
//
// This function does NOT call Set() — it only reads and returns.
// Use it to inspect the environment before committing.
func DetectEnvironment() Environment {
	for _, key := range []string{"ENV_NAME", "ENV", "GO_ENV"} {
		raw := strings.TrimSpace(os.Getenv(key))
		if raw == "" {
			continue
		}

		env := Environment(strings.ToLower(raw))
		if env.IsValid() {
			return env
		}

		// A non-empty but unrecognized value is treated as strict via
		// Environment.SecurityTier() to fail safe instead of silently falling back
		// to a permissive local environment.
		return env
	}

	return Local
}

// SetEnvironment configures the canonical environment for the process.
// It must be called at most once during startup. Subsequent calls return
// ErrEnvironmentAlreadySet. It never panics.
//
// If env is not a recognized value, ErrInvalidEnvironment is returned
// and the environment remains unset.
func SetEnvironment(env Environment) error {
	if !env.IsValid() {
		return ErrInvalidEnvironment
	}

	envMu.Lock()
	defer envMu.Unlock()

	if envSet {
		return ErrEnvironmentAlreadySet
	}

	currentEnv = env
	envSet = true

	return nil
}

// CurrentEnvironment returns the configured environment.
// If SetEnvironment has not been called, it falls back to DetectEnvironment.
func CurrentEnvironment() Environment {
	envMu.RLock()
	defer envMu.RUnlock()

	if envSet {
		return currentEnv
	}

	// Not explicitly set — detect from env vars.
	// This is intentionally NOT cached so env var changes before Set() are visible.
	return DetectEnvironment()
}

// CurrentTier returns the SecurityTier for the current environment.
// Convenience wrapper around CurrentEnvironment().SecurityTier().
func CurrentTier() SecurityTier {
	return CurrentEnvironment().SecurityTier()
}
