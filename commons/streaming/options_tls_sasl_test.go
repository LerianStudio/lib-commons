//go:build unit

package streaming

import (
	"crypto/tls"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/sasl/plain"
)

// Testing strategy (T8). Each kgo.Opt is a closure over private franz-go
// state — we cannot inspect what SeedBrokers / DialTLSConfig / SASL *did*
// without importing internal/cfg. Two surrogate assertions are sufficient:
//
//  1. Store-level: the option setter records the value on emitterOptions.
//  2. Count-level: buildKgoOpts emits exactly one extra kgo.Opt for each of
//     {TLS, SASL} — matching the pattern already established by
//     TestBuildKgoOpts_AcksAllKeepsIdempotent and
//     TestBuildKgoOpts_ClientIDOptional (both count-based).
//
// A real TLS handshake / SCRAM round-trip is out of scope per the T8 scope
// note — documented on WithTLSConfig / WithSASL godoc.

// validT8Config returns a cfg.validate()-passing Config for buildKgoOpts
// tests. Kept local to this file — producer_kgo_test.go builds its own
// inline Configs; centralizing would ripple beyond T8.
func validT8Config() Config {
	return Config{
		Brokers:               []string{"broker:9092"},
		BatchLingerMs:         5,
		BatchMaxBytes:         1_048_576,
		MaxBufferedRecords:    1000,
		Compression:           "none",
		RecordRetries:         3,
		RecordDeliveryTimeout: 10 * time.Second,
		RequiredAcks:          "all",
	}
}

// TestWithTLSConfig_StoresOption proves the setter plumbs the *tls.Config
// pointer onto emitterOptions without copying or mutating it. We use a
// distinctive ServerName so field-level swaps (e.g. applying the wrong
// field) would fail the equality check.
func TestWithTLSConfig_StoresOption(t *testing.T) {
	t.Parallel()

	opts := &emitterOptions{}
	tlsCfg := &tls.Config{
		ServerName: "tls-plumbing-test",
		MinVersion: tls.VersionTLS13,
	}
	WithTLSConfig(tlsCfg)(opts)

	if opts.tlsConfig == nil {
		t.Fatal("WithTLSConfig did not set opts.tlsConfig")
	}
	if opts.tlsConfig != tlsCfg {
		t.Error("WithTLSConfig stored a copy; expected identity pointer")
	}
	if opts.tlsConfig.ServerName != "tls-plumbing-test" {
		t.Errorf("ServerName = %q; want tls-plumbing-test", opts.tlsConfig.ServerName)
	}
	if opts.tlsConfig.MinVersion != tls.VersionTLS13 {
		t.Errorf("MinVersion = %x; want %x", opts.tlsConfig.MinVersion, tls.VersionTLS13)
	}
}

// TestWithTLSConfig_NilPassesThrough proves nil is a valid input and leaves
// opts.tlsConfig at the zero value. Callers who construct Producer without
// TLS rely on this — otherwise every New() call would need a guard.
func TestWithTLSConfig_NilPassesThrough(t *testing.T) {
	t.Parallel()

	opts := &emitterOptions{}
	WithTLSConfig(nil)(opts)

	if opts.tlsConfig != nil {
		t.Errorf("WithTLSConfig(nil) set opts.tlsConfig = %v; want nil", opts.tlsConfig)
	}
}

// TestWithSASL_StoresOption proves the setter plumbs the sasl.Mechanism
// onto emitterOptions. We use plain.Auth.AsMechanism() because PLAIN is the
// simplest mechanism franz-go ships and has no side effects at construction
// (no network I/O, no timer goroutines). The mechanism's Name() returning
// "PLAIN" is a public invariant of the franz-go sasl/plain package.
func TestWithSASL_StoresOption(t *testing.T) {
	t.Parallel()

	opts := &emitterOptions{}
	mech := plain.Auth{User: "u", Pass: "p"}.AsMechanism()
	WithSASL(mech)(opts)

	if opts.saslMechanism == nil {
		t.Fatal("WithSASL did not set opts.saslMechanism")
	}
	if got := opts.saslMechanism.Name(); got != "PLAIN" {
		t.Errorf("mechanism Name = %q; want PLAIN", got)
	}
}

// TestWithSASL_NilPassesThrough mirrors the TLS nil-passthrough test.
// Plaintext, unauthenticated is the default and nil must preserve it.
func TestWithSASL_NilPassesThrough(t *testing.T) {
	t.Parallel()

	opts := &emitterOptions{}
	WithSASL(nil)(opts)

	if opts.saslMechanism != nil {
		t.Errorf("WithSASL(nil) set opts.saslMechanism = %v; want nil", opts.saslMechanism)
	}
}

