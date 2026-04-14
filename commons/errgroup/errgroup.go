package errgroup

import (
	"context"
	"errors"
	"fmt"
	"sync"

	libLog "github.com/LerianStudio/lib-commons/v5/commons/log"
	"github.com/LerianStudio/lib-commons/v5/commons/runtime"
)

var (
	// ErrPanicRecovered is returned when a goroutine in the group panics.
	ErrPanicRecovered = errors.New("errgroup: panic recovered")

	// ErrNilGroup is returned when Go or Wait is called on a nil *Group.
	ErrNilGroup = errors.New("errgroup: nil group")
)

// Group manages a set of goroutines that share a cancellation context.
// The first error returned by any goroutine cancels the group's context
// and is returned by Wait. Subsequent errors are discarded.
type Group struct {
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	errOnce  sync.Once
	err      error
	loggerMu sync.RWMutex
	logger   libLog.Logger
}

// SetLogger sets an optional logger for panic recovery observability.
// When set, panics recovered in goroutines will be logged before the
// error is propagated via Wait. Safe for concurrent use.
func (grp *Group) SetLogger(logger libLog.Logger) {
	if grp == nil {
		return
	}

	grp.loggerMu.Lock()
	grp.logger = logger
	grp.loggerMu.Unlock()
}

// getLogger returns the current logger in a concurrency-safe manner.
func (grp *Group) getLogger() libLog.Logger {
	grp.loggerMu.RLock()
	l := grp.logger
	grp.loggerMu.RUnlock()

	return l
}

// effectiveCtx returns the group's context, falling back to context.Background()
// for zero-value Groups not created via WithContext.
func (grp *Group) effectiveCtx() context.Context {
	if grp.ctx != nil {
		return grp.ctx
	}

	return context.Background()
}

// WithContext returns a new Group and a derived context.Context.
// The derived context is canceled when the first goroutine in the Group
// returns a non-nil error or when Wait returns, whichever occurs first.
func WithContext(ctx context.Context) (*Group, context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	return &Group{ctx: ctx, cancel: cancel}, ctx
}

// Go starts a new goroutine in the Group. The first non-nil error returned
// by a goroutine is recorded and triggers cancellation of the group context.
// Callers must not mutate shared state without synchronization.
// If called on a nil *Group, Go is a no-op.
func (grp *Group) Go(fn func() error) {
	if grp == nil {
		return
	}

	grp.wg.Go(func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				runtime.HandlePanicValue(grp.effectiveCtx(), grp.getLogger(), recovered, "errgroup", "group.Go")

				grp.errOnce.Do(func() {
					grp.err = fmt.Errorf("%w: %v", ErrPanicRecovered, recovered)
					if grp.cancel != nil {
						grp.cancel()
					}
				})
			}
		}()

		if err := fn(); err != nil {
			grp.errOnce.Do(func() {
				grp.err = err
				if grp.cancel != nil {
					grp.cancel()
				}
			})
		}
	})
}

// Wait blocks until all goroutines in the Group have completed.
// It cancels the group context after all goroutines finish and returns
// the first non-nil error (if any) recorded by Go.
// Returns ErrNilGroup if called on a nil *Group.
func (grp *Group) Wait() error {
	if grp == nil {
		return ErrNilGroup
	}

	grp.wg.Wait()

	if grp.cancel != nil {
		grp.cancel()
	}

	return grp.err
}
