//go:build unit

package commons

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/LerianStudio/lib-commons/v4/commons/log"
	"github.com/stretchr/testify/require"
)

type securityLoggerSpy struct {
	entries []securityLogEntry
}

type securityLogEntry struct {
	level  log.Level
	msg    string
	fields []log.Field
}

func unsetEnvForTest(t *testing.T, key string) {
	t.Helper()

	original, present := os.LookupEnv(key)
	require.NoError(t, os.Unsetenv(key))
	t.Cleanup(func() {
		if present {
			require.NoError(t, os.Setenv(key, original))
			return
		}

		require.NoError(t, os.Unsetenv(key))
	})
}

func (s *securityLoggerSpy) Log(_ context.Context, level log.Level, msg string, fields ...log.Field) {
	s.entries = append(s.entries, securityLogEntry{level: level, msg: msg, fields: append([]log.Field(nil), fields...)})
}

func (s *securityLoggerSpy) With(...log.Field) log.Logger { return s }
func (s *securityLoggerSpy) WithGroup(string) log.Logger  { return s }
func (s *securityLoggerSpy) Enabled(log.Level) bool       { return true }
func (s *securityLoggerSpy) Sync(context.Context) error   { return nil }

func TestReadSecurityOverride_Present(t *testing.T) {
	t.Setenv(EnvAllowInsecureTLS, "Istio mTLS at sidecar")

	override := ReadSecurityOverride(RuleTLSRequired)
	if override == nil {
		t.Fatal("ReadSecurityOverride returned nil, expected override")
	}

	if override.Rule != RuleTLSRequired {
		t.Errorf("override.Rule = %q, want %q", override.Rule, RuleTLSRequired)
	}

	if override.Reason != "Istio mTLS at sidecar" {
		t.Errorf("override.Reason = %q, want %q", override.Reason, "Istio mTLS at sidecar")
	}

	if override.Source != "env" {
		t.Errorf("override.Source = %q, want %q", override.Source, "env")
	}
}

func TestReadSecurityOverride_Absent(t *testing.T) {
	unsetEnvForTest(t, EnvAllowInsecureTLS)

	override := ReadSecurityOverride(RuleTLSRequired)
	if override != nil {
		t.Errorf("ReadSecurityOverride returned %+v, expected nil", override)
	}
}

func TestReadSecurityOverride_WhitespaceOnly(t *testing.T) {
	t.Setenv(EnvAllowInsecureTLS, "   ")

	override := ReadSecurityOverride(RuleTLSRequired)
	if override == nil {
		t.Fatal("ReadSecurityOverride returned nil for whitespace-only value, expected override with empty reason")
	}

	if override.Reason != "" {
		t.Errorf("override.Reason = %q, want empty string", override.Reason)
	}
}

func TestReadSecurityOverride_UnknownRule(t *testing.T) {
	override := ReadSecurityOverride("nonexistent_rule")
	if override != nil {
		t.Errorf("ReadSecurityOverride returned %+v for unknown rule, expected nil", override)
	}
}

func TestCheckSecurityRule_NotViolated(t *testing.T) {
	result := CheckSecurityRule(RuleTLSRequired, false)
	if result.Violated {
		t.Error("CheckSecurityRule with violated=false returned Violated=true")
	}

	if result.Override != nil {
		t.Error("CheckSecurityRule with violated=false returned non-nil Override")
	}
}

func TestCheckSecurityRule_ViolatedWithOverride(t *testing.T) {
	t.Setenv(EnvAllowInsecureTLS, "testing override")

	result := CheckSecurityRule(RuleTLSRequired, true)
	if !result.Violated {
		t.Error("CheckSecurityRule with violated=true returned Violated=false")
	}

	if result.Override == nil {
		t.Fatal("CheckSecurityRule returned nil Override when env var is set")
	}

	if result.Override.Reason != "testing override" {
		t.Errorf("Override.Reason = %q, want %q", result.Override.Reason, "testing override")
	}
}

func TestCheckSecurityRule_ViolatedWithoutOverride(t *testing.T) {
	unsetEnvForTest(t, EnvAllowInsecureTLS)

	result := CheckSecurityRule(RuleTLSRequired, true)
	if !result.Violated {
		t.Error("expected Violated=true")
	}

	if result.Override != nil {
		t.Error("expected nil Override when env var not set")
	}

	if result.EnvVar != EnvAllowInsecureTLS {
		t.Errorf("EnvVar = %q, want %q", result.EnvVar, EnvAllowInsecureTLS)
	}
}

