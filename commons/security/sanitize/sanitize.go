package sanitize

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"github.com/LerianStudio/lib-commons/v7/commons/internal/nilcheck"
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
// The bound exists because the cost is real, AND THE SHAPE OF THE INPUT DECIDES
// IT FAR MORE THAN THE LENGTH. Each regexp pass is linear, and on unbroken digits
// the whole of String costs roughly 400 milliseconds per megabyte on an ordinary
// devbox. The card pass is what varies: an over-long run goes to a window scan
// over its digit GROUPS, which costs one walk of the remaining groups for each
// card it finds. At this bound a 64 KiB line of grouped four-digit ledger ids
// that are not cards costs about 42 ms, and the worst shape there is — 64 KiB of
// back-to-back card numbers, where every window the scan tries is a real card —
// costs about 283 ms. 64 KiB is far above any error string worth reading and low
// enough that the ordinary shapes stay in the tens of milliseconds.
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

// queryValueTerminators are the bytes a query parameter's value does NOT admit.
//
// IT IS THE ONLY COPY, AND THAT IS THE POINT. The pattern's value class is built
// from this constant and redactQueryParameters' byte test reads the same
// constant, because the two were written out by hand once and drifted: the
// helper listed '\v' as a terminator and RE2's \s does not contain it
// ([\t\n\f\r ] only), so the regexp admitted '\v' as an ordinary value byte
// while the helper called a token ending there complete. The helper then
// extended a sensitive value over the run, and the next pass took "****\v" as
// one value and redacted further — a sanitizer whose output changed when it was
// run twice. A hand-copied character class drifting from its source is exactly
// the failure this helper exists to prevent one level down.
//
// '\v' IS IN THE SET, AND IT IS THE WHOLE OF [[:space:]] THAT IS. Keeping it
// out left this pass disagreeing with the key=value pass about a marker's
// right-hand edge once that one stopped at a vertical tab: "&Cpf=****\v postgres://..."
// came back as "&Cpf=**** postgres://...", the '\v' swallowed into the query
// value and deleted with it. A sanitizer whose output changes on the second run
// is the same defect as the drift above, arrived at from the other side, and
// FuzzString found it in ninety seconds.
//
// TWO CLASSES IN THIS FILE STILL SPELL \s, AND BOTH ARE ON THE OVER-REDACTING
// SIDE. urlPattern's tail and azureSASSignaturePattern's value run past a
// vertical tab instead of stopping at one, so "?sig=abc\vrc=200" comes back as
// "?sig=****" with the response code eaten, and a URL's authority is looked for
// across a longer span. Neither prints a credential, which is why they are left
// alone: a class that over-redacts on a byte is a diagnosability cost, and a
// class that under-redacts on it is a leak. The value and body classes — this
// one, the key=value separator and value, and the headless PEM body — are the
// ones that had to change, and they are the ones that did.
const queryValueTerminators = "&\t\n\v\f\r ,;\"'#"

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
//
// A GROUPED DIGIT RUN IS TAKEN WHOLE, and redactQueryParameters is where that
// happens rather than here. Stopping at whitespace is right for a credential and
// wrong for a card number printed the way it is printed on the card:
// "?pan=4111 1111 1111 1111" was ONE pair keyed on a value of "4111", so the
// pass scrubbed that group and the twelve digits after it were already orphaned
// by the time the card pass ran. The line reached the log as
// "?pan=**** 1111 1111 1111" — a marker asserting it had been scrubbed, with an
// ordinary sixteen-digit card beside it. Any URL-shaped log line carried it.
var queryParameterPattern = regexp.MustCompile(
	`([?&])([A-Za-z0-9_.-]+)=([^` + regexp.QuoteMeta(queryValueTerminators) + `]+)`)

// keyValueSeparator is the optional spaces, '=', optional spaces that joins a
// key to its value. IT IS WRITTEN ONCE. keyValuePattern is built from it and so
// is the anchored lookahead below, because a hand-written copy of this class
// drifted the moment it existed: " \t" missed the \n, \v, \f and \r that
// [[:space:]] also holds, and three of the five separators stayed
// non-idempotent. That is the same failure queryValueTerminators exists to
// prevent one pass down, repeated here, which is why this constant exists
// rather than a second spelling of the class.
const keyValueSeparator = `[` + keyValueValueSpace + `]*=[` + keyValueValueSpace + `]*`

// keyValueValueSpace is the whitespace a VALUE refuses, and the class the
// separator's trailing run is built from rather than a second one that looks
// like it.
//
// THE TWO CLASSES HAVE TO BE COMPLEMENTS OR THE PATTERNS DISAGREE ABOUT ONE
// BYTE, AND THAT BYTE IS '\v'. [[:space:]] holds it and RE2's \s ([\t\n\f\r ])
// does not — over all 256 bytes it is the only place the two sets differ — and
// the disagreement leaked in BOTH directions, one per side of the separator.
//
// On the key side, a value class of [^\s,;&] admitted '\v' while a separator
// ending in [[:space:]]* ate it. In the full pattern the value's '+' makes the
// separator give that byte back; keyPrefixPattern has nothing after the
// separator to force it, so its match ended ONE BYTE PAST where the full
// pattern's value starts, every offset the walker compares against the value's
// end was off by one on a line ending in "<name>=\v", and "k=k=pwd=\v" went to
// the log untouched.
//
// On the value side, a lone '\v' WAS a value. "password=\v hunter2" put the
// marker over the whitespace byte and printed the credential behind it, under a
// line asserting it had been scrubbed — which is the worse half, and the half a
// value class written as the complement of \s could not avoid.
//
// [[:space:]] IS THE WHITESPACE, once, written as the body of a class so both
// sides spell the same set. This is the THIRD appearance of this asymmetry in
// this file — queryValueTerminators and the separator above both carry the
// story of the previous two.
const keyValueValueSpace = `[:space:]`

// keyValueValue is one byte of a key=value value: anything but the whitespace
// above and the separators that start the next entry in a list.
const keyValueValue = `[^` + keyValueValueSpace + `,;&]`

// keyValuePair is the pair itself — key, separator, value — and it is written
// ONCE for the same reason the separator is: the anchored form below has to be
// the SAME question asked at a known offset, not a second spelling of it.
const keyValuePair = `\b(` + keyValueName + `)(` + keyValueSeparator + `)(` + keyValueValue + `+)`

// keyValueName is the shape of a field name: what a key is allowed to be.
const keyValueName = `[a-z][a-z0-9._-]*`

