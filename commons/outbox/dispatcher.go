package outbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/LerianStudio/lib-commons/v6/commons/internal/nilcheck"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	libCommons "github.com/LerianStudio/lib-commons/v6/commons"
	"github.com/LerianStudio/lib-commons/v6/commons/backoff"
	observability "github.com/LerianStudio/lib-observability/v2"
	libLog "github.com/LerianStudio/lib-observability/v2/log"
	"github.com/LerianStudio/lib-observability/v2/runtime"
	libOpentelemetry "github.com/LerianStudio/lib-observability/v2/tracing"
)

const overflowTenantMetricLabel = "_other"

type tenantRequirementReporter interface {
	RequiresTenant() bool
}

// Dispatcher handles publishing outbox events through registered handlers.
type Dispatcher struct {
	repo            OutboxRepository
	handlers        *HandlerRegistry
	retryClassifier RetryClassifier
	logger          libLog.Logger
	tracer          trace.Tracer
	cfg             DispatcherConfig

	listPendingFailureCounts map[string]int
	failureCountsMu          sync.Mutex
	tenantMetricKeys         map[string]struct{}
	tenantMetricMu           sync.Mutex

	stop       chan struct{}
	stopOnce   sync.Once
	runStateMu sync.Mutex
	running    bool
	cancelFunc context.CancelFunc
	dispatchWg sync.WaitGroup
	tenantTurn int

	metrics dispatcherMetrics
}

var _ libCommons.App = (*Dispatcher)(nil)

// DispatchResult captures one dispatch cycle outcome.
type DispatchResult struct {
	Processed         int
	Published         int
	Failed            int
	StateUpdateFailed int
}

// NewDispatcher creates a generic outbox dispatcher.
func NewDispatcher(
	repo OutboxRepository,
	handlers *HandlerRegistry,
	logger libLog.Logger,
	tracer trace.Tracer,
	opts ...DispatcherOption,
) (*Dispatcher, error) {
	if nilcheck.Interface(repo) {
		return nil, ErrOutboxRepositoryRequired
	}

	if handlers == nil {
		return nil, ErrHandlerRegistryRequired
	}

	if nilcheck.Interface(tracer) {
		tracer = noop.NewTracerProvider().Tracer("commons.noop")
	}

	if nilcheck.Interface(logger) {
		logger = libLog.NewNop()
	}

	dispatcher := &Dispatcher{
		repo:                     repo,
		handlers:                 handlers,
		logger:                   logger,
		tracer:                   tracer,
		cfg:                      DefaultDispatcherConfig(),
		listPendingFailureCounts: make(map[string]int),
		tenantMetricKeys:         make(map[string]struct{}),
		stop:                     make(chan struct{}),
	}

	for _, opt := range opts {
		if opt != nil {
			opt(dispatcher)
		}
	}

	dispatcher.cfg.normalize()
	dispatcher.ensureFailureCounterFallback()

	if dispatcher.cfg.IncludeTenantMetrics {
		dispatcher.logger.Log(
			context.Background(),
			libLog.LevelWarn,
			fmt.Sprintf(
				"outbox tenant metric attributes enabled; cardinality capped at %d with overflow label %q",
				dispatcher.cfg.MaxTenantMetricDimensions,
				overflowTenantMetricLabel,
			),
		)
	}

	metrics, err := newDispatcherMetrics(dispatcher.cfg.MeterProvider, dispatcher.logger)
	if err != nil {
		return nil, fmt.Errorf("init outbox metrics: %w", err)
	}

	dispatcher.metrics = metrics

	return dispatcher, nil
}

// Run starts the dispatcher loop until Stop is called.
func (dispatcher *Dispatcher) Run(launcher *libCommons.Launcher) error {
	return dispatcher.RunContext(context.Background(), launcher)
}

