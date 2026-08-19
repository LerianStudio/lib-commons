//go:build unit

package server

import (
	"testing"

	"github.com/gofiber/fiber/v3"
)

// TestExecuteShutdown_BeforeLaunch_RefusesLaterFiberLaunch pins the ordering
// guarantee between shutdown and server startup: once executeShutdown has run,
// a fiber launch that lost the race must be refused instead of starting a
// Listen goroutine that would keep serving after graceful shutdown completed.
func TestExecuteShutdown_BeforeLaunch_RefusesLaterFiberLaunch(t *testing.T) {
	t.Parallel()

	sm := NewServerManager(nil, nil, nil).WithHTTPServer(fiber.New(), ":0")

	// Shutdown wins the race: it runs before any launch published its
	// lifecycle signal.
	sm.executeShutdown()

	if launched := sm.launchFiberHTTPServer(); launched {
		t.Fatal("launchFiberHTTPServer must refuse to launch after shutdown was initiated")
	}

	sm.lifecycleMu.Lock()
	defer sm.lifecycleMu.Unlock()

	if sm.fiberListenDone != nil {
		t.Fatal("no listen lifecycle signal must be published for a refused launch")
	}
}

// TestExecuteShutdown_AfterLaunch_WaitsForListenExit pins the normal ordering:
// a launch that published its lifecycle signal before shutdown must be waited
// on, so the Listen goroutine cannot outlive executeShutdown.
func TestExecuteShutdown_AfterLaunch_WaitsForListenExit(t *testing.T) {
	t.Parallel()

	sm := NewServerManager(nil, nil, nil).WithHTTPServer(fiber.New(), ":0")

	if launched := sm.launchFiberHTTPServer(); !launched {
		t.Fatal("launchFiberHTTPServer must launch before shutdown is initiated")
	}

	sm.executeShutdown()

	select {
	case <-sm.fiberListenDone:
		// Listen goroutine exited before executeShutdown returned.
	default:
		t.Fatal("executeShutdown must not return while the fiber Listen goroutine is still running")
	}
}
