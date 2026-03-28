// Copyright 2025 Lerian Studio.

package service

import (
	"context"
	"errors"
	"fmt"
)

// ShutdownSequence performs the canonical 5-step systemplane shutdown.
// All steps execute regardless of errors. Nil fields are skipped.
type ShutdownSequence struct {
	// PreventMutations stops accepting config writes (e.g., configManager.Stop()).
	PreventMutations func()
	// CancelFeed cancels the change feed subscription context.
	CancelFeed context.CancelFunc
	// StopSupervisor stops the supervisor and closes the active bundle.
	StopSupervisor func(context.Context) error
	// CloseBackend closes the systemplane store connection.
	CloseBackend func() error
	// StopWorkers stops background workers.
	StopWorkers func()
}

// Execute runs all shutdown steps in canonical order.
// Returns combined errors via errors.Join. All steps run even if earlier ones fail or panic.
func (s *ShutdownSequence) Execute(ctx context.Context) error {
	if s == nil {
		return nil
	}

	if ctx == nil {
		ctx = context.Background()
	}

	var errs []error

	appendPanic := func(step string) {
		if recovered := recover(); recovered != nil {
			errs = append(errs, fmt.Errorf("%s panic: %v", step, recovered))
		}
	}
	appendError := func(step string, err error) {
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", step, err))
		}
	}

	// Step 1: Prevent mutations (fire-and-forget).
	if s.PreventMutations != nil {
		func() {
			defer appendPanic("prevent mutations")

			s.PreventMutations()
		}()
	}

	// Step 2: Cancel feed (fire-and-forget).
	if s.CancelFeed != nil {
		func() {
			defer appendPanic("cancel feed")

			s.CancelFeed()
		}()
	}

	// Step 3: Stop supervisor (collects error).
	if s.StopSupervisor != nil {
		func() {
			defer appendPanic("stop supervisor")

			appendError("stop supervisor", s.StopSupervisor(ctx))
		}()
	}

	// Step 4: Close backend (collects error).
	if s.CloseBackend != nil {
		func() {
			defer appendPanic("close backend")

			appendError("close backend", s.CloseBackend())
		}()
	}

	// Step 5: Stop workers (fire-and-forget).
	if s.StopWorkers != nil {
		func() {
			defer appendPanic("stop workers")

			s.StopWorkers()
		}()
	}

	return errors.Join(errs...)
}
