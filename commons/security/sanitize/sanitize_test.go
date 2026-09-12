//go:build unit

package sanitize_test

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strings"
	"testing"
	"time"

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
		// The API-key header names are classified by the SHARED taxonomy, which
		// splits on non-alphanumerics and treats "key" as a whole token — so the
		// addendum deliberately does NOT repeat them. These rows are what makes
		// that omission safe: if the upstream list ever stops covering them, this
		// fails here rather than silently in a log line.
		{name: "taxonomy covers x-api-key", in: "x-api-key=abc123", want: "x-api-key=" + marker},
		{name: "taxonomy covers api-key", in: "api-key=abc123", want: "api-key=" + marker},
		{name: "taxonomy covers apikey", in: "apikey=abc123", want: "apikey=" + marker},

		// THE ADDENDUM'S SHORT ENTRIES CUT BOTH WAYS. "rg", "conta" and
		// "document" are two to eight characters and match by whole word, so
		// these rows pin that they do not swallow the ordinary field names that
		// merely contain those letters — which is how a denylist starts emptying
		// the messages it was added to protect.
		{name: "conta does not match contact", in: "contact=maria", want: "contact=maria"},
		{name: "rg does not match org_id", in: "org_id=abc123", want: "org_id=abc123"},
		{name: "document does not match documents_count", in: "documents_count=12", want: "documents_count=12"},
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
			// TWELVE DIGITS, THE SHORTEST CARD THE PACKAGE ACCEPTS. Every other
			// card in these tables is 13 to 19 digits long, and the blind spot
			// that left was exactly where a regression lived: widening the
			// candidate shape to 4-4-4-N stopped twelve-digit cards being
			// redacted at all, and nothing here noticed.
			name:   "twelve-digit PAN, the shortest accepted",
			in:     "declined 4222 2222 2222 at acquirer",
			secret: "2222 2222",
			keep:   []string{"declined", "at acquirer"},
		},
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
	// A SENSITIVE PAIR, not a bare value, and it is here because the value class
	// of the key=value pass admits '=': a non-sensitive key in front of this one
	// swallowed it whole and the property never saw it go. Nothing else in this
	// list can find that, since every other entry is anchored on a vendor shape
	// that the bare-value pass catches whatever an earlier pass did to the text.
	"password=hunter2",

	// TWO PAIRS WHOSE SEPARATOR CARRIES WHITESPACE, AND WHOSE VALUE NOTHING ELSE
	// WOULD SAVE. Every entry above is either anchored on a vendor shape or glued
	// to its key, so the bare-value pass rescues it however an earlier pass has
	// carved the text up. "hunter2" is an ordinary word and "12345678901" an
	// ordinary digit run: the field name is the ONLY thing that redacts either,
	// so a mis-parse that loses the name prints the value, which is exactly the
	// defect this list could not see. The space after the '=' is the half that
	// puts the value outside a match the key was absorbed into.
	"password= hunter2",
	"cpf= 12345678901",
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
	for _, seed := range fuzzSeeds() {
		f.Add(seed, " refused")
	}

	f.Fuzz(func(t *testing.T, prefix, suffix string) {
		assertNoSecretSurvives(t, prefix, " ", " ", suffix)
	})
}

// fuzzSeeds is the seed set both fuzz targets start from.
func fuzzSeeds() []string {
	return []string{
		"",
		"dial tcp:",
		`{"host":"db","password":`,
		"Authorization: Bearer",
		`"{\"Authorization\": \"Bearer`,
		"endpoint=https://api/v1?apikey=",
		"amqp://a:5672,",
		"-----BEGIN RSA PRIVATE KEY-----",
		"cpf=12345678909",
		// A non-sensitive key and a dangling '=', which is what makes the pair
		// after it the VALUE of this one instead of a pair of its own.
		"opt =",
		// One seed in unicode territory: a non-breaking space where the parser
		// expects an ordinary one, a zero-width space inside a field name, and a
		// long s — which folds to ASCII 's' under (?i) while \b, an ASCII rule,
		// does not count it as a word character. That disagreement is what made
		// the key=value rebuild drop bytes, and nothing in the seed set went
		// anywhere near it.
		"cpf\u00a0=\u200bpassword=\u017f",
		strings.Repeat("a", 300),
	}
}

// fuzzSeparators are the joins a credential is actually written against: the
// '=' of a pair, the '@' of an authority, the '://' of a scheme, the '&' of a
// query, the '#' a fragment is mistaken for, a path '/'. FuzzStringGlued picks
// one for each side of the secret.
//
// THE EMPTY STRING IS DELIBERATELY NOT IN THE SET, and leaving it out is what
// keeps the property total rather than a guess. Every pattern in this package
// anchors on a word boundary, so gluing fuzzer-chosen text straight onto the
// secret makes a DIFFERENT word: "xAKIAIOSFODNN7EXAMPLE" is not an access key
// id and "apassword=hunter2" is not the field "password". Asserting that those
// must be redacted would be asserting something false. Every entry here is a
// non-word byte, which is the boundary the anchors need and the only context
// any of them gets.
var fuzzSeparators = []string{" ", "=", " =", "= ", "@", "://", "&", "#", "/", "\t", "\v"}

// FuzzStringGlued is FuzzString with the two joins around the credential under
// the fuzzer's control instead of fixed at a space.
//
// IT EXISTS BECAUSE THE SPACES WERE HIDING A WHOLE CLASS. With " " on both
// sides, "<prefix>=<key>= <secret>" and "<key>=<secret>@host" were unreachable,
// so the key=value walker's habit of absorbing the next pair's key into a value
// was invisible to the fuzzer even after it shipped as a leak. It is a second
// target rather than two more arguments on the first so the sixteen committed
// FuzzString corpus files keep loading unchanged; the property body is shared.
//
// BOTH TARGETS NEED AN ANCHORED -fuzz FLAG. The flag takes a regexp, so
// `-fuzz=FuzzString` now matches this target as well as FuzzString and go test
// refuses to run either. Spell them `-fuzz='^FuzzString$'` and
// `-fuzz='^FuzzStringGlued$'`.
func FuzzStringGlued(f *testing.F) {
	// THE JOINS ARE PART OF THE SEED CORPUS, NOT ONLY OF THE MUTATION SPACE.
	// CI runs the seeds without fuzzing, so seeding every one of them with
	// byte(0) — a space on both sides — left the shape this target exists for,
	// "<prefix>=<key>= <secret>", resting on whichever entries the fuzzer had
	// already found and committed. Spreading the two indices over the separator
	// set puts '=' and ' =' in the seeds themselves.
	for i, seed := range fuzzSeeds() {
		f.Add(seed, " refused", byte(i%len(fuzzSeparators)), byte((i+2)%len(fuzzSeparators)))
	}

	f.Fuzz(func(t *testing.T, prefix, suffix string, before, after byte) {
		assertNoSecretSurvives(t,
			prefix,
			fuzzSeparators[int(before)%len(fuzzSeparators)],
			fuzzSeparators[int(after)%len(fuzzSeparators)],
			suffix)
	})
}

