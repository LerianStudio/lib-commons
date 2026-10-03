package postgres

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	commons "github.com/LerianStudio/lib-commons/v7/commons"
	"github.com/LerianStudio/lib-commons/v7/commons/obs"
)

// SSLMode is a libpq sslmode a TLS posture may require as its floor.
type SSLMode string

// The sslmodes a TLS posture accepts as its floor, weakest first.
const (
	// SSLModeRequire encrypts but does not verify the server certificate.
	SSLModeRequire SSLMode = "require"
	// SSLModeVerifyCA encrypts and verifies the certificate chain, not the hostname.
	SSLModeVerifyCA SSLMode = "verify-ca"
	// SSLModeVerifyFull encrypts and verifies both the chain and the hostname.
	SSLModeVerifyFull SSLMode = "verify-full"
)

// TLSPosture is the consumer's declared TLS stance for its Postgres
// connections. The library never derives it from the environment: the
// consumer decides, typically from its own deployment class, and passes it in.
type TLSPosture uint8

const (
	// TLSPostureDefault keeps the client's long-standing rule: sslmode must
	// be require, verify-ca or verify-full, unless ALLOW_INSECURE_TLS=true.
	TLSPostureDefault TLSPosture = iota
	// TLSPostureHardened refuses, before dialing, any sslmode weaker than the
	// consumer's floor (MinSSLMode; verify-full when empty). An absent sslmode
	// is refused, and ALLOW_INSECURE_TLS never lifts the floor.
	TLSPostureHardened
	// TLSPostureSaaS is TLSPostureHardened with the floor fixed at verify-full.
	TLSPostureSaaS
)

// String names the posture as it appears in errors and logs.
func (p TLSPosture) String() string {
	switch p {
	case TLSPostureDefault:
		return "default"
	case TLSPostureHardened:
		return "hardened"
	case TLSPostureSaaS:
		return "saas"
	default:
		return "TLSPosture(" + strconv.Itoa(int(p)) + ")"
	}
}

// ErrWeakSSLMode is the sentinel every posture refusal wraps.
var ErrWeakSSLMode = errors.New("postgres: sslmode weaker than the required TLS posture")

// WeakSSLModeError reports a connection setting refused by a TLS posture. It
// carries the sslmode value only, never the DSN or any credential in it.
type WeakSSLModeError struct {
	// Label names the connection: "primary" or "replica" for the client, or
	// the caller's label for CheckSSLMode.
	Label string
	// Setting is the connection setting judged: always "sslmode".
	Setting string
	// Got is the sslmode as written; "" when the DSN does not set it or
	// cannot be parsed.
	Got string
	// Min is the weakest sslmode the posture accepts.
	Min SSLMode
	// Posture is the posture that refused it.
	Posture TLSPosture
}

// Error describes the refusal without the DSN.
func (e *WeakSSLModeError) Error() string {
	if e == nil {
		return ErrWeakSSLMode.Error()
	}

	if e.Got == "" {
		return fmt.Sprintf("postgres-%s: %s is not set (or the DSN cannot be read); the %s TLS posture requires %s or stronger",
			e.Label, e.Setting, e.Posture, e.Min)
	}

	return fmt.Sprintf("postgres-%s: %s %q is weaker than the %s TLS posture allows; it requires %s or stronger",
		e.Label, e.Setting, e.Got, e.Posture, e.Min)
}

// Unwrap returns ErrWeakSSLMode, so errors.Is matches every refusal.
func (e *WeakSSLModeError) Unwrap() error { return ErrWeakSSLMode }

// sslModeStrength orders libpq's sslmodes; an unknown spelling is absent.
// pgx compares sslmode case-sensitively and refuses anything else as invalid.
var sslModeStrength = map[string]int{
	"disable":     0,
	"allow":       1,
	"prefer":      2,
	"require":     3,
	"verify-ca":   4,
	"verify-full": 5,
}

// ValidateTLSPosture reports whether posture and minMode form a valid pair.
// The default posture takes no floor; the hardened posture takes require,
// verify-ca, verify-full or empty (verify-full); the SaaS posture takes only
// verify-full or empty. Every other pair wraps ErrInvalidConfig.
func ValidateTLSPosture(posture TLSPosture, minMode SSLMode) error {
	_, err := postureFloor(posture, minMode)

	return err
}

