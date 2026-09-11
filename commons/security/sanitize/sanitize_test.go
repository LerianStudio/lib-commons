//go:build unit

package sanitize_test

import (
	"encoding/json"
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

func TestStringRedactsBrazilianDocumentAndAccountFields(t *testing.T) {
	t.Parallel()

	// A denylist only covers the shapes someone enumerated, and the shared
	// taxonomy was enumerated in English. The document and bank-account field
	// names a Brazilian ledger actually emits — in a unique-constraint violation,
	// a validator message, a marshaled payload — were not on it.
	tests := []struct {
		name    string
		in      string
		secret  string
		present []string
	}{
		{
			name:    "CPF in a key=value driver error",
			in:      "cpf=12345678909 duplicate key value violates unique constraint",
			secret:  "12345678909",
			present: []string{"duplicate key value", marker},
		},
		{
			name:    "CPF as a JSON key in an echoed body",
			in:      `{"cpf":"123.456.789-09","name":"Maria"}`,
			secret:  "123.456.789-09",
			present: []string{`"name":"Maria"`, marker},
		},
		{
			name:    "CNPJ",
			in:      "cnpj=12345678000195 not found",
			secret:  "12345678000195",
			present: []string{"not found", marker},
		},
		{
			name:    "RG",
			in:      "rg=123456789 failed validation",
			secret:  "123456789",
			present: []string{"failed validation", marker},
		},
		{
			name:    "bank account and branch",
			in:      "conta=00012345 agencia=0001 rejected",
			secret:  "00012345",
			present: []string{"rejected", "conta=" + marker, "agencia=" + marker},
		},
		{
			name:    "account holder name",
			in:      "holder_name=Maria Silva mismatch",
			secret:  "holder_name=Maria",
			present: []string{marker},
		},
		{
			name:    "account holder name in Portuguese",
			in:      "nome_titular=Maria mismatch",
			secret:  "nome_titular=Maria",
			present: []string{marker},
		},
		{
			name:    "generic document field",
			in:      "document=12345678909 invalid",
			secret:  "document=12345678909",
			present: []string{marker},
		},
		{
			name:    "generic document field in Portuguese",
			in:      "documento=12345678909 invalid",
			secret:  "documento=12345678909",
			present: []string{marker},
		},
		{
			// A RANDOM Pix key, not the e-mail or CPF forms: those are already
			// redacted by the bare e-mail pattern and by cpf, so only this shape
			// tells us the field name itself is covered.
			name:    "Pix key",
			in:      "chave_pix=7f3a9b2c-1d4e-4a5b-8c6d-9e0f1a2b3c4d not registered",
			secret:  "7f3a9b2c-1d4e-4a5b-8c6d-9e0f1a2b3c4d",
			present: []string{"not registered", marker},
		},
		{
			name:    "birthdate",
			in:      "birthdate=1988-04-02 mismatch",
			secret:  "birthdate=1988-04-02",
			present: []string{marker},
		},
		{
			name:    "birthdate in Portuguese",
			in:      "data_nascimento=1988-04-02 mismatch",
			secret:  "data_nascimento=1988-04-02",
			present: []string{marker},
		},
		{
			name:    "session id in a Cookie header",
			in:      "upstream refused: Cookie: session=abc123def",
			secret:  "abc123def",
			present: []string{"Cookie", marker},
		},
		{
			name:    "servlet session id",
			in:      "jsessionid=0A1B2C3D4E rejected",
			secret:  "0A1B2C3D4E",
			present: []string{"rejected", marker},
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

func TestStringRedactsApiKeyHeaders(t *testing.T) {
	t.Parallel()

	// An API key travels in its own header at least as often as in
	// Authorization, and the header form carries no '=' for the key=value pass
	// to key on.
	tests := []struct {
		name   string
		in     string
		secret string
		keep   string
	}{
		{name: "X-Api-Key header", in: "GET /v1/x: X-Api-Key: sk-abcdefghij", secret: "sk-abcdefghij", keep: "X-Api-Key"},
		{name: "api-key header", in: "rejected api-key: abcdefghij", secret: "abcdefghij", keep: "api-key"},
		{name: "Proxy-Authorization header", in: "Proxy-Authorization: Basic dXNlcjpwdw==", secret: "dXNlcjpwdw", keep: "Basic"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := sanitize.String(tt.in)

			assert.NotContains(t, got, tt.secret)
			assert.Contains(t, got, tt.keep)
			assert.Contains(t, got, marker)
		})
	}
}

func TestStringRedactsEscapedQuoteForms(t *testing.T) {
	t.Parallel()

	// A body that has been through %q, or nested one JSON document inside
	// another's string value, arrives with every quote backslash-escaped. Both
	// the JSON pass and the Authorization pass anchored on a BARE quote and let
	// the whole shape through.
	tests := []struct {
		name   string
		in     string
		secret string
	}{
		{
			name:   "escaped Authorization with an opaque token",
			in:     `upstream rejected "{\"Authorization\": \"9f2c4b6a8d0e\"}"`,
			secret: "9f2c4b6a8d0e",
		},
		{
			name:   "escaped Authorization with a Bearer scheme",
			in:     `upstream rejected "{\"Authorization\": \"Bearer tok123abc\"}"`,
			secret: "tok123abc",
		},
		{
			name:   "escaped Authorization with a Basic scheme",
			in:     `upstream rejected "{\"Authorization\": \"Basic dXNlcjpwdw==\"}"`,
			secret: "dXNlcjpwdw",
		},
		{
			name:   "escaped around the key only",
			in:     `body {\"password\":"hunter2"}`,
			secret: "hunter2",
		},
		{
			name:   "escaped around the value only",
			in:     `body {"password":\"hunter2\"}`,
			secret: "hunter2",
		},
		{
			name:   "fully escaped JSON body",
			in:     `POST /v1/x body "{\"client_secret\":\"s3cr3tvalue\",\"grant_type\":\"client_credentials\"}" rejected`,
			secret: "s3cr3tvalue",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := sanitize.String(tt.in)

			assert.NotContains(t, got, tt.secret)
			assert.Contains(t, got, marker)
		})
	}
}

func TestStringRedactsSeparatedCardNumbers(t *testing.T) {
	t.Parallel()

	// A PAN is written the way it is printed on the card at least as often as
	// unbroken. Worse than missing it: behind a field name the key=value pass
	// stopped at the first space and redacted ONE group, leaving twelve of
	// sixteen digits in the log and a marker suggesting the line was scrubbed.
	tests := []struct {
		name   string
		in     string
		secret string
		keep   []string
	}{
		{name: "space-separated PAN", in: "charge declined for 4741 8529 6307 4182 at acquirer", secret: "8529", keep: []string{"charge declined", "at acquirer"}},
		{name: "dash-separated PAN", in: "charge declined for 4741-8529-6307-4182 at acquirer", secret: "8529", keep: []string{"at acquirer"}},
		{name: "dot-separated PAN", in: "charge declined for 4741.8529.6307.4182 at acquirer", secret: "8529", keep: []string{"at acquirer"}},
		{name: "separated PAN behind a field name", in: "card_number=4741 8529 6307 4182", secret: "8529", keep: []string{"card_number="}},
		{name: "Amex grouping", in: "amex 3782 822463 10005 declined", secret: "822463", keep: []string{"declined"}},
		{name: "Diners grouping", in: "diners 3852 000002 3237 declined", secret: "000002", keep: []string{"declined"}},
		{
			// A PAN behind a leading four-digit group, where the FIRST twelve
			// digits of the run also satisfy Luhn. Taking the longest window at
			// the earliest offset redacts those twelve and leaves the last eight
			// digits of the real card in the clear; preferring the widest window
			// anywhere in the run takes the card itself.
			name:   "a leading group whose own window also passes Luhn",
			in:     "declined 0001 4741 8529 6307 4182 at acquirer",
			secret: "6307 4182",
			keep:   []string{"0001", "at acquirer"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := sanitize.String(tt.in)

			assert.NotContains(t, got, tt.secret)
			assert.Equal(t, 1, strings.Count(got, marker), "the PAN must be consumed whole, as ONE marker: %q", got)

			for _, want := range tt.keep {
				assert.Contains(t, got, want)
			}
		})
	}
}

func TestStringKeepsSeparatedNumbersThatAreNotCards(t *testing.T) {
	t.Parallel()

	// Same gate as the unbroken run: a grouped number that fails Luhn is an
	// order id, a reference, an invoice — and redacting it silently empties the
	// message this package exists to keep diagnosable.
	tests := []string{
		"order 1234 5678 9012 3456 pending",
		"reference 1234-5678-9012-3456 not found",
		"balance 12345.67 does not settle",
	}

	for _, in := range tests {
		t.Run(in, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, in, sanitize.String(in))
		})
	}
}