// assertNoSecretSurvives is the property both fuzz targets assert: every
// credential family planted in fuzzer-chosen text goes, and the output is a
// fixed point.
func assertNoSecretSurvives(t *testing.T, prefix, before, after, suffix string) {
	t.Helper()

	// Bounded so the fuzzer spends its budget on shapes rather than on
	// length. Length is not a free dimension here: a regexp pass is linear,
	// but a long run of digit groups becomes one over-long card candidate and
	// is then searched a window of whole groups at a time, which is quadratic
	// in the number of groups. Left unbounded the fuzzer would spend its whole
	// budget inside that scan on one input.
	if len(prefix) > 512 {
		prefix = prefix[:512]
	}

	if len(suffix) > 512 {
		suffix = suffix[:512]
	}

	for _, secret := range fuzzSecrets {
		in := prefix + before + secret + after + suffix

		got := sanitize.String(in)

		require.NotContains(t, got, secret,
			"credential survived with prefix %q before %q after %q suffix %q", prefix, before, after, suffix)
		require.Equal(t, got, sanitize.String(got),
			"re-running the sanitizer must not change an already-sanitized string")
	}
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

// TestStringStaysBoundedOnAGroupedDigitRunAtTheLimit is a RUNTIME regression
// test, not a behaviour one.
//
// A run of 4-digit groups that is NOT a card — three hundred grouped ledger ids
// on one line, a fixed-width report pasted into an error — is the worst input
// this package has, because the whole run becomes ONE over-long candidate and
// the window scan then walks every window of whole groups inside it. With the
// window width unbounded that scan is cubic in the number of groups: measured
// 15.6 ms at 1 KB, 922 ms at 4 KB, 6.9 s at 8 KB and 7m13s at 32 KB, which at
// MaxInputLen is most of an hour — on whatever goroutine happened to be writing
// a log line.
//
// The bound is deliberately generous (a hundred times the measured cost) so
// ordinary CI noise can never flake it. What it catches is the shape of the
// curve coming back, not a few milliseconds of drift.
//
// IT FAILS AFTER THE BOUND RATHER THAN WAITING FOR THE CALL TO RETURN. A cubic
// regression does not take slightly too long, it takes an hour, and a test that
// waits for the answer before checking the clock reports that as a CI hang with
// no output rather than as a failure.
func TestStringStaysBoundedOnAGroupedDigitRunAtTheLimit(t *testing.T) {
	t.Parallel()

	assertStringReturnsWithin(t, fillToBound("1234 "), "grouped four-digit ledger ids")
}

// fillToBound builds exactly MaxInputLen bytes of the given shape, since the
// bound is what the package admits and therefore what an attacker or an unlucky
// report gets to send.
func fillToBound(unit string) string {
	filled := strings.Repeat(unit, sanitize.MaxInputLen/len(unit)+1)

	return filled[:sanitize.MaxInputLen]
}

// coverageBoundFactor is how much slower the instrumented build is, rounded up
// and then some. Measured at the bound on this package's five shapes: the card
// scan is 11.5x slower under -race -covermode=atomic than under -race alone
// (42.3 s worst, against 3.7), the other four 1.3-1.5x, and without -race the
// worst of the five is 1.77 s against the ordinary 2 s ceiling. Six keeps the
// doctrine's margin on both builds — 120 s against 42.3, 12 s against 1.77 —
// without turning either into a ceiling that catches nothing.
const coverageBoundFactor = 6

// assertStringReturnsWithin FAILS AFTER THE BOUND RATHER THAN WAITING FOR THE
// CALL TO RETURN. A regression in the card scan does not take slightly too long,
// it takes minutes to an hour, and a test that waits for the answer before
// checking the clock reports that as a CI hang with no output rather than as a
// failure.
//
// The bound is deliberately generous — two orders of magnitude above the
// measured cost — so ordinary CI noise can never flake it. What it catches is
// the shape of the curve coming back, not a few milliseconds of drift.
func assertStringReturnsWithin(t *testing.T, input, shape string) {
	t.Helper()

	// A FUZZING BUILD runs this package under its own instrumentation and for as
	// long as the fuzzer wants, so a wall-clock ceiling there measures neither
	// the scan nor a fixed factor over it.
	if f := flag.Lookup("test.fuzz"); f != nil && f.Value.String() != "" {
		t.Skip("wall-clock bounds are not meaningful under the instrumented fuzzing build")
	}

	bound := stringTimingBound

	// COVERAGE IS A CONSTANT FACTOR, NOT A DIFFERENT CURVE, AND CI ONLY EVER
	// RUNS THIS PACKAGE UNDER IT. `make coverage-unit` is the shared workflow's
	// ONLY unit run and it passes -race AND -covermode=atomic; counting every
	// statement of the card scan's innermost loop then took the back-to-back
	// card shape from 3.7 s to past 20, so the ceiling that exists to catch a
	// curve coming back was failing on the instrumentation instead. The bound is
	// raised rather than skipped: a skip would have removed every bound guard
	// this package has from the only build CI runs.
	if testing.CoverMode() != "" {
		bound *= coverageBoundFactor
	}

	done := make(chan time.Duration, 1)

	go func() {
		start := time.Now()
		sanitize.String(input)
		done <- time.Since(start)
	}()

	select {
	case elapsed := <-done:
		assert.Less(t, elapsed, bound, "String() on %d bytes of %s took %s", len(input), shape, elapsed)
	case <-time.After(bound):
		t.Fatalf("String() on %d bytes of %s did not return within %s", len(input), shape, bound)
	}
}

// TestStringStaysBoundedOnBackToBackCardNumbersAtTheLimit is the OTHER end of the
// card scan's cost, and the one the width cap did not reach.
//
// A line that is nothing but card numbers — a batch import echoing the rows it
// rejected, a reconciliation dump — makes every window the scan tries a real
// card, so it redacts, then starts again on what is left. Re-grouping the
// remainder and re-walking every width once per card found turned 64 KiB of this
// shape into a minute of CPU on whatever goroutine was writing the log line.
func TestStringStaysBoundedOnBackToBackCardNumbersAtTheLimit(t *testing.T) {
	t.Parallel()

	assertStringReturnsWithin(t, fillToBound("4111 1111 1111 1111 "), "back-to-back card numbers")
}

// TestStringStaysBoundedOnANestedKeyChainAtTheLimit is the OTHER quadratic in
// this package, and it has nothing to do with card numbers.
//
// The key=value value class runs to the next whitespace, comma, semicolon or
// ampersand, so a line with none of those is ONE value however many '=' it
// holds: every key in "a=b=a=b=..." owns a value reaching the end of the input.
// Re-running the full pattern for each of them re-measured the same tail every
// time. 64 KiB of this shape cost 50 seconds inside one log call -- 96 before
// the walker learned to step past a key it had already judged -- on whatever
// goroutine happened to be writing the line.
func TestStringStaysBoundedOnANestedKeyChainAtTheLimit(t *testing.T) {
	t.Parallel()

	assertStringReturnsWithin(t, fillToBound("a=b="), "a chain of nested keys")
}

// TestStringStaysBoundedOnAKeyChainInFrontOfACredentialAtTheLimit is the same
// shape with something to find at the end of it, which is the expensive half.
//
// With no field name in the chain the walk gives up at the first key it cannot
// use; with one at the end it descends the whole way, which is what the old
// recursion did once per level over the whole remaining value. 64 KiB of it took
// three minutes and eleven seconds.
func TestStringStaysBoundedOnAKeyChainInFrontOfACredentialAtTheLimit(t *testing.T) {
	t.Parallel()

	assertStringReturnsWithin(t,
		fillToBound("a=")[:sanitize.MaxInputLen-16]+"password=hunter2",
		"a chain of nested keys in front of a credential")
}

func TestStringRedactsASensitivePairInsideANonSensitiveValue(t *testing.T) {
	t.Parallel()

	// The key=value value class admits '=', so a NON-sensitive key immediately
	// before a sensitive pair swallows it: "opt = password=hunter2" is one pair
	// keyed on "opt", judged harmless, and the credential travels on under a
	// marker-free line nobody looks at twice. An acquirer response or a driver
	// message that puts any word and an '=' in front of the real field is enough.
	tests := []struct {
		name   string
		in     string
		secret string
		want   string
	}{
		{
			name:   "a config key in front of a password",
			in:     "opt = password=hunter2",
			secret: "hunter2",
			want:   "opt = password=" + marker,
		},
		{
			name:   "an acquirer response in front of a card verification code",
			in:     "POST /charge =cvc=999 rc=05",
			secret: "=999",
			want:   "POST /charge =cvc=" + marker + " rc=05",
		},
		{
			name:   "prose in front of a Brazilian tax id",
			in:     "charge declined =cpf=12345678909",
			secret: "12345678909",
			want:   "charge declined =cpf=" + marker,
		},
		{
			name:   "a broker option in front of the SASL password",
			in:     "opt=sasl_password=s3cr3t",
			secret: "s3cr3t",
			want:   "opt=sasl_password=" + marker,
		},
		{
			name:   "several non-sensitive keys deep",
			in:     "a=b=c=password=hunter2",
			secret: "hunter2",
			want:   "a=b=c=password=" + marker,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := sanitize.String(tt.in)

			assert.NotContains(t, got, tt.secret)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, got, sanitize.String(got), "the rescan must stay idempotent")
		})
	}
}

