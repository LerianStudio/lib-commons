package cron

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ErrInvalidExpression is returned when a cron expression cannot be parsed
// due to incorrect field count, out-of-range values, or malformed syntax.
var ErrInvalidExpression = errors.New("invalid cron expression")

// ErrNoMatch is returned when Next exhausts its iteration limit without
// finding a time that satisfies all cron fields.
var ErrNoMatch = errors.New("cron: no matching time found within iteration limit")

// ErrNilSchedule is returned when Next is called on a nil schedule receiver.
var ErrNilSchedule = errors.New("cron schedule is nil")

// Cron field boundary constants.
const (
	cronFieldCount = 5  // number of fields in a standard cron expression
	maxMinute      = 59 // maximum value for minute field
	maxHour        = 23 // maximum value for hour field
	minDayOfMonth  = 1  // minimum value for day-of-month field
	maxDayOfMonth  = 31 // maximum value for day-of-month field
	minMonth       = 1  // minimum value for month field
	maxMonth       = 12 // maximum value for month field
	maxDayOfWeek   = 7  // maximum accepted day-of-week value (7 is normalized to 0 = Sunday)
	splitParts     = 2  // number of parts when splitting step or range expressions
)

// Schedule represents a parsed cron schedule capable of computing
// the next execution time after a given reference time.
type Schedule interface {
	Next(t time.Time) (time.Time, error)
}

type schedule struct {
	minutes   []int
	hours     []int
	doms      []int
	months    []int
	dows      []int
	domIsWild bool // true when the day-of-month field was "*" (unrestricted)
	dowIsWild bool // true when the day-of-week field was "*" (unrestricted)
}

// Parse parses a standard 5-field cron expression and returns a Schedule
// that can compute the next execution time. The expression format is:
// minute hour day-of-month month day-of-week
// Returns ErrInvalidExpression if the expression is malformed or contains out-of-range values.
func Parse(expr string) (Schedule, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, fmt.Errorf("%w: empty expression", ErrInvalidExpression)
	}

	fields := strings.Fields(expr)
	if len(fields) != cronFieldCount {
		return nil, fmt.Errorf("%w: expected %d fields, got %d", ErrInvalidExpression, cronFieldCount, len(fields))
	}

	minutes, err := parseField(fields[0], 0, maxMinute)
	if err != nil {
		return nil, fmt.Errorf("invalid minute field: %w", err)
	}

	hours, err := parseField(fields[1], 0, maxHour)
	if err != nil {
		return nil, fmt.Errorf("invalid hour field: %w", err)
	}

	domIsWild := isWildcard(fields[2])

	doms, err := parseField(fields[2], minDayOfMonth, maxDayOfMonth)
	if err != nil {
		return nil, fmt.Errorf("invalid day-of-month field: %w", err)
	}

	months, err := parseField(fields[3], minMonth, maxMonth)
	if err != nil {
		return nil, fmt.Errorf("invalid month field: %w", err)
	}

	dowIsWild := isWildcard(fields[4])

	dows, err := parseField(fields[4], 0, maxDayOfWeek)
	if err != nil {
		return nil, fmt.Errorf("invalid day-of-week field: %w", err)
	}

	// Normalize DOW 7 → 0 (both mean Sunday) per widespread cron convention.
	dows = normalizeDOW(dows)

	return &schedule{
		minutes:   minutes,
		hours:     hours,
		doms:      doms,
		months:    months,
		dows:      dows,
		domIsWild: domIsWild,
		dowIsWild: dowIsWild,
	}, nil
}

// Next computes the next execution time after the given reference time.
// It normalizes the input to UTC, advances by one minute, and iteratively
// checks each cron field (month, day-of-month, day-of-week, hour, minute)
// to find the next matching time. Returns the matching time in UTC, or
// ErrNoMatch if no match is found within maxIterations.
//
// DOM/DOW semantics follow the standard cron convention: when BOTH fields are
// restricted (not wildcards), the day matches if EITHER condition is true (OR).
// When only one is restricted, that field alone determines the match.
func (sched *schedule) Next(from time.Time) (time.Time, error) {
	if sched == nil {
		return time.Time{}, ErrNilSchedule
	}

	from = from.UTC()
	candidate := from.Add(time.Minute)
	candidate = time.Date(candidate.Year(), candidate.Month(), candidate.Day(), candidate.Hour(), candidate.Minute(), 0, 0, time.UTC)

	// 4 years (1461 days) to accommodate leap-day and other sparse schedules.
	const maxIterations = 1461 * 24 * 60
	for range maxIterations {
		if !slices.Contains(sched.months, int(candidate.Month())) {
			candidate = time.Date(candidate.Year(), candidate.Month()+1, 1, 0, 0, 0, 0, time.UTC)

			continue
		}

		if !sched.matchDay(candidate) {
			candidate = candidate.AddDate(0, 0, 1)
			candidate = time.Date(candidate.Year(), candidate.Month(), candidate.Day(), 0, 0, 0, 0, time.UTC)

			continue
		}

		if !slices.Contains(sched.hours, candidate.Hour()) {
			candidate = candidate.Add(time.Hour)
			candidate = time.Date(candidate.Year(), candidate.Month(), candidate.Day(), candidate.Hour(), 0, 0, 0, time.UTC)

			continue
		}

		if !slices.Contains(sched.minutes, candidate.Minute()) {
			candidate = candidate.Add(time.Minute)

			continue
		}

		return candidate, nil
	}

	return time.Time{}, ErrNoMatch
}

