package outbox

import "strings"

// MaxLastErrorBytes is the hard ceiling on the accumulated last_error column.
// A row retries up to MaxDispatchAttempts times and each attempt may contribute
// a distinct cause, so the column accumulates; without a ceiling a pathological
// row could store MaxDispatchAttempts × maxErrorLength of text. Individual
// messages are already bounded to maxErrorLength by
// SanitizeErrorMessageForStorage, so this bounds the accumulation, not the
// message.
const MaxLastErrorBytes = 2048

// LastErrorTruncationMarker is appended, exactly once, when a further cause no
// longer fits under MaxLastErrorBytes. Dropping diagnostic text is explicit:
// an operator reading a saturated column can tell "these are all the causes"
// from "there were more and they were discarded".
const LastErrorTruncationMarker = "\n... (later causes truncated)"

// lastErrorCauseSeparator joins accumulated causes. A newline keeps each cause
// on its own line for an operator reading the column directly.
const lastErrorCauseSeparator = "\n"

// StuckInProcessingCause is the cause contributed by the stuck-event reclaim
// when a row abandoned mid-PROCESSING has no attempts left. That path has no
// error of its own to record — nothing returned one — so it names the condition
// instead. It accumulates like any other cause rather than replacing the
// diagnosis a row already earned.
const StuckInProcessingCause = "abandoned in processing with no dispatch attempts left"

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
// literal is gone: the column holds the why, the other two columns hold the
// what.
//
// The rules, in order:
//   - nothing stored yet: the cause becomes the value;
//   - a cause already present: the value is unchanged, so ten identical
//     timeouts cannot evict the first cause;
//   - room left under the cap: the cause is appended after a newline;
//   - no room: LastErrorTruncationMarker is appended once and the value is
//     stable from then on.
//
// Truncation always drops the NEWEST causes. The first cause is the one worth
// keeping, so it is never the one evicted.
//
// The Postgres repository mirrors this rule in SQL rather than calling this
// function, because accumulating in Go would require a read-modify-write and
// lose the atomicity of a single UPDATE. The shared contract suite in
// outboxtest asserts the behaviour against every backend, so a drift between
// the two expressions fails the contract rather than diverging silently.
func AppendErrorCause(existing, cause string) string {
	cause = strings.TrimSpace(cause)
	if cause == "" {
		return existing
	}

	// Room for the marker is reserved up front so that appending it can never
	// push the stored value past MaxLastErrorBytes. Every return below is
	// therefore bounded by the cap, not merely close to it.
	budget := MaxLastErrorBytes - len(LastErrorTruncationMarker)

	if strings.TrimSpace(existing) == "" {
		return BoundErrorCause(cause)
	}

	if strings.Contains(existing, cause) {
		return existing
	}

	if strings.Contains(existing, LastErrorTruncationMarker) {
		return existing
	}

	if len(existing)+len(lastErrorCauseSeparator)+len(cause) <= budget {
		return existing + lastErrorCauseSeparator + cause
	}

	return existing + LastErrorTruncationMarker
}

// BoundErrorCause trims ONE cause to the largest size that can be stored on its
// own without breaching MaxLastErrorBytes.
//
// The SQL backends call this before handing the cause to their UPDATE, so that
// the statement never has to truncate: it either stores a whole cause or
// appends the marker, and never has to slice text it cannot inspect for rune
// boundaries. A cause is bounded upstream in RUNES
// (SanitizeErrorMessageForStorage), so a message of multi-byte characters can
// still exceed a BYTE budget.
func BoundErrorCause(cause string) string {
	return truncateToByteBudget(
		strings.TrimSpace(cause),
		MaxLastErrorBytes-len(LastErrorTruncationMarker),
	)
}

// truncateToByteBudget trims s to at most budget bytes without splitting a
// multi-byte rune. A single sanitized cause is bounded in RUNES
// (maxErrorLength), so a cause of multi-byte characters can still exceed a byte
// budget; cutting it mid-rune would store invalid UTF-8 in the column.
func truncateToByteBudget(s string, budget int) string {
	if len(s) <= budget {
		return s
	}

	return s[:lastRuneBoundaryAtOrBefore(s, budget)]
}

// lastRuneBoundaryAtOrBefore returns the largest index <= budget that starts a
// rune, so slicing there never splits one.
func lastRuneBoundaryAtOrBefore(s string, budget int) int {
	boundary := 0

	for i := range s {
		if i > budget {
			break
		}

		boundary = i
	}

	return boundary
}
