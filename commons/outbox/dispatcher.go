package outbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/LerianStudio/lib-commons/v5/commons/internal/nilcheck"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	libCommons "github.com/LerianStudio/lib-commons/v5/commons"
	"github.com/LerianStudio/lib-commons/v5/commons/backoff"
	libLog "github.com/LerianStudio/lib-commons/v5/commons/log"
	libOpentelemetry "github.com/LerianStudio/lib-commons/v5/commons/opentelemetry"
	"github.com/LerianStudio/lib-commons/v5/commons/runtime"
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

	metrics, err := newDispatcherMetrics(dispatcher.cfg.MeterProvider)
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
	dispatcher.recordDispatchLatency(ctx, tenantKey, time.Since(start).Seconds())

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

	dispatcher.metrics.queueDepth.Record(ctx, depth, dispatcher.tenantRecordOptions(tenantKey)...)
}

func (dispatcher *Dispatcher) addDispatchedEvents(ctx context.Context, tenantKey string, count int64) {
	if dispatcher.metrics.eventsDispatched == nil || count <= 0 {
		return
	}

	dispatcher.metrics.eventsDispatched.Add(ctx, count, dispatcher.tenantAddOptions(tenantKey)...)
}

func (dispatcher *Dispatcher) addFailedEvents(ctx context.Context, tenantKey string, count int64) {
	if dispatcher.metrics.eventsFailed == nil || count <= 0 {
		return
	}

	dispatcher.metrics.eventsFailed.Add(ctx, count, dispatcher.tenantAddOptions(tenantKey)...)
}

func (dispatcher *Dispatcher) addStateUpdateFailure(ctx context.Context, tenantKey string, count int64) {
	if dispatcher.metrics.eventsStateFailed == nil || count <= 0 {
		return
	}

	dispatcher.metrics.eventsStateFailed.Add(ctx, count, dispatcher.tenantAddOptions(tenantKey)...)
}

func (dispatcher *Dispatcher) recordDispatchLatency(ctx context.Context, tenantKey string, latencySeconds float64) {
	if dispatcher.metrics.dispatchLatency == nil {
		return
	}

	dispatcher.metrics.dispatchLatency.Record(ctx, latencySeconds, dispatcher.tenantRecordOptions(tenantKey)...)
}

// dispatchAcrossTenants intentionally keeps tenant dispatch sequential for per-cycle
// predictability, but rotates the starting tenant between cycles to reduce unfairness
// when a single tenant is consistently slow.
func (dispatcher *Dispatcher) dispatchAcrossTenants(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}

	logger, tracer, _, _ := libCommons.NewTrackingFromContext(ctx)
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

func (dispatcher *Dispatcher) tenantAddOptions(tenantKey string) []metric.AddOption {
	if attr, ok := dispatcher.tenantMetricAttribute(tenantKey); ok {
		return []metric.AddOption{metric.WithAttributes(attr)}
	}

	return nil
}

func (dispatcher *Dispatcher) tenantRecordOptions(tenantKey string) []metric.RecordOption {
	if attr, ok := dispatcher.tenantMetricAttribute(tenantKey); ok {
		return []metric.RecordOption{metric.WithAttributes(attr)}
	}

	return nil
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
	if dispatcher.isNonRetryableError(err) {
		if markErr := dispatcher.repo.MarkInvalid(ctx, event.ID, sanitizeErrorForStorage(err)); markErr != nil {
			logger.Log(ctx, libLog.LevelError, "failed to mark outbox invalid", libLog.String("error", sanitizeErrorForStorage(markErr)))
		}

		return
	}

	if markErr := dispatcher.repo.MarkFailed(ctx, event.ID, sanitizeErrorForStorage(err), dispatcher.cfg.MaxDispatchAttempts); markErr != nil {
		logger.Log(ctx, libLog.LevelError, "failed to mark outbox failed", libLog.String("error", sanitizeErrorForStorage(markErr)))
	}
}

func (dispatcher *Dispatcher) isNonRetryableError(err error) bool {
	if err == nil || nilcheck.Interface(dispatcher.retryClassifier) {
		return false
	}

	return dispatcher.retryClassifier.IsNonRetryable(err)
}
