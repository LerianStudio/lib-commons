//go:build integration

package rabbitmq

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	libOtel "github.com/LerianStudio/lib-commons/v5/commons/opentelemetry"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// containsKeyInsensitive checks if a string-keyed map contains a key (case-insensitive).
// The W3C TraceContext propagator uses http.Header which canonicalizes keys to Pascal-case.
func containsKeyInsensitive[V any](m map[string]V, key string) bool {
	lower := strings.ToLower(key)
	for k := range m {
		if strings.ToLower(k) == lower {
			return true
		}
	}

	return false
}

// getValueInsensitive retrieves a string value by case-insensitive key lookup.
func getValueInsensitive(m map[string]any, key string) (any, bool) {
	lower := strings.ToLower(key)
	for k, v := range m {
		if strings.ToLower(k) == lower {
			return v, true
		}
	}

	return nil, false
}

// mapKeys returns the keys of a string-keyed map for diagnostic messages.
func mapKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	return keys
}

// saveAndRestoreOTELGlobals saves the current global tracer provider and propagator,
// returning a restore function that resets them. Every test that configures OTEL
// globals MUST defer this to avoid polluting sibling tests.
func saveAndRestoreOTELGlobals(t *testing.T) func() {
	t.Helper()

	prevTP := otel.GetTracerProvider()
	prevProp := otel.GetTextMapPropagator()

	return func() {
		otel.SetTracerProvider(prevTP)
		otel.SetTextMapPropagator(prevProp)
	}
}

// setupTestTracer creates a real SDK tracer provider and configures OTEL globals.
// It returns a tracer and a cleanup function that shuts down the provider.
func setupTestTracer(t *testing.T) (trace.Tracer, func()) {
	t.Helper()

	tp := sdktrace.NewTracerProvider()
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(
		propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}),
	)

	tracer := tp.Tracer("trace-propagation-integration-test")

	return tracer, func() {
		_ = tp.Shutdown(context.Background())
	}
}

// declareTestQueue declares an auto-delete queue with a unique name for test isolation.
func declareTestQueue(t *testing.T, ch *amqp.Channel, prefix string) amqp.Queue {
	t.Helper()

	queueName := fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())

	q, err := ch.QueueDeclare(
		queueName,
		false, // durable
		true,  // autoDelete
		false, // exclusive
		false, // noWait
		nil,   // args
	)
	require.NoError(t, err, "QueueDeclare should succeed")

	return q
}

// consumeOne reads exactly one message from a queue within the test deadline.
func consumeOne(t *testing.T, ch *amqp.Channel, queueName string) amqp.Delivery {
	t.Helper()

	msgs, err := ch.Consume(
		queueName,
		"",    // consumer tag (auto-generated)
		true,  // autoAck
		false, // exclusive
		false, // noLocal
		false, // noWait
		nil,   // args
	)
	require.NoError(t, err, "Consume should succeed")

	ctx, cancel := context.WithTimeout(context.Background(), testConsumeDeadline)
	defer cancel()

	select {
	case msg, ok := <-msgs:
		require.True(t, ok, "message channel should deliver a message")
		return msg
	case <-ctx.Done():
		t.Fatal("timed out waiting for message from RabbitMQ")
		return amqp.Delivery{} // unreachable but satisfies compiler
	}
}

// consumeN reads exactly n messages from a queue within the test deadline.
func consumeN(t *testing.T, ch *amqp.Channel, queueName string, n int) []amqp.Delivery {
	t.Helper()

	msgs, err := ch.Consume(
		queueName,
		"",    // consumer tag (auto-generated)
		true,  // autoAck
		false, // exclusive
		false, // noLocal
		false, // noWait
		nil,   // args
	)
	require.NoError(t, err, "Consume should succeed")

	ctx, cancel := context.WithTimeout(context.Background(), testConsumeDeadline)
	defer cancel()

	deliveries := make([]amqp.Delivery, 0, n)

	for range n {
		select {
		case msg, ok := <-msgs:
			require.True(t, ok, "message channel should deliver a message")
			deliveries = append(deliveries, msg)
		case <-ctx.Done():
			t.Fatalf("timed out waiting for message %d/%d from RabbitMQ", len(deliveries)+1, n)
		}
	}

	return deliveries
}

