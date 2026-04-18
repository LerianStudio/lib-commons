package streaming

import (
	"context"

	libCommons "github.com/LerianStudio/lib-commons/v5/commons"
	"github.com/LerianStudio/lib-commons/v5/commons/log"
)

// Compile-time assertion: *Producer must satisfy commons.App. A missing
// method fails the build here rather than at a distant call site in the
// consuming service's main.go.
//
// The App contract is a single method — Run(*Launcher) error — so this
// assertion pins the full surface at once.
var _ libCommons.App = (*Producer)(nil)

// Run registers the Producer with the Launcher lifecycle. It blocks until
// Close (or CloseContext) is called, then returns nil.
//
// Run delegates to RunContext with a background context, mirroring the
// Close/CloseContext pairing across the package. Consuming services
// typically call launcher.Add("streaming", producer); the Launcher then
// invokes Run from its goroutine pool and waits for it to return before
// Launcher.RunWithError completes.
//
// Nil-receiver safe: returns ErrNilProducer so a mis-wired bootstrap
// surfaces as a typed error rather than a crash.
func (p *Producer) Run(launcher *libCommons.Launcher) error {
	return p.RunContext(context.Background(), launcher)
}

// RunContext is the context-aware variant. It blocks until any of:
//   - ctx is canceled (typical: signal.NotifyContext with SIGTERM),
//   - the internal stop channel is closed (via Close/CloseContext from
//     another goroutine),
//
// then invokes CloseContext(ctx) so the Producer flushes and releases its
// broker connections before Run returns.
//
// RunContext is SAFE TO CALL MULTIPLE TIMES — the idempotence guard lives
// in CloseContext. The second caller just blocks on stop, then sees a
// no-op CloseContext return.
//
// Parameters:
//   - ctx: Shutdown signal. When ctx is nil RunContext substitutes
//     context.Background() so the lifecycle ladder still works, matching
//     rabbitmq/rabbitmq.go's nil-context tolerance. A nil ctx in a
//     production bootstrap is a code smell but not a crash vector.
//   - launcher: Optional. RunContext accepts nil so ad-hoc test usage
//     (where the caller wants Run's blocking behavior without a full
//     Launcher) works. When non-nil and its Logger is set, RunContext
//     emits one INFO log at start and one at stop — mirroring the
//     outbox.Dispatcher convention.
//
// Nil-receiver safe: returns ErrNilProducer.
func (p *Producer) RunContext(ctx context.Context, launcher *libCommons.Launcher) error {
	if p == nil {
		return ErrNilProducer
	}

	if ctx == nil {
		ctx = context.Background()
	}

	// Already-closed Producer: Run returns immediately with nil. The
	// Launcher will note the app "finished" and move on. This is the
	// correct behavior for the rare case where a service calls Close
	// before Run (test fixtures sometimes do this).
	if p.closed.Load() {
		return nil
	}

	logStart(ctx, launcher, "streaming producer started")

	select {
	case <-ctx.Done():
		// Upstream shutdown signal — propagate via CloseContext.
	case <-p.stop:
		// Another goroutine (typically Close from signal handler)
		// triggered shutdown. CloseContext below is a no-op under
		// the CAS guard, but we still call it to keep the exit path
		// uniform.
	}

	// Use a fresh context for Close so a canceled caller ctx does not
	// abort the Flush before it starts. The Producer's closeTimeout caps
	// the wait; a misbehaving broker still cannot deadlock shutdown.
	// Without this detachment, ctx-cancel-driven shutdowns would always
	// surface "flush on close: context canceled" — true but unhelpful
	// noise that every graceful shutdown would emit (DX-E04/E05).
	closeCtx := context.Background()
	err := p.CloseContext(closeCtx)

	logStop(ctx, launcher, "streaming producer stopped")

	return err
}

// logStart is a tiny helper that keeps the launcher-logger branches out of
// RunContext's hot path. Nil-safe for both launcher and launcher.Logger.
func logStart(ctx context.Context, launcher *libCommons.Launcher, msg string) {
	if launcher == nil || launcher.Logger == nil {
		return
	}

	launcher.Logger.Log(ctx, log.LevelInfo, msg)
}

// logStop mirrors logStart for the stop-side log.
func logStop(ctx context.Context, launcher *libCommons.Launcher, msg string) {
	if launcher == nil || launcher.Logger == nil {
		return
	}

	launcher.Logger.Log(ctx, log.LevelInfo, msg)
}
