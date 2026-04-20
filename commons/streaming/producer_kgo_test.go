//go:build unit

package streaming

import (
	"errors"
	"testing"
	"time"
)

// TestResolveCompression_Table proves every compression codec string
// accepted by Config.validate() maps to a valid kgo.CompressionCodec.
// The unknown-codec branch surfaces ErrInvalidCompression defensively.
func TestResolveCompression_Table(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantErr error
	}{
		{"snappy maps cleanly", "snappy", nil},
		{"lz4 maps cleanly", "lz4", nil},
		{"zstd maps cleanly", "zstd", nil},
		{"gzip maps cleanly", "gzip", nil},
		{"none maps cleanly", "none", nil},
		{"unknown codec surfaces ErrInvalidCompression", "brotli", ErrInvalidCompression},
		{"empty string surfaces ErrInvalidCompression", "", ErrInvalidCompression},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := resolveCompression(tt.input)
			if tt.wantErr == nil {
				if err != nil {
					t.Errorf("resolveCompression(%q) err = %v; want nil", tt.input, err)
				}
				return
			}

			if !errors.Is(err, tt.wantErr) {
				t.Errorf("resolveCompression(%q) err = %v; want %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

// TestResolveAcks_Table proves every acks string accepted by
// Config.validate() maps to a valid kgo.Acks. Unknown values surface
// ErrInvalidAcks.
func TestResolveAcks_Table(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantErr error
	}{
		{"all maps cleanly", "all", nil},
		{"leader maps cleanly", "leader", nil},
		{"none maps cleanly", "none", nil},
		{"unknown value surfaces ErrInvalidAcks", "maybe", ErrInvalidAcks},
		{"empty string surfaces ErrInvalidAcks", "", ErrInvalidAcks},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := resolveAcks(tt.input)
			if tt.wantErr == nil {
				if err != nil {
					t.Errorf("resolveAcks(%q) err = %v; want nil", tt.input, err)
				}
				return
			}

			if !errors.Is(err, tt.wantErr) {
				t.Errorf("resolveAcks(%q) err = %v; want %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

// TestBuildKgoOpts_AcksAllKeepsIdempotent proves that when acks=all, the
// kgo option slice does NOT include DisableIdempotentWrite — franz-go's
// idempotent producer remains enabled for the strongest durability
// posture.
//
// We assert by count: acks=all yields N options; acks=leader yields N+1
// (plus DisableIdempotentWrite).
func TestBuildKgoOpts_AcksAllKeepsIdempotent(t *testing.T) {
	t.Parallel()

	base := Config{
		Brokers:               []string{"broker:9092"},
		BatchLingerMs:         5,
		BatchMaxBytes:         1_048_576,
		MaxBufferedRecords:    1000,
		Compression:           "none",
		RecordRetries:         3,
		RecordDeliveryTimeout: 10 * time.Second,
	}

	allCfg := base
	allCfg.RequiredAcks = "all"

	leaderCfg := base
	leaderCfg.RequiredAcks = "leader"

	allOpts, err := buildKgoOpts(allCfg, emitterOptions{})
	if err != nil {
		t.Fatalf("buildKgoOpts(all) err = %v", err)
	}

	leaderOpts, err := buildKgoOpts(leaderCfg, emitterOptions{})
	if err != nil {
		t.Fatalf("buildKgoOpts(leader) err = %v", err)
	}

	// acks=leader adds exactly one extra option (DisableIdempotentWrite)
	// relative to acks=all.
	if len(leaderOpts) != len(allOpts)+1 {
		t.Errorf("leader opts count = %d; all opts count = %d; want leader = all+1",
			len(leaderOpts), len(allOpts))
	}
}

// TestBuildKgoOpts_ClientIDOptional proves the ClientID option is only
// added when cfg.ClientID is non-empty. Empty ClientID means franz-go
// picks a reasonable default.
func TestBuildKgoOpts_ClientIDOptional(t *testing.T) {
	t.Parallel()

	base := Config{
		Brokers:               []string{"broker:9092"},
		BatchLingerMs:         5,
		BatchMaxBytes:         1_048_576,
		MaxBufferedRecords:    1000,
		Compression:           "none",
		RecordRetries:         3,
		RecordDeliveryTimeout: 10 * time.Second,
		RequiredAcks:          "all",
	}

	noIDCfg := base
	noIDCfg.ClientID = ""

	withIDCfg := base
	withIDCfg.ClientID = "service-x"

	noIDOpts, err := buildKgoOpts(noIDCfg, emitterOptions{})
	if err != nil {
		t.Fatalf("buildKgoOpts(no id) err = %v", err)
	}

	withIDOpts, err := buildKgoOpts(withIDCfg, emitterOptions{})
	if err != nil {
		t.Fatalf("buildKgoOpts(with id) err = %v", err)
	}

	if len(withIDOpts) != len(noIDOpts)+1 {
		t.Errorf("with-id opts count = %d; no-id opts count = %d; want with = no+1",
			len(withIDOpts), len(noIDOpts))
	}
}

// TestBuildKgoOpts_InvalidCompressionSurfaces asserts that even though
// cfg.validate() should catch this first, buildKgoOpts defends against a
// call with an unvalidated Config and returns ErrInvalidCompression
// without panicking.
func TestBuildKgoOpts_InvalidCompressionSurfaces(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Brokers:      []string{"broker:9092"},
		Compression:  "brotli",
		RequiredAcks: "all",
	}

	_, err := buildKgoOpts(cfg, emitterOptions{})
	if !errors.Is(err, ErrInvalidCompression) {
		t.Errorf("buildKgoOpts err = %v; want ErrInvalidCompression", err)
	}
}

// TestBuildKgoOpts_InvalidAcksSurfaces same as above but for acks.
func TestBuildKgoOpts_InvalidAcksSurfaces(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Brokers:      []string{"broker:9092"},
		Compression:  "none",
		RequiredAcks: "maybe",
	}

	_, err := buildKgoOpts(cfg, emitterOptions{})
	if !errors.Is(err, ErrInvalidAcks) {
		t.Errorf("buildKgoOpts err = %v; want ErrInvalidAcks", err)
	}
}
