//go:build unit

package systemplanetest_test

import (
	"testing"

	"github.com/LerianStudio/lib-commons/v5/commons/systemplane"
	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/systemplanetest"
)

// This compile-only pin models the downstream service import set. It must not
// require commons/systemplane/internal/... to name the Run factory type.
var _ systemplanetest.ClientFactory = func(t *testing.T) *systemplane.Client {
	t.Helper()

	return nil
}

var _ systemplanetest.Factory = func(t *testing.T) *systemplane.Client {
	t.Helper()

	return nil
}

var _ = systemplanetest.RunClient
