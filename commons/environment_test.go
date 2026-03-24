//go:build unit

package commons

import (
	"errors"
	"os"
	"testing"
)

func TestEnvironment_SecurityTier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		env  Environment
		want SecurityTier
	}{
		{Local, TierPermissive},
		{Development, TierPermissive},
		{Staging, TierModerate},
		{UAT, TierModerate},
		{Production, TierStrict},
		{Environment("unknown"), TierStrict},    // unknown = strict (fail safe)
		{Environment(""), TierStrict},           // empty = strict (fail safe)
		{Environment("custom-env"), TierStrict}, // custom = strict (fail safe)
	}

	for _, tc := range tests {
		t.Run(string(tc.env), func(t *testing.T) {
			t.Parallel()

			got := tc.env.SecurityTier()
			if got != tc.want {
				t.Errorf("Environment(%q).SecurityTier() = %v, want %v", tc.env, got, tc.want)
			}
		})
	}
}

func TestSecurityTier_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		tier SecurityTier
		want string
	}{
		{TierPermissive, "permissive"},
		{TierModerate, "moderate"},
		{TierStrict, "strict"},
		{SecurityTier(99), "unknown"},
	}

	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()

			if got := tc.tier.String(); got != tc.want {
				t.Errorf("SecurityTier(%d).String() = %q, want %q", tc.tier, got, tc.want)
			}
		})
	}
}

func TestEnvironment_IsValid(t *testing.T) {
	t.Parallel()

	valid := []Environment{Production, Staging, UAT, Development, Local}
	for _, env := range valid {
		if !env.IsValid() {
			t.Errorf("Environment(%q).IsValid() = false, want true", env)
		}
	}

	invalid := []Environment{"", "custom", "PRODUCTION", "prod"}
	for _, env := range invalid {
		if env.IsValid() {
			t.Errorf("Environment(%q).IsValid() = true, want false", env)
		}
	}
}

func TestSetEnvironment_HappyPath(t *testing.T) {
	SetEnvironmentForTest(t, Local) // reset via cleanup
	t.Setenv(EnvSecurityTier, "")

	resetEnvironment()

	err := SetEnvironment(Production)
	if err != nil {
		t.Fatalf("SetEnvironment(Production) = %v, want nil", err)
	}

	got := CurrentEnvironment()
	if got != Production {
		t.Errorf("CurrentEnvironment() = %q, want %q", got, Production)
	}

	if tier := CurrentTier(); tier != TierStrict {
		t.Errorf("CurrentTier() = %v, want TierStrict", tier)
	}
}

func TestSetEnvironment_DoubleCallReturnsError(t *testing.T) {
	SetEnvironmentForTest(t, Local) // reset via cleanup

	resetEnvironment()

	if err := SetEnvironment(Staging); err != nil {
		t.Fatalf("first SetEnvironment = %v", err)
	}

	err := SetEnvironment(Production)
	if err == nil {
		t.Fatal("second SetEnvironment should return error, got nil")
	}

	if !errors.Is(err, ErrEnvironmentAlreadySet) {
		t.Errorf("second SetEnvironment error = %v, want ErrEnvironmentAlreadySet", err)
	}

	// Original value preserved.
	if got := CurrentEnvironment(); got != Staging {
		t.Errorf("CurrentEnvironment() = %q after double-set, want %q", got, Staging)
	}
}

func TestSetEnvironment_InvalidValue(t *testing.T) {
	SetEnvironmentForTest(t, Local) // reset via cleanup

	resetEnvironment()

	err := SetEnvironment(Environment("invalid"))
	if err == nil {
		t.Fatal("SetEnvironment(invalid) should return error, got nil")
	}

	if !errors.Is(err, ErrInvalidEnvironment) {
		t.Errorf("SetEnvironment(invalid) error = %v, want ErrInvalidEnvironment", err)
	}
}

func TestDetectEnvironment_Priority(t *testing.T) {
	// Save and restore env vars.
	for _, key := range []string{"ENV_NAME", "ENV", "GO_ENV"} {
		orig := os.Getenv(key)
		t.Cleanup(func() { os.Setenv(key, orig) })
	}

	// Clear all.
	os.Setenv("ENV_NAME", "")
	os.Setenv("ENV", "")
	os.Setenv("GO_ENV", "")

	// No env vars set → Local.
	if got := DetectEnvironment(); got != Local {
		t.Errorf("DetectEnvironment() with no vars = %q, want %q", got, Local)
	}

	// Only GO_ENV set.
	os.Setenv("GO_ENV", "production")
	if got := DetectEnvironment(); got != Production {
		t.Errorf("DetectEnvironment() with GO_ENV=production = %q, want %q", got, Production)
	}

	// ENV overrides GO_ENV.
	os.Setenv("ENV", "staging")
	if got := DetectEnvironment(); got != Staging {
		t.Errorf("DetectEnvironment() with ENV=staging = %q, want %q", got, Staging)
	}

	// ENV_NAME overrides ENV.
	os.Setenv("ENV_NAME", "development")
	if got := DetectEnvironment(); got != Development {
		t.Errorf("DetectEnvironment() with ENV_NAME=development = %q, want %q", got, Development)
	}
}

func TestDetectEnvironment_CaseInsensitive(t *testing.T) {
	t.Setenv("ENV", "")
	t.Setenv("GO_ENV", "")

	t.Setenv("ENV_NAME", "PRODUCTION")
	if got := DetectEnvironment(); got != Production {
		t.Errorf("DetectEnvironment() with ENV_NAME=PRODUCTION = %q, want %q", got, Production)
	}

	t.Setenv("ENV_NAME", "Staging")
	if got := DetectEnvironment(); got != Staging {
		t.Errorf("DetectEnvironment() with ENV_NAME=Staging = %q, want %q", got, Staging)
	}
}