// RunContext starts the dispatcher loop until Stop is called or ctx is cancelled.
func (dispatcher *Dispatcher) RunContext(parentCtx context.Context, launcher *libCommons.Launcher) error {
	if dispatcher == nil {
		return ErrOutboxDispatcherRequired
	}

	if dispatcher.repo == nil || dispatcher.handlers == nil {
		return ErrOutboxDispatcherRequired
	}

	if parentCtx == nil {
		parentCtx = context.Background()
	}

	ctx, cancel := context.WithCancel(parentCtx)
	if !dispatcher.registerRun(cancel) {
		cancel()

		return ErrOutboxDispatcherRunning
	}

	defer dispatcher.clearRun()

	if launcher != nil && launcher.Logger != nil {
		launcher.Logger.Log(context.Background(), libLog.LevelInfo, "outbox dispatcher started")
		defer launcher.Logger.Log(context.Background(), libLog.LevelInfo, "outbox dispatcher stopped")
	}

	defer runtime.RecoverAndLogWithContext(
		ctx,
		dispatcher.logger,
		"outbox",
		"dispatcher_run",
	)

	ticker := time.NewTicker(dispatcher.cfg.DispatchInterval)
	defer ticker.Stop()

	func() {
		dispatcher.dispatchWg.Add(1)
		defer dispatcher.dispatchWg.Done()

		initCtx, span := dispatcher.tracer.Start(ctx, "outbox.dispatcher.initial_dispatch")
		defer span.End()
		defer runtime.RecoverAndLogWithContext(initCtx, dispatcher.logger, "outbox", "dispatcher_initial")

		dispatcher.dispatchAcrossTenants(initCtx)
	}()

	for {
		select {
		case <-dispatcher.stop:
			return nil
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			select {
			case <-dispatcher.stop:
				return nil
			case <-ctx.Done():
				return nil
			default:
			}

			func() {
				dispatcher.dispatchWg.Add(1)
				defer dispatcher.dispatchWg.Done()

				tickCtx, span := dispatcher.tracer.Start(ctx, "outbox.dispatcher.dispatch_once")
				defer span.End()
				defer runtime.RecoverAndLogWithContext(tickCtx, dispatcher.logger, "outbox", "dispatcher_tick")

				dispatcher.dispatchAcrossTenants(tickCtx)
			}()
		}
	}
}

// Stop signals the dispatcher loop to stop.
func (dispatcher *Dispatcher) Stop() {
	if dispatcher == nil {
		return
	}

	dispatcher.stopOnce.Do(func() {
		dispatcher.runStateMu.Lock()
		cancel := dispatcher.cancelFunc

		stop := dispatcher.stop
		if stop == nil {
			stop = make(chan struct{})
			dispatcher.stop = stop
		}
		dispatcher.runStateMu.Unlock()

		if cancel != nil {
			cancel()
		}

		close(stop)
	})
}

// Shutdown waits for in-flight dispatch cycle completion.
func (dispatcher *Dispatcher) Shutdown(ctx context.Context) error {
	if dispatcher == nil {
		return nil
	}

	if ctx == nil {
		ctx = context.Background()
	}

	dispatcher.Stop()

	done := make(chan struct{})

	runtime.SafeGo(dispatcher.logger, "outbox.dispatcher_shutdown_wait", runtime.KeepRunning, func() {
		dispatcher.dispatchWg.Wait()
		close(done)
	})

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("dispatcher shutdown: %w", ctx.Err())
	}
}

// DispatchOnce processes one tenant-scoped dispatch cycle.
func (dispatcher *Dispatcher) DispatchOnce(ctx context.Context) int {
	return dispatcher.DispatchOnceResult(ctx).Processed
}