// postureFloor resolves the weakest sslmode the posture accepts. The default
// posture has no floor of its own and resolves to "".
func postureFloor(posture TLSPosture, minMode SSLMode) (SSLMode, error) {
	switch posture {
	case TLSPostureDefault:
		if minMode != "" {
			return "", fmt.Errorf("%w: MinSSLMode %q needs the hardened or saas TLS posture", ErrInvalidConfig, minMode)
		}

		return "", nil
	case TLSPostureHardened:
		switch minMode {
		case "":
			return SSLModeVerifyFull, nil
		case SSLModeRequire, SSLModeVerifyCA, SSLModeVerifyFull:
			return minMode, nil
		default:
			return "", fmt.Errorf("%w: MinSSLMode %q is not one of require, verify-ca, verify-full", ErrInvalidConfig, minMode)
		}
	case TLSPostureSaaS:
		if minMode != "" && minMode != SSLModeVerifyFull {
			return "", fmt.Errorf("%w: the saas TLS posture requires verify-full; MinSSLMode %q would lower it", ErrInvalidConfig, minMode)
		}

		return SSLModeVerifyFull, nil
	default:
		return "", fmt.Errorf("%w: unknown TLS posture %s", ErrInvalidConfig, posture)
	}
}

// CheckSSLMode runs the client's TLS check on one DSN without opening
// anything: the check New runs for each pool and Migrator.Up runs before
// opening its database. Use it where a pool is opened outside New, such as
// a raw sql.Open, a migration runner, or a DSN from BuildConnectionString.
//
// Under TLSPostureDefault it applies the default rule (require or stronger,
// unless ALLOW_INSECURE_TLS=true). Under a posture it refuses, with a
// *WeakSSLModeError, any sslmode weaker than the floor, an absent sslmode,
// an unknown spelling and an unparseable DSN; PGSSLMODE is not consulted. The
// DSN is read the way pgx reads it: the URL form only for an exact
// postgres:// or postgresql:// prefix (first sslmode wins), the
// keyword/value form otherwise (last sslmode wins). Only the sslmode as
// written is judged: libpq treating require plus sslrootcert as verify-ca is
// not credited. An invalid posture/floor pair wraps ErrInvalidConfig.
func CheckSSLMode(label, dsn string, posture TLSPosture, minMode SSLMode) error {
	if posture == TLSPostureDefault {
		if _, err := postureFloor(posture, minMode); err != nil {
			return err
		}

		return enforceTLSPolicy(context.Background(), nil, label, dsn)
	}

	return CheckSSLModeValue(label, postureSSLMode(dsn), posture, minMode)
}

// CheckSSLModeValue is CheckSSLMode for an sslmode already read out of its
// DSN, such as a configuration field. An empty mode means unset.
func CheckSSLModeValue(label, mode string, posture TLSPosture, minMode SSLMode) error {
	floor, err := postureFloor(posture, minMode)
	if err != nil {
		return err
	}

	if posture == TLSPostureDefault {
		normalized := strings.ToLower(strings.TrimSpace(mode))
		if sslModeStrength[normalized] >= sslModeStrength[string(SSLModeRequire)] || commons.AllowInsecureTLS() {
			return nil
		}

		return fmt.Errorf("postgres-%s: TLS required (set %s=true to bypass)", label, commons.EnvAllowInsecureTLS)
	}

	if strength, known := sslModeStrength[mode]; known && strength >= sslModeStrength[string(floor)] {
		return nil
	}

	return &WeakSSLModeError{Label: label, Setting: "sslmode", Got: mode, Min: floor, Posture: posture}
}

// postureSSLMode reads the sslmode pgx would use from the DSN, exactly as
// written: no trimming, no case folding. A DSN pgx cannot parse yields "".
func postureSSLMode(dsn string) string {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			return ""
		}

		return parsed.Query().Get("sslmode")
	}

	return dsnKeywordValue(dsn, "sslmode")
}

// enforceConnTLS applies the configured posture, or the default rule, to one
// DSN before it is opened.
func enforceConnTLS(ctx context.Context, logger obs.Logger, label, dsn string, posture TLSPosture, minMode SSLMode) error {
	if posture == TLSPostureDefault {
		return enforceTLSPolicy(ctx, logger, label, dsn)
	}

	return CheckSSLMode(label, dsn, posture, minMode)
}