func TestDetectEnvironment_FailsSafeOnUnrecognizedValues(t *testing.T) {
	for _, key := range []string{"ENV_NAME", "ENV", "GO_ENV"} {
		orig := os.Getenv(key)
		t.Cleanup(func() { os.Setenv(key, orig) })
	}

	os.Setenv("ENV_NAME", "custom-invalid")
	os.Setenv("ENV", "staging")
	os.Setenv("GO_ENV", "production")

	// ENV_NAME is invalid and must fail safe instead of falling through.
	got := DetectEnvironment()
	if got != Environment("custom-invalid") {
		t.Errorf("DetectEnvironment() = %q, want invalid environment value preserved for fail-safe handling", got)
	}

	if tier := got.SecurityTier(); tier != TierStrict {
		t.Errorf("DetectEnvironment().SecurityTier() = %v, want %v", tier, TierStrict)
	}
}

func TestCurrentEnvironment_FallsBackToDetect(t *testing.T) {
	SetEnvironmentForTest(t, Local) // reset via cleanup

	resetEnvironment() // ensure not explicitly set

	for _, key := range []string{"ENV_NAME", "ENV", "GO_ENV"} {
		orig := os.Getenv(key)
		t.Cleanup(func() { os.Setenv(key, orig) })
	}

	os.Setenv("ENV_NAME", "staging")
	os.Setenv("ENV", "")
	os.Setenv("GO_ENV", "")

	got := CurrentEnvironment()
	if got != Staging {
		t.Errorf("CurrentEnvironment() without Set = %q, want %q (from DetectEnvironment)", got, Staging)
	}
}

func TestCurrentEnvironment_InvalidatesCacheWhenEnvVarsChange(t *testing.T) {
	SetEnvironmentForTest(t, Local)
	resetEnvironment()

	t.Setenv("ENV_NAME", "staging")
	t.Setenv("ENV", "")
	t.Setenv("GO_ENV", "")

	if got := CurrentEnvironment(); got != Staging {
		t.Fatalf("CurrentEnvironment() = %q, want %q", got, Staging)
	}

	t.Setenv("ENV_NAME", "production")

	if got := CurrentEnvironment(); got != Production {
		t.Fatalf("CurrentEnvironment() after env change = %q, want %q", got, Production)
	}
}

func TestCurrentTier_UsesSecurityTierOverride(t *testing.T) {
	SetEnvironmentForTest(t, Local)
	resetEnvironment()

	t.Setenv("ENV_NAME", Staging.String())
	t.Setenv("ENV", "")
	t.Setenv("GO_ENV", "")
	t.Setenv(EnvSecurityTier, TierStrict.String())

	if got := CurrentEnvironment(); got != Staging {
		t.Fatalf("CurrentEnvironment() = %q, want %q", got, Staging)
	}

	if tier := CurrentTier(); tier != TierStrict {
		t.Fatalf("CurrentTier() = %v, want %v", tier, TierStrict)
	}

	if tier := EffectiveSecurityTier(Staging); tier != TierStrict {
		t.Fatalf("EffectiveSecurityTier(Staging) = %v, want %v", tier, TierStrict)
	}
}

func TestCurrentTier_InvalidOverrideFailsSafeToStrict(t *testing.T) {
	SetEnvironmentForTest(t, Local)
	resetEnvironment()

	t.Setenv("ENV_NAME", Local.String())
	t.Setenv("ENV", "")
	t.Setenv("GO_ENV", "")
	t.Setenv(EnvSecurityTier, "definitely-not-a-tier")

	if tier := CurrentTier(); tier != TierStrict {
		t.Fatalf("CurrentTier() with invalid SECURITY_TIER = %v, want %v", tier, TierStrict)
	}
}

func TestCurrentTier_InvalidatesOverrideCacheWhenEnvVarChanges(t *testing.T) {
	SetEnvironmentForTest(t, Local)
	resetEnvironment()

	t.Setenv("ENV_NAME", Staging.String())
	t.Setenv("ENV", "")
	t.Setenv("GO_ENV", "")
	t.Setenv(EnvSecurityTier, TierStrict.String())

	if tier := CurrentTier(); tier != TierStrict {
		t.Fatalf("CurrentTier() = %v, want %v", tier, TierStrict)
	}

	t.Setenv(EnvSecurityTier, TierModerate.String())
	if tier := CurrentTier(); tier != TierModerate {
		t.Fatalf("CurrentTier() after SECURITY_TIER change = %v, want %v", tier, TierModerate)
	}

	t.Setenv(EnvSecurityTier, "")
	if tier := CurrentTier(); tier != TierModerate {
		t.Fatalf("CurrentTier() after clearing SECURITY_TIER = %v, want %v", tier, TierModerate)
	}
}

func TestSetEnvironmentForTest(t *testing.T) {
	// Verify that SetEnvironmentForTest properly restores state.
	resetEnvironment()

	original := CurrentEnvironment() // will detect from env vars

	t.Run("subtest_overrides", func(t *testing.T) {
		SetEnvironmentForTest(t, Production)
		t.Setenv(EnvSecurityTier, "")

		if got := CurrentEnvironment(); got != Production {
			t.Errorf("inside subtest: CurrentEnvironment() = %q, want %q", got, Production)
		}

		if tier := CurrentTier(); tier != TierStrict {
			t.Errorf("inside subtest: CurrentTier() = %v, want TierStrict", tier)
		}
	})

	// After subtest cleanup, state should be restored.
	after := CurrentEnvironment()
	if after != original {
		t.Errorf("after subtest: CurrentEnvironment() = %q, want %q (restored)", after, original)
	}
}