func TestStringKeepsANonSensitivePairInsideANonSensitiveValue(t *testing.T) {
	t.Parallel()

	// The rescan looks for a SENSITIVE inner name and nothing else. A paging
	// parameter, a filter or a status code carried inside another value is what
	// makes the line diagnosable and must come back verbatim.
	for _, in := range []string{
		"opt = page=2",
		"GET https://api/v1/accounts?page=2&limit=50&sort=rank rejected",
		"retry = backoff=250ms attempt=3",
	} {
		assert.Equal(t, in, sanitize.String(in))
	}
}

func TestStringRedactsAPEMBlockWhoseEndLineIsMissing(t *testing.T) {
	t.Parallel()

	// The rule required a closing -----END line, and a block that lost one is
	// the ORDINARY accident, not an exotic case: a secret pasted out of a
	// kubectl output, a value truncated by a config loader, a key read from a
	// file that ended without its last line. The BEGIN line then matched no
	// rule of its own, the key=value pass took "-----BEGIN" as the value of
	// private_key= and stopped at the first space, and what reached the log was
	// the entire armored body behind a marker asserting the line had been
	// scrubbed — worse than no redaction, because it tells the reader there is
	// nothing left to find.
	tests := []struct {
		name   string
		in     string
		absent []string
	}{
		{
			name: "headless private key behind a field name",
			in: "loading signer: private_key=-----BEGIN RSA PRIVATE KEY-----\n" +
				"MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQ\n" +
				"hkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQDLVtBjTm3xEOtq8H\n",
			absent: []string{"MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQ", "DLVtBjTm3xEOtq8H"},
		},
		{
			name:   "headless certificate followed by prose",
			in:     "-----BEGIN CERTIFICATE-----\nMIIBkTCB+wIJAKZLVtBjTm3x\n dial failed",
			absent: []string{"MIIBkTCB+wIJAKZLVtBjTm3x"},
		},
		{
			// An ENCRYPTED key carries RFC 1421 headers, and those headers carry
			// '-'. Any rule that reads the body as a run of base64-legal bytes
			// stops dead on the first one and leaves everything after it —
			// including the whole armored payload — in the clear, which is the
			// same failure this test exists to close, on a block that is not
			// even malformed.
			name: "encrypted key whose headers contain dashes",
			in: "-----BEGIN RSA PRIVATE KEY-----\nProc-Type: 4,ENCRYPTED\n" +
				"DEK-Info: DES-EDE3-CBC,0123456789ABCDEF\n\nMIIEvQIBADANBgkq\n" +
				"-----END RSA PRIVATE KEY-----",
			absent: []string{"MIIEvQIBADANBgkq", "DEK-Info", "ENCRYPTED"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := sanitize.String(tt.in)

			for _, secret := range tt.absent {
				assert.NotContains(t, got, secret, "got %q", got)
			}

			assert.Contains(t, got, marker)
			assert.Equal(t, got, sanitize.String(got), "re-running must not change it")
		})
	}
}

func TestStringLeavesACardGluedToItsNeighbourAlone(t *testing.T) {
	t.Parallel()

	// PINNED BECAUSE IT IS A GAP, not because it is desirable, exactly like the
	// Unwrap residual door above. Every card shape is anchored on a word
	// boundary, so a PAN with an acquirer's response code run onto the end of it
	// is never a candidate at all.
	//
	// Closing it means dropping the boundary, and that is worse than the gap:
	// every 12-to-19-digit window inside every longer identifier becomes a
	// candidate, and roughly one arbitrary identifier in ten satisfies Luhn by
	// chance — so the package would start silently emptying out the correlation
	// ids and ledger ids it exists to keep readable, which is the failure it was
	// written to avoid. The package doc states the same gap; this keeps the two
	// from drifting apart silently in either direction.
	tests := []struct {
		name string
		in   string
	}{
		{name: "digits run onto the end of the PAN", in: "41111111111111119999"},
		{name: "letters on both sides of the PAN", in: "refA4111111111111111B"},
		{name: "the PAN glued to a field value with no separator", in: "txn=ORD4111111111111111"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			assert.Equal(t, tt.in, sanitize.String(tt.in),
				"this is the gap the doc names: a glued PAN is not a candidate")
		})
	}

	// The same digits with a boundary around them ARE redacted, which is what
	// makes the three above a boundary gap rather than a broken Luhn gate.
	assert.NotContains(t, sanitize.String("pan 4111111111111111 9999"), "4111111111111111")
}

func TestStringRedactsTheVendorShapesTheFirstPassMissed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		in     string
		secret string
	}{
		{
			// Base64url padding is legal and libraries emit it. The class did not
			// admit '=', so a padded token matched up to the pad and the pattern
			// then failed its closing boundary — the whole JWT survived.
			name:   "JWT with base64 padding",
			in:     "verify: eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0=.dBjftJeZ4CVPmB92K27uhbUJU1p1r_wW= rejected",
			secret: "eyJzdWIiOiIxIn0=",
		},
		{
			// alg:none carries an EMPTY signature, and it is the token shape most
			// worth catching: it is what an attacker sends. The third segment was
			// required to be non-empty, so this one was never matched at all.
			name:   "alg:none JWT with an empty signature",
			in:     "token eyJhbGciOiJub25lIn0.eyJzdWIiOiJhZG1pbiIsInJvbGUiOiJyb290In0. refused",
			secret: "eyJzdWIiOiJhZG1pbiIsInJvbGUiOiJyb290In0",
		},
		{
			// Fine-grained PATs replaced the ghp_ family and have a different
			// prefix entirely, so the ghp_/gho_/ghu_/ghs_/ghr_ rule never saw one.
			name:   "GitHub fine-grained personal access token",
			in:     "clone failed: github_pat_11ABCDEFG0abcdefghijklmnopqrstuvwxyz1234567890 denied",
			secret: "github_pat_11ABCDEFG0abcdefghijklmnopqrstuvwxyz1234567890",
		},
		{
			name:   "Slack app-level token",
			in:     "socket mode: xapp-1-A01234567-1234567890123-abcdef0123456789 invalid",
			secret: "xapp-1-A01234567-1234567890123-abcdef0123456789",
		},
		{
			name:   "Slack token-rotation refresh token",
			in:     "refresh: xoxe-1-My01234567890abcdefghijklmn expired",
			secret: "xoxe-1-My01234567890abcdefghijklmn",
		},
		{
			// Tools that lowercase their output exist, and the armor is still a
			// private key whatever case the label is written in.
			name:   "PEM block with a lowercase label",
			in:     "-----begin rsa private key-----\nMIIEvQIBADANBgkqhkiG9w0\n-----end rsa private key-----",
			secret: "MIIEvQIBADANBgkqhkiG9w0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := sanitize.String(tt.in)

			assert.NotContains(t, got, tt.secret, "got %q", got)
			assert.Contains(t, got, marker)
			assert.Equal(t, got, sanitize.String(got), "re-running must not change it")
		})
	}
}