func TestStringRedactsEveryURLInARun(t *testing.T) {
	t.Parallel()

	// A broker DSN is routinely a LIST — commons/secretsmanager hands Kafka a
	// comma-separated Brokers string — and the URL token pattern runs to the
	// next whitespace, so the whole list is ONE match. Redacting only its first
	// authority let every later credential through, and a list whose FIRST entry
	// carried no userinfo came back completely untouched: no marker, nothing to
	// tell an operator the line had not been scrubbed.
	tests := []struct {
		name   string
		in     string
		absent []string
		keep   []string
	}{
		{
			name:   "comma-separated list, both entries carry credentials",
			in:     "dial brokers amqp://u1:p1@a:5672,amqp://u2:p2@b:5672 refused",
			absent: []string{"u1", "p1", "u2", "p2"},
			keep:   []string{"a:5672", "b:5672", "refused"},
		},
		{
			name:   "semicolon-separated list",
			in:     "dial amqp://u1:p1@a:5672;amqp://u2:p2@b:5672 refused",
			absent: []string{"u1", "p1", "u2", "p2"},
			keep:   []string{"a:5672", "b:5672"},
		},
		{
			name:   "the first entry has no credentials and the second does",
			in:     "dial amqp://a:5672,amqp://svc:s3cr3t@b:5672 refused",
			absent: []string{"svc:s3cr3t", "s3cr3t"},
			keep:   []string{"a:5672", "b:5672", marker},
		},
		{
			name:   "JSON array of URLs",
			in:     `brokers ["amqp://u1:p1@a:5672","amqp://u2:p2@b:5672"] refused`,
			absent: []string{"u1", "p1", "u2", "p2"},
			keep:   []string{"a:5672", "b:5672"},
		},
		{
			name:   "a URL glued to the tail of a previous one",
			in:     "dial redis://h:6379-redis://u:p@h2 refused",
			absent: []string{"u:p@"},
			keep:   []string{"h2", marker},
		},
		{
			name:   "a scheme preceded by a non-URL host:port pair",
			in:     "dial cache:6379-redis://u:p@h refused",
			absent: []string{"u:p@"},
			keep:   []string{"@h", marker},
		},
		{
			name:   "two URLs inside one JSON object, only the second with credentials",
			in:     `config {"a":"https://x/h","db":"postgres://svc:s3cr3t@db/ledger"}`,
			absent: []string{"s3cr3t"},
			keep:   []string{"https://x/h", "@db/ledger", marker},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := sanitize.String(tt.in)

			for _, secret := range tt.absent {
				assert.NotContains(t, got, secret, "got %q", got)
			}

			for _, want := range tt.keep {
				assert.Contains(t, got, want)
			}
		})
	}
}