func TestIntegration_TraceContext_SurvivesPublishConsume(t *testing.T) {
	// — Setup —
	restoreGlobals := saveAndRestoreOTELGlobals(t)
	defer restoreGlobals()

	tracer, shutdownTP := setupTestTracer(t)
	defer shutdownTP()

	amqpURL, mgmtURL, cleanup := setupRabbitMQContainer(t)
	defer cleanup()

	rc := newTestConnection(amqpURL, mgmtURL)

	ctx := context.Background()

	err := rc.ConnectContext(ctx)
	require.NoError(t, err, "ConnectContext should succeed")

	defer func() { _ = rc.CloseContext(ctx) }()

	ch, err := rc.GetNewConnectContext(ctx)
	require.NoError(t, err, "GetNewConnectContext should succeed")

	q := declareTestQueue(t, ch, "trace-survive")

	// — Produce: create a real span and inject its trace context into AMQP headers —
	spanCtx, span := tracer.Start(ctx, "test-publish-operation")
	originalTraceID := span.SpanContext().TraceID().String()
	require.NotEmpty(t, originalTraceID, "span should have a valid trace ID")

	traceHeaders := libOtel.InjectQueueTraceContext(spanCtx)
	// The W3C propagator uses http.Header which canonicalizes keys to Pascal-case ("Traceparent").
	require.True(t, containsKeyInsensitive(traceHeaders, "traceparent"),
		"trace injection should produce a traceparent header, got keys: %v", mapKeys(traceHeaders))

	amqpHeaders := amqp.Table{}
	for k, v := range traceHeaders {
		amqpHeaders[k] = v
	}

	publishCtx, publishCancel := context.WithTimeout(ctx, 5*time.Second)
	defer publishCancel()

	err = ch.PublishWithContext(
		publishCtx,
		"",     // default exchange
		q.Name, // routing key
		false,  // mandatory
		false,  // immediate
		amqp.Publishing{
			ContentType: "application/json",
			Body:        []byte(`{"test":"trace_survives"}`),
			Headers:     amqpHeaders,
		},
	)
	require.NoError(t, err, "PublishWithContext should succeed")

	span.End()

	// — Consume: extract trace context from the received message —
	msg := consumeOne(t, ch, q.Name)

	require.NotNil(t, msg.Headers, "consumed message should have headers")

	// amqp.Table is map[string]interface{} — pass directly to ExtractTraceContextFromQueueHeaders.
	extractedCtx := libOtel.ExtractTraceContextFromQueueHeaders(context.Background(), map[string]any(msg.Headers))
	extractedTraceID := libOtel.GetTraceIDFromContext(extractedCtx)

	assert.Equal(t, originalTraceID, extractedTraceID,
		"trace ID extracted from consumed message must match the producer's trace ID")
}

