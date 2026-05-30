//go:build unit

package commons

import (
	"errors"
	"os"
	"testing"
)

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

	resetEnvironment()

	err := SetEnvironment(Production)
	if err != nil {
		t.Fatalf("SetEnvironment(Production) = %v, want nil", err)
	}

	got := CurrentEnvironment()
	if got != Production {
		t.Errorf("CurrentEnvironment() = %q, want %q", got, Production)
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

func TestDetectEnvironment_PreservesUnrecognizedValue(t *testing.T) {
	for _, key := range []string{"ENV_NAME", "ENV", "GO_ENV"} {
		orig := os.Getenv(key)
		t.Cleanup(func() { os.Setenv(key, orig) })
	}

	os.Setenv("ENV_NAME", "custom-invalid")
	os.Setenv("ENV", "staging")
	os.Setenv("GO_ENV", "production")

	// ENV_NAME is non-empty but unrecognized: the value is returned as-is
	// (lower-cased). Callers that need a recognized value must check
	// IsValid() before using it.
	got := DetectEnvironment()
	if got != Environment("custom-invalid") {
		t.Errorf("DetectEnvironment() = %q, want %q (unrecognized values are preserved verbatim)", got, "custom-invalid")
	}

	if got.IsValid() {
		t.Errorf("DetectEnvironment().IsValid() = true for %q, want false", got)
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

func TestSetEnvironmentForTest(t *testing.T) {
	// Verify that SetEnvironmentForTest properly restores state.
	resetEnvironment()

	original := CurrentEnvironment() // will detect from env vars

	t.Run("subtest_overrides", func(t *testing.T) {
		SetEnvironmentForTest(t, Production)

		if got := CurrentEnvironment(); got != Production {
			t.Errorf("inside subtest: CurrentEnvironment() = %q, want %q", got, Production)
		}
	})

	// After subtest cleanup, state should be restored.
	after := CurrentEnvironment()
	if after != original {
		t.Errorf("after subtest: CurrentEnvironment() = %q, want %q (restored)", after, original)
	}
}