func TestStringRedactsSensitiveQueryParameters(t *testing.T) {
	t.Parallel()

	// The key=value value class admits '=', so a URL carrying a credential in
	// its query string is ONE match keyed on the OUTER field name. With
	// "endpoint" or "broker" outside, the pair is judged not sensitive and the
	// inner apikey= is never a candidate at all.
	tests := []struct {
		name   string
		in     string
		secret string
		keep   []string
	}{
		{
			name:   "api key in the query string of a non-sensitive field",
			in:     "call failed endpoint=https://api.example.com/v1?apikey=s3cr3tvalue",
			secret: "s3cr3tvalue",
			keep:   []string{"endpoint=", "api.example.com", "apikey=" + marker},
		},
		{
			name:   "password in the query string",
			in:     "retry_url=https://h/cb?password=s3cr3tvalue failed",
			secret: "s3cr3tvalue",
			keep:   []string{"retry_url=", "password=" + marker},
		},
		{
			name:   "auth token in a broker URL query string",
			in:     "broker=amqps://h/?auth_token=s3cr3tvalue refused",
			secret: "s3cr3tvalue",
			keep:   []string{"broker=", "auth_token=" + marker},
		},
		{
			name:   "libpq sslpassword connection parameter",
			in:     "open postgres://svc@db/ledger?sslmode=verify-full&sslpassword=s3cr3tvalue",
			secret: "s3cr3tvalue",
			keep:   []string{"sslmode=verify-full", "sslpassword=" + marker},
		},
		{
			name:   "access token as a second parameter",
			in:     "GET https://api/v1?page=2&access_token=s3cr3tvalue rejected",
			secret: "s3cr3tvalue",
			keep:   []string{"page=2", "access_token=" + marker},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := sanitize.String(tt.in)

			assert.NotContains(t, got, tt.secret)

			for _, want := range tt.keep {
				assert.Contains(t, got, want, "got %q", got)
			}
		})
	}
}

