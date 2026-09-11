//go:build unit

package sanitize_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/LerianStudio/lib-commons/v7/commons/security/sanitize"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const marker = sanitize.SecretRedactionMarker

func TestStringRedactsURLUserinfo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		in    string
		want  string
		keeps []string
	}{
		{
			name:  "postgres DSN with user and password",
			in:    "dial tcp: postgres://svc_user:s3cr3t@db.internal:5432/ledger?sslmode=require",
			want:  "dial tcp: postgres://" + marker + ":" + marker + "@db.internal:5432/ledger?sslmode=require",
			keeps: []string{"db.internal:5432", "ledger", "sslmode=require"},
		},
		{
			// An unescaped '@' is legal in a password and common in generated
			// ones. The authority's LAST at-sign is the userinfo separator; take
			// the first and the tail of the password walks out in the clear.
			name:  "password containing a literal at-sign",
			in:    "dial postgres://user:p@ssw0rd@db.internal:5432/ledger",
			want:  "dial postgres://" + marker + ":" + marker + "@db.internal:5432/ledger",
			keeps: []string{"db.internal:5432", "ledger"},
		},
		{
			name:  "username containing a literal at-sign",
			in:    "dial postgres://user@corp:hunter2@db.internal:5432/ledger",
			want:  "dial postgres://" + marker + ":" + marker + "@db.internal:5432/ledger",
			keeps: []string{"db.internal:5432"},
		},
		{
			name: "amqp broker URL",
			in:   "amqp://guest:guest@rabbit:5672/%2f",
			want: "amqp://" + marker + ":" + marker + "@rabbit:5672/%2f",
		},
		{
			name: "userinfo with no password",
			in:   "redis://admin@cache:6379",
			want: "redis://" + marker + "@cache:6379",
		},
		{
			name: "no userinfo is left alone",
			in:   "https://api.lerian.studio/v1/ledgers",
			want: "https://api.lerian.studio/v1/ledgers",
		},
		{
			// The fixture deliberately is NOT an e-mail address: an at-sign after
			// the authority is not userinfo, and this row is about that rule
			// alone. An e-mail in the same position is redacted, by the bare-PII
			// pass rather than by this one — see the case below.
			name: "an at-sign in the path is not userinfo",
			in:   "https://api.example.com/users/a@b/x",
			want: "https://api.example.com/users/a@b/x",
		},
		{
			name: "an e-mail address in a path is PII and goes, host and route stay",
			in:   "https://api.example.com/users/a@b.com/x",
			want: "https://api.example.com/users/" + marker + "/x",
		},
		{
			name: "trailing sentence punctuation stays outside the redaction",
			in:   "could not reach postgres://u:p@host/db.",
			want: "could not reach postgres://" + marker + ":" + marker + "@host/db.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := sanitize.String(tt.in)
			assert.Equal(t, tt.want, got)

			for _, keep := range tt.keeps {
				assert.Contains(t, got, keep, "the destination must stay diagnosable")
			}
		})
	}
}

func TestStringRedactsCredentialsByShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		absent  []string
		present []string
	}{
		{
			name:    "authorization header keeps the scheme",
			in:      "GET /v1/x failed: Authorization: Bearer abc.def.ghi",
			absent:  []string{"abc.def.ghi"},
			present: []string{"Authorization", "Bearer", marker},
		},
		{
			name:    "basic auth credential",
			in:      "auth: Basic dXNlcjpwYXNzd29yZA==",
			absent:  []string{"dXNlcjpwYXNzd29yZA"},
			present: []string{"Basic", marker},
		},
		{
			name:    "AWS access key id",
			in:      "signature mismatch for AKIAIOSFODNN7EXAMPLE in us-east-1",
			absent:  []string{"AKIAIOSFODNN7EXAMPLE"},
			present: []string{"us-east-1", marker},
		},
		{
			name:    "AWS temporary access key id",
			in:      "denied for ASIAIOSFODNN7EXAMPLE",
			absent:  []string{"ASIAIOSFODNN7EXAMPLE"},
			present: []string{marker},
		},
		{
			name:    "GCP API key",
			in:      "key AIzaSyD-1234567890abcdefghijklmnopqrstu rejected",
			absent:  []string{"AIzaSyD-1234567890abcdefghijklmnopqrstu"},
			present: []string{marker},
		},
		{
			name:    "GitHub token",
			in:      "remote: ghp_1234567890abcdefghijklmnopqrstuvwxyzAB denied",
			absent:  []string{"ghp_1234567890abcdefghijklmnopqrstuvwxyzAB"},
			present: []string{marker},
		},
		{
			name:    "Stripe live secret key",
			in:      "charge failed with sk_live_1234567890abcdefghij",
			absent:  []string{"sk_live_1234567890abcdefghij"},
			present: []string{marker},
		},
		{
			name:    "Slack bot token",
			in:      "post failed xoxb-12345678901-abcdefghijkl",
			absent:  []string{"xoxb-12345678901-abcdefghijkl"},
			present: []string{marker},
		},
		{
			name:    "bare JWT",
			in:      "invalid token eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.dBjftJeZ4CVPmB92K27uhbUJU1p1r_wW1gFWFOEjXk",
			absent:  []string{"eyJhbGciOiJIUzI1NiJ9"},
			present: []string{marker},
		},
		{
			name:    "Azure SAS signature parameter",
			in:      "https://acct.blob.core.windows.net/c/b?sv=2021-08-06&sig=abc%2Fdef%3D&se=2026-01-01",
			absent:  []string{"abc%2Fdef%3D"},
			present: []string{"sig=" + marker, "sv=2021-08-06", "se=2026-01-01"},
		},
		{
			// THIS ONE PINS THE PASS ORDER. The armored block is the VALUE of a
			// sensitive key, which is how a config-loading error echoes a key.
			// Run key=value first and it eats "-----BEGIN" as the value, leaving
			// no BEGIN marker for the PEM rule to anchor on — so the whole base64
			// body survives into the log. PEM has to go first.
			name: "PEM block as the value of a sensitive key",
			in: "loading signer: private_key=-----BEGIN RSA PRIVATE KEY-----\n" +
				"MIIEowIBAAKCAQEA1234\nabcd+/==\n-----END RSA PRIVATE KEY----- rejected",
			absent:  []string{"MIIEowIBAAKCAQEA1234", "abcd+/=="},
			present: []string{"loading signer:", "rejected", marker},
		},
		{
			name: "PEM private key block",
			in: "bad key: -----BEGIN RSA PRIVATE KEY-----\nMIIEowIBAAKCAQEA1234\nabcd+/==\n" +
				"-----END RSA PRIVATE KEY----- while loading",
			absent:  []string{"MIIEowIBAAKCAQEA1234", "BEGIN RSA PRIVATE KEY"},
			present: []string{"bad key:", "while loading", marker},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := sanitize.String(tt.in)

			for _, secret := range tt.absent {
				assert.NotContains(t, got, secret)
			}

			for _, keep := range tt.present {
				assert.Contains(t, got, keep)
			}
		})
	}
}

