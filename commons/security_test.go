//go:build unit

package commons

import (
	"os"
	"testing"
)

func TestParseBool(t *testing.T) {
	t.Parallel()

	cases := []struct {
		raw      string
		fallback bool
		want     bool
	}{
		{"true", false, true},
		{"TRUE", false, true},
		{"True", false, true},
		{" true ", false, true},
		{"1", false, true},
		{"yes", false, true},
		{"YES", false, true},
		{"on", false, true},
		{"false", true, false},
		{"FALSE", true, false},
		{"0", true, false},
		{"no", true, false},
		{"off", true, false},
		// Empty / unknown values use the fallback.
		{"", true, true},
		{"", false, false},
		{"some-reason", true, true},
		{"some-reason", false, false},
		{"break-glass: tls", true, true},
		{"break-glass: tls", false, false},
	}

	for _, tc := range cases {
		got := parseBool(tc.raw, tc.fallback)
		if got != tc.want {
			t.Errorf("parseBool(%q, %v) = %v, want %v", tc.raw, tc.fallback, got, tc.want)
		}
	}
}

func TestGetenvBool(t *testing.T) {
	const key = "TEST_GETENV_BOOL_KEY"

	cases := []struct {
		name string
		set  bool
		val  string
		want bool
	}{
		{"unset", false, "", false},
		{"empty", true, "", false},
		{"truthy_true", true, "true", true},
		{"truthy_1", true, "1", true},
		{"truthy_yes", true, "yes", true},
		{"truthy_on", true, "on", true},
		{"truthy_mixed_case", true, "True", true},
		{"falsy_false", true, "false", false},
		{"falsy_0", true, "0", false},
		{"falsy_no", true, "no", false},
		{"falsy_off", true, "off", false},
		{"legacy_reason_string_is_false", true, "break-glass: tls", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			unsetEnvForTest(t, key)

			if tc.set {
				t.Setenv(key, tc.val)
			}

			if got := getenvBool(key); got != tc.want {
				t.Errorf("getenvBool(%q) with set=%v val=%q = %v, want %v", key, tc.set, tc.val, got, tc.want)
			}
		})
	}
}

func TestGetenvBoolDefault(t *testing.T) {
	const key = "TEST_GETENV_BOOL_DEFAULT_KEY"

	t.Run("unset_uses_default_true", func(t *testing.T) {
		unsetEnvForTest(t, key)

		if got := getenvBoolDefault(key, true); !got {
			t.Errorf("getenvBoolDefault unset = false, want true")
		}
	})

	t.Run("unset_uses_default_false", func(t *testing.T) {
		unsetEnvForTest(t, key)

		if got := getenvBoolDefault(key, false); got {
			t.Errorf("getenvBoolDefault unset = true, want false")
		}
	})

	t.Run("set_truthy_overrides_default_false", func(t *testing.T) {
		unsetEnvForTest(t, key)
		t.Setenv(key, "true")

		if got := getenvBoolDefault(key, false); !got {
			t.Errorf("getenvBoolDefault truthy = false, want true")
		}
	})

	t.Run("set_falsy_overrides_default_true", func(t *testing.T) {
		unsetEnvForTest(t, key)
		t.Setenv(key, "false")

		if got := getenvBoolDefault(key, true); got {
			t.Errorf("getenvBoolDefault falsy = true, want false")
		}
	})

	t.Run("set_unknown_uses_default", func(t *testing.T) {
		unsetEnvForTest(t, key)
		t.Setenv(key, "maybe")

		if got := getenvBoolDefault(key, true); !got {
			t.Errorf("getenvBoolDefault unknown w/default=true = false, want true")
		}
	})
}

func TestSecurityToggles(t *testing.T) {
	cases := []struct {
		name string
		env  string
		fn   func() bool
	}{
		{"AllowInsecureTLS", EnvAllowInsecureTLS, AllowInsecureTLS},
		{"AllowRateLimitDisabled", EnvAllowRateLimitDisabled, AllowRateLimitDisabled},
		{"AllowRateLimitFailOpen", EnvAllowRateLimitFailOpen, AllowRateLimitFailOpen},
		{"AllowCORSWildcard", EnvAllowCORSWildcard, AllowCORSWildcard},
		{"AllowInsecureOTEL", EnvAllowInsecureOTEL, AllowInsecureOTEL},
		{"AllowWebhookPrivateNet", EnvAllowWebhookPrivateNet, AllowWebhookPrivateNet},
	}

	for _, tc := range cases {
		t.Run(tc.name+"_unset_is_false", func(t *testing.T) {
			unsetEnvForTest(t, tc.env)

			if got := tc.fn(); got {
				t.Errorf("%s() unset = true, want false", tc.name)
			}
		})

		t.Run(tc.name+"_truthy_is_true", func(t *testing.T) {
			unsetEnvForTest(t, tc.env)
			t.Setenv(tc.env, "true")

			if got := tc.fn(); !got {
				t.Errorf("%s() with %s=true = false, want true", tc.name, tc.env)
			}
		})

		t.Run(tc.name+"_reason_string_is_false", func(t *testing.T) {
			unsetEnvForTest(t, tc.env)
			t.Setenv(tc.env, "break-glass: documented in INC-1234")

			if got := tc.fn(); got {
				t.Errorf("%s() with %s=<reason> = true, want false (legacy reason strings are no longer truthy)", tc.name, tc.env)
			}
		})
	}
}

func TestRateLimitEnabledDefaultsFalse(t *testing.T) {
	cases := []struct {
		name string
		set  bool
		val  string
		want bool
	}{
		{"unset_is_false_by_default", false, "", false},
		{"truthy_enables", true, "true", true},
		{"falsy_disables", true, "false", false},
		{"garbage_falls_back_to_default_false", true, "garbage", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			unsetEnvForTest(t, EnvRateLimitEnabled)

			if tc.set {
				t.Setenv(EnvRateLimitEnabled, tc.val)
			}

			if got := RateLimitEnabled(); got != tc.want {
				t.Errorf("RateLimitEnabled() with set=%v val=%q = %v, want %v", tc.set, tc.val, got, tc.want)
			}
		})
	}
}

// unsetEnvForTest unsets the env var for the duration of the test, restoring
// the prior value (if any) via t.Cleanup. Mirrors t.Setenv semantics without
// the empty-string compromise: an env var unset before the test must remain
// unset after, and an env var set before must be restored to its prior value.
func unsetEnvForTest(t *testing.T, key string) {
	t.Helper()

	prev, hadPrev := os.LookupEnv(key)

	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("os.Unsetenv(%q) failed: %v", key, err)
	}

	t.Cleanup(func() {
		if hadPrev {
			_ = os.Setenv(key, prev)
		} else {
			_ = os.Unsetenv(key)
		}
	})
}
