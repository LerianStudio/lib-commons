//go:build unit

package streaming

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kerr"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/LerianStudio/lib-commons/v5/commons/circuitbreaker"
	"github.com/LerianStudio/lib-commons/v5/commons/log"
	libMetrics "github.com/LerianStudio/lib-commons/v5/commons/opentelemetry/metrics"
)

// --- Shared T6 helpers. ---

// newManualMeterSetup builds a ManualReader + MeterProvider + *MetricsFactory
// and returns all three plus a snapshot helper. The helper performs Collect
// into a fresh ResourceMetrics so each call reflects the current cumulative
// state.
//
// Using a real SDK meter (not a mock) keeps these tests honest: we're
// asserting the library produces the instruments the SDK accepts, with the
// attribute sets the SDK observes. A hand-rolled spy would be easier to
// reason about but less representative of the production plumbing.
func newManualMeterSetup(t *testing.T) (*libMetrics.MetricsFactory, func() metricdata.ResourceMetrics) {
	t.Helper()

	reader := metric.NewManualReader()
	provider := metric.NewMeterProvider(metric.WithReader(reader))

	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
	})

	factory, err := libMetrics.NewMetricsFactory(provider.Meter("streaming-test"), log.NewNop())
	if err != nil {
		t.Fatalf("NewMetricsFactory err = %v", err)
	}

	snapshot := func() metricdata.ResourceMetrics {
		var rm metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &rm); err != nil {
			t.Fatalf("Collect err = %v", err)
		}
		return rm
	}

	return factory, snapshot
}

// findMetric walks the ResourceMetrics snapshot returning the Metrics entry
// with the given name. Returns (zero, false) when not present so callers can
// differentiate "never-recorded" from "recorded with wrong attrs".
func findMetric(rm metricdata.ResourceMetrics, name string) (metricdata.Metrics, bool) {
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			if m.Name == name {
				return m, true
			}
		}
	}
	return metricdata.Metrics{}, false
}

// attrSetToMap converts an attribute.Set into a map[string]string via
// ToSlice. Values are stringified through attribute.Value.Emit() so
// non-string types (bool, int, float) still serialise.
func attrSetToMap(set attribute.Set) map[string]string {
	out := make(map[string]string, set.Len())
	for _, kv := range set.ToSlice() {
		out[string(kv.Key)] = kv.Value.Emit()
	}
	return out
}

// sumInt64DataPoints returns the total Value across all DataPoints of an
// int64 Sum aggregation, along with the attribute sets observed. Per
// DataPoint attribute-set is a fresh map[string]string so callers can
// hold them across future Collect calls without aliasing.
func sumInt64DataPoints(t *testing.T, m metricdata.Metrics) (int64, []map[string]string) {
	t.Helper()

	agg, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("metric %s: data type = %T; want metricdata.Sum[int64]", m.Name, m.Data)
	}

	var total int64
	attrSets := make([]map[string]string, 0, len(agg.DataPoints))

	for _, dp := range agg.DataPoints {
		total += dp.Value
		attrSets = append(attrSets, attrSetToMap(dp.Attributes))
	}

	return total, attrSets
}

// gaugeInt64Values returns the latest recorded value (the last DataPoint)
// for each attribute set in the gauge aggregation.
func gaugeInt64Values(t *testing.T, m metricdata.Metrics) []int64 {
	t.Helper()

	agg, ok := m.Data.(metricdata.Gauge[int64])
	if !ok {
		t.Fatalf("metric %s: data type = %T; want metricdata.Gauge[int64]", m.Name, m.Data)
	}

	values := make([]int64, 0, len(agg.DataPoints))
	for _, dp := range agg.DataPoints {
		values = append(values, dp.Value)
	}

	return values
}

// histogramCount returns the total Count across all DataPoints of an int64
// Histogram aggregation. Used to assert "at least one observation recorded".
func histogramCount(t *testing.T, m metricdata.Metrics) uint64 {
	t.Helper()

	agg, ok := m.Data.(metricdata.Histogram[int64])
	if !ok {
		t.Fatalf("metric %s: data type = %T; want metricdata.Histogram[int64]", m.Name, m.Data)
	}

	var total uint64
	for _, dp := range agg.DataPoints {
		total += dp.Count
	}

	return total
}

