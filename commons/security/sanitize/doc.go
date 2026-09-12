// Package sanitize provides shared redaction helpers used by logging,
// telemetry, and error-message handling across lib-commons.
//
// # What it is for
//
// Error strings are the most reliable credential leak in a service: a driver
// echoes the DSN it failed to dial, a broker client echoes its SASL config, an
// SDK echoes the signed URL it rejected. Those strings then travel into logs, a
// traced span, a database column, and sometimes a 500 body. String and Error are
// the one place that gets scrubbed, so the redaction rules live in one package
// instead of one per call site.
//
// String redacts a free-form string. Error wraps an error with a redacted
// message while KEEPING THE CHAIN INTACT, so errors.Is and errors.As still work
// on it — which is the whole point: a caller must still be able to classify a
// driver error it may no longer print.
//
// # This is a denylist, and it is the last line
//
// Everything here recognises SHAPES someone enumerated, so a shape nobody
// thought of passes through. It is the last line of defence, not a licence to
// let PII reach an error string in the first place; omission by design comes
// first, and this catches what slipped.
//
// # What decides sensitivity
//
// Field-name sensitivity is NOT decided here. It delegates to
// github.com/LerianStudio/lib-observability/v4/redaction, the canonical Lerian
// taxonomy, extended with an addendum for names that taxonomy does not carry:
// the AWS and SASL key names broker clients and SDKs echo, and the Brazilian
// document and bank-account names a ledger echoes — the shared list was
// enumerated in English, so it knows "ssn" and "account_number" and has never
// heard of "cpf" or "agencia". Structural stripping (URL userinfo, Authorization
// and Cookie headers, query parameters, JSON and key=value pairs) and bare-token
// value patterns are layered ON TOP of that field check.
//
// That taxonomy classifies PII (email, phone, address, iban, swift, ...) as
// sensitive in addition to secrets, so a PII field NAME is redacted BY DESIGN.
// For a tenant-isolated fintech service that is the intended consequence of
// adopting the shared taxonomy, not over-redaction.
//
// A field name only helps when there is one. Two kinds of PII show up in error
// strings BARE, with nothing around them to key on, and both are handled by
// value instead: an e-mail address, and a card number — 12 to 19 digits, grouped
// as printed on the card or unbroken, gated on Luhn so an order id or an
// epoch-millisecond timestamp of the same length stays readable. Nothing else is
// inferred from shape alone.
//
// # The order of the passes, which is load-bearing
//
// String applies, in this order: PEM blocks; URL userinfo, per URL; sensitive
// query parameters; the Azure SAS signature parameter; Authorization, Cookie and
// X-Api-Key header values; sensitive JSON "key":"value" pairs; card numbers;
// sensitive key=value pairs; and finally the bare vendor-token, JWT and e-mail
// patterns.
//
// Three of those positions are decisions rather than sequence. PEM goes FIRST
// because an armored block is often the value of a sensitive key, and letting
// key=value consume "-----BEGIN" as that value leaves the rule no marker to
// anchor on and the whole base64 body in the log. Query parameters go BEFORE
// key=value, whose value class admits '=' and would otherwise swallow a whole
// URL as one non-sensitive pair. Card numbers go BEFORE key=value too, whose
// value class stops at the first space and would otherwise redact one group of a
// grouped PAN and leave the remaining twelve digits behind a marker claiming the
// line was scrubbed. The query-parameter pass ends a value at the bytes in
// queryValueTerminators — space, tab, CR, LF, FF, '&', '#', both quotes, comma
// and semicolon — for the same reason and had the same failure, but it runs
// BEFORE the card pass and cannot be reordered without leaving the remainder of
// a sensitive non-card value in the clear, so it takes a grouped digit run whole
// instead. That list is deliberately spelled out rather than called
// "whitespace": RE2's \s does not contain a vertical tab, a hand-written copy of
// the class said it did, and a sanitizer that changed its own output on a second
// run was the result. The bare patterns go LAST so a vendor token is matched
// against the text as written rather than one an earlier pass has carved into.
//
// # The pipeline runs to a fixed point
//
// The nine passes above do not run once. String repeats them until a round
// changes nothing, capped at four rounds.
//
// This is not defensive tidying, it is the redaction. Four separate defects
// turned out to be one family: a later pass rewrites or deletes a byte an
// EARLIER pass used as a token boundary, so a second run parses a different
// string and finds a secret the first run missed. The URL pass ended an
// authority at '#', the key=value pass then redacted "keY=#" to "keY=****" and
// deleted that '#', and the second run found an '@' inside the authority and
// collapsed the userinfo; the same shape occurs with '/' and '?', which unlike
// '#' cannot simply be dropped from the terminator set. Separately, a
// credential-shaped token stole the key slot in front of "=Rg =0", so the RG
// was never seen until the bare-credential pass replaced the thief with a
// marker and the second run tokenised the pair correctly. In every one of them
// the second run's answer was the correct redaction and the first was a miss.
//
// TERMINATION IS THE CAP, NOT A SHRINKING ARGUMENT. A round can lengthen the
// string, so there is no measure to descend. The cap is what bounds it, and
// measurement is what makes the cap adequate: the worst input found so far
// needs three rounds, most need one or two, and nothing has needed four. An
// input that needed more would fail the fuzz harness, which asserts idempotence
// under the cap, rather than pass silently.
//
// THE COST IS LESS THAN DOUBLE, because the second round runs over text the
// first has already emptied. At MaxInputLen a line of nothing but card numbers
// costs 290 ms against 285 ms for a single pass, and a line with nothing to
// redact costs one round, since the first round changes nothing and the loop
// stops. The expensive work is scanning digits, and after the first round there
// are none left to scan.
//
// # What it does not cover
//
// The e-mail pattern is ASCII and requires a dotted TLD, so three shapes are
// known gaps and are deliberately not taken: an internationalised address with a
// unicode local part or domain, an IP-literal domain (user@[192.0.2.1]), and a
// TLD-less intranet address (user@localhost). Widening any of them costs more
// false positives in ordinary prose than the shapes are worth here; a service
// that handles them should not be relying on this package for that PII anyway.
//
// A CARD NUMBER GLUED TO OTHER ALPHANUMERICS IS NOT FOUND. Every card shape is
// anchored on a word boundary, so "41111111111111119999" — a PAN with an
// acquirer's four-digit code run onto the end of it — and "refA4111111111111111B"
// pass through in the clear. The boundary is what keeps the Luhn gate meaningful:
// without it every 12-to-19-digit window inside every longer identifier becomes a
// candidate, and roughly one arbitrary identifier in ten satisfies Luhn by
// chance, so the package would start silently emptying out the correlation ids
// and ledger ids it exists to keep readable. A PAN printed glued to a neighbour
// is a shape this package does not undertake to find; do not let one be written
// that way.
//
// A URL PASSWORD CONTAINING '/' OR '?' IS NOT REDACTED. The userinfo of
// "postgres://user:hun?ter2@db" is read as ending at the '?', so the authority
// is "user:hun", the at-sign belongs to the query, and no credential is found;
// the same happens with a '/' in the password. That reading is what RFC 3986
// says: a '?' or a '/' ends the authority, and a password carrying either has to
// percent-encode it to be a valid URL at all. Taking the LAST at-sign on the line
// instead would find these, and would also collapse the host out of every URL
// whose query carries an address or a pair — "http://h:8080/p?next=user@e.com"
// becomes "http://****@e.com", which loses the host and port an operator needs
// while the address it swallows is already redacted by the bare e-mail pass
// today, as "http://h:8080/p?next=****". A DSN whose password holds one of those bytes
// unencoded is a shape this package does not undertake to find; encode it, as the
// grammar already requires.
//
// A SENSITIVE VALUE THAT IS NOT A DIGIT RUN KEEPS ITS REMAINDER. The
// query-parameter pass ends the value at the first space and then grows it back
// over following groups only while those groups are bare digits, so
// "?cpf=1234 abcd efgh" is redacted to "?cpf=**** abcd efgh". The pass cannot
// tell a value that continues after a space from a value followed by prose, and
// growing over non-digits would swallow the sentence after every sensitive
// parameter. A non-digit secret written with spaces in it is not covered.
//
// A PARTIALLY PERCENT-ENCODED GROUPED CARD KEEPS TWELVE DIGITS.
// "?pan=4111%20 1111 1111 1111" leaves "?pan=**** 1111 1111 1111": the encoded
// first separator makes the value one token that is not a bare digit group, so
// the run is not grown over, and the remaining three groups are only twelve
// digits — below the card minimum — so the card pass does not take them either.
// A fully encoded value and a fully unencoded one are both handled; only the
// mixture falls between them.
//
// ON A SENSITIVE KEY, A VALUE THAT READS AS A FIELD NAME IS REDACTED TOGETHER
// WITH THE TOKEN THAT NAME WOULD INTRODUCE. "password=cpf= 12345678901" and
// "password=password =0" are each a credential under one reading and a pair
// boundary under the other, and both readings go under one marker rather than
// the package picking between them — so a weak password that happens to be a
// field word, and a document behind a name-shaped value, are both gone.
//
// The chain stops at the first covered token that is NOT followed by a
// separator, so "cpf=cpf =cpf 12345678901" prints the eleven digits: the third
// "cpf" is a bare word, nothing introduces anything behind it, and eleven digits
// are not a card. The extension also fires only for a SENSITIVE name, so
// "password=x= hunter2" prints hunter2 — extending through a bare word behind
// any "<x>=" would swallow the text behind an ordinary base64 value that merely
// ends in its padding.
//
// A whole pair behind the name is the exception, and only for one of the two
// spellings. When the value ENDS in the separator it is already a complete
// value, so "password=abc_token= rc=200" keeps its response code. When the
// separator sits OUTSIDE the value there is no second reading to weigh — the
// value is whatever follows — so "password=secret = rc=200" takes the response
// code under the marker as well. It takes exactly one pair, never two:
// "password=secret = host=db rc=200" keeps rc=200. One diagnostic pair is the
// price of not choosing between the two readings, and choosing is what leaked a
// credential in four consecutive passes.
//
// A VERTICAL TAB IS WHITESPACE, on both sides of the separator. It is in
// [[:space:]] and not in RE2's \s, and a value class written from the wrong one
// of those made a lone '\v' a value: "password=\v hunter2" put the marker over
// the whitespace byte and printed the credential behind it. The cost of the
// correction is that a pair whose entire value was one '\v' has no value at
// all, so "k=k=pwd=\v" comes back unchanged — there is no credential on that
// line to redact.
//
// A PEM BLOCK WITH NO CLOSING LINE IS OVER-REDACTED ON PURPOSE. A block that
// kept its -----END is consumed exactly to that line. One that lost it — a key
// pasted out of a kubectl output, a value a config loader cut — is consumed as
// far as base64-legal bytes and whitespace run, which carries on past the end of
// the armor and into any prose that happens to be made of letters and digits on
// the same lines. That text is lost from the log. The alternative was the block
// matching nothing at all and the whole armored body reaching the log behind a
// marker claiming the line had been scrubbed, so this is the cheaper error, and
// it only arises on a block that is already malformed.
//
// # Length is bounded, and the bound refuses rather than cuts
// Both armor lines are found in ONE pass and paired, rather than each BEGIN
// searching the rest of the input for an END: written the second way, armor with
// no END line at all cost 6.5 seconds at MaxInputLen against 0.02 for a
// well-formed block of the same size.
//
// Truncating BEFORE redaction is actively unsafe: a cut landing mid-secret
// strands a readable prefix ("postgres://user:pas") that no later pattern can
// recognise. So input above MaxInputLen is REFUSED, with a marker sentence
// naming its size, and never shortened. commons/outbox owns the length-bounded
// variant for the last_error column, which is a storage concern rather than a
// redaction one.
//
// The bound exists because the cost is real, AND THE COST IS DECIDED BY THE
// SHAPE OF THE INPUT RATHER THAN BY ITS LENGTH. The patterns are RE2 and each
// pass is linear; on unbroken digits — the one shape that never enters the card
// window scan — the whole of String costs roughly 400 milliseconds per megabyte
// on an ordinary devbox. A run of digit GROUPS is different: it becomes one
// over-long card candidate and is then searched a window of whole groups at a
// time, one walk of the remaining groups per card found. Measured at MaxInputLen
// on an ordinary devbox: 27 ms for unbroken digits, 42 ms for grouped
// four-digit ledger ids that are not cards, 20 ms for Amex grouping, and 283 ms
// for the worst shape there is — 64 KiB of back-to-back card numbers, where
// every window the scan tries is a real card.
package sanitize
