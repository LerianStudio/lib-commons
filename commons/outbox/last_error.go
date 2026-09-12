package outbox

import (
	"slices"
	"strings"
	"unicode/utf8"
)

// MaxLastErrorLength is the hard ceiling, in CHARACTERS, on the accumulated
// last_error value.
//
// It is 512 because that is the width of the column this library ships:
// `last_error VARCHAR(512)` in commons/outbox/postgres/migrations. Postgres
// counts VARCHAR(n) in characters, not bytes, and REJECTS an oversized value
// outright (SQLSTATE 22001) rather than truncating it. A cap wider than the
// column would therefore not merely overflow — it would make MarkFailed return
// an error, leaving the row stuck in PROCESSING and retrying forever, which is
// worse than the overwrite this file exists to fix.
//
// So the ceiling is derived from the schema, never chosen independently of it.
// Widening the accumulation budget is a migration on every deployment that ran
// the shipped DDL, not a constant change here.
const MaxLastErrorLength = 512

// LastErrorTruncationMarker is appended, exactly once, when a further cause no
// longer fits under MaxLastErrorLength. Dropping diagnostic text is explicit:
// an operator reading a saturated value can tell "these are all the causes"
// from "there were more and they were discarded".
const LastErrorTruncationMarker = "\n... (later causes truncated)"

// lastErrorCauseSeparator separates accumulated causes. It is also the record
// delimiter the duplicate check matches on, which is why a cause never contains
// one: normalizeCause folds any newline inside a message to a space first.
const lastErrorCauseSeparator = "\n"

// StuckInProcessingCause is the cause contributed by the stuck-event reclaim
// when a row abandoned mid-PROCESSING has no attempts left. That path has no
// error of its own to record — nothing returned one — so it names the condition
// instead. It accumulates like any other cause rather than replacing the
// diagnosis a row already earned.
const StuckInProcessingCause = "abandoned in processing with no dispatch attempts left"

// LastErrorCauseBudget is the room available for causes, in characters, once
// the marker is reserved — so appending the marker can never push the value
// past the column width. The SQL backends pass it into their UPDATE so the
// statement bounds itself by the same number this package does.
func LastErrorCauseBudget() int {
	return MaxLastErrorLength - utf8.RuneCountInString(LastErrorTruncationMarker)
}

// AppendErrorCause accumulates dispatch failure causes into the value stored in
// last_error, and is the single definition of that rule.
//
// Why accumulate rather than overwrite: a row that dies after ten attempts is
// diagnosed by its FIRST cause far more often than its last. The first says the
// handler was never registered; the tenth says a timeout, which is what
// everything looks like once a row has been failing for an hour. Overwriting on
// every attempt destroyed the diagnosis, and the terminal write then replaced
// even that with "max dispatch attempts exceeded" — a tautology, because
// attempts and status already carry the fact that the budget ran out. That
// literal is gone: this value holds the why, the other two columns hold the
// what.
//
// The rules, in order:
//   - nothing stored yet: the cause becomes the value;
//   - this exact cause already recorded: unchanged, so ten identical timeouts
//     cannot evict the first cause from a bounded value;
//   - room left under the cap: appended on its own line;
//   - no room: LastErrorTruncationMarker appended once, stable from then on.
//
// Duplicate detection compares WHOLE causes, not substrings. A substring test
// would silently drop a distinct cause that happens to be contained in an
// earlier one — "timeout" after "request timeout while calling broker" — and
// dropping a distinct diagnosis is precisely the defect this file removes.
// Storing a near-duplicate is only noise; losing a cause is the bug.
//
// Truncation always drops the NEWEST causes. The first cause is the one worth
// keeping, so it is never the one evicted.
//
// The Postgres repository mirrors this rule in SQL rather than calling this
// function, because accumulating in Go would require a read-modify-write and
// lose the atomicity of a single UPDATE. The shared contract suite in
// outboxtest asserts the behaviour against every backend — including the
// truncation path — so a drift between the two expressions fails the contract
// rather than diverging silently.
func AppendErrorCause(existing, cause string) string {
	cause = normalizeCause(cause)
	if cause == "" {
		return existing
	}

	if strings.TrimSpace(existing) == "" {
		return BoundErrorCause(cause)
	}

	if hasCause(existing, cause) {
		return existing
	}

	if strings.Contains(existing, LastErrorTruncationMarker) {
		return existing
	}

	if utf8.RuneCountInString(existing)+1+utf8.RuneCountInString(cause) <= LastErrorCauseBudget() {
		return existing + lastErrorCauseSeparator + cause
	}

	return existing + LastErrorTruncationMarker
}

// BoundErrorCause trims ONE cause to the largest size that can be stored on its
// own without breaching MaxLastErrorLength.
//
// The SQL backends call this before handing the cause to their UPDATE, so the
// statement never has to truncate: it either stores a whole cause or appends
// the marker, and never has to slice text it cannot inspect for character
// boundaries.
func BoundErrorCause(cause string) string {
	return truncateToRuneBudget(normalizeCause(cause), LastErrorCauseBudget())
}

// hasCause reports whether cause is already recorded as a WHOLE entry. The
// Postgres mirror of this test is a delimiter-wrapped position() search, which
// is the same membership question asked in SQL.
func hasCause(existing, cause string) bool {
	return slices.Contains(strings.Split(existing, lastErrorCauseSeparator), cause)
}

// normalizeCause folds any newline inside a message to a space. Causes are
// separated by newlines, so a message carrying its own would break the record
// boundary and make a whole-cause comparison meaningless.
func normalizeCause(cause string) string {
	if strings.ContainsAny(cause, "\r\n") {
		cause = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(cause)
	}

	return strings.TrimSpace(cause)
}

// truncateToRuneBudget trims s to at most budget characters without splitting a
// multi-byte rune. The column counts characters, so this counts characters too.
func truncateToRuneBudget(s string, budget int) string {
	if utf8.RuneCountInString(s) <= budget {
		return s
	}

	count := 0
	for i := range s {
		if count == budget {
			return s[:i]
		}

		count++
	}

	return s
}