func TestStringRedactsACardPrintedAsThreeGroupsAndAShortTail(t *testing.T) {
	t.Parallel()

	// A 13, 14 or 15 digit card is printed four-four-four-and-the-rest, which is
	// how the shorter Visa, Diners and Amex numbers sit on the card. The uniform
	// grouped shape took the first THREE groups greedily — twelve digits, which
	// is card-length, so it failed Luhn and was left alone as an ordinary
	// identifier rather than searched — and the short tail never joined anything.
	//
	// Behind a field name that is the headless-PEM failure again: the key=value
	// pass stops at the first space, so the line went to the log as
	// "pan=**** 8224 6310 005" — eleven digits of a live card behind a marker
	// asserting the line had been scrubbed.
	cards := []struct{ name, pan string }{
		{name: "Amex 15 as 4-4-4-3", pan: "3782 8224 6310 005"},
		{name: "Diners 14 as 4-4-4-2", pan: "3056 9309 0259 04"},
		{name: "Visa 13 as 4-4-4-1", pan: "4222 2222 2222 2"},
	}

	contexts := []struct{ name, before, after string }{
		{name: "bare"},
		{name: "behind pan=", before: "pan="},
		{name: "behind card=", before: "card="},
		{name: "mid-sentence", before: "declined ", after: " at 12:00"},
	}

	for _, card := range cards {
		for _, ctx := range contexts {
			t.Run(card.name+" "+ctx.name, func(t *testing.T) {
				t.Parallel()

				got := sanitize.String(ctx.before + card.pan + ctx.after)

				// Exact equality is the assertion that no digit of the PAN
				// survives AND that the rest of the line is untouched. Checking
				// only for absence would pass on a line that redacted the
				// timestamp too.
				assert.Equal(t, ctx.before+marker+ctx.after, got)

				// The equality above is what asserts no digit of the card
				// survives; this names WHICH group leaked when one does. Groups
				// of a single digit are skipped because a lone digit occurs in
				// ordinary surrounding text — the timestamp in this very table.
				for _, group := range strings.Fields(card.pan) {
					if len(group) > 1 {
						assert.NotContains(t, got, group, "a group of the card survived in %q", got)
					}
				}

				assert.Equal(t, got, sanitize.String(got), "re-running must not change it")
			})
		}
	}
}

func TestStringRedactsATwelveDigitCardFollowedByAShortTail(t *testing.T) {
	t.Parallel()

	// THE SHORT-TAIL BRANCH TOOK THE OFFSET AND GAVE NOTHING BACK.
	//
	// Widening the candidate shape to 4-4-4-N so a 13, 14 or 15 digit card is
	// caught whole cost the reading underneath it. The branch matches first at a
	// given offset, so "4222 2222 2222 999" is offered to Luhn as fifteen
	// digits; that fails; the candidate is handed back unchanged AND the offset
	// is consumed, so the twelve-digit reading — which is card-length and passes
	// Luhn — is never offered at all. redactCardInsideRun does not rescue it
	// either: it deliberately skips runs that are already card-length.
	//
	// A response code after the PAN is what an acquirer decline looks like, so
	// this is not an exotic shape. Behind a field name it was the documented
	// failure once more: "pan=4222 2222 2222 123" reached the log as
	// "pan=**** 2222 2222 123", eight digits of a live card sitting beside a
	// marker asserting the line had been scrubbed.
	tests := []struct{ name, in, want string }{
		{
			name: "decline with a response code after the PAN",
			in:   "declined 4222 2222 2222 999 at 12:00",
			want: "declined " + marker + " 999 at 12:00",
		},
		{
			name: "behind a field name",
			in:   "pan=4222 2222 2222 123",
			want: "pan=" + marker + " 123",
		},
		{
			name: "no tail at all still redacts",
			in:   "card 4222 2222 2222 paid",
			want: "card " + marker + " paid",
		},
		{name: "one-digit tail", in: "4222 2222 2222 1", want: marker + " 1"},
		{name: "two-digit tail", in: "4222 2222 2222 12", want: marker + " 12"},
		{name: "three-digit tail", in: "4222 2222 2222 123", want: marker + " 123"},
		{name: "dash separated", in: "4222-2222-2222-999", want: marker + "-999"},
		{name: "dot separated", in: "4222.2222.2222.999", want: marker + ".999"},

		// BOTH READINGS VALID: the longer one wins and the tail goes with it.
		// This is the row that stops the retry from being written as "always
		// prefer the head", which would leave the last three digits of a live
		// 15-digit card in the log.
		{name: "fifteen-digit reading also passes Luhn", in: "4222 2222 2222 101", want: marker},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := sanitize.String(tt.in)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, got, sanitize.String(got), "re-running must not change it")
		})
	}
}

func TestStringReadsAHashBeforeAnAtSignAsUserinfo(t *testing.T) {
	t.Parallel()

	// TWO PASSES DISAGREED ABOUT WHETHER '#' ENDS AN AUTHORITY, AND ONE OF THEM
	// REWROTE THE BYTE THAT DECIDED IT.
	//
	// redactOneURL ended the authority at the first '/', '?' or '#', so in
	// "A://keY=#&@" there was no '@' inside the authority and the URL pass left
	// the line alone. The key=value pass then redacted "keY=#" to "keY=****",
	// deleting the '#'. On a SECOND run the authority was "keY=****&", the '@'
	// was found, and the userinfo collapsed to a marker.
	//
	// A sanitizer whose output is not a fixed point rewrites evidence every time
	// anything sanitizes twice: a retry, a second handler, a log shipper. The
	// second run's output is the one that redacts, so it is the one pinned here.
	tests := []struct{ name, in, want string }{
		{
			name: "hash before the at-sign",
			in:   "A://keY=#&@",
			want: "A://" + marker + "@",
		},
		{
			name: "hash before the at-sign, followed by a real credential URL",
			in:   "A://keY=#&@ next http://u:p@h",
			want: "A://" + marker + "@ next http://" + marker + ":" + marker + "@h",
		},

		// CONTROLS. Each was already a fixed point and must stay one.
		{name: "control: no hash", in: "A://key=v&@", want: "A://" + marker + "@"},
		{name: "control: plain userinfo", in: "A://key=v@", want: "A://" + marker + "@"},
		{name: "control: already redacted value", in: "A://x=****&@", want: "A://" + marker + "@"},
		{name: "control: named host", in: "http://key=****&@host", want: "http://" + marker + "@host"},

		// CONTROL. A real fragment, after a path, that happens to contain '@'.
		// The authority ended at the '/' long before the '#', so this must not
		// move: the host is not userinfo and nothing here is a credential.
		{name: "control: at-sign inside a real fragment", in: "http://h/p#frag@x", want: "http://h/p#frag@x"},

		// THE PRICE, RECORDED RATHER THAN DISCOVERED LATER. With no path or
		// query before it, a fragment holding an '@' now reads as userinfo and
		// the host is redacted with it. RFC 3986 does not allow '#' inside an
		// authority at all, so this shape is already malformed as a URL, and the
		// cost is a hostname lost from a log line rather than a credential kept
		// in one. Measured at 994 lines of 5,024 changing verdict, none of them
		// a new leak.
		{name: "accepted over-reach: fragment with an at-sign and no path", in: "http://h#frag@x", want: "http://" + marker + "@x"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := sanitize.String(tt.in)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, got, sanitize.String(got), "re-running must not change it")
		})
	}
}

