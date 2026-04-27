//go:build unit

package mongo

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	libMongo "github.com/LerianStudio/lib-commons/v5/commons/mongo"
	"github.com/LerianStudio/lib-commons/v5/commons/outbox"
	tmcore "github.com/LerianStudio/lib-commons/v5/commons/tenant-manager/core"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
)

func TestNewRepository_Validation(t *testing.T) {
	t.Parallel()

	repo, err := NewRepository(nil)
	require.Nil(t, repo)
	require.ErrorIs(t, err, ErrConnectionRequired)

	repo, err = NewRepository(&libMongo.Client{}, WithCollectionName("bad-name"))
	require.Nil(t, repo)
	require.ErrorIs(t, err, ErrInvalidIdentifier)

	repo, err = NewRepository(&libMongo.Client{}, WithTenantField("bad-name"))
	require.Nil(t, repo)
	require.ErrorIs(t, err, ErrInvalidIdentifier)

	opt := WithModule(" payments ")
	repo = &Repository{}
	opt(repo)
	require.Equal(t, " payments ", repo.tenantModule)
}

func TestRepository_CreateWithTxUnsupported(t *testing.T) {
	t.Parallel()

	repo := &Repository{}
	_, err := repo.CreateWithTx(context.Background(), &sql.Tx{}, nil)
	require.ErrorIs(t, err, ErrTransactionUnsupported)
}

func TestRepository_TenantIDFromContext(t *testing.T) {
	t.Parallel()

	repo := &Repository{requireTenant: true, tenantField: defaultTenantField}

	_, err := repo.tenantIDFromContext(context.Background())
	require.ErrorIs(t, err, outbox.ErrTenantIDRequired)

	ctx := outbox.ContextWithTenantID(context.Background(), "tenant-a")
	tenantID, err := repo.tenantIDFromContext(ctx)
	require.NoError(t, err)
	require.Equal(t, "tenant-a", tenantID)

	ctx = tmcore.ContextWithTenantID(context.Background(), "_invalid")
	_, err = repo.tenantIDFromContext(ctx)
	require.ErrorIs(t, err, outbox.ErrInvalidTenantID)

	repo.requireTenant = false
	tenantID, err = repo.tenantIDFromContext(context.Background())
	require.NoError(t, err)
	require.Equal(t, defaultScopeTenantID, tenantID)
}

func TestValidateCreateEvent(t *testing.T) {
	t.Parallel()

	valid := &outbox.OutboxEvent{ID: uuid.New(), EventType: "payment.created", AggregateID: uuid.New(), Payload: []byte(`{"ok":true}`)}
	require.NoError(t, validateCreateEvent(valid))

	tests := []struct {
		name  string
		event *outbox.OutboxEvent
		err   error
	}{
		{name: "nil event", event: nil, err: outbox.ErrOutboxEventRequired},
		{name: "nil id", event: &outbox.OutboxEvent{EventType: "payment.created", AggregateID: uuid.New(), Payload: []byte(`{"ok":true}`)}, err: ErrIDRequired},
		{name: "empty event type", event: &outbox.OutboxEvent{ID: uuid.New(), AggregateID: uuid.New(), Payload: []byte(`{"ok":true}`)}, err: ErrEventTypeRequired},
		{name: "nil aggregate", event: &outbox.OutboxEvent{ID: uuid.New(), EventType: "payment.created", Payload: []byte(`{"ok":true}`)}, err: ErrAggregateIDRequired},
		{name: "empty payload", event: &outbox.OutboxEvent{ID: uuid.New(), EventType: "payment.created", AggregateID: uuid.New()}, err: outbox.ErrOutboxEventPayloadRequired},
		{name: "payload too large", event: &outbox.OutboxEvent{ID: uuid.New(), EventType: "payment.created", AggregateID: uuid.New(), Payload: []byte(strings.Repeat("x", outbox.DefaultMaxPayloadBytes+1))}, err: outbox.ErrOutboxEventPayloadTooLarge},
		{name: "payload not json", event: &outbox.OutboxEvent{ID: uuid.New(), EventType: "payment.created", AggregateID: uuid.New(), Payload: []byte(`not-json`)}, err: outbox.ErrOutboxEventPayloadNotJSON},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.ErrorIs(t, validateCreateEvent(tt.event), tt.err)
		})
	}
}

func TestNormalizedCreateValuesForcesLifecycleInvariants(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	publishedAt := now.Add(-time.Minute)
	event := &outbox.OutboxEvent{
		ID:          uuid.New(),
		EventType:   "  payment.created  ",
		AggregateID: uuid.New(),
		Payload:     []byte(`{"ok":true}`),
		Status:      outbox.OutboxStatusPublished,
		Attempts:    10,
		PublishedAt: &publishedAt,
		LastError:   "should clear",
		CreatedAt:   now,
		UpdatedAt:   now.Add(-time.Hour),
	}

	values := normalizedCreateValues(event, now.Add(time.Minute))
	require.Equal(t, outbox.OutboxStatusPending, values.status)
	require.Equal(t, 0, values.attempts)
	require.Nil(t, values.publishedAt)
	require.Empty(t, values.lastError)
	require.False(t, values.updatedAt.Before(values.createdAt))
	require.Equal(t, "payment.created", values.eventType)
}

func TestDocumentFromBSONValidatesStatusAndAttempts(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	raw := bsonM(uuid.NewString(), uuid.NewString(), outbox.OutboxStatusPending, 0, now)
	_, err := documentFromBSON(raw, defaultTenantField)
	require.NoError(t, err)

	raw["status"] = "BROKEN"
	_, err = documentFromBSON(raw, defaultTenantField)
	require.ErrorIs(t, err, outbox.ErrOutboxStatusInvalid)

	raw = bsonM(uuid.NewString(), uuid.NewString(), outbox.OutboxStatusPending, -1, now)
	_, err = documentFromBSON(raw, defaultTenantField)
	require.Error(t, err)

	raw = bsonM(uuid.NewString(), uuid.NewString(), outbox.OutboxStatusPending, 1, now)
	raw["attempts"] = 1.9
	_, err = documentFromBSON(raw, defaultTenantField)
	require.Error(t, err)
}

func bsonM(id string, aggregateID string, status string, attempts int, now time.Time) bson.M {
	return bson.M{
		"id":               id,
		"event_type":       "payment.created",
		"aggregate_id":     aggregateID,
		"payload":          `{"ok":true}`,
		"status":           status,
		"attempts":         attempts,
		"created_at":       now,
		"updated_at":       now,
		defaultTenantField: "tenant-a",
	}
}

func TestDocumentRoundTrip_CustomTenantField(t *testing.T) {
	t.Parallel()

	publishedAt := time.Now().UTC().Truncate(time.Millisecond)
	doc := document{
		ID:          uuid.NewString(),
		EventType:   "payment.created",
		AggregateID: uuid.NewString(),
		Payload:     `{"ok":true}`,
		Status:      outbox.OutboxStatusPending,
		Attempts:    2,
		PublishedAt: &publishedAt,
		LastError:   "failure",
		CreatedAt:   publishedAt,
		UpdatedAt:   publishedAt,
		TenantID:    "tenant-a",
	}

	raw := doc.toBSON("scope")
	decoded, err := documentFromBSON(raw, "scope")
	require.NoError(t, err)
	require.Equal(t, doc, decoded)
}