// matchDay implements standard cron DOM/DOW semantics:
//   - Both wildcards: any day matches.
//   - Only DOM restricted: match on DOM alone.
//   - Only DOW restricted: match on DOW alone.
//   - Both restricted: match if EITHER DOM or DOW is satisfied (OR semantics).
func (sched *schedule) matchDay(t time.Time) bool {
	domMatch := slices.Contains(sched.doms, t.Day())
	dowMatch := slices.Contains(sched.dows, int(t.Weekday()))

	switch {
	case sched.domIsWild && sched.dowIsWild:
		return true
	case sched.domIsWild:
		return dowMatch
	case sched.dowIsWild:
		return domMatch
	default:
		// Both restricted: standard cron OR semantics.
		return domMatch || dowMatch
	}
}

func parseField(field string, minVal, maxVal int) ([]int, error) {
	var result []int

	for part := range strings.SplitSeq(field, ",") {
		vals, err := parsePart(part, minVal, maxVal)
		if err != nil {
			return nil, err
		}

		result = append(result, vals...)
	}

	return deduplicate(result), nil
}

func parsePart(part string, minVal, maxVal int) ([]int, error) {
	var rangeStart, rangeEnd, step int

	stepParts := strings.SplitN(part, "/", splitParts)
	hasStep := len(stepParts) == splitParts

	if hasStep {
		s, err := parseStep(stepParts[1])
		if err != nil {
			return nil, err
		}

		step = s
	}

	rangePart := stepParts[0]

	switch {
	case rangePart == "*":
		rangeStart = minVal
		rangeEnd = maxVal
	case strings.Contains(rangePart, "-"):
		lo, hi, err := parseRange(rangePart, minVal, maxVal)
		if err != nil {
			return nil, err
		}

		rangeStart = lo
		rangeEnd = hi
	default:
		val, err := strconv.Atoi(rangePart)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid value %q", ErrInvalidExpression, rangePart)
		}

		if val < minVal || val > maxVal {
			return nil, fmt.Errorf("%w: value %d out of bounds [%d, %d]", ErrInvalidExpression, val, minVal, maxVal)
		}

		if hasStep {
			rangeStart = val
			rangeEnd = maxVal
		} else {
			return []int{val}, nil
		}
	}

	if !hasStep {
		step = 1
	}

	var vals []int
	for v := rangeStart; v <= rangeEnd; v += step {
		vals = append(vals, v)
	}

	return vals, nil
}

// parseStep parses and validates a cron step value, ensuring it is a positive integer.
func parseStep(raw string) (int, error) {
	s, err := strconv.Atoi(raw)
	if err != nil || s <= 0 {
		return 0, fmt.Errorf("%w: invalid step %q", ErrInvalidExpression, raw)
	}

	return s, nil
}

// parseRange parses a "lo-hi" range expression, validates bounds against
// [minVal, maxVal], and returns the low and high values.
func parseRange(rangePart string, minVal, maxVal int) (int, int, error) {
	bounds := strings.SplitN(rangePart, "-", splitParts)

	lo, err := strconv.Atoi(bounds[0])
	if err != nil {
		return 0, 0, fmt.Errorf("%w: invalid range start %q", ErrInvalidExpression, bounds[0])
	}

	hi, err := strconv.Atoi(bounds[1])
	if err != nil {
		return 0, 0, fmt.Errorf("%w: invalid range end %q", ErrInvalidExpression, bounds[1])
	}

	if lo < minVal || hi > maxVal || lo > hi {
		return 0, 0, fmt.Errorf("%w: range %d-%d out of bounds [%d, %d]", ErrInvalidExpression, lo, hi, minVal, maxVal)
	}

	return lo, hi, nil
}

func deduplicate(vals []int) []int {
	seen := make(map[int]bool, len(vals))
	result := make([]int, 0, len(vals))

	for _, v := range vals {
		if !seen[v] {
			seen[v] = true
			result = append(result, v)
		}
	}

	slices.Sort(result)

	return result
}

// isWildcard reports whether a cron field token is an unrestricted wildcard.
// Only bare "*" counts; "*/5" is a step expression and is considered restricted.
func isWildcard(field string) bool {
	return field == "*"
}

// normalizeDOW rewrites day-of-week value 7 to 0 (both represent Sunday)
// and deduplicates the result, matching widespread cron convention.
func normalizeDOW(dows []int) []int {
	for i, v := range dows {
		if v == 7 {
			dows[i] = 0
		}
	}

	return deduplicate(dows)
}
