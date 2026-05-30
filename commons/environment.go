package commons

import (
	"errors"
	"os"
	"strings"
	"sync"
)

// Environment represents a deployment environment.
// Five named environments map to deployment topologies; the previous
// SecurityTier coupling has been removed (see commons/security.go for the
// boolean toggle model that replaced it).
type Environment string

const (
	// Production identifies the production deployment environment.
	Production Environment = "production"
	// Staging identifies the staging deployment environment.
	Staging Environment = "staging"
	// UAT identifies the user-acceptance-testing deployment environment.
	UAT Environment = "uat"
	// Development identifies the development deployment environment.
	Development Environment = "development"
	// Local identifies the local-developer deployment environment.
	Local Environment = "local"
)

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

// ErrEnvironmentAlreadySet is returned by SetEnvironment when the environment has already been configured.
var ErrEnvironmentAlreadySet = errors.New("environment: already set; call SetEnvironment() only once during startup")

// ErrInvalidEnvironment is returned by SetEnvironment when the environment value is not recognized.
var ErrInvalidEnvironment = errors.New("environment: invalid value; must be one of: production, staging, uat, development, local")

var (
	currentEnv Environment
	envMu      sync.RWMutex
	envSet     bool

	detectedEnvCache detectedEnvironmentStateCache
)

type environmentInputs struct {
	envName string
	env     string
	goEnv   string
}

type detectedEnvironmentStateCache struct {
	inputs environmentInputs
	env    Environment
	valid  bool
}

// DetectEnvironment reads ENV_NAME, then ENV, then GO_ENV (in priority order)
// and returns the corresponding Environment. If no environment variable is set,
// it returns Local.
//
// This function does NOT call Set() — it only reads and returns.
// Use it to inspect the environment before committing.
func DetectEnvironment() Environment {
	return detectEnvironmentFromInputs(readEnvironmentInputs())
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
	detectedEnvCache = detectedEnvironmentStateCache{}

	return nil
}

// CurrentEnvironment returns the configured environment.
// If SetEnvironment has not been called, it falls back to DetectEnvironment and
// memoizes the last detected result for the current env var inputs.
func CurrentEnvironment() Environment {
	envMu.RLock()

	if envSet {
		env := currentEnv

		envMu.RUnlock()

		return env
	}

	cached := detectedEnvCache

	envMu.RUnlock()

	inputs := readEnvironmentInputs()
	if cached.valid && cached.inputs == inputs {
		return cached.env
	}

	detected := detectEnvironmentFromInputs(inputs)

	envMu.Lock()
	defer envMu.Unlock()

	if envSet {
		return currentEnv
	}

	detectedEnvCache = detectedEnvironmentStateCache{
		inputs: inputs,
		env:    detected,
		valid:  true,
	}

	return detected
}

func readEnvironmentInputs() environmentInputs {
	return environmentInputs{
		envName: os.Getenv("ENV_NAME"),
		env:     os.Getenv("ENV"),
		goEnv:   os.Getenv("GO_ENV"),
	}
}

func detectEnvironmentFromInputs(inputs environmentInputs) Environment {
	for _, raw := range []string{inputs.envName, inputs.env, inputs.goEnv} {
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" {
			continue
		}

		env := Environment(strings.ToLower(trimmed))
		if env.IsValid() {
			return env
		}

		// A non-empty but unrecognized value is returned as-is; the previous
		// fail-safe coupling to TierStrict has moved to per-toggle boolean
		// checks in commons/security.go.
		return env
	}

	return Local
}