// DispatchOnceResult processes one tenant-scoped dispatch cycle and returns counters.
func (dispatcher *Dispatcher) DispatchOnceResult(ctx context.Context) DispatchResult {
	if dispatcher == nil {
		return DispatchResult{}
	}

	if dispatcher.repo == nil || dispatcher.handlers == nil {
		return DispatchResult{}
	}

	if ctx == nil {
		ctx = context.Background()
	}

	logger := dispatcher.logger
	if nilcheck.Interface(logger) {
		logger = libLog.NewNop()
	}

	tracer := dispatcher.tracer
	if nilcheck.Interface(tracer) {
		tracer = noop.NewTracerProvider().Tracer("commons.noop")
	}

	start := time.Now().UTC()

	ctx, span := tracer.Start(ctx, "outbox.dispatch")
	defer span.End()

	events := dispatcher.collectEvents(ctx, span)
	processed := 0
	published := 0
	failed := 0
	stateUpdateFailed := 0

	tenantKey := tenantKeyFromContext(ctx)
	dispatcher.recordQueueDepth(ctx, tenantKey, int64(len(events)))

	// Delivery semantics are at-least-once: publish happens before MarkPublished.
	// If state persistence fails after publish, consumers must remain idempotent.
	for _, event := range events {
		if ctx.Err() != nil {
			break
		}

		if event == nil {
			continue
		}

		processed++

		if err := dispatcher.publishEventWithRetry(ctx, event); err != nil {
			dispatcher.handlePublishError(ctx, logger, event, err)

			failed++

			continue
		}

		published++

		if err := dispatcher.repo.MarkPublished(ctx, event.ID, time.Now().UTC()); err != nil {
			logger.Log(
				ctx,
				libLog.LevelError,
				"outbox event published to broker but failed to persist PUBLISHED state; event may be retried",
				libLog.String("event_id", event.ID.String()),
				libLog.String("error", sanitizeErrorForStorage(err)),
			)
			dispatcher.addStateUpdateFailure(ctx, tenantKey, 1)

			stateUpdateFailed++

			continue
		}
	}

	dispatcher.addDispatchedEvents(ctx, tenantKey, int64(published))
	dispatcher.addFailedEvents(ctx, tenantKey, int64(failed))
	dispatcher.recordDispatchLatency(ctx, tenantKey, time.Since(start))

	return DispatchResult{
		Processed:         processed,
		Published:         published,
		Failed:            failed,
		StateUpdateFailed: stateUpdateFailed,
	}
}

func (dispatcher *Dispatcher) tenantMetricAttribute(tenantKey string) (attribute.KeyValue, bool) {
	if !dispatcher.cfg.IncludeTenantMetrics {
		return attribute.KeyValue{}, false
	}

	boundedTenant := dispatcher.boundedTenantMetricKey(tenantKey)

	return attribute.String("tenant", boundedTenant), true
}

func (dispatcher *Dispatcher) boundedTenantMetricKey(tenantKey string) string {
	if tenantKey == "" {
		tenantKey = defaultTenantFailureCounterFallback
	}

	dispatcher.tenantMetricMu.Lock()
	defer dispatcher.tenantMetricMu.Unlock()

	if dispatcher.tenantMetricKeys == nil {
		dispatcher.tenantMetricKeys = make(map[string]struct{})
	}

	if _, exists := dispatcher.tenantMetricKeys[tenantKey]; exists {
		return tenantKey
	}

	if len(dispatcher.tenantMetricKeys) < dispatcher.cfg.MaxTenantMetricDimensions {
		dispatcher.tenantMetricKeys[tenantKey] = struct{}{}

		return tenantKey
	}

	return overflowTenantMetricLabel
}

func (dispatcher *Dispatcher) recordQueueDepth(ctx context.Context, tenantKey string, depth int64) {
	if dispatcher.metrics.queueDepth == nil {
		return
	}

	builder := dispatcher.metrics.queueDepth
	if attr, ok := dispatcher.tenantMetricAttribute(tenantKey); ok {
		builder = builder.WithAttributes(attr)
	}

	if err := builder.Set(ctx, depth); err != nil {
		dispatcher.logMetricError(ctx, "record outbox.queue.depth", err)
	}
}

func (dispatcher *Dispatcher) addDispatchedEvents(ctx context.Context, tenantKey string, count int64) {
	if dispatcher.metrics.eventsDispatched == nil || count <= 0 {
		return
	}

	builder := dispatcher.metrics.eventsDispatched
	if attr, ok := dispatcher.tenantMetricAttribute(tenantKey); ok {
		builder = builder.WithAttributes(attr)
	}

	if err := builder.Add(ctx, count); err != nil {
		dispatcher.logMetricError(ctx, "record outbox.events.dispatched", err)
	}
}