func TestStringRedactsByFieldName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "key=value password", in: "connect failed password=hunter2", want: "connect failed password=" + marker},
		{name: "key=value token", in: "token = abc123", want: "token = " + marker},
		{name: "camelCase field name", in: "accessToken=abc123", want: "accessToken=" + marker},
		{name: "AWS key name covered by the default taxonomy", in: "aws_secret_access_key=abc123", want: "aws_secret_access_key=" + marker},
		{name: "SASL field covered by the default taxonomy", in: "sasl_password=abc123", want: "sasl_password=" + marker},

		// The cases below are the ADDENDUM'S REASON TO EXIST: the shared
		// lib-observability taxonomy does not classify any of these, and they are
		// exactly the spellings AWS SDKs and broker clients emit. Verified against
		// redaction.IsSensitiveField with no extras, which returns false for all
		// five.
		{name: "addendum only: pwd", in: "pwd=hunter2", want: "pwd=" + marker},
		{name: "addendum only: signature", in: "signature=abc123", want: "signature=" + marker},
		{name: "addendum only: sessiontoken", in: "sessiontoken=abc123", want: "sessiontoken=" + marker},
		{name: "addendum only: accesskey", in: "accesskey=abc123", want: "accesskey=" + marker},
		{name: "addendum only: secretaccesskey", in: "secretaccesskey=abc123", want: "secretaccesskey=" + marker},
		{name: "non-sensitive field is left alone", in: "timeout=30s", want: "timeout=30s"},
		{name: "non-sensitive field with a number", in: "max_retries=5", want: "max_retries=5"},
		{
			name: "JSON string value by field name",
			in:   `{"host":"db","password":"hunter2"}`,
			want: `{"host":"db","password":"` + marker + `"}`,
		},
		{
			name: "JSON non-string value is not a credential",
			in:   `{"retries":3,"port":5432}`,
			want: `{"retries":3,"port":5432}`,
		},
		{
			name: "JSON with spacing",
			in:   `{"apiKey": "abc123"}`,
			want: `{"apiKey": "` + marker + `"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.want, sanitize.String(tt.in))
		})
	}
}

func TestStringLeavesCleanTextAlone(t *testing.T) {
	t.Parallel()

	tests := []string{
		"",
		"connection refused",
		"pq: relation \"outbox_events\" does not exist",
		"context deadline exceeded after 15s",
		"tenant 9f8e7d6c-5b4a-3210-fedc-ba9876543210 has no ledger",
		"balance 12345.67 does not settle",
	}

	for _, in := range tests {
		t.Run(in, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, in, sanitize.String(in))
		})
	}
}

func TestStringIsIdempotent(t *testing.T) {
	t.Parallel()

	in := "postgres://u:p@host/db password=hunter2 Authorization: Bearer abc.def.ghi"

	once := sanitize.String(in)
	twice := sanitize.String(once)

	assert.Equal(t, once, twice, "re-sanitizing an already sanitized message must not mangle it further")
}

func TestErrorRedactsTheMessage(t *testing.T) {
	t.Parallel()

	cause := errors.New("dial postgres://svc:s3cr3t@db.internal:5432/ledger: refused")

	got := sanitize.Error(cause)

	require.Error(t, got)
	assert.NotContains(t, got.Error(), "s3cr3t")
	assert.Contains(t, got.Error(), "db.internal:5432")
	assert.Contains(t, got.Error(), marker)
}

func TestErrorPreservesTheChain(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("upstream refused")
	wrapped := fmt.Errorf("querying with password=hunter2: %w", sentinel)

	got := sanitize.Error(wrapped)

	require.ErrorIs(t, got, sentinel, "errors.Is must still find the sentinel")
	require.ErrorIs(t, got, wrapped)
	assert.NotContains(t, got.Error(), "hunter2")
}

func TestErrorPreservesTypedCauses(t *testing.T) {
	t.Parallel()

	pgErr := &pgconn.PgError{Code: "28P01", Message: `password authentication failed for user "svc"`}
	wrapped := fmt.Errorf("connect postgres://svc:s3cr3t@db/ledger: %w", pgErr)

	got := sanitize.Error(wrapped)

	var found *pgconn.PgError

	require.ErrorAs(t, got, &found, "errors.As must still classify the driver error")
	assert.Equal(t, "28P01", found.Code)
	assert.NotContains(t, got.Error(), "s3cr3t")
}

func TestErrorOnNil(t *testing.T) {
	t.Parallel()

	assert.NoError(t, sanitize.Error(nil))
}

func TestErrorIsItselfWrappable(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("boom")
	redacted := sanitize.Error(fmt.Errorf("token=abc123: %w", sentinel))

	outer := fmt.Errorf("handling request: %w", redacted)

	require.ErrorIs(t, outer, sentinel)
	assert.NotContains(t, outer.Error(), "abc123")
	assert.Contains(t, outer.Error(), "handling request")
}

func TestErrorDoesNotRedactAnAlreadyCleanMessage(t *testing.T) {
	t.Parallel()

	cause := errors.New("connection refused")

	got := sanitize.Error(cause)

	assert.Equal(t, "connection refused", got.Error())
	require.ErrorIs(t, got, cause)
}

func TestStringHandlesLongInputWithoutTruncating(t *testing.T) {
	t.Parallel()

	// This is a redactor, not a storage bounder: commons/outbox owns the
	// length-bounded variant for the last_error column.
	in := strings.Repeat("a", 4096) + " password=hunter2"

	got := sanitize.String(in)

	assert.Len(t, got, 4096+len(" password=")+len(marker))
	assert.NotContains(t, got, "hunter2")
}

func TestStringRedactsBareCardNumbers(t *testing.T) {
	t.Parallel()

	// Fixtures from commons/outbox's own sanitizer tests: what the outbox
	// already scrubs before writing last_error, this must scrub before the same
	// text reaches a log line or a span.
	tests := []struct {
		name    string
		in      string
		secret  string
		present []string
	}{
		{
			name:    "the outbox's own kitchen-sink fixture",
			in:      "bearer eyJabc.def.ghi api_key=secret123 user@mail.com 4111111111111111",
			secret:  "4111111111111111",
			present: []string{marker},
		},
		{
			name:    "a bare PAN with no field name around it",
			in:      "charge declined for 5500005555555559 at acquirer",
			secret:  "5500005555555559",
			present: []string{"charge declined", "at acquirer", marker},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := sanitize.String(tt.in)

			assert.NotContains(t, got, tt.secret)

			for _, want := range tt.present {
				assert.Contains(t, got, want)
			}
		})
	}
}

func TestStringKeepsLongNumbersThatAreNotCards(t *testing.T) {
	t.Parallel()

	// The Luhn gate is the whole reason a bare 12-to-19-digit run may be touched
	// at all. Without it every epoch-millisecond timestamp, order number and
	// ledger id in an error string disappears, and the message stops being
	// diagnosable — which is a worse failure than the one being fixed, because
	// it is silent.
	tests := []string{
		"failed at unix_ms=1700000000000 while parsing request",
		"order 4111111111111112 not found",
		"balance 12345.67 does not settle",
	}

	for _, in := range tests {
		t.Run(in, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, in, sanitize.String(in))
		})
	}
}

func TestStringRedactsBareEmailAddresses(t *testing.T) {
	t.Parallel()

	// PII the taxonomy classifies as sensitive by FIELD NAME, appearing with no
	// field name at all — which is how a driver or a validator echoes it.
	tests := []struct {
		in     string
		secret string
	}{
		{in: "erro de autenticacao usuario=test@example.com senha=segredo", secret: "test@example.com"},
		{in: "no ledger for operator maria.silva+ops@lerian.studio", secret: "maria.silva+ops@lerian.studio"},
	}

	for _, tt := range tests {
		t.Run(tt.secret, func(t *testing.T) {
			t.Parallel()

			got := sanitize.String(tt.in)

			assert.NotContains(t, got, tt.secret)
			assert.Contains(t, got, marker)
		})
	}
}

func TestStringRedactsAnAuthorizationHeaderWithNoScheme(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      string
		secret  string
		present []string
	}{
		{
			name:    "bare opaque token, no scheme at all",
			in:      "GET /v1/x failed: Authorization: 9f2c4b6a8d0e1f3a5c7b9d0e",
			secret:  "9f2c4b6a8d0e1f3a5c7b9d0e",
			present: []string{"Authorization", marker},
		},
		{
			// The token here STARTS WITH A LETTER, which is what makes this case
			// discriminating: read as "the first word is the scheme", the token
			// itself survives in the clear and only the prose after it is
			// redacted.
			name:    "an opaque token that starts with a letter is not a scheme",
			in:      "Authorization: abc123token456 retried after 3s",
			secret:  "abc123token456",
			present: []string{"Authorization", marker},
		},
		{
			name:    "proxy-authorization is the same header",
			in:      "Proxy-Authorization: Basic dXNlcjpwYXNz",
			secret:  "dXNlcjpwYXNz",
			present: []string{"Proxy-Authorization", "Basic", marker},
		},
		{
			name:    "a known scheme is still kept for readability",
			in:      "GET /v1/x failed: Authorization: Bearer abc.def.ghi",
			secret:  "abc.def.ghi",
			present: []string{"Authorization", "Bearer", marker},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := sanitize.String(tt.in)

			assert.NotContains(t, got, tt.secret)

			for _, want := range tt.present {
				assert.Contains(t, got, want)
			}
		})
	}
}

func TestErrorHidesTheRawCauseFromEveryPrinter(t *testing.T) {
	t.Parallel()

	dsn := "postgres://svc:s3cr3t@db.internal:5432/ledger"
	cause := errors.New("dial " + dsn + ": refused")
	got := sanitize.Error(cause)

	// errors.Unwrap is the hole: it hands a caller the cause, whose own Error()
	// is the raw DSN. Classification must survive; the raw text must not.
	assert.Nil(t, errors.Unwrap(got), "no caller may reach a printable cause")

	for _, format := range []string{"%v", "%s", "%+v", "%#v"} {
		assert.NotContains(t, fmt.Sprintf(format, got), "s3cr3t", "format %s leaked the cause", format)
	}

	assert.NotContains(t, fmt.Errorf("querying ledger: %w", got).Error(), "s3cr3t")
}

func TestErrorStillClassifiesWithoutUnwrap(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("upstream refused")
	pgErr := &pgconn.PgError{Code: "28P01", Message: `password authentication failed for user "svc"`}

	got := sanitize.Error(fmt.Errorf("connect postgres://svc:s3cr3t@db/ledger: %w: %w", sentinel, pgErr))

	require.ErrorIs(t, got, sentinel, "errors.Is must keep reaching the cause")

	var found *pgconn.PgError

	require.ErrorAs(t, got, &found, "errors.As must keep reaching the cause")
	assert.Equal(t, "28P01", found.Code)
}

// dsnError is an error whose concrete type is a struct VALUE, not a pointer —
// the shape that makes %#v dangerous, because default struct formatting prints
// the field contents rather than an address.
type dsnError struct{ DSN string }

func (e dsnError) Error() string { return "dial " + e.DSN + ": refused" }

func TestErrorHidesAValueTypedCauseFromGoSyntaxFormatting(t *testing.T) {
	t.Parallel()

	got := sanitize.Error(dsnError{DSN: "postgres://svc:s3cr3t@db.internal:5432/ledger"})

	assert.NotContains(t, fmt.Sprintf("%#v", got), "s3cr3t")
	assert.NotContains(t, got.Error(), "s3cr3t")

	var found dsnError

	require.ErrorAs(t, got, &found, "classification must still reach a value-typed cause")
}
