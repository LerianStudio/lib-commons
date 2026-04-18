//go:build unit

package streaming

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

// TestBuildAndParseCloudEventsHeaders_RoundTrip exercises the symmetry of the
// buildCloudEventsHeaders + ParseCloudEventsHeaders pair across a matrix of
// Event shapes: minimum viable, fully-populated, system event, subject-less,
// dataschema-present, etc.
func TestBuildAndParseCloudEventsHeaders_RoundTrip(t *testing.T) {
	t.Parallel()

	// Use a fixed timestamp so round-tripping is deterministic; sub-second
	// precision is preserved by RFC3339Nano.
	ts := time.Date(2026, 4, 18, 10, 30, 45, 123456789, time.UTC)

	tests := []struct {
		name  string
		event Event
	}{
		{
			name: "minimum required fields",
			event: Event{
				TenantID:        "t-abc",
				ResourceType:    "transaction",
				EventType:       "created",
				EventID:         "evt-1",
				SchemaVersion:   "1.0.0",
				Timestamp:       ts,
				Source:          "//lerian.midaz/tx",
				DataContentType: "application/json",
				Payload:         json.RawMessage(`{"v":1}`),
			},
		},
		{
			name: "fully populated incl. subject and dataschema",
			event: Event{
				TenantID:        "t-xyz",
				ResourceType:    "account",
				EventType:       "updated",
				EventID:         "evt-full",
				SchemaVersion:   "2.3.1",
				Timestamp:       ts,
				Source:          "//lerian.midaz/acct",
				Subject:         "acct-789",
				DataContentType: "application/json",
				DataSchema:      "https://schemas.lerian/account/updated/v2.json",
				Payload:         json.RawMessage(`{"id":"acct-789"}`),
			},
		},
		{
			name: "system event with empty tenant",
			event: Event{
				TenantID:        "",
				ResourceType:    "platform",
				EventType:       "reaper_pass",
				EventID:         "evt-sys",
				SchemaVersion:   "1.0.0",
				Timestamp:       ts,
				Source:          "//lerian.platform/reaper",
				DataContentType: "application/json",
				SystemEvent:     true,
				Payload:         json.RawMessage(`{"pass":true}`),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			headers := buildCloudEventsHeaders(tt.event)

			// Sanity: at minimum the 8 required headers are present.
			if len(headers) < 8 {
				t.Fatalf("expected >= 8 headers; got %d", len(headers))
			}

			parsed, err := ParseCloudEventsHeaders(headers)
			if err != nil {
				t.Fatalf("ParseCloudEventsHeaders err = %v", err)
			}

			if parsed.EventID != tt.event.EventID {
				t.Errorf("EventID round-trip: got %q, want %q", parsed.EventID, tt.event.EventID)
			}
			if parsed.Source != tt.event.Source {
				t.Errorf("Source round-trip: got %q, want %q", parsed.Source, tt.event.Source)
			}
			if parsed.ResourceType != tt.event.ResourceType {
				t.Errorf("ResourceType round-trip: got %q, want %q", parsed.ResourceType, tt.event.ResourceType)
			}
			if parsed.EventType != tt.event.EventType {
				t.Errorf("EventType round-trip: got %q, want %q", parsed.EventType, tt.event.EventType)
			}
			if parsed.SchemaVersion != tt.event.SchemaVersion {
				t.Errorf("SchemaVersion round-trip: got %q, want %q", parsed.SchemaVersion, tt.event.SchemaVersion)
			}
			if !parsed.Timestamp.Equal(tt.event.Timestamp) {
				t.Errorf("Timestamp round-trip: got %v, want %v", parsed.Timestamp, tt.event.Timestamp)
			}
			if parsed.TenantID != tt.event.TenantID {
				t.Errorf("TenantID round-trip: got %q, want %q", parsed.TenantID, tt.event.TenantID)
			}
			if parsed.Subject != tt.event.Subject {
				t.Errorf("Subject round-trip: got %q, want %q", parsed.Subject, tt.event.Subject)
			}
			if parsed.DataContentType != tt.event.DataContentType {
				t.Errorf("DataContentType round-trip: got %q, want %q", parsed.DataContentType, tt.event.DataContentType)
			}
			if parsed.DataSchema != tt.event.DataSchema {
				t.Errorf("DataSchema round-trip: got %q, want %q", parsed.DataSchema, tt.event.DataSchema)
			}
			if parsed.SystemEvent != tt.event.SystemEvent {
				t.Errorf("SystemEvent round-trip: got %v, want %v", parsed.SystemEvent, tt.event.SystemEvent)
			}
		})
	}
}

// TestBuildCloudEventsHeaders_SystemEventOmitsTenant asserts the header list
// does NOT include ce-tenantid when SystemEvent=true and TenantID="". This
// is the wire-format contract — system events MUST NOT carry an empty-string
// tenant header.
func TestBuildCloudEventsHeaders_SystemEventOmitsTenant(t *testing.T) {
	t.Parallel()

	event := Event{
		TenantID:        "",
		ResourceType:    "platform",
		EventType:       "reaper",
		EventID:         "evt-1",
		SchemaVersion:   "1.0.0",
		Timestamp:       time.Now().UTC(),
		Source:          "//lerian.platform",
		DataContentType: "application/json",
		SystemEvent:     true,
	}

	headers := buildCloudEventsHeaders(event)

	for _, h := range headers {
		if h.Key == headerCETenantID {
			t.Errorf("ce-tenantid header was emitted on a system event with empty tenant (value=%q)", string(h.Value))
		}
	}

	// And ce-systemevent=true MUST be present.
	found := false

	for _, h := range headers {
		if h.Key == headerCESystemEvent && string(h.Value) == "true" {
			found = true
			break
		}
	}

	if !found {
		t.Errorf("ce-systemevent=true header missing on a SystemEvent=true Event")
	}
}

