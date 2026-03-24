package commons

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/LerianStudio/lib-commons/v4/commons/internal/nilcheck"
	"github.com/LerianStudio/lib-commons/v4/commons/log"
)

// SecurityOverride represents an explicit relaxation of a security rule.
// It is read from environment variables following the ALLOW_<RULE>="reason" pattern.
type SecurityOverride struct {
	// Rule is the security rule being relaxed (e.g. "tls_required", "cors_wildcard_origin").
	Rule string

	// Reason is the justification for the override. Read from the env var value.
	// Must be non-empty in Moderate and Strict tiers.
	Reason string

	// Source indicates where the override came from ("env" or "code").
	Source string
}

// Well-known override env var names.
const (
	EnvAllowInsecureTLS       = "ALLOW_INSECURE_TLS"
	EnvAllowCORSWildcard      = "ALLOW_CORS_WILDCARD"
	EnvAllowRateLimitDisabled = "ALLOW_RATELIMIT_DISABLED"
	EnvAllowRateLimitFailOpen = "ALLOW_RATELIMIT_FAIL_OPEN"
	EnvAllowInsecureOTEL      = "ALLOW_INSECURE_OTEL"
	EnvSecurityEnforcement    = "SECURITY_ENFORCEMENT"
)

// Well-known rule names (used in SecurityOverride.Rule and metrics labels).
const (
	RuleTLSRequired          = "tls_required"
	RuleCORSWildcardOrigin   = "cors_wildcard_origin"
	RuleRateLimitDisabled    = "ratelimit_disabled"
	RuleRateLimitFailOpen    = "ratelimit_fail_open"
	RuleOTELInsecureExporter = "otel_insecure_exporter"
)

// Errors returned by security policy checks.
var (
	// ErrSecurityViolation is returned when a security rule is violated and no override is present.
	ErrSecurityViolation = errors.New("security policy violation")

	// ErrOverrideReasonRequired is returned when an override env var is set but its value is empty.
	ErrOverrideReasonRequired = errors.New("security override reason is required (env var value must not be empty)")
)

// envVarForRule maps rule names to their corresponding ALLOW_* env var.
var envVarForRule = map[string]string{
	RuleTLSRequired:          EnvAllowInsecureTLS,
	RuleCORSWildcardOrigin:   EnvAllowCORSWildcard,
	RuleRateLimitDisabled:    EnvAllowRateLimitDisabled,
	RuleRateLimitFailOpen:    EnvAllowRateLimitFailOpen,
	RuleOTELInsecureExporter: EnvAllowInsecureOTEL,
}

// SecurityEnvVarForRule returns the ALLOW_* environment variable name for a given rule.
// Returns empty string if the rule is not recognized.
func SecurityEnvVarForRule(rule string) string {
	return envVarForRule[rule]
}

// IsSecurityEnforcementEnabled returns true if SECURITY_ENFORCEMENT is set to "true".
// This controls the Phase 2→3 transition: when false, violations produce
// warnings only. When true, violations return errors from constructors.
func IsSecurityEnforcementEnabled() bool {
	return GetenvBoolOrDefault(EnvSecurityEnforcement, false)
}

// SecurityCheckResult holds the outcome of a security rule check.
type SecurityCheckResult struct {
	// Rule is the name of the security rule being checked.
	Rule string

	// Violated is true when the rule's condition is not met.
	Violated bool

	// Override is non-nil when an ALLOW_* env var provides an override.
	Override *SecurityOverride

	// EnvVar is the name of the ALLOW_* env var for this rule (for error messages).
	EnvVar string
}

// ReadSecurityOverride reads the ALLOW_* env var for the given rule and returns
// a SecurityOverride if the env var is set and non-empty, or nil if absent/empty.
func ReadSecurityOverride(rule string) *SecurityOverride {
	envVar, ok := envVarForRule[rule]
	if !ok {
		return nil
	}

	reason := strings.TrimSpace(GetenvOrDefault(envVar, ""))
	if reason == "" {
		return nil
	}

	return &SecurityOverride{
		Rule:   rule,
		Reason: reason,
		Source: "env",
	}
}

// CheckSecurityRule evaluates a security rule against the current state.
// The violated parameter indicates whether the rule's condition is currently unmet.
//
// Returns a SecurityCheckResult describing the violation and any override.
// This function does NOT log or return errors — it only evaluates state.
func CheckSecurityRule(rule string, violated bool) SecurityCheckResult {
	envVar := envVarForRule[rule]
	if !violated {
		return SecurityCheckResult{Rule: rule, Violated: false, EnvVar: envVar}
	}

	override := ReadSecurityOverride(rule)

	return SecurityCheckResult{
		Rule:     rule,
		Violated: true,
		Override: override,
		EnvVar:   envVar,
	}
}