func (dispatcher *Dispatcher) addFailedEvents(ctx context.Context, tenantKey string, count int64) {
	if dispatcher.metrics.eventsFailed == nil || count <= 0 {
		return
	}

	builder := dispatcher.metrics.eventsFailed
	if attr, ok := dispatcher.tenantMetricAttribute(tenantKey); ok {
		builder = builder.WithAttributes(attr)
	}

	if err := builder.Add(ctx, count); err != nil {
		dispatcher.logMetricError(ctx, "record outbox.events.failed", err)
	}
}

func (dispatcher *Dispatcher) addStateUpdateFailure(ctx context.Context, tenantKey string, count int64) {
	if dispatcher.metrics.eventsStateFailed == nil || count <= 0 {
		return
	}

	builder := dispatcher.metrics.eventsStateFailed
	if attr, ok := dispatcher.tenantMetricAttribute(tenantKey); ok {
		builder = builder.WithAttributes(attr)
	}

	if err := builder.Add(ctx, count); err != nil {
		dispatcher.logMetricError(ctx, "record outbox.events.state_update_failed", err)
	}
}

func (dispatcher *Dispatcher) recordDispatchLatency(ctx context.Context, tenantKey string, latency time.Duration) {
	if dispatcher.metrics.dispatchLatency == nil {
		return
	}

	options := []metric.RecordOption(nil)
	if attr, ok := dispatcher.tenantMetricAttribute(tenantKey); ok {
		options = append(options, metric.WithAttributes(attr))
	}

	dispatcher.metrics.dispatchLatency.Record(ctx, latency.Seconds(), options...)
}

func (dispatcher *Dispatcher) logMetricError(ctx context.Context, operation string, err error) {
	if err == nil {
		return
	}

	logger := dispatcher.logger
	if nilcheck.Interface(logger) {
		logger = libLog.NewNop()
	}

	logger.Log(ctx, libLog.LevelWarn, "outbox metric recording failed", libLog.String("operation", operation), libLog.Err(err))
}

// dispatchAcrossTenants intentionally keeps tenant dispatch sequential for per-cycle
// predictability, but rotates the starting tenant between cycles to reduce unfairness
// when a single tenant is consistently slow.
func (dispatcher *Dispatcher) dispatchAcrossTenants(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}

	logger, tracer, _, _ := observability.NewTrackingFromContext(ctx)
	if nilcheck.Interface(logger) {
		logger = dispatcher.logger
	}

	if nilcheck.Interface(tracer) {
		tracer = dispatcher.tracer
	}

	if nilcheck.Interface(tracer) {
		tracer = noop.NewTracerProvider().Tracer("commons.noop")
	}

	ctx, span := tracer.Start(ctx, "outbox.dispatcher.tenants")
	defer span.End()

	tenants, err := dispatcher.repo.ListTenants(ctx)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to list tenants", err)
		libLog.SafeError(logger, ctx, "failed to list tenants", err, false)

		return
	}

	orderedTenants := dispatcher.tenantDispatchOrder(nonEmptyTenants(tenants))
	if len(orderedTenants) == 0 {
		dispatcher.dispatchWithoutDiscoveredTenant(ctx, tracer)

		return
	}

	for _, tenantID := range orderedTenants {
		if ctx.Err() != nil {
			break
		}

		tenantCtx := ContextWithTenantID(ctx, tenantID)
		tenantCtx, tenantSpan := tracer.Start(tenantCtx, "outbox.dispatcher.tenant")
		result := dispatcher.DispatchOnceResult(tenantCtx)
		// Keep tenant trace correlation without exposing raw tenant identifiers.
		tenantSpan.SetAttributes(
			attribute.String("tenant.id_hash", hashTenantID(tenantID)),
			attribute.Int("outbox.dispatch.processed", result.Processed),
			attribute.Int("outbox.dispatch.published", result.Published),
			attribute.Int("outbox.dispatch.failed", result.Failed),
			attribute.Int("outbox.dispatch.state_update_failed", result.StateUpdateFailed),
		)

		tenantSpan.End()
	}
}