func TestSecurityEnvVarForRule(t *testing.T) {
	t.Parallel()

	tests := []struct {
		rule string
		want string
	}{
		{RuleTLSRequired, EnvAllowInsecureTLS},
		{RuleCORSWildcardOrigin, EnvAllowCORSWildcard},
		{RuleRateLimitDisabled, EnvAllowRateLimitDisabled},
		{RuleRateLimitFailOpen, EnvAllowRateLimitFailOpen},
		{RuleOTELInsecureExporter, EnvAllowInsecureOTEL},
		{"unknown", ""},
	}

	for _, tc := range tests {
		t.Run(tc.rule, func(t *testing.T) {
			t.Parallel()

			got := SecurityEnvVarForRule(tc.rule)
			if got != tc.want {
				t.Errorf("SecurityEnvVarForRule(%q) = %q, want %q", tc.rule, got, tc.want)
			}
		})
	}
}

func TestIsSecurityEnforcementEnabled(t *testing.T) {
	// Default: disabled.
	t.Setenv(EnvSecurityEnforcement, "")
	if IsSecurityEnforcementEnabled() {
		t.Error("IsSecurityEnforcementEnabled() = true without env var, want false")
	}

	// Explicitly enabled.
	t.Setenv(EnvSecurityEnforcement, "true")
	if !IsSecurityEnforcementEnabled() {
		t.Error("IsSecurityEnforcementEnabled() = false with SECURITY_ENFORCEMENT=true, want true")
	}

	// Explicitly disabled.
	t.Setenv(EnvSecurityEnforcement, "false")
	if IsSecurityEnforcementEnabled() {
		t.Error("IsSecurityEnforcementEnabled() = true with SECURITY_ENFORCEMENT=false, want false")
	}
}

func TestEnforceSecurityRule_PermissiveNeverErrors(t *testing.T) {
	SetEnvironmentForTest(t, Local) // Permissive tier
	unsetEnvForTest(t, EnvAllowInsecureTLS)
	t.Setenv(EnvSecurityEnforcement, "true") // Even with enforcement ON

	result := CheckSecurityRule(RuleTLSRequired, true)
	err := EnforceSecurityRule(context.Background(), nil, "test-postgres", result)
	if err != nil {
		t.Errorf("EnforceSecurityRule in permissive tier returned error: %v", err)
	}
}

func TestEnforceSecurityRule_ModerateWarnOnly(t *testing.T) {
	SetEnvironmentForTest(t, Staging) // Moderate tier
	unsetEnvForTest(t, EnvAllowInsecureTLS)
	t.Setenv(EnvSecurityEnforcement, "") // Phase 2: enforcement OFF

	result := CheckSecurityRule(RuleTLSRequired, true)
	err := EnforceSecurityRule(context.Background(), nil, "test-postgres", result)
	if err != nil {
		t.Errorf("EnforceSecurityRule in moderate/warn-only returned error: %v", err)
	}
}

func TestEnforceSecurityRule_StrictWarnOnlyDoesNotError(t *testing.T) {
	SetEnvironmentForTest(t, Production)
	unsetEnvForTest(t, EnvAllowInsecureTLS)
	t.Setenv(EnvSecurityEnforcement, "false")

	result := CheckSecurityRule(RuleTLSRequired, true)
	err := EnforceSecurityRule(context.Background(), nil, "test-postgres", result)
	if err != nil {
		t.Errorf("EnforceSecurityRule in strict/warn-only returned error: %v", err)
	}
}

func TestEnforceSecurityRule_ModerateEnforcedErrors(t *testing.T) {
	SetEnvironmentForTest(t, Staging) // Moderate tier
	unsetEnvForTest(t, EnvAllowInsecureTLS)
	t.Setenv(EnvSecurityEnforcement, "true") // Phase 3: enforcement ON

	result := CheckSecurityRule(RuleTLSRequired, true)
	err := EnforceSecurityRule(context.Background(), nil, "test-postgres", result)
	if err == nil {
		t.Fatal("EnforceSecurityRule in moderate+enforced should return error, got nil")
	}

	if !errors.Is(err, ErrSecurityViolation) {
		t.Errorf("expected ErrSecurityViolation, got: %v", err)
	}
}

