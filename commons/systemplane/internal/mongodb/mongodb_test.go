//go:build unit

package mongodb

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/LerianStudio/lib-commons/v5/commons/systemplane/internal/store"
)

// TestExtractEvent_DeleteUsesDocumentKey verifies the critical tenant-attribution
// path: on a delete change-stream event, the extractor MUST read the tuple
// from documentKey._id (compound ID), not fullDocument (which is nil on
// deletes). This is the invariant that makes DeleteForTenant's changefeed
// echo route to the correct tenant subscribers without requiring MongoDB
// pre/post-images. See TRD §3.2 and research.md §MongoDB.
func TestExtractEvent_DeleteUsesDocumentKey(t *testing.T) {
	t.Parallel()

	ev := changeEvent{
		OperationType: operationTypeDelete,
		FullDocument:  nil, // deletes never carry a fullDocument
		DocumentKey: struct {
			ID compoundID `bson:"_id"`
		}{
			ID: compoundID{
				Namespace: "global",
				Key:       "fees.fail_closed_default",
				TenantID:  "tenant-A",
			},
		},
	}

	got, ok := extractEvent(ev)
	require.True(t, ok, "delete event with complete documentKey must extract cleanly")

	assert.Equal(t, "global", got.Namespace)
	assert.Equal(t, "fees.fail_closed_default", got.Key)
	assert.Equal(t, "tenant-A", got.TenantID)
}

// TestExtractEvent_DeleteRejectsIncompleteDocumentKey pins the negative case:
// a delete event whose documentKey does not carry all three tuple fields
// MUST be rejected (logged and dropped) rather than silently emitted with
// empty fields — the Client would otherwise dispatch a global-looking event.
func TestExtractEvent_DeleteRejectsIncompleteDocumentKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		id   compoundID
	}{
		{name: "missing namespace", id: compoundID{Key: "k", TenantID: "t"}},
		{name: "missing key", id: compoundID{Namespace: "ns", TenantID: "t"}},
		{name: "missing tenant_id", id: compoundID{Namespace: "ns", Key: "k"}},
		{name: "all empty", id: compoundID{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ev := changeEvent{
				OperationType: operationTypeDelete,
				DocumentKey: struct {
					ID compoundID `bson:"_id"`
				}{ID: tc.id},
			}

			_, ok := extractEvent(ev)
			assert.False(t, ok, "incomplete delete event must be dropped")
		})
	}
}

// TestExtractEvent_InsertUpdateReadsFullDocument verifies the non-delete path
// (insert / update / replace) reads tenant_id from fullDocument — which the
// server populates courtesy of SetFullDocument(UpdateLookup).
func TestExtractEvent_InsertUpdateReadsFullDocument(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		opType   string
		wantTID  string
		payload  changeEventFullDoc
		expectOK bool
	}{
		{
			name:     "insert global row",
			opType:   "insert",
			wantTID:  sentinelGlobal,
			payload:  changeEventFullDoc{Namespace: "global", Key: "log.level", TenantID: sentinelGlobal},
			expectOK: true,
		},
		{
			name:     "update tenant row",
			opType:   "update",
			wantTID:  "tenant-42",
			payload:  changeEventFullDoc{Namespace: "global", Key: "log.level", TenantID: "tenant-42"},
			expectOK: true,
		},
		{
			name:     "replace legacy row missing tenant_id falls back to sentinel",
			opType:   "replace",
			wantTID:  sentinelGlobal,
			payload:  changeEventFullDoc{Namespace: "global", Key: "log.level"}, // TenantID == ""
			expectOK: true,
		},
		{
			name:     "missing namespace is rejected",
			opType:   "insert",
			payload:  changeEventFullDoc{Key: "log.level", TenantID: "t"},
			expectOK: false,
		},
		{
			name:     "missing key is rejected",
			opType:   "insert",
			payload:  changeEventFullDoc{Namespace: "global", TenantID: "t"},
			expectOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			payload := tc.payload
			ev := changeEvent{
				OperationType: tc.opType,
				FullDocument:  &payload,
			}

			got, ok := extractEvent(ev)

			assert.Equal(t, tc.expectOK, ok)

			if tc.expectOK {
				assert.Equal(t, tc.payload.Namespace, got.Namespace)
				assert.Equal(t, tc.payload.Key, got.Key)
				assert.Equal(t, tc.wantTID, got.TenantID)
			}
		})
	}
}

// TestExtractEvent_InsertWithoutFullDocument verifies that a non-delete event
// that somehow arrived without a fullDocument (e.g., a change stream option
// regression) is rejected rather than emitted with empty fields.
func TestExtractEvent_InsertWithoutFullDocument(t *testing.T) {
	t.Parallel()

	ev := changeEvent{
		OperationType: "insert",
		FullDocument:  nil,
	}

	_, ok := extractEvent(ev)
	assert.False(t, ok)
}

