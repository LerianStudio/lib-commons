//go:build unit

package streaming

import "github.com/google/uuid"

// DeriveAggregateIDForTest is a test-only accessor for the unexported
// deriveAggregateID helper. Same unexported-method-exposed-via-test-file
// pattern used by cb_listener_export_test.go. Keeping the export on a
// build-tagged file ensures the main build carries no dead code.
func DeriveAggregateIDForTest(event Event) uuid.UUID {
	return deriveAggregateID(event)
}