func TestStringRedactsAFieldNameStolenIntoTheValueSlot(t *testing.T) {
	t.Parallel()

	// A TOKEN IN FRONT OF A SENSITIVE FIELD NAME STOLE THE KEY SLOT, AND THE
	// FIELD NAME ENDED UP IN THE VALUE SLOT WITH ITS OWN VALUE ORPHANED.
	//
	// The key=value scanner read "AKIAIOSFODNN7EXAMPLE =Rg" as one pair: key
	// "AKIAIOSFODNN7EXAMPLE", value "Rg". That key is not a sensitive name, so
	// the pair was left alone, and the " =0" behind it belonged to nothing. The
	// RG went to the log in the clear.
	//
	// It surfaced as non-idempotence because the bare-credential pass then
	// replaced the stealing token with a marker, and a marker cannot start a
	// key, so a SECOND run tokenised "Rg =0" correctly and redacted it. The
	// second run's output was the right answer all along; the first run was a
	// miss. Pinning the fixed point is therefore pinning the correct redaction.
	tests := []struct{ name, in, want string }{
		{
			name: "an AWS key id steals the slot in front of a sensitive name",
			in:   "0 AKIAIOSFODNN7EXAMPLE =Rg =0",
			want: "0 " + marker + " =Rg =" + marker,
		},
		{
			name: "a GitHub token steals the slot",
			in:   "0 ghp_abcdefghijklmnopqrstuvwxyz0123456789 =Rg =0",
			want: "0 " + marker + " =Rg =" + marker,
		},
		{
			// THE THIEF DOES NOT HAVE TO BE A CREDENTIAL. This is the row that
			// separates a structural fix from one that only rescues the shapes
			// the bare-credential pass happens to recognise: an ordinary word
			// steals the slot just as well, and no later pass rewrites it.
			name: "an ordinary word steals the slot",
			in:   "plainword =cpf =0",
			want: "plainword =cpf =" + marker,
		},
		{
			name: "no separating space",
			in:   "0 AKIAIOSFODNN7EXAMPLE =cpf=0",
			want: "0 " + marker + " =cpf=" + marker,
		},

		// ONE ROW PER BYTE [[:space:]] ADMITS. The lookahead that decides
		// whether a value is really the next key is built from the pattern's own
		// separator class for exactly this reason: the first cut hand-wrote
		// " \t", and \n, \v, \f and \r kept leaking. The fuzzer found the form
		// feed in eleven seconds.
		{name: "separated by a space", in: "0 AKIAIOSFODNN7EXAMPLE =Rg =0", want: "0 " + marker + " =Rg =" + marker},
		{name: "separated by a tab", in: "0 AKIAIOSFODNN7EXAMPLE =Rg\t=0", want: "0 " + marker + " =Rg\t=" + marker},
		{name: "separated by a form feed", in: "0 AKIAIOSFODNN7EXAMPLE =Rg\f=0", want: "0 " + marker + " =Rg\f=" + marker},
		{name: "separated by a newline", in: "0 AKIAIOSFODNN7EXAMPLE =Rg\n=0", want: "0 " + marker + " =Rg\n=" + marker},
		{name: "separated by a carriage return", in: "0 AKIAIOSFODNN7EXAMPLE =Rg\r=0", want: "0 " + marker + " =Rg\r=" + marker},
		{name: "separated by a vertical tab", in: "0 AKIAIOSFODNN7EXAMPLE =Rg\v=0", want: "0 " + marker + " =Rg\v=" + marker},

		// CONTROLS. Both were already fixed points and must stay ones.
		{name: "control: the pair on its own", in: "Rg =0", want: "Rg =" + marker},
		{name: "control: already-redacted thief", in: "0 **** =Rg =0", want: "0 **** =Rg =" + marker},
		{name: "control: ordinary pair is untouched", in: "ref =abc", want: "ref =abc"},
		{name: "control: sensitive pair is redacted", in: "password =abc", want: "password =" + marker},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := sanitize.String(tt.in)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, got, sanitize.String(got), "re-running must not change it")
		})
	}
}

func TestStringRedactsTheSecretWhoseKeyTheValueSlotAbsorbed(t *testing.T) {
	t.Parallel()

	// THE REWIND HANDED THE VALUE BACK AS A KEY, AND THE VALUE CLASS THEN ATE
	// THE NEXT PAIR'S KEY AND SEPARATOR, LEAVING THE SECRET OUTSIDE THE MATCH.
	//
	// "host=db =password= hunter2" reads as key "host", value "db"; the value is
	// followed by a separator, so "db" is handed back to the scanner as a key.
	// Its value class admits '=' and stops at whitespace, so the pair becomes
	// key "db", value "password=" — A NAME AND THE SEPARATOR THAT INTRODUCES THE
	// CREDENTIAL. The credential sits after the space, outside the match, and is
	// copied across verbatim. Redacting that value scrubs the field name and
	// prints the secret.
	//
	// It is silent by construction. The output is a fixed point, so the round
	// loop and the fuzz idempotence assertion both agree with it, and every
	// secret the fuzzer plants is either vendor-anchored or a self-contained
	// pair, so "the credential must not survive" never sees this shape.
	//
	// THE INVARIANT IS THAT A VALUE NEVER ENDS IN THE SEPARATOR. When a rewound
	// value reads as "<name>=", the pair is "<name>" plus the separator plus the
	// token after it — which is exactly what keyValueSeparator admits, since it
	// takes whitespace on both sides of the '='.
	tests := []struct{ name, in, want, secret string }{
		{
			name:   "a pgx DSN with a spaced password parameter",
			in:     "pgx: host=db =password= hunter2 sslmode=require",
			want:   "pgx: host=db =password= " + marker + " sslmode=require",
			secret: "hunter2",
		},
		{
			name:   "an acquirer response carrying a CPF",
			in:     "op=charge =cpf= 12345678901 rc=200",
			want:   "op=charge =cpf= " + marker + " rc=200",
			secret: "12345678901",
		},
		{
			name:   "a broker option list carrying an AWS secret",
			in:     "kafka: acks=all =aws_secret_access_key= wJalrXUtnFEMI",
			want:   "kafka: acks=all =aws_secret_access_key= " + marker,
			secret: "wJalrXUtnFEMI",
		},
		{
			name:   "a one-letter key in front of the pair",
			in:     "k=v =cpf= hunter2 refused",
			want:   "k=v =cpf= " + marker + " refused",
			secret: "hunter2",
		},
		{
			// THE ROW THAT SHOWS THE DAMAGE IN BOTH DIRECTIONS: the sensitive
			// name in the value slot was redacted as though it were the secret,
			// and the document it introduces was printed. A marker on the line
			// asserts it was scrubbed.
			name:   "a validator message naming the field twice",
			in:     "validator: field=cpf =cpf= 12345678901",
			want:   "validator: field=cpf =" + marker,
			secret: "12345678901",
		},
		{
			// NO REWIND IN FRONT OF IT AT ALL, which is what says the defect is
			// the absorbed key and not the rewind that exposed it. This shape
			// leaked on every head back to the one the direction harness pins,
			// and the committed corpus entry "opt =" found it the moment the
			// fuzzer was given a credential the field name alone can save.
			name:   "a broker option list with no token in front",
			in:     "opt = password= hunter2 rc=05",
			want:   "opt = password= " + marker + " rc=05",
			secret: "hunter2",
		},
		{
			name:   "a dangling equals on the first key",
			in:     "tok= password= hunter2",
			want:   "tok= password= " + marker,
			secret: "hunter2",
		},
		{
			name:   "an ordinary word in front of a document field",
			in:     "plainword =cpf= abc",
			want:   "plainword =cpf= " + marker,
			secret: "abc",
		},
		{
			// The shape the Go security standard prints as the example of a DSN
			// worth redacting. It has no spaced separator and must be unaffected.
			name:   "control: an ordinary DSN password stays redacted",
			in:     "host=db password=s3cr3t sslmode=require",
			want:   "host=db password=" + marker + " sslmode=require",
			secret: "s3cr3t",
		},
		{
			// THE ONE SHAPE WHERE A TRAILING '=' IS NOT A BOUNDARY. "token" is a
			// sensitive key, "aGVsbG8" is not a field name, so the '=' is base64
			// padding and the value stays the secret. Rewinding here would print
			// the token.
			name:   "control: base64 padding is not a pair boundary",
			in:     "token=aGVsbG8= more",
			want:   "token=" + marker + " more",
			secret: "aGVsbG8",
		},
		{
			// THE VALUE IS NOT A NAME, IT MERELY HOLDS ONE. FuzzStringGlued
			// found this: the first cut of the rule above asked whether the raw
			// text between the key and the trailing '=' looked sensitive, and
			// "hunter2://CVC!0" does, on the "CVC" inside it. Stepping past it
			// then printed the password. The question is whether the value IS a
			// field name, front to back.
			name:   "control: a value holding a field name is still a value",
			in:     "0=password=hunter2://CVC!0=",
			want:   "0=password=" + marker,
			secret: "hunter2",
		},
		{
			name:   "control: a nested chain under a sensitive key is its value",
			in:     "password=b=c=",
			want:   "password=" + marker,
			secret: "b=c",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := sanitize.String(tt.in)
			assert.Equal(t, tt.want, got)
			assert.NotContains(t, got, tt.secret, "the credential survived")
			assert.Equal(t, got, sanitize.String(got), "re-running must not change it")
		})
	}
}

