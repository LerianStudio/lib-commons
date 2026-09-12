//go:build unit

package outbox

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// The defect this file pins: a row that exhausts its dispatch budget used to
// have last_error overwritten with "max dispatch attempts exceeded" — a
// tautology, since attempts and status already say the budget ran out. Worse,
// every earlier attempt had already overwritten the column with its own
// message, so the terminal row carried no diagnosis at all. AppendErrorCause is
// the rule that keeps the FIRST cause (the diagnosis) while still showing the
// degradation, under a hard byte cap.

func TestAppendErrorCauseKeepsTheFirstCause(t *testing.T) {
	got := AppendErrorCause("connection refused", "context deadline exceeded")

	require.Contains(t, got, "connection refused",
		"the first cause is the diagnosis and must survive every later attempt")
	require.Contains(t, got, "context deadline exceeded",
		"later distinct causes show the degradation")
	require.True(t, strings.HasPrefix(got, "connection refused"),
		"the first cause stays first so an operator reads the origin, not the symptom")
}

func TestAppendErrorCauseOnEmptyExistingReturnsTheCause(t *testing.T) {
	require.Equal(t, "broker unreachable", AppendErrorCause("", "broker unreachable"))
	require.Equal(t, "broker unreachable", AppendErrorCause("   ", "broker unreachable"))
}

func TestAppendErrorCauseDoesNotRepeatTheSameCause(t *testing.T) {
	// Ten identical timeouts must not fill the column with ten copies and
	// push the first cause out past the cap.
	got := "connection refused"
	for range 10 {
		got = AppendErrorCause(got, "connection refused")
	}

	require.Equal(t, "connection refused", got)
}

func TestAppendErrorCauseIsBoundedAndMarksTruncation(t *testing.T) {
	got := ""
	for i := range 200 {
		got = AppendErrorCause(got, strings.Repeat("x", 120)+string(rune('a'+i%26)))
	}

	require.LessOrEqual(t, utf8.RuneCountInString(got), MaxLastErrorLength,
		"the column must never grow without bound")
	require.Contains(t, got, LastErrorTruncationMarker,
		"dropping causes must be explicit, never silent")
	require.True(t, strings.HasPrefix(got, strings.Repeat("x", 120)),
		"truncation drops the NEWEST causes, never the first one")
}

func TestAppendErrorCauseStopsGrowingOnceTruncated(t *testing.T) {
	got := ""
	for i := range 200 {
		got = AppendErrorCause(got, strings.Repeat("y", 120)+string(rune('a'+i%26)))
	}

	saturated := got
	for i := range 50 {
		got = AppendErrorCause(got, strings.Repeat("z", 120)+string(rune('a'+i%26)))
	}

	require.Equal(t, saturated, got,
		"a saturated column is stable: the marker is appended once, not once per attempt")
}

func TestAppendErrorCauseIgnoresAnEmptyCause(t *testing.T) {
	// A backend that reports failure without a message must not erase the
	// diagnosis the row already earned.
	require.Equal(t, "connection refused", AppendErrorCause("connection refused", ""))
	require.Equal(t, "connection refused", AppendErrorCause("connection refused", "   "))
	require.Empty(t, AppendErrorCause("", ""))
}

func TestAppendErrorCauseHoldsTheCapForEveryCauseSize(t *testing.T) {
	// The cap must hold whatever the message length is, not only for sizes
	// that happen to land just under it.
	for size := 1; size <= 600; size++ {
		got := ""
		for i := range 40 {
			got = AppendErrorCause(got, strings.Repeat("x", size)+string(rune('a'+i%26)))
		}

		require.LessOrEqualf(t, utf8.RuneCountInString(got), MaxLastErrorLength,
			"cause size %d pushed the column past the cap", size)
	}
}

func TestAppendErrorCauseNeverSplitsAMultiByteRune(t *testing.T) {
	// A cause is bounded in RUNES upstream, so a message of 4-byte characters
	// can still exceed a BYTE budget. Cutting it mid-rune would store invalid
	// UTF-8 in the column.
	got := AppendErrorCause("", strings.Repeat("日", MaxLastErrorLength*2))

	require.LessOrEqual(t, utf8.RuneCountInString(got), MaxLastErrorLength)
	require.True(t, utf8.ValidString(got), "the stored value must remain valid UTF-8")
}

func TestAppendErrorCauseNeverEmitsTheExhaustionTautology(t *testing.T) {
	got := AppendErrorCause("handler not registered", "handler not registered")

	require.Equal(t, "handler not registered", got)
	require.NotContains(t, got, "max dispatch attempts exceeded",
		"exhaustion is carried by attempts and status, not by the column that holds the why")
}

func TestAppendErrorCauseFitsALegacyFullWidthValue(t *testing.T) {
	// A row written BEFORE this rule existed can hold a full-width value: the
	// old code wrote one sanitized message straight into the column. Appending
	// the marker to that blindly would breach VARCHAR(512) and strand the row
	// in PROCESSING for ever — the exact failure the cap exists to prevent.
	legacy := strings.Repeat("L", MaxLastErrorLength)
	require.Equal(t, MaxLastErrorLength, utf8.RuneCountInString(legacy))

	got := AppendErrorCause(legacy, "a brand new cause")

	require.LessOrEqual(t, utf8.RuneCountInString(got), MaxLastErrorLength,
		"a legacy full-width value must not overflow when the marker is appended")
	require.Contains(t, got, LastErrorTruncationMarker)
	require.True(t, strings.HasPrefix(got, "LLLL"), "the legacy diagnosis is still what leads")
}

func TestBoundErrorCauseMarksAnOversizedFirstCause(t *testing.T) {
	// A single cause too long to store must not read as if it were complete.
	got := BoundErrorCause(strings.Repeat("q", MaxLastErrorLength*2))

	require.LessOrEqual(t, utf8.RuneCountInString(got), MaxLastErrorLength)
	require.Contains(t, got, LastErrorTruncationMarker,
		"a first cause that was cut must say so, not read as the whole diagnosis")

	// A cause that fits is returned untouched, with no marker noise.
	require.Equal(t, "short cause", BoundErrorCause("short cause"))
}