func (dispatcher *Dispatcher) dispatchWithoutDiscoveredTenant(ctx context.Context, tracer trace.Tracer) {
	tenantID, ok := TenantIDFromContext(ctx)
	if ok && tenantID != "" {
		dispatcher.DispatchOnceResult(ctx)

		return
	}

	requiresTenant := true
	if reporter, ok := dispatcher.repo.(tenantRequirementReporter); ok {
		requiresTenant = reporter.RequiresTenant()
	}

	if requiresTenant {
		dispatcher.logger.Log(
			ctx,
			libLog.LevelWarn,
			"outbox tenant discovery returned no tenants; skipping dispatch because repository requires tenant context",
		)

		return
	}

	fallbackCtx, fallbackSpan := tracer.Start(ctx, "outbox.dispatcher.default_scope")
	result := dispatcher.DispatchOnceResult(fallbackCtx)
	fallbackSpan.SetAttributes(
		attribute.Int("outbox.dispatch.processed", result.Processed),
		attribute.Int("outbox.dispatch.published", result.Published),
		attribute.Int("outbox.dispatch.failed", result.Failed),
		attribute.Int("outbox.dispatch.state_update_failed", result.StateUpdateFailed),
	)
	fallbackSpan.End()
}

func nonEmptyTenants(tenants []string) []string {
	if len(tenants) == 0 {
		return nil
	}

	result := make([]string, 0, len(tenants))
	for _, tenantID := range tenants {
		tenantID = strings.TrimSpace(tenantID)

		if tenantID == "" {
			continue
		}

		result = append(result, tenantID)
	}

	return result
}

func (dispatcher *Dispatcher) registerRun(cancel context.CancelFunc) bool {
	dispatcher.runStateMu.Lock()
	defer dispatcher.runStateMu.Unlock()

	if dispatcher.running {
		return false
	}

	if dispatcher.stop == nil || isClosedSignal(dispatcher.stop) {
		dispatcher.stop = make(chan struct{})
		dispatcher.stopOnce = sync.Once{}
	}

	dispatcher.running = true
	dispatcher.cancelFunc = cancel

	return true
}

func (dispatcher *Dispatcher) clearRun() {
	dispatcher.runStateMu.Lock()
	defer dispatcher.runStateMu.Unlock()

	dispatcher.running = false
	dispatcher.cancelFunc = nil
}

func (dispatcher *Dispatcher) tenantDispatchOrder(tenants []string) []string {
	if len(tenants) <= 1 {
		return append([]string(nil), tenants...)
	}

	dispatcher.runStateMu.Lock()
	start := dispatcher.tenantTurn % len(tenants)
	dispatcher.tenantTurn = (dispatcher.tenantTurn + 1) % len(tenants)
	dispatcher.runStateMu.Unlock()

	ordered := make([]string, 0, len(tenants))
	ordered = append(ordered, tenants[start:]...)
	ordered = append(ordered, tenants[:start]...)

	return ordered
}

