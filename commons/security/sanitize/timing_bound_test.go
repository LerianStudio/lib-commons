//go:build unit && !race

package sanitize_test

import "time"

// stringTimingBound is the ceiling the runtime regression tests hold String to
// at MaxInputLen. Two orders of magnitude above the measured cost of the worst
// shape, because what these tests catch is the shape of the curve coming back,
// not a few milliseconds of drift.
const stringTimingBound = 2 * time.Second