// extractAttributeSets walks all DataPoints on a Metrics entry and returns
// their attribute sets flattened to map[string]string. Handles Sum, Gauge,
// and Histogram aggregations (the three the streaming package uses).
func extractAttributeSets(m metricdata.Metrics) []map[string]string {
	switch agg := m.Data.(type) {
	case metricdata.Sum[int64]:
		out := make([]map[string]string, len(agg.DataPoints))
		for i, dp := range agg.DataPoints {
			out[i] = attrSetToMap(dp.Attributes)
		}
		return out
	case metricdata.Gauge[int64]:
		out := make([]map[string]string, len(agg.DataPoints))
		for i, dp := range agg.DataPoints {
			out[i] = attrSetToMap(dp.Attributes)
		}
		return out
	case metricdata.Histogram[int64]:
		out := make([]map[string]string, len(agg.DataPoints))
		for i, dp := range agg.DataPoints {
			out[i] = attrSetToMap(dp.Attributes)
		}
		return out
	}
	return nil
}

// --- Lazy-init + nil-factory safety. ---

// TestMetrics_LazyInit_NilFactory_NoPanic_WarnsOnce: a nil MetricsFactory
// must not panic on any record method; the Producer logs exactly one WARN
// at the first record, and every subsequent record is silent.
//
// Load-bearing for the nil-safe promise in the streamingMetrics godoc —
// every record* method checks m.factory == nil and falls through to
// warnNilFactoryOnce, which sync.Once-guards the log.
func TestMetrics_LazyInit_NilFactory_NoPanic_WarnsOnce(t *testing.T) {
	spy := &spyLogger{}
	m := newStreamingMetrics(nil, spy)

	ctx := context.Background()

	// Exercise every record* method multiple times. None should panic, none
	// should produce more than the single WARN.
	for i := 0; i < 5; i++ {
		m.recordEmitted(ctx, "topic.a", "send", outcomeProduced)
		m.recordEmitDuration(ctx, "topic.a", outcomeProduced, int64(i))
		m.recordDLQ(ctx, "topic.a", "auth_error")
		m.recordDLQFailed(ctx, "topic.a")
		m.recordOutboxRouted(ctx, "topic.a", "circuit_open")
		m.recordCircuitState(ctx, flagCBOpen)
	}

	warnCount := 0

	spy.mu.Lock()
	for _, e := range spy.entries {
		if e.level == log.LevelWarn && strings.Contains(e.msg, "metrics factory is nil") {
			warnCount++
		}
	}
	spy.mu.Unlock()

	if warnCount != 1 {
		t.Errorf("nil-factory WARN count = %d; want 1", warnCount)
	}
}

// TestMetrics_LazyInit_NilReceiver_NoPanic: calling record* on a nil
// *streamingMetrics pointer must not panic. Defends against a future
// refactor that assumes p.metrics is always non-nil.
func TestMetrics_LazyInit_NilReceiver_NoPanic(t *testing.T) {
	var m *streamingMetrics

	ctx := context.Background()
	m.recordEmitted(ctx, "t", "send", outcomeProduced)
	m.recordEmitDuration(ctx, "t", outcomeProduced, 1)
	m.recordDLQ(ctx, "t", "auth_error")
	m.recordDLQFailed(ctx, "t")
	m.recordOutboxRouted(ctx, "t", "circuit_open")
	m.recordCircuitState(ctx, flagCBClosed)

	// If we reached here without panic the test is green.
}

// TestMetrics_LazyInit_NilLogger_SubstitutesNop: newStreamingMetrics with a
// nil logger must still be usable (internal logger becomes log.NewNop()).
// This is an API-shape test, not a behaviour test.
func TestMetrics_LazyInit_NilLogger_SubstitutesNop(t *testing.T) {
	m := newStreamingMetrics(nil, nil)
	if m.logger == nil {
		t.Fatalf("logger = nil; want non-nil nop logger")
	}

	// Must not panic when exercised.
	m.recordEmitted(context.Background(), "t", "send", outcomeProduced)
}

// --- Integration with Producer.Emit. ---