// collectEvents gathers events for a single dispatch cycle using a priority-layered
// strategy. Events are collected in this order:
//
//  1. Priority events: pending events matching PriorityEventTypes (up to PriorityBudget)
//  2. Stuck events: PROCESSING events older than ProcessingTimeout (reclaimed for retry)
//  3. Failed events: FAILED events older than RetryWindow with remaining attempts
//  4. Pending events: remaining PENDING events ordered by created_at ASC
//
// Within each layer, ordering follows the respective SQL query (typically ASC by
// created_at or updated_at). The total batch is bounded by BatchSize. Duplicate
// events (e.g., a priority event also in the pending set) are removed.
func (dispatcher *Dispatcher) collectEvents(ctx context.Context, span trace.Span) []*OutboxEvent {
	logger := dispatcher.logger
	failedBefore := time.Now().UTC().Add(-dispatcher.cfg.RetryWindow)
	processingBefore := time.Now().UTC().Add(-dispatcher.cfg.ProcessingTimeout)

	priorityBudget := min(dispatcher.cfg.PriorityBudget, dispatcher.cfg.BatchSize)
	priorityEvents := dispatcher.collectPriorityEvents(ctx, span, priorityBudget)
	collected := len(priorityEvents)

	stuckLimit := dispatcher.cfg.BatchSize - collected
	if stuckLimit <= 0 {
		return deduplicateEvents(priorityEvents)
	}

	stuckEvents, err := dispatcher.repo.ResetStuckProcessing(
		ctx,
		stuckLimit,
		processingBefore,
		dispatcher.cfg.MaxDispatchAttempts,
	)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to reset stuck events", err)
		libLog.SafeError(logger, ctx, "failed to reset stuck events", err, false)
	}

	collected += len(stuckEvents)

	failedLimit := min(dispatcher.cfg.BatchSize-collected, dispatcher.cfg.MaxFailedPerBatch)
	if failedLimit <= 0 {
		return deduplicateEvents(append(priorityEvents, stuckEvents...))
	}

	failedEvents, err := dispatcher.repo.ResetForRetry(
		ctx,
		failedLimit,
		failedBefore,
		dispatcher.cfg.MaxDispatchAttempts,
	)
	if err != nil {
		libOpentelemetry.HandleSpanError(span, "failed to reset failed events for retry", err)
		libLog.SafeError(logger, ctx, "failed to reset failed events for retry", err, false)
	}

	collected += len(failedEvents)

	remaining := dispatcher.cfg.BatchSize - collected
	if remaining <= 0 {
		return deduplicateEvents(append(append(priorityEvents, stuckEvents...), failedEvents...))
	}

	pendingEvents, err := dispatcher.repo.ListPending(ctx, remaining)
	if err != nil {
		tenantKey := tenantKeyFromContext(ctx)
		dispatcher.handleListPendingError(ctx, span, tenantKey, err)

		return deduplicateEvents(append(append(priorityEvents, stuckEvents...), failedEvents...))
	}

	tenantKey := tenantKeyFromContext(ctx)
	dispatcher.clearListPendingFailureCount(tenantKey)

	all := make([]*OutboxEvent, 0, collected+len(pendingEvents))
	all = append(all, priorityEvents...)
	all = append(all, stuckEvents...)
	all = append(all, failedEvents...)
	all = append(all, pendingEvents...)

	return deduplicateEvents(all)
}

func deduplicateEvents(events []*OutboxEvent) []*OutboxEvent {
	if len(events) == 0 {
		return events
	}

	seen := make(map[uuid.UUID]bool, len(events))
	result := make([]*OutboxEvent, 0, len(events))

	for _, event := range events {
		if event == nil {
			continue
		}

		if seen[event.ID] {
			continue
		}

		seen[event.ID] = true
		result = append(result, event)
	}

	return result
}

func (dispatcher *Dispatcher) collectPriorityEvents(
	ctx context.Context,
	span trace.Span,
	budget int,
) []*OutboxEvent {
	if budget <= 0 || len(dispatcher.cfg.PriorityEventTypes) == 0 {
		return nil
	}

	var result []*OutboxEvent

	for _, eventType := range dispatcher.cfg.PriorityEventTypes {
		remaining := budget - len(result)
		if remaining <= 0 {
			break
		}

		events, err := dispatcher.repo.ListPendingByType(ctx, eventType, remaining)
		if err != nil {
			libOpentelemetry.HandleSpanError(span, "failed to list priority events", err)
			libLog.SafeError(dispatcher.logger, ctx, "failed to list priority events", err, false)

			continue
		}

		result = append(result, events...)
	}

	return result
}

func tenantKeyFromContext(ctx context.Context) string {
	tenantID, ok := TenantIDFromContext(ctx)
	if ok && tenantID != "" {
		return tenantID
	}

	return defaultTenantFailureCounterFallback
}

func hashTenantID(tenantID string) string {
	if tenantID == "" {
		return ""
	}

	sum := sha256.Sum256([]byte(tenantID))

	return hex.EncodeToString(sum[:8])
}

func isClosedSignal(signal <-chan struct{}) bool {
	if signal == nil {
		return false
	}

	select {
	case <-signal:
		return true
	default:
		return false
	}
}