func TestStringSettlesOnTheFixedPoint(t *testing.T) {
	t.Parallel()

	// THE FOUR DEFECTS THAT MADE THE PIPELINE ITERATE, PLUS THE TWO CONTROLS
	// THAT SAY IT DID NOT COST ANYTHING.
	//
	// Each of the first four is the same family: a later pass rewrote or deleted
	// a byte an earlier pass had used as a token boundary, so a second run
	// parsed a different string and reached a different answer. In every one the
	// SECOND answer was the correct redaction and the first was a miss, which is
	// why running to a fixed point is the redaction rather than a tidy-up.
	tests := []struct{ name, in, want string }{
		{
			name: "a hash deleted from inside an authority",
			in:   "A://keY=#&@ postgres://u:p@db.internal:5432/ledger 0",
			want: "A://" + marker + "@ postgres://" + marker + ":" + marker + "@db.internal:5432/ledger 0",
		},
		{
			name: "a slash deleted from inside an authority",
			in:   "A://keY=/&@ postgres://u:p@db.internal:5432/ledger 0",
			want: "A://" + marker + "@ postgres://" + marker + ":" + marker + "@db.internal:5432/ledger 0",
		},
		{
			name: "a credential steals the key slot",
			in:   "0 AKIAIOSFODNN7EXAMPLE =Rg =0",
			want: "0 " + marker + " =Rg =" + marker,
		},
		{
			// The value class admits '=', so "Rg=" was swallowed whole as one
			// value and the pair behind it was never seen. Only once the thief
			// became a marker did the second run read "Rg= 0" as a pair.
			name: "a value absorbs the equals sign before a space",
			in:   "0 AKIAIOSFODNN7EXAMPLE =Rg= 0",
			want: "0 " + marker + " =Rg= " + marker,
		},

		// CONTROLS. Neither moved when the loop went in, and both are shapes a
		// fixed point could plausibly have damaged: one holds two secrets that
		// must BOTH go, the other holds no secret at all and must survive whole.
		{name: "control: two secrets on one line", in: "password=hunter2 =Rg =0", want: "password=" + marker + " =Rg =" + marker},
		{name: "control: a real fragment is not userinfo", in: "http://h/p#frag@x", want: "http://h/p#frag@x"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := sanitize.String(tt.in)
			assert.Equal(t, tt.want, got)
			assert.Equal(t, got, sanitize.String(got), "the settled output must be a fixed point")
		})
	}
}

func TestStringScrubsAGroupedCardInsideAQueryString(t *testing.T) {
	t.Parallel()

	// The query-parameter pass stops its value at the first whitespace, so a
	// space-grouped PAN behind a sensitive parameter was scrubbed ONE GROUP at a
	// time: "?pan=4111 1111 1111 1111" went to the log as "?pan=**** 1111 1111
	// 1111", twelve live digits behind a marker asserting the line was clean.
	//
	// It is the headless-PEM and the 4-4-4-N failure one level up: a pass that
	// carves a credential into pieces before the pass that would have recognised
	// it whole ever runs. Any URL-shaped log line carries it, and unlike the
	// 4-4-4-N gap it bites the ordinary sixteen-digit card.
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "sensitive parameter, trailing prose",
			in:   "GET /charge?pan=4111 1111 1111 1111 failed",
			want: "GET /charge?pan=" + marker + " failed",
		},
		{
			name: "value glued to the next parameter",
			in:   "GET /charge?card_number=4111 1111 1111 1111&rc=05",
			want: "GET /charge?card_number=" + marker + "&rc=05",
		},
		{
			name: "card is the last query value",
			in:   "GET /charge?token=abc&pan=4111 1111 1111 1111",
			want: "GET /charge?token=" + marker + "&pan=" + marker,
		},
		{
			name: "the three-groups-and-a-short-tail shape through the query path",
			in:   "GET /charge?pan=3782 8224 6310 005 failed",
			want: "GET /charge?pan=" + marker + " failed",
		},
		{
			name: "inside a full URL",
			in:   "https://api/v1/charge?pan=4111 1111 1111 1111",
			want: "https://api/v1/charge?pan=" + marker,
		},
		{
			// Dash grouping never split, because the value class admits '-'.
			// Here to catch a fix that trades one grouping for another.
			name: "dash-grouped value was already whole",
			in:   "GET /charge?pan=4111-1111-1111-1111 failed",
			want: "GET /charge?pan=" + marker + " failed",
		},
		{
			// CONTROL. No '?', so this is the key=value pass at step 8, which
			// already runs after the card pass. Must not change.
			name: "control: no query string",
			in:   "pan=4111 1111 1111 1111 failed",
			want: "pan=" + marker + " failed",
		},
		{
			// CONTROL. A percent-encoded value is one token with no whitespace
			// to split on, and is redacted by field name. Must not change.
			name: "control: percent-encoded value",
			in:   "?pan=4111%201111%201111%201111",
			want: "?pan=" + marker,
		},
		{
			// CONTROL. "ref" is not a sensitive field name, so the query pass
			// leaves the value alone and the card pass redacts the PAN whole.
			// Must not change.
			name: "control: non-sensitive parameter still loses the card",
			in:   "GET /charge?ref=4111 1111 1111 1111 failed",
			want: "GET /charge?ref=" + marker + " failed",
		},
		{
			// CONTROL. A tab ends the value for the pattern AND for the byte
			// test, so the grouped run is taken whole and the tab itself is
			// kept. \n, \f, \r and a plain space behave the same way.
			name: "control: a tab ends the run and survives",
			in:   "&cpf=1234 5678\t",
			want: "&cpf=" + marker + "\t",
		},

		// ONE ROW PER BYTE IN queryValueTerminators.
		//
		// The agreement test pins the compiled pattern against isQueryValueByte,
		// but both are derived from the same constant, so deleting a byte from
		// the constant moves them together and that test stays green. Only
		// behaviour notices. Dropping '#' widened the value across a URL
		// fragment and left four digits of a card in the log with every other
		// test still passing, which is what these rows exist to stop.
		{name: "terminator &", in: "&cpf=1234 5678&9012", want: "&cpf=" + marker + "&9012"},
		{name: "terminator comma", in: "&cpf=1234 5678,9012", want: "&cpf=" + marker + ",9012"},
		{name: "terminator semicolon", in: "&cpf=1234 5678;9012", want: "&cpf=" + marker + ";9012"},
		{name: "terminator newline", in: "&cpf=1234 5678\n9012", want: "&cpf=" + marker + "\n9012"},
		{name: "terminator form feed", in: "&cpf=1234 5678\f9012", want: "&cpf=" + marker + "\f9012"},
		{name: "terminator carriage return", in: "&cpf=1234 5678\r9012", want: "&cpf=" + marker + "\r9012"},

		// THESE THREE LOSE THE REMAINDER, AND THAT IS OVER-REACH, NOT A LEAK.
		// The byte ends the query value correctly; what follows is then eaten by
		// the key=value pass at step 8, which stops at whitespace and so takes
		// the rest of the token once the value ahead of it is a bare marker. It
		// is the same loss already documented for a trailing quote. Recorded as
		// measured so that a change in either direction has to be deliberate.
		{name: "terminator hash loses the fragment", in: "&cpf=1234 5678#9012", want: "&cpf=" + marker},
		{name: "terminator double quote", in: "&cpf=1234 5678\"9012", want: "&cpf=" + marker},
		{name: "terminator single quote", in: "&cpf=1234 5678'9012", want: "&cpf=" + marker},

		// The fragment case on a full sixteen-digit card, which is the line the
		// dropped-'#' mutant leaked through.
		{
			name: "url fragment after a grouped card",
			in:   "GET /charge?pan=4111 1111 1111 1111#frag",
			want: "GET /charge?pan=" + marker,
		},

		// A space is a terminator too; here the whole run is grouped digits, so
		// the extension takes all three groups and nothing is left over.
		{name: "terminator space", in: "&cpf=1234 5678 9012", want: "&cpf=" + marker},

		// THE TWO GUARDS THAT DECIDE HOW FAR THE EXTENSION REACHES. Both are
		// one condition each, both change behaviour when removed, and neither
		// had a row: removing either over-redacts, which is the failure that
		// does not announce itself because the output still looks scrubbed.
		//
		// A value that is not itself all digits is not the head of a grouped
		// run, so the extension must not start. Without that guard the marker
		// swallows the prose after any sensitive parameter.
		{
			name: "guard: a non-digit value does not start an extension",
			in:   "?apikey=abc 1234 5678",
			want: "?apikey=" + marker + " 1234 5678",
		},
		{
			// Arabic-Indic digits are digits to a reader and not to allDigits,
			// which is ASCII. The value is therefore not a run head, the
			// extension does not start, and the grouped ASCII card after it is
			// redacted on its own by the card pass.
			name: "guard: non-ASCII digits are not a run head",
			in:   "?cpf=١٢٣٤ 5678 9012 3456",
			want: "?cpf=" + marker + " " + marker,
		},
		{
			// A space with no digit after it ends the run. Without that guard
			// the trailing space is eaten, and a value that merely ends the line
			// loses the character that separates it from whatever is appended
			// next.
			name: "guard: a trailing space with no group after it is kept",
			in:   "&cpf=1 ",
			want: "&cpf=" + marker + " ",
		},
		{
			// CONTROL, AND THE ONE THAT WAS WRONG. A vertical tab is NOT in
			// RE2's \s, so the pattern admits it as ordinary value material and
			// "5678\v" is one token rather than a bare group of digits. Nothing
			// extends over it and the \v is kept — which is the same visible
			// rule as the tab above, reached the other way round.
			//
			// The byte test used to disagree with the pattern here and call the
			// token complete, so the value swallowed the \v, and a second run
			// then took "****\v" as one value and redacted further. A sanitizer
			// whose output changes when it is run twice.
			name: "control: a vertical tab is value material, not a terminator",
			in:   "&cpf=1234 5678\v",
			want: "&cpf=" + marker + " 5678\v",
		},
		{
			name: "control: a vertical tab mid-token is value material too",
			in:   "&cpf=1234 5678\vx",
			want: "&cpf=" + marker + " 5678\vx",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := sanitize.String(tt.in)

			assert.Equal(t, tt.want, got)

			for _, group := range []string{"4111", "1111", "3782", "8224", "6310"} {
				assert.NotContains(t, got, group, "a group of the card survived in %q", got)
			}

			assert.Equal(t, got, sanitize.String(got), "re-running must not change it")
		})
	}
}

