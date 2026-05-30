package events

import "testing"

func TestTenantEventsChannel(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want string
	}{
		{
			name: "staging produces env-scoped channel",
			env:  "staging",
			want: "tenant-events:staging:",
		},
		{
			name: "production produces env-scoped channel",
			env:  "production",
			want: "tenant-events:production:",
		},
		{
			// Documents that this function does NOT validate. Callers must
			// validate the env upstream via commons.CurrentEnv() — passing
			// an empty string here yields a malformed channel name.
			name: "empty env yields malformed channel (caller-validation contract)",
			env:  "",
			want: "tenant-events::",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TenantEventsChannel(tt.env)
			if got != tt.want {
				t.Errorf("TenantEventsChannel(%q) = %q, want %q", tt.env, got, tt.want)
			}
		})
	}
}

func TestTenantEventsChannelPrefix(t *testing.T) {
	const want = "tenant-events:"
	if TenantEventsChannelPrefix != want {
		t.Errorf("TenantEventsChannelPrefix = %q, want %q", TenantEventsChannelPrefix, want)
	}
}