func TestStringKeepsNonSensitiveQueryParameters(t *testing.T) {
	t.Parallel()

	// The query-parameter pass is keyed on the field name exactly like the
	// others. A paging or filter parameter is what makes a failed request
	// diagnosable and must survive.
	in := "GET https://api/v1/accounts?page=2&limit=50&sort=rank rejected"

	assert.Equal(t, in, sanitize.String(in))
}

// fuzzSecrets is one representative of every credential family the package
// claims to redact. Each is whitespace-delimited in the generated input, which
// is what makes the property TOTAL: every pattern here is anchored on a word
// boundary, so a space on either side is the only context any of them needs.
var fuzzSecrets = []string{
	"postgres://svc:s3cr3t@db.internal:5432/ledger",
	"AKIAIOSFODNN7EXAMPLE",
	"AIzaSyD-1234567890abcdefghijklmnopqrstu",
	"ghp_1234567890abcdefghijklmnopqrstuvwxyzAB",
	"sk_live_1234567890abcdefghij",
	"xoxb-12345678901-abcdefghijkl",
	"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.dBjftJeZ4CVPmB92K27uhbUJU1p1r_wW",
	"maria.silva+ops@lerian.studio",
	"4111111111111111",
	"4741 8529 6307 4182",
}

// FuzzString fuzzes the TEXT AROUND a credential rather than the credential
// itself, which is the only shape in which "the secret must not survive" is a
// property and not a guess: fuzzing the secret too would mostly generate strings
// that are no longer credentials, and every pass would be a false alarm.
//
// What it hunts is a pass INTERACTION — surrounding text that steers an earlier
// pass into carving up the input so a later pattern no longer recognises what is
// left. That is exactly how the grouped-PAN and multi-URL defects behaved, and
// neither was reachable from a hand-written fixture.
func FuzzString(f *testing.F) {
	seeds := []string{
		"",
		"dial tcp:",
		`{"host":"db","password":`,
		"Authorization: Bearer",
		`"{\"Authorization\": \"Bearer`,
		"endpoint=https://api/v1?apikey=",
		"amqp://a:5672,",
		"-----BEGIN RSA PRIVATE KEY-----",
		"cpf=12345678909",
		strings.Repeat("a", 300),
	}

	for _, seed := range seeds {
		f.Add(seed, " refused")
	}

	f.Fuzz(func(t *testing.T, prefix, suffix string) {
		// Bounded so the fuzzer spends its budget on shapes rather than on
		// length; the cost of a pass is linear in the input.
		if len(prefix) > 512 {
			prefix = prefix[:512]
		}

		if len(suffix) > 512 {
			suffix = suffix[:512]
		}

		for _, secret := range fuzzSecrets {
			in := prefix + " " + secret + " " + suffix

			got := sanitize.String(in)

			require.NotContains(t, got, secret, "credential survived with prefix %q suffix %q", prefix, suffix)
			require.Equal(t, got, sanitize.String(got), "re-running the sanitizer must not change an already-sanitized string")
		}
	})
}

