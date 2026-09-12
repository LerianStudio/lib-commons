//go:build unit && race

package sanitize_test

import "time"

// stringTimingBound is raised under the race detector, which instruments every
// memory access and costs this package roughly a factor of ten. Holding the
// same wall-clock ceiling there would measure the detector rather than the card
// scan, and CI runs -race. The ratio to the measured cost is what matters and it
// is preserved: a regression of the kind these tests exist for is a factor of
// hundreds, not of two.
const stringTimingBound = 20 * time.Second