// keyValuePattern finds key=value fragments in config dumps and driver errors.
// Sensitivity is decided by the field name, not by a package-local taxonomy.
//
// The value runs to the next whitespace, comma, semicolon or ampersand, which
// means a trailing colon is swallowed into the redaction ("token=abc: refused"
// becomes "token=**** refused"). That errs toward redacting one character too
// many rather than one too few, which is the correct direction here.
var keyValuePattern = regexp.MustCompile(`(?i)` + keyValuePair)

// nextPairPattern is keyValuePattern anchored, for asking whether a whole pair
// begins exactly here rather than somewhere ahead. Anchored because the
// unanchored search scans to the end of the line on failure, and this question
// is asked once per redacted pair: on a line that is nothing but pairs that is
// the quadratic the walker exists to avoid.
var nextPairPattern = regexp.MustCompile(`(?i)^` + keyValuePair)

// bareValueAheadPattern is the separator's whitespace followed by a RUN of the
// bytes the value class admits: where the bare value behind this one starts,
// and where it ends. Built from the same classes as the pair above so it cannot
// drift.
//
// THE RUN IS CAPTURED BECAUSE THE REDACTION REACHES THROUGH IT. sensitiveValueEnd
// reads the run's END and carries the marker to it, so a capture of one byte
// would end the redaction one byte into the token it is covering.
// bareValueFollows asks only whether the run exists, and could have made do with
// a single byte; there is one pattern rather than two because a second spelling
// of this class is how every other drift in this file started.
var bareValueAheadPattern = regexp.MustCompile(`^[` + keyValueValueSpace + `]*(` + keyValueValue + `+)`)

// nextPairSeparatorPattern is keyValueSeparator anchored at the start of the
// text, for asking whether what follows a value is the next pair's separator.
var nextPairSeparatorPattern = regexp.MustCompile(`^` + keyValueSeparator)

// keyPrefixPattern is keyValuePattern WITHOUT its value class: the same key and
// the same separator, both built from the constants keyValuePair is built from,
// so it finds a nested pair's key at exactly the offset the full pattern would.
//
// IT IS WHAT MAKES A CHAIN OF KEYS LINEAR. The value class runs to the next
// whitespace, comma, semicolon or ampersand, so on a line with none of those
// every key in "a=b=c=...=password=hunter2" owns a value reaching the end of the
// input. Re-running the full pattern for each key in that chain re-measured the
// same tail every time, which is quadratic: 64 KiB of it cost 50 seconds inside
// one log call, and 96 seconds before the walker short-circuited part of the
// chain. This pattern stops at the separator, and the value's end is carried
// along instead of re-derived, because it cannot move: a value is a run with no
// terminator in it, so every key nested inside one ends at the same byte.
var keyPrefixPattern = regexp.MustCompile(`(?i)\b(` + keyValueName + `)` + keyValueSeparator)

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
	// GitHub FINE-GRAINED personal access tokens, which are the ones GitHub now
	// issues and carry a prefix the rule above does not cover at all. The body
	// admits '_' and is much longer than the floor here; the floor only has to
	// be past the point where an ordinary identifier could reach it.
	regexp.MustCompile(`\bgithub_pat_[0-9A-Za-z_]{20,}\b`),
	// Stripe secret and restricted keys, live and test.
	regexp.MustCompile(`\b(sk|rk)_(live|test)_[0-9A-Za-z]{16,}\b`),
	// Slack tokens: the xox* family, plus xoxe- (the refresh token issued under
	// token rotation) and xapp- (an app-level token, which is what a Socket Mode
	// client echoes when it fails to connect). Both were outside the xox[baprs]
	// class entirely.
	regexp.MustCompile(`\b(?:xox[baprse]|xapp)-[0-9A-Za-z-]{10,}\b`),
	// Bare JWTs, anchored on the "eyJ" header so it does not eat an ordinary
	// dotted identifier.
	//
	// THE SEGMENTS ADMIT '=' AND THE SIGNATURE MAY BE EMPTY. Base64url padding
	// is legal and libraries emit it, and a class without '=' matched up to the
	// pad and then failed its closing boundary, so a padded token survived
	// whole. The empty signature is the alg:none shape - two segments and a
	// trailing dot - which is the one token worth catching most, because it is
	// what an attacker sends. The closing \b goes with it: a match ending on the
	// dot has no word character behind it for the boundary to hold on.
	regexp.MustCompile(`\beyJ[0-9A-Za-z_=-]+\.[0-9A-Za-z_=-]+\.[0-9A-Za-z_=-]*`),
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
	// Grouped as printed. TWO READINGS IN ONE PATTERN, AND THE ORDER OF THE
	// BRANCHES IS THE WHOLE POINT.
	//
	// The second branch is the uniform one: 4-4-4, 4-4-4-4, and any longer run.
	// THE REPEAT IS UNBOUNDED ON PURPOSE. Capping it truncates the run mid-way,
	// and a card that straddles the cut is then invisible to the window scan that
	// would otherwise find it — which is how a PAN behind two leading four-digit
	// groups survived. Length is judged on the digits, below, not here.
	//
	// The first branch is three four-digit groups and a SHORT TAIL: the 13, 14
	// and 15 digit cards, printed four-four-four-and-the-rest. Without it the
	// uniform branch took the three groups and stopped — twelve digits, which is
	// card-length, so it failed Luhn and was left alone as an ordinary identifier
	// rather than searched, and the tail never joined anything. Behind a field
	// name that was the headless-PEM failure again: the key=value pass stops at
	// the first space, so the log line read "pan=**** 8224 6310 005", eleven
	// digits of a live card behind a marker asserting it had been scrubbed.
	//
	// THEY CANNOT BE TWO SEPARATE PATTERNS, and this is the part that is not
	// obvious. Go's regexp is leftmost-FIRST: branch order is a preference among
	// readings that start at the SAME offset, but a separate pattern slides all
	// the way across the string before the next pattern is tried at all. Measured
	// over 400,000 generated cards, both orderings of two patterns leak about one
	// card in ten — the uniform pattern first strands the tail of a 13-to-15
	// digit card whenever its leading twelve digits happen to satisfy Luhn, and
	// the short-tail pattern first matches one group late on a sixteen-digit card
	// that has a response code after it, redacting the wrong span and leaving the
	// leading group in the clear. As one pattern with the short-tail branch
	// first, both readings are offered at the earliest start and neither shape
	// leaks: at that offset the tail branch matches only when no fourth group
	// follows, because \b refuses to end a 1-to-3 digit tail in front of another
	// digit.
	regexp.MustCompile(`\b\d{4}(?:[ .-]\d{4}){2}[ .-]\d{1,3}\b|\b\d{4}(?:[ .-]\d{4}){2,}\b`),
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

