package commons

import (
	"sync"
	"testing"
)

var envTestMu sync.Mutex

// SetEnvironmentForTest sets the canonical environment for the duration of a single test.
// It calls t.Cleanup to automatically restore the previous state when the test ends.
// Tests that use this helper are serialized for the duration of the test to
// avoid cross-test interference through the shared package-global state.
//
// Example:
//
//	func TestMyFeature_Production(t *testing.T) {
//	    commons.SetEnvironmentForTest(t, commons.Production)
//	    // CurrentTier() now returns TierStrict
//	}
func SetEnvironmentForTest(t *testing.T, env Environment) {
	t.Helper()

	envTestMu.Lock()
	t.Cleanup(envTestMu.Unlock)

	// Save previous state.
	envMu.RLock()
	prevEnv := currentEnv
	prevSet := envSet
	envMu.RUnlock()

	// Apply the test environment.
	envMu.Lock()
	currentEnv = env
	envSet = true
	envMu.Unlock()

	// Restore on cleanup.
	t.Cleanup(func() {
		envMu.Lock()
		currentEnv = prevEnv
		envSet = prevSet
		envMu.Unlock()
	})
}

func resetEnvironment() {
	envMu.Lock()
	defer envMu.Unlock()

	currentEnv = ""
	envSet = false
}
