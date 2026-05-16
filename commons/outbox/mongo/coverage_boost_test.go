//go:build unit

package mongo

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// -------------------------------------------------------------------
// stringField — missing branch (type mismatch)
// -------------------------------------------------------------------

func TestStringField_MissingKey(t *testing.T) {
	t.Parallel()

	raw := bson.M{"other": "value"}
	_, err := stringField(raw, "missing_key")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing field")
}

func TestStringField_WrongType(t *testing.T) {
	t.Parallel()

	raw := bson.M{"count": 42}
	_, err := stringField(raw, "count")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be string")
}

func TestStringField_ValidString(t *testing.T) {
	t.Parallel()

	raw := bson.M{"name": "hello"}
	val, err := stringField(raw, "name")
	require.NoError(t, err)
	assert.Equal(t, "hello", val)
}

// -------------------------------------------------------------------
// optionalStringField — type mismatch branch
// -------------------------------------------------------------------

func TestOptionalStringField_MissingKey(t *testing.T) {
	t.Parallel()

	raw := bson.M{}
	val, err := optionalStringField(raw, "missing")
	require.NoError(t, err)
	assert.Equal(t, "", val)
}

func TestOptionalStringField_NilValue(t *testing.T) {
	t.Parallel()

	raw := bson.M{"key": nil}
	val, err := optionalStringField(raw, "key")
	require.NoError(t, err)
	assert.Equal(t, "", val)
}

func TestOptionalStringField_WrongType(t *testing.T) {
	t.Parallel()

	raw := bson.M{"count": 42}
	_, err := optionalStringField(raw, "count")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be string")
}

// -------------------------------------------------------------------
// intField — additional type branches
// -------------------------------------------------------------------

func TestIntField_MissingKey(t *testing.T) {
	t.Parallel()

	raw := bson.M{}
	_, err := intField(raw, "missing")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing field")
}

func TestIntField_Int32Type(t *testing.T) {
	t.Parallel()

	raw := bson.M{"count": int32(42)}
	val, err := intField(raw, "count")
	require.NoError(t, err)
	assert.Equal(t, 42, val)
}

func TestIntField_Int64Type(t *testing.T) {
	t.Parallel()

	raw := bson.M{"count": int64(99)}
	val, err := intField(raw, "count")
	require.NoError(t, err)
	assert.Equal(t, 99, val)
}

func TestIntField_WrongType(t *testing.T) {
	t.Parallel()

	raw := bson.M{"count": "not-a-number"}
	_, err := intField(raw, "count")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be integer")
}

// -------------------------------------------------------------------
// timeField — additional type branches
// -------------------------------------------------------------------

func TestTimeField_MissingKey(t *testing.T) {
	t.Parallel()

	raw := bson.M{}
	_, err := timeField(raw, "missing")
	require.Error(t, err)
}

func TestTimeField_PrimitiveDateTime(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC().Truncate(time.Millisecond)
	raw := bson.M{"ts": primitive.NewDateTimeFromTime(now)}
	val, err := timeField(raw, "ts")
	require.NoError(t, err)
	assert.WithinDuration(t, now, val, time.Millisecond)
}

func TestTimeField_WrongType(t *testing.T) {
	t.Parallel()

	raw := bson.M{"ts": "not-a-time"}
	_, err := timeField(raw, "ts")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be time")
}

// -------------------------------------------------------------------
// optionalTimeField — additional branches
// -------------------------------------------------------------------

func TestOptionalTimeField_MissingKey(t *testing.T) {
	t.Parallel()

	raw := bson.M{}
	val, err := optionalTimeField(raw, "missing")
	require.NoError(t, err)
	assert.Nil(t, val)
}

func TestOptionalTimeField_NilValue(t *testing.T) {
	t.Parallel()

	raw := bson.M{"ts": nil}
	val, err := optionalTimeField(raw, "ts")
	require.NoError(t, err)
	assert.Nil(t, val)
}

func TestOptionalTimeField_TimeValue(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	raw := bson.M{"ts": now}
	val, err := optionalTimeField(raw, "ts")
	require.NoError(t, err)
	require.NotNil(t, val)
	assert.WithinDuration(t, now, *val, time.Millisecond)
}

func TestOptionalTimeField_PrimitiveDateTime(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC().Truncate(time.Millisecond)
	raw := bson.M{"ts": primitive.NewDateTimeFromTime(now)}
	val, err := optionalTimeField(raw, "ts")
	require.NoError(t, err)
	require.NotNil(t, val)
	assert.WithinDuration(t, now, *val, time.Millisecond)
}

func TestOptionalTimeField_WrongType(t *testing.T) {
	t.Parallel()

	raw := bson.M{"ts": "not-a-time"}
	_, err := optionalTimeField(raw, "ts")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be time")
}

// -------------------------------------------------------------------
// documentFromBSON — missing/invalid required fields
// -------------------------------------------------------------------

func TestDocumentFromBSON_MissingIDField(t *testing.T) {
	t.Parallel()

	raw := bson.M{} // No _id field
	_, err := documentFromBSON(raw, "tenant_id")
	require.Error(t, err)
}