// TestStringRedactsWhenTheValueIsAFieldNameWithNothingAfterIt is the FIRST half
// of "a value never ends in the separator", and the half that rule got wrong.
//
// The rule reads "<name>=" in a value slot as a pair boundary and hands the
// position back to the scanner, because the credential normally sits past the
// whitespace behind it. When NOTHING claimable follows, there is no such
// credential: the scanner needs at least one value byte, finds none, and the
// bytes it was handed are copied across verbatim — so a SENSITIVE key whose
// value happens to end in '=' printed its value instead of redacting it.
//
// The question the rewind has to ask on a sensitive key is therefore not "is
// this value a field name" but "is there a bare value after it to redact
// instead". A pair after it answers no as well: "password=abc_token= rc=200"
// has a pair behind the value, so "abc_token=" was the literal value.
func TestStringRedactsWhenTheValueIsAFieldNameWithNothingAfterIt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"a field name and nothing else", "password=abc_token=", "password=" + marker},
		{"the key's own name in its value", "password=my-password=", "password=" + marker},
		{"a dotted field name", "password=user.password.old=", "password=" + marker},
		{"an underscore breaks the name", "authorization=Bearer_token=", "authorization=" + marker},
		{"a terminator follows", "accesskey=access_key=,next=1", "accesskey=" + marker + ",next=1"},
		{"nested under a harmless key", "a=accesskey=access_key=", "a=accesskey=" + marker},
		{"a non-word byte in front of it", "token=!password=", "token=" + marker},
		{
			// A pair follows, so the value really was the literal text.
			name: "a pair follows the value",
			in:   "password=abc_token= rc=200",
			want: "password=" + marker + " rc=200",
		},

		// Base64 padding is not a pair boundary, and neither is an '=' inside a
		// value that merely holds a name. These are the shapes the rule was
		// written for and must keep.
		{"base64 padding", "password=aGVsbG8=", "password=" + marker},
		{"base64 padding with a tail", "token=aGVsbG8= more", "token=" + marker + " more"},
		{"a name inside a value", "password=hunter2://CVC!0=", "password=" + marker},
		{
			// The name is sensitive and a BARE value follows it, so the
			// redaction runs through that value too: neither reading of
			// "cpf=" — padding on the credential, or the key of the number
			// behind it — leaves anything in the clear.
			name: "a bare value does follow",
			in:   "password=cpf= 12345678901",
			want: "password=" + marker,
		},
		{
			name: "the field name printed twice",
			in:   "validator: field=cpf =cpf= 12345678901",
			want: "validator: field=cpf =" + marker,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := sanitize.String(tt.in)

			assert.Equal(t, tt.want, got)
			assert.Equal(t, got, sanitize.String(got), "re-running must not change it")
		})
	}
}

// TestStringRedactsTheValueBehindAFieldNameUnderAHarmlessKey is the same rule
// read from the other side: the key is harmless, so the walker rewinds onto the
// name in the value slot — but only when what FOLLOWS that value is the next
// pair's separator, and that test was never asked on a sensitive key.
//
// "a=password= rg =hunter2" is one pair keyed on "a" whose value is the field
// name "password". The walker rewinds onto "password", whose own value is then
// "rg" — a token, not the credential — and the secret after the spaced '='
// behind it is orphaned and printed. The value that is itself a field name has
// to be handed back a second time.
func TestStringRedactsTheValueBehindAFieldNameUnderAHarmlessKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "a token steals the sensitive key's value slot",
			in:   "a=password= rg =hunter2",
			want: "a=password= " + marker,
		},
		{
			name: "a tab separator on a driver option list",
			in:   "pgx: opt=password= cpf\t=12345678901 sslmode=require",
			want: "pgx: opt=password= " + marker + " sslmode=require",
		},

		// The credential is the value here, not a name, so the rewind must not
		// fire and these keep the answers they have.
		{"a real secret then a pair", "password=hunter2 =Rg =0", "password=" + marker + " =Rg =" + marker},
		{"a token in the value slot", "tok =rg =0", "tok =rg =" + marker},
		{"a harmless key in front", "opt = password=hunter2", "opt = password=" + marker},
		{
			name: "a driver option list",
			in:   "host=db =password= hunter2 sslmode=require",
			want: "host=db =password= " + marker + " sslmode=require",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := sanitize.String(tt.in)

			assert.Equal(t, tt.want, got)
			assert.Equal(t, got, sanitize.String(got), "re-running must not change it")
		})
	}
}

// TestStringRedactsWhenANonWordByteLeadsTheFieldNameInTheValue closes the last
// hole in the same rule: the field name in the value slot was only believed
// when it began at the first byte of the value.
//
// A quote, a bracket, a bang or a second '=' in front of it is an ordinary way
// for a driver or a validator to print the pair, and on a SENSITIVE key the
// offset test turned the whole shape back into a value: the name was redacted
// and the credential behind it printed. The offset test also buys nothing that
// the word boundary does not already buy — a key starts with a letter, so any
// match past the first byte already has a non-word byte in front of it.
func TestStringRedactsWhenANonWordByteLeadsTheFieldNameInTheValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"a bang", "token=!password= hunter2", "token=" + marker},
		{
			// The value class stops at whitespace, a comma, a semicolon and an
			// ampersand — not at a quote — so the credential's own closing
			// quote goes under the marker with it. That is the direction this
			// package errs in everywhere else ("token=abc: refused" loses the
			// colon too): one character too many, never one too few.
			name: "a quote",
			in:   `pwd="password= hunter2"`,
			want: "pwd=" + marker,
		},
		{"a bracket", "secret=[cpf= 12345678901", "secret=" + marker},
		{
			// The separator is "= " here — its trailing whitespace belongs to
			// the separator, not to the value — so those bytes are carried
			// across and the marker starts where the value does.
			name: "a second separator",
			in:   "password= =cpf= 12345678901",
			want: "password= " + marker,
		},

		// A WORD byte in front is not a boundary: "0password" is not the field
		// "password", and a value that merely holds a name is still a value.
		{"a digit in front is not a boundary", "k=0password= hunter2", "k=0password= hunter2"},
		{
			// The credential itself is word-shaped and a vendor word follows it
			// behind a non-word byte, which is not a pair boundary either: the
			// whole thing is the value. FuzzStringGlued found this one.
			name: "a credential ending in a name",
			in:   "0= password=hunter2@CVC= 0",
			want: "0= password=" + marker,
		},
		{"already right under a harmless key", "x=b=!password= hunter2", "x=b=!password= " + marker},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := sanitize.String(tt.in)

			assert.Equal(t, tt.want, got)
			assert.Equal(t, got, sanitize.String(got), "re-running must not change it")
		})
	}
}