func TestStringKeepsTheRestOfTheLineAroundAnAuthHeader(t *testing.T) {
	t.Parallel()

	// The Authorization value runs to the end of the line, which is right when
	// the header sits in prose and wrong when it sits inside a structure: a map
	// dump or a JSON object puts the fields an operator needs — request id, host
	// — AFTER the header, and they were all being consumed with it.
	tests := []struct {
		name   string
		in     string
		secret string
		keep   []string
	}{
		{
			name:   "Go map dump keeps the sibling fields",
			in:     "upstream map[Authorization:[Bearer tok123] X-Request-Id:[7f3a] Host:[api.internal]]",
			secret: "tok123",
			keep:   []string{"X-Request-Id:[7f3a]", "Host:[api.internal]"},
		},
		{
			name:   "semicolon-separated header pairs",
			in:     "Cookie: session=abc123; Path=/; HttpOnly",
			secret: "abc123",
			keep:   []string{"Path=/", "HttpOnly"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := sanitize.String(tt.in)

			assert.NotContains(t, got, tt.secret)

			for _, want := range tt.keep {
				assert.Contains(t, got, want, "got %q", got)
			}
		})
	}
}

func TestStringLeavesRedactedJSONParsable(t *testing.T) {
	t.Parallel()

	// Redaction that breaks the escaping produces a dangling quote, and the log
	// pipeline that was going to parse this line drops it — turning a redacted
	// record into no record at all.
	tests := []struct {
		name   string
		in     string
		secret string
	}{
		{
			name:   "header inside a JSON object",
			in:     `{"a":"1","Authorization":"Bearer tok123","b":"2"}`,
			secret: "tok123",
		},
		{
			name:   "JSON body nested in a JSON string value",
			in:     `{"msg":"upstream rejected {\"Authorization\": \"Bearer tok123\"}","id":"7f3a"}`,
			secret: "tok123",
		},
		{
			name:   "credential field inside a JSON object",
			in:     `{"host":"db","password":"hunter2","port":"5432"}`,
			secret: "hunter2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := sanitize.String(tt.in)

			assert.NotContains(t, got, tt.secret)
			assert.True(t, json.Valid([]byte(got)), "redaction left unparsable JSON: %q", got)
		})
	}
}

// stringerError is a cause that implements fmt.Stringer and fmt.Formatter on top
// of error, which is ordinary for a driver or SDK type. Each extra interface is
// another way for errors.As to hand a caller something whose own printing is the
// raw text.
type stringerError struct{ dsn string }

func (e *stringerError) Error() string  { return "dial " + e.dsn + ": refused" }
func (e *stringerError) String() string { return "dial " + e.dsn }
func (e *stringerError) Format(f fmt.State, _ rune) {
	_, _ = fmt.Fprintf(f, "dial %s", e.dsn)
}

