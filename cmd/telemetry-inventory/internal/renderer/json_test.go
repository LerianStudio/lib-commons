//go:build unit

package renderer_test

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/renderer"
	"github.com/LerianStudio/lib-commons/v5/cmd/telemetry-inventory/internal/schema"
)

// TestJSON_RoundTrip serializes a sample dictionary and verifies the output
// unmarshals back to an equivalent struct. Pins the JSON contract so a
// renamed/dropped json tag breaks the test instead of silently dropping a
// field at runtime.
func TestJSON_RoundTrip(t *testing.T) {
	want := buildSampleDictionary()
	payload, err := renderer.JSON(want)
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}

	if len(payload) == 0 || payload[len(payload)-1] != '\n' {
		t.Fatalf("expected JSON output to end with a newline; got %q", string(payload))
	}

	var got schema.Dictionary
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("Unmarshal: %v\npayload=%s", err, string(payload))
	}

	if !reflect.DeepEqual(*want, got) {
		t.Fatalf("round-trip mismatch\n--- want ---\n%+v\n--- got ---\n%+v", *want, got)
	}
}

// TestJSON_NilDictionaryReturnsError pins the nil-guard contract: callers
// that accidentally pass nil get a clear error instead of a runtime panic.
func TestJSON_NilDictionaryReturnsError(t *testing.T) {
	if _, err := renderer.JSON(nil); err == nil {
		t.Fatalf("expected error for nil dictionary, got nil")
	}
}

// TestJSON_Deterministic asserts byte-equal output for identical inputs across
// successive calls. Unlike the markdown renderer, JSON does NOT sort its
// inputs — the orchestrator's merge layer is responsible for ordering — so
// this only pins same-input determinism, not invariance under shuffle. The
// LogField LevelDistribution map is the one place where Go map-iteration
// could leak nondeterminism: encoding/json sorts map keys, so this test
// also pins that contract.
func TestJSON_Deterministic(t *testing.T) {
	a, err := renderer.JSON(buildSampleDictionary())
	if err != nil {
		t.Fatalf("JSON (a): %v", err)
	}

	b, err := renderer.JSON(buildSampleDictionary())
	if err != nil {
		t.Fatalf("JSON (b): %v", err)
	}

	if !bytes.Equal(a, b) {
		t.Fatalf("JSON not deterministic for identical inputs\n--- a ---\n%s\n--- b ---\n%s", string(a), string(b))
	}
}