func (dispatcher *Dispatcher) ensureFailureCounterFallback() {
	dispatcher.failureCountsMu.Lock()
	defer dispatcher.failureCountsMu.Unlock()

	if dispatcher.listPendingFailureCounts == nil {
		dispatcher.listPendingFailureCounts = make(map[string]int)
	}

	dispatcher.listPendingFailureCounts[defaultTenantFailureCounterFallback] = 0
}

func (dispatcher *Dispatcher) handleListPendingError(ctx context.Context, span trace.Span, tenantKey string, err error) {
	logger := dispatcher.logger

	libOpentelemetry.HandleSpanError(span, "failed to list outbox events", err)
	libLog.SafeError(logger, ctx, "failed to list outbox events", err, false)

	counterTenantKey := tenantKey

	dispatcher.failureCountsMu.Lock()

	maxTracked := dispatcher.cfg.MaxTrackedListPendingFailureTenants
	if maxTracked <= 1 {
		counterTenantKey = defaultTenantFailureCounterFallback
	} else if _, exists := dispatcher.listPendingFailureCounts[counterTenantKey]; !exists &&
		len(dispatcher.listPendingFailureCounts) >= maxTracked {
		counterTenantKey = defaultTenantFailureCounterFallback
	}

	dispatcher.listPendingFailureCounts[counterTenantKey]++
	count := dispatcher.listPendingFailureCounts[counterTenantKey]
	dispatcher.failureCountsMu.Unlock()

	if count >= dispatcher.cfg.ListPendingFailureThreshold {
		fields := []libLog.Field{libLog.Int("count", count)}
		if counterTenantKey == "" || counterTenantKey == defaultTenantFailureCounterFallback {
			fields = append(fields, libLog.String("tenant_bucket", defaultTenantFailureCounterFallback))
		} else {
			fields = append(fields, libLog.String("tenant_hash", hashTenantID(counterTenantKey)))
		}

		logger.Log(ctx, libLog.LevelError, "outbox list pending failures exceeded threshold", fields...)
	}
}

func (dispatcher *Dispatcher) clearListPendingFailureCount(tenantKey string) {
	dispatcher.failureCountsMu.Lock()
	defer dispatcher.failureCountsMu.Unlock()

	if tenantKey == "" || tenantKey == defaultTenantFailureCounterFallback {
		dispatcher.listPendingFailureCounts[defaultTenantFailureCounterFallback] = 0
		return
	}

	if _, exists := dispatcher.listPendingFailureCounts[tenantKey]; !exists {
		// Untracked tenants are folded into fallback when cap is reached. Any
		// successful list for such tenants should also clear fallback failures.
		dispatcher.listPendingFailureCounts[defaultTenantFailureCounterFallback] = 0
		return
	}

	delete(dispatcher.listPendingFailureCounts, tenantKey)
}

func (dispatcher *Dispatcher) publishEventWithRetry(ctx context.Context, event *OutboxEvent) error {
	maxAttempts := dispatcher.cfg.PublishMaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = defaultPublishMaxAttempts
	}

	publishBackoff := dispatcher.cfg.PublishBackoff
	if publishBackoff <= 0 {
		publishBackoff = defaultPublishBackoff
	}

	var lastErr error

	for attempt := range maxAttempts {
		err := dispatcher.publishEvent(ctx, event)
		if err == nil {
			return nil
		}

		lastErr = fmt.Errorf("publish attempt %d/%d failed: %w", attempt+1, maxAttempts, err)
		if dispatcher.isNonRetryableError(err) || attempt == maxAttempts-1 {
			break
		}

		delay := backoff.ExponentialWithJitter(publishBackoff, attempt)
		if waitErr := backoff.WaitContext(ctx, delay); waitErr != nil {
			lastErr = fmt.Errorf("publish retry wait interrupted: %w", waitErr)
			break
		}
	}

	return lastErr
}

func (dispatcher *Dispatcher) publishEvent(ctx context.Context, event *OutboxEvent) error {
	if event == nil {
		return ErrOutboxEventRequired
	}

	if len(event.Payload) == 0 {
		return ErrOutboxEventPayloadRequired
	}

	return dispatcher.handlers.Handle(ctx, event)
}

