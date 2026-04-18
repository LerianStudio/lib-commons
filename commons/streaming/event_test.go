//go:build unit

package streaming

import (
	"testing"
	"time"
)

// TestEvent_Topic exercises the semver-major suffix rule from TRD §C1:
// base form always, optional ".v<major>" when the parsed major version is ≥ 2.
func TestEvent_Topic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		resourceType  string
		eventType     string
		schemaVersion string
		want          string
	}{
		{
			name:          "semver 1.0.0 yields base form",
			resourceType:  "transaction",
			eventType:     "created",
			schemaVersion: "1.0.0",
			want:          "lerian.streaming.transaction.created",
		},
		{
			name:          "semver 1.2.3 yields base form",
			resourceType:  "account",
			eventType:     "updated",
			schemaVersion: "1.2.3",
			want:          "lerian.streaming.account.updated",
		},
		{
			name:          "semver 2.0.0 yields .v2 suffix",
			resourceType:  "transaction",
			eventType:     "created",
			schemaVersion: "2.0.0",
			want:          "lerian.streaming.transaction.created.v2",
		},
		{
			name:          "semver 2.3.1 yields .v2 suffix",
			resourceType:  "account",
			eventType:     "created",
			schemaVersion: "2.3.1",
			want:          "lerian.streaming.account.created.v2",
		},
		{
			name:          "semver 10.0.0 yields .v10 suffix",
			resourceType:  "ledger",
			eventType:     "closed",
			schemaVersion: "10.0.0",
			want:          "lerian.streaming.ledger.closed.v10",
		},
		{
			name:          "empty schema version yields base form",
			resourceType:  "transaction",
			eventType:     "created",
			schemaVersion: "",
			want:          "lerian.streaming.transaction.created",
		},
		{
			name:          "invalid semver falls through to base form",
			resourceType:  "transaction",
			eventType:     "created",
			schemaVersion: "not-a-version",
			want:          "lerian.streaming.transaction.created",
		},
		{
			name:          "semver with v prefix accepted",
			resourceType:  "transaction",
			eventType:     "created",
			schemaVersion: "v2.0.0",
			want:          "lerian.streaming.transaction.created.v2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := Event{
				ResourceType:  tt.resourceType,
				EventType:     tt.eventType,
				SchemaVersion: tt.schemaVersion,
			}
			if got := e.Topic(); got != tt.want {
				t.Errorf("Event.Topic() = %q; want %q", got, tt.want)
			}
		})
	}
}

// TestEvent_PartitionKey covers the TRD §C1 rules: TenantID by default,
// "system:" + EventType when SystemEvent is true (DX-A05/A06 adjacent).
func TestEvent_PartitionKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		event Event
		want  string
	}{
		{
			name:  "tenant id is the default partition key",
			event: Event{TenantID: "t-abc", EventType: "created"},
			want:  "t-abc",
		},
		{
			name:  "system event uses system prefix + event type",
			event: Event{TenantID: "", EventType: "reaper_pass", SystemEvent: true},
			want:  "system:reaper_pass",
		},
		{
			name: "system event with tenant still uses system prefix",
			event: Event{
				TenantID:    "ignored",
				EventType:   "announce",
				SystemEvent: true,
			},
			want: "system:announce",
		},
		{
			name:  "empty tenant and non-system returns empty string",
			event: Event{EventType: "created"},
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.event.PartitionKey(); got != tt.want {
				t.Errorf("Event.PartitionKey() = %q; want %q", got, tt.want)
			}
		})
	}
}

// TestEvent_ApplyDefaults ensures auto-population of optional fields when the
// caller leaves them zero: EventID (uuid), Timestamp, SchemaVersion, DataContentType.
// This is the behavioral contract required for DX-A01 (3-line emit).
func TestEvent_ApplyDefaults(t *testing.T) {
	t.Parallel()

	before := time.Now().UTC()
	e := Event{
		ResourceType: "transaction",
		EventType:    "created",
		Source:       "//lerian.midaz/tx-service",
	}

	e.ApplyDefaults()

	after := time.Now().UTC()

	if e.EventID == "" {
		t.Error("ApplyDefaults: EventID should be auto-populated")
	}
	if len(e.EventID) != 36 {
		t.Errorf("ApplyDefaults: EventID should be a UUID (36 chars), got len=%d value=%q", len(e.EventID), e.EventID)
	}
	if e.Timestamp.IsZero() {
		t.Error("ApplyDefaults: Timestamp should be auto-populated")
	}
	if e.Timestamp.Before(before) || e.Timestamp.After(after) {
		t.Errorf("ApplyDefaults: Timestamp %v outside expected window [%v, %v]", e.Timestamp, before, after)
	}
	if e.SchemaVersion != "1.0.0" {
		t.Errorf("ApplyDefaults: SchemaVersion = %q; want %q", e.SchemaVersion, "1.0.0")
	}
	if e.DataContentType != "application/json" {
		t.Errorf("ApplyDefaults: DataContentType = %q; want %q", e.DataContentType, "application/json")
	}
}

// TestEvent_ApplyDefaults_PreservesNonZero verifies that explicit values are
// NOT overwritten. Defaults only fill zero values.
func TestEvent_ApplyDefaults_PreservesNonZero(t *testing.T) {
	t.Parallel()

	explicitID := "my-explicit-id"
	explicitTime := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	explicitSchema := "3.1.4"
	explicitCT := "application/avro"

	e := Event{
		EventID:         explicitID,
		Timestamp:       explicitTime,
		SchemaVersion:   explicitSchema,
		DataContentType: explicitCT,
	}

	e.ApplyDefaults()

	if e.EventID != explicitID {
		t.Errorf("ApplyDefaults overwrote explicit EventID: got %q, want %q", e.EventID, explicitID)
	}
	if !e.Timestamp.Equal(explicitTime) {
		t.Errorf("ApplyDefaults overwrote explicit Timestamp: got %v, want %v", e.Timestamp, explicitTime)
	}
	if e.SchemaVersion != explicitSchema {
		t.Errorf("ApplyDefaults overwrote explicit SchemaVersion: got %q, want %q", e.SchemaVersion, explicitSchema)
	}
	if e.DataContentType != explicitCT {
		t.Errorf("ApplyDefaults overwrote explicit DataContentType: got %q, want %q", e.DataContentType, explicitCT)
	}
}