func TestIntegration_TraceContext_PrepareQueueHeaders(t *testing.T) {
	// — Setup —
	restoreGlobals := saveAndRestoreOTELGlobals(t)
	defer restoreGlobals()

	tracer, shutdownTP := setupTestTracer(t)
	defer shutdownTP()

	amqpURL, mgmtURL, cleanup := setupRabbitMQContainer(t)
	defer cleanup()

	rc := newTestConnection(amqpURL, mgmtURL)

	ctx := context.Background()

	err := rc.ConnectContext(ctx)
	require.NoError(t, err, "ConnectContext should succeed")

	defer func() { _ = rc.CloseContext(ctx) }()

	ch, err := rc.GetNewConnectContext(ctx)
	require.NoError(t, err, "GetNewConnectContext should succeed")

	q := declareTestQueue(t, ch, "trace-prepare")

	// — Build headers via PrepareQueueHeaders —
	spanCtx, span := tracer.Start(ctx, "test-prepare-headers-operation")
	defer span.End()

	baseHeaders := map[string]any{
		"correlation_id": "abc",
	}

	merged := libOtel.PrepareQueueHeaders(spanCtx, baseHeaders)

	// Verify merge semantics: both base and trace keys must be present.
	assert.Contains(t, merged, "correlation_id", "merged headers should preserve base header correlation_id")
	assert.Equal(t, "abc", merged["correlation_id"], "correlation_id value should be unchanged")
	assert.True(t, containsKeyInsensitive(merged, "traceparent"),
		"merged headers should contain injected traceparent, got keys: %v", mapKeys(merged))

	// Original baseHeaders must be unmodified (PrepareQueueHeaders creates a new map).
	assert.False(t, containsKeyInsensitive(baseHeaders, "traceparent"),
		"PrepareQueueHeaders should not mutate the original baseHeaders map")

	// — Publish with merged headers and verify on the consumer side —
	amqpHeaders := amqp.Table{}
	for k, v := range merged {
		amqpHeaders[k] = v
	}

	publishCtx, publishCancel := context.WithTimeout(ctx, 5*time.Second)
	defer publishCancel()

	err = ch.PublishWithContext(
		publishCtx,
		"",     // default exchange
		q.Name, // routing key
		false,  // mandatory
		false,  // immediate
		amqp.Publishing{
			ContentType: "application/json",
			Body:        []byte(`{"test":"prepare_headers"}`),
			Headers:     amqpHeaders,
		},
	)
	require.NoError(t, err, "PublishWithContext should succeed")

	msg := consumeOne(t, ch, q.Name)

	require.NotNil(t, msg.Headers, "consumed message should have headers")
	assert.True(t, containsKeyInsensitive(map[string]any(msg.Headers), "traceparent"),
		"consumed message headers should include traceparent from PrepareQueueHeaders")
	assert.True(t, containsKeyInsensitive(map[string]any(msg.Headers), "correlation_id"),
		"consumed message headers should include correlation_id from base headers")
}

func TestIntegration_TraceContext_NoTraceContext(t *testing.T) {
	// — Setup —
	restoreGlobals := saveAndRestoreOTELGlobals(t)
	defer restoreGlobals()

	// Deliberately set a real propagator so extraction doesn't panic,
	// but do NOT create a span — the context carries no trace.
	tp := sdktrace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()

	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(
		propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}),
	)

	amqpURL, mgmtURL, cleanup := setupRabbitMQContainer(t)
	defer cleanup()

	rc := newTestConnection(amqpURL, mgmtURL)

	ctx := context.Background()

	err := rc.ConnectContext(ctx)
	require.NoError(t, err, "ConnectContext should succeed")

	defer func() { _ = rc.CloseContext(ctx) }()

	ch, err := rc.GetNewConnectContext(ctx)
	require.NoError(t, err, "GetNewConnectContext should succeed")

	q := declareTestQueue(t, ch, "trace-none")

	// — Publish WITHOUT any trace headers —
	publishCtx, publishCancel := context.WithTimeout(ctx, 5*time.Second)
	defer publishCancel()

	err = ch.PublishWithContext(
		publishCtx,
		"",     // default exchange
		q.Name, // routing key
		false,  // mandatory
		false,  // immediate
		amqp.Publishing{
			ContentType: "application/json",
			Body:        []byte(`{"test":"no_trace"}`),
			// No Headers field — nil headers.
		},
	)
	require.NoError(t, err, "PublishWithContext should succeed")

	msg := consumeOne(t, ch, q.Name)

	// — Extract from nil headers — must not panic and should return empty trace ID —
	extractedFromNil := libOtel.ExtractTraceContextFromQueueHeaders(context.Background(), nil)
	traceIDFromNil := libOtel.GetTraceIDFromContext(extractedFromNil)
	assert.Empty(t, traceIDFromNil,
		"extracting from nil headers should yield an empty/invalid trace ID")

	// — Extract from empty headers map — same graceful degradation —
	extractedFromEmpty := libOtel.ExtractTraceContextFromQueueHeaders(context.Background(), map[string]any{})
	traceIDFromEmpty := libOtel.GetTraceIDFromContext(extractedFromEmpty)
	assert.Empty(t, traceIDFromEmpty,
		"extracting from empty headers should yield an empty/invalid trace ID")

	// — Extract from the actual consumed message (which has nil or empty headers) —
	consumedHeaders := map[string]any(msg.Headers) // amqp.Table -> map[string]any; may be nil
	extractedFromMsg := libOtel.ExtractTraceContextFromQueueHeaders(context.Background(), consumedHeaders)
	traceIDFromMsg := libOtel.GetTraceIDFromContext(extractedFromMsg)
	assert.Empty(t, traceIDFromMsg,
		"extracting from message published without trace headers should yield an empty trace ID")
}