func TestEnforceSecurityRule_StrictWithOverride(t *testing.T) {
	SetEnvironmentForTest(t, Production) // Strict tier
	t.Setenv(EnvAllowInsecureTLS, "Istio mTLS handles encryption")
	t.Setenv(EnvSecurityEnforcement, "true")

	result := CheckSecurityRule(RuleTLSRequired, true)
	err := EnforceSecurityRule(context.Background(), nil, "test-postgres", result)
	if err != nil {
		t.Errorf("EnforceSecurityRule with valid override should not error, got: %v", err)
	}
}

func TestEnforceSecurityRule_ModerateWithOverride(t *testing.T) {
	SetEnvironmentForTest(t, Staging)
	t.Setenv(EnvAllowInsecureTLS, "mesh handles encryption")
	t.Setenv(EnvSecurityEnforcement, "true")

	result := CheckSecurityRule(RuleTLSRequired, true)
	err := EnforceSecurityRule(context.Background(), nil, "test-postgres", result)
	if err != nil {
		t.Errorf("EnforceSecurityRule in moderate tier with override should not error, got: %v", err)
	}
}

func TestEnforceSecurityRule_NotViolated(t *testing.T) {
	SetEnvironmentForTest(t, Production)
	t.Setenv(EnvSecurityEnforcement, "true")

	result := CheckSecurityRule(RuleTLSRequired, false)
	err := EnforceSecurityRule(context.Background(), nil, "test-postgres", result)
	if err != nil {
		t.Errorf("EnforceSecurityRule with violated=false should not error, got: %v", err)
	}
}

func TestEnforceSecurityRule_StrictEnforcedErrors(t *testing.T) {
	SetEnvironmentForTest(t, Production)     // Strict tier
	unsetEnvForTest(t, EnvAllowInsecureTLS)  // No override
	t.Setenv(EnvSecurityEnforcement, "true") // Phase 3: enforcement ON

	result := CheckSecurityRule(RuleTLSRequired, true)
	err := EnforceSecurityRule(context.Background(), nil, "test-postgres", result)
	if err == nil {
		t.Fatal("EnforceSecurityRule in strict+enforced should return error, got nil")
	}

	if !errors.Is(err, ErrSecurityViolation) {
		t.Errorf("expected ErrSecurityViolation, got: %v", err)
	}
}

func TestEnforceSecurityRule_UsesSecurityTierOverride(t *testing.T) {
	SetEnvironmentForTest(t, Local)
	// Clear cached global environment state so the t.Setenv values below drive
	// fresh detection instead of the SetEnvironmentForTest override.
	resetEnvironment()

	t.Setenv("ENV_NAME", Staging.String())
	t.Setenv("ENV", "")
	t.Setenv("GO_ENV", "")
	t.Setenv(EnvSecurityTier, TierStrict.String())
	unsetEnvForTest(t, EnvAllowInsecureTLS)
	t.Setenv(EnvSecurityEnforcement, "true")

	spy := &securityLoggerSpy{}
	result := CheckSecurityRule(RuleTLSRequired, true)
	err := EnforceSecurityRule(context.Background(), spy, "postgres", result)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrSecurityViolation)
	require.Len(t, spy.entries, 1)

	if !strings.Contains(err.Error(), "strict tier (staging; overridden by SECURITY_TIER)") {
		t.Fatalf("error = %q, want strict tier override context", err)
	}

	fieldsByKey := map[string]any{}
	for _, field := range spy.entries[0].fields {
		fieldsByKey[field.Key] = field.Value
	}

	if fieldsByKey["tier"] != TierStrict.String() {
		t.Fatalf("tier = %v, want %v", fieldsByKey["tier"], TierStrict.String())
	}
	if fieldsByKey["environment"] != Staging.String() {
		t.Fatalf("environment = %v, want %v", fieldsByKey["environment"], Staging.String())
	}
	if fieldsByKey["tier_source"] != EnvSecurityTier {
		t.Fatalf("tier_source = %v, want %v", fieldsByKey["tier_source"], EnvSecurityTier)
	}
}