// TestCompoundID_BSONRoundTrip round-trips a compoundID through BSON
// marshal/unmarshal. Guards against a future struct-tag drift that would
// silently break change-stream delete attribution.
func TestCompoundID_BSONRoundTrip(t *testing.T) {
	t.Parallel()

	orig := compoundID{
		Namespace: "global",
		Key:       "fees.fail_closed_default",
		TenantID:  "tenant-A",
	}

	data, err := bson.Marshal(orig)
	require.NoError(t, err)

	var got compoundID
	require.NoError(t, bson.Unmarshal(data, &got))

	assert.Equal(t, orig, got)
}

// TestCompoundID_BSONDecodeFromWire mimics what the MongoDB server sends back
// on change-stream documentKey — a raw BSON document with the three expected
// field names and string values. This asserts the bson:"..." tags on
// compoundID match the server wire shape.
func TestCompoundID_BSONDecodeFromWire(t *testing.T) {
	t.Parallel()

	wire := bson.D{
		{Key: "namespace", Value: "global"},
		{Key: "key", Value: "feature.enabled"},
		{Key: "tenant_id", Value: "acme-corp"},
	}

	data, err := bson.Marshal(wire)
	require.NoError(t, err)

	var got compoundID
	require.NoError(t, bson.Unmarshal(data, &got))

	assert.Equal(t, "global", got.Namespace)
	assert.Equal(t, "feature.enabled", got.Key)
	assert.Equal(t, "acme-corp", got.TenantID)
}

// TestEntryDoc_ToEntryPopulatesTenantID pins the contract that entryDoc.toEntry
// surfaces TenantID onto the store.Entry so Client-layer consumers receive
// tenant attribution on every read.
func TestEntryDoc_ToEntryPopulatesTenantID(t *testing.T) {
	t.Parallel()

	doc := entryDoc{
		Namespace: "global",
		Key:       "log.level",
		TenantID:  "tenant-7",
		Value:     `"debug"`,
		UpdatedBy: "actor",
	}

	got := doc.toEntry()

	assert.Equal(t, "global", got.Namespace)
	assert.Equal(t, "log.level", got.Key)
	assert.Equal(t, "tenant-7", got.TenantID)
	assert.Equal(t, `"debug"`, string(got.Value))
	assert.Equal(t, "actor", got.UpdatedBy)
}

// TestIsIndexNotFoundErr_CommandError pins the canonical detection path:
// mongo.CommandError with Code == 27 is an IndexNotFound. Guards against a
// driver-version drift that would cause DropOne to fail the constructor on
// fresh collections.
func TestIsIndexNotFoundErr_CommandError(t *testing.T) {
	t.Parallel()

	cmdErr := mongo.CommandError{
		Code:    indexNotFoundCode,
		Name:    "IndexNotFound",
		Message: "index not found with name [namespace_1_key_1]",
	}

	assert.True(t, isIndexNotFoundErr(cmdErr))
}

// TestIsIndexNotFoundErr_CommandErrorWrongCode verifies the detector does not
// match unrelated CommandErrors (a duplicate-key error, for example, is
// code 11000 and MUST NOT be swallowed by DropOne's fallthrough).
func TestIsIndexNotFoundErr_CommandErrorWrongCode(t *testing.T) {
	t.Parallel()

	cmdErr := mongo.CommandError{
		Code:    11000,
		Name:    "DuplicateKey",
		Message: "E11000 duplicate key error",
	}

	assert.False(t, isIndexNotFoundErr(cmdErr))
}

// TestIsIndexNotFoundErr_MessageFallback covers the defensive string-match
// path for drivers or error shapes that do not surface as CommandError but
// still carry the canonical server message.
func TestIsIndexNotFoundErr_MessageFallback(t *testing.T) {
	t.Parallel()

	err := errors.New("operation failed: index not found with name [foo]")
	assert.True(t, isIndexNotFoundErr(err))
}

// TestIsIndexNotFoundErr_NilAndUnrelated covers the boundary conditions.
func TestIsIndexNotFoundErr_NilAndUnrelated(t *testing.T) {
	t.Parallel()

	assert.False(t, isIndexNotFoundErr(nil))
	assert.False(t, isIndexNotFoundErr(errors.New("connection refused")))
	assert.False(t, isIndexNotFoundErr(errors.New("")))
}

// TestLegacyNamespaceKeyIndex_NameMatchesDriverConvention is a regression pin
// against the driver's index-naming rule. mongo-driver/v2 composes a compound
// index name as "<field1>_<dir1>_<field2>_<dir2>"; the pre-Task-4 index on
// {namespace: 1, key: 1} was therefore named "namespace_1_key_1". If the
// driver ever changes the convention this constant must be revisited.
func TestLegacyNamespaceKeyIndex_NameMatchesDriverConvention(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "namespace_1_key_1", legacyNamespaceKeyIndex,
		"legacy index name must match what driver auto-generated for "+
			"bson.D{{'namespace',1},{'key',1}} on the old schema")
}