// pemArmorPattern matches one PEM armor line, BEGIN or END, with the keyword
// and label folded so a lowercased block is still found; see redactPemBlocks.
var pemArmorPattern = regexp.MustCompile(`-----(?i:(BEGIN|END) [A-Z0-9 ]+)-----`)

// pemHeadlessBodyPattern is the body of a block whose END line never arrives:
// base64-legal bytes AND whitespace, anchored where the armor line ended.
//
// THE WHITESPACE IS keyValueValueSpace, WHICH IS WHY IT IS NOT SPELLED HERE.
// This class was written as RE2's \s, and \s is [[:space:]] without the
// vertical tab — so a body that began after one ended at the armor line and the
// base64 behind it reached the log under a marker asserting the line had been
// scrubbed. "-----BEGIN A-----\vMIIB..." printed its body on every head this
// package has shipped; the key=value pass hid the keyed spelling for as long as
// a vertical tab was a value byte for it, and stopped hiding it when that was
// corrected. A space or a tab in the same position was always covered, which is
// what makes this an asymmetry between two spellings of one set rather than a
// decision about PEM. It is the fourth time that asymmetry has cost this file a
// leak, and the last class that had it.
var pemHeadlessBodyPattern = regexp.MustCompile(`^[` + keyValueValueSpace + `A-Za-z0-9+/=]*`)

// redactPemBlocks replaces each PEM block — armor lines and body — with the
// marker, so the base64 payload between the BEGIN and END lines never reaches a
// log line.
//
// THE CLOSING LINE IS NOT REQUIRED. A block whose -----END went missing is the
// ordinary accident — a key pasted out of a kubectl output, a value a config
// loader cut, a file that ended a line early — and with the END mandatory that
// block matched nothing here at all. What reached the log then was
// "private_key=**** RSA PRIVATE KEY-----" followed by the entire body: the
// key=value pass had taken "-----BEGIN" as the value and stopped at the first
// space, so the line carried a marker asserting it had been scrubbed and the
// whole armored payload behind it. That is worse than no redaction, because it
// tells the reader there is nothing left to look for.
//
// THE WELL-FORMED READING WINS, AND IT HAS TO. Each BEGIN line is paired with
// the next END line at or after it, so a block that HAS its END line is
// consumed to that line whatever it contains — which is what keeps an ENCRYPTED
// key intact. RFC 1421 headers ("Proc-Type: 4,ENCRYPTED", "DEK-Info:
// DES-EDE3-CBC,...") carry '-', and a body read as a run of base64-legal bytes
// stops on the first one and leaves the payload after it in the clear.
//
// A BEGIN WITH NO END BEHIND IT ANYWHERE falls back to pemHeadlessBodyPattern,
// base64-legal bytes AND whitespace, which runs past the end of the armor into
// whatever prose follows on the same lines. That over-redaction is accepted:
// the alternative is leaving a private key in a log, and the shape only arises
// on a block that is already malformed.
//
// THE ARMOR IS MATCHED CASE-INSENSITIVELY, scoped to the keyword and label so
// the body class is not: a pipeline that lowercases what it stores exists,
// canonical PEM is uppercase so nothing valid is lost, and the armor is still a
// private key whatever case "BEGIN RSA PRIVATE KEY" is written in. Redacting a
// lowercased block is the safe direction and the shape is distinctive enough
// that it costs nothing else.
//
// THE TWO ARMOR LINES ARE FOUND ONCE AND PAIRED, rather than searched for once
// per block, and that is a cost fix rather than a taste one. Asked as one
// pattern with the END line optional, the well-formed reading is a LAZY run to
// the first END line, so a BEGIN line with no END behind it scanned to the end
// of the input before falling back to the body class — once per BEGIN line.
// 64 KiB of armor with no END line cost 6.5 seconds on the goroutine writing
// the log line, against 0.02 for a well-formed block of the same size, and the
// armor is trivial to write.
func redactPemBlocks(s string) string {
	armor := pemArmorPattern.FindAllStringSubmatchIndex(s, -1)
	if armor == nil {
		return s
	}

	// The next END line at or after each armor line, walked once from the back.
	// A BEGIN line is never an END line, so this is the END line strictly behind
	// it — which is the one the lazy run would have stopped at.
	nextEnd := make([]int, len(armor))

	next := -1

	for i, a := range slices.Backward(armor) {
		if strings.EqualFold(s[a[2]:a[3]], "END") {
			next = i
		}

		nextEnd[i] = next
	}

	var out strings.Builder

	pos := 0

	for i, line := range armor {
		// A stray END line is not a block, and an armor line inside a block
		// already replaced is not a second one.
		if line[0] < pos || nextEnd[i] == i {
			continue
		}

		end := line[1]
		if nextEnd[i] >= 0 {
			end = armor[nextEnd[i]][1]
		} else {
			end += len(pemHeadlessBodyPattern.FindString(s[line[1]:]))
		}

		out.WriteString(s[pos:line[0]])
		out.WriteString(SecretRedactionMarker)

		pos = end
	}

	out.WriteString(s[pos:])

	return out.String()
}