func (dispatcher *Dispatcher) handlePublishError(
	ctx context.Context,
	logger libLog.Logger,
	event *OutboxEvent,
	err error,
) {
	// Always surface the publish failure before mutating event state so that
	// transitions to FAILED/INVALID are observable in logs.
	logger.Log(
		ctx,
		libLog.LevelWarn,
		"outbox event publish failed",
		libLog.String("event_id", event.ID.String()),
		libLog.String("event_type", event.EventType),
		libLog.Err(err),
	)

	if dispatcher.isNonRetryableError(err) {
		// Non-retryable errors send the event straight to INVALID; log at ERROR.
		logger.Log(
			ctx,
			libLog.LevelError,
			"outbox event is non-retryable; marking invalid",
			libLog.String("event_id", event.ID.String()),
			libLog.String("event_type", event.EventType),
			libLog.Err(err),
		)

		if markErr := dispatcher.repo.MarkInvalid(ctx, event.ID, sanitizeErrorForStorage(err)); markErr != nil {
			logger.Log(ctx, libLog.LevelError, "failed to mark outbox invalid", libLog.String("error", sanitizeErrorForStorage(markErr)))

			return
		}

		// Only invoke the OnInvalid callback after the event has been durably
		// persisted as INVALID; otherwise we would emit a callback for an event
		// that was not actually transitioned in storage.
		dispatcher.invokeOnInvalid(ctx, event, err)

		return
	}

	if markErr := dispatcher.repo.MarkFailed(ctx, event.ID, sanitizeErrorForStorage(err), dispatcher.cfg.MaxDispatchAttempts); markErr != nil {
		logger.Log(ctx, libLog.LevelError, "failed to mark outbox failed", libLog.String("error", sanitizeErrorForStorage(markErr)))

		return
	}

	// MarkFailed transitions FAILED->INVALID internally once attempts reach
	// MaxDispatchAttempts. Mirror that decision here so the INVALID callback
	// fires when this attempt exhausts the budget; otherwise treat as a
	// retryable failure.
	if event.Attempts+1 >= dispatcher.cfg.MaxDispatchAttempts {
		dispatcher.invokeOnInvalid(ctx, event, err)

		return
	}

	dispatcher.invokeOnFailed(ctx, event, err)
}

// invokeOnInvalid runs the optional OnInvalid callback best-effort, swallowing panics and errors.
func (dispatcher *Dispatcher) invokeOnInvalid(ctx context.Context, event *OutboxEvent, err error) {
	if dispatcher.cfg.OnInvalid == nil {
		return
	}

	dispatcher.safeInvokeLifecycle(ctx, "OnInvalid", dispatcher.cfg.OnInvalid, event, err)
}

// invokeOnFailed runs the optional OnFailed callback best-effort, swallowing panics and errors.
func (dispatcher *Dispatcher) invokeOnFailed(ctx context.Context, event *OutboxEvent, err error) {
	if dispatcher.cfg.OnFailed == nil {
		return
	}

	dispatcher.safeInvokeLifecycle(ctx, "OnFailed", dispatcher.cfg.OnFailed, event, err)
}

// safeInvokeLifecycle invokes a lifecycle callback, recovering from panics and
// logging at WARN so a misbehaving callback never breaks the dispatch loop.
func (dispatcher *Dispatcher) safeInvokeLifecycle(
	ctx context.Context,
	name string,
	fn func(ctx context.Context, event *OutboxEvent, err error),
	event *OutboxEvent,
	err error,
) {
	logger := dispatcher.logger
	if nilcheck.Interface(logger) {
		logger = libLog.NewNop()
	}

	defer func() {
		if r := recover(); r != nil {
			logger.Log(
				ctx,
				libLog.LevelWarn,
				"outbox lifecycle callback panicked",
				libLog.String("callback", name),
				libLog.String("event_id", event.ID.String()),
				libLog.String("panic", fmt.Sprintf("%v", r)),
			)
		}
	}()

	fn(ctx, event, err)
}

func (dispatcher *Dispatcher) isNonRetryableError(err error) bool {
	if err == nil || nilcheck.Interface(dispatcher.retryClassifier) {
		return false
	}

	return dispatcher.retryClassifier.IsNonRetryable(err)
}