// TestChangeEvent_BSONDecodesDeleteEventShape simulates the exact wire
// shape of a MongoDB change-stream delete event (operationType="delete",
// documentKey containing the compound _id, no fullDocument) and runs it
// through the BSON codec the driver uses via stream.Decode. This is the
// end-to-end validation that the changeEvent struct's tags line up with
// the server response.
func TestChangeEvent_BSONDecodesDeleteEventShape(t *testing.T) {
	t.Parallel()

	wire := bson.D{
		{Key: "operationType", Value: operationTypeDelete},
		{Key: "documentKey", Value: bson.D{
			{Key: "_id", Value: bson.D{
				{Key: "namespace", Value: "global"},
				{Key: "key", Value: "feature.enabled"},
				{Key: "tenant_id", Value: "acme"},
			}},
		}},
	}

	data, err := bson.Marshal(wire)
	require.NoError(t, err)

	var ev changeEvent
	require.NoError(t, bson.Unmarshal(data, &ev))

	assert.Equal(t, operationTypeDelete, ev.OperationType)
	assert.Nil(t, ev.FullDocument, "delete events must not carry fullDocument")
	assert.Equal(t, "global", ev.DocumentKey.ID.Namespace)
	assert.Equal(t, "feature.enabled", ev.DocumentKey.ID.Key)
	assert.Equal(t, "acme", ev.DocumentKey.ID.TenantID)

	// Round-trip through the extractor too.
	got, ok := extractEvent(ev)
	require.True(t, ok)
	assert.Equal(t, "global", got.Namespace)
	assert.Equal(t, "feature.enabled", got.Key)
	assert.Equal(t, "acme", got.TenantID)
}

// TestSetTenantValue_Phase1ReturnsErrTenantSchemaNotEnabled pins the H6
// rolling-deploy guard for the MongoDB backend: when the Store is running
// in phase-1 compat mode (the default, TenantSchemaEnabled=false),
// SetTenantValue MUST return store.ErrTenantSchemaNotEnabled BEFORE touching
// the collection. The check runs at the very top of the method, so a nil
// *mongo.Collection is sufficient — the guard short-circuits the driver
// call entirely.
func TestSetTenantValue_Phase1ReturnsErrTenantSchemaNotEnabled(t *testing.T) {
	t.Parallel()

	s := &Store{
		cfg:    Config{TenantSchemaEnabled: false, Database: "test"},
		tracer: noop.NewTracerProvider().Tracer(tracerName),
	}

	err := s.SetTenantValue(context.Background(), "tenant-A", store.Entry{
		Namespace: "global", Key: "fee.rate", Value: []byte(`0.01`),
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrTenantSchemaNotEnabled,
		"phase-1 SetTenantValue must reject with ErrTenantSchemaNotEnabled")
}

// TestDeleteTenantValue_Phase1ReturnsErrTenantSchemaNotEnabled mirrors the
// SetTenantValue guard for the MongoDB delete path.
func TestDeleteTenantValue_Phase1ReturnsErrTenantSchemaNotEnabled(t *testing.T) {
	t.Parallel()

	s := &Store{
		cfg:    Config{TenantSchemaEnabled: false, Database: "test"},
		tracer: noop.NewTracerProvider().Tracer(tracerName),
	}

	err := s.DeleteTenantValue(context.Background(), "tenant-A", "global", "fee.rate", "admin")
	require.Error(t, err)
	assert.ErrorIs(t, err, store.ErrTenantSchemaNotEnabled,
		"phase-1 DeleteTenantValue must reject with ErrTenantSchemaNotEnabled")
}

// TestChangeEvent_BSONDecodesInsertEventShape simulates the wire shape of
// an insert change-stream event — includes operationType, documentKey, and
// fullDocument — and confirms the extractor reads tenant_id from
// fullDocument as intended.
func TestChangeEvent_BSONDecodesInsertEventShape(t *testing.T) {
	t.Parallel()

	wire := bson.D{
		{Key: "operationType", Value: "insert"},
		{Key: "documentKey", Value: bson.D{
			{Key: "_id", Value: bson.D{
				{Key: "namespace", Value: "global"},
				{Key: "key", Value: "log.level"},
				{Key: "tenant_id", Value: sentinelGlobal},
			}},
		}},
		{Key: "fullDocument", Value: bson.D{
			{Key: "_id", Value: bson.D{
				{Key: "namespace", Value: "global"},
				{Key: "key", Value: "log.level"},
				{Key: "tenant_id", Value: sentinelGlobal},
			}},
			{Key: "namespace", Value: "global"},
			{Key: "key", Value: "log.level"},
			{Key: "tenant_id", Value: sentinelGlobal},
			{Key: "value", Value: `"info"`},
		}},
	}

	data, err := bson.Marshal(wire)
	require.NoError(t, err)

	var ev changeEvent
	require.NoError(t, bson.Unmarshal(data, &ev))

	require.NotNil(t, ev.FullDocument)
	assert.Equal(t, "global", ev.FullDocument.Namespace)
	assert.Equal(t, "log.level", ev.FullDocument.Key)
	assert.Equal(t, sentinelGlobal, ev.FullDocument.TenantID)

	got, ok := extractEvent(ev)
	require.True(t, ok)
	assert.Equal(t, sentinelGlobal, got.TenantID)
}