// String strips credential material from a free-form string so DSNs, broker
// URLs, SASL passwords, bearer tokens, AWS/GCP/GitHub/Stripe/Slack keys, JWTs
// and PEM private-key blocks never reach a log line, a span attribute or a
// stored error column.
//
// It preserves everything that makes a failure diagnosable — scheme, host, port,
// database, non-sensitive parameters — and is safe on an empty string, on a
// string with no credentials (returned verbatim), and on a string it has already
// sanitized (re-running it does not mangle the marker).
//
// RE-RUNNING IS SAFE UP TO THE BOUND, NOT PAST IT. A pass can grow what it
// returns by up to 1.60x, so an input of 40 KiB or more of URL userinfo
// comes back longer than MaxInputLen and the SECOND call answers with the
// over-bound refusal instead of the redacted text. That is the safe direction —
// a refusal naming a size, never a credential — and the bound is unchanged
// here; a caller that re-sanitizes at that size is asking twice for work the
// first call already did.
func String(s string) string {
	if s == "" {
		return ""
	}

	// THE BOUND IS CHECKED ONCE, ON THE WAY IN, AND A ROUND CAN GROW THE STRING
	// PAST IT. 64 KiB of "cpf=# " settles at 98,302 bytes and 64 KiB of
	// "=Rg =0 " at 93,622 — 1.50 and 1.43 times the bound — and the worst
	// measured shape is a URL, "A://a:b@h ", at 1.60 (65,530 in, 104,848 out).
	// "keY=#" is NOT one of them: it collapses to eight bytes, since the whole
	// separator-free run is one value.
	//
	// A per-round check would buy nothing. The growth is ONE-SHOT: a site grows
	// when its value is first replaced by the marker, and the marker is a fixed
	// point of every pass that can match it, so the second round of both shapes
	// above is already the fixed point and adds nothing. The ceiling is
	// therefore a property of the input, not of the number of rounds, and the
	// rounds after the first cost what a scan of that settled string costs —
	// which the bound tests measure at the limit. Re-checking would only turn a
	// completed redaction into a refusal that names a size.
	if len(s) > MaxInputLen {
		return fmt.Sprintf("%s (input of %d bytes exceeds the sanitizer bound; not scanned)",
			SecretRedactionMarker, len(s))
	}

	// THE PIPELINE RUNS TO A FIXED POINT, AND THAT IS THE CONTRACT, NOT A TIDINESS.
	//
	// Four separate defects in two passes were one family: a later pass rewrites
	// or deletes a byte an EARLIER pass used as a token boundary, so a second run
	// parses a different string than the first and produces a different answer.
	// The URL pass ended an authority at '#', the key=value pass then redacted
	// "keY=#" to "keY=****" and deleted that '#', and the second run found an '@'
	// inside the authority and collapsed the userinfo. Same shape with '/' and
	// '?'. Separately, a credential-shaped token stole the key slot in front of
	// "=Rg =0" so the RG was never seen, until the bare pass replaced the thief
	// with a marker and the second run tokenised it correctly.
	//
	// In EVERY one of those the second run's answer was the right one: the first
	// run had missed a secret. Iterating is therefore not belt and braces, it is
	// the redaction the package already promised — the fuzz harness has asserted
	// String(String(x)) == String(x) all along, so the fixed point is simply what
	// that assertion says the answer is.
	//
	// TERMINATION IS THE CAP, NOT A SHRINKING ARGUMENT. Do not read this loop as
	// "each round is strictly smaller": it is not. "keY=#" becomes "keY=****",
	// which is longer, so there is no monotone measure to descend. What bounds it
	// is maxSanitizeRounds, plus measurement — and the measurement is THREE, not
	// two. "A://keY=/&@" needs the key=value pass to redact the value and delete
	// the '/' (one), the URL pass to then see the '@' that is suddenly inside the
	// authority (two), and a third round to confirm nothing further moves. Most
	// inputs need one or two; about one in seven of the URL and key=value shapes
	// needs three, and nothing measured has ever needed four.
	//
	// TestEveryCorpusInputSettlesWithinThreeRounds pins that across the committed
	// corpus, every string literal in the test sources and the card, query, URL
	// and key=value generators. The fuzz harness still asserts idempotence under
	// the cap, so an input needing more rounds than the cap surfaces as a fuzz
	// failure rather than as silence.
	for range maxSanitizeRounds {
		next := sanitizeOnce(s)
		if next == s {
			break
		}

		s = next
	}

	return s
}

// maxSanitizeRounds bounds the fixed-point loop in String.
//
// Three rounds is what the worst measured input needs, and most need one or two.
// Four leaves exactly one round of headroom for a shape nobody has generated yet
// without letting a pathological input spin: the cost of the cap is paid only by
// an input that keeps changing, and such an input gets the fourth round's
// output, which is more redacted than the first round's, never less.
const maxSanitizeRounds = 4

// sanitizeOnce is one pass of the pipeline. String calls it until it stops
// changing its input; see the termination note there.
func sanitizeOnce(s string) string {
	// 1. PEM BLOCKS FIRST, AND THE ORDER IS LOAD-BEARING. An armored block is
	// often the VALUE of a sensitive key ("private_key=-----BEGIN ..."), which is
	// how a config-loading error echoes one. Let the key=value pass run first and
	// it consumes "-----BEGIN" as that key's value, leaving no marker for this
	// rule to anchor on — and the entire base64 body survives into the log.
	s = redactPemBlocks(s)

	// 2. URL-shaped tokens: strip userinfo from EVERY URL in the match, keep
	// scheme and host.
	s = urlPattern.ReplaceAllStringFunc(s, redactURLUserinfo)

	// 3. Sensitive query parameters, by field name. Before the key=value pass,
	// which would otherwise swallow the whole URL as one non-sensitive pair.
	s = redactQueryParameters(s)

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
	s = redactKeyValuePairs(s)

	// 9. Bare credential values, with no surrounding field name. Last, so a
	// vendor token is matched against the text as it was written rather than
	// against a version some earlier pass has already carved into.
	for _, pattern := range bareSecretPatterns {
		s = pattern.ReplaceAllString(s, SecretRedactionMarker)
	}

	return s
}

// redactKeyValuePairs walks the pairs so a "value" that is really the next
// pair's key can be handed back to the scanner instead of being consumed.
func redactKeyValuePairs(s string) string {
	var out strings.Builder

	pos := 0

	for pos < len(s) {
		loc := keyValuePattern.FindStringSubmatchIndex(s[pos:])
		if loc == nil {
			break
		}

		for i := range loc {
			if loc[i] >= 0 {
				loc[i] += pos
			}
		}

		key, valueStart, valueEnd := s[loc[2]:loc[3]], loc[6], loc[7]

		valueStart, valueEnd, sensitive, rewind := resolvePair(s, key, valueStart, valueEnd)

		// Every rewind moves past at least the key and the separator, so pos
		// strictly increases and the walk still terminates.
		if rewind {
			out.WriteString(s[pos:valueStart])

			pos = valueStart

			continue
		}

		replacement := SecretRedactionMarker
		if !sensitive {
			replacement = keyValuePattern.ReplaceAllStringFunc(s[valueStart:valueEnd], redactKeyValuePair)
		}

		out.WriteString(s[pos:valueStart])
		out.WriteString(replacement)

		pos = valueEnd
	}

	out.WriteString(s[pos:])

	return out.String()
}

// prefixInsideValue finds the key of a pair nested inside a value, and only
// when the value ends in the separator that pair would be introduced by.
func prefixInsideValue(s string, valueStart, valueEnd int) []int {
	if s[valueEnd-1] != '=' {
		return nil
	}

	return keyPrefixPattern.FindStringSubmatchIndex(s[valueStart:valueEnd])
}

