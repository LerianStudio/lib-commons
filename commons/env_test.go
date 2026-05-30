package commons

import (
	"strings"
	"testing"
)

func TestCurrentEnv(t *testing.T) {
	tests := []struct {
		name           string
		environmentVar string
		envNameVar     string
		want           string
		wantErr        bool
		errContains    string
	}{
		{
			name:           "reads ENVIRONMENT_NAME (preferred)",
			environmentVar: "staging",
			want:           "staging",
		},
		{
			name:           "reads ENVIRONMENT_NAME for production",
			environmentVar: "production",
			want:           "production",
		},
		{
			name:       "accepts ENV_NAME when ENVIRONMENT_NAME unset",
			envNameVar: "staging",
			want:       "staging",
		},
		{
			name:           "ENVIRONMENT_NAME wins over ENV_NAME",
			environmentVar: "production",
			envNameVar:     "staging",
			want:           "production",
		},
		{
			name:       "accepts ENV_NAME for production",
			envNameVar: "production",
			want:       "production",
		},
		{
			name:           "case-insensitive uppercase",
			environmentVar: "STAGING",
			want:           "staging",
		},
		{
			name:           "case-insensitive mixed case",
			environmentVar: "Production",
			want:           "production",
		},
		{
			name:           "trims whitespace",
			environmentVar: "  staging  ",
			want:           "staging",
		},
		{
			name:           "trims tabs and newlines",
			environmentVar: "\tstaging\n",
			want:           "staging",
		},
		{
			name:        "rejects when both unset",
			wantErr:     true,
			errContains: "is required",
		},
		{
			name:           "rejects empty string in ENVIRONMENT_NAME with ENV_NAME also empty",
			environmentVar: "",
			envNameVar:     "",
			wantErr:        true,
			errContains:    "is required",
		},
		{
			name:           "rejects whitespace-only value",
			environmentVar: "   ",
			wantErr:        true,
			errContains:    "is required",
		},
		{
			name:           "rejects unknown value 'dev'",
			environmentVar: "dev",
			wantErr:        true,
			errContains:    "invalid environment",
		},
		{
			name:           "rejects unknown value 'uat'",
			environmentVar: "uat",
			wantErr:        true,
			errContains:    "invalid environment",
		},
		{
			name:           "rejects unknown value 'local'",
			environmentVar: "local",
			wantErr:        true,
			errContains:    "invalid environment",
		},
		{
			name:           "rejects unknown value 'prod' (must be full word)",
			environmentVar: "prod",
			wantErr:        true,
			errContains:    "invalid environment",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("ENVIRONMENT_NAME", tt.environmentVar)
			t.Setenv("ENV_NAME", tt.envNameVar)

			got, err := CurrentEnv()

			if tt.wantErr {
				if err == nil {
					t.Fatalf("CurrentEnv() expected error, got nil (value=%q)", got)
				}

				if tt.errContains != "" && !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("CurrentEnv() error = %q, want substring %q", err.Error(), tt.errContains)
				}

				return
			}

			if err != nil {
				t.Fatalf("CurrentEnv() unexpected error: %v", err)
			}

			if got != tt.want {
				t.Errorf("CurrentEnv() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMustCurrentEnv_Success(t *testing.T) {
	t.Setenv("ENVIRONMENT_NAME", "production")
	t.Setenv("ENV_NAME", "")

	got := MustCurrentEnv()
	if got != "production" {
		t.Errorf("MustCurrentEnv() = %q, want %q", got, "production")
	}
}

func TestMustCurrentEnv_PanicsOnUnset(t *testing.T) {
	t.Setenv("ENVIRONMENT_NAME", "")
	t.Setenv("ENV_NAME", "")

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("MustCurrentEnv() expected panic, did not panic")
		}
	}()

	_ = MustCurrentEnv()
}

func TestMustCurrentEnv_PanicsOnInvalid(t *testing.T) {
	t.Setenv("ENVIRONMENT_NAME", "qa")
	t.Setenv("ENV_NAME", "")

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("MustCurrentEnv() expected panic, did not panic")
		}
	}()

	_ = MustCurrentEnv()
}
