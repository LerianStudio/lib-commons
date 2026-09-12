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
// line was scrubbed. The bare patterns go LAST so a vendor token is matched
// against the text as written rather than one an earlier pass has carved into.
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
// A PEM BLOCK WITH NO CLOSING LINE IS OVER-REDACTED ON PURPOSE. A block that
// kept its -----END is consumed exactly to that line. One that lost it — a key
// pasted out of a kubectl output, a value a config loader cut — is consumed as
// far as base64-legal bytes and whitespace run, which carries on past the end of
// the armor and into any prose that happens to be made of letters and digits on
// the same lines. That text is lost from the log. The alternative was the block
// matching nothing at all and the whole armored body reaching the log behind a
// marker claiming the line had been scrubbed, so this is the cheaper error, and
// it only arises on a block that is already malformed.

// # Length is bounded, and the bound refuses rather than cuts
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
// time, which is quadratic in the number of groups. At MaxInputLen that is about
// 75 ms for 64 KiB of grouped four-digit ledger ids, and seconds for 64 KiB of
// back-to-back card numbers — see "# What it does not cover" for the residual.
package sanitize
