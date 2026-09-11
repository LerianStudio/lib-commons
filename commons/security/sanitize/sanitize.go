package sanitize

import (
	"errors"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"

	"github.com/LerianStudio/lib-observability/v4/redaction"
)

// SecretRedactionMarker is the canonical replacement literal used by
// lib-commons packages when redacting sensitive values in strings.
//
// The literal is intentionally four asterisks - short enough to keep error
// messages legible, long enough to stand out in logs. Do not shorten or
// extend without updating every call site in the same change; the marker
// is an implicit operator contract (dashboards and SIEM rules may key off
// it).
const SecretRedactionMarker = "****"

// MaxInputLen is the largest string String will scan. Above it the input is
// REFUSED — never truncated — and a marker sentence naming the size is returned
// instead.
//
// Refusing rather than cutting is the same rule the package doc states for
// length generally: a cut landing mid-secret strands a readable prefix
// ("postgres://user:pas") that no later pattern can recognise, so truncation
// turns a redactor into a leak. A refusal loses the message and keeps the
// guarantee.
//
// The bound exists because the cost is real: the passes are linear, but there
// are eighteen of them plus the card pass's fixed-point rounds, measured at
// roughly 400 milliseconds per megabyte on an ordinary devbox — 8 MB of digit
// runs takes about 3.4 seconds, on whatever goroutine happened to be writing a
// log line. 64 KiB is far above any error string worth reading and far below the
// point where that matters.
const MaxInputLen = 64 << 10

// sensitiveFieldExtras augments the centralized lib-observability taxonomy with
// the key names that show up in Lerian error strings but are not part of the
// generic default list. Field sensitivity itself is decided by
// redaction.IsSensitiveField; this is only the addendum.
//
// Two groups, and the second is the one worth explaining. The AWS/SASL names are
// what broker clients and SDKs echo. The BRAZILIAN document and bank-account
// names are what a ledger echoes: the shared taxonomy was enumerated in English,
// so it knows "ssn" and "account_number" and has never heard of "cpf" or
// "agencia" — and those are the exact spellings a unique-constraint violation, a
// validator message or a marshaled payload carries here.
//
// Matching is by whole word, so an entry also covers its compounds
// ("conta_corrente", "cpf_titular") without covering an unrelated word that
// merely contains the letters.
var sensitiveFieldExtras = []string{
	// AWS, SASL and signature material.
	"accesskey",
	"access_key",
	"accesskeyid",
	"aws_access_key_id",
	"aws_secret_access_key",
	"aws_session_token",
	"secretaccesskey",
	"sessiontoken",
	"signature",
	"sasl_password",
	"passwd",
	"pwd",
	// libpq/pgx connection parameter carrying the client key's passphrase. The
	// taxonomy's "password" does not reach it: the match is by whole word, and
	// "sslpassword" has no boundary in front of "password".
	"sslpassword",

	// Brazilian tax and identity documents.
	"cpf",
	"cnpj",
	"rg",
	"document",
	"documento",

	// Bank account, branch and Pix addressing.
	"conta",
	"agencia",
	"chave_pix",
	"holder_name",
	"nome_titular",

	// Date of birth, in both spellings this codebase sees.
	"birthdate",
	"data_nascimento",

	// Session identifiers. The taxonomy carries "session_id" and "sessionid",
	// neither of which matches a bare "session" or a servlet "jsessionid".
	"session",
	"jsessionid",
}