func TestDocumentFromBSON_InvalidStatusType(t *testing.T) {
	t.Parallel()

	raw := bson.M{
		"_id":        "00000000-0000-0000-0000-000000000001",
		"status":     42, // wrong type
		"event_type": "test.event",
		"payload":    "{}",
		"tenant_id":  "tenant-1",
		"created_at": time.Now(),
		"updated_at": time.Now(),
	}
	_, err := documentFromBSON(raw, "tenant_id")
	require.Error(t, err)
}

// -------------------------------------------------------------------
// validateClaimDocument — branches
// -------------------------------------------------------------------

func TestValidateClaimDocument_EmptyID(t *testing.T) {
	t.Parallel()

	doc := document{
		ID:        "",
		Status:    "PENDING",
		Attempts:  0,
		UpdatedAt: time.Now(),
	}
	_, err := validateClaimDocument(doc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "id")
}

func TestValidateClaimDocument_InvalidStatus(t *testing.T) {
	t.Parallel()

	doc := document{
		ID:        "some-id",
		Status:    "invalid-status",
		Attempts:  0,
		UpdatedAt: time.Now(),
	}
	_, err := validateClaimDocument(doc)
	require.Error(t, err)
}

func TestValidateClaimDocument_NegativeAttempts(t *testing.T) {
	t.Parallel()

	doc := document{
		ID:        "some-id",
		Status:    "PENDING",
		Attempts:  -1,
		UpdatedAt: time.Now(),
	}
	_, err := validateClaimDocument(doc)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "attempts")
}

func TestValidateClaimDocument_ZeroUpdatedAt(t *testing.T) {
	t.Parallel()

	doc := document{
		ID:        "some-id",
		Status:    "PENDING",
		Attempts:  0,
		UpdatedAt: time.Time{},
	}
	_, err := validateClaimDocument(doc)
	require.Error(t, err)
	// Error should be about missing updated_at
	assert.Error(t, err)
}

// -------------------------------------------------------------------
// toOutboxEvent — invalid UUID paths
// -------------------------------------------------------------------

func TestToOutboxEvent_InvalidID(t *testing.T) {
	t.Parallel()

	doc := document{
		ID:          "not-a-uuid",
		AggregateID: "00000000-0000-0000-0000-000000000001",
		EventType:   "test.event",
		Status:      "PENDING",
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	_, err := doc.toOutboxEvent()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "id")
}

func TestToOutboxEvent_InvalidAggregateID(t *testing.T) {
	t.Parallel()

	doc := document{
		ID:          "00000000-0000-0000-0000-000000000001",
		AggregateID: "not-a-uuid",
		EventType:   "test.event",
		Status:      "PENDING",
		CreatedAt:   time.Now(),
		UpdatedAt:   time.Now(),
	}
	_, err := doc.toOutboxEvent()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "aggregate")
}

func TestToOutboxEvent_Valid(t *testing.T) {
	t.Parallel()

	now := time.Now()
	doc := document{
		ID:          "00000000-0000-0000-0000-000000000001",
		AggregateID: "00000000-0000-0000-0000-000000000002",
		EventType:   "test.event",
		Status:      "PENDING",
		Payload:     "{}",
		Attempts:    0,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	event, err := doc.toOutboxEvent()
	require.NoError(t, err)
	require.NotNil(t, event)
	assert.Equal(t, "test.event", event.EventType)
}

// -------------------------------------------------------------------
// docsToEvents — error propagation
// -------------------------------------------------------------------

func TestDocsToEvents_InvalidDoc(t *testing.T) {
	t.Parallel()

	docs := []document{
		{ID: "not-a-uuid", AggregateID: "00000000-0000-0000-0000-000000000001"},
	}
	_, err := docsToEvents(docs)
	require.Error(t, err)
}

func TestDocsToEvents_EmptySlice(t *testing.T) {
	t.Parallel()

	result, err := docsToEvents(nil)
	require.NoError(t, err)
	assert.Empty(t, result)
}

// -------------------------------------------------------------------
// documentFromCreateValues
// -------------------------------------------------------------------

func TestDocumentFromCreateValues_Basic(t *testing.T) {
	t.Parallel()

	now := time.Now()
	id := uuid.New()
	aggID := uuid.New()

	cv := createValues{
		id:          id,
		eventType:   "test.event",
		aggregateID: aggID,
		payload:     []byte("{}"),
		status:      "pending",
		attempts:    0,
		createdAt:   now,
		updatedAt:   now,
	}

	doc := documentFromCreateValues(cv, "tenant-test")
	assert.Equal(t, id.String(), doc.ID)
	assert.Equal(t, "test.event", doc.EventType)
	assert.Equal(t, "tenant-test", doc.TenantID)
}

// -------------------------------------------------------------------
// claimDocumentFromBSON — valid path
// -------------------------------------------------------------------

func TestClaimDocumentFromBSON_Valid(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	raw := bson.M{
		"id":         "some-claim-id",
		"status":     "PENDING",
		"attempts":   int32(0),
		"updated_at": now,
	}

	doc, err := claimDocumentFromBSON(raw, "")
	require.NoError(t, err)
	assert.Equal(t, "some-claim-id", doc.ID)
	assert.Equal(t, "PENDING", doc.Status)
}

func TestClaimDocumentFromBSON_MissingID(t *testing.T) {
	t.Parallel()

	raw := bson.M{
		"status":     "pending",
		"attempts":   int32(0),
		"updated_at": time.Now(),
	}

	_, err := claimDocumentFromBSON(raw, "")
	require.Error(t, err)
}