// bareValueFollows reports whether the text behind a value that ended in the
// separator holds a value the scanner can redact in its place.
func bareValueFollows(s string, valueEnd int) bool {
	ahead := bareValueAheadPattern.FindStringSubmatchIndex(s[valueEnd:])
	if ahead == nil {
		return false
	}

	return !nextPairPattern.MatchString(s[valueEnd+ahead[2]:])
}

// introducesAValue returns where to look for the value that the token
// s[start:end] would introduce if the field name it ends with were a key, or -1
// when it introduces nothing.
//
// THE TWO SHAPES DIFFER ON WHERE THE SEPARATOR SITS, and that is the whole
// difference in what a following pair means. A token that ENDS in the separator
// ("abc_token=") is already a complete value, so a pair behind it
// ("password=abc_token= rc=200") is the next pair and the value was the literal
// text: a bare value is required there, and bareValueFollows refuses one that
// starts a pair. A token with the separator BEHIND it and outside the value
// ("password=secret =0") leaves a separator with no value at all unless what
// follows it is one — there is no second reading to weigh, so whatever follows
// is that value, pair-shaped or not.
//
// THE FIRST SHAPE ASKS ITS QUESTION TWICE, AND THE SECOND ASKING IS THE LOOSE
// ONE. keyPrefixPattern needs the name to ABUT the separator, so a name the
// writer quoted, bracketed or parenthesised slips past the rewind entirely:
// "password=\"cpf\"= hunter2", "password=cpf]= hunter2" and
// "password=(cpf)= hunter2" each put the marker over the value and copied the
// password out beside it. Asking isSensitiveFieldName of the whole token in
// front of the '=' reaches those, and it is ADDED beside the rewind rather than
// replacing it: U+212A folds to 'k' but is not an ASCII word byte, so
// "secret=\u212Acpf=" is a name to the rewind and not to the whole-token
// question, and substituting one for the other reopened that whole family.
//
// ALL THREE SHAPES ASK THE SAME QUESTION OF THE NAME, AND IT IS THE LOOSEST ONE
// THE PACKAGE HAS. The second used to require the token to BE a field name and
// nothing else, and a driver that pads its '=' prints the pair the other way
// round often enough to matter: "secret=[cpf =12345678901",
// "pgx: opt=password =!password =hunter2", "unique constraint: key=cpf =(cpf
// =12345678901". None of those values is a clean name, so each introduced
// nothing, and the credential behind the spaced separator was copied out
// whole — while "secret=[cpf= 12345678901", one space to the left, was already
// redacted by the first shape. Under redact-both the substring question can
// only cost one over-redacted token; demanding the whole name cost the other
// reading of the line.
//
// A TOKEN THE CHAIN REACHED HAS NO SECOND READING LEFT, AND THAT IS WHAT
// chained SAYS. The bare-value requirement above is kept for the FIRST token
// of a value, and that is a choice, not a derivation: the first token and a
// chained one are both a maximal run that stopped on whitespace, so a
// pair-shaped tail is "the next pair" behind either of them. On the first
// token the package keeps that pair, whatever it carries:
// "password=abc_token= rc=200" keeps its response code, and
// "password=session_token= sid=9f3ca82b1d4e" keeps the session id beside the
// marker just the same. That row is pinned as a known gap. On a token the
// chain STEPPED ONTO the same refusal ended the span between a key the chain
// swallowed and the value that key was protecting: "password=cpf= = cpf=
// sig=abc" printed sig=abc in the clear on a line carrying a marker that said
// it had been scrubbed. That half is closed, and it is the only half this
// flag closes: on a chained token the question is only what it introduced,
// and the answer goes under the marker with it.
func introducesAValue(s string, start, end int, chained bool) int {
	// THE SPACED SHAPE ASKED FIRST, WHICH IS WHAT THIS CLAUSE IS: the same
	// question as the last one, with a lookahead, and it is here rather than
	// there because first-match-wins decides between two readings that DISAGREE
	// ON THE END. A token can carry both spellings at once:
	// "password=\"cpf\"= = hunter2" ends in a bare '=' AND has a padded " = "
	// behind it. The bare-'=' clause below returns the end of the token; the
	// spaced clause returns the end of the separator, one reading further
	// along. The shorter end is a PREFIX of the longer one, so whichever is
	// asked first wins, and the shorter one leaves the credential outside the
	// match: the marker lands on the lone '=' and hunter2 is copied out beside
	// it, which is what 90b10a1 printed. The longer reading is the one that
	// covers the credential, so it is the one asked first, and the lookahead is
	// what keeps it from firing where there is no value to cover
	// ("password=\"cpf\"= =") and where the text behind the separator is not a
	// bare value at all.
	//
	// The bare-'=' clause asks isSensitiveFieldName of s[start:end-1] and this
	// one asks it of s[start:end]; on the current taxonomy those are the same
	// question, because a trailing '=' is a boundary byte on every path of
	// IsSensitiveField — do not "fix" either one into the other.
	if sep := nextPairSeparatorPattern.FindString(s[end:]); sep != "" &&
		isSensitiveFieldName(s[start:end]) &&
		bareValueAheadPattern.MatchString(s[end+len(sep):]) {
		return end + len(sep)
	}

	if loc := prefixInsideValue(s, start, end); loc != nil && start+loc[1] == end &&
		isSensitiveFieldName(s[start+loc[2]:start+loc[3]]) &&
		(chained || bareValueFollows(s, end)) {
		return end
	}

	if s[end-1] == '=' && isSensitiveFieldName(s[start:end-1]) &&
		(chained || bareValueFollows(s, end)) {
		return end
	}

	if sep := nextPairSeparatorPattern.FindString(s[end:]); sep != "" &&
		isSensitiveFieldName(s[start:end]) {
		return end + len(sep)
	}

	return -1
}

// sensitiveValueEnd is where a SENSITIVE key's redaction ends: past its value,
// and past the bare value a field name at the end of that value would
// introduce, chained for as long as each token it covers introduces the next.
//
// ON A SENSITIVE KEY THIS PACKAGE NO LONGER CHOOSES A READING, AND THAT IS WHY
// THE RULE IS SHAPED THIS WAY. "password=password =0" is a weak password whose
// text happens to be a field word, and it is also, byte for byte, a key whose
// value sits behind the spaced '='. Four passes in a row shipped a sharper test
// of which one it was — is the value a whole name, is the name sensitive, does
// a word byte sit in front of it — and each one printed the reading it had
// ruled out: the credential itself under the marker's key, or the credential
// the name introduced, orphaned outside the match and copied across verbatim.
//
// So the value is redacted, always, and the redaction EXTENDS over the other
// reading instead of picking it. The extension trigger is the substring and
// camelCase question isSensitiveFieldName asks, and it has to be the LOOSEST
// test in the package rather than the tightest: a false positive costs ONE
// over-redacted word, the direction this file already declares safe, and a
// false negative costs the token the name introduces — which under the second
// reading IS the credential. The value goes under the marker either way, but
// the other reading does not, and "secret=[cpf =12345678901" is what a trigger
// that demanded a clean whole name printed.
//
// It terminates because each step starts at or after the previous end and the
// value class needs at least one byte, so end strictly increases and is bounded
// by the length of the string.
func sensitiveValueEnd(s string, valueStart, valueEnd int) int {
	start, end := valueStart, valueEnd
	chained := false

	for {
		after := introducesAValue(s, start, end, chained)
		if after < 0 {
			return end
		}

		ahead := bareValueAheadPattern.FindStringSubmatchIndex(s[after:])
		if ahead == nil {
			return end
		}

		start, end, chained = after+ahead[2], after+ahead[3], true
	}
}

