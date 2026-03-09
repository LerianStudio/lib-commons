//go:build unit

package cron

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParse_DailyMidnight(t *testing.T) {
	t.Parallel()

	sched, err := Parse("0 0 * * *")
	require.NoError(t, err)

	from := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	next, err := sched.Next(from)

	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 1, 16, 0, 0, 0, 0, time.UTC), next)
}

func TestParse_EveryFiveMinutes(t *testing.T) {
	t.Parallel()

	sched, err := Parse("*/5 * * * *")
	require.NoError(t, err)

	from := time.Date(2026, 1, 15, 10, 3, 0, 0, time.UTC)
	next, err := sched.Next(from)

	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 1, 15, 10, 5, 0, 0, time.UTC), next)
}

func TestParse_DailySixThirty(t *testing.T) {
	t.Parallel()

	sched, err := Parse("30 6 * * *")
	require.NoError(t, err)

	from := time.Date(2026, 1, 15, 7, 0, 0, 0, time.UTC)
	next, err := sched.Next(from)

	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 1, 16, 6, 30, 0, 0, time.UTC), next)
}

func TestParse_DailyNoon(t *testing.T) {
	t.Parallel()

	sched, err := Parse("0 12 * * *")
	require.NoError(t, err)

	from := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	next, err := sched.Next(from)

	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC), next)
}

func TestParse_EveryMonday(t *testing.T) {
	t.Parallel()

	sched, err := Parse("0 0 * * 1")
	require.NoError(t, err)

	from := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	next, err := sched.Next(from)

	require.NoError(t, err)
	assert.Equal(t, time.Monday, next.Weekday())
	assert.Equal(t, 0, next.Hour())
	assert.Equal(t, 0, next.Minute())
	assert.True(t, next.After(from))
}

func TestParse_FifteenthOfMonth(t *testing.T) {
	t.Parallel()

	sched, err := Parse("0 0 15 * *")
	require.NoError(t, err)

	from := time.Date(2026, 1, 16, 0, 0, 0, 0, time.UTC)
	next, err := sched.Next(from)

	require.NoError(t, err)
	assert.Equal(t, 15, next.Day())
	assert.Equal(t, 0, next.Hour())
	assert.Equal(t, 0, next.Minute())
	assert.True(t, next.After(from))
}

func TestParse_Ranges(t *testing.T) {
	t.Parallel()

	sched, err := Parse("0 9-17 * * *")
	require.NoError(t, err)

	from := time.Date(2026, 1, 15, 18, 0, 0, 0, time.UTC)
	next, err := sched.Next(from)

	require.NoError(t, err)
	assert.Equal(t, 9, next.Hour())
	assert.Equal(t, time.Date(2026, 1, 16, 9, 0, 0, 0, time.UTC), next)
}

func TestParse_Lists(t *testing.T) {
	t.Parallel()

	sched, err := Parse("0 6,12,18 * * *")
	require.NoError(t, err)

	from := time.Date(2026, 1, 15, 7, 0, 0, 0, time.UTC)
	next, err := sched.Next(from)

	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC), next)
}

func TestParse_RangeWithStep(t *testing.T) {
	t.Parallel()

	sched, err := Parse("0 1-10/3 * * *")
	require.NoError(t, err)

	from := time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)
	next, err := sched.Next(from)

	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 1, 15, 1, 0, 0, 0, time.UTC), next)

	next, err = sched.Next(next)

	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 1, 15, 4, 0, 0, 0, time.UTC), next)
}

func TestParse_InvalidExpression(t *testing.T) {
	t.Parallel()

	_, err := Parse("not-a-cron")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidExpression)
}

func TestParse_EmptyString(t *testing.T) {
	t.Parallel()

	_, err := Parse("")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidExpression)
}

func TestParse_TooFewFields(t *testing.T) {
	t.Parallel()

	_, err := Parse("0 0 *")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidExpression)
}

func TestParse_TooManyFields(t *testing.T) {
	t.Parallel()

	_, err := Parse("0 0 * * * *")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidExpression)
}