// EnforceSecurityRule processes a SecurityCheckResult according to the current
// security tier and enforcement mode. Returns an error if the violation should
// block construction.
//
// Behavior matrix:
//
//	Permissive: log INFO, never return error
//	Moderate:   log WARN, return error only if enforcement enabled and no override
//	Strict:     log ERROR, return error only if enforcement enabled and no override
//
// When an override is present with a valid reason, the log is emitted but
// no error is returned (the override suppresses the error).
func EnforceSecurityRule(ctx context.Context, logger log.Logger, component string, result SecurityCheckResult) error {
	env, tier, tierSource := currentSecurityContext()

	return enforceSecurityRule(ctx, logger, component, env, tier, tierSource, result)
}

// EnforceSecurityRuleForEnvironment processes a SecurityCheckResult using the
// provided environment instead of the ambient process environment for
// environment classification. If SECURITY_TIER is set, that process-wide tier
// override still applies.
func EnforceSecurityRuleForEnvironment(ctx context.Context, logger log.Logger, component string, env Environment, result SecurityCheckResult) error {
	_, tier, tierSource := securityContextForEnvironment(env)

	return enforceSecurityRule(ctx, logger, component, env, tier, tierSource, result)
}

func enforceSecurityRule(
	ctx context.Context,
	logger log.Logger,
	component string,
	env Environment,
	tier SecurityTier,
	tierSource string,
	result SecurityCheckResult,
) error {
	if !result.Violated {
		return nil
	}

	logger = normalizeSecurityLogger(logger)

	fields := []log.Field{
		log.String("component", component),
		log.String("environment", env.String()),
		log.String("tier", tier.String()),
	}
	if tierSource != "" {
		fields = append(fields, log.String("tier_source", tierSource))
	}

	// Override present — log it and allow through.
	if result.Override != nil {
		// Validate reason in non-permissive tiers.
		if tier > TierPermissive && strings.TrimSpace(result.Override.Reason) == "" {
			return fmt.Errorf("%w: %s requires a non-empty reason in %s tier; set %s=\"your reason\"",
				ErrOverrideReasonRequired, result.Override.Rule, tier, result.EnvVar)
		}

		level := overrideLogLevel(tier)

		overrideFields := append([]log.Field{}, fields...)
		overrideFields = append(overrideFields,
			log.String("rule", result.Override.Rule),
			log.Bool("reason_present", strings.TrimSpace(result.Override.Reason) != ""),
			log.String("source", result.Override.Source),
		)
		logger.Log(ctx, level, "security override active", overrideFields...)

		return nil // override suppresses the error
	}

	// No override — this is a real violation.
	level := violationLogLevel(tier)

	violationFields := append([]log.Field{}, fields...)
	violationFields = append(violationFields,
		log.String("rule", result.Rule),
		log.String("override_env_var", result.EnvVar),
	)
	logger.Log(ctx, level, "security policy violation", violationFields...)

	// In permissive tier, violations are just warnings.
	if tier == TierPermissive {
		return nil
	}

	// In moderate/strict tiers, respect enforcement flag.
	if !IsSecurityEnforcementEnabled() {
		return nil // Phase 2: warn only
	}

	contextLabel := env.String()
	if tierSource != "" {
		contextLabel = fmt.Sprintf("%s; overridden by %s", contextLabel, tierSource)
	}

	return fmt.Errorf("%w: %s not met in %s tier (%s); set %s=\"reason\" to override",
		ErrSecurityViolation, result.Rule, tier, contextLabel, result.EnvVar)
}

func normalizeSecurityLogger(logger log.Logger) log.Logger {
	if nilcheck.Interface(logger) {
		return log.NewNop()
	}

	return logger
}

// overrideLogLevel returns the log level for override messages per tier.
// NOTE: Currently identical to violationLogLevel. Kept separate because
// a future phase may downgrade override log levels (overrides are intentional
// operator decisions; violations are unintentional gaps).
func overrideLogLevel(tier SecurityTier) log.Level {
	switch tier {
	case TierStrict:
		return log.LevelError
	case TierModerate:
		return log.LevelWarn
	default:
		return log.LevelInfo
	}
}

// violationLogLevel returns the log level for unresolved violation messages per tier.
// NOTE: Currently identical to overrideLogLevel. Kept separate for future
// divergence — violations may warrant stricter alerting than overrides.
func violationLogLevel(tier SecurityTier) log.Level {
	switch tier {
	case TierStrict:
		return log.LevelError
	case TierModerate:
		return log.LevelWarn
	default:
		return log.LevelInfo
	}
}