// TestBuildKgoOpts_WithTLSConfig_AppendsOneOpt asserts that a non-nil
// tlsConfig adds exactly one kgo.Opt to the returned slice vs. the
// baseline. We can't inspect the opt itself (franz-go closures are
// opaque), but the count delta is deterministic: kgo.DialTLSConfig emits
// one Opt.
//
// This is the same assertion pattern used for DisableIdempotentWrite and
// ClientID in producer_kgo_test.go.
func TestBuildKgoOpts_WithTLSConfig_AppendsOneOpt(t *testing.T) {
	t.Parallel()

	cfg := validT8Config()

	baseline, err := buildKgoOpts(cfg, emitterOptions{})
	if err != nil {
		t.Fatalf("buildKgoOpts(baseline) err = %v", err)
	}

	withTLS, err := buildKgoOpts(cfg, emitterOptions{
		tlsConfig: &tls.Config{MinVersion: tls.VersionTLS13},
	})
	if err != nil {
		t.Fatalf("buildKgoOpts(withTLS) err = %v", err)
	}

	if got, want := len(withTLS), len(baseline)+1; got != want {
		t.Errorf("withTLS opts = %d; baseline = %d; want delta = 1",
			len(withTLS), len(baseline))
	}
}

// TestBuildKgoOpts_WithSASL_AppendsOneOpt mirrors the TLS assertion for
// the SASL path. kgo.SASL is variadic but we always pass exactly one
// mechanism in v1, so the delta is 1.
func TestBuildKgoOpts_WithSASL_AppendsOneOpt(t *testing.T) {
	t.Parallel()

	cfg := validT8Config()

	baseline, err := buildKgoOpts(cfg, emitterOptions{})
	if err != nil {
		t.Fatalf("buildKgoOpts(baseline) err = %v", err)
	}

	withSASL, err := buildKgoOpts(cfg, emitterOptions{
		saslMechanism: plain.Auth{User: "u", Pass: "p"}.AsMechanism(),
	})
	if err != nil {
		t.Fatalf("buildKgoOpts(withSASL) err = %v", err)
	}

	if got, want := len(withSASL), len(baseline)+1; got != want {
		t.Errorf("withSASL opts = %d; baseline = %d; want delta = 1",
			len(withSASL), len(baseline))
	}
}

// TestBuildKgoOpts_WithTLSAndSASL_AppendsTwoOpts proves the two options
// compose: using both at once adds exactly two kgo.Opt entries. Guards
// against a regression where one option accidentally short-circuits the
// other (e.g. `if tls != nil && sasl == nil` instead of two independent
// if-blocks).
func TestBuildKgoOpts_WithTLSAndSASL_AppendsTwoOpts(t *testing.T) {
	t.Parallel()

	cfg := validT8Config()

	baseline, err := buildKgoOpts(cfg, emitterOptions{})
	if err != nil {
		t.Fatalf("buildKgoOpts(baseline) err = %v", err)
	}

	withBoth, err := buildKgoOpts(cfg, emitterOptions{
		tlsConfig:     &tls.Config{MinVersion: tls.VersionTLS13},
		saslMechanism: plain.Auth{User: "u", Pass: "p"}.AsMechanism(),
	})
	if err != nil {
		t.Fatalf("buildKgoOpts(withBoth) err = %v", err)
	}

	if got, want := len(withBoth), len(baseline)+2; got != want {
		t.Errorf("withBoth opts = %d; baseline = %d; want delta = 2",
			len(withBoth), len(baseline))
	}
}

// TestBuildKgoOpts_NilTLSAndSASL_NoDelta defends against a regression
// where a nil-check was dropped and a bare kgo.DialTLSConfig(nil) / kgo.SASL()
// would be appended. The guarantee: with zero-valued emitterOptions, the
// count matches the old single-arg buildKgoOpts behavior.
func TestBuildKgoOpts_NilTLSAndSASL_NoDelta(t *testing.T) {
	t.Parallel()

	cfg := validT8Config()

	baseline, err := buildKgoOpts(cfg, emitterOptions{})
	if err != nil {
		t.Fatalf("buildKgoOpts(baseline) err = %v", err)
	}

	// Explicit nils — identical contract to zero-value emitterOptions but
	// sharper intent when reading the test log.
	explicitNils, err := buildKgoOpts(cfg, emitterOptions{
		tlsConfig:     nil,
		saslMechanism: nil,
	})
	if err != nil {
		t.Fatalf("buildKgoOpts(explicitNils) err = %v", err)
	}

	if len(explicitNils) != len(baseline) {
		t.Errorf("explicit-nils opts = %d; baseline = %d; want equal",
			len(explicitNils), len(baseline))
	}
}