// TestStringRedactsBothReadingsOfANameShapedValue is the answer this package
// gives when the value under a SENSITIVE key is itself a field name.
//
// IT IS A CREDENTIAL UNDER ONE READING AND THE NEXT PAIR'S KEY UNDER THE OTHER,
// AND EVERY RULE THAT PICKED ONE OF THEM PRINTED THE OTHER. "password=password
// =0" is a weak password whose text happens to be a field word; it is also,
// byte for byte, a key whose value is behind the spaced '='. Four passes in a
// row shipped a sharper test of which one it was — is the value a whole name,
// is it sensitive, does a word byte precede it — and each one leaked the
// reading it had ruled out: the credential itself, or the credential the name
// introduced.
//
// So the rule stops deciding. On a sensitive key the value is redacted, ALWAYS;
// and when a sensitive name sits at the end of it, the redaction extends
// through the bare value that name would introduce, and through the chain
// behind that. A false positive costs one over-redacted word, which is the
// direction this package already errs in; a false negative costs nothing,
// because the value went under the marker either way.
//
// A WHOLE PAIR BEHIND THE NAME STOPS THE EXTENSION, and only there: a value
// that ends in "<name>=" is a complete value, so "rc=200" behind it is the next
// pair and stays diagnosable. A name with the separator OUTSIDE the value has
// no such reading — that separator has no value unless the token behind it is
// one — so the chain runs through it.
func TestStringRedactsBothReadingsOfANameShapedValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		// The value is a whole field name and the separator follows it.
		{"a password that is a field word", "password=password =0", "password=" + marker},
		{"a spaced value behind the name", "password=secret = hunter2", "password=" + marker},
		{"the name introduces nothing", "password=secret =", "password=" + marker + " ="},
		{"camelCase folds to a field name", "password=myPassword = 1", "password=" + marker},
		{"a credential holding a vendor word", "password=hunter2.rg =", "password=" + marker + " ="},
		{"a dotted credential reads as a name", "token=s3cr3t.pin =0", "token=" + marker},
		{"a chain of names", "password=password =password =0", "password=" + marker},
		{"the key's own name in the value slot", "cpf=cpf =0", "cpf=" + marker},

		// The value ENDS in a sensitive name and the separator.
		{"base64 one dot from padding", "token=aGVsbG8.cvc= more", "token=" + marker},
		{"base64url with an underscore", "token=xY9_key= expired", "token=" + marker},
		{"camelCase and the separator", "password=myPassword= 0", "password=" + marker},
		{"a bare word behind the name", "password=abc_token= rc", "password=" + marker},
		{"a document behind the name", "password=cpf= 12345678901", "password=" + marker},
		{"a vendor word behind a non-word byte", "password=hunter2@CVC= 0", "password=" + marker},
		{"punctuation in front of the name", "password=!@#$%^*()password= hunter2", "password=" + marker},

		// Kept: a pair behind the name is the next pair, base64 padding is not a
		// name, and a credential that is not a name is only itself.
		{"a pair behind the name", "password=abc_token= rc=200", "password=" + marker + " rc=200"},
		{"nothing behind the name", "password=abc_token=", "password=" + marker},
		{"base64 padding", "password=aGVsbG8=", "password=" + marker},
		{"base64 padding and a word", "token=aGVsbG8= more", "token=" + marker + " more"},
		{"a credential then a pair", "password=hunter2 =Rg =0", "password=" + marker + " =Rg =" + marker},
		{"a name inside a value", "password=hunter2://CVC!0=", "password=" + marker},
		{"a credential is not a name", "password=Tr0ub4dor =1", "password=" + marker + " =1"},

		// A harmless key keeps every rewind it has: the name in the value slot
		// is diagnostic there, and the credential it introduces is what goes.
		{"a harmless key rewinds", "a=password= rg =hunter2", "a=password= " + marker},
		{"a token in the value slot", "tok =rg =0", "tok =rg =" + marker},
		{"a non-word byte under a harmless key", "x=b=!password= hunter2", "x=b=!password= " + marker},
		{
			name: "a driver option list",
			in:   "host=db =password= hunter2 sslmode=require",
			want: "host=db =password= " + marker + " sslmode=require",
		},
		{"a digit in front is not a name", "k=0password= hunter2", "k=0password= hunter2"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := sanitize.String(tt.in)

			assert.Equal(t, tt.want, got)
			assert.Equal(t, got, sanitize.String(got), "re-running must not change it")
		})
	}
}

// TestStringRedactsAcrossAVerticalTabSeparator pins the ONE byte on which the
// two key=value patterns used to disagree, from both sides of the separator.
//
// keyValuePattern's value class was [^\s,;&]+ and RE2's \s is [\t\n\f\r ],
// which does not hold '\v'; the separator both patterns are built from ends in
// [[:space:]]*, which does. On the key side that made keyPrefixPattern's match
// end one byte past where the full pattern's value starts, so a chain ending in
// "<name>=\v" read as finished and was copied across. On the value side it made
// a lone '\v' a VALUE, so "password=\v hunter2" put the marker over a
// whitespace byte and printed the credential behind it.
//
// Both classes are [[:space:]] now, so a '\v' is whitespace wherever it sits:
// the chains below have no value to redact and come back unchanged, and the
// credential behind the whitespace is the value.
func TestStringRedactsAcrossAVerticalTabSeparator(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		// A LONE '\v' IS NOT A VALUE. It is whitespace on both sides of the
		// separator now, so a chain that ends in one ends in the separator and
		// its trailing space: there is no value here to redact, and the marker
		// these lines used to carry was a marker over a whitespace byte.
		{"a chain ending in the separator", "k=k=pwd=\v", "k=k=pwd=\v"},
		{"a key chain", "t=b=key=\v", "t=b=key=\v"},
		{"a nested secret", "b=s=secret=\v", "b=s=secret=\v"},

		// And the credential behind that whitespace is the value, which is what
		// the value side of the disagreement was hiding.
		{"a credential behind it", "a=b=password=\v hunter2", "a=b=password=\v " + marker},
		{"a lone vertical tab separator", "password=\v hunter2", "password=\v " + marker},
		{"a document behind one", "cpf=\v 12345678901", "cpf=\v " + marker},
		{"a space then a vertical tab", "password= \v hunter2", "password= \v " + marker},
		{"two whitespace bytes, no space", "password=\v\thunter2", "password=\v\t" + marker},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := sanitize.String(tt.in)

			assert.Equal(t, tt.want, got)
			assert.Equal(t, got, sanitize.String(got), "re-running must not change it")
		})
	}
}

// TestStringStaysBoundedOnHeadlessPemArmorAtTheLimit is the cost of a PEM block
// whose END line never arrives, which is what a truncated log line is.
//
// The well-formed reading is tried first and has to be, so for every BEGIN line
// the scan looked for an END line anywhere behind it before falling back to the
// headless body — and with no END line in the input at all, that search ran to
// the end of the string once per BEGIN. 64 KiB of armor lines cost 6.5 seconds
// on the goroutine writing the log line, against 0.02 for a well-formed block of
// the same size: an attacker-shaped input, since the armor is trivial to write
// and the bound is what the package admits.
func TestStringStaysBoundedOnHeadlessPemArmorAtTheLimit(t *testing.T) {
	t.Parallel()

	assertStringReturnsWithin(t, fillToBound("-----BEGIN A-----"), "headless PEM armor")
}