func TestEnforceSecurityRuleForEnvironment_UsesSecurityTierOverride(t *testing.T) {
	SetEnvironmentForTest(t, Local)
	// Clear cached global environment state so SECURITY_TIER is resolved from the
	// process environment rather than the earlier test helper override.
	resetEnvironment()

	t.Setenv(EnvSecurityTier, TierStrict.String())
	unsetEnvForTest(t, EnvAllowInsecureTLS)
	t.Setenv(EnvSecurityEnforcement, "true")

	spy := &securityLoggerSpy{}
	result := CheckSecurityRule(RuleTLSRequired, true)
	err := EnforceSecurityRuleForEnvironment(context.Background(), spy, "otel", Staging, result)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrSecurityViolation)
	require.Len(t, spy.entries, 1)

	fieldsByKey := map[string]any{}
	for _, field := range spy.entries[0].fields {
		fieldsByKey[field.Key] = field.Value
	}

	if fieldsByKey["tier"] != TierStrict.String() {
		t.Fatalf("tier = %v, want %v", fieldsByKey["tier"], TierStrict.String())
	}
	if fieldsByKey["environment"] != Staging.String() {
		t.Fatalf("environment = %v, want %v", fieldsByKey["environment"], Staging.String())
	}
}

func TestEnforceSecurityRule_EmptyEnvOverrideReasonInStrictTier(t *testing.T) {
	SetEnvironmentForTest(t, Production)
	t.Setenv(EnvAllowInsecureTLS, "   ")
	t.Setenv(EnvSecurityEnforcement, "true")

	result := CheckSecurityRule(RuleTLSRequired, true)
	err := EnforceSecurityRule(context.Background(), nil, "postgres", result)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrOverrideReasonRequired)
}

func TestEnforceSecurityRule_EmptyReasonInModerate(t *testing.T) {
	SetEnvironmentForTest(t, Staging) // Moderate tier

	result := SecurityCheckResult{
		Rule:     RuleTLSRequired,
		Violated: true,
		Override: &SecurityOverride{Rule: RuleTLSRequired, Reason: "  ", Source: "code"},
		EnvVar:   EnvAllowInsecureTLS,
	}

	err := EnforceSecurityRule(context.Background(), nil, "test", result)
	if err == nil {
		t.Fatal("EnforceSecurityRule with empty reason in moderate should return error, got nil")
	}

	if !errors.Is(err, ErrOverrideReasonRequired) {
		t.Errorf("expected ErrOverrideReasonRequired, got: %v", err)
	}
}

func TestEnforceSecurityRule_RedactsOverrideReasonInLogs(t *testing.T) {
	SetEnvironmentForTest(t, Production)
	t.Setenv(EnvSecurityEnforcement, "true")

	spy := &securityLoggerSpy{}
	result := SecurityCheckResult{
		Rule:     RuleTLSRequired,
		Violated: true,
		Override: &SecurityOverride{Rule: RuleTLSRequired, Reason: "secret-ticket-123", Source: "code"},
		EnvVar:   EnvAllowInsecureTLS,
	}

	require.NoError(t, EnforceSecurityRule(context.Background(), spy, "postgres", result))
	require.Len(t, spy.entries, 1)

	fieldsByKey := map[string]any{}
	for _, field := range spy.entries[0].fields {
		fieldsByKey[field.Key] = field.Value
	}

	_, hasReason := fieldsByKey["reason"]
	if hasReason {
		t.Fatal("override reason should not be logged verbatim")
	}

	if fieldsByKey["reason_present"] != true {
		t.Fatalf("reason_present = %v, want true", fieldsByKey["reason_present"])
	}
	if fieldsByKey["rule"] != RuleTLSRequired {
		t.Fatalf("rule = %v, want %v", fieldsByKey["rule"], RuleTLSRequired)
	}
	if fieldsByKey["component"] != "postgres" {
		t.Fatalf("component = %v, want postgres", fieldsByKey["component"])
	}
}

func TestEnforceSecurityRule_TypedNilLoggerDoesNotPanic(t *testing.T) {
	SetEnvironmentForTest(t, Production)
	unsetEnvForTest(t, EnvAllowInsecureTLS)
	t.Setenv(EnvSecurityEnforcement, "true")

	var typedNil *securityLoggerSpy
	result := CheckSecurityRule(RuleTLSRequired, true)

	require.NotPanics(t, func() {
		_ = EnforceSecurityRule(context.Background(), typedNil, "postgres", result)
	})
}

func TestAllOverrideEnvVarsMapped(t *testing.T) {
	t.Parallel()

	// Every rule constant must have a corresponding env var.
	rules := []string{
		RuleTLSRequired,
		RuleCORSWildcardOrigin,
		RuleRateLimitDisabled,
		RuleRateLimitFailOpen,
		RuleOTELInsecureExporter,
	}

	for _, rule := range rules {
		envVar := SecurityEnvVarForRule(rule)
		if envVar == "" {
			t.Errorf("rule %q has no mapped env var", rule)
		}
	}
}