// TestMetrics_Emit_RecordsEmittedCounterWithOutcomeProduced: happy-path Emit
// through kfake increments streaming_emitted_total with outcome=produced.
func TestMetrics_Emit_RecordsEmittedCounterWithOutcomeProduced(t *testing.T) {
	cfg, _ := kfakeConfig(t)
	factory, snapshot := newManualMeterSetup(t)

	emitter, err := New(context.Background(), cfg,
		WithLogger(log.NewNop()),
		WithMetricsFactory(factory),
	)
	if err != nil {
		t.Fatalf("New err = %v", err)
	}
	t.Cleanup(func() { _ = emitter.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := emitter.Emit(ctx, sampleEvent()); err != nil {
		t.Fatalf("Emit err = %v", err)
	}

	rm := snapshot()

	m, ok := findMetric(rm, metricNameEmitted)
	if !ok {
		t.Fatalf("metric %q not found in snapshot", metricNameEmitted)
	}

	total, attrSets := sumInt64DataPoints(t, m)
	if total != 1 {
		t.Errorf("streaming_emitted_total = %d; want 1", total)
	}

	if len(attrSets) != 1 {
		t.Fatalf("attrSets len = %d; want 1", len(attrSets))
	}

	got := attrSets[0]
	if got["outcome"] != outcomeProduced {
		t.Errorf("outcome label = %q; want %q", got["outcome"], outcomeProduced)
	}
	if got["operation"] != "send" {
		t.Errorf("operation label = %q; want %q", got["operation"], "send")
	}
	if _, tenantPresent := got["tenant_id"]; tenantPresent {
		t.Errorf("tenant_id label MUST NOT appear on streaming_emitted_total (DX-D02); got %v", got)
	}
}

// TestMetrics_Emit_RecordsDurationHistogram: a successful Emit produces at
// least one streaming_emit_duration_ms observation.
func TestMetrics_Emit_RecordsDurationHistogram(t *testing.T) {
	cfg, _ := kfakeConfig(t)
	factory, snapshot := newManualMeterSetup(t)

	emitter, err := New(context.Background(), cfg,
		WithLogger(log.NewNop()),
		WithMetricsFactory(factory),
	)
	if err != nil {
		t.Fatalf("New err = %v", err)
	}
	t.Cleanup(func() { _ = emitter.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := emitter.Emit(ctx, sampleEvent()); err != nil {
		t.Fatalf("Emit err = %v", err)
	}

	rm := snapshot()

	m, ok := findMetric(rm, metricNameEmitDurationMS)
	if !ok {
		t.Fatalf("metric %q not found", metricNameEmitDurationMS)
	}

	if got := histogramCount(t, m); got < 1 {
		t.Errorf("histogram count = %d; want >= 1", got)
	}
}

// TestMetrics_Emit_PreflightFailure_RecordsCallerErrorOutcome: a caller-
// validation error (missing tenant) surfaces on the counter with
// outcome=caller_error. Preflight paths record the counter but NOT the
// duration histogram — verifying that matters: a skew in duration metrics
// would mix pure-validation fast-fails in with actual-emission latency.
func TestMetrics_Emit_PreflightFailure_RecordsCallerErrorOutcome(t *testing.T) {
	cfg, _ := kfakeConfig(t)
	factory, snapshot := newManualMeterSetup(t)

	emitter, err := New(context.Background(), cfg,
		WithLogger(log.NewNop()),
		WithMetricsFactory(factory),
	)
	if err != nil {
		t.Fatalf("New err = %v", err)
	}
	t.Cleanup(func() { _ = emitter.Close() })

	bad := sampleEvent()
	bad.TenantID = ""

	if err := emitter.Emit(context.Background(), bad); !errors.Is(err, ErrMissingTenantID) {
		t.Fatalf("Emit err = %v; want ErrMissingTenantID", err)
	}

	rm := snapshot()

	m, ok := findMetric(rm, metricNameEmitted)
	if !ok {
		t.Fatalf("metric %q not found", metricNameEmitted)
	}

	_, attrSets := sumInt64DataPoints(t, m)
	if len(attrSets) != 1 {
		t.Fatalf("attrSets len = %d; want 1", len(attrSets))
	}

	if got := attrSets[0]["outcome"]; got != outcomeCallerError {
		t.Errorf("outcome = %q; want %q", got, outcomeCallerError)
	}

	// Duration histogram MUST NOT have received a datapoint — preflight
	// rejections are not emission attempts.
	if _, ok := findMetric(rm, metricNameEmitDurationMS); ok {
		// Present is acceptable only if count is zero; the SDK reports a
		// metric even with no observations if the instrument was created.
		if m, _ := findMetric(rm, metricNameEmitDurationMS); histogramCount(t, m) > 0 {
			t.Errorf("duration histogram recorded for preflight failure; want 0")
		}
	}
}

// TestMetrics_DLQ_RecordsDlqCounter: force a source-topic publish failure
// into a DLQ-routable class; the DLQ counter records (topic, error_class).
func TestMetrics_DLQ_RecordsDlqCounter(t *testing.T) {
	cfg, cluster := kfakeDLQConfig(t)

	sourceTopic := "lerian.streaming.transaction.created"
	injectProduceError(cluster, sourceTopic, kerr.MessageTooLarge.Code)

	factory, snapshot := newManualMeterSetup(t)

	emitter, err := New(context.Background(), cfg,
		WithLogger(log.NewNop()),
		WithMetricsFactory(factory),
	)
	if err != nil {
		t.Fatalf("New err = %v", err)
	}
	t.Cleanup(func() { _ = emitter.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = emitter.Emit(ctx, sampleEvent())
	if err == nil {
		t.Fatalf("Emit err = nil; want an error (source publish was forced to fail)")
	}

	rm := snapshot()

	m, ok := findMetric(rm, metricNameDLQTotal)
	if !ok {
		t.Fatalf("metric %q not found; dlq path did not route", metricNameDLQTotal)
	}

	total, attrSets := sumInt64DataPoints(t, m)
	if total < 1 {
		t.Errorf("streaming_dlq_total = %d; want >= 1", total)
	}

	if len(attrSets) == 0 {
		t.Fatalf("no attribute sets recorded")
	}

	found := false
	for _, set := range attrSets {
		if set["topic"] == sourceTopic && set["error_class"] != "" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("no (topic=%q, error_class=*) datapoint in %v", sourceTopic, attrSets)
	}

	// Also verify emitted_total has outcome=dlq for the same attempt.
	em, ok := findMetric(rm, metricNameEmitted)
	if !ok {
		t.Fatalf("metric %q not found", metricNameEmitted)
	}

	_, emSets := sumInt64DataPoints(t, em)
	hasDLQ := false
	for _, set := range emSets {
		if set["outcome"] == outcomeDLQ {
			hasDLQ = true
			break
		}
	}
	if !hasDLQ {
		t.Errorf("emitted_total: no outcome=%q datapoint in %v", outcomeDLQ, emSets)
	}
}

// TestMetrics_DLQFailed_RecordsCounter: when the DLQ publish itself fails,
// streaming_dlq_publish_failed_total increments.
func TestMetrics_DLQFailed_RecordsCounter(t *testing.T) {
	cfg, cluster := kfakeDLQConfig(t)

	sourceTopic := "lerian.streaming.transaction.created"
	dlqT := sourceTopic + ".dlq"

	// Fail BOTH the source and the DLQ topics.
	injectProduceError(cluster, sourceTopic, kerr.MessageTooLarge.Code)
	injectProduceError(cluster, dlqT, kerr.BrokerNotAvailable.Code)

	factory, snapshot := newManualMeterSetup(t)

	emitter, err := New(context.Background(), cfg,
		WithLogger(log.NewNop()),
		WithMetricsFactory(factory),
	)
	if err != nil {
		t.Fatalf("New err = %v", err)
	}
	t.Cleanup(func() { _ = emitter.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := emitter.Emit(ctx, sampleEvent()); err == nil {
		t.Fatalf("Emit err = nil; want an error (source + DLQ forced to fail)")
	}

	rm := snapshot()

	m, ok := findMetric(rm, metricNameDLQFailed)
	if !ok {
		t.Fatalf("metric %q not found", metricNameDLQFailed)
	}

	total, attrSets := sumInt64DataPoints(t, m)
	if total < 1 {
		t.Errorf("streaming_dlq_publish_failed_total = %d; want >= 1", total)
	}

	found := false
	for _, set := range attrSets {
		if set["topic"] == sourceTopic {
			found = true
		}
		if _, ok := set["tenant_id"]; ok {
			t.Errorf("tenant_id label MUST NOT appear on streaming_dlq_publish_failed_total; got %v", set)
		}
	}
	if !found {
		t.Errorf("no (topic=%q) datapoint in %v", sourceTopic, attrSets)
	}
}

// TestMetrics_OutboxRouted_RecordsCounter: with an outbox wired and the
// circuit forced OPEN, Emit routes to the outbox and increments
// streaming_outbox_routed_total with reason="circuit_open".
func TestMetrics_OutboxRouted_RecordsCounter(t *testing.T) {
	cfg, _ := kfakeConfig(t)
	factory, snapshot := newManualMeterSetup(t)

	repo := &fakeOutboxRepo{}

	emitter, err := New(context.Background(), cfg,
		WithLogger(log.NewNop()),
		WithMetricsFactory(factory),
		WithOutboxRepository(repo),
	)
	if err != nil {
		t.Fatalf("New err = %v", err)
	}
	t.Cleanup(func() { _ = emitter.Close() })

	p := asProducer(t, emitter)

	// Force the mirrored breaker flag to OPEN directly; the outbox fallback
	// path observes this flag before the CB.Execute closure fires. Same
	// technique as producer_cb_test.go.
	p.cbStateFlag.Store(flagCBOpen)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := emitter.Emit(ctx, sampleEvent()); err != nil {
		t.Fatalf("Emit err = %v; want nil (outbox absorbs circuit_open)", err)
	}

	repo.mu.Lock()
	createdCount := len(repo.created)
	repo.mu.Unlock()

	if createdCount != 1 {
		t.Fatalf("outbox create count = %d; want 1", createdCount)
	}

	rm := snapshot()

	m, ok := findMetric(rm, metricNameOutboxRouted)
	if !ok {
		t.Fatalf("metric %q not found", metricNameOutboxRouted)
	}

	total, attrSets := sumInt64DataPoints(t, m)
	if total != 1 {
		t.Errorf("streaming_outbox_routed_total = %d; want 1", total)
	}

	if len(attrSets) != 1 {
		t.Fatalf("attrSets len = %d; want 1", len(attrSets))
	}

	if got := attrSets[0]["reason"]; got != "circuit_open" {
		t.Errorf("reason label = %q; want %q", got, "circuit_open")
	}

	// Also verify the corresponding emitted counter has outcome=outboxed.
	em, _ := findMetric(rm, metricNameEmitted)
	_, emSets := sumInt64DataPoints(t, em)

	hasOutboxed := false
	for _, set := range emSets {
		if set["outcome"] == outcomeOutboxed {
			hasOutboxed = true
			break
		}
	}
	if !hasOutboxed {
		t.Errorf("emitted_total: no outcome=%q datapoint in %v", outcomeOutboxed, emSets)
	}
}

// TestMetrics_CircuitOpen_NoOutbox_RecordsCircuitOpenOutcome: without an
// outbox, forcing the breaker OPEN makes Emit return ErrCircuitOpen and
// the emitted counter records outcome=circuit_open.
func TestMetrics_CircuitOpen_NoOutbox_RecordsCircuitOpenOutcome(t *testing.T) {
	cfg, _ := kfakeConfig(t)
	factory, snapshot := newManualMeterSetup(t)

	emitter, err := New(context.Background(), cfg,
		WithLogger(log.NewNop()),
		WithMetricsFactory(factory),
	)
	if err != nil {
		t.Fatalf("New err = %v", err)
	}
	t.Cleanup(func() { _ = emitter.Close() })

	p := asProducer(t, emitter)
	p.cbStateFlag.Store(flagCBOpen)

	err = emitter.Emit(context.Background(), sampleEvent())
	if !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("Emit err = %v; want ErrCircuitOpen", err)
	}

	rm := snapshot()

	m, _ := findMetric(rm, metricNameEmitted)
	_, attrSets := sumInt64DataPoints(t, m)

	hasCircuitOpen := false
	for _, set := range attrSets {
		if set["outcome"] == outcomeCircuitOpen {
			hasCircuitOpen = true
			break
		}
	}

	if !hasCircuitOpen {
		t.Errorf("no outcome=%q datapoint in %v", outcomeCircuitOpen, attrSets)
	}
}

// TestMetrics_CircuitState_UpdatedByListener: drive the state-change listener
// directly and verify the gauge reflects each transition.
func TestMetrics_CircuitState_UpdatedByListener(t *testing.T) {
	cfg, _ := kfakeConfig(t)
	factory, snapshot := newManualMeterSetup(t)

	emitter, err := New(context.Background(), cfg,
		WithLogger(log.NewNop()),
		WithMetricsFactory(factory),
	)
	if err != nil {
		t.Fatalf("New err = %v", err)
	}
	t.Cleanup(func() { _ = emitter.Close() })

	p := asProducer(t, emitter)
	listener := &streamingStateListener{producer: p}

	ctx := context.Background()

	// Closed → Open.
	listener.OnStateChange(ctx, p.cbServiceName, circuitbreaker.StateClosed, circuitbreaker.StateOpen)

	rm := snapshot()

	m, ok := findMetric(rm, metricNameCircuitState)
	if !ok {
		t.Fatalf("metric %q not found", metricNameCircuitState)
	}

	vals := gaugeInt64Values(t, m)
	if len(vals) == 0 {
		t.Fatalf("no gauge datapoints recorded")
	}
	if got := vals[0]; got != int64(flagCBOpen) {
		t.Errorf("gauge value = %d; want %d (open)", got, flagCBOpen)
	}

	// Transition through half-open → closed and verify gauge follows the
	// latest recorded value.
	listener.OnStateChange(ctx, p.cbServiceName, circuitbreaker.StateOpen, circuitbreaker.StateHalfOpen)
	listener.OnStateChange(ctx, p.cbServiceName, circuitbreaker.StateHalfOpen, circuitbreaker.StateClosed)

	rm = snapshot()
	m, _ = findMetric(rm, metricNameCircuitState)
	vals = gaugeInt64Values(t, m)
	if len(vals) == 0 {
		t.Fatalf("no gauge datapoints after transitions")
	}
	if got := vals[0]; got != int64(flagCBClosed) {
		t.Errorf("gauge value after closed = %d; want %d", got, flagCBClosed)
	}
}

// TestMetrics_CircuitState_UnknownStateDoesNotRecord: an unknown CB state
// must not write to the gauge — corrupting the 0/1/2 semantics with e.g.
// -1 would break downstream alert rules.
func TestMetrics_CircuitState_UnknownStateDoesNotRecord(t *testing.T) {
	cfg, _ := kfakeConfig(t)
	factory, snapshot := newManualMeterSetup(t)

	emitter, err := New(context.Background(), cfg,
		WithLogger(log.NewNop()),
		WithMetricsFactory(factory),
	)
	if err != nil {
		t.Fatalf("New err = %v", err)
	}
	t.Cleanup(func() { _ = emitter.Close() })

	p := asProducer(t, emitter)
	listener := &streamingStateListener{producer: p}

	listener.OnStateChange(context.Background(), p.cbServiceName,
		circuitbreaker.StateClosed, circuitbreaker.StateUnknown)

	rm := snapshot()

	// The gauge may or may not be registered; if it is, the value count
	// must be zero.
	if m, ok := findMetric(rm, metricNameCircuitState); ok {
		vals := gaugeInt64Values(t, m)
		if len(vals) > 0 {
			t.Errorf("unknown state wrote gauge values %v; want none", vals)
		}
	}
}

// --- DX-D02 automated cardinality test: no tenant_id on any metric label. ---

// TestMetrics_NoTenantIDLabel_10kEmits emits 10k events with distinct
// synthetic tenant IDs. After Collect, no metric attribute set may contain
// a key of "tenant_id" (or "tenant").
//
// Load-bearing DX-D02 gate. A regression would explode Prometheus/Mimir
// cardinality: 10k unique tenants × every label combination would blow
// past any reasonable storage budget.
//
// 10k matches the AC-06 / DX-D02 target; any leak would surface well
// within the first 100 emits. The large iteration count is intentional —
// a smaller count would miss a leak that only manifests once a
// label-cache resize crosses a threshold.
//
// Runtime: ~10-30s on typical CI. Skipped under -short.
func TestMetrics_NoTenantIDLabel_10kEmits(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 10k cardinality test in -short mode")
	}

	cfg, _ := kfakeConfig(t)
	factory, snapshot := newManualMeterSetup(t)

	emitter, err := New(context.Background(), cfg,
		WithLogger(log.NewNop()),
		WithMetricsFactory(factory),
	)
	if err != nil {
		t.Fatalf("New err = %v", err)
	}
	t.Cleanup(func() { _ = emitter.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const emits = 10_000

	for i := 0; i < emits; i++ {
		ev := sampleEvent()
		ev.TenantID = fmt.Sprintf("tenant-%06d", i)
		// Inline a distinct payload per emit so json.Valid still passes but
		// there's no payload-side collapse through caching.
		ev.Payload = json.RawMessage(fmt.Sprintf(`{"i":%d}`, i))

		if err := emitter.Emit(ctx, ev); err != nil {
			t.Fatalf("Emit[%d] err = %v", i, err)
		}
	}

	rm := snapshot()

	forbidden := map[string]struct{}{
		"tenant_id": {},
		"tenant":    {},
	}

	var violations []string

	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			attrSets := extractAttributeSets(m)
			for _, set := range attrSets {
				for key := range set {
					if _, bad := forbidden[key]; bad {
						violations = append(violations, fmt.Sprintf("metric=%s key=%s", m.Name, key))
					}
				}
			}
		}
	}

	if len(violations) > 0 {
		t.Fatalf("DX-D02 violation: forbidden tenant keys found on metrics:\n  %s",
			strings.Join(violations, "\n  "))
	}
}