// resolvePair decides which pair the match at [valueStart, valueEnd) really is.
// It returns the span to redact — which on a sensitive key can reach past the
// value, see sensitiveValueEnd — whether the key that owns it is sensitive,
// which it has already had to decide and which is all the walker wanted the key
// for, and whether the walker should hand the position back to the scanner
// instead of redacting here.
//
// end IS MEANINGLESS WHEN rewind IS TRUE. The caller reads it only on the path
// that redacts, and the rewind paths return the match's own end because there
// is nothing else to return, not because it means anything. A mutant that
// returns -1 from both of them leaves the suite green.
//
// EVERY REWIND IS ON A HARMLESS KEY. A sensitive key's value is the credential
// under one reading of the line and the next pair's key under the other, and
// handing it back picks the second: the marker then lands on whatever that
// reading calls the value and the credential is copied across. Nothing is
// handed back from under a sensitive key any more.
//
// IT WALKS THE CHAIN RATHER THAN REWINDING ONCE PER KEY, and that is the whole
// cost story. Every key nested inside a value ends its own value at the byte
// this one does — a value is a run with no terminator in it, by construction —
// so keyPrefixPattern finds the next key without measuring the tail again.
// Rewinding into the value and re-running the FULL pattern re-measured that tail
// once per key instead: 64 KiB of "a=b=" cost 50 seconds inside one log call.
func resolvePair(s, key string, valueStart, valueEnd int) (start, end int, sensitive, rewind bool) {
	for {
		if sensitive = isSensitiveFieldName(key); sensitive {
			return valueStart, sensitiveValueEnd(s, valueStart, valueEnd), true, false
		}

		// A VALUE NEVER ENDS IN THE SEPARATOR.
		//
		// The value class admits '=' and stops at whitespace, so "opt =
		// password= hunter2" and "host=db =password= hunter2" both match a value
		// of "password=" — a field NAME and the separator that introduces the
		// credential. The credential itself sits after the space, OUTSIDE the
		// match, and is copied across verbatim: redacting that value scrubs the
		// name and prints the secret.
		if loc := prefixInsideValue(s, valueStart, valueEnd); loc != nil {
			// A pair nested inside this value, owning the rest of it: step onto
			// its key. Otherwise the value is a name and the separator, and the
			// value THAT introduces sits past the whitespace behind it, outside
			// this match — only the scanner matches across that, and
			// keyValueSeparator admits exactly the whitespace involved.
			if valueStart+loc[1] < valueEnd {
				key, valueStart = s[valueStart+loc[2]:valueStart+loc[3]], valueStart+loc[1]

				continue
			}

			return valueStart, valueEnd, false, true
		}

		// THE VALUE IS REALLY THE NEXT PAIR'S KEY. "tok =rg =0" reads as key
		// "tok", value "rg" — and the " =0" behind it is then orphaned, so a
		// sensitive name sitting in the value slot never gets its own value
		// redacted. Hand the value back and let it be matched as a key instead.
		//
		// The lookahead uses the pattern's OWN separator class. Any whitespace
		// [[:space:]] admits can sit between the value and the next '=', so a
		// form feed or a newline there is the same shape as a space.
		if nextPairSeparatorPattern.MatchString(s[valueEnd:]) {
			return valueStart, valueEnd, false, true
		}

		return valueStart, valueEnd, false, false
	}
}

// redactKeyValuePair redacts one key=value fragment when its key is sensitive,
// and otherwise LOOKS INSIDE THE VALUE FOR A PAIR THAT IS.
//
// The inner scan is the whole point. keyValuePattern's value class admits '=',
// so "opt = password=hunter2" is ONE pair keyed on "opt" — judged harmless, and
// the credential after it never became a candidate at all. Any word and an '='
// in front of the real field is enough to do it, which is an ordinary shape for
// an acquirer response ("POST /charge =cvc=999 rc=05"), a broker option list or
// a validator message to print. The same swallowing is why the query-parameter
// pass runs ahead of this one; this closes the rest of it.
//
// It terminates because the scan is a WALK rather than a recursion: each step
// moves valueStart forward by at least a key and a separator, and valueEnd never
// moves, so the two meet. It is idempotent because the marker it leaves is
// itself a value with no inner pair.
//
// THE RESULT IS SPLICED BY INDEX, AND EVERYTHING OUTSIDE THE PAIR IS CARRIED
// ACROSS VERBATIM, because re-finding the pattern inside the match it produced
// does not always land where the match began. \b is an ASCII word boundary while
// (?i) folds Unicode, so a key starting with a letter that folds to ASCII — ſ
// (U+017F) folds to s, K (U+212A) to k — matches in the full string, where the
// preceding ASCII digit supplies the boundary, and does NOT match at offset zero
// of the same text in isolation, where there is no preceding character at all.
// Rebuilding from the submatches then silently dropped the head: one pass over
// "00ſ00ſ00ſ=0" lost four bytes, and the next pass lost four more, so the output
// was not even stable. keyValuePattern is the only pass that can hit this, being
// the only one whose match can begin on a non-ASCII rune.
func redactKeyValuePair(match string) string {
	loc := keyValuePattern.FindStringSubmatchIndex(match)
	if loc == nil {
		return match
	}

	key, valueStart, valueEnd := match[loc[2]:loc[3]], loc[6], loc[7]

	// THE NESTED PAIRS ARE WALKED, NOT RECURSED, for the reason resolvePair
	// gives: the value's end never moves, so only the next key has to be found.
	// Recursing re-ran the full pattern over the whole remaining value at every
	// level, which turned 64 KiB of "a=" in front of one credential into three
	// minutes of CPU on whatever goroutine was writing the log line.
	//
	// At most one NON-OVERLAPPING match can start inside a value, because the
	// value class is greedy and every match it can hold therefore reaches the
	// same end. Nested keys are found by the prefix pattern instead, and the
	// walk is that chain: step over each key that is not a field name, and
	// redact the value of the first one that is.
	for !isSensitiveFieldName(key) {
		inner := keyPrefixPattern.FindStringSubmatchIndex(match[valueStart:valueEnd])
		if inner == nil || valueStart+inner[1] >= valueEnd {
			return match
		}

		key, valueStart = match[valueStart+inner[2]:valueStart+inner[3]], valueStart+inner[1]
	}

	return match[:valueStart] + SecretRedactionMarker + match[valueEnd:]
}