func TestErrorClosesTheInterfaceDoorsToTheCause(t *testing.T) {
	t.Parallel()

	// Withholding Unwrap is not enough on its own. errors.As walks the chain and
	// assigns the FIRST value assignable to the target — so a target naming an
	// interface the wrapper did not implement, but the cause did, reached the
	// cause and printed it. The wrapper now implements the printing interfaces
	// itself, so it is what gets assigned.
	got := sanitize.Error(&stringerError{dsn: "postgres://svc:s3cr3t@db.internal:5432/ledger"})

	t.Run("Stringer target yields the wrapper", func(t *testing.T) {
		t.Parallel()

		var target fmt.Stringer

		require.True(t, errors.As(got, &target))
		assert.NotContains(t, target.String(), "s3cr3t")
	})

	t.Run("Formatter target yields the wrapper", func(t *testing.T) {
		t.Parallel()

		var target fmt.Formatter

		require.True(t, errors.As(got, &target))
		assert.NotContains(t, fmt.Sprintf("%v", target), "s3cr3t")
	})

	t.Run("every print verb stays redacted", func(t *testing.T) {
		t.Parallel()

		for _, verb := range []string{"%v", "%s", "%q", "%+v", "%#v"} {
			assert.NotContains(t, fmt.Sprintf(verb, got), "s3cr3t", "verb %s leaked the cause", verb)
		}
	})

	t.Run("the Unwrap interface target is the documented residual door", func(t *testing.T) {
		t.Parallel()

		// PINNED BECAUSE IT IS A GAP, not because it is desirable. Closing it
		// would mean refusing every interface target, which takes legitimate
		// classification (interface{ SQLState() string } and its kin) down with
		// it. The package doc states the same thing; this keeps the two from
		// drifting apart silently in either direction.
		// The cause is wrapped, which is the ordinary shape: anything that has
		// been through fmt.Errorf("...: %w", err) implements Unwrap.
		wrapped := sanitize.Error(fmt.Errorf("query ledger: %w",
			errors.New("dial postgres://svc:s3cr3t@db.internal:5432/ledger: refused")))

		var target interface{ Unwrap() error }

		require.True(t, errors.As(wrapped, &target), "still reachable: the doc says so")

		reached, ok := target.(error)
		require.True(t, ok)
		assert.Contains(t, reached.Error(), "s3cr3t",
			"this is the gap the doc names: the value reached is the cause, printing raw")
	})

	t.Run("naming the concrete type still classifies", func(t *testing.T) {
		t.Parallel()

		var target *stringerError

		require.ErrorAs(t, got, &target, "a caller that names the type must still reach it")
		assert.Contains(t, target.dsn, "s3cr3t", "classification is the point; this caller asked for it")
	})
}

func TestStringRefusesInputAboveTheBound(t *testing.T) {
	t.Parallel()

	// Every pass is linear, but there are eighteen of them plus the card pass's
	// fixed-point rounds, and the constant is ~400ms per megabyte. An error
	// string that large is a bug upstream, not a message anyone will read, and
	// scanning it stalls whatever goroutine is logging.
	//
	// It is REFUSED, never truncated: a cut landing mid-secret strands a readable
	// prefix that no later pattern can recognise, which is the hazard this
	// package documents at length.
	oversized := strings.Repeat("a", sanitize.MaxInputLen) + " password=hunter2"

	got := sanitize.String(oversized)

	assert.NotContains(t, got, "hunter2")
	assert.Contains(t, got, marker)
	assert.Contains(t, got, fmt.Sprintf("%d", len(oversized)))
	assert.Less(t, len(got), 128, "the refusal is a marker sentence, not a copy of the input")
}

func TestStringAcceptsInputAtTheBound(t *testing.T) {
	t.Parallel()

	filler := strings.Repeat("a", sanitize.MaxInputLen-len(" password=hunter2"))

	got := sanitize.String(filler + " password=hunter2")

	assert.NotContains(t, got, "hunter2")
	assert.Contains(t, got, "password="+marker)
}

// nilDerefError dereferences its receiver, so a typed nil of this type panics on
// Error(). That is the ordinary shape: any method reading a field does it.
type nilDerefError struct{ dsn string }

func (e *nilDerefError) Error() string { return "dial " + e.dsn }

func TestErrorOnTypedNilCause(t *testing.T) {
	t.Parallel()

	// A nil POINTER inside a non-nil error interface is not caught by err == nil,
	// and calling Error() on it panics — inside a redaction helper that exists to
	// be called on an error path, where a panic is the second failure on top of
	// the first.
	var cause error = (*nilDerefError)(nil)

	assert.NotPanics(t, func() {
		assert.Nil(t, sanitize.Error(cause), "a typed nil is a nil error")
	})
}
