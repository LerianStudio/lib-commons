// Package systemplanetest provides backend-agnostic contract tests for the
// internal store.Store interface. Both the Postgres and MongoDB backends
// run this shared suite in their integration tests to ensure behavioral
// equivalence.
package systemplanetest

import (
	"testing"

	"github.com/LerianStudio/lib-commons/v4/commons/systemplane/internal/store"
)

// Factory constructs a fresh Store for each test. The caller is responsible
// for any cleanup (e.g., dropping tables, closing connections).
type Factory func(t *testing.T) store.Store

// Run executes the full contract test suite against a Store implementation.
//
// TODO(phase-4): implement contract test cases (CRUD, subscribe, concurrent writes)
func Run(t *testing.T, factory Factory) {
	t.Helper()

	_ = factory
}
