//go:build unit

package mongo

import (
	"testing"
	"time"

	"github.com/LerianStudio/lib-commons/v7/commons/outbox"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
)

const testTraceparent = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"

func TestDocument_ToBSON_TraceContext(t *testing.T) {
	t.Parallel()

	t.Run("writes the carrier when present", func(t *testing.T) {
		t.Parallel()

		doc := document{
			ID:           uuid.NewString(),
			TraceContext: map[string]string{outbox.TraceContextTraceparent: testTraceparent},
		}

		raw := doc.toBSON(defaultTenantField)
		require.Equal(
			t,
			map[string]string{outbox.TraceContextTraceparent: testTraceparent},
			raw[mongoFieldTraceContext],
		)
	})

	t.Run("omits the field when absent", func(t *testing.T) {
		t.Parallel()

		raw := document{ID: uuid.NewString()}.toBSON(defaultTenantField)
		require.NotContains(t, raw, mongoFieldTraceContext)
	})
}

func TestDocumentFromBSON_TraceContext(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()

	base := func() bson.M {
		return bson.M{
			"id":                uuid.NewString(),
			"event_type":        "payment.created",
			"aggregate_id":      uuid.NewString(),
			"payload":           `{"ok":true}`,
			mongoFieldStatus:    outbox.OutboxStatusPending,
			mongoFieldAttempts:  0,
			mongoFieldCreatedAt: now,
			mongoFieldUpdatedAt: now,
			"custom_tenant":     "tenant-a",
		}
	}

	t.Run("decodes a bson sub-document carrier", func(t *testing.T) {
		t.Parallel()

		raw := base()
		raw[mongoFieldTraceContext] = bson.M{
			outbox.TraceContextTraceparent: testTraceparent,
			"baggage":                      "user=root",
		}

		doc, err := documentFromBSON(raw, "custom_tenant")
		require.NoError(t, err)
		require.Equal(
			t,
			map[string]string{outbox.TraceContextTraceparent: testTraceparent},
			doc.TraceContext,
		)

		event, err := doc.toOutboxEvent()
		require.NoError(t, err)
		require.Equal(
			t,
			map[string]string{outbox.TraceContextTraceparent: testTraceparent},
			event.TraceContext,
		)
	})

	t.Run("absent carrier decodes to nil", func(t *testing.T) {
		t.Parallel()

		doc, err := documentFromBSON(base(), "custom_tenant")
		require.NoError(t, err)
		require.Nil(t, doc.TraceContext)
	})

	t.Run("unreadable carrier decodes to nil, never an error", func(t *testing.T) {
		t.Parallel()

		raw := base()
		raw[mongoFieldTraceContext] = bson.M{outbox.TraceContextTraceparent: 42}

		doc, err := documentFromBSON(raw, "custom_tenant")
		require.NoError(t, err)
		require.Nil(t, doc.TraceContext)
	})

	t.Run("carrier of an unexpected shape decodes to nil", func(t *testing.T) {
		t.Parallel()

		raw := base()
		raw[mongoFieldTraceContext] = "traceparent=x"

		doc, err := documentFromBSON(raw, "custom_tenant")
		require.NoError(t, err)
		require.Nil(t, doc.TraceContext)
	})
}

func TestNormalizedCreateValues_CarriesTraceContext(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	event := &outbox.OutboxEvent{
		ID:          uuid.New(),
		EventType:   "payment.created",
		AggregateID: uuid.New(),
		Payload:     []byte(`{"ok":true}`),
		TraceContext: map[string]string{
			outbox.TraceContextTraceparent: testTraceparent,
			"baggage":                      "user=root",
		},
	}

	values := normalizedCreateValues(event, now)
	require.Equal(
		t,
		map[string]string{outbox.TraceContextTraceparent: testTraceparent},
		values.traceContext,
	)

	doc := documentFromCreateValues(values, "tenant-a")
	require.Equal(
		t,
		map[string]string{outbox.TraceContextTraceparent: testTraceparent},
		doc.TraceContext,
	)
}