// redactQueryParameters redacts sensitive query parameters by field name, and
// EXTENDS A SENSITIVE VALUE ACROSS A SPACE-GROUPED DIGIT RUN.
//
// queryParameterPattern's value stops at whitespace, which is right for a
// credential and wrong for a card printed the way it is printed on the card:
// "?pan=4111 1111 1111 1111" was one pair with a value of "4111", so this pass
// scrubbed that one group and the twelve digits after it were already orphaned
// before the card pass ran. What reached the log was "?pan=**** 1111 1111 1111",
// a marker asserting the line had been scrubbed with an ordinary sixteen-digit
// card beside it, on any URL-shaped line.
//
// THE EXTENSION CANNOT LIVE IN THE PATTERN, and that is not for want of trying.
// A value that runs across spaces has to stop at a character the value class
// itself would not take, or the marker it leaves is absorbed on the next run and
// the pass is no longer idempotent — the fuzzer found both spellings of that in
// under fifteen seconds ("&cpf=0 <card> 0A" and "...0!"). RE2 has no lookahead,
// and CONSUMING the terminator eats the '&' that the following parameter needs
// as its own delimiter, so "&apikey=..." after a grouped card would stop being
// matched at all. Reading the next byte here costs a helper and is exact.
//
// Each extension step takes " " + digits, and only when that token is COMPLETE —
// followed by end of input or by a character the value class excludes. A token
// that continues into non-digits is left alone, which is what keeps "?page=2
// rejected" and "&cpf=0 <card> 0!" intact. The first value must itself be all
// digits, so nothing extends off the back of an ordinary credential.
func redactQueryParameters(s string) string {
	matches := queryParameterPattern.FindAllStringSubmatchIndex(s, -1)
	if matches == nil {
		return s
	}

	var out strings.Builder

	written := 0

	for _, m := range matches {
		// A previous match may have been extended past where this one starts.
		if m[0] < written {
			continue
		}

		name := s[m[4]:m[5]]
		if !isSensitiveFieldName(name) {
			continue
		}

		valueStart, valueEnd := m[6], m[7]

		out.WriteString(s[written:valueStart])
		out.WriteString(SecretRedactionMarker)

		written = extendOverGroupedDigits(s, valueStart, valueEnd)
	}

	if written == 0 {
		return s
	}

	out.WriteString(s[written:])

	return out.String()
}

// extendOverGroupedDigits returns the end of the value at [start, end), grown
// over any following space-separated groups of digits that are whole tokens.
func extendOverGroupedDigits(s string, start, end int) int {
	if !allDigits(s[start:end]) {
		return end
	}

	for end < len(s) && s[end] == ' ' {
		next := end + 1
		for next < len(s) && s[next] >= '0' && s[next] <= '9' {
			next++
		}

		// No digits after the space, or a token that carries on into something
		// else: either way this is not another group of the same run.
		if next == end+1 || (next < len(s) && isQueryValueByte(s[next])) {
			return end
		}

		end = next
	}

	return end
}

func allDigits(s string) bool {
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}

	return s != ""
}

// isQueryValueByte reports whether b is a byte queryParameterPattern's value
// class admits, and therefore a byte that would make the token before it
// something other than a bare group of digits.
//
// It reads queryValueTerminators, which is also what the pattern's class is
// built from, so the two cannot disagree. A test pins that for all 256 byte
// values.
func isQueryValueByte(b byte) bool {
	return strings.IndexByte(queryValueTerminators, b) < 0
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
		// THE SHORT-TAIL BRANCH CONSUMED THE OFFSET, SO THIS IS THE ONLY PLACE
		// THE SHORTER READING CAN STILL BE OFFERED.
		//
		// 4-4-4-N matches before the uniform branch at the same offset, so a
		// twelve-digit card followed by an unrelated one-to-three digit number —
		// an acquirer response code after the PAN, which is ordinary — was read
		// as one thirteen-to-fifteen digit number, failed Luhn, and was handed
		// back whole. The twelve-digit reading is card-length, so
		// redactCardInsideRun deliberately does not search it either, and the
		// card reached the log.
		//
		// One more Luhn check on the head, no window search: a 4-4-4 head is
		// exactly minCardDigits digits, so this admits precisely the shape the
		// uniform branch admitted before the tail was added, and the documented
		// false-positive posture is unchanged. The tail is kept because it is
		// not part of the card.
		if head := cardShortTailPattern.FindStringSubmatch(candidate); head != nil &&
			passesLuhn(cardSeparators.Replace(head[1])) {
			return SecretRedactionMarker + candidate[len(head[1]):]
		}

		return candidate
	}

	return SecretRedactionMarker
}

// cardShortTailPattern splits a 4-4-4-N candidate into its twelve-digit head and
// its tail. It is anchored because it is applied to a whole candidate that
// cardCandidatePatterns already matched, never used to search.
var cardShortTailPattern = regexp.MustCompile(`^(\d{4}[ .-]\d{4}[ .-]\d{4})[ .-]\d{1,3}$`)

