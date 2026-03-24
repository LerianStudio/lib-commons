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

// ErrEnvironmentAlreadySet is returned by SetEnvironment when the environment has already been configured.
var ErrEnvironmentAlreadySet = errors.New("environment: already set; call SetEnvironment() only once during startup")

// ErrInvalidEnvironment is returned by SetEnvironment when the environment value is not recognized.
var ErrInvalidEnvironment = errors.New("environment: invalid value; must be one of: production, staging, uat, development, local")

var (
	currentEnv Environment
	envMu      sync.RWMutex
	envSet     bool

	detectedEnvCache          detectedEnvironmentStateCache
	detectedTierOverrideCache tierOverrideStateCache
)

// EnvSecurityTier optionally overrides the security posture derived from the
// deployment environment. Supported values are: permissive, moderate, strict.
// Invalid non-empty values fail safe to strict.
const EnvSecurityTier = "SECURITY_TIER"

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

type tierOverrideStateCache struct {
	raw     string
	tier    SecurityTier
	present bool
	valid   bool
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

// EffectiveSecurityTier returns the security tier for env after applying the
// optional SECURITY_TIER override.
func EffectiveSecurityTier(env Environment) SecurityTier {
	if tier, ok := currentSecurityTierOverride(); ok {
		return tier
	}

	return env.SecurityTier()
}

// CurrentTier returns the SecurityTier for the current process, applying the
// optional SECURITY_TIER override when present.
func CurrentTier() SecurityTier {
	return EffectiveSecurityTier(CurrentEnvironment())
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

		// A non-empty but unrecognized value is treated as strict via
		// Environment.SecurityTier() to fail safe instead of silently falling back
		// to a permissive local environment.
		return env
	}

	return Local
}

func parseSecurityTierOverride(raw string) (SecurityTier, bool) {
	trimmed := strings.ToLower(strings.TrimSpace(raw))
	if trimmed == "" {
		return 0, false
	}

	switch trimmed {
	case TierPermissive.String():
		return TierPermissive, true
	case TierModerate.String():
		return TierModerate, true
	case TierStrict.String():
		return TierStrict, true
	default:
		return TierStrict, true
	}
}

func currentSecurityTierOverride() (SecurityTier, bool) {
	raw := os.Getenv(EnvSecurityTier)

	envMu.RLock()

	if detectedTierOverrideCache.valid && detectedTierOverrideCache.raw == raw {
		tier := detectedTierOverrideCache.tier
		present := detectedTierOverrideCache.present

		envMu.RUnlock()

		return tier, present
	}

	envMu.RUnlock()

	tier, present := parseSecurityTierOverride(raw)

	envMu.Lock()
	detectedTierOverrideCache = tierOverrideStateCache{
		raw:     raw,
		tier:    tier,
		present: present,
		valid:   true,
	}
	envMu.Unlock()

	return tier, present
}

func currentSecurityContext() (Environment, SecurityTier, string) {
	env := CurrentEnvironment()

	return securityContextForEnvironment(env)
}

func securityContextForEnvironment(env Environment) (Environment, SecurityTier, string) {
	if tier, ok := currentSecurityTierOverride(); ok {
		return env, tier, EnvSecurityTier
	}

	return env, env.SecurityTier(), ""
}
