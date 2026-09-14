package outbox

import (
	"context"
	"sync"
	"testing"
)

// TestDispatcherStopDoesNotRaceRunRegistration is a regression test for a data
// race between Dispatcher.Stop and the run registration that RunContext performs
// on its way into the loop.
//
// Before the fix, the run state kept a sync.Once (stopOnce) that registerRun
// reassigned wholesale while holding runStateMu, whereas Stop reached the same
// word through stopOnce.Do with runStateMu taken only INSIDE the Do closure. A
// sync.Once's internal atomics synchronise Do against Do; they do not synchronise
// Do against an assignment of the Once value itself, so the two were unordered.
//
// The window is real for any caller that registers a shutdown hook before the
// dispatcher goroutine has entered its loop -- shutdown immediately after boot,
// and unit tests that start and drain back to back. Run this file with -race.
func TestDispatcherStopDoesNotRaceRunRegistration(t *testing.T) {
	t.Parallel()

	const rounds = 200

	for round := 0; round < rounds; round++ {
		dispatcher := &Dispatcher{}

		_, cancel := context.WithCancel(context.Background())

		var waitGroup sync.WaitGroup

		waitGroup.Add(2)

		// The loop's own startup: RunContext calls this before it selects.
		go func() {
			defer waitGroup.Done()

			dispatcher.registerRun(cancel)
		}()

		// A shutdown hook firing in the same instant.
		go func() {
			defer waitGroup.Done()

			dispatcher.Stop()
		}()

		waitGroup.Wait()
		cancel()
	}
}

// TestDispatcherStopIsIdempotentWithinARun pins the behaviour the sync.Once
// provided, so the fix is not free to drop it: Stop closes the run's stop channel
// exactly once, and a second call is a no-op rather than a close of a closed
// channel.
func TestDispatcherStopIsIdempotentWithinARun(t *testing.T) {
	t.Parallel()

	dispatcher := &Dispatcher{}

	_, cancel := context.WithCancel(context.Background())
	defer cancel()

	dispatcher.registerRun(cancel)

	dispatcher.Stop()
	dispatcher.Stop()
	dispatcher.Stop()

	select {
	case <-dispatcher.stop:
	default:
		t.Fatal("Stop did not close the run's stop channel")
	}
}

// TestDispatcherStopSignalResetsForTheNextRun pins the other half the reassignment
// existed for: a dispatcher that was stopped must be stoppable again after it is
// re-registered, otherwise the second run can never be shut down.
func TestDispatcherStopSignalResetsForTheNextRun(t *testing.T) {
	t.Parallel()

	dispatcher := &Dispatcher{}

	_, firstCancel := context.WithCancel(context.Background())
	defer firstCancel()

	dispatcher.registerRun(firstCancel)
	dispatcher.Stop()

	first := dispatcher.stop

	dispatcher.clearRun()

	_, secondCancel := context.WithCancel(context.Background())
	defer secondCancel()

	if !dispatcher.registerRun(secondCancel) {
		t.Fatal("the dispatcher refused to register a second run")
	}

	if dispatcher.stop == first {
		t.Fatal("registerRun reused the closed stop channel; the second run starts already stopped")
	}

	dispatcher.Stop()

	select {
	case <-dispatcher.stop:
	default:
		t.Fatal("the second run's stop channel was never closed; the stop signal did not reset")
	}
}