// redactCardInsideRun redacts every card-length window of whole groups inside a
// run that is too long to be one card.
//
// IT IS DELIBERATELY NOT APPLIED to a run that is already card-length and merely
// fails Luhn. That run is an ordinary identifier, and hunting sub-windows inside
// one would redact roughly one identifier in ten — silently emptying the messages
// this package exists to keep diagnosable, which is the worse failure of the two.
//
// THE RUN IS GROUPED ONCE AND WALKED ONCE. It used to re-run the group regexp
// over the remainder, re-walk every width, and rebuild the string, once per card
// it found — so a line that is nothing but card numbers, which is what a batch
// import echoing its rejected rows looks like, cost a minute of CPU at
// MaxInputLen on whatever goroutine was writing the log line. Grouping once,
// deciding window length from prefix sums, and appending into one builder leaves
// exactly the same spans redacted for 283 ms — measured against the
// implementation it replaced over the committed corpus, every string literal in
// the test sources, and five thousand seeded random runs, with no difference.
func redactCardInsideRun(run string) string {
	groups := digitGroupPattern.FindAllStringIndex(run, -1)
	if len(groups) < 2 {
		return run
	}

	// digitsBefore[j] is how many digits the first j groups hold, so the digit
	// count of any window of whole groups is one subtraction. It is computed once
	// for the whole run, not once per card found.
	digitsBefore := make([]int, len(groups)+1)
	for j, g := range groups {
		digitsBefore[j+1] = digitsBefore[j] + g[1] - g[0]
	}

	var out strings.Builder

	written, first := 0, 0

	for first < len(groups) {
		start, end, next := findCardWindow(run, groups, digitsBefore, first)
		if next < 0 {
			break
		}

		// Only what follows the card is scanned again. The head is left to the
		// next round of the fixed-point loop in redactCardNumbers, which sees it
		// as a run of its own — and that asymmetry is deliberate, because a
		// card-length head that fails Luhn is an ordinary identifier that
		// redactCardCandidate leaves alone.
		out.WriteString(run[written:start])
		out.WriteString(SecretRedactionMarker)

		written, first = end, next
	}

	if written == 0 {
		return run
	}

	out.WriteString(run[written:])

	return out.String()
}

// findCardWindow returns the span of the widest card-length window of whole
// groups at or after group `from`, and the index of the first group past it.
// next is -1 when there is none.
//
// WIDEST WINDOW FIRST, ACROSS THE WHOLE REMAINDER, rather than longest-at-each-
// starting-point. Scanning per start position would let a shorter window that
// happens to satisfy Luhn win at an earlier offset and redact a span only partly
// overlapping the real card, leaving the rest of its digits in the clear.
// Preferring width means a sixteen-digit reading always beats a twelve-digit
// one, wherever each begins.
//
// THE WIDTH STARTS AT maxCardDigits, NOT AT THE NUMBER OF GROUPS. A window of w
// whole groups holds at least w digits, so every width above nineteen fails the
// length test below and was only ever costing a full pass over the run per
// discarded width.
//
// THE LENGTH TEST IS A SUBTRACTION AND LUHN RUNS ONLY BEHIND IT. The length used
// to be measured by stripping the window's separators and taking what was left,
// which allocated a string for every window at every width — and all but a
// handful of those windows were about to fail on length. Counting digits from the
// prefix sums decides the same windows without touching the run.
//
// The two readings differ on exactly one shape: a window holding a character
// that is neither a digit nor one of the three separators stripped here, which
// the old length counted and this one does not. Every such window fails Luhn,
// which rejects any byte outside '0'-'9', so the verdict is the same either way.
func findCardWindow(run string, groups [][]int, digitsBefore []int, from int) (int, int, int) {
	for width := min(len(groups)-from, maxCardDigits); width >= 2; width-- {
		for i := from; i+width <= len(groups); i++ {
			if count := digitsBefore[i+width] - digitsBefore[i]; count < minCardDigits || count > maxCardDigits {
				continue
			}

			start, end := groups[i][0], groups[i+width-1][1]

			if !passesLuhn(cardSeparators.Replace(run[start:end])) {
				continue
			}

			return start, end, i + width
		}
	}

	return 0, 0, -1
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
// CLASSIFICATION SURVIVES, AND THE RAW TEXT SURVIVES WITH IT — inside the chain,
// not in anything this value prints. errors.Is and errors.As still reach
// everything underneath, because a caller must still be able to classify the
// driver error it may no longer print. What is deliberately NOT provided is
// Unwrap: with it, errors.Unwrap(sanitized).Error() hands any caller the raw DSN
// straight back from the most obvious call there is.
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
// SO THE RULE IS: THIS REDACTS WHAT PRINTS, NOT WHAT THE CHAIN CONTAINS. The
// cause is still in there, and any errors.As target that reaches it hands back a
// value that prints the raw text — verified reachable, all of them:
//
//   - interface{ Unwrap() error } and interface{ Unwrap() []error }
//   - json.Marshaler and encoding.TextMarshaler
//   - fmt.GoStringer
//   - the concrete driver type (*pgconn.PgError and its kin)
//
// THESE ARE OPEN ON PURPOSE. Closing the interface ones means refusing every
// interface target, which takes legitimate classification down with it
// (interface{ SQLState() string }, Timeout(), Temporary()), and the concrete
// target is the whole reason the chain is kept intact. Closing them would also
// buy less than it looks: errors.As on the concrete driver type reaches the raw
// text regardless, so the interface doors are not the last lock on the door.
//
// The rule for callers is therefore behavioural, not enforced: ASK, CLASSIFY,
// AND PRINT ONLY THE WRAPPER. What this type guarantees is that every ordinary
// way of printing IT — Error, String, and Format for every verb including %#v —
// is redacted.
//
// Returns nil for a nil error — including a typed nil pointer inside a non-nil
// interface — so it composes at a return site without a guard.
func Error(err error) error {
	if err == nil {
		return nil
	}

	// A NIL VALUE INSIDE A NON-NIL INTERFACE is not caught above, and calling
	// Error() on it panics for any implementation that reads through it: a nil
	// pointer reading a field, a nil func calling itself, a nil map being
	// written. A panic here lands on an error path, on top of the failure being
	// reported, in a helper whose whole job is to be safe to call. The check is
	// the one the rest of lib-commons uses, over every nilable kind.
	if nilcheck.Interface(err) {
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

	// The authority ends at the first '/' or '?'; an '@' after that is part of a
	// path or query and is not userinfo.
	//
	// '#' DELIBERATELY DOES NOT END IT, THOUGH RFC 3986 SAYS IT WOULD. This pass
	// used to stop there too, and it disagreed with the key=value pass about
	// what that byte meant: "A://keY=#&@" has no '@' inside an authority that
	// stops at the '#', so this pass left the line alone, the key=value pass
	// then redacted "keY=#" to "keY=****" and DELETED the '#', and a second run
	// found the '@' and collapsed the userinfo. The sanitizer's output was not a
	// fixed point, which means anything that sanitizes twice rewrites the
	// evidence. A '#' before an '@' with no path or query between them is not a
	// real fragment boundary anyway: RFC 3986 does not permit '#' inside an
	// authority at all, so the text is already malformed and reading those bytes
	// as userinfo errs toward redaction.
	authorityEnd := len(rest)

	for i := range len(rest) {
		if c := rest[i]; c == '/' || c == '?' {
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