func TestIntegration_TraceContext_MultipleMessages(t *testing.T) {
	// — Setup —
	restoreGlobals := saveAndRestoreOTELGlobals(t)
	defer restoreGlobals()

	tracer, shutdownTP := setupTestTracer(t)
	defer shutdownTP()

	amqpURL, mgmtURL, cleanup := setupRabbitMQContainer(t)
	defer cleanup()

	rc := newTestConnection(amqpURL, mgmtURL)

	ctx := context.Background()

	err := rc.ConnectContext(ctx)
	require.NoError(t, err, "ConnectContext should succeed")

	defer func() { _ = rc.CloseContext(ctx) }()

	ch, err := rc.GetNewConnectContext(ctx)
	require.NoError(t, err, "GetNewConnectContext should succeed")

	q := declareTestQueue(t, ch, "trace-multi")

	const messageCount = 3

	// — Publish 3 messages, each under a distinct span (= distinct trace ID) —
	publishedTraceIDs := make([]string, 0, messageCount)

	for i := range messageCount {
		spanCtx, span := tracer.Start(ctx, fmt.Sprintf("test-multi-operation-%d", i))
		traceID := span.SpanContext().TraceID().String()
		require.NotEmpty(t, traceID, "span %d should have a valid trace ID", i)

		publishedTraceIDs = append(publishedTraceIDs, traceID)

		traceHeaders := libOtel.InjectQueueTraceContext(spanCtx)

		amqpHeaders := amqp.Table{}
		for k, v := range traceHeaders {
			amqpHeaders[k] = v
		}

		publishCtx, publishCancel := context.WithTimeout(ctx, 5*time.Second)

		err = ch.PublishWithContext(
			publishCtx,
			"",     // default exchange
			q.Name, // routing key
			false,  // mandatory
			false,  // immediate
			amqp.Publishing{
				ContentType: "application/json",
				Body:        []byte(fmt.Sprintf(`{"msg":%d}`, i)),
				Headers:     amqpHeaders,
			},
		)
		require.NoError(t, err, "PublishWithContext for message %d should succeed", i)

		publishCancel()

		span.End()
	}

	// Sanity check: all 3 published trace IDs must be unique.
	uniqueIDs := make(map[string]struct{}, messageCount)
	for _, id := range publishedTraceIDs {
		uniqueIDs[id] = struct{}{}
	}

	require.Len(t, uniqueIDs, messageCount,
		"each published message must carry a unique trace ID")

	// — Consume all 3 and verify each extracts its own trace ID —
	deliveries := consumeN(t, ch, q.Name, messageCount)

	extractedTraceIDs := make([]string, 0, messageCount)

	for i, msg := range deliveries {
		require.NotNil(t, msg.Headers, "consumed message %d should have headers", i)

		extractedCtx := libOtel.ExtractTraceContextFromQueueHeaders(
			context.Background(), map[string]any(msg.Headers),
		)
		extractedID := libOtel.GetTraceIDFromContext(extractedCtx)
		require.NotEmpty(t, extractedID, "consumed message %d should yield a valid trace ID", i)

		extractedTraceIDs = append(extractedTraceIDs, extractedID)
	}

	// AMQP guarantees FIFO ordering on a single queue with a single publisher,
	// so extracted order matches published order.
	assert.Equal(t, publishedTraceIDs, extractedTraceIDs,
		"extracted trace IDs must match published trace IDs in order")
}