func TestParse_OutOfRangeValue(t *testing.T) {
	t.Parallel()

	_, err := Parse("60 0 * * *")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidExpression)
}

func TestParse_InvalidStep(t *testing.T) {
	t.Parallel()

	_, err := Parse("*/0 * * * *")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidExpression)
}

func TestParse_WhitespaceHandling(t *testing.T) {
	t.Parallel()

	sched, err := Parse("  0 0 * * *  ")
	require.NoError(t, err)

	from := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	next, err := sched.Next(from)

	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 1, 16, 0, 0, 0, 0, time.UTC), next)
}

func TestNext_ExhaustionReturnsError(t *testing.T) {
	t.Parallel()

	// Schedule for Feb 30 — a date that never exists.
	// DOW is wildcard so day matching uses DOM alone; February never has day 30.
	// This forces the iterator to exhaust maxIterations without finding a match.
	sched := &schedule{
		minutes:   []int{0},
		hours:     []int{0},
		doms:      []int{30},
		months:    []int{2},
		dows:      []int{0, 1, 2, 3, 4, 5, 6},
		domIsWild: false,
		dowIsWild: true, // simulate "0 0 30 2 *"
	}

	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	next, err := sched.Next(from)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNoMatch)
	assert.True(t, next.IsZero(), "expected zero time on exhaustion")
}

func TestParse_DOW7NormalizedToSunday(t *testing.T) {
	t.Parallel()

	// DOW 7 should be accepted and treated as Sunday (0).
	sched, err := Parse("0 0 * * 7")
	require.NoError(t, err)

	// 2026-01-18 is a Sunday.
	from := time.Date(2026, 1, 17, 12, 0, 0, 0, time.UTC)
	next, err := sched.Next(from)

	require.NoError(t, err)
	assert.Equal(t, time.Sunday, next.Weekday())
	assert.Equal(t, time.Date(2026, 1, 18, 0, 0, 0, 0, time.UTC), next)
}

func TestParse_DOMAndDOWBothRestricted_ORSemantics(t *testing.T) {
	t.Parallel()

	// "0 0 15 * 1" = midnight on the 15th OR on any Monday.
	// Standard cron: when both DOM and DOW are restricted, match EITHER.
	sched, err := Parse("0 0 15 * 1")
	require.NoError(t, err)

	// 2026-01-15 is a Thursday. Should match because DOM=15.
	from := time.Date(2026, 1, 14, 23, 0, 0, 0, time.UTC)
	next, err := sched.Next(from)

	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC), next,
		"should match DOM=15 even though it's Thursday, not Monday (OR semantics)")
}

func TestParse_DOMAndDOWBothRestricted_MatchesDOW(t *testing.T) {
	t.Parallel()

	// "0 0 15 * 1" = midnight on the 15th OR on any Monday.
	sched, err := Parse("0 0 15 * 1")
	require.NoError(t, err)

	// 2026-01-19 is a Monday. Should match because DOW=1.
	from := time.Date(2026, 1, 18, 12, 0, 0, 0, time.UTC)
	next, err := sched.Next(from)

	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, 1, 19, 0, 0, 0, 0, time.UTC), next,
		"should match DOW=Monday even though DOM is not 15 (OR semantics)")
}

func TestParse_LeapDaySparseSchedule(t *testing.T) {
	t.Parallel()

	// "0 0 29 2 *" = Feb 29 only. Needs 4-year search window.
	sched, err := Parse("0 0 29 2 *")
	require.NoError(t, err)

	// Starting from 2025, the next Feb 29 is 2028-02-29.
	from := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)
	next, err := sched.Next(from)

	require.NoError(t, err)
	assert.Equal(t, time.Date(2028, 2, 29, 0, 0, 0, 0, time.UTC), next)
}

func TestNext_NilScheduleReturnsError(t *testing.T) {
	t.Parallel()

	var sched *schedule

	next, err := sched.Next(time.Now())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNilSchedule)
	assert.True(t, next.IsZero())
}