// TestBuildCloudEventsHeaders_NonSystemOmitsSystemEventFlag: for a regular
// tenant event, ce-systemevent MUST NOT appear. Consumers distinguish the
// two flows by header presence, not by string value.
func TestBuildCloudEventsHeaders_NonSystemOmitsSystemEventFlag(t *testing.T) {
	t.Parallel()

	event := Event{
		TenantID:        "t-abc",
		ResourceType:    "transaction",
		EventType:       "created",
		EventID:         "evt-1",
		SchemaVersion:   "1.0.0",
		Timestamp:       time.Now().UTC(),
		Source:          "//lerian.midaz",
		DataContentType: "application/json",
		SystemEvent:     false,
	}

	headers := buildCloudEventsHeaders(event)

	for _, h := range headers {
		if h.Key == headerCESystemEvent {
			t.Errorf("ce-systemevent header emitted for non-system event (value=%q)", string(h.Value))
		}
	}
}

// TestBuildCloudEventsHeaders_TypeComposition asserts ce-type follows the
// "studio.lerian.<resource>.<event>" reverse-DNS shape verbatim.
func TestBuildCloudEventsHeaders_TypeComposition(t *testing.T) {
	t.Parallel()

	event := Event{
		TenantID:        "t-abc",
		ResourceType:    "transaction",
		EventType:       "created",
		EventID:         "evt-1",
		SchemaVersion:   "1.0.0",
		Timestamp:       time.Now().UTC(),
		Source:          "//lerian.midaz",
		DataContentType: "application/json",
	}

	headers := buildCloudEventsHeaders(event)

	got := findHeader(headers, headerCEType)
	want := "studio.lerian.transaction.created"

	if got != want {
		t.Errorf("ce-type = %q; want %q", got, want)
	}
}

// TestBuildCloudEventsHeaders_SpecVersionLiteral asserts ce-specversion is
// emitted with the literal "1.0".
func TestBuildCloudEventsHeaders_SpecVersionLiteral(t *testing.T) {
	t.Parallel()

	event := Event{
		ResourceType:    "t",
		EventType:       "e",
		TenantID:        "t",
		EventID:         "id",
		Source:          "s",
		SchemaVersion:   "1.0.0",
		Timestamp:       time.Now().UTC(),
		DataContentType: "application/json",
	}

	got := findHeader(buildCloudEventsHeaders(event), headerCESpecVersion)
	if got != "1.0" {
		t.Errorf("ce-specversion = %q; want %q", got, "1.0")
	}
}

// TestBuildCloudEventsHeaders_SkipsEmptyOptionals proves Subject, DataSchema,
// DataContentType are omitted when empty. This keeps the on-wire footprint
// lean for the minimum-viable event.
func TestBuildCloudEventsHeaders_SkipsEmptyOptionals(t *testing.T) {
	t.Parallel()

	event := Event{
		TenantID:      "t-abc",
		ResourceType:  "transaction",
		EventType:     "created",
		EventID:       "evt-1",
		SchemaVersion: "1.0.0",
		Timestamp:     time.Now().UTC(),
		Source:        "//lerian.midaz",
		// Subject, DataContentType, DataSchema deliberately empty.
	}

	headers := buildCloudEventsHeaders(event)

	for _, h := range headers {
		switch h.Key {
		case headerCESubject, headerCEDataContentType, headerCEDataSchema:
			t.Errorf("optional header %q emitted with empty value; want omitted", h.Key)
		}
	}
}

// TestParseCloudEventsHeaders_MissingRequiredHeader rejects malformed input
// so tests can assert the classifier's contract (required = required).
func TestParseCloudEventsHeaders_MissingRequiredHeader(t *testing.T) {
	t.Parallel()

	// Deliberately missing ce-id.
	headers := []kgo.RecordHeader{
		{Key: headerCESpecVersion, Value: []byte("1.0")},
		{Key: headerCESource, Value: []byte("//source")},
		{Key: headerCEType, Value: []byte("studio.lerian.t.e")},
		{Key: headerCETime, Value: []byte(time.Now().UTC().Format(time.RFC3339Nano))},
	}

	_, err := ParseCloudEventsHeaders(headers)
	if !errors.Is(err, ErrMissingRequiredHeader) {
		t.Fatalf("expected ErrMissingRequiredHeader; got %v", err)
	}

	if !strings.Contains(err.Error(), headerCEID) {
		t.Errorf("error message should mention %q; got %q", headerCEID, err.Error())
	}
}

// TestParseCloudEventsHeaders_InvalidTime surfaces a parse error for
// ce-time payload that is not RFC3339Nano.
func TestParseCloudEventsHeaders_InvalidTime(t *testing.T) {
	t.Parallel()

	headers := []kgo.RecordHeader{
		{Key: headerCESpecVersion, Value: []byte("1.0")},
		{Key: headerCEID, Value: []byte("evt-1")},
		{Key: headerCESource, Value: []byte("//s")},
		{Key: headerCEType, Value: []byte("studio.lerian.t.e")},
		{Key: headerCETime, Value: []byte("not-a-timestamp")},
	}

	_, err := ParseCloudEventsHeaders(headers)
	if err == nil {
		t.Fatal("expected parse error for invalid ce-time")
	}

	if !strings.Contains(err.Error(), "ce-time") {
		t.Errorf("error message should mention ce-time; got %q", err.Error())
	}
}

// findHeader is a tiny test helper that extracts a header value by key.
// Returns an empty string when the key is absent — callers use this for
// positive assertions only.
func findHeader(headers []kgo.RecordHeader, key string) string {
	for _, h := range headers {
		if h.Key == key {
			return string(h.Value)
		}
	}

	return ""
}