// urlPattern matches scheme://rest-of-URL tokens so embedded userinfo
// credentials can be stripped.
//
// The tail runs to the next whitespace, which means ONE match can span a whole
// comma- or semicolon-separated list of URLs — a broker DSN is routinely written
// that way. redactURLUserinfo therefore splits what it is given rather than
// assuming one authority per match.
var urlPattern = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s]+`)

// queryParameterPattern finds one ?name=value or &name=value pair inside a query
// string, so a credential carried as a URL parameter is redacted by field name.
//
// IT EXISTS BECAUSE THE key=value PASS CANNOT SEE THESE. That pattern's value
// class admits '=', so "endpoint=https://api/v1?apikey=s3cr3t" is a SINGLE pair
// keyed on "endpoint" — judged not sensitive, and the inner apikey= is never a
// candidate at all. The same swallowing hides sslpassword= on a Postgres DSN and
// auth_token= on a broker URL.
//
// The value stops at the next parameter, whitespace, quote, fragment, comma or
// semicolon, so only the one credential goes and the rest of the query string
// stays diagnosable. Comma and semicolon are in that set for the same reason
// keyValuePattern has them: they separate the next URL in a broker list, and
// swallowing one merged two entries — which also made the pass non-idempotent,
// since a second run then ate the separator the first had left.
var queryParameterPattern = regexp.MustCompile(`([?&])([A-Za-z0-9_.-]+)=([^&\s,;"'#]+)`)

// keyValuePattern finds key=value fragments in config dumps and driver errors.
// Sensitivity is decided by the field name, not by a package-local taxonomy.
//
// The value runs to the next whitespace, comma, semicolon or ampersand, which
// means a trailing colon is swallowed into the redaction ("token=abc: refused"
// becomes "token=**** refused"). That errs toward redacting one character too
// many rather than one too few, which is the correct direction here.
var keyValuePattern = regexp.MustCompile(`(?i)\b([a-z][a-z0-9._-]*)([[:space:]]*=[[:space:]]*)([^\s,;&]+)`)

// jsonKeyValuePattern finds "key":"value" fragments so a JSON-shaped secret (a
// marshaled config, or a request body echoed into an error) is redacted by field
// name just like a key=value pair. Only string-valued keys are matched: a
// numeric or boolean value is not a credential.
//
// EVERY QUOTE IS OPTIONALLY ESCAPED, AND EACH ONE INDEPENDENTLY. A body that has
// been through %q, or that was nested inside another JSON document's string
// value, arrives as \"password\": \"hunter2\" — and a pattern anchored on a bare
// quote matches none of it, so the whole body went to the log untouched. The
// escaping can also be one-sided (a template that quoted the key but
// interpolated the value), so the four quotes are four separate optional
// backslashes rather than one mode flag.
//
// The delimiters are CAPTURED, not assumed, because the replacement has to put
// back exactly what it found: reconstructing with a bare quote where the input
// had an escaped one produces a string that is no longer valid JSON.
//
// The value admits an escape sequence (\\, \/, \n) but NOT an escaped quote,
// which is the value's terminator in the escaped form exactly as a bare quote is
// in the plain one.
var jsonKeyValuePattern = regexp.MustCompile(
	`(\\?")([a-zA-Z][a-zA-Z0-9._-]*)(\\?")([[:space:]]*:[[:space:]]*)(\\?")((?:[^"\\]|\\[^"])*)(\\?")`)

// authHeaderPattern matches credential-bearing header forms — Authorization,
// Proxy-Authorization, Cookie and the X-Api-Key family — and redacts EVERYTHING
// after the colon to the end of the line (or to the first ',' or '"', which end a
// header value inside a JSON or struct dump).
//
// THREE THINGS ARE DELIBERATE HERE. The scheme is optional, because
// `Authorization: <bare-opaque-token>` is a real header and requiring a scheme
// let it through untouched unless some other pattern happened to catch it. The
// scheme is matched from a CLOSED LIST rather than as "the first word", because
// "the first word" is indistinguishable from the first word OF an opaque token:
// `Authorization: sessiontoken abc123` would keep "sessiontoken" as a scheme and
// redact only what follows. An unrecognised leading word is treated as credential
// material, which is the safe reading. And the separator tolerates an escaped or
// bare quote on either side of the colon, so the header survives %q and JSON
// nesting — see jsonKeyValuePattern for the same reasoning at length.
//
// Cookie and X-Api-Key carry a credential in a header that has no '=' for the
// key=value pass to key on, and no scheme for this one to keep: the WHOLE value
// goes.
//
// Running to end of line rather than to the next space redacts a word or two of
// surrounding prose when the header sits mid-sentence. That is the correct
// direction of error for a credential.
//
// THE TERMINATOR SET IS WHAT KEEPS THE REST OF THE LINE READABLE. A header
// rarely sits alone: it arrives inside a map dump or a JSON object, and running
// to end of line there consumed every sibling field — the request id and host an
// operator needs in order to find the call at all. ']' and '}' close the
// structure and ';' separates the next pair, so each ends the value.
//
// '\' ends it too, and that one is about correctness rather than volume: a
// backslash immediately before a quote is the quote's ESCAPE, so swallowing it
// leaves a dangling quote and a string that is no longer valid JSON. It also made
// the function non-idempotent, which is how it was found.
var authHeaderPattern = regexp.MustCompile(
	`(?i)\b((?:proxy-)?authorization|auth|cookie|(?:x-)?api-key)(\\?"?[[:space:]]*:[[:space:]]*\\?"?)` +
		`((?:bearer|basic|digest|negotiate|ntlm|token|apikey|hmac|aws4-hmac-sha256)[[:space:]]+)?` +
		`[^\r\n,"\]};\\]+`)

// bareSecretPatterns match credential material appearing WITHOUT a surrounding
// field name — SDKs and broker clients routinely echo the bare token. Each is
// anchored on a vendor-stable prefix and shape so it cannot match an ordinary
// word. The whole match is replaced.
//
// Field-name-keyed redaction catches the `key=<secret>` forms; these close the
// bare-value gap on top of it.
var bareSecretPatterns = []*regexp.Regexp{
	// AWS access key IDs (AKIA/ASIA + 16 base32 chars).
	regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b`),
	// Google / GCP API keys (AIza + 35 url-safe chars).
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`),
	// GitHub tokens (ghp_/gho_/ghu_/ghs_/ghr_ + 36 or more chars).
	regexp.MustCompile(`\bgh[pousr]_[0-9A-Za-z]{36,}\b`),
	// Stripe secret and restricted keys, live and test.
	regexp.MustCompile(`\b(sk|rk)_(live|test)_[0-9A-Za-z]{16,}\b`),
	// Slack tokens (xoxb-/xoxp-/xoxa-/xoxr-/xoxs- + dash-delimited segments).
	regexp.MustCompile(`\bxox[baprs]-[0-9A-Za-z-]{10,}\b`),
	// Bare JWTs, anchored on the "eyJ" header so it does not eat an ordinary
	// dotted identifier.
	regexp.MustCompile(`\beyJ[0-9A-Za-z_-]+\.[0-9A-Za-z_-]+\.[0-9A-Za-z_-]+\b`),
	// Bare e-mail addresses. PII the taxonomy already calls sensitive by field
	// name, appearing with no field name — which is how a validator, an SMTP
	// client or a unique-constraint violation echoes it. Same expression
	// commons/outbox has redacted before storing last_error.
	regexp.MustCompile(`(?i)\b[A-Z0-9._%+\-]+@[A-Z0-9.\-]+\.[A-Z]{2,}\b`),
}

// Card numbers carry 12 to 19 digits once their separators are stripped. The
// bounds are applied to the DIGITS, not to the matched text, so a grouped PAN and
// an unbroken one are judged the same way.
const (
	minCardDigits = 12
	maxCardDigits = 19
)

// cardCandidatePatterns are the CANDIDATE shapes for a card number. They are
// candidates and not verdicts — see redactCardNumbers, where the Luhn check
// decides.
//
// A PAN is written the way it is printed on the card at least as often as
// unbroken, and the grouped form was worse than merely missed: behind a field
// name, the key=value pass stopped at the first space and redacted ONE group,
// leaving twelve of sixteen digits in the log under a marker that said the line
// had been scrubbed.
//
// GROUPED SHAPES COME FIRST so a PAN is consumed whole. The unbroken pattern
// cannot match a four-digit group anyway, but the order also says which reading
// wins if that ever changes.
var cardCandidatePatterns = []*regexp.Regexp{
	// Grouped as printed: 4-4-4, 4-4-4-4, and any longer run. THE REPEAT IS
	// UNBOUNDED ON PURPOSE. Capping it truncates the run mid-way, and a card
	// that straddles the cut is then invisible to the window scan that would
	// otherwise find it — which is how a PAN behind two leading four-digit
	// groups survived. Length is judged on the digits, below, not here.
	regexp.MustCompile(`\b\d{4}(?:[ .-]\d{4}){2,}\b`),
	// Amex (4-6-5) and Diners (4-6-4), which group unevenly.
	regexp.MustCompile(`\b\d{4}[ .-]\d{6}[ .-]\d{4,5}\b`),
	// One unbroken run.
	regexp.MustCompile(`\b\d{12,19}\b`),
}

// cardSeparators strips the grouping characters before the checksum runs. Luhn is
// defined over digits; the separators are presentation.
var cardSeparators = strings.NewReplacer(" ", "", ".", "", "-", "")

// digitGroupPattern locates the individual groups inside a candidate, so an
// over-long run can be searched a whole group at a time.
var digitGroupPattern = regexp.MustCompile(`\d+`)

// azureSASSignaturePattern redacts the Azure SAS `sig=` parameter inside a query
// string. The token is URL-encoded, so the value runs to the next delimiter.
var azureSASSignaturePattern = regexp.MustCompile(`(?i)([?&]sig=)[^&\s]+`)

// pemBlockPattern matches a whole PEM block and replaces the armored body, so
// the base64 payload between the BEGIN and END lines never reaches a log line.
var pemBlockPattern = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]+-----.*?-----END [A-Z0-9 ]+-----`)

// String strips credential material from a free-form string so DSNs, broker
// URLs, SASL passwords, bearer tokens, AWS/GCP/GitHub/Stripe/Slack keys, JWTs
// and PEM private-key blocks never reach a log line, a span attribute or a
// stored error column.
//
// It preserves everything that makes a failure diagnosable — scheme, host, port,
// database, non-sensitive parameters — and is safe on an empty string, on a
// string with no credentials (returned verbatim), and on a string it has already
// sanitized (re-running it does not mangle the marker).
func String(s string) string {
	if s == "" {
		return ""
	}

	if len(s) > MaxInputLen {
		return fmt.Sprintf("%s (input of %d bytes exceeds the sanitizer bound; not scanned)",
			SecretRedactionMarker, len(s))
	}

	// 1. PEM BLOCKS FIRST, AND THE ORDER IS LOAD-BEARING. An armored block is
	// often the VALUE of a sensitive key ("private_key=-----BEGIN ..."), which is
	// how a config-loading error echoes one. Let the key=value pass run first and
	// it consumes "-----BEGIN" as that key's value, leaving no marker for this
	// rule to anchor on — and the entire base64 body survives into the log.
	s = pemBlockPattern.ReplaceAllString(s, SecretRedactionMarker)

	// 2. URL-shaped tokens: strip userinfo from EVERY URL in the match, keep
	// scheme and host.
	s = urlPattern.ReplaceAllStringFunc(s, redactURLUserinfo)

	// 3. Sensitive query parameters, by field name. Before the key=value pass,
	// which would otherwise swallow the whole URL as one non-sensitive pair.
	s = queryParameterPattern.ReplaceAllStringFunc(s, func(match string) string {
		parts := queryParameterPattern.FindStringSubmatch(match)
		if len(parts) != 4 || !isSensitiveFieldName(parts[2]) {
			return match
		}

		return parts[1] + parts[2] + "=" + SecretRedactionMarker
	})

	// 4. Azure SAS signature parameter inside a query string. After the pass
	// above, which does not classify "sig" as a field name and leaves it here.
	s = azureSASSignaturePattern.ReplaceAllString(s, "${1}"+SecretRedactionMarker)

	// 5. Authorization header forms: keep the scheme, redact the credential.
	s = authHeaderPattern.ReplaceAllStringFunc(s, func(match string) string {
		parts := authHeaderPattern.FindStringSubmatch(match)
		if len(parts) != 4 {
			return match
		}

		// parts[3] is the recognised scheme WITH its trailing space, or empty.
		return parts[1] + parts[2] + parts[3] + SecretRedactionMarker
	})

	// 6. Sensitive JSON "key":"value" pairs, by field name. The quote characters
	// are put back exactly as they were found, escaped or bare.
	s = jsonKeyValuePattern.ReplaceAllStringFunc(s, func(match string) string {
		parts := jsonKeyValuePattern.FindStringSubmatch(match)
		if len(parts) != 8 || !isSensitiveFieldName(parts[2]) {
			return match
		}

		return parts[1] + parts[2] + parts[3] + parts[4] + parts[5] + SecretRedactionMarker + parts[7]
	})

	// 7. Card numbers, BEFORE key=value. A grouped PAN behind a field name is the
	// reason for the order: the key=value value class stops at the first space,
	// so it would redact one four-digit group and leave the remaining twelve
	// digits behind a marker claiming the line was scrubbed. Consuming the PAN
	// whole here leaves key=value nothing but a marker to redact again.
	s = redactCardNumbers(s)

	// 8. Sensitive key=value pairs, by field name.
	s = keyValuePattern.ReplaceAllStringFunc(s, func(match string) string {
		parts := keyValuePattern.FindStringSubmatch(match)
		if len(parts) != 4 || !isSensitiveFieldName(parts[1]) {
			return match
		}

		return parts[1] + parts[2] + SecretRedactionMarker
	})

	// 9. Bare credential values, with no surrounding field name. Last, so a
	// vendor token is matched against the text as it was written rather than
	// against a version some earlier pass has already carved into.
	for _, pattern := range bareSecretPatterns {
		s = pattern.ReplaceAllString(s, SecretRedactionMarker)
	}

	return s
}

// redactCardNumbers redacts digit runs that pass the Luhn check, and ONLY those.
//
// THE GATE IS THE POINT. A 12-to-19-digit run, grouped or not, is also an order
// number, a ledger id, an epoch-millisecond timestamp and half the correlation
// ids in a fintech error string. Redacting the shape unconditionally would empty
// out the messages this package exists to keep diagnosable, and it would do it
// silently. Luhn is what a card number satisfies and an arbitrary identifier
// satisfies only one time in ten.
func redactCardNumbers(s string) string {
	// REPEATED TO A FIXED POINT. The candidates run in sequence, and a span one
	// pattern rejected — and therefore skipped past — can be re-partitioned by a
	// later one, leaving behind a run that nobody looks at again. That is not
	// hypothetical: an uneven grouping matched the Amex shape, and redacting it
	// exposed three four-digit groups the uniform pattern had already walked
	// over, so the card sitting in them survived until something ran the
	// sanitizer a second time.
	//
	// Each round that changes anything replaces at least twelve characters with
	// four, so the string strictly shrinks and this terminates. In practice it
	// settles on the first or second round.
	for {
		next := s
		for _, pattern := range cardCandidatePatterns {
			next = pattern.ReplaceAllStringFunc(next, redactCardCandidate)
		}

		if next == s {
			return s
		}

		s = next
	}
}

// redactCardCandidate decides one candidate.
func redactCardCandidate(candidate string) string {
	digits := cardSeparators.Replace(candidate)

	// TOO LONG TO BE ONE CARD, so the run necessarily holds something BESIDES a
	// card and has to be searched rather than dismissed. Found by fuzzing: the
	// grouped pattern is greedy, so "0000 4741 8529 6307 4182" matched as twenty
	// digits, failed the bound, and was handed back whole — the PAN inside it
	// never became a candidate of its own, and a four-digit code in front of a
	// card number is an ordinary thing for an acquirer error to print.
	if len(digits) > maxCardDigits {
		return redactCardInsideRun(candidate)
	}

	if len(digits) < minCardDigits || !passesLuhn(digits) {
		return candidate
	}

	return SecretRedactionMarker
}

// redactCardInsideRun looks for a card-length window of whole groups inside a run
// that is too long to be one card, and redacts the first it finds.
//
// IT IS DELIBERATELY NOT APPLIED to a run that is already card-length and merely
// fails Luhn. That run is an ordinary identifier, and hunting sub-windows inside
// one would redact roughly one identifier in ten — silently emptying the messages
// this package exists to keep diagnosable, which is the worse failure of the two.
func redactCardInsideRun(run string) string {
	groups := digitGroupPattern.FindAllStringIndex(run, -1)

	// WIDEST WINDOW FIRST, ACROSS THE WHOLE RUN, rather than longest-at-each-
	// starting-point. Scanning per start position would let a shorter window
	// that happens to satisfy Luhn win at an earlier offset and redact a span
	// only partly overlapping the real card, leaving the rest of its digits in
	// the clear. Preferring width means a sixteen-digit reading always beats a
	// twelve-digit one, wherever each begins.
	for width := len(groups); width >= 2; width-- {
		for i := 0; i+width <= len(groups); i++ {
			start, end := groups[i][0], groups[i+width-1][1]

			digits := cardSeparators.Replace(run[start:end])
			if len(digits) < minCardDigits || len(digits) > maxCardDigits || !passesLuhn(digits) {
				continue
			}

			// Only the tail is rescanned here. The head is left to the next round
			// of the fixed-point loop in redactCardNumbers, which sees it as a
			// run of its own.
			return run[:start] + SecretRedactionMarker + redactCardInsideRun(run[end:])
		}
	}

	return run
}

// passesLuhn reports whether the digits satisfy the Luhn checksum every payment
// card number carries.
//
// It is implemented here rather than shared with commons/outbox's copy because
// sanitize is a leaf package and outbox is not: importing outbox to reach one
// checksum would hang a broker, a dispatcher and two database adapters off every
// service that only wanted to redact a log line. Consolidating the two belongs
// with the replacement of the outbox redactor, not here.
func passesLuhn(number string) bool {
	sum := 0
	double := false

	for i := len(number) - 1; i >= 0; i-- {
		digit := int(number[i] - '0')
		if digit < 0 || digit > 9 {
			return false
		}

		if double {
			if digit *= 2; digit > 9 {
				digit -= 9
			}
		}

		sum += digit
		double = !double
	}

	return sum%10 == 0
}

// Error wraps err so its MESSAGE is redacted while its CHAIN stays intact.
//
// This is the logging-boundary companion to the error-returning helpers
// elsewhere in lib-commons: wrap at the point where a message becomes a log
// line, a span attribute or a stored column, not at the point where the error is
// produced.
//
//	if err := db.PingContext(ctx); err != nil {
//	    return sanitize.Error(fmt.Errorf("ping ledger pool: %w", err))
//	}
//
// CLASSIFICATION SURVIVES, THE RAW TEXT DOES NOT. errors.Is and errors.As still
// reach everything underneath, because a caller must still be able to classify
// the driver error it may no longer print. What is deliberately NOT provided is
// Unwrap: with it, errors.Unwrap(sanitized).Error() hands any caller the raw DSN
// straight back, and every redaction above is decorative.
//
// # What errors.As can still reach, measured
//
// Withholding Unwrap is NOT by itself a seal, and the earlier claim here that no
// caller could obtain the cause was wrong. errors.As assigns the first value in
// the chain assignable to its target, so a target naming an INTERFACE that the
// cause implements and the wrapper does not skips the redaction entirely. The
// printing interfaces are closed for that reason — the wrapper now implements
// fmt.Stringer and fmt.Formatter itself, so those targets are assigned the
// wrapper and print the redacted message.
//
// ONE DOOR IS LEFT OPEN ON PURPOSE: a target of interface{ Unwrap() error }
// still reaches a wrapper inside the cause, and that value's Error() is the raw
// text. Closing it would mean refusing every interface target, which would take
// down legitimate classification (interface{ SQLState() string } and its kin)
// along with it. So the rule for callers is behavioural, not enforced: ASK,
// CLASSIFY, AND PRINT ONLY THE WRAPPER.
//
// Returns nil for a nil error — including a typed nil pointer inside a non-nil
// interface — so it composes at a return site without a guard.
func Error(err error) error {
	if err == nil {
		return nil
	}

	// A NIL POINTER INSIDE A NON-NIL INTERFACE is not caught above, and calling
	// Error() on it panics for any implementation that reads a field — which is
	// most of them. A panic here lands on an error path, on top of the failure
	// being reported, in a helper whose whole job is to be safe to call.
	if v := reflect.ValueOf(err); v.Kind() == reflect.Ptr && v.IsNil() {
		return nil
	}

	return &redactedError{message: String(err.Error()), cause: err}
}

// redactedError carries the sanitized message and the untouched cause.
type redactedError struct {
	message string
	cause   error
}

func (e *redactedError) Error() string { return e.message }

// Is and As delegate to the cause so errors.Is and errors.As keep classifying.
// Both are the hooks errors.Is/As look for before they walk Unwrap.
func (e *redactedError) Is(target error) bool { return errors.Is(e.cause, target) }

func (e *redactedError) As(target any) bool { return errors.As(e.cause, target) }

// String and Format CLOSE THE PRINTING DOORS THAT WITHHOLDING Unwrap LEFT OPEN.
//
// errors.As assigns the first value in the chain assignable to the target, and
// only falls through to the As method above when nothing matched. So a target
// naming an interface the WRAPPER did not implement but the CAUSE did —
// fmt.Stringer and fmt.Formatter, which driver and SDK error types routinely
// implement — skipped straight past the redaction and handed back a value whose
// own printing is the raw text. Implementing both here means the wrapper is what
// gets assigned.
//
// Format also replaces the GoString this type used to carry: writing the
// redacted message for EVERY verb covers %#v along with the rest, rather than
// patching one verb at a time.
func (e *redactedError) String() string { return e.message }

func (e *redactedError) Format(f fmt.State, _ rune) {
	_, _ = io.WriteString(f, e.message)
}

// isSensitiveFieldName delegates to the centralized lib-observability taxonomy,
// extended with the AWS/SASL addendum.
func isSensitiveFieldName(fieldName string) bool {
	return redaction.IsSensitiveField(fieldName, sensitiveFieldExtras...)
}

// redactURLUserinfo replaces the userinfo portion of EVERY URL inside one
// matched token.
//
// ONE MATCH IS NOT ONE URL. urlPattern's tail runs to the next whitespace, and a
// broker DSN is routinely a comma- or semicolon-separated list —
// commons/secretsmanager hands Kafka its brokers as exactly that string. Treating
// the match as a single authority had two failure modes, and the second is the
// bad one: with credentials on every entry, only the first was redacted; with the
// FIRST entry carrying no userinfo, the lookup for an at-sign failed and the
// whole run was returned untouched, no marker anywhere to tell an operator the
// line had not been scrubbed. The redactor commons/outbox already ships anchors
// per URL and is correct on both, so this was a regression against the thing it
// replaces.
func redactURLUserinfo(token string) string {
	starts := schemeStarts(token)
	if len(starts) <= 1 {
		return redactOneURL(token)
	}

	var out strings.Builder

	out.WriteString(token[:starts[0]])

	for i, start := range starts {
		end := len(token)
		if i+1 < len(starts) {
			end = starts[i+1]
		}

		out.WriteString(redactOneURL(token[start:end]))
	}

	return out.String()
}

// schemeStarts reports the offset at which each URL inside token begins.
//
// It anchors on "://" and rewinds over the scheme character class, then steps
// FORWARD to the first letter. The forward step is what keeps a glued run from
// mis-anchoring: rewinding out of "redis://h:6379-redis://u:p@h2" reaches back
// through "6379-redis", and a scheme starts with a letter, so the second URL
// begins at the "r" and not at the "6" that belongs to the first one's port.
func schemeStarts(token string) []int {
	var starts []int

	for i := 0; i+3 <= len(token); i++ {
		if token[i:i+3] != "://" {
			continue
		}

		start := i
		for start > 0 && isSchemeByte(token[start-1]) {
			start--
		}

		for start < i && !isLetter(token[start]) {
			start++
		}

		// No scheme letters in front of the separator: not a URL start.
		if start == i {
			continue
		}

		if len(starts) > 0 && start <= starts[len(starts)-1] {
			continue
		}

		starts = append(starts, start)
	}

	return starts
}

func isSchemeByte(c byte) bool {
	return isLetter(c) || (c >= '0' && c <= '9') || c == '+' || c == '.' || c == '-'
}

func isLetter(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// redactOneURL replaces the userinfo portion of one URL-shaped token, preserving
// scheme and host so the destination stays diagnosable.
//
// Done at the string level rather than through url.URL.String, which
// percent-escapes the marker.
//
// TRAILING SENTENCE PUNCTUATION NEEDS NO SPECIAL HANDLING, which is worth saying
// because it looks like it should. A URL swept out of a log line carries its
// trailing '.', ')' or '"' along, but that punctuation always lands AFTER the
// '@' — inside the tail this function copies verbatim — so stripping it first
// and re-appending it produces the same string. A differential run over ~12k
// generated scheme/userinfo/host/path/punctuation combinations found zero
// inputs where the two differ.
func redactOneURL(token string) string {
	schemeSep := strings.Index(token, "://")
	if schemeSep == -1 {
		return token
	}

	rest := token[schemeSep+3:]

	// The authority ends at the first '/', '?' or '#'; an '@' after that is part
	// of a path or query and is not userinfo.
	authorityEnd := len(rest)

	for i := range len(rest) {
		if c := rest[i]; c == '/' || c == '?' || c == '#' {
			authorityEnd = i

			break
		}
	}

	atIndex := strings.LastIndex(rest[:authorityEnd], "@")
	if atIndex == -1 {
		return token
	}

	tail := rest[atIndex:]
	userinfo := rest[:atIndex]

	if strings.Contains(userinfo, ":") {
		return token[:schemeSep+3] + SecretRedactionMarker + ":" + SecretRedactionMarker + tail
	}

	return token[:schemeSep+3] + SecretRedactionMarker + tail
}
